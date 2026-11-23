// Package crewmcp exposes the port's tools to agents over MCP (streamable
// HTTP at /mcp, crew token): reply, history, conversations, skill when
// the agent's profile has skills, destinations and open when its docks
// have destinations.
package crewmcp

import (
	"context"
	"encoding/base64"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/Abraxas-365/seaport/internal/agent"
	"github.com/Abraxas-365/seaport/internal/conversation"
	"github.com/Abraxas-365/seaport/internal/crew"
	"github.com/Abraxas-365/seaport/internal/dock"
	"github.com/Abraxas-365/seaport/internal/errx"
	"github.com/Abraxas-365/seaport/internal/httpx"
	"github.com/Abraxas-365/seaport/internal/httpx/stdhttp"
	"github.com/Abraxas-365/seaport/internal/identity"
	"github.com/Abraxas-365/seaport/internal/query"
	"github.com/Abraxas-365/seaport/internal/ship"
	"github.com/Abraxas-365/seaport/internal/vault"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type Deps struct {
	Ships ship.Commands
	// Reads answers the agent's reads (history, attachments, approvals);
	// history only through AgentHistory, which checks the docks' tool
	// switches (TestCrewReadsCheckTools).
	Reads         ship.Queries
	Conversations conversation.Queries
	// Agents scopes a token's tools to its agent's docks.
	Agents agent.Queries
	// Takeovers records the sites where the agent needs a person to take
	// over its browser (ask_takeover; registered when the profile's
	// browser is on).
	Takeovers agent.Commands
	// Alarms sets, lists and cancels the agent's alarms (set_alarm, alarms,
	// cancel_alarm; registered when the profile has alarms on; nil: none).
	Alarms AlarmDeps
	// Docks lists the destinations the agent may open conversations at
	// (nil: no open tool).
	Docks dock.Queries
	// Vaults and VaultWrites give the agent its memory (the vault tools;
	// nil: none). Writes go only through VaultWrites, which checks the
	// grant (TestVaultWritesCheckAccess).
	Vaults      vault.Queries
	VaultWrites vault.Writer
	Version     string
}

// Handler returns the MCP endpoint. Each request gets a server of its own
// (stateless): its skill tool lists the calling agent's current skills.
func Handler(d Deps) http.Handler {
	// The SDK's DNS-rebinding check (loopback address + foreign Host →
	// 403) is Seaport's own already: httpx.Open checks Host in none mode,
	// and the other modes need a bearer a rebound page can't send. It would
	// refuse hosted slots, which reach a port on the host as
	// host.docker.internal (through loopback on Docker Desktop).
	h := mcp.NewStreamableHTTPHandler(func(r *http.Request) *mcp.Server { return server(r.Context(), d) },
		&mcp.StreamableHTTPOptions{Stateless: true, JSONResponse: true, DisableLocalhostProtection: true,
			MaxRequestBodyBytes: crew.MaxReplyBody})
	return withAgent(d.Agents, h)
}

// withAgent resolves the agent the request acts as (crew.AgentHeader): in
// open mode the header names it, and it becomes the principal's agent, so
// the tools are scoped to it like a token's; with a token the header must
// name the token's agent.
func withAgent(agents agent.Queries, next http.Handler) http.Handler {
	return stdhttp.Handle(func(w http.ResponseWriter, r *http.Request) error {
		p, ok := httpx.PrincipalFrom(r.Context())
		if !ok {
			return errx.Unauthorized("missing crew principal")
		}
		name := r.Header.Get(crew.AgentHeader)
		if name == "" {
			next.ServeHTTP(w, r)
			return nil
		}
		id, resolved, err := crew.ResolveAgent(r.Context(), agents,
			crew.Caller{Organization: p.Organization, Agent: p.Agent, AgentName: p.AgentName}, name)
		if err != nil {
			return err
		}
		p.Agent, p.AgentName = id, resolved
		next.ServeHTTP(w, r.WithContext(httpx.WithPrincipal(r.Context(), p)))
		return nil
	})
}

func server(ctx context.Context, d Deps) *mcp.Server {
	srv := mcp.NewServer(&mcp.Implementation{Name: "seaport", Title: "Seaport", Version: d.Version}, &mcp.ServerOptions{
		Instructions: "Seaport is the port where your messages (WhatsApp and other channels) arrive. " +
			"Each prompt from seaport names a conversation_id. Answer people with the reply tool; " +
			"use history to read earlier messages of a conversation. Before anything consequential or irreversible " +
			"(deploy, delete, spend, send on someone's behalf), ask people with ask_approval and act only on the " +
			"option they choose; if it expires or is cancelled, do not do it. " +
			"Long-term memory lives in vaults (when you have the vault tools): search it with memory_search before answering " +
			"about the company, a customer or a past decision, and save durable facts with memory_save " +
			"(one fact per entry, no secrets, nothing transient). " +
			"When you have the alarm tools, use set_alarm to come back to a conversation later instead of waiting.",
	})
	t := tools{d: d}
	mcp.AddTool(srv, &mcp.Tool{
		Name: "reply",
		Description: "Send a message to a seaport conversation (WhatsApp chat, GitHub pull request...), optionally with files " +
			"(reports, screenshots; base64). Only allowed conversations accept replies. For files on disk, prefer the shell: " +
			"`seaport reply <conversation_id> -file report.md -file shot.png \"text\"`.",
	}, t.reply)
	on := t.toolSet(ctx)
	if on.History {
		mcp.AddTool(srv, &mcp.Tool{
			Name: "history",
			Description: "Read the latest messages of a conversation, newest first (inbound and outbound). " +
				"With thread: true, the messages of every conversation linked with it (the same matter seen from " +
				"other channels: a pull request and the Slack thread about it), each naming its conversation_id.",
		}, t.history)
	}
	mcp.AddTool(srv, &mcp.Tool{
		Name: "ask_approval",
		Description: "Ask the people of a conversation to choose between 2 to 6 options before you do something consequential. " +
			"The question goes out as a message with numbered options; they answer in the channel or in the web app. " +
			"With wait_seconds (at most 290) the call blocks for the answer; otherwise, or when it times out, it returns " +
			"status pending: stop there (an answer comes back to you as a message) or read it later with approval. " +
			"One pending question per conversation; it expires after a day. Act only on status answered.",
	}, t.askApproval)
	mcp.AddTool(srv, &mcp.Tool{
		Name:        "approval",
		Description: "Read an approval you asked (status pending | answered | expired | cancelled, and the answer); wait_seconds blocks while it is pending.",
	}, t.approval)
	if on.Attachments {
		mcp.AddTool(srv, &mcp.Tool{
			Name:        "attachment",
			Description: "Look at a file attached to a message (an image, a document), by the attachment id that history lists.",
		}, t.attachment)
	}
	if on.Conversations {
		mcp.AddTool(srv, &mcp.Tool{
			Name:        "conversations",
			Description: "List the allowed conversations the agent can reply to (those of the docks it works on).",
		}, t.conversations)
	}
	t.addSkill(ctx, srv)
	t.addOpen(ctx, srv)
	t.addTakeover(ctx, srv)
	t.addAlarms(ctx, srv)
	t.addVaults(ctx, srv)
	return srv
}

type tools struct{ d Deps }

// toolSet says which of the dock-switched tools (dock.Tools) the caller
// gets: those some dock of its agent turns on. Each call checks the
// conversation's own dock again (the ship service for history and
// attachments, conversations by filtering). Without an agent (the local
// owner) or a dock reader, or for an agent on no dock (it finds nothing
// anyway), every one.
func (t tools) toolSet(ctx context.Context) dock.Tools {
	org, agentID, err := caller(ctx)
	if err != nil || agentID == nil || t.d.Docks == nil {
		return dock.AllTools
	}
	ids, err := t.docks(ctx, org, *agentID)
	if err != nil || len(ids) == 0 {
		return dock.AllTools
	}
	var on dock.Tools
	for _, id := range ids {
		d, err := t.d.Docks.Find(ctx, org, id)
		if err != nil {
			continue
		}
		on.History = on.History || d.Tools.History
		on.Conversations = on.Conversations || d.Tools.Conversations
		on.Attachments = on.Attachments || d.Tools.Attachments
	}
	return on
}

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
	ConversationID string      `json:"conversation_id" jsonschema:"the conversation_id given in the seaport prompt"`
	Text           string      `json:"text,omitempty" jsonschema:"the message to send (may be empty when files go)"`
	Files          []ReplyFile `json:"files,omitempty" jsonschema:"files sent with the message (at most 10; 10 MiB each, 25 MiB in all)"`
}

