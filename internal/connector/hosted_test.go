package connector

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Abraxas-365/seaport/internal/hosting"
	"github.com/Abraxas-365/seaport/internal/identity"
)

func workspaceServer(t *testing.T, ws hosting.Workspace) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/crew/workspace" || r.Header.Get("Authorization") != "Bearer sp_crew_slot" {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"error":{"message":"revoked"}}`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(ws)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func run(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command(args[0], args[1:]...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("%v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

// git's credential helper gets the token of the repo asked for, from the
// port each time; a revoked token gets nothing.
func TestGitCredential(t *testing.T) {
	srv := workspaceServer(t, hosting.Workspace{Slot: "pm-1", Repos: []hosting.WorkspaceRepo{
		{URL: "https://github.com/acme/app.git", Path: "app", Token: "ghp_app"},
		{URL: "https://github.com/acme/lib", Path: "lib", Token: "ghp_lib"},
		{URL: "https://github.com/acme/open", Path: "open"},
	}})
	t.Setenv(hosting.EnvURL, srv.URL)
	t.Setenv(hosting.EnvToken, "sp_crew_slot")
	ask := func(in string) string {
		var out strings.Builder
		if err := GitCredential(context.Background(), "get", strings.NewReader(in), &out); err != nil {
			t.Fatalf("%q: %v", in, err)
		}
		return out.String()
	}
	if got := ask("protocol=https\nhost=github.com\npath=acme/lib.git\n"); got != "username=x-access-token\npassword=ghp_lib\n" {
		t.Fatalf("lib: %q", got)
	}
	if got := ask("protocol=https\nhost=github.com\npath=acme/app\n"); !strings.Contains(got, "ghp_app") {
		t.Fatalf("app: %q", got)
	}
	for _, in := range []string{"protocol=https\nhost=github.com\npath=acme/open.git\n", "protocol=https\nhost=gitlab.com\npath=acme/app.git\n", "protocol=http\nhost=github.com\npath=acme/app.git\n"} {
		if got := ask(in); got != "" {
			t.Fatalf("%q answered %q", in, got)
		}
	}
	t.Setenv(hosting.EnvToken, "sp_crew_revoked")
	if err := GitCredential(context.Background(), "get", strings.NewReader("protocol=https\nhost=github.com\npath=acme/app.git\n"), &strings.Builder{}); err == nil {
		t.Fatal("a revoked token got an answer")
	}
}

