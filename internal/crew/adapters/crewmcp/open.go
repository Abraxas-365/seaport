package crewmcp

import (
	"context"
	"encoding/base64"
	"strings"

	"github.com/Abraxas-365/seaport/internal/dock"
	"github.com/Abraxas-365/seaport/internal/errx"
	"github.com/Abraxas-365/seaport/internal/httpx"
	"github.com/Abraxas-365/seaport/internal/identity"
	"github.com/Abraxas-365/seaport/internal/query"
	"github.com/Abraxas-365/seaport/internal/ship"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// addOpen adds the destinations and open tools when the calling agent has
// a destination to open at: the places the owner named on the docks it
// works on (dock.Destination). Like skill, the tools show only to an
// agent that can use them, and reflect the owner's current list on each
// request.
func (t tools) addOpen(ctx context.Context, srv *mcp.Server) {
	if t.d.Docks == nil {
		return
	}
	p, ok := httpx.PrincipalFrom(ctx)
	if !ok {
		return
	}
	var agentID *identity.AgentID
	if !p.Agent.IsZero() {
		agentID = &p.Agent
	}
	dests, err := t.destinationsOf(ctx, p.Organization, agentID)
	if err != nil || len(dests) == 0 {
		return
	}
	var b strings.Builder
	b.WriteString("List the destinations: places (a Slack channel, a WhatsApp chat) where you may start a new conversation with the open tool. " +
		"Each has a name, the channel, and what the owner says it is for. Current destinations:\n")
	for _, d := range dests {
		b.WriteString("- " + d.Name + " (" + string(d.Channel) + ")")
		if d.Title != "" {
			b.WriteString(": " + d.Title)
		}
		b.WriteString("\n")
	}
	mcp.AddTool(srv, &mcp.Tool{Name: "destinations", Description: b.String()}, t.destinations)
	mcp.AddTool(srv, &mcp.Tool{
		Name: "open",
		Description: "Start a new conversation at a destination (see the destinations tool) with a first message, optionally with files. " +
			"Use it to bring a matter to people where they are: an alert to the on-call channel, a review to the team's chat. " +
			"With thread_of (a conversation_id), the new conversation joins that conversation's thread: what people answer there " +
			"reaches you in the same session, and history with thread: true reads both. Only allowed where the owner made a destination.",
	}, t.open)
}

// destinationsOf lists the destinations of the agent's docks (every dock
// without an agent: the local owner).
func (t tools) destinationsOf(ctx context.Context, org identity.OrganizationID, agentID *identity.AgentID) ([]Destination, error) {
	var ids []identity.DockID
	if agentID != nil {
		var err error
		if ids, err = t.docks(ctx, org, *agentID); err != nil {
			return nil, err
		}
	} else {
		page, err := t.d.Docks.List(ctx, org, query.Pagination{Limit: query.MaxLimit})
		if err != nil {
			return nil, err
		}
		for _, d := range page.Items {
			ids = append(ids, d.ID)
		}
	}
	out := []Destination{}
	for _, id := range ids {
		list, err := t.d.Docks.Destinations(ctx, org, id)
		if err != nil {
			return nil, err
		}
		for _, d := range list {
			out = append(out, Destination{Dock: d.Dock.String(), Name: d.Name, Channel: d.Channel, Title: d.Title})
		}
	}
	return out, nil
}

// Destination is a place an agent may open a conversation at.
type Destination struct {
	Dock    string       `json:"dock_id"`
	Name    string       `json:"name"`
	Channel dock.Channel `json:"channel"`
	Title   string       `json:"title,omitempty"`
}

type DestinationsOut struct {
	Destinations []Destination `json:"destinations"`
}

func (t tools) destinations(ctx context.Context, _ *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, DestinationsOut, error) {
	org, agentID, err := caller(ctx)
	if err != nil {
		return failure(err), DestinationsOut{}, nil
	}
	dests, err := t.destinationsOf(ctx, org, agentID)
	if err != nil {
		return failure(err), DestinationsOut{}, nil
	}
	return nil, DestinationsOut{Destinations: dests}, nil
}

type OpenIn struct {
	Destination string      `json:"destination" jsonschema:"the destination's name (destinations tool); dock_id too when two docks name one the same"`
	DockID      string      `json:"dock_id,omitempty" jsonschema:"the dock of the destination (optional when the name is unique among your docks)"`
	Text        string      `json:"text,omitempty" jsonschema:"the first message (may be empty when files go)"`
	Files       []ReplyFile `json:"files,omitempty" jsonschema:"files sent with the message (at most 10; 10 MiB each, 25 MiB in all)"`
	ThreadOf    string      `json:"thread_of,omitempty" jsonschema:"a conversation_id whose thread the new conversation joins (the matter it is about)"`
}

type OpenOut struct {
	ConversationID string `json:"conversation_id"`
	ShipID         string `json:"ship_id"`
	Status         string `json:"status"`
}

func (t tools) open(ctx context.Context, _ *mcp.CallToolRequest, in OpenIn) (*mcp.CallToolResult, OpenOut, error) {
	org, agentID, err := caller(ctx)
	if err != nil {
		return failure(err), OpenOut{}, nil
	}
	name := strings.TrimSpace(in.Destination)
	dests, err := t.destinationsOf(ctx, org, agentID)
	if err != nil {
		return failure(err), OpenOut{}, nil
	}
	var dockID identity.DockID
	if strings.TrimSpace(in.DockID) != "" {
		if dockID, err = identity.ParseDockID(strings.TrimSpace(in.DockID)); err != nil {
			return failure(err), OpenOut{}, nil
		}
	}
	var found []Destination
	for _, d := range dests {
		if d.Name == name && (dockID.IsZero() || d.Dock == dockID.String()) {
			found = append(found, d)
		}
	}
	switch len(found) {
	case 0:
		return failure(errx.NotFound("no destination named " + name + " on your docks")), OpenOut{}, nil
	case 1:
		if dockID, err = identity.ParseDockID(found[0].Dock); err != nil {
			return failure(err), OpenOut{}, nil
		}
	default:
		return failure(errx.Validation("several docks have a destination named " + name + ": give dock_id too")), OpenOut{}, nil
	}
	files := make([]ship.File, 0, len(in.Files))
	for _, f := range in.Files {
		data, err := base64.StdEncoding.DecodeString(strings.TrimSpace(f.Base64))
		if err != nil {
			return failure(errx.InvalidField("files.content_base64", "file "+f.Name+" is not valid base64")), OpenOut{}, nil
		}
		files = append(files, ship.File{Name: strings.TrimSpace(f.Name), Data: data})
	}
	input := ship.Open{Dock: dockID, Destination: name, Text: in.Text, Agent: agentID, Files: files}
	if strings.TrimSpace(in.ThreadOf) != "" {
		if input.ThreadOf, err = identity.ParseConversationID(strings.TrimSpace(in.ThreadOf)); err != nil {
			return failure(err), OpenOut{}, nil
		}
	}
	s, err := t.d.Ships.Open(ctx, org, input)
	if err != nil {
		return failure(err), OpenOut{}, nil
	}
	return nil, OpenOut{ConversationID: s.Conversation.String(), ShipID: s.ID.String(), Status: string(s.Status)}, nil
}
