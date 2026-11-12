package crewhttp_test

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Abraxas-365/seaport/internal/agent"
	"github.com/Abraxas-365/seaport/internal/crew"
	"github.com/Abraxas-365/seaport/internal/crew/adapters/crewhttp"
	"github.com/Abraxas-365/seaport/internal/errx"
	"github.com/Abraxas-365/seaport/internal/httpx"
	"github.com/Abraxas-365/seaport/internal/identity"
	"github.com/Abraxas-365/seaport/internal/permission"
	"github.com/Abraxas-365/seaport/internal/query"
	"github.com/Abraxas-365/seaport/internal/ship"
	"github.com/gofiber/fiber/v3"
)

var org = identity.MustParseOrganizationID("00000000-0000-4000-8000-000000000001")

type tokens struct {
	crew.TokenCommands
	crew.TokenQueries
	revoked identity.CrewTokenID
	filter  crew.TokenFilter
}

func (f *tokens) Create(_ context.Context, _ identity.OrganizationID, in crew.CreateToken) (crew.Issued, error) {
	return crew.Issued{Token: crew.Token{ID: identity.NewCrewTokenID(), Name: in.Name}, Value: "sp_crew_x"}, nil
}

func (f *tokens) Revoke(_ context.Context, _ identity.OrganizationID, id identity.CrewTokenID) error {
	f.revoked = id
	return nil
}

func (f *tokens) List(_ context.Context, _ identity.OrganizationID, filter crew.TokenFilter, _ query.Pagination) (query.Paginated[crew.Token], error) {
	f.filter = filter
	return query.Paginated[crew.Token]{Items: []crew.Token{}}, nil
}

type status struct{}

func (status) Status(context.Context, identity.OrganizationID) (crew.Status, error) {
	return crew.Status{Agents: []crew.Agent{}}, nil
}

type ships struct {
	ship.Commands
	ship.Queries
	open ship.OpenAttachment
	file ship.AttachmentFile
	sent ship.Outbound
}

func (s *ships) OpenAttachment(_ context.Context, _ identity.OrganizationID, in ship.OpenAttachment) (ship.AttachmentFile, error) {
	s.open = in
	if s.file.Data == nil {
		return ship.AttachmentFile{}, errx.NotFound("attachment not found")
	}
	return s.file, nil
}

func (s *ships) Send(_ context.Context, _ identity.OrganizationID, out ship.Outbound) (ship.Ship, error) {
	s.sent = out
	return ship.Ship{ID: identity.NewShipID(), Status: ship.StatusPending}, nil
}

type agents struct {
	agent.Queries
	agent.Kits
}

func (agents) FindByName(_ context.Context, _ identity.OrganizationID, name string) (agent.Agent, error) {
	return agent.Agent{ID: identity.NewAgentID(), Name: name}, nil
}

func (agents) Equip(_ context.Context, _ identity.OrganizationID, id identity.AgentID) (agent.Kit, error) {
	return agent.Kit{Agent: id, Name: "coder"}, nil
}

func app(p *httpx.Principal, mount func(*fiber.App)) *fiber.App {
	a := fiber.New(fiber.Config{ErrorHandler: httpx.ErrorHandler, Immutable: true})
	a.Use(func(c fiber.Ctx) error {
		if p != nil {
			httpx.SetPrincipal(c, *p)
		}
		return c.Next()
	})
	mount(a)
	return a
}

