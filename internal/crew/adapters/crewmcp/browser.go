package crewmcp

import (
	"context"
	"strings"

	"github.com/Abraxas-365/seaport/internal/agent"
	"github.com/Abraxas-365/seaport/internal/errx"
	"github.com/Abraxas-365/seaport/internal/httpx"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// addLoginNeeded registers login_needed when the agent's profile has the
// managed browser on: a site asking for credentials is a person's job
// (they sign in from the web's browser viewer; the agent never types a
// password it doesn't have).
func (t tools) addLoginNeeded(ctx context.Context, srv *mcp.Server) {
	p, ok := httpx.PrincipalFrom(ctx)
	if !ok || p.Agent.IsZero() || t.d.Logins == nil {
		return
	}
	profile, err := t.d.Agents.Profile(ctx, p.Organization, p.Agent, 0)
	if err != nil || !profile.Browser {
		return
	}
	mcp.AddTool(srv, &mcp.Tool{
		Name: "login_needed",
		Description: "Tell the people of this agent that a site in the browser asks for a sign-in you cannot do " +
			"(a login page, an expired session, a 2FA prompt). Never guess or ask for passwords: call this with the site's host " +
			"and what you were doing, tell the person in the conversation that you need them to sign in from the web app's " +
			"browser tab, and go on with what doesn't need it. Once they sign in, the session is yours: retry later.",
	}, t.loginNeeded)
}

type LoginNeededIn struct {
	Host   string `json:"host" jsonschema:"the site's host name, as in the address bar (app.example.com)"`
	Reason string `json:"reason,omitempty" jsonschema:"what you were doing there, in one line"`
}

type LoginNeededOut struct {
	Host     string `json:"host"`
	Recorded bool   `json:"recorded"`
	Pending  int    `json:"pending_logins"`
}

func (t tools) loginNeeded(ctx context.Context, _ *mcp.CallToolRequest, in LoginNeededIn) (*mcp.CallToolResult, LoginNeededOut, error) {
	org, agentID, err := caller(ctx)
	if err != nil {
		return failure(err), LoginNeededOut{}, nil
	}
	if agentID == nil {
		return failure(errx.Validation("no agent: a login request belongs to an agent")), LoginNeededOut{}, nil
	}
	a, err := t.d.Logins.RequestLogin(ctx, org, *agentID, agent.RequestLogin{Host: in.Host, Reason: in.Reason})
	if err != nil {
		return failure(err), LoginNeededOut{}, nil
	}
	return nil, LoginNeededOut{Host: strings.ToLower(strings.TrimSpace(in.Host)), Recorded: true, Pending: len(a.LoginRequests)}, nil
}