// ReplyFile is a file of a reply; its type is sniffed from the bytes.
type ReplyFile struct {
	Name   string `json:"name" jsonschema:"the file name people see, e.g. qa-report.md"`
	Base64 string `json:"content_base64" jsonschema:"the file's bytes, base64 (standard)"`
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
	files := make([]ship.File, 0, len(in.Files))
	for _, f := range in.Files {
		data, err := base64.StdEncoding.DecodeString(strings.TrimSpace(f.Base64))
		if err != nil {
			return failure(errx.InvalidField("files.content_base64", "file "+f.Name+" is not valid base64")), ReplyOut{}, nil
		}
		files = append(files, ship.File{Name: strings.TrimSpace(f.Name), Data: data})
	}
	// Send refuses a conversation of a dock the agent doesn't work on.
	s, err := t.d.Ships.Send(ctx, org, ship.Outbound{Conversation: id, Text: in.Text, Agent: agentID, Files: files})
	if err != nil {
		return failure(err), ReplyOut{}, nil
	}
	return nil, ReplyOut{ShipID: s.ID.String(), Status: string(s.Status)}, nil
}

type HistoryIn struct {
	ConversationID string `json:"conversation_id"`
	Limit          int    `json:"limit,omitempty" jsonschema:"how many messages (1-100, default 20)"`
	Thread         bool   `json:"thread,omitempty" jsonschema:"read every conversation of this one's thread together (each message names its conversation_id)"`
}

