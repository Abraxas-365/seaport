package bootstrap_test

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Abraxas-365/seaport/internal/bootstrap"
	"github.com/Abraxas-365/seaport/internal/config"
	"github.com/Abraxas-365/seaport/internal/connector/acptest"
	"github.com/Abraxas-365/seaport/internal/crew"
)

// uiContext is what the Playwright specs (web/e2e) read from the file named
// by SEAPORT_UI_CONTEXT: where the port is, how to talk to its API for the
// setup the UI can't do, and the fake Slack and the echo agent's gate.
type uiContext struct {
	URL   string `json:"url"`
	Token string `json:"token"`
	Agent struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	} `json:"agent"`
	Slack struct {
		// URL is the fake Slack; PostsURL answers what the port posted there.
		URL           string `json:"url"`
		PostsURL      string `json:"posts_url"`
		SigningSecret string `json:"signing_secret"`
		BotToken      string `json:"bot_token"`
	} `json:"slack"`
	// Gate is the file that lets a "hold:" turn of the echo agent finish.
	Gate string `json:"gate"`
}

// lineLog writes what a process prints to the test log, line by line.
type lineLog struct{ t *testing.T }

func (l lineLog) Write(p []byte) (int, error) {
	sc := bufio.NewScanner(bytes.NewReader(p))
	sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
	for sc.Scan() {
		l.t.Log(sc.Text())
	}
	return len(p), nil
}

// TestUI drives the admin web app in a real browser (Playwright, web/e2e)
// against the real server with the embedded build, a fake Slack and the echo
// agent connected through the real connector. Opt-in (SEAPORT_TEST_UI=1,
// even in CI) since it needs Node, Chromium and web/dist (`cd web && pnpm
// build`). With CI set, a missing Node or browser fails instead of skipping.
func TestUI(t *testing.T) {
	if os.Getenv("SEAPORT_TEST_UI") != "1" {
		t.Skip("set SEAPORT_TEST_UI=1 to run the browser tests (needs Node, Chromium and `cd web && pnpm build`)")
	}
	ci := os.Getenv("CI") != ""
	missing := func(format string, args ...any) {
		t.Helper()
		if ci {
			t.Fatalf(format, args...)
		}
		t.Skipf(format, args...)
	}
	npx, err := exec.LookPath("npx")
	if err != nil {
		missing("npx not found: install Node to run the browser tests")
	}
	webDir, err := filepath.Abs(filepath.Join("..", "..", "web"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(webDir, "dist", "index.html")); err != nil {
		t.Fatal("web/dist/index.html is missing: run `cd web && pnpm build` first (the port embeds the build)")
	}
	if _, err := os.Stat(filepath.Join(webDir, "node_modules", ".bin", "playwright")); err != nil {
		t.Fatal("Playwright is not installed: run `cd web && pnpm install --frozen-lockfile`")
	}

	// The fake Slack, with a debug route of its own (never of the port) that
	// answers what the port posted.
	sl := newFakeSlack(t)
	front := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/__fake/posts" {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(sl.posted())
			return
		}
		sl.srv.Config.Handler.ServeHTTP(w, r)
	}))
	t.Cleanup(front.Close)

	p := startPortWith(t, func(c *config.Server) {
		c.Auth = config.AuthToken
		c.SecretKey = "ui-e2e-secret-key-0123456789abcdef"
		c.SlackURL, c.SlackAPIURL = front.URL, front.URL+"/api"
		c.BlobURL = config.LocalBlobURL(filepath.Join(t.TempDir(), "blobs"))
	}, bootstrap.Options{})

	// The echo agent works as "test" with a profile already (v1), so the
	// specs can change it and watch the connection go outdated.
	tester := p.agent(testAgent)
	if _, code := p.saveProfile(tester.ID, map[string]any{"base": 0, "instructions": "You are the echo agent."}); code != http.StatusOK {
		t.Fatalf("save profile: %d", code)
	}
	h := p.startConnector(connectorOpts{})
	p.ready(func(a crew.Agent) bool { return a.ProfileVersion == 1 && !a.Outdated })

	var uc uiContext
	uc.URL, uc.Token = p.srv.URL, adminToken
	uc.Agent.ID, uc.Agent.Name = tester.ID.String(), tester.Name
	uc.Slack.URL, uc.Slack.PostsURL = front.URL, front.URL+"/__fake/posts"
	uc.Slack.SigningSecret, uc.Slack.BotToken = slSigning, slBotToken
	uc.Gate = acptest.GatePath(h.log)
	ctxFile := filepath.Join(t.TempDir(), "ui-context.json")
	raw, _ := json.Marshal(uc)
	if err := os.WriteFile(ctxFile, raw, 0o600); err != nil {
		t.Fatal(err)
	}

	cmd := exec.Command(npx, "playwright", "test")
	cmd.Dir = filepath.Join(webDir, "e2e")
	cmd.Env = append(os.Environ(), "SEAPORT_UI_URL="+p.srv.URL, "SEAPORT_UI_TOKEN="+adminToken, "SEAPORT_UI_CONTEXT="+ctxFile)
	var out bytes.Buffer
	w := io.MultiWriter(lineLog{t}, &out)
	cmd.Stdout, cmd.Stderr = w, w
	if err := cmd.Run(); err != nil {
		if !ci && strings.Contains(out.String(), "Executable doesn't exist") {
			t.Skip("Chromium is not installed: run `cd web && npx playwright install chromium`")
		}
		t.Fatalf("playwright failed: %v (traces of failures are in web/e2e/test-results)", err)
	}
}
