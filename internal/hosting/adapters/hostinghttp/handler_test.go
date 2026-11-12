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
	build    hosting.BuildEnvironment
	built    identity.EnvironmentID
	slot     hosting.SlotAction
	created  hosting.CreateEnvironment
	home     []byte
	homePath string
	days     int
	byName   string
}

func (f *fake) CreateEnvironment(_ context.Context, _ identity.OrganizationID, in hosting.CreateEnvironment) (hosting.Environment, error) {
	f.created = in
	return hosting.Environment{ID: identity.NewEnvironmentID(), Name: in.Name}, nil
}

func (f *fake) UploadHome(_ context.Context, _ identity.OrganizationID, in hosting.UploadHome) (hosting.Home, error) {
	f.upload = in
	return hosting.Home{Digest: "sha256:abc", Harness: in.Harness}, nil
}

func (f *fake) StartBuild(_ context.Context, _ identity.OrganizationID, id identity.EnvironmentID, in hosting.BuildEnvironment) (hosting.Build, error) {
	f.built, f.build = id, in
	return hosting.Build{}, nil
}

func (f *fake) StartSlot(_ context.Context, _ identity.OrganizationID, in hosting.SlotAction) (hosting.Hosted, error) {
	f.slot = in
	return hosting.Hosted{}, nil
}

func (f *fake) OpenWorkspace(_ context.Context, _ identity.OrganizationID, _ identity.AgentID, slot int) (hosting.Workspace, error) {
	return hosting.Workspace{}, nil
}

func (f *fake) EnvironmentByName(_ context.Context, _ identity.OrganizationID, name string) (hosting.Environment, error) {
	f.byName = name
	if name != "prod" {
		return hosting.Environment{}, errx.NotFound("environment not found")
	}
	return hosting.Environment{ID: identity.NewEnvironmentID(), Name: name}, nil
}

func (f *fake) Environment(_ context.Context, _ identity.OrganizationID, id identity.EnvironmentID, v int) (hosting.Environment, error) {
	return hosting.Environment{ID: id}, nil
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

func TestEnvironmentByIDOrName(t *testing.T) {
	f := &fake{}
	app := newApp(f, admin(permission.All...))
	if resp, _ := call(t, app, "POST", "/api/v1/environments/prod/build", ""); resp.StatusCode != 202 || f.byName != "prod" || f.built.IsZero() {
		t.Fatalf("build by name: %d", resp.StatusCode)
	}
	id := identity.NewEnvironmentID()
	if resp, _ := call(t, app, "POST", "/api/v1/environments/"+id.String()+"/build", `{"version":2,"force":true}`); resp.StatusCode != 202 || f.built != id || f.build.Version != 2 || !f.build.Force {
		t.Fatalf("build by id: %d %+v", resp.StatusCode, f.build)
	}
	if resp, _ := call(t, app, "POST", "/api/v1/environments/nope/build", ""); resp.StatusCode != 404 {
		t.Fatalf("unknown: %d", resp.StatusCode)
	}
	if resp, _ := call(t, app, "GET", "/api/v1/environments/"+id.String()+"/versions/0", ""); resp.StatusCode != 400 {
		t.Fatalf("bad version: %d", resp.StatusCode)
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

func TestSpecSecretsNeedAgentsWrite(t *testing.T) {
	body := `{"name":"e","spec":{"harness":"claude-code","models":{"anthropic":"${KEY}"}}}`
	f := &fake{}
	app := newApp(f, admin(permission.EnvironmentsWrite))
	resp, rb := call(t, app, "POST", "/api/v1/environments", body)
	if resp.StatusCode != 403 || !strings.Contains(rb, permission.AgentsWrite) {
		t.Fatalf("without agents:write: %d %s", resp.StatusCode, rb)
	}
	app = newApp(f, admin(permission.EnvironmentsWrite, permission.AgentsWrite))
	if resp, rb := call(t, app, "POST", "/api/v1/environments", body); resp.StatusCode != 201 || f.created.CreatedBy != "t" {
		t.Fatalf("with agents:write: %d %s", resp.StatusCode, rb)
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
	app := newApp(&fake{}, admin(permission.EnvironmentsRead))
	agent := identity.NewAgentID().String()
	for _, c := range [][2]string{
		{"POST", "/api/v1/homes"},
		{"POST", "/api/v1/agents/" + agent + "/hosting/slots/1/start"},
		{"GET", "/api/v1/agents/" + agent + "/hosting/slots/1/logs"},
		{"GET", "/api/v1/agents/" + agent + "/usage"},
	} {
		if resp, _ := call(t, app, c[0], c[1], ""); resp.StatusCode != 403 {
			t.Fatalf("%s %s: %d", c[0], c[1], resp.StatusCode)
		}
	}
}
