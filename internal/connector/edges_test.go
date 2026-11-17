package connector

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/Abraxas-365/seaport/internal/agent"
	"github.com/Abraxas-365/seaport/internal/crew"
	"github.com/Abraxas-365/seaport/internal/errx"
	"github.com/Abraxas-365/seaport/internal/hosting"
	"github.com/Abraxas-365/seaport/internal/identity"
	"github.com/Abraxas-365/seaport/internal/ship"
)

// fakeClaude puts a `claude` script first on PATH that prints out and
// exits with code; it records its arguments in the returned file.
func fakeClaude(t *testing.T, out string, code int) string {
	t.Helper()
	return fakeHarness(t, "claude", out, code)
}

// fakeHarness is fakeClaude for any harness binary name.
func fakeHarness(t *testing.T, name, out string, code int) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("shell script fake harness")
	}
	dir := t.TempDir()
	args := filepath.Join(dir, "args")
	script := "#!/bin/sh\necho \"$*\" >> " + args + "\ncat <<'EOF'\n" + out + "\nEOF\nexit " + strings.TrimSpace(string(rune('0'+code))) + "\n"
	if err := os.WriteFile(filepath.Join(dir, name), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return args
}

// Claude Code's status JSON is the answer even when claude exits 1
// (signed out).
func TestHarnessAccount(t *testing.T) {
	ctx := context.Background()
	if acc, err := harnessAccount(ctx, HostedConfig{Harness: "other"}, nil); err != nil || acc.Supported || acc.Method != "none" {
		t.Fatalf("other harness: %+v %v", acc, err)
	}
	cfg := HostedConfig{Harness: hosting.HarnessClaudeCode}
	fakeClaude(t, `{"loggedIn":true,"authMethod":"claude.ai","subscriptionType":"max"}`, 0)
	acc, err := harnessAccount(ctx, cfg, os.Environ())
	if err != nil || !acc.Supported || !acc.LoggedIn || acc.Method != "claude.ai" || acc.Subscription != "max" {
		t.Fatalf("signed in: %+v %v", acc, err)
	}
	fakeClaude(t, `{"loggedIn":false}`, 1)
	acc, err = harnessAccount(ctx, cfg, os.Environ())
	if err != nil || acc.LoggedIn || acc.Method != "none" {
		t.Fatalf("signed out: %+v %v", acc, err)
	}
	fakeClaude(t, `not json`, 0)
	if _, err := harnessAccount(ctx, cfg, os.Environ()); err == nil {
		t.Fatal("an unreadable status must be an error")
	}
}

// rness's account is its Anthropic credential only (a slot without the
// gateway starts on Anthropic): the key in its environment, else a usable
// sign-in; another provider's key or sign-in, a stored key or a dead
// sign-in leave it signed out.
func TestRnessAccount(t *testing.T) {
	ctx := context.Background()
	cfg := HostedConfig{Harness: hosting.HarnessRness}
	status := func(creds, env string) string {
		return `{"credentials":[` + creds + `],"env":[` + env + `],"file":"/home/agent/.rness/credentials.json"}`
	}
	signedIn := `{"name":"anthropic","api_key":false,"oauth":{"expired":true,"refreshable":true,"subscription":"max"}}`
	for _, c := range []struct {
		name, out    string
		code         int
		logged       bool
		method, plan string
		fails        bool
	}{
		{name: "signed in (a log line first)", out: "WARN something\n" + status(signedIn, `{"provider":"openrouter","env":"OPENROUTER_API_KEY","set":false}`),
			logged: true, method: "claude.ai", plan: "max"},
		{name: "a key wins over the sign-in", out: status(signedIn, `{"provider":"anthropic","env":"ANTHROPIC_API_KEY","set":true}`),
			logged: true, method: "api_key"},
		{name: "signed out", out: status(`{"name":"anthropic","api_key":false,"oauth":null}`, ""), method: "none"},
		{name: "a dead sign-in", out: status(`{"name":"anthropic","api_key":false,"oauth":{"expired":true,"refreshable":false,"subscription":""}}`, ""), method: "none"},
		{name: "a stored key", out: status(`{"name":"anthropic","api_key":true,"oauth":null}`, ""), method: "none"},
		{name: "other providers", out: status(`{"name":"openai-chatgpt","api_key":false,"oauth":{"expired":false,"refreshable":true,"subscription":"plus"}}`,
			`{"provider":"openrouter","env":"OPENROUTER_API_KEY","set":true}`), method: "none"},
		{name: "rness fails", out: "Error: startup configuration: boom", code: 1, fails: true},
		{name: "not json", out: "hello", fails: true},
	} {
		args := fakeHarness(t, "rness", c.out, c.code)
		acc, err := harnessAccount(ctx, cfg, os.Environ())
		if c.fails {
			if err == nil {
				t.Errorf("%s: want an error, got %+v", c.name, acc)
			}
			continue
		}
		if err != nil || !acc.Supported || acc.LoggedIn != c.logged || acc.Method != c.method || acc.Subscription != c.plan {
			t.Errorf("%s: %+v %v", c.name, acc, err)
		}
		if raw, _ := os.ReadFile(args); strings.TrimSpace(string(raw)) != "auth status --json" {
			t.Errorf("%s: rness ran with %q", c.name, raw)
		}
	}
}