func call(t *testing.T, a *fiber.App, method, path, body string) (*http.Response, string) {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := a.Test(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp, string(b)
}

func admin(perms []string) *httpx.Principal {
	return &httpx.Principal{Organization: org, Role: httpx.RoleAdmin, Subject: "t", Permissions: perms}
}

func TestTokensAdmin(t *testing.T) {
	f := &tokens{}
	a := app(admin(permission.All), func(a *fiber.App) {
		crewhttp.New(f, f).Register(a)
		a.Get("/api/v1/crew", httpx.RequireFunc(permission.CrewRead, crewhttp.ServeStatus(status{})))
	})
	resp, body := call(t, a, "POST", "/api/v1/crew/tokens", `{"name":"laptop","agent_id":"`+identity.NewAgentID().String()+`"}`)
	if resp.StatusCode != 201 || resp.Header.Get("Cache-Control") != "no-store" || resp.Header.Get("Location") == "" || !strings.Contains(body, "sp_crew_x") {
		t.Fatalf("create: %d %v %s", resp.StatusCode, resp.Header, body)
	}
	agentID := identity.NewAgentID()
	if resp, _ := call(t, a, "GET", "/api/v1/crew/tokens?agent_id="+agentID.String(), ""); resp.StatusCode != 200 || f.filter.Agent == nil || *f.filter.Agent != agentID {
		t.Fatalf("list: %d %+v", resp.StatusCode, f.filter)
	}
	if resp, _ := call(t, a, "GET", "/api/v1/crew/tokens?agent_id=nope", ""); resp.StatusCode != 400 {
		t.Fatalf("list bad agent: %d", resp.StatusCode)
	}
	id := identity.NewCrewTokenID()
	if resp, _ := call(t, a, "DELETE", "/api/v1/crew/tokens/"+id.String(), ""); resp.StatusCode != 204 || f.revoked != id {
		t.Fatalf("revoke: %d", resp.StatusCode)
	}
	if resp, body := call(t, a, "GET", "/api/v1/crew", ""); resp.StatusCode != 200 || !strings.Contains(body, `"agents":[]`) {
		t.Fatalf("status: %d %s", resp.StatusCode, body)
	}
}

func TestTokensNeedPermissions(t *testing.T) {
	f := &tokens{}
	a := app(admin([]string{permission.CrewTokensRead}), func(a *fiber.App) { crewhttp.New(f, f).Register(a) })
	if resp, _ := call(t, a, "GET", "/api/v1/crew/tokens", ""); resp.StatusCode != 200 {
		t.Fatalf("read: %d", resp.StatusCode)
	}
	if resp, _ := call(t, a, "POST", "/api/v1/crew/tokens", `{}`); resp.StatusCode != 403 {
		t.Fatalf("create without write: %d", resp.StatusCode)
	}
}

func TestAttachment(t *testing.T) {
	s := &ships{file: ship.AttachmentFile{Attachment: ship.Attachment{Kind: ship.AttachmentImage, MIME: "image/png"}, Data: []byte("png")}}
	agentID := identity.NewAgentID()
	p := &httpx.Principal{Organization: org, Role: httpx.RoleCrew, Agent: agentID}
	a := app(p, func(a *fiber.App) { a.Get("/crew/attachments/:id", crewhttp.Attachment{Ships: s}.Handle) })
	id := identity.NewAttachmentID()
	resp, body := call(t, a, "GET", "/crew/attachments/"+id.String(), "")
	if resp.StatusCode != 200 || body != "png" || resp.Header.Get("Content-Type") != "image/png" ||
		s.open.ID != id || s.open.Agent == nil || *s.open.Agent != agentID {
		t.Fatalf("image: %d %q %v %+v", resp.StatusCode, body, resp.Header, s.open)
	}
	s.file.Kind, s.file.Name = ship.AttachmentDocument, "a.txt"
	s.file.MIME = "application/octet-stream"
	if resp, _ := call(t, a, "GET", "/crew/attachments/"+id.String(), ""); !strings.Contains(resp.Header.Get("Content-Disposition"), "a.txt") {
		t.Fatalf("document: %v", resp.Header)
	}
	if resp, _ := call(t, a, "GET", "/crew/attachments/x", ""); resp.StatusCode != 400 {
		t.Fatalf("bad id: %d", resp.StatusCode)
	}
	s.file = ship.AttachmentFile{}
	if resp, _ := call(t, a, "GET", "/crew/attachments/"+id.String(), ""); resp.StatusCode != 404 {
		t.Fatalf("missing: %d", resp.StatusCode)
	}
	open := app(&httpx.Principal{Organization: org, Role: httpx.RoleCrew}, func(a *fiber.App) { a.Get("/crew/attachments/:id", crewhttp.Attachment{Ships: s}.Handle) })
	call(t, open, "GET", "/crew/attachments/"+id.String(), "")
	if s.open.Agent != nil {
		t.Fatal("the open mode's owner reads any attachment: no agent filter")
	}
}

func TestKit(t *testing.T) {
	ag := agents{}
	k := crewhttp.Kit{Kits: ag, Agents: ag}
	agentID := identity.NewAgentID()
	own := app(&httpx.Principal{Organization: org, Role: httpx.RoleCrew, Agent: agentID, AgentName: "coder"}, func(a *fiber.App) { a.Get("/crew/profile", k.Handle) })
	resp, body := call(t, own, "GET", "/crew/profile", "")
	if resp.StatusCode != 200 || resp.Header.Get("Cache-Control") != "no-store" || !strings.Contains(body, agentID.String()) {
		t.Fatalf("own kit: %d %v %s", resp.StatusCode, resp.Header, body)
	}
	if resp, _ := call(t, own, "GET", "/crew/profile?agent=other", ""); resp.StatusCode != 403 {
		t.Fatalf("another agent's kit: %d", resp.StatusCode)
	}
	owner := app(&httpx.Principal{Organization: org, Role: httpx.RoleCrew}, func(a *fiber.App) { a.Get("/crew/profile", k.Handle) })
	if resp, _ := call(t, owner, "GET", "/crew/profile", ""); resp.StatusCode != 400 {
		t.Fatalf("open mode without a name: %d", resp.StatusCode)
	}
	if resp, _ := call(t, owner, "GET", "/crew/profile?agent=coder", ""); resp.StatusCode != 200 {
		t.Fatalf("open mode with a name: %d", resp.StatusCode)
	}
	none := app(nil, func(a *fiber.App) { a.Get("/crew/profile", k.Handle) })
	if resp, _ := call(t, none, "GET", "/crew/profile", ""); resp.StatusCode != 401 {
		t.Fatalf("no principal: %d", resp.StatusCode)
	}
}

func TestReply(t *testing.T) {
	s := &ships{}
	agentID := identity.NewAgentID()
	mount := func(a *fiber.App) { a.Post("/crew/reply", crewhttp.Reply{Ships: s}.Handle) }
	a := app(&httpx.Principal{Organization: org, Role: httpx.RoleCrew, Agent: agentID}, mount)
	conv := identity.NewConversationID()
	resp, body := call(t, a, "POST", "/crew/reply", `{"conversation_id":"`+conv.String()+`","text":"hi"}`)
	if resp.StatusCode != 201 || !strings.Contains(body, `"status":"pending"`) || s.sent.Conversation != conv || s.sent.Agent == nil || *s.sent.Agent != agentID {
		t.Fatalf("reply: %d %s %+v", resp.StatusCode, body, s.sent)
	}
	if resp, _ := call(t, a, "POST", "/crew/reply", `{"conversation_id":"`+conv.String()+`"}`); resp.StatusCode != 400 {
		t.Fatalf("empty reply: %d", resp.StatusCode)
	}
	req := httptest.NewRequest("POST", "/crew/reply", strings.NewReader(`{}`))
	req.Header.Set("Content-Type", "text/plain")
	if resp, _ := a.Test(req); resp.StatusCode != 415 {
		t.Fatalf("content type: %d", resp.StatusCode)
	}
	owner := app(&httpx.Principal{Organization: org, Role: httpx.RoleCrew}, mount)
	if resp, _ := call(t, owner, "POST", "/crew/reply", `{}`); resp.StatusCode != 403 {
		t.Fatalf("reply without agent token: %d", resp.StatusCode)
	}
}