// The harness's state paths become links into the state volume (a file
// the image ships seeds it once); a second start keeps what's there.
func TestLinkState(t *testing.T) {
	cfg := HostedConfig{Harness: hosting.HarnessClaudeCode, Home: t.TempDir(), State: t.TempDir()}
	if err := os.WriteFile(filepath.Join(cfg.Home, ".claude.json"), []byte(`{"image":true}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := linkState(cfg); err != nil {
		t.Fatal(err)
	}
	if raw, err := os.ReadFile(filepath.Join(cfg.State, "claude-code", "claude.json")); err != nil || string(raw) != `{"image":true}` {
		t.Fatalf("seeded: %q %v", raw, err)
	}
	if err := os.WriteFile(filepath.Join(cfg.Home, ".claude", "projects", "s.jsonl"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(cfg.State, "claude-code", "projects", "s.jsonl")); err != nil {
		t.Fatalf("session not in the state volume: %v", err)
	}
	// A fresh home (the container restarted) with the image's file again.
	cfg.Home = t.TempDir()
	if err := os.WriteFile(filepath.Join(cfg.Home, ".claude.json"), []byte(`{"image":true}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cfg.State, "claude-code", "claude.json"), []byte(`{"signed":true}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := linkState(cfg); err != nil {
		t.Fatal(err)
	}
	if raw, _ := os.ReadFile(filepath.Join(cfg.Home, ".claude.json")); string(raw) != `{"signed":true}` {
		t.Fatalf("state kept: %q", raw)
	}
	if _, err := os.Stat(filepath.Join(cfg.Home, ".claude", "projects", "s.jsonl")); err != nil {
		t.Fatalf("sessions after restart: %v", err)
	}
}

// Repos are cloned once and fetched afterwards (the agent's work stays);
// each conversation gets a worktree of its own.
func TestReposAndWorktrees(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("no git")
	}
	t.Setenv("HOME", t.TempDir())
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	origin := t.TempDir()
	run(t, origin, "git", "init", "-q", "-b", "main")
	run(t, origin, "git", "-c", "user.name=t", "-c", "user.email=t@t", "commit", "-q", "--allow-empty", "-m", "first")

	ctx := context.Background()
	cfg := HostedConfig{Workspace: t.TempDir(), Home: os.Getenv("HOME"), Self: "/opt/seaport/bin/seaport"}
	repo := hosting.WorkspaceRepo{URL: origin, Path: "app", OnStart: "touch started"}
	ws := hosting.Workspace{Slot: "pm-1", Repos: []hosting.WorkspaceRepo{repo, {URL: "https://example.test/x.git", Path: "x", Token: "t"}}}
	if err := configureGit(ctx, cfg, ws); err != nil {
		t.Fatal(err)
	}
	if got := run(t, cfg.Home, "git", "config", "--global", "credential.https://example.test.helper"); got != "!'/opt/seaport/bin/seaport' git-credential" {
		t.Fatalf("helper: %q", got)
	}
	if err := prepareRepo(ctx, cfg, repo); err != nil {
		t.Fatal(err)
	}
	app := filepath.Join(cfg.Workspace, "app")
	if _, err := os.Stat(filepath.Join(app, "started")); err != nil {
		t.Fatalf("on_start: %v", err)
	}
	if err := os.WriteFile(filepath.Join(app, "work.txt"), []byte("agent's"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := prepareRepo(ctx, cfg, repo); err != nil { // the next start
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(app, "work.txt")); err != nil {
		t.Fatalf("work lost on restart: %v", err)
	}

	trees := &worktrees{root: cfg.Workspace, repos: []hosting.WorkspaceRepo{repo}}
	a, b := identity.NewConversationID(), identity.NewConversationID()
	da, err := trees.dir(ctx, a)
	if err != nil {
		t.Fatal(err)
	}
	db, err := trees.dir(ctx, b)
	if err != nil {
		t.Fatal(err)
	}
	if da == db || run(t, filepath.Join(da, "app"), "git", "branch", "--show-current") != "seaport/"+a.String() {
		t.Fatalf("worktrees: %s %s", da, db)
	}
	if again, err := trees.dir(ctx, a); err != nil || again != da {
		t.Fatalf("again: %s %v", again, err)
	}
	if none, _ := (&worktrees{root: cfg.Workspace}).dir(ctx, a); none != cfg.Workspace {
		t.Fatalf("no repos: %s", none)
	}
}

// The relay serves the port's MCP on loopback (with the caller's headers)
// and nothing else.
func TestRelay(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(r.URL.Path + " " + r.Header.Get("Authorization")))
	}))
	t.Cleanup(srv.Close)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	base, err := startRelay(ctx, srv.URL)
	if err != nil || !strings.HasPrefix(base, "http://127.0.0.1:") {
		t.Fatalf("relay: %q %v", base, err)
	}
	get := func(path string) (int, string) {
		req, _ := http.NewRequest(http.MethodGet, base+path, nil)
		req.Header.Set("Authorization", "Bearer x")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = resp.Body.Close() }()
		var b strings.Builder
		_, _ = io.Copy(&b, resp.Body)
		return resp.StatusCode, b.String()
	}
	if code, body := get("/mcp"); code != http.StatusOK || body != "/mcp Bearer x" {
		t.Fatalf("mcp: %d %q", code, body)
	}
	if code, _ := get("/api/v1/agents"); code != http.StatusNotFound {
		t.Fatalf("admin API through the relay: %d", code)
	}
}