// The slot's account answer is JSON on out, and its environment adds the
// kit's secrets but never the protected variables.
func TestAccountStatusAndKitEnv(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/crew/profile" || r.Header.Get("Authorization") != "Bearer sp_crew_slot" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(agent.Kit{Name: "pm", Env: map[string]string{"REGION": "eu-1", "HTTPS_PROXY": "http://evil.example.com"}})
	}))
	defer srv.Close()
	t.Setenv(hosting.EnvURL, srv.URL)
	t.Setenv(hosting.EnvToken, "sp_crew_slot")
	t.Setenv(hosting.EnvHarness, hosting.HarnessClaudeCode)
	t.Setenv("HTTPS_PROXY", "http://proxy.example.com")
	fakeClaude(t, `{"loggedIn":true,"authMethod":"oauth_token"}`, 0)

	env := kitEnv(context.Background(), HostedConfig{Server: srv.URL, Token: "sp_crew_slot"})
	var region, proxy []string
	for _, e := range env {
		switch {
		case strings.HasPrefix(e, "REGION="):
			region = append(region, e)
		case strings.HasPrefix(e, "HTTPS_PROXY="):
			proxy = append(proxy, e)
		}
	}
	if len(region) != 1 || len(proxy) != 1 || proxy[0] != "HTTPS_PROXY=http://proxy.example.com" {
		t.Fatalf("env: region %v proxy %v", region, proxy)
	}
	// A kit that can't be read adds nothing.
	if got := kitEnv(context.Background(), HostedConfig{Server: "http://127.0.0.1:1"}); len(got) != len(os.Environ()) {
		t.Fatalf("env without a kit: %d vs %d", len(got), len(os.Environ()))
	}
	var out bytes.Buffer
	if err := AccountStatus(context.Background(), &out); err != nil || !strings.Contains(out.String(), `"logged_in":true`) && !strings.Contains(out.String(), "oauth_token") {
		t.Fatalf("account: %q %v", out.String(), err)
	}
	t.Setenv(hosting.EnvToken, "")
	if err := AccountStatus(context.Background(), &out); err == nil {
		t.Fatal("no slot environment must be refused")
	}
}

