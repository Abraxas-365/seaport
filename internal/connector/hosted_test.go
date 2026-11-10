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
// Claude Code's config dir is linked whole: the image's files are copied
// in at each start, its sessions and sign-in kept.
func TestLinkState(t *testing.T) {
	write := func(p, v string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(v), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	read := func(p string) string {
		raw, _ := os.ReadFile(p)
		return string(raw)
	}
	cfg := HostedConfig{Harness: hosting.HarnessClaudeCode, Home: t.TempDir(), State: t.TempDir()}
	write(filepath.Join(cfg.Home, ".claude.json"), `{"image":true}`)
	write(filepath.Join(cfg.Home, ".claude", "settings.json"), `{"v":1}`)
	if err := linkState(cfg); err != nil {
		t.Fatal(err)
	}
	if got := read(filepath.Join(cfg.State, "claude-code", "claude.json")); got != `{"image":true}` {
		t.Fatalf("seeded: %q", got)
	}
	if got := read(filepath.Join(cfg.Home, ".claude", "settings.json")); got != `{"v":1}` {
		t.Fatalf("image settings: %q", got)
	}
	// The harness writes its sessions and sign-in through the link.
	write(filepath.Join(cfg.Home, ".claude", "projects", "s.jsonl"), "x")
	write(filepath.Join(cfg.Home, ".claude", ".credentials.json"), `{"oauth":1}`)
	if got := read(filepath.Join(cfg.State, "claude-code", "config", ".credentials.json")); got != `{"oauth":1}` {
		t.Fatalf("sign-in not in the state volume: %q", got)
	}
	// A fresh home (the container restarted) with the image's files again,
	// a newer settings file and an image credentials file that must lose.
	cfg.Home = t.TempDir()
	write(filepath.Join(cfg.Home, ".claude.json"), `{"image":true}`)
	write(filepath.Join(cfg.Home, ".claude", "settings.json"), `{"v":2}`)
	write(filepath.Join(cfg.Home, ".claude", ".credentials.json"), `{"image":1}`)
	write(filepath.Join(cfg.State, "claude-code", "claude.json"), `{"signed":true}`)
	if err := linkState(cfg); err != nil {
		t.Fatal(err)
	}
	if got := read(filepath.Join(cfg.Home, ".claude.json")); got != `{"signed":true}` {
		t.Fatalf("state kept: %q", got)
	}
	if got := read(filepath.Join(cfg.Home, ".claude", "projects", "s.jsonl")); got != "x" {
		t.Fatalf("sessions after restart: %q", got)
	}
	if got := read(filepath.Join(cfg.Home, ".claude", ".credentials.json")); got != `{"oauth":1}` {
		t.Fatalf("sign-in after restart: %q", got)
	}
	if got := read(filepath.Join(cfg.Home, ".claude", "settings.json")); got != `{"v":2}` {
		t.Fatalf("image settings after restart: %q", got)
	}
}

// repo_config ignore keeps the repos' configuration from the harness:
// rness reads no instruction files, Claude Code only user settings.
func TestHarnessFollowsRepoConfig(t *testing.T) {
	for _, c := range []struct {
		harness, repoConfig, command, meta string
	}{
		{hosting.HarnessRness, hosting.RepoConfigTrust, "rness --acp", "null"},
		{hosting.HarnessRness, hosting.RepoConfigIgnore, "rness --acp --instructions none", "null"},
		{hosting.HarnessClaudeCode, hosting.RepoConfigTrust, "claude-agent-acp", "null"},
		{hosting.HarnessClaudeCode, hosting.RepoConfigIgnore, "claude-agent-acp",
			`{"claudeCode":{"options":{"settingSources":["user"]}}}`},
	} {
		h, err := harnessFor(c.harness, c.repoConfig)
		if err != nil {
			t.Fatal(err)
		}
		meta, _ := json.Marshal(h.meta)
		if got := strings.Join(h.command, " "); got != c.command || string(meta) != c.meta {
			t.Errorf("%s %s: %q %s", c.harness, c.repoConfig, got, meta)
		}
		if c.harness == hosting.HarnessClaudeCode && h.mode != "default" {
			t.Errorf("claude mode = %q", h.mode)
		}
	}
	if _, err := harnessFor("codex", ""); err == nil {
		t.Fatal("unknown harness accepted")
	}
}

// Claude Code's managed settings pin the slot's network variables (the
// kit's or a settings file's ANTHROPIC_BASE_URL would go around the
// gateway) and blank the key overrides.
func TestPinClaudeSettings(t *testing.T) {
	t.Setenv(hosting.EnvGateway, "http://port/gateway")
	t.Setenv("ANTHROPIC_BASE_URL", "http://port/gateway/anthropic")
	t.Setenv("ANTHROPIC_API_KEY", "sp_crew_slot")
	t.Setenv("HTTPS_PROXY", "http://pm-1:sp_crew_slot@port:8742")
	dir := filepath.Join(t.TempDir(), "claude-code")
	if err := pinClaudeSettings(dir); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(dir, "managed-settings.json"))
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		Env          map[string]string `json:"env"`
		APIKeyHelper *string           `json:"apiKeyHelper"`
	}
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	want := map[string]string{
		hosting.EnvGateway: "http://port/gateway", "ANTHROPIC_BASE_URL": "http://port/gateway/anthropic",
		"ANTHROPIC_API_KEY": "sp_crew_slot", "HTTPS_PROXY": "http://pm-1:sp_crew_slot@port:8742",
		"ANTHROPIC_AUTH_TOKEN": "",
	}
	for k, v := range want {
		if g, ok := got.Env[k]; !ok || g != v {
			t.Errorf("env %s = %q (%v), want %q", k, g, ok, v)
		}
	}
	if got.APIKeyHelper == nil || *got.APIKeyHelper != "" {
		t.Errorf("apiKeyHelper = %v, want blank", got.APIKeyHelper)
	}
	if fi, err := os.Stat(filepath.Join(dir, "managed-settings.json")); err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %v, %v", fi.Mode(), err)
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
