// Package crewmcp exposes the port's tools to agents over MCP (streamable
// HTTP at /mcp, crew token): reply, history, conversations.
package crewmcp

import (
	"context"
	"net/http"
	"strings"
	"time"

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
	Version       string
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
		Description: "List the allowed conversations the agent can reply to.",
	}, t.conversations)
	return mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return srv },
		&mcp.StreamableHTTPOptions{Stateless: true, JSONResponse: true})
}

type tools struct{ d Deps }

// organization is the tenant of the crew token that made the HTTP request
// (the stateless handler runs each call under the request's context).
// Tools fail closed without one.
func organization(ctx context.Context) (identity.OrganizationID, error) {
	p, ok := httpx.PrincipalFrom(ctx)
	if !ok {
		return identity.OrganizationID{}, errx.Unauthorized("missing crew principal")
	}
	return p.Organization, nil
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
	org, err := organization(ctx)
	if err != nil {
		return failure(err), ReplyOut{}, nil
	}
	id, err := identity.ParseConversationID(strings.TrimSpace(in.ConversationID))
	if err != nil {
		return failure(err), ReplyOut{}, nil
	}
	s, err := t.d.Ships.Send(ctx, org, ship.Outbound{Conversation: id, Text: in.Text})
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
	org, err := organization(ctx)
	if err != nil {
		return failure(err), HistoryOut{}, nil
	}
	id, err := identity.ParseConversationID(strings.TrimSpace(in.ConversationID))
	if err != nil {
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
	org, err := organization(ctx)
	if err != nil {
		return failure(err), ConversationsOut{}, nil
	}
	allowed := conversation.StatusAllowed
	page, err := t.d.Conversations.List(ctx, org, conversation.Filter{Status: &allowed}, query.Pagination{Limit: query.MaxLimit})
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