// The sign-in mark moves with each sign-in, watchSignIns reports each new
// one, and a slot signed in already starts at once.
func TestSignInMarks(t *testing.T) {
	cfg := HostedConfig{State: t.TempDir(), Harness: hosting.HarnessClaudeCode}
	if !markTime(cfg).IsZero() {
		t.Fatal("no mark yet")
	}
	if err := markSignedIn(cfg); err != nil {
		t.Fatal(err)
	}
	first := markTime(cfg)
	if first.IsZero() {
		t.Fatal("the mark was not written")
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	reload := watchSignIns(ctx, cfg)
	// The watcher reads the mark's time when its goroutine starts, which
	// may be after the first change: keep moving the mark until it reports.
	deadline := time.After(20 * time.Second)
	for n := 1; ; n++ {
		moved := time.Now().Add(time.Duration(n) * time.Hour)
		if err := os.Chtimes(filepath.Join(cfg.State, signedInMark), moved, moved); err != nil {
			t.Fatal(err)
		}
		select {
		case <-reload:
		case <-time.After(3 * time.Second):
			continue
		case <-deadline:
			t.Fatal("a new sign-in was not reported")
		}
		break
	}
	if markTime(cfg).Equal(first) {
		t.Fatal("the mark did not move")
	}
	if err := markSignedIn(HostedConfig{State: filepath.Join(cfg.State, signedInMark)}); err == nil {
		t.Fatal("a state path that is a file can't hold the mark")
	}

	// signed in: waitSignedIn returns at once; ctx ends a wait.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(agent.Kit{Name: "pm"})
	}))
	defer srv.Close()
	cfg.Server, cfg.Token = srv.URL, "t"
	fakeClaude(t, `{"loggedIn":true,"authMethod":"claude.ai"}`, 0)
	if err := waitSignedIn(context.Background(), cfg); err != nil {
		t.Fatal(err)
	}
	fakeClaude(t, `{"loggedIn":false}`, 1)
	wctx, wcancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer wcancel()
	if err := waitSignedIn(wctx, cfg); err == nil {
		t.Fatal("a signed-out slot waits until its context ends")
	}
}

// Login runs the slot's harness's own sign-in (rness: the paste-code one)
// with the relay's stdin, marks a success for agent-run and refuses
// another harness or no slot environment.
func TestLoginRefusals(t *testing.T) {
	t.Setenv(hosting.EnvURL, "")
	if err := Login(context.Background(), strings.NewReader(""), io.Discard); err == nil {
		t.Fatal("no slot environment")
	}
	t.Setenv(hosting.EnvURL, "http://port.example.com")
	t.Setenv(hosting.EnvToken, "tok")
	t.Setenv(hosting.EnvHarness, "other")
	err := Login(context.Background(), strings.NewReader(""), io.Discard)
	if err == nil || !errx.IsType(err, errx.TypeBusiness) {
		t.Fatalf("other harness: %v", err)
	}
}

func TestLoginRunsTheHarness(t *testing.T) {
	state := t.TempDir()
	for _, c := range []struct {
		harness, bin, args string
	}{
		{hosting.HarnessRness, "rness", "auth login --provider anthropic --manual"},
		{hosting.HarnessClaudeCode, "claude", "auth login --claudeai"},
	} {
		cfg := HostedConfig{Harness: c.harness, State: state}
		_ = os.Remove(filepath.Join(state, signedInMark))
		args := fakeHarness(t, c.bin, "Login successful!", 0)
		var out bytes.Buffer
		if err := login(context.Background(), cfg, strings.NewReader("code#state\n"), &out); err != nil {
			t.Fatalf("%s: %v", c.harness, err)
		}
		if raw, _ := os.ReadFile(args); strings.TrimSpace(string(raw)) != c.args {
			t.Errorf("%s ran with %q", c.harness, raw)
		}
		if !strings.Contains(out.String(), "Login successful") || markTime(cfg).IsZero() {
			t.Errorf("%s: output %q, mark %v", c.harness, out.String(), markTime(cfg))
		}
		// a failed sign-in leaves no mark
		_ = os.Remove(filepath.Join(state, signedInMark))
		fakeHarness(t, c.bin, "Error: oauth: that code is from another sign-in", 1)
		if err := login(context.Background(), cfg, strings.NewReader("x#y\n"), io.Discard); err == nil {
			t.Errorf("%s: a failed sign-in must fail", c.harness)
		}
		if !markTime(cfg).IsZero() {
			t.Errorf("%s: a failed sign-in marked the state", c.harness)
		}
	}
}

