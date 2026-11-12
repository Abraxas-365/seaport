package dockgithub

import (
	"context"
	"net/http"
	"net/url"
	"strings"

	"github.com/Abraxas-365/seaport/internal/dock"
	"github.com/Abraxas-365/seaport/internal/errx"
	"github.com/Abraxas-365/seaport/internal/httpx"
	"github.com/Abraxas-365/seaport/internal/identity"
	"github.com/gofiber/fiber/v3"
)

// AgentToken mints a token for repo for the agent: the repo must be served
// by an active installation of the App of a GitHub dock the agent works
// on (else NotFound). It hands out a credential: only the crew handler
// below calls it, for the agent's own token (secretCalls).
func (g *Gateway) AgentToken(ctx context.Context, org identity.OrganizationID, agentID identity.AgentID, in dock.GitHubTokenRequest) (dock.GitHubToken, error) {
	if err := in.Validate(); err != nil {
		return dock.GitHubToken{}, err
	}
	host := webHost(g.cfg.WebURL)
	if in.Host != "" && !strings.EqualFold(in.Host, host) {
		return dock.GitHubToken{}, errx.NotFound("no GitHub App of this port serves " + in.Host)
	}
	_, _, teams := g.bound()
	if teams == nil {
		return dock.GitHubToken{}, errx.External("this port is starting; try again")
	}
	a, err := teams.Find(ctx, org, agentID)
	if err != nil {
		return dock.GitHubToken{}, err
	}
	for _, as := range a.Docks {
		app, err := g.store.App(ctx, as.Dock)
		if errx.IsType(err, errx.TypeNotFound) {
			continue // not a GitHub dock, or no App yet
		}
		if err != nil {
			return dock.GitHubToken{}, err
		}
		if app.Organization != org {
			continue
		}
		inst, err := g.client.installationOf(ctx, app, in.Repository)
		if errx.IsType(err, errx.TypeNotFound) {
			continue // the App isn't installed on that repo
		}
		if err != nil {
			return dock.GitHubToken{}, errx.Wrap(err, "find the installation of "+in.Repository, errx.TypeExternal)
		}
		installs, err := g.store.Installations(ctx, app.Dock)
		if err != nil {
			return dock.GitHubToken{}, err
		}
		if !activeInstallation(installs, inst) {
			continue
		}
		t, err := g.client.scopedToken(ctx, app, inst, in.Repository)
		if err != nil {
			return dock.GitHubToken{}, errx.Wrap(err, "mint a token for "+in.Repository, errx.TypeExternal)
		}
		return dock.GitHubToken{Token: t.value, ExpiresAt: t.expires, Repository: in.Repository, Host: host,
			APIURL: g.cfg.APIURL, App: app.Slug}, nil
	}
	return dock.GitHubToken{}, errx.NotFound("no GitHub dock of agent " + a.Name + " has its App installed on " + in.Repository)
}

func activeInstallation(installs []Installation, id int64) bool {
	for _, in := range installs {
		if in.ID == id && !in.Suspended {
			return true
		}
	}
	return false
}

// webHost is the host of GitHub's web URL (github.com by default).
func webHost(web string) string {
	u, err := url.Parse(web)
	if err != nil || u.Host == "" {
		return "github.com"
	}
	return strings.ToLower(u.Host)
}

// Token serves POST /crew/github/token (behind crew auth): an App token
// for one repository, for the caller's own agent (a crew token of an
// agent; the open mode's local owner names none and is refused).
func (h *Handler) Token() fiber.Handler {
	return func(c fiber.Ctx) error {
		p, ok := httpx.PrincipalFrom(c.Context())
		if !ok || p.Role != httpx.RoleCrew {
			return errx.Unauthorized("missing crew principal")
		}
		if p.Agent.IsZero() {
			return errx.Forbidden("GitHub tokens go to an agent's crew token")
		}
		var in dock.GitHubTokenRequest
		if err := httpx.DecodeJSON(c, &in); err != nil {
			return err
		}
		t, err := h.g.AgentToken(c.Context(), p.Organization, p.Agent, in)
		if err != nil {
			return err
		}
		c.Set("Cache-Control", "no-store")
		return httpx.JSON(c, http.StatusOK, t)
	}
}
