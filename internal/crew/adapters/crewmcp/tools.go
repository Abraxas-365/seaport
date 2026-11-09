// Package crewmcp exposes the port's tools to agents over MCP (streamable
// HTTP at /mcp, crew token): reply, history, conversations.
package crewmcp

import (
	"context"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/Abraxas-365/seaport/internal/agent"
	"github.com/Abraxas-365/seaport/internal/conversation"
	"github.com/Abraxas-365/seaport/internal/errx"
	"github.com/Abraxas-365/seaport/internal/httpx"
	"github.com/Abraxas-365/seaport/internal/identity"
	"github.com/Abraxas-365/seaport/internal/query"
	"github.com/Abraxas-365/seaport/internal/ship"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type Deps struct {
	Ships         ship.Commands
	History       ship.Queries
	Conversations conversation.Queries
	// Agents scopes a token's tools to its agent's docks.
	Agents  agent.Queries
	Version string
}

// Handler returns the MCP endpoint.
func Handler(d Deps) http.Handler {
	srv := mcp.NewServer(&mcp.Implementation{Name: "seaport", Title: "Seaport", Version: d.Version}, &mcp.ServerOptions{
		Instructions: "Seaport is the port where your messages (WhatsApp and other channels) arrive. " +
			"Each prompt from seaport names a conversation_id. Answer people with the reply tool; " +
			"use history to read earlier messages of a conversation.",
	})
	t := tools{d: d}
	mcp.AddTool(srv, &mcp.Tool{
		Name:        "reply",
		Description: "Send a text message to a seaport conversation (WhatsApp chat...). Only allowed conversations accept replies.",
	}, t.reply)
	mcp.AddTool(srv, &mcp.Tool{
		Name:        "history",
		Description: "Read the latest messages of a conversation, newest first (inbound and outbound).",
	}, t.history)
	mcp.AddTool(srv, &mcp.Tool{
		Name:        "conversations",
		Description: "List the allowed conversations the agent can reply to (those of the docks it works on).",
	}, t.conversations)
	return mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return srv },
		&mcp.StreamableHTTPOptions{Stateless: true, JSONResponse: true})
}

type tools struct{ d Deps }

// caller is the crew principal that made the HTTP request (the stateless
// handler runs each call under the request's context): its organization,
// and its agent with a crew token (nil in open mode: the local owner sees
// every dock). Tools fail closed without one.
func caller(ctx context.Context) (identity.OrganizationID, *identity.AgentID, error) {
	p, ok := httpx.PrincipalFrom(ctx)
	if !ok {
		return identity.OrganizationID{}, nil, errx.Unauthorized("missing crew principal")
	}
	if p.Agent.IsZero() {
		return p.Organization, nil, nil
	}
	return p.Organization, &p.Agent, nil
}

// docks are the docks an agent works on.
func (t tools) docks(ctx context.Context, org identity.OrganizationID, id identity.AgentID) ([]identity.DockID, error) {
	a, err := t.d.Agents.Find(ctx, org, id)
	if err != nil {
		return nil, err
	}
	out := make([]identity.DockID, 0, len(a.Docks))
	for _, d := range a.Docks {
		out = append(out, d.Dock)
	}
	return out, nil
}

// visible fails NotFound for a conversation of a dock the agent doesn't
// work on.
func (t tools) visible(ctx context.Context, org identity.OrganizationID, id *identity.AgentID, conv identity.ConversationID) error {
	if id == nil {
		return nil
	}
	c, err := t.d.Conversations.Find(ctx, org, conv)
	if err != nil {
		return err
	}
	docks, err := t.docks(ctx, org, *id)
	if err != nil {
		return err
	}
	if !slices.Contains(docks, c.Dock) {
		return errx.NotFound("conversation not found")
	}
	return nil
}

type ReplyIn struct {
	ConversationID string `json:"conversation_id" jsonschema:"the conversation_id given in the seaport prompt"`
	Text           string `json:"text" jsonschema:"the message to send"`
}

type ReplyOut struct {
	ShipID string `json:"ship_id"`
	Status string `json:"status"`
}