// `seaport reply` and `seaport approval` need the slot environment, check
// their arguments before calling and report what the port answers.
func TestReplyAndApprovalCommands(t *testing.T) {
	conv := identity.NewConversationID()
	approval := identity.NewApprovalID()
	var gotReply crew.ReplyRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == crew.ReplyPath:
			raw, _ := io.ReadAll(r.Body)
			_ = json.Unmarshal(raw, &gotReply)
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(crew.ReplyAnswer{ShipID: identity.NewShipID().String(), Status: "sent"})
		case r.URL.Path == crew.ApprovalsPath+"/"+approval.String():
			_ = json.NewEncoder(w).Encode(ship.Approval{ID: approval, Status: ship.ApprovalPending})
		default:
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(`{"error":{"message":"boom"}}`))
		}
	}))
	defer srv.Close()
	ctx := context.Background()
	var out bytes.Buffer
	t.Setenv(hosting.EnvURL, "")
	t.Setenv(hosting.EnvToken, "")
	if err := ReplyCommand(ctx, []string{conv.String(), "hello"}, &out); err == nil {
		t.Fatal("reply without the slot environment")
	}
	if err := ApprovalCommand(ctx, []string{approval.String()}, &out); err == nil {
		t.Fatal("approval without the slot environment")
	}
	t.Setenv(hosting.EnvURL, srv.URL)
	t.Setenv(hosting.EnvToken, "tok")

	file := filepath.Join(t.TempDir(), "report.txt")
	if err := os.WriteFile(file, []byte("all good"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := ReplyCommand(ctx, []string{conv.String(), "-file", file, "done", "now"}, &out); err != nil {
		t.Fatal(err)
	}
	if gotReply.ConversationID != conv || gotReply.Text != "done now" || len(gotReply.Files) != 1 || gotReply.Files[0].Name != "report.txt" ||
		!strings.HasSuffix(strings.TrimSpace(out.String()), "sent") {
		t.Fatalf("reply: %+v %q", gotReply, out.String())
	}
	for _, args := range [][]string{nil, {"not-an-id"}, {conv.String(), "-file"}, {conv.String(), "-file", "/definitely/not/here"}, {conv.String()}} {
		if err := ReplyCommand(ctx, args, &out); err == nil {
			t.Errorf("reply %v must be refused", args)
		}
	}

	out.Reset()
	if err := ApprovalCommand(ctx, []string{approval.String()}, &out); err != nil || !strings.Contains(out.String(), "pending") {
		t.Fatalf("approval: %q %v", out.String(), err)
	}
	if err := ApprovalCommand(ctx, []string{approval.String(), "-wait", "0"}, &out); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{nil, {"not-an-id"}, {approval.String(), "-wait", "x"}, {approval.String(), "-wait", "-1"}, {approval.String(), "extra"}} {
		if err := ApprovalCommand(ctx, args, &out); err == nil {
			t.Errorf("approval %v must be refused", args)
		}
	}
	if err := ApprovalCommand(ctx, []string{identity.NewApprovalID().String()}, &out); err == nil {
		t.Fatal("a port error must be reported")
	}
}

// `seaport github-token` with an explicit repo prints the token; without
// one it needs a GitHub origin remote.
func TestGitHubTokenCommand(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"token":"app-token-example","repository":"acme/web","host":"github.com"}`))
	}))
	defer srv.Close()
	t.Setenv(hosting.EnvURL, srv.URL)
	t.Setenv(hosting.EnvToken, "tok")
	var out bytes.Buffer
	if err := GitHubTokenCommand(context.Background(), []string{"acme/web"}, &out); err != nil || strings.TrimSpace(out.String()) != "app-token-example" {
		t.Fatalf("token: %q %v", out.String(), err)
	}
	if err := GitHubTokenCommand(context.Background(), []string{"a", "b"}, &out); err == nil {
		t.Fatal("two arguments")
	}
	// Run in a directory with no git origin.
	t.Chdir(t.TempDir())
	if err := GitHubTokenCommand(context.Background(), nil, &out); err == nil {
		t.Fatal("no origin remote")
	}
	if got := errMessage(errx.Validation("bad")); got != "bad" {
		t.Fatalf("errMessage: %q", got)
	}
	if got := errMessage(io.EOF); got != "EOF" {
		t.Fatalf("errMessage: %q", got)
	}
}

// remoteRepo reads a repository's origin remote.
func TestRemoteRepo(t *testing.T) {
	dir := t.TempDir()
	run(t, dir, "git", "init", "-q")
	if _, _, ok := remoteRepo(context.Background(), dir); ok {
		t.Fatal("no origin yet")
	}
	run(t, dir, "git", "remote", "add", "origin", "git@github.com:acme/web.git")
	host, repo, ok := remoteRepo(context.Background(), dir)
	if !ok || host != "github.com" || repo != "acme/web" {
		t.Fatalf("remote: %q %q %v", host, repo, ok)
	}
}

// home-setup: rness keeps its require line, Claude Code installs what its
// settings enable (through `claude plugin`), anything else is refused.
func TestHomeSetup(t *testing.T) {
	ctx := context.Background()
	home := t.TempDir()
	if err := HomeSetup(ctx, "other", home, io.Discard); err == nil {
		t.Fatal("unknown harness")
	}
	if err := HomeSetup(ctx, hosting.HarnessRness, home, io.Discard); err != nil {
		t.Fatalf("rness without init.lua: %v", err)
	}
	if err := HomeSetup(ctx, hosting.HarnessClaudeCode, home, io.Discard); err != nil {
		t.Fatalf("claude without settings: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(home, ".claude"), 0o755); err != nil {
		t.Fatal(err)
	}
	settings := filepath.Join(home, ".claude", "settings.json")
	if err := os.WriteFile(settings, []byte(`{"enabledPlugins":{"tool@acme":true},"extraKnownMarketplaces":{"acme":{"source":{"source":"github","repo":"acme/plugins"}}}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	args := fakeClaude(t, "ok", 0)
	if err := HomeSetup(ctx, hosting.HarnessClaudeCode, home, io.Discard); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(args)
	if got := strings.TrimSpace(string(raw)); got != "plugin marketplace add acme/plugins\nplugin install tool@acme" {
		t.Fatalf("claude calls: %q", got)
	}
	fakeClaude(t, "first line\nit broke", 1)
	if err := installPlugins(ctx, settings, io.Discard); err == nil || !strings.Contains(err.Error(), "it broke") {
		t.Fatalf("a failed install must fail: %v", err)
	}
	if err := os.WriteFile(settings, []byte("{"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := installPlugins(ctx, settings, io.Discard); err == nil {
		t.Fatal("bad settings.json")
	}
	if got := lastLine("a\nb\n"); got != "b" {
		t.Fatalf("lastLine %q", got)
	}
	if got := lastLine(strings.Repeat("x", 400)); len([]rune(got)) != 301 {
		t.Fatalf("long line: %d", len([]rune(got)))
	}
}

// The browser's helpers: a nil browser is no engine, the CDP socket is
// read from /json/version and the engine's noise is dropped.
func TestBrowserHelpers(t *testing.T) {
	if b, err := StartBrowser(context.Background(), "", t.TempDir()); b != nil || err != nil {
		t.Fatalf("no engine: %v %v", b, err)
	}
	if _, err := StartBrowser(context.Background(), "netscape", t.TempDir()); err == nil {
		t.Fatal("unknown engine")
	}
	var nilBrowser *Browser
	nilBrowser.Stop()
	if _, _, _, ok := nilBrowser.MCPServer(); ok {
		t.Fatal("nil browser has no MCP")
	}
	b := &Browser{URL: "http://127.0.0.1:9", done: make(chan struct{})}
	if b.Done() != b.done {
		t.Fatal("Done")
	}
	b.Stop() // no process, no bridge
	if !strings.HasSuffix(browserURL(), ":9222") || basicAuth("u:p") != "dTpw" {
		t.Fatalf("browserURL %q basicAuth %q", browserURL(), basicAuth("u:p"))
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/json/version" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_, _ = w.Write([]byte(`{"webSocketDebuggerUrl":"ws://127.0.0.1:9222/devtools/browser/abc"}`))
	}))
	defer srv.Close()
	ws, err := browserSocket(context.Background(), srv.URL)
	if err != nil || ws != "ws://127.0.0.1:9222/devtools/browser/abc" {
		t.Fatalf("socket %q %v", ws, err)
	}
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(`{}`)) }))
	defer other.Close()
	if _, err := browserSocket(context.Background(), other.URL); err == nil {
		t.Fatal("no debugger URL")
	}
	if _, err := browserSocket(context.Background(), "http://127.0.0.1:1"); err == nil {
		t.Fatal("no browser")
	}
	// waitReady: ready once /json/version answers 200; exits end it.
	ready := &Browser{URL: srv.URL, done: make(chan struct{})}
	if err := ready.waitReady(context.Background()); err != nil {
		t.Fatal(err)
	}
	gone := &Browser{URL: "http://127.0.0.1:1", done: make(chan struct{})}
	close(gone.done)
	if err := gone.waitReady(context.Background()); err == nil {
		t.Fatal("an exited browser is not ready")
	}
	cctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := (&Browser{URL: "http://127.0.0.1:1", done: make(chan struct{})}).waitReady(cctx); err == nil {
		t.Fatal("a cancelled wait")
	}
	l := &browserLog{}
	if n, err := l.Write([]byte("real line\nsome dbus noise\nDevTools listening on x\npartial")); err != nil || n == 0 {
		t.Fatal("log write")
	}
	if string(l.buf) != "partial" {
		t.Fatalf("log buffer %q", l.buf)
	}
}