type Message struct {
	Direction string    `json:"direction"`
	From      string    `json:"from"`
	Text      string    `json:"text"`
	SentAt    time.Time `json:"sent_at"`
	Status    string    `json:"status"`
	// ConversationID names the message's door when reading a thread.
	ConversationID string           `json:"conversation_id,omitempty"`
	Attachments    []AttachmentInfo `json:"attachments,omitempty"`
}

// AttachmentInfo names a message's file; the attachment tool shows it.
// A withheld one (its dock keeps files from agents) has no id.
type AttachmentInfo struct {
	ID       string `json:"attachment_id,omitempty"`
	Kind     string `json:"kind"`
	MIME     string `json:"mime"`
	Size     int64  `json:"size"`
	Name     string `json:"name,omitempty"`
	Withheld bool   `json:"withheld,omitempty"`
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
	// AgentHistory scopes the read to the agent's docks and their tool
	// switches. With Thread, the thread's other doors are read too (the
	// owner linked them, and the prompt names them); replies still go only
	// to the agent's docks.
	page, err := t.d.Reads.AgentHistory(ctx, org, ship.AgentHistory{Agent: agentID, Conversation: id,
		Thread: in.Thread, Page: query.Pagination{Limit: in.Limit}})
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
		m := Message{Direction: string(s.Direction), From: from, Text: s.Text, SentAt: s.SentAt, Status: string(s.Status)}
		if in.Thread {
			m.ConversationID = s.Conversation.String()
		}
		for _, a := range s.Attachments {
			info := AttachmentInfo{Kind: string(a.Kind), MIME: a.MIME, Size: a.Size, Name: a.Name, Withheld: a.Withheld}
			if !a.Withheld {
				info.ID = a.ID.String()
			}
			m.Attachments = append(m.Attachments, info)
		}
		out.Messages = append(out.Messages, m)
	}
	return nil, out, nil
}

type AttachmentIn struct {
	AttachmentID string `json:"attachment_id" jsonschema:"an attachment_id listed by history"`
}

// maxAttachmentText bounds the text of a document the attachment tool
// hands back (the rest is cut, saying so: the whole file is a download).
const maxAttachmentText = 256 << 10

// attachment answers the file (the agent's docks only: OpenAttachment
// checks them): an image as MCP image content, a text document as text,
// and any other document as a note on where to download it (MCP clients
// such as rness take no embedded resources).
func (t tools) attachment(ctx context.Context, _ *mcp.CallToolRequest, in AttachmentIn) (*mcp.CallToolResult, any, error) {
	org, agentID, err := caller(ctx)
	if err != nil {
		return failure(err), nil, nil
	}
	id, err := identity.ParseAttachmentID(strings.TrimSpace(in.AttachmentID))
	if err != nil {
		return failure(err), nil, nil
	}
	f, err := t.d.Reads.OpenAttachment(ctx, org, ship.OpenAttachment{ID: id, Agent: agentID})
	if err != nil {
		return failure(err), nil, nil
	}
	if f.Kind == ship.AttachmentImage {
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.ImageContent{Data: f.Data, MIMEType: f.MIME}}}, nil, nil
	}
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: documentText(f)}}}, nil, nil
}

// documentText is what the attachment tool says of a document: its text
// when it is text (cut at maxAttachmentText), else where to get it.
func documentText(f ship.AttachmentFile) string {
	name := f.Name
	if name == "" {
		name = "(unnamed)"
	}
	head := fmt.Sprintf("document %s (%s, %d bytes)", name, f.MIME, len(f.Data))
	if !strings.HasPrefix(f.MIME, "text/") || !utf8.Valid(f.Data) {
		return head + ": not text, so not shown here. The connector saves the files of the messages it hands you under " +
			"<working directory>/.seaport/attachments/" + f.ID.String() + "/; else GET /crew/attachments/" + f.ID.String() +
			" with your crew token."
	}
	body := f.Data
	cut := ""
	if len(body) > maxAttachmentText {
		body = body[:maxAttachmentText]
		for len(body) > 0 && !utf8.Valid(body) {
			body = body[:len(body)-1]
		}
		cut = fmt.Sprintf("\n[cut at %d bytes; the whole file is GET /crew/attachments/%s]", len(body), f.ID)
	}
	return head + ":\n" + string(body) + cut
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
		ids, err := t.docks(ctx, org, *agentID)
		if err != nil {
			return failure(err), ConversationsOut{}, nil
		}
		// Only docks whose owner lets agents list their chats.
		filter.Docks = []identity.DockID{}
		for _, id := range ids {
			if t.d.Docks != nil {
				d, err := t.d.Docks.Find(ctx, org, id)
				if err != nil {
					return failure(err), ConversationsOut{}, nil
				}
				if !d.Tools.Conversations {
					continue
				}
			}
			filter.Docks = append(filter.Docks, id)
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
