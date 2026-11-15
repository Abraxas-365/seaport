package authhttp

import (
	"net/http"
	"strings"

	"github.com/Abraxas-365/seaport/internal/auth"
	"github.com/Abraxas-365/seaport/internal/errx"
	"github.com/Abraxas-365/seaport/internal/httpx"
	"github.com/Abraxas-365/seaport/internal/query"
	"github.com/gofiber/fiber/v3"
)

// Members is the tenant administration API (iam mode, optional): people
// manage their organization's members, invitations and CLI API keys. Every
// route runs as the signed-in person (their own access token goes to
// IAMKit), so the routes are guarded by httpx.Delegated, not by a Seaport
// permission: IAMKit's iam:org:* permissions decide, and its 403 reaches the
// client.
type Members struct {
	auth     auth.Commands
	commands auth.MemberCommands
	queries  auth.MemberQueries
}

func NewMembers(a auth.Commands, c auth.MemberCommands, q auth.MemberQueries) *Members {
	return &Members{auth: a, commands: c, queries: q}
}

// Register mounts the routes on r (already behind admin auth).
func (m *Members) Register(r httpx.Router) {
	r.Get("/api/v1/members/access", httpx.Delegated(m.access))
	r.Get("/api/v1/members", httpx.Delegated(m.list))
	r.Put("/api/v1/members/:user", httpx.Delegated(m.setAccess))
	r.Delete("/api/v1/members/:user", httpx.Delegated(m.remove))
	r.Get("/api/v1/members/invitations", httpx.Delegated(m.invitations))
	r.Post("/api/v1/members/invitations", httpx.Delegated(m.invite))
	r.Post("/api/v1/members/invitations/:id/resend", httpx.Delegated(m.resend))
	r.Delete("/api/v1/members/invitations/:id", httpx.Delegated(m.revokeInvitation))
	r.Get("/api/v1/members/api-keys", httpx.Delegated(m.apiKeys))
	r.Post("/api/v1/members/api-keys", httpx.Delegated(m.createKey))
	r.Delete("/api/v1/members/api-keys/:id", httpx.Delegated(m.revokeKey))
}

// actor is the person behind the request: the access token their session or
// bearer holds. An API key is a credential of the organization, not of a
// person, so it can't act as one.
func (m *Members) actor(c fiber.Ctx) (auth.Actor, error) {
	p, err := httpx.Caller(c)
	if err != nil {
		return auth.Actor{}, err
	}
	if strings.HasPrefix(p.Subject, auth.APIKeySubjectPrefix) {
		return auth.Actor{}, errx.Forbidden("sign in as a person to manage the organization: an API key can't")
	}
	token := httpx.Bearer(c)
	if c.Get("Authorization") == "" {
		_, session, err := m.auth.Authenticate(c.Context(), sessionID(c))
		if err != nil {
			return auth.Actor{}, err
		}
		token = session.Tokens.Access
	}
	return auth.Actor{Token: token, Subject: p.Subject, Permissions: p.Permissions}, nil
}

// idOf is a path id IAMKit's client can put in a URL: ids are opaque
// letters, digits, '-' and '_'; anything else names nothing.
func idOf(c fiber.Ctx, key string) (string, error) {
	id := httpx.Param(c, key)
	if id == "" || len(id) > 128 {
		return "", errx.NotFound("not found")
	}
	for _, r := range id {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_') {
			return "", errx.NotFound("not found")
		}
	}
	return id, nil
}

func list[T any](items []T) query.Paginated[T] {
	if items == nil {
		items = []T{}
	}
	return query.Paginated[T]{Items: items, Page: query.Page{Total: len(items), Limit: len(items)}}
}

func (m *Members) access(c fiber.Ctx) error {
	org, err := httpx.Organization(c)
	if err != nil {
		return err
	}
	who, err := m.actor(c)
	if err != nil {
		return err
	}
	out, err := m.queries.Access(c.Context(), org, who)
	if err != nil {
		return err
	}
	return httpx.JSON(c, http.StatusOK, out)
}

func (m *Members) list(c fiber.Ctx) error {
	org, err := httpx.Organization(c)
	if err != nil {
		return err
	}
	who, err := m.actor(c)
	if err != nil {
		return err
	}
	out, err := m.queries.Members(c.Context(), org, who)
	if err != nil {
		return err
	}
	return httpx.JSON(c, http.StatusOK, list(out))
}

type accessRequest struct {
	Role           string `json:"role"`
	ManagesMembers bool   `json:"manages_members"`
}

