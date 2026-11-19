package hostinghttp_test

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Abraxas-365/seaport/internal/errx"
	"github.com/Abraxas-365/seaport/internal/hosting"
	"github.com/Abraxas-365/seaport/internal/hosting/adapters/hostinghttp"
	"github.com/Abraxas-365/seaport/internal/httpx"
	"github.com/Abraxas-365/seaport/internal/identity"
	"github.com/Abraxas-365/seaport/internal/permission"
	"github.com/gofiber/fiber/v3"
)

var org = identity.MustParseOrganizationID("00000000-0000-4000-8000-000000000001")

type fake struct {
	hosting.Commands
	hosting.Queries
	upload   hosting.UploadHome
	build    hosting.BuildAgent
	built    identity.AgentID
	slot     hosting.SlotAction
	host     hosting.Host
	hostOf   identity.AgentID
	home     []byte
	homePath string
	days     int
}

func (f *fake) Host(_ context.Context, _ identity.OrganizationID, id identity.AgentID, in hosting.Host) (hosting.Hosted, error) {
	f.hostOf, f.host = id, in
	return hosting.Hosted{Agent: id}, nil
}

func (f *fake) UploadHome(_ context.Context, _ identity.OrganizationID, in hosting.UploadHome) (hosting.Home, error) {
	f.upload = in
	return hosting.Home{Digest: "sha256:abc", Harness: in.Harness}, nil
}

func (f *fake) StartBuild(_ context.Context, _ identity.OrganizationID, id identity.AgentID, in hosting.BuildAgent) (hosting.Build, error) {
	f.built, f.build = id, in
	return hosting.Build{}, nil
}

func (f *fake) Build(_ context.Context, _ identity.OrganizationID, id identity.AgentID) (hosting.Build, error) {
	if id != f.built {
		return hosting.Build{}, errx.NotFound("build not found")
	}
	return hosting.Build{Digest: "d"}, nil
}

func (f *fake) StartSlot(_ context.Context, _ identity.OrganizationID, in hosting.SlotAction) (hosting.Hosted, error) {
	f.slot = in
	return hosting.Hosted{}, nil
}

func (f *fake) OpenWorkspace(_ context.Context, _ identity.OrganizationID, _ identity.AgentID, slot int) (hosting.Workspace, error) {
	return hosting.Workspace{}, nil
}

func (f *fake) Home(_ context.Context, _ identity.OrganizationID, digest string) (hosting.Home, error) {
	return hosting.Home{Digest: digest}, nil
}

func (f *fake) HomeArchive(context.Context, identity.OrganizationID, string) ([]byte, error) {
	return []byte("tar"), nil
}

func (f *fake) HomeFile(_ context.Context, _ identity.OrganizationID, _, path string) ([]byte, error) {
	f.homePath = path
	return f.home, nil
}

func (f *fake) Usage(_ context.Context, _ identity.OrganizationID, _ identity.AgentID, in hosting.UsageQuery) ([]hosting.UsageDay, error) {
	f.days = in.Days
	return []hosting.UsageDay{}, nil
}

func newApp(f *fake, p httpx.Principal) *fiber.App {
	app := fiber.New(fiber.Config{ErrorHandler: httpx.ErrorHandler, Immutable: true})
	app.Use(func(c fiber.Ctx) error {
		httpx.SetPrincipal(c, p)
		return c.Next()
	})
	h := hostinghttp.New(f, f, true)
	h.Register(app)
	app.Get("/crew/workspace", h.Workspace())
	return app
}

func admin(perms ...string) httpx.Principal {
	return httpx.Principal{Organization: org, Role: httpx.RoleAdmin, Subject: "t", Permissions: perms}
}

