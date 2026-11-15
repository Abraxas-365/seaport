package crewmcp

import (
	"context"
	"strings"

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
			"the site's host, what they must do (kind) and the conversation you are working, tell the person in the " +
			"conversation that you need them in the web app's browser tab, and go on with what doesn't need it. Once they " +
			"are done, the browser is yours again: retry later.",
	}, t.askTakeover)
}

type AskTakeoverIn struct {
	Host           string `json:"host" jsonschema:"the site's host name, as in the address bar (app.example.com)"`
	Kind           string `json:"kind,omitempty" jsonschema:"what the person must do: login (default), captcha, 2fa or other"`
	Note           string `json:"note,omitempty" jsonschema:"what you were doing there, in one line (required when kind is other)"`
	ConversationID string `json:"conversation_id,omitempty" jsonschema:"the conversation you are working (the one in the seaport prompt)"`
}

type AskTakeoverOut struct {
	Host     string `json:"host"`
	Kind     string `json:"kind"`
	Recorded bool   `json:"recorded"`
	Pending  int    `json:"pending_takeovers"`
}

func (t tools) askTakeover(ctx context.Context, _ *mcp.CallToolRequest, in AskTakeoverIn) (*mcp.CallToolResult, AskTakeoverOut, error) {
	org, agentID, err := caller(ctx)
	if err != nil {
		return failure(err), AskTakeoverOut{}, nil
	}
	if agentID == nil {
		return failure(errx.Validation("no agent: a takeover belongs to an agent")), AskTakeoverOut{}, nil
	}
	ask := agent.AskTakeover{Host: in.Host, Kind: agent.TakeoverKind(strings.ToLower(strings.TrimSpace(in.Kind))), Note: in.Note}
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
	out := AskTakeoverOut{Host: strings.ToLower(strings.TrimSpace(in.Host)), Kind: string(agent.KindLogin), Recorded: true, Pending: len(a.Takeovers)}
	if len(a.Takeovers) > 0 {
		out.Kind = string(a.Takeovers[0].Kind)
	}
	return nil, out, nil
}
