package httpx

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
)

func TestSPA(t *testing.T) {
	ui := fstest.MapFS{
		"index.html":       {Data: []byte("<html>app</html>")},
		"favicon.svg":      {Data: []byte("<svg/>")},
		"assets/app-1.js":  {Data: []byte("console.log(1)")},
		".gitkeep":         {Data: nil},
		"assets/style.css": {Data: []byte("body{}")},
	}
	h := SPA(ui)
	cases := []struct {
		path, body, cache string
		status            int
	}{
		{"/", "<html>app</html>", "no-cache", 200},
		{"/inbox/c-123", "<html>app</html>", "no-cache", 200},
		{"/docks", "<html>app</html>", "no-cache", 200},
		{"/favicon.svg", "<svg/>", "no-cache", 200},
		{"/assets/app-1.js", "console.log(1)", "public, max-age=31536000, immutable", 200},
		{"/assets/missing.js", "", "", 404},
		{"/.gitkeep", "", "", 404},
		{"/../../etc/passwd", "<html>app</html>", "no-cache", 200}, // cleaned; never leaves fsys
		{"/../favicon.svg", "<svg/>", "no-cache", 200},
	}
	for _, c := range cases {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, c.path, nil))
		if rec.Code != c.status {
			t.Errorf("%s: status %d, want %d", c.path, rec.Code, c.status)
			continue
		}
		if c.status != 200 {
			continue
		}
		if rec.Body.String() != c.body {
			t.Errorf("%s: body %q", c.path, rec.Body.String())
		}
		if got := rec.Header().Get("Cache-Control"); got != c.cache {
			t.Errorf("%s: Cache-Control %q, want %q", c.path, got, c.cache)
		}
	}

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	if csp := rec.Header().Get("Content-Security-Policy"); !strings.Contains(csp, "script-src 'self'") {
		t.Errorf("index CSP = %q", csp)
	}

	// Not built yet: a clear error instead of an empty page.
	rec = httptest.NewRecorder()
	SPA(fstest.MapFS{".gitkeep": {}}).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/inbox", nil))
	if rec.Code != 404 || !strings.Contains(rec.Body.String(), "not built") {
		t.Errorf("unbuilt UI: %d %s", rec.Code, rec.Body.String())
	}
}
