package agenthttp_test

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Abraxas-365/seaport/internal/agent"
	"github.com/Abraxas-365/seaport/internal/agent/adapters/agenthttp"
	"github.com/Abraxas-365/seaport/internal/httpx"
	"github.com/Abraxas-365/seaport/internal/identity"
	"github.com/Abraxas-365/seaport/internal/permission"
	"github.com/Abraxas-365/seaport/internal/query"
	"github.com/gofiber/fiber/v3"
)

var org = identity.MustParseOrganizationID("00000000-0000-4000-8000-000000000001")

type fake struct {
	agent.Commands
	agent.Queries
	assign  agent.Assign
	secret  agent.SetSecret
	deleted string
	version int
	saved   agent.SaveProfile
}

func (f *fake) Create(_ context.Context, _ identity.OrganizationID, in agent.Create) (agent.Agent, error) {
	return agent.Agent{ID: identity.NewAgentID(), Name: in.Name}, nil
}

func (f *fake) Assign(_ context.Context, _ identity.OrganizationID, in agent.Assign) (agent.Agent, error) {
	f.assign = in
	return agent.Agent{ID: in.Agent}, nil
}

func (f *fake) SaveProfile(_ context.Context, _ identity.OrganizationID, _ identity.AgentID, in agent.SaveProfile) (agent.Profile, error) {
	f.saved = in
	return agent.Profile{Version: in.Base + 1}, nil
}

func (f *fake) SetSecret(_ context.Context, _ identity.OrganizationID, _ identity.AgentID, in agent.SetSecret) (agent.Secret, error) {
	f.secret = in
	return agent.Secret{Name: in.Name}, nil
}

func (f *fake) DeleteSecret(_ context.Context, _ identity.OrganizationID, _ identity.AgentID, name string) error {
	f.deleted = name
	return nil
}

func (f *fake) Profile(_ context.Context, _ identity.OrganizationID, _ identity.AgentID, version int) (agent.Profile, error) {
	f.version = version
	return agent.Profile{Version: version}, nil
}

func (f *fake) List(context.Context, identity.OrganizationID, query.Pagination) (query.Paginated[agent.Agent], error) {
	return query.Paginated[agent.Agent]{Items: []agent.Agent{{Name: "coder"}}}, nil
}

func newApp(f *fake, perms []string) *fiber.App {
	app := fiber.New(fiber.Config{ErrorHandler: httpx.ErrorHandler, Immutable: true})
	app.Use(func(c fiber.Ctx) error {
		httpx.SetPrincipal(c, httpx.Principal{Organization: org, Role: httpx.RoleAdmin, Subject: "t", Name: "Tester", Permissions: perms})
		return c.Next()
	})
	agenthttp.New(f, f).Register(app)
	return app
}

func call(t *testing.T, app *fiber.App, method, path, body string) (*http.Response, string) {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := app.Test(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp, string(b)
}

func TestCreateAndList(t *testing.T) {
	app := newApp(&fake{}, permission.All)
	resp, body := call(t, app, "POST", "/api/v1/agents", `{"name":"coder"}`)
	if resp.StatusCode != 201 || resp.Header.Get("Location") == "" || !strings.Contains(body, `"coder"`) {
		t.Fatalf("create: %d %q %s", resp.StatusCode, resp.Header.Get("Location"), body)
	}
	if resp, body := call(t, app, "GET", "/api/v1/agents", ""); resp.StatusCode != 200 || !strings.Contains(body, "coder") {
		t.Fatalf("list: %d %s", resp.StatusCode, body)
	}
}

func TestAssignBodyIsOptional(t *testing.T) {
	f := &fake{}
	app := newApp(f, permission.All)
	dock, id := identity.NewDockID(), identity.NewAgentID()
	path := "/api/v1/docks/" + dock.String() + "/agents/" + id.String()
	if resp, _ := call(t, app, "PUT", path, ""); resp.StatusCode != 200 || f.assign.Default || f.assign.Dock != dock || f.assign.Agent != id {
		t.Fatalf("assign without body: %d %+v", resp.StatusCode, f.assign)
	}
	if resp, _ := call(t, app, "PUT", path, `{"default":true}`); resp.StatusCode != 200 || !f.assign.Default {
		t.Fatalf("assign default: %d %+v", resp.StatusCode, f.assign)
	}
	if resp, _ := call(t, app, "PUT", path, `{"nope":1}`); resp.StatusCode != 400 {
		t.Fatalf("assign unknown field: %d", resp.StatusCode)
	}
}

func TestProfileVersionsAndSave(t *testing.T) {
	f := &fake{}
	app := newApp(f, permission.All)
	id := identity.NewAgentID().String()
	if resp, _ := call(t, app, "GET", "/api/v1/agents/"+id+"/profile", ""); resp.StatusCode != 200 || f.version != 0 {
		t.Fatalf("current profile: %d v%d", resp.StatusCode, f.version)
	}
	if resp, _ := call(t, app, "GET", "/api/v1/agents/"+id+"/profile/versions/3", ""); resp.StatusCode != 200 || f.version != 3 {
		t.Fatalf("version 3: %d v%d", resp.StatusCode, f.version)
	}
	if resp, _ := call(t, app, "GET", "/api/v1/agents/"+id+"/profile/versions/x", ""); resp.StatusCode != 400 {
		t.Fatalf("bad version: %d", resp.StatusCode)
	}
	if resp, body := call(t, app, "PUT", "/api/v1/agents/"+id+"/profile", `{"base":2,"instructions":"hi"}`); resp.StatusCode != 200 ||
		f.saved.CreatedBy != "Tester" || !strings.Contains(body, `"version":3`) {
		t.Fatalf("save: %d %+v %s", resp.StatusCode, f.saved, body)
	}
}

func TestSecretNameIsDecodedFromThePath(t *testing.T) {
	f := &fake{}
	app := newApp(f, permission.All)
	id := identity.NewAgentID().String()
	if resp, _ := call(t, app, "PUT", "/api/v1/agents/"+id+"/secrets/GH_TOKEN", `{"value":"v","env":true}`); resp.StatusCode != 200 ||
		f.secret.Name != "GH_TOKEN" || !f.secret.Env {
		t.Fatalf("set: %d %+v", resp.StatusCode, f.secret)
	}
	if resp, _ := call(t, app, "DELETE", "/api/v1/agents/"+id+"/secrets/GH%5FTOKEN", ""); resp.StatusCode != 204 || f.deleted != "GH_TOKEN" {
		t.Fatalf("delete: %d %q", resp.StatusCode, f.deleted)
	}
}

func TestWritesNeedAgentsWrite(t *testing.T) {
	app := newApp(&fake{}, []string{permission.AgentsRead})
	if resp, _ := call(t, app, "GET", "/api/v1/agents", ""); resp.StatusCode != 200 {
		t.Fatalf("read: %d", resp.StatusCode)
	}
	id := identity.NewAgentID().String()
	for _, c := range [][2]string{{"POST", "/api/v1/agents"}, {"PUT", "/api/v1/agents/" + id + "/secrets/X"}, {"DELETE", "/api/v1/agents/" + id}} {
		if resp, _ := call(t, app, c[0], c[1], `{}`); resp.StatusCode != 403 {
			t.Fatalf("%s %s without write: %d", c[0], c[1], resp.StatusCode)
		}
	}
}