// browserFor starts nothing unless the kit asks and the run has an engine.
func TestBrowserForNeedsKitAndEngine(t *testing.T) {
	ctx := context.Background()
	for name, c := range map[string]struct {
		cfg Config
		kit *agent.Kit
	}{
		"no kit":            {Config{Browser: "chromium"}, nil},
		"kit without":       {Config{Browser: "chromium"}, &agent.Kit{}},
		"kit but no engine": {Config{}, &agent.Kit{Browser: true}},
	} {
		if b, err := browserFor(ctx, c.cfg, c.kit); b != nil || err != nil {
			t.Errorf("%s: %v %v", name, b, err)
		}
	}
}

// The bridge adds the slot's proxy credentials: to a plain http request
// and to a CONNECT, which it splices both ways; a refusal reaches the
// browser as it is, and an upstream that is down is a 502.
func TestProxyBridge(t *testing.T) {
	var sawAuth []string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sawAuth = append(sawAuth, r.Header.Get("Proxy-Authorization"))
		if r.Method == http.MethodConnect {
			if r.Host == "denied.example.com:443" {
				http.Error(w, "host not allowed", http.StatusForbidden)
				return
			}
			down, _, err := w.(http.Hijacker).Hijack()
			if err != nil {
				return
			}
			_, _ = io.WriteString(down, "HTTP/1.1 200 OK\r\n\r\n")
			buf := make([]byte, 4)
			_, _ = io.ReadFull(down, buf)
			_, _ = down.Write([]byte(strings.ToUpper(string(buf))))
			_ = down.Close()
			return
		}
		_, _ = w.Write([]byte("plain " + r.URL.String()))
	}))
	defer upstream.Close()

	b := &Browser{}
	addr, err := b.startBridge("http://slot:secret@" + strings.TrimPrefix(upstream.URL, "http://"))
	if err != nil {
		t.Fatal(err)
	}
	defer b.closeBridge()
	want := "Basic " + basicAuth("slot:secret")

	proxy := &http.Client{Transport: &http.Transport{Proxy: http.ProxyURL(mustURL(t, "http://"+addr))}, Timeout: 10 * time.Second}
	resp, err := proxy.Get("http://site.example.com/page")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if string(body) != "plain http://site.example.com/page" || sawAuth[0] != want {
		t.Fatalf("plain: %q auth %v", body, sawAuth)
	}

	// CONNECT by hand: send through the tunnel, get it back upper-cased.
	conn, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	_ = conn.SetDeadline(time.Now().Add(10 * time.Second))
	_, _ = io.WriteString(conn, "CONNECT site.example.com:443 HTTP/1.1\r\nHost: site.example.com:443\r\n\r\n")
	head := make([]byte, len("HTTP/1.1 200 Connection Established\r\n\r\n"))
	if _, err := io.ReadFull(conn, head); err != nil || !strings.HasPrefix(string(head), "HTTP/1.1 200") {
		t.Fatalf("connect head %q %v", head, err)
	}
	_, _ = io.WriteString(conn, "ping")
	echo := make([]byte, 4)
	if _, err := io.ReadFull(conn, echo); err != nil || string(echo) != "PING" {
		t.Fatalf("tunnel: %q %v", echo, err)
	}
	if sawAuth[len(sawAuth)-1] != want {
		t.Fatalf("connect auth: %v", sawAuth)
	}

	denied, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = denied.Close() }()
	_ = denied.SetDeadline(time.Now().Add(10 * time.Second))
	_, _ = io.WriteString(denied, "CONNECT denied.example.com:443 HTTP/1.1\r\nHost: denied.example.com:443\r\n\r\n")
	all, _ := io.ReadAll(denied)
	if !strings.Contains(string(all), "403") || !strings.Contains(string(all), "host not allowed") {
		t.Fatalf("refusal: %q", all)
	}

	// The upstream is down.
	down := &Browser{}
	daddr, err := down.startBridge("http://127.0.0.1:1")
	if err != nil {
		t.Fatal(err)
	}
	defer down.closeBridge()
	dproxy := &http.Client{Transport: &http.Transport{Proxy: http.ProxyURL(mustURL(t, "http://"+daddr))}, Timeout: 10 * time.Second}
	r2, err := dproxy.Get("http://site.example.com/")
	if err != nil || r2.StatusCode != http.StatusBadGateway {
		t.Fatalf("down upstream: %v %v", r2, err)
	}
	_ = r2.Body.Close()
	if _, err := (&Browser{}).startBridge("::not a url"); err == nil {
		t.Fatal("bad proxy URL")
	}
}