func (t tools) reply(ctx context.Context, _ *mcp.CallToolRequest, in ReplyIn) (*mcp.CallToolResult, ReplyOut, error) {
	org, agentID, err := caller(ctx)
	if err != nil {
		return failure(err), ReplyOut{}, nil
	}
	id, err := identity.ParseConversationID(strings.TrimSpace(in.ConversationID))
	if err != nil {
		return failure(err), ReplyOut{}, nil
	}
	// Send refuses a conversation of a dock the agent doesn't work on.
	s, err := t.d.Ships.Send(ctx, org, ship.Outbound{Conversation: id, Text: in.Text, Agent: agentID})
	if err != nil {
		return failure(err), ReplyOut{}, nil
	}
	return nil, ReplyOut{ShipID: s.ID.String(), Status: string(s.Status)}, nil
}

type HistoryIn struct {
	ConversationID string `json:"conversation_id"`
	Limit          int    `json:"limit,omitempty" jsonschema:"how many messages (1-100, default 20)"`
}

type Message struct {
	Direction string    `json:"direction"`
	From      string    `json:"from"`
	Text      string    `json:"text"`
	SentAt    time.Time `json:"sent_at"`
	Status    string    `json:"status"`
}

type HistoryOut struct {
	Messages []Message `json:"messages"`
	Total    int       `json:"total"`
}

func (t tools) history(ctx context.Context, _ *mcp.CallToolRequest, in HistoryIn) (*mcp.CallToolResult, HistoryOut, error) {
	org, agentID, err := caller(ctx)
	if err != nil {
		return failure(err), HistoryOut{}, nil
	}
	id, err := identity.ParseConversationID(strings.TrimSpace(in.ConversationID))
	if err != nil {
		return failure(err), HistoryOut{}, nil
	}
	if err := t.visible(ctx, org, agentID, id); err != nil {
		return failure(err), HistoryOut{}, nil
	}
	page, err := t.d.History.History(ctx, org, id, query.Pagination{Limit: in.Limit})
	if err != nil {
		return failure(err), HistoryOut{}, nil
	}
	out := HistoryOut{Messages: []Message{}, Total: page.Page.Total}
	for _, s := range page.Items {
		from := s.SenderName
		if s.Direction == ship.DirectionOut {
			from = "agent"
		} else if from == "" {
			from = s.SenderID
		}
		out.Messages = append(out.Messages, Message{Direction: string(s.Direction), From: from,
			Text: s.Text, SentAt: s.SentAt, Status: string(s.Status)})
	}
	return nil, out, nil
}

type ConversationsIn struct{}

type ConversationItem struct {
	ID      string `json:"conversation_id"`
	Channel string `json:"channel"`
	Kind    string `json:"kind"`
	Title   string `json:"title"`
}

type ConversationsOut struct {
	Conversations []ConversationItem `json:"conversations"`
}

func (t tools) conversations(ctx context.Context, _ *mcp.CallToolRequest, _ ConversationsIn) (*mcp.CallToolResult, ConversationsOut, error) {
	org, agentID, err := caller(ctx)
	if err != nil {
		return failure(err), ConversationsOut{}, nil
	}
	allowed := conversation.StatusAllowed
	filter := conversation.Filter{Status: &allowed}
	if agentID != nil {
		if filter.Docks, err = t.docks(ctx, org, *agentID); err != nil {
			return failure(err), ConversationsOut{}, nil
		}
	}
	page, err := t.d.Conversations.List(ctx, org, filter, query.Pagination{Limit: query.MaxLimit})
	if err != nil {
		return failure(err), ConversationsOut{}, nil
	}
	out := ConversationsOut{Conversations: []ConversationItem{}}
	for _, c := range page.Items {
		out.Conversations = append(out.Conversations, ConversationItem{ID: c.ID.String(),
			Channel: string(c.Channel), Kind: string(c.Kind), Title: c.Title})
	}
	return nil, out, nil
}

// failure reports a tool error to the model (isError) with a safe message.
func failure(err error) *mcp.CallToolResult {
	msg := errx.GenericMessage
	var e *errx.Error
	if errx.As(err, &e) && e.HTTPStatus < 500 {
		msg = e.Message
	}
	return &mcp.CallToolResult{IsError: true, Content: []mcp.Content{&mcp.TextContent{Text: msg}}}
}
