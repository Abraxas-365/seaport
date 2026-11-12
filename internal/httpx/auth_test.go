package httpx_test

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Abraxas-365/seaport/internal/errx"
	"github.com/Abraxas-365/seaport/internal/httpx"
	"github.com/Abraxas-365/seaport/internal/identity"
	"github.com/Abraxas-365/seaport/internal/permission"
	"github.com/gofiber/fiber/v3"
)

var testOrg = identity.MustParseOrganizationID("00000000-0000-4000-8000-000000000001")

func newApp() *fiber.App {
	return fiber.New(fiber.Config{ErrorHandler: httpx.ErrorHandler, Immutable: true})
}

func do(t *testing.T, app *fiber.App, req *http.Request) (*http.Response, string) {
	t.Helper()
	resp, err := app.Test(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp, string(b)
}

func TestRequire(t *testing.T) {
	var current *httpx.Principal
	app := newApp()
	app.Use(func(c fiber.Ctx) error {
		if current != nil {
			httpx.SetPrincipal(c, *current)
		}
		return c.Next()
	})
	app.Post("/api/v1/docks", httpx.Require(permission.DocksWrite, func(c fiber.Ctx) error { return c.SendStatus(http.StatusTeapot) }))
	cases := []struct {
		name string
		p    *httpx.Principal
		want int
	}{
		{"no principal", nil, http.StatusUnauthorized},
		{"admin without it", &httpx.Principal{Organization: testOrg, Role: httpx.RoleAdmin, Permissions: []string{permission.DocksRead}}, http.StatusForbidden},
		{"crew with it", &httpx.Principal{Organization: testOrg, Role: httpx.RoleCrew, Permissions: permission.All}, http.StatusForbidden},
		{"admin with it", &httpx.Principal{Organization: testOrg, Role: httpx.RoleAdmin, Permissions: []string{permission.DocksWrite}}, http.StatusTeapot},
	}
	for _, c := range cases {
		current = c.p
		resp, _ := do(t, app, httptest.NewRequest(http.MethodPost, "/api/v1/docks", nil))
		if resp.StatusCode != c.want {
			t.Errorf("%s: %d, want %d", c.name, resp.StatusCode, c.want)
		}
	}
}

// who answers the principal the authenticator set, read back from the
// request context as every handler (and the net/http fallback) reads it.
func who(c fiber.Ctx) error {
	p, ok := httpx.PrincipalFrom(c.Context())
	if !ok {
		return errx.Unauthorized("no principal")
	}
	return c.SendString(string(p.Role) + ":" + p.Subject)
}

func TestTokensAdminAuth(t *testing.T) {
	tokens := httpx.Tokens{Organization: testOrg, Sessions: httpx.Sessions{Token: "admin-token-0123456789", TTL: 3600e9}}
	app := newApp()
	session := tokens.Session()
	app.Post("/api/v1/session", session.Login)
	app.Get("/api/v1/session", session.Current)
	app.Use("/api/", tokens.Admin())
	app.Get("/api/v1/who", who)
	app.Post("/api/v1/who", who)
	app.Delete("/api/v1/session", session.Logout)

	get := func(method, path string, hdr map[string]string, body string) (*http.Response, string) {
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		for k, v := range hdr {
			req.Header.Set(k, v)
		}
		return do(t, app, req)
	}
	// No credentials, a wrong bearer, a wrong cookie: 401 with the error body.
	for name, hdr := range map[string]map[string]string{
		"none": nil, "bad bearer": {"Authorization": "Bearer nope"},
		"bad cookie": {"Cookie": "seaport_session=1.abc"},
	} {
		resp, body := get("GET", "/api/v1/who", hdr, "")
		if resp.StatusCode != 401 || !strings.Contains(body, `"error"`) || resp.Header.Get("WWW-Authenticate") != "Bearer" {
			t.Errorf("%s: %d %s", name, resp.StatusCode, body)
		}
	}
	if resp, body := get("GET", "/api/v1/who", map[string]string{"Authorization": "Bearer admin-token-0123456789"}, ""); resp.StatusCode != 200 || body != "admin:admin" {
		t.Errorf("bearer: %d %s", resp.StatusCode, body)
	}

	// Login sets the cookie (HttpOnly, Strict, Max-Age); wrong token is 401.
	if resp, _ := get("POST", "/api/v1/session", map[string]string{"Content-Type": "application/json"}, `{"token":"wrong"}`); resp.StatusCode != 401 {
		t.Errorf("wrong token: %d", resp.StatusCode)
	}
	resp, body := get("POST", "/api/v1/session", map[string]string{"Content-Type": "application/json"}, `{"token":"admin-token-0123456789"}`)
	if resp.StatusCode != 200 || !strings.Contains(body, `"authenticated":true`) {
		t.Fatalf("login: %d %s", resp.StatusCode, body)
	}
	set := resp.Header.Get("Set-Cookie")
	for _, want := range []string{"seaport_session=", "HttpOnly", "SameSite=Strict", "path=/", "max-age=3600"} {
		if !strings.Contains(strings.ToLower(set), strings.ToLower(want)) {
			t.Errorf("Set-Cookie %q lacks %q", set, want)
		}
	}
	cookie := strings.SplitN(set, ";", 2)[0]
	if resp, body := get("GET", "/api/v1/who", map[string]string{"Cookie": cookie}, ""); resp.StatusCode != 200 || body != "admin:admin" {
		t.Errorf("cookie: %d %s", resp.StatusCode, body)
	}
	if _, body := get("GET", "/api/v1/session", map[string]string{"Cookie": cookie}, ""); !strings.Contains(body, `"authenticated":true`) {
		t.Errorf("current with cookie: %s", body)
	}
	if resp, body := get("GET", "/api/v1/session", nil, ""); resp.StatusCode != 200 || !strings.Contains(body, `"authenticated":false`) {
		t.Errorf("current without: %d %s", resp.StatusCode, body)
	}
	// Cookie writes from another origin are CSRF-rejected; a bearer is not
	// a browser credential and passes.
	cross := map[string]string{"Cookie": cookie, "Sec-Fetch-Site": "cross-site", "Origin": "https://evil.example"}
	if resp, body := get("POST", "/api/v1/who", cross, ""); resp.StatusCode != 403 || !strings.Contains(body, "cross-origin request rejected") {
		t.Errorf("cross-origin cookie write: %d %s", resp.StatusCode, body)
	}
	same := map[string]string{"Cookie": cookie, "Sec-Fetch-Site": "same-origin"}
	if resp, _ := get("POST", "/api/v1/who", same, ""); resp.StatusCode != 200 {
		t.Errorf("same-origin cookie write: %d", resp.StatusCode)
	}
	cross["Authorization"] = "Bearer admin-token-0123456789"
	delete(cross, "Cookie")
	if resp, _ := get("POST", "/api/v1/who", cross, ""); resp.StatusCode != 200 {
		t.Errorf("bearer from another origin: %d", resp.StatusCode)
	}
	// Logout clears the cookie.
	resp, _ = get("DELETE", "/api/v1/session", map[string]string{"Cookie": cookie}, "")
	if resp.StatusCode != 204 || !strings.Contains(strings.ToLower(resp.Header.Get("Set-Cookie")), "max-age=0") && !strings.Contains(strings.ToLower(resp.Header.Get("Set-Cookie")), "expires=") {
		t.Errorf("logout: %d %q", resp.StatusCode, resp.Header.Get("Set-Cookie"))
	}
}

type crewTokens map[string]httpx.Principal

func (c crewTokens) ResolveCrew(_ context.Context, v string) (httpx.Principal, error) {
	if p, ok := c[v]; ok {
		return p, nil
	}
	return httpx.Principal{}, errx.Unauthorized("missing or invalid token")
}

func TestCrewAuthAndAPIKey(t *testing.T) {
	tokens := crewTokens{"sp_crew_ok": {Organization: testOrg, Role: httpx.RoleCrew, Subject: "bot"}}
	app := newApp()
	app.Get("/crew/who", httpx.CrewAuth(tokens), who)
	app.Get("/gateway/x", httpx.APIKeyAsBearer(), httpx.CrewAuth(tokens), who)
	for _, c := range []struct {
		path string
		hdr  map[string]string
		want int
	}{
		{"/crew/who", nil, 401},
		{"/crew/who", map[string]string{"Authorization": "Bearer sp_crew_no"}, 401},
		{"/crew/who", map[string]string{"Authorization": "Bearer sp_crew_ok"}, 200},
		{"/crew/who", map[string]string{"X-Api-Key": "sp_crew_ok"}, 401}, // only the gateway reads it
		{"/gateway/x", map[string]string{"X-Api-Key": "sp_crew_ok"}, 200},
		{"/gateway/x", map[string]string{"X-Api-Key": "sp_crew_no"}, 401},
	} {
		req := httptest.NewRequest("GET", c.path, nil)
		for k, v := range c.hdr {
			req.Header.Set(k, v)
		}
		if resp, body := do(t, app, req); resp.StatusCode != c.want {
			t.Errorf("%s %v: %d %s, want %d", c.path, c.hdr, resp.StatusCode, body, c.want)
		}
	}
}

func TestOpenAuth(t *testing.T) {
	open := httpx.Open{Organization: testOrg}
	app := newApp()
	app.Use("/api/", open.Admin())
	app.Get("/api/v1/who", who)
	app.Post("/api/v1/who", who)
	app.Get("/api/v1/session", open.Session().Current)
	for _, c := range []struct {
		name, method, host string
		hdr                map[string]string
		want               int
	}{
		{"localhost", "GET", "localhost:8740", nil, 200},
		{"loopback ip", "GET", "127.0.0.1:8740", nil, 200},
		{"foreign host (rebinding)", "GET", "evil.example:8740", nil, 403},
		{"forwarded", "GET", "localhost:8740", map[string]string{"X-Forwarded-For": "203.0.113.9"}, 403},
		{"cross-origin write", "POST", "localhost:8740", map[string]string{"Sec-Fetch-Site": "cross-site"}, 403},
		{"same-origin write", "POST", "localhost:8740", map[string]string{"Sec-Fetch-Site": "same-origin"}, 200},
	} {
		req := httptest.NewRequest(c.method, "http://"+c.host+"/api/v1/who", nil)
		for k, v := range c.hdr {
			req.Header.Set(k, v)
		}
		if resp, body := do(t, app, req); resp.StatusCode != c.want {
			t.Errorf("%s: %d %s, want %d", c.name, resp.StatusCode, body, c.want)
		}
	}
	// The session state also refuses a foreign host: a rebinding page must
	// not learn the port is open.
	req := httptest.NewRequest("GET", "http://evil.example/api/v1/session", nil)
	if resp, _ := do(t, app, req); resp.StatusCode != 403 {
		t.Errorf("session from a foreign host: %d", resp.StatusCode)
	}
}
