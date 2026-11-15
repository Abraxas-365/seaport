package bootstrap_test

import (
	"bytes"
	"encoding/json"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Abraxas-365/seaport/internal/httpx"
)

// TestUIMembers runs web/e2e/tests/members.spec.ts: the members page in a
// real browser, against a port in iam mode with a real IAMKit. The Go side
// signs the people in (IAMKit's hosted login has no JavaScript here) and
// hands Playwright their session cookies as storage state. Needs
// SEAPORT_TEST_UI=1 and SEAPORT_TEST_IAMKIT=1.
func TestUIMembers(t *testing.T) {
	if os.Getenv("SEAPORT_TEST_UI") != "1" {
		t.Skip("set SEAPORT_TEST_UI=1 (and SEAPORT_TEST_IAMKIT=1) to run the browser tests")
	}
	npx, err := exec.LookPath("npx")
	if err != nil {
		t.Skip("npx not found: install Node to run the browser tests")
	}
	webDir, err := filepath.Abs(filepath.Join("..", "..", "web"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(webDir, "dist", "index.html")); err != nil {
		t.Fatal("web/dist/index.html is missing: run `cd web && pnpm build` first (the port embeds the build)")
	}

	p, _ := startIAMPort(t)
	org := newIAMOrg(t)
	grantSeaport(t, org)
	admin := newIAMMemberAdmin(t, org, "Admin", true)
	viewer := newIAMMemberAdmin(t, org, "Viewer", false)

	state := func(u iamUser) string {
		b := newBrowser(t)
		signIn(t, b, p, u, "", nil)
		host, _ := url.Parse(p.srv.URL)
		file := filepath.Join(t.TempDir(), "state.json")
		raw, _ := json.Marshal(map[string]any{"origins": []any{}, "cookies": []map[string]any{{
			"name": httpx.SessionCookie, "value": b.cookie(p.srv.URL, httpx.SessionCookie), "domain": host.Hostname(),
			"path": "/", "expires": -1, "httpOnly": true, "secure": false, "sameSite": "Strict",
		}}})
		if err := os.WriteFile(file, raw, 0o600); err != nil {
			t.Fatal(err)
		}
		return file
	}

	cmd := exec.Command(npx, "playwright", "test", "tests/members.spec.ts")
	cmd.Dir = filepath.Join(webDir, "e2e")
	cmd.Env = append(os.Environ(), "SEAPORT_UI_URL="+p.srv.URL,
		"SEAPORT_UI_MEMBERS_ADMIN_STATE="+state(admin), "SEAPORT_UI_MEMBERS_VIEWER_STATE="+state(viewer))
	var out bytes.Buffer
	w := io.MultiWriter(lineLog{t}, &out)
	cmd.Stdout, cmd.Stderr = w, w
	if err := cmd.Run(); err != nil {
		if strings.Contains(out.String(), "Executable doesn't exist") {
			t.Skip("Chromium is not installed: run `cd web && npx playwright install chromium`")
		}
		t.Fatalf("playwright failed: %v (traces of failures are in web/e2e/test-results)", err)
	}
}
