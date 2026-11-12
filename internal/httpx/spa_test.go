package httpx

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/gofiber/fiber/v3"
)

func spaApp(fsys fstest.MapFS, formActions ...string) *fiber.App {
	app := fiber.New(fiber.Config{ErrorHandler: ErrorHandler, CaseSensitive: true, StrictRouting: true})
	app.All("/*", SPA(fsys, formActions...))
	return app
}

func get(t *testing.T, app *fiber.App, method, path string) (*http.Response, string) {
	t.Helper()
	resp, err := app.Test(httptest.NewRequest(method, path, nil))
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	return resp, string(b)
}

func TestSPA(t *testing.T) {
	ui := fstest.MapFS{
		"index.html":       {Data: []byte("<html>app</html>")},
		"favicon.svg":      {Data: []byte("<svg/>")},
		"assets/app-1.js":  {Data: []byte("console.log(1)")},
		".gitkeep":         {Data: nil},
		"assets/style.css": {Data: []byte("body{}")},
	}
	app := spaApp(ui, "https://github.com")
	cases := []struct {
		path, body, cache, ctype string
		status                   int
	}{
		{"/", "<html>app</html>", "no-cache", "text/html", 200},
		{"/inbox/c-123", "<html>app</html>", "no-cache", "text/html", 200},
		{"/docks", "<html>app</html>", "no-cache", "text/html", 200},
		{"/favicon.svg", "<svg/>", "no-cache", "image/svg+xml", 200},
		{"/assets/app-1.js", "console.log(1)", "public, max-age=31536000, immutable", "javascript", 200},
		{"/assets/style.css", "body{}", "public, max-age=31536000, immutable", "text/css", 200},
		{"/assets/missing.js", "", "", "", 404},
		{"/.gitkeep", "", "", "", 404},
		{"/../../etc/passwd", "<html>app</html>", "no-cache", "text/html", 200}, // cleaned; never leaves fsys
		{"/%2e%2e/favicon.svg", "<svg/>", "no-cache", "image/svg+xml", 200},
	}
	for _, c := range cases {
		resp, body := get(t, app, http.MethodGet, c.path)
		if resp.StatusCode != c.status {
			t.Errorf("%s: status %d, want %d", c.path, resp.StatusCode, c.status)
			continue
		}
		if c.status != 200 {
			if !strings.Contains(body, `"NOT_FOUND"`) {
				t.Errorf("%s: body %q", c.path, body)
			}
			continue
		}
		if body != c.body {
			t.Errorf("%s: body %q", c.path, body)
		}
		if got := resp.Header.Get("Cache-Control"); got != c.cache {
			t.Errorf("%s: Cache-Control %q, want %q", c.path, got, c.cache)
		}
		if got := resp.Header.Get("Content-Type"); !strings.Contains(got, c.ctype) {
			t.Errorf("%s: Content-Type %q, want %q", c.path, got, c.ctype)
		}
	}

	resp, _ := get(t, app, http.MethodGet, "/")
	if csp := resp.Header.Get("Content-Security-Policy"); !strings.Contains(csp, "script-src 'self'") || !strings.Contains(csp, "form-action 'self' https://github.com;") {
		t.Errorf("index CSP = %q", csp)
	}
	if resp.Header.Get("X-Frame-Options") != "DENY" {
		t.Error("index without X-Frame-Options")
	}
	if resp, _ := get(t, app, http.MethodGet, "/favicon.svg"); resp.Header.Get("Content-Security-Policy") != "" {
		t.Error("CSP on a static file")
	}

	// HEAD answers the headers; anything else is a 404, not a 405 (the
	// catch-all of a router that has no other answer).
	resp, body := get(t, app, http.MethodHead, "/inbox")
	if resp.StatusCode != 200 || body != "" {
		t.Errorf("HEAD: %d %q", resp.StatusCode, body)
	}
	for _, m := range []string{http.MethodPost, http.MethodPut, http.MethodDelete} {
		if resp, _ := get(t, app, m, "/inbox"); resp.StatusCode != 404 {
			t.Errorf("%s: %d, want 404", m, resp.StatusCode)
		}
	}

	// Not built yet (go install, or before pnpm build): a clear error
	// instead of an empty page, for the root too.
	unbuilt := spaApp(fstest.MapFS{".gitkeep": {}})
	for _, p := range []string{"/inbox", "/", "/index.html"} {
		resp, body := get(t, unbuilt, http.MethodGet, p)
		if resp.StatusCode != 404 || !strings.Contains(body, "not built") {
			t.Errorf("unbuilt UI %s: %d %s", p, resp.StatusCode, body)
		}
	}
}
