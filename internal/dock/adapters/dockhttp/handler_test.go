package dockhttp_test

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Abraxas-365/seaport/internal/dock"
	"github.com/Abraxas-365/seaport/internal/dock/adapters/dockhttp"
	"github.com/Abraxas-365/seaport/internal/errx"
	"github.com/Abraxas-365/seaport/internal/httpx"
	"github.com/Abraxas-365/seaport/internal/identity"
	"github.com/Abraxas-365/seaport/internal/permission"
	"github.com/Abraxas-365/seaport/internal/query"
	"github.com/gofiber/fiber/v3"
)

var org = identity.MustParseOrganizationID("00000000-0000-4000-8000-000000000001")

type fake struct {
	dock.Commands
	dock.Queries
	created dock.Create
	deleted identity.DockID
	name    *string
}

func (f *fake) Create(_ context.Context, _ identity.OrganizationID, in dock.Create) (dock.Dock, error) {
	f.created = in
	return dock.Dock{ID: identity.NewDockID(), Name: in.Name}, nil
}

func (f *fake) Update(_ context.Context, _ identity.OrganizationID, id identity.DockID, in dock.Update) (dock.Dock, error) {
	f.name = in.Name
	return dock.Dock{ID: id}, nil
}

func (f *fake) Delete(_ context.Context, _ identity.OrganizationID, id identity.DockID) error {
	f.deleted = id
	return nil
}

func (f *fake) Find(_ context.Context, _ identity.OrganizationID, id identity.DockID) (dock.Dock, error) {
	return dock.Dock{}, errx.NotFound("dock not found")
}

func (f *fake) List(context.Context, identity.OrganizationID, query.Pagination) (query.Paginated[dock.Dock], error) {
	return query.Paginated[dock.Dock]{Items: []dock.Dock{{Name: "one"}}}, nil
}

func newApp(f *fake, perms []string) *fiber.App {
	app := fiber.New(fiber.Config{ErrorHandler: httpx.ErrorHandler, Immutable: true})
	app.Use(func(c fiber.Ctx) error {
		httpx.SetPrincipal(c, httpx.Principal{Organization: org, Role: httpx.RoleAdmin, Subject: "t", Permissions: perms})
		return c.Next()
	})
	dockhttp.New(f, f).Register(app)
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

func TestListFindUpdateDelete(t *testing.T) {
	f := &fake{}
	app := newApp(f, permission.All)
	if resp, body := call(t, app, "GET", "/api/v1/docks", ""); resp.StatusCode != 200 || !strings.Contains(body, `"one"`) {
		t.Fatalf("list: %d %s", resp.StatusCode, body)
	}
	id := identity.NewDockID().String()
	if resp, _ := call(t, app, "GET", "/api/v1/docks/"+id, ""); resp.StatusCode != 404 {
		t.Fatalf("find unknown: %d", resp.StatusCode)
	}
	if resp, _ := call(t, app, "GET", "/api/v1/docks/not-an-id", ""); resp.StatusCode != 400 {
		t.Fatalf("find bad id: %d", resp.StatusCode)
	}
	if resp, _ := call(t, app, "PATCH", "/api/v1/docks/"+id, `{"name":"x"}`); resp.StatusCode != 200 || f.name == nil || *f.name != "x" {
		t.Fatalf("update: %d", resp.StatusCode)
	}
	if resp, _ := call(t, app, "DELETE", "/api/v1/docks/"+id, ""); resp.StatusCode != 204 || f.deleted.String() != id {
		t.Fatalf("delete: %d", resp.StatusCode)
	}
}

func TestCreateValidatesBody(t *testing.T) {
	f := &fake{}
	app := newApp(f, permission.All)
	if resp, _ := call(t, app, "POST", "/api/v1/docks", `{"nope":1}`); resp.StatusCode != 400 {
		t.Fatalf("unknown field: %d", resp.StatusCode)
	}
	if resp, _ := call(t, app, "POST", "/api/v1/docks", `{"name":"a","channel":"api"}`); resp.StatusCode != 201 || f.created.Name != "a" {
		t.Fatalf("create: %d", resp.StatusCode)
	}
}

func TestWritesNeedDocksWrite(t *testing.T) {
	app := newApp(&fake{}, []string{permission.DocksRead})
	if resp, _ := call(t, app, "GET", "/api/v1/docks", ""); resp.StatusCode != 200 {
		t.Fatalf("read: %d", resp.StatusCode)
	}
	if resp, _ := call(t, app, "DELETE", "/api/v1/docks/"+identity.NewDockID().String(), ""); resp.StatusCode != 403 {
		t.Fatalf("delete without write: %d", resp.StatusCode)
	}
}