func mustURL(t *testing.T, raw string) *url.URL {
	t.Helper()
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	return u
}

// A slot's workspace is fetched again after a failure, and a refused
// token ends the wait.
func TestFetchWorkspaceRetrying(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++
		w.Header().Set("Content-Type", "application/json")
		if calls == 1 {
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		_ = json.NewEncoder(w).Encode(hosting.Workspace{Slot: "pm-1"})
	}))
	defer srv.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	ws, err := fetchWorkspaceRetrying(ctx, HostedConfig{Server: srv.URL, Token: "t"})
	if err != nil || ws.Slot != "pm-1" || calls != 2 {
		t.Fatalf("workspace %+v err %v calls %d", ws, err, calls)
	}
	refused := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusUnauthorized) }))
	defer refused.Close()
	if _, err := fetchWorkspaceRetrying(ctx, HostedConfig{Server: refused.URL, Token: "t"}); !errx.IsType(err, errx.TypeAuthorization) {
		t.Fatalf("refused: %v", err)
	}
	down, dcancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer dcancel()
	if _, err := fetchWorkspaceRetrying(down, HostedConfig{Server: "http://127.0.0.1:1", Token: "t"}); err == nil {
		t.Fatal("an unreachable port ends with the context")
	}
}

// Hosted ends cleanly when the port refuses the slot's token, and refuses
// a harness it doesn't know.
func TestHostedStopsOnARefusedToken(t *testing.T) {
	refused := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusUnauthorized) }))
	defer refused.Close()
	if err := Hosted(context.Background(), HostedConfig{Server: refused.URL, Token: "t", Harness: hosting.HarnessRness}); err != nil {
		t.Fatalf("refused token: %v", err)
	}
	srv := workspaceServer(t, hosting.Workspace{Slot: "pm-1", Harness: "emacs"})
	if err := Hosted(context.Background(), HostedConfig{Server: srv.URL, Token: "sp_crew_slot"}); err == nil {
		t.Fatal("unknown harness")
	}
}

func TestHostedFromEnv(t *testing.T) {
	t.Setenv(hosting.EnvURL, "")
	if _, err := HostedFromEnv(); err == nil {
		t.Fatal("no URL")
	}
	t.Setenv(hosting.EnvURL, "http://port.example.com/")
	t.Setenv(hosting.EnvToken, "tok")
	t.Setenv(hosting.EnvSlot, "pm-1")
	t.Setenv(hosting.EnvHarness, hosting.HarnessRness)
	cfg, err := HostedFromEnv()
	if err != nil || cfg.Server != "http://port.example.com" || cfg.Slot != "pm-1" || cfg.Harness != hosting.HarnessRness || cfg.Self == "" {
		t.Fatalf("cfg %+v err %v", cfg, err)
	}
}