func send(t *testing.T, app *fiber.App, req *http.Request) (*http.Response, string) {
	t.Helper()
	resp, err := app.Test(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp, string(b)
}

func call(t *testing.T, app *fiber.App, method, path, body string) (*http.Response, string) {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	return send(t, app, req)
}

func TestHomeUploadReadsARawBody(t *testing.T) {
	f := &fake{}
	app := newApp(f, admin(permission.All...))
	tarball := bytes.Repeat([]byte{1}, 1024)
	req := httptest.NewRequest("POST", "/api/v1/homes?harness=claude-code", bytes.NewReader(tarball))
	req.Header.Set("Content-Type", "application/x-tar")
	resp, _ := send(t, app, req)
	if resp.StatusCode != 201 || resp.Header.Get("Location") == "" || f.upload.Harness != "claude-code" || !bytes.Equal(f.upload.Archive, tarball) || f.upload.CreatedBy != "t" {
		t.Fatalf("upload: %d %v %+v", resp.StatusCode, resp.Header, f.upload.Harness)
	}
	req = httptest.NewRequest("POST", "/api/v1/homes?harness=claude-code", strings.NewReader("x"))
	req.Header.Set("Content-Type", "application/json")
	if resp, _ := send(t, app, req); resp.StatusCode != 415 {
		t.Fatalf("wrong type: %d", resp.StatusCode)
	}
	req = httptest.NewRequest("POST", "/api/v1/homes?harness=claude-code", nil)
	req.Header.Set("Content-Type", "application/gzip")
	if resp, _ := send(t, app, req); resp.StatusCode != 400 {
		t.Fatalf("empty: %d", resp.StatusCode)
	}
}

func TestHomeRoutes(t *testing.T) {
	f := &fake{home: []byte("hola")}
	app := newApp(f, admin(permission.All...))
	if resp, body := call(t, app, "GET", "/api/v1/homes/rules", ""); resp.StatusCode != 200 || !strings.Contains(body, "claude-code") {
		t.Fatalf("rules: %d %s", resp.StatusCode, body)
	}
	if resp, body := call(t, app, "GET", "/api/v1/homes/sha256:abc", ""); resp.StatusCode != 200 || !strings.Contains(body, "sha256:abc") {
		t.Fatalf("home: %d %s", resp.StatusCode, body)
	}
	if resp, body := call(t, app, "GET", "/api/v1/homes/sha256:abc/archive", ""); resp.StatusCode != 200 || body != "tar" || resp.Header.Get("Content-Type") != "application/x-tar" {
		t.Fatalf("archive: %d %q", resp.StatusCode, body)
	}
	resp, body := call(t, app, "GET", "/api/v1/homes/sha256:abc/files/skills/my%20skill/SKILL.md", "")
	if resp.StatusCode != 200 || body != "hola" || f.homePath != "skills/my skill/SKILL.md" || !strings.HasPrefix(resp.Header.Get("Content-Type"), "text/plain") {
		t.Fatalf("file: %d %q %q %v", resp.StatusCode, body, f.homePath, resp.Header)
	}
	f.home = []byte{0xff, 0xfe}
	if resp, _ := call(t, app, "GET", "/api/v1/homes/sha256:abc/files/x", ""); resp.Header.Get("Content-Type") != "application/octet-stream" {
		t.Fatalf("binary file: %v", resp.Header)
	}
}

// The agent's build: POST starts it (body optional), GET reads it.
func TestAgentBuild(t *testing.T) {
	f := &fake{}
	app := newApp(f, admin(permission.All...))
	id := identity.NewAgentID()
	if resp, _ := call(t, app, "POST", "/api/v1/agents/"+id.String()+"/build", ""); resp.StatusCode != 202 || f.built != id || f.build.Force {
		t.Fatalf("build: %d %+v", resp.StatusCode, f.build)
	}
	if resp, _ := call(t, app, "POST", "/api/v1/agents/"+id.String()+"/build", `{"force":true}`); resp.StatusCode != 202 || !f.build.Force {
		t.Fatalf("force: %d %+v", resp.StatusCode, f.build)
	}
	if resp, body := call(t, app, "GET", "/api/v1/agents/"+id.String()+"/build", ""); resp.StatusCode != 200 || !strings.Contains(body, `"d"`) {
		t.Fatalf("read: %d %s", resp.StatusCode, body)
	}
	if resp, _ := call(t, app, "GET", "/api/v1/agents/"+identity.NewAgentID().String()+"/build", ""); resp.StatusCode != 404 {
		t.Fatalf("never built: %d", resp.StatusCode)
	}
	if resp, _ := call(t, app, "POST", "/api/v1/agents/nope/build", ""); resp.StatusCode != 400 {
		t.Fatalf("bad id: %d", resp.StatusCode)
	}
}

// Hosting takes only a slot count (the machine is the agent's config);
// the body is optional.
func TestHost(t *testing.T) {
	f := &fake{}
	app := newApp(f, admin(permission.All...))
	id := identity.NewAgentID()
	if resp, _ := call(t, app, "PUT", "/api/v1/agents/"+id.String()+"/hosting", `{"slots":2}`); resp.StatusCode != 200 || f.hostOf != id || f.host.Slots != 2 || f.host.By != "t" {
		t.Fatalf("host: %d %+v", resp.StatusCode, f.host)
	}
	if resp, _ := call(t, app, "PUT", "/api/v1/agents/"+id.String()+"/hosting", ""); resp.StatusCode != 200 || f.host.Slots != 0 {
		t.Fatalf("host without body: %d %+v", resp.StatusCode, f.host)
	}
	if resp, _ := call(t, app, "PUT", "/api/v1/agents/"+id.String()+"/hosting", `{"environment":"prod"}`); resp.StatusCode != 400 {
		t.Fatalf("an environment is no longer taken: %d", resp.StatusCode)
	}
}

func TestSlotsAndUsage(t *testing.T) {
	f := &fake{}
	app := newApp(f, admin(permission.All...))
	agent := identity.NewAgentID()
	if resp, _ := call(t, app, "POST", "/api/v1/agents/"+agent.String()+"/hosting/slots/2/start", ""); resp.StatusCode != 202 || f.slot.Agent != agent || f.slot.Index != 2 {
		t.Fatalf("start: %d %+v", resp.StatusCode, f.slot)
	}
	if resp, _ := call(t, app, "POST", "/api/v1/agents/"+agent.String()+"/hosting/slots/x/start", ""); resp.StatusCode != 400 {
		t.Fatalf("bad slot: %d", resp.StatusCode)
	}
	if resp, _ := call(t, app, "GET", "/api/v1/agents/"+agent.String()+"/usage?days=7", ""); resp.StatusCode != 200 || f.days != 7 {
		t.Fatalf("usage: %d days=%d", resp.StatusCode, f.days)
	}
	if resp, _ := call(t, app, "GET", "/api/v1/agents/"+agent.String()+"/usage?days=x", ""); resp.StatusCode != 400 {
		t.Fatalf("usage bad days: %d", resp.StatusCode)
	}
}

func TestWorkspaceIsForSlotTokens(t *testing.T) {
	f := &fake{}
	crewP := httpx.Principal{Organization: org, Role: httpx.RoleCrew, Agent: identity.NewAgentID()}
	if resp, _ := call(t, newApp(f, crewP), "GET", "/crew/workspace", ""); resp.StatusCode != 403 {
		t.Fatalf("agent token: %d", resp.StatusCode)
	}
	crewP.Slot = 1
	if resp, _ := call(t, newApp(f, crewP), "GET", "/crew/workspace", ""); resp.StatusCode != 200 {
		t.Fatalf("slot token: %d", resp.StatusCode)
	}
}

func TestPermissions(t *testing.T) {
	app := newApp(&fake{}, admin(permission.HostingRead))
	agent := identity.NewAgentID().String()
	for _, c := range [][2]string{
		{"POST", "/api/v1/homes"},
		{"PUT", "/api/v1/agents/" + agent + "/hosting"},
		{"POST", "/api/v1/agents/" + agent + "/build"},
		{"POST", "/api/v1/agents/" + agent + "/hosting/slots/1/start"},
		{"GET", "/api/v1/agents/" + agent + "/hosting/slots/1/logs"},
		{"GET", "/api/v1/agents/" + agent + "/usage"},
		{"GET", "/api/v1/homes/rules"},
	} {
		if resp, _ := call(t, app, c[0], c[1], ""); resp.StatusCode != 403 {
			t.Fatalf("%s %s: %d", c[0], c[1], resp.StatusCode)
		}
	}
}
