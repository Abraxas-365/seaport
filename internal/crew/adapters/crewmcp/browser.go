package crewmcp

import (
	"context"
	"strings"
	"time"

	"github.com/Abraxas-365/seaport/internal/agent"
	"github.com/Abraxas-365/seaport/internal/errx"
	"github.com/Abraxas-365/seaport/internal/httpx"
	"github.com/Abraxas-365/seaport/internal/identity"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// addTakeover registers ask_takeover when the agent's profile has the
// managed browser on: a site asking for credentials, a captcha or a code
// is a person's job (they take over from the web's browser viewer; the
// agent never types a password it doesn't have).
func (t tools) addTakeover(ctx context.Context, srv *mcp.Server) {
	p, ok := httpx.PrincipalFrom(ctx)
	if !ok || p.Agent.IsZero() || t.d.Takeovers == nil {
		return
	}
	profile, err := t.d.Agents.Profile(ctx, p.Organization, p.Agent, 0)
	if err != nil || !profile.Browser {
		return
	}
	mcp.AddTool(srv, &mcp.Tool{
		Name: "ask_takeover",
		Description: "Ask the people of this agent to take over the browser at a site: a sign-in you cannot do, a captcha, " +
			"a 2FA code, a 'verify you are human' wall, a payment. Never guess or ask for passwords or codes: call this with " +
			"the site's host, what they must do (kind) and the conversation you are working. Seaport tells a person where " +
			"the profile says (notified: true) with a link to the browser; else tell them yourself in the conversation. " +
			"Either wait here (wait_seconds; done: true means they finished) or go on with what doesn't need the browser: " +
			"when they press Listo you get a system message in the conversation. Then the browser is yours again.",
	}, t.askTakeover)
}

type AskTakeoverIn struct {
	Host           string `json:"host" jsonschema:"the site's host name, as in the address bar (app.example.com)"`
	Kind           string `json:"kind,omitempty" jsonschema:"what the person must do: login (default), captcha, 2fa or other"`
	Note           string `json:"note,omitempty" jsonschema:"what you were doing there, in one line (required when kind is other)"`
	ConversationID string `json:"conversation_id,omitempty" jsonschema:"the conversation you are working (the one in the seaport prompt)"`
	WaitSeconds    int    `json:"wait_seconds,omitempty" jsonschema:"block up to this many seconds (at most 290) for the person to finish; 0 returns at once"`
}

type AskTakeoverOut struct {
	Host     string `json:"host"`
	Kind     string `json:"kind"`
	Recorded bool   `json:"recorded"`
	// Notified: a person was told (now or when this host was first asked).
	Notified bool `json:"notified"`
	// Done: the person finished while the call waited (the takeover is
	// gone); false with wait_seconds means it is still open.
	Done    bool `json:"done"`
	Pending int  `json:"pending_takeovers"`
}

func (t tools) askTakeover(ctx context.Context, _ *mcp.CallToolRequest, in AskTakeoverIn) (*mcp.CallToolResult, AskTakeoverOut, error) {
	org, agentID, err := caller(ctx)
	if err != nil {
		return failure(err), AskTakeoverOut{}, nil
	}
	if agentID == nil {
		return failure(errx.Validation("no agent: a takeover belongs to an agent")), AskTakeoverOut{}, nil
	}
	ask := agent.AskTakeover{Host: in.Host, Kind: agent.TakeoverKind(strings.ToLower(strings.TrimSpace(in.Kind))), Note: in.Note,
		Wait: time.Duration(in.WaitSeconds) * time.Second}
	if raw := strings.TrimSpace(in.ConversationID); raw != "" {
		conv, err := identity.ParseConversationID(raw)
		if err != nil {
			return failure(err), AskTakeoverOut{}, nil
		}
		if err := t.visible(ctx, org, agentID, conv); err != nil {
			return failure(err), AskTakeoverOut{}, nil
		}
		ask.Conversation = &conv
	}
	a, err := t.d.Takeovers.RequestTakeover(ctx, org, *agentID, ask)
	if err != nil {
		return failure(err), AskTakeoverOut{}, nil
	}
	out := AskTakeoverOut{Host: strings.ToLower(strings.TrimSpace(in.Host)), Kind: string(ask.Kind), Recorded: true, Pending: len(a.Takeovers)}
	if out.Kind == "" {
		out.Kind = string(agent.KindLogin)
	}
	if tk, open := a.Takeovers.Find(out.Host); open {
		out.Notified = tk.NotifiedAt != nil
	} else {
		out.Done = true
	}
	return nil, out, nil
}
