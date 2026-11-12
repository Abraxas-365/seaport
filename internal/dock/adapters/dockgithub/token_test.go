package dockgithub

import (
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Abraxas-365/seaport/internal/httpx"
	"github.com/Abraxas-365/seaport/internal/identity"
	"github.com/gofiber/fiber/v3"
)

func tokenApp(p *httpx.Principal) *fiber.App {
	app := fiber.New(fiber.Config{ErrorHandler: httpx.ErrorHandler, Immutable: true})
	app.Use(func(c fiber.Ctx) error {
		if p != nil {
			httpx.SetPrincipal(c, *p)
		}
		return c.Next()
	})
	app.Post("/crew/github/token", NewHandler(nil, nil).Token())
	return app
}

func postToken(t *testing.T, app *fiber.App, contentType, body string) int {
	t.Helper()
	req := httptest.NewRequest("POST", "/crew/github/token", strings.NewReader(body))
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	resp, err := app.Test(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	return resp.StatusCode
}

func TestTokenGuards(t *testing.T) {
	org := identity.MustParseOrganizationID("00000000-0000-4000-8000-000000000001")
	if got := postToken(t, tokenApp(nil), "application/json", `{}`); got != 401 {
		t.Fatalf("no principal: %d", got)
	}
	admin := &httpx.Principal{Organization: org, Role: httpx.RoleAdmin}
	if got := postToken(t, tokenApp(admin), "application/json", `{}`); got != 401 {
		t.Fatalf("admin principal: %d", got)
	}
	owner := &httpx.Principal{Organization: org, Role: httpx.RoleCrew}
	if got := postToken(t, tokenApp(owner), "application/json", `{}`); got != 403 {
		t.Fatalf("crew without agent: %d", got)
	}
	agent := &httpx.Principal{Organization: org, Role: httpx.RoleCrew, Agent: identity.NewAgentID()}
	if got := postToken(t, tokenApp(agent), "text/plain", `{}`); got != 415 {
		t.Fatalf("wrong content type: %d", got)
	}
}