func (m *Members) setAccess(c fiber.Ctx) error {
	org, err := httpx.Organization(c)
	if err != nil {
		return err
	}
	who, err := m.actor(c)
	if err != nil {
		return err
	}
	user, err := idOf(c, "user")
	if err != nil {
		return err
	}
	var req accessRequest
	if err := httpx.DecodeJSON(c, &req); err != nil {
		return err
	}
	out, err := m.commands.SetAccess(c.Context(), org, who, user, auth.SetAccess{Role: req.Role, ManagesMembers: req.ManagesMembers})
	if err != nil {
		return err
	}
	return httpx.JSON(c, http.StatusOK, out)
}

func (m *Members) remove(c fiber.Ctx) error {
	org, err := httpx.Organization(c)
	if err != nil {
		return err
	}
	who, err := m.actor(c)
	if err != nil {
		return err
	}
	user, err := idOf(c, "user")
	if err != nil {
		return err
	}
	if err := m.commands.Remove(c.Context(), org, who, user); err != nil {
		return err
	}
	return c.SendStatus(http.StatusNoContent)
}

func (m *Members) invitations(c fiber.Ctx) error {
	org, err := httpx.Organization(c)
	if err != nil {
		return err
	}
	who, err := m.actor(c)
	if err != nil {
		return err
	}
	out, err := m.queries.Invitations(c.Context(), org, who)
	if err != nil {
		return err
	}
	return httpx.JSON(c, http.StatusOK, list(out))
}

type inviteRequest struct {
	Email          string `json:"email"`
	Role           string `json:"role"`
	ManagesMembers bool   `json:"manages_members"`
}

func (m *Members) invite(c fiber.Ctx) error {
	org, err := httpx.Organization(c)
	if err != nil {
		return err
	}
	who, err := m.actor(c)
	if err != nil {
		return err
	}
	var req inviteRequest
	if err := httpx.DecodeJSON(c, &req); err != nil {
		return err
	}
	out, err := m.commands.Invite(c.Context(), org, who, auth.Invite{Email: req.Email, Role: req.Role, ManagesMembers: req.ManagesMembers})
	if err != nil {
		return err
	}
	return httpx.Created(c, out.ID, out)
}

func (m *Members) resend(c fiber.Ctx) error {
	org, err := httpx.Organization(c)
	if err != nil {
		return err
	}
	who, err := m.actor(c)
	if err != nil {
		return err
	}
	id, err := idOf(c, "id")
	if err != nil {
		return err
	}
	out, err := m.commands.Resend(c.Context(), org, who, id)
	if err != nil {
		return err
	}
	return httpx.JSON(c, http.StatusOK, out)
}

func (m *Members) revokeInvitation(c fiber.Ctx) error {
	org, err := httpx.Organization(c)
	if err != nil {
		return err
	}
	who, err := m.actor(c)
	if err != nil {
		return err
	}
	id, err := idOf(c, "id")
	if err != nil {
		return err
	}
	if err := m.commands.RevokeInvitation(c.Context(), org, who, id); err != nil {
		return err
	}
	return c.SendStatus(http.StatusNoContent)
}

func (m *Members) apiKeys(c fiber.Ctx) error {
	org, err := httpx.Organization(c)
	if err != nil {
		return err
	}
	who, err := m.actor(c)
	if err != nil {
		return err
	}
	out, err := m.queries.APIKeys(c.Context(), org, who)
	if err != nil {
		return err
	}
	return httpx.JSON(c, http.StatusOK, list(out))
}

type keyRequest struct {
	Name      string `json:"name"`
	Role      string `json:"role"`
	ExpiresIn string `json:"expires_in"`
}

func (m *Members) createKey(c fiber.Ctx) error {
	org, err := httpx.Organization(c)
	if err != nil {
		return err
	}
	who, err := m.actor(c)
	if err != nil {
		return err
	}
	var req keyRequest
	if err := httpx.DecodeJSON(c, &req); err != nil {
		return err
	}
	out, err := m.commands.CreateAPIKey(c.Context(), org, who, auth.NewAPIKey{Name: req.Name, Role: req.Role, ExpiresIn: req.ExpiresIn})
	if err != nil {
		return err
	}
	// The value is shown once: never cached anywhere.
	c.Set("Cache-Control", "no-store")
	return httpx.Created(c, out.ID, out)
}

func (m *Members) revokeKey(c fiber.Ctx) error {
	org, err := httpx.Organization(c)
	if err != nil {
		return err
	}
	who, err := m.actor(c)
	if err != nil {
		return err
	}
	id, err := idOf(c, "id")
	if err != nil {
		return err
	}
	if err := m.commands.RevokeAPIKey(c.Context(), org, who, id); err != nil {
		return err
	}
	return c.SendStatus(http.StatusNoContent)
}
