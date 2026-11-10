package connector

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/Abraxas-365/seaport/internal/errx"
	"github.com/Abraxas-365/seaport/internal/hosting"
	"github.com/Abraxas-365/seaport/internal/identity"
)

// HostedConfig is a hosted slot's run (`seaport agent-run`, the base
// images' entrypoint): everything comes from the container's environment
// (hosting.Env*) and the port.
type HostedConfig struct {
	Server, Token, Slot, Harness string
	// Home, Workspace and State are the container's paths (tests move
	// them).
	Home, Workspace, State string
	// Self is this binary (git's credential helper).
	Self string
	// ClaudeManaged is where Claude Code's managed settings go
	// (ClaudeManaged; tests move it).
	ClaudeManaged string
}

// HostedFromEnv reads a slot container's environment.
func HostedFromEnv() (HostedConfig, error) {
	self, err := os.Executable()
	if err != nil {
		return HostedConfig{}, errx.Wrap(err, "find the seaport binary", errx.TypeInternal)
	}
	cfg := HostedConfig{
		Server: strings.TrimRight(os.Getenv(hosting.EnvURL), "/"), Token: os.Getenv(hosting.EnvToken),
		Slot: os.Getenv(hosting.EnvSlot), Harness: os.Getenv(hosting.EnvHarness),
		Home: os.Getenv("HOME"), Workspace: hosting.WorkspaceDir, State: hosting.StateDir, Self: self,
		ClaudeManaged: ClaudeManaged,
	}
	if cfg.Server == "" || cfg.Token == "" {
		return cfg, errx.Validation("agent-run runs in a hosted slot: " + hosting.EnvURL + " and " + hosting.EnvToken + " are required")
	}
	if cfg.Home == "" {
		cfg.Home = "/home/agent"
	}
	return cfg, nil
}

// Hosted runs a hosted slot until ctx ends: it reads the slot's workspace
// plan from the port, links the harness's state into the state volume,
// clones or fetches the repos, then works as the agent (Run) with each
// conversation in a worktree of its own. A refused token (the slot was
// stopped) ends it without error, so the container isn't restarted.
func Hosted(ctx context.Context, cfg HostedConfig) error {
	ws, err := fetchWorkspaceRetrying(ctx, cfg)
	if err != nil {
		if errx.IsType(err, errx.TypeAuthorization) {
			slog.Warn("the port refused this slot's token; stopping", "err", err)
			return nil
		}
		return err
	}
	if ws.Harness != "" {
		cfg.Harness = ws.Harness
	}
	h, err := harnessFor(cfg.Harness, ws.RepoConfig)
	if err != nil {
		return err
	}
	if err := linkState(cfg); err != nil {
		return err
	}
	if cfg.Harness == hosting.HarnessClaudeCode && cfg.ClaudeManaged != "" {
		if err := pinClaudeSettings(cfg.ClaudeManaged); err != nil {
			return err
		}
	}
	if err := configureGit(ctx, cfg, ws); err != nil {
		return err
	}
	for _, r := range ws.Repos {
		if err := prepareRepo(ctx, cfg, r); err != nil {
			return err
		}
	}
	var reload <-chan struct{}
	if ws.SignIn {
		if err := waitSignedIn(ctx, cfg); err != nil {
			return nil // ctx ended
		}
		reload = watchSignIns(ctx, cfg)
	}
	relay, err := startRelay(ctx, cfg.Server)
	if err != nil {
		return err
	}
	trees := &worktrees{root: cfg.Workspace, repos: ws.Repos}
	err = Run(ctx, Config{
		Reload: reload,
		Server: cfg.Server, Token: cfg.Token, AgentName: cfg.Slot, Concurrency: 2, Deliver: DeliverSteer,
		Mode: h.mode, Cwd: cfg.Workspace, Command: h.command, SessionMeta: h.meta, Workdir: trees.dir, MCPURL: relay + "/mcp",
		SessionsFile: filepath.Join(cfg.State, "connector", "sessions.json"),
		Protect:      protected(),
	})
	if errx.IsType(err, errx.TypeAuthorization) {
		slog.Warn("the port refused this slot's token; stopping", "err", err)
		return nil
	}
	return err
}

// relayed are the port's paths the harness reaches through the relay.
var relayed = []string{"/mcp"}

// slotSet are the variables the port sets on a slot for its network (the
// model gateway and the egress proxy).
var slotSet = []string{hosting.EnvGateway, "ANTHROPIC_BASE_URL", "ANTHROPIC_API_KEY",
	"HTTPS_PROXY", "HTTP_PROXY", "https_proxy", "http_proxy", "NO_PROXY", "no_proxy"}

// protected are the slot's network variables the port set: the agent's
// kit can't point the harness elsewhere (an ANTHROPIC_BASE_URL secret
// would take its calls around the gateway).
func protected() []string {
	var out []string
	for _, k := range slotSet {
		if _, ok := os.LookupEnv(k); ok {
			out = append(out, k)
		}
	}
	if _, ok := os.LookupEnv(hosting.EnvGateway); ok {
		out = append(out, "ANTHROPIC_AUTH_TOKEN") // would win over the slot's key
	}
	return out
}

// ClaudeManaged is where agent-run writes Claude Code's managed settings:
// the base image links /etc/claude-code (the managed settings dir, read
// over every other settings source) here, since the root fs is read-only
// and /tmp is the slot's tmpfs.
const ClaudeManaged = "/tmp/seaport/claude-code"

// pinClaudeSettings writes Claude Code's managed settings with the slot's
// network variables, so no settings file (the image's, a trusted repo's,
// or one written by mistake) points it around the gateway or the egress
// proxy: managed env wins over user and project env, and an empty
// apiKeyHelper and ANTHROPIC_AUTH_TOKEN keep the slot's key in use. The
// harness runs as the same user, so this guards configuration, not a
// hostile agent (it holds the slot token anyway; the gateway and the
// egress proxy are the boundary).
func pinClaudeSettings(dir string) error {
	env := map[string]string{}
	for _, k := range protected() {
		env[k] = os.Getenv(k)
	}
	settings := map[string]any{"env": env}
	if _, ok := os.LookupEnv(hosting.EnvGateway); ok {
		settings["apiKeyHelper"] = ""
	}
	raw, _ := json.MarshalIndent(settings, "", "  ")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return errx.Wrap(err, "prepare Claude Code's managed settings", errx.TypeInternal)
	}
	tmp := filepath.Join(dir, ".managed-settings.json.tmp")
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		return errx.Wrap(err, "write Claude Code's managed settings", errx.TypeInternal)
	}
	if err := os.Rename(tmp, filepath.Join(dir, "managed-settings.json")); err != nil {
		return errx.Wrap(err, "write Claude Code's managed settings", errx.TypeInternal)
	}
	return nil
}

// startRelay serves the port's MCP on loopback until ctx ends and answers
// its base URL: harnesses take plain-HTTP MCP servers only on loopback,
// and the port may be reached over plain HTTP on the host's network.
func startRelay(ctx context.Context, server string) (string, error) {
	target, err := url.Parse(server)
	if err != nil {
		return "", errx.Wrap(err, "port URL", errx.TypeValidation)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return "", errx.Wrap(err, "start the MCP relay", errx.TypeInternal)
	}
	proxy := &httputil.ReverseProxy{
		Rewrite:       func(r *httputil.ProxyRequest) { r.SetURL(target); r.Out.Host = target.Host },
		FlushInterval: -1, // streamed MCP responses
	}
	mux := http.NewServeMux()
	for _, p := range relayed {
		mux.Handle(p, proxy)
		mux.Handle(p+"/", proxy)
	}
	srv := &http.Server{Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	go func() { _ = srv.Serve(ln) }()
	go func() {
		<-ctx.Done()
		_ = srv.Close()
	}()
	return "http://" + ln.Addr().String(), nil
}

// harness is how agent-run runs a harness: its ACP command, the session
// mode the connector puts it in and the _meta of its session requests.
type harness struct {
	command []string
	mode    string
	meta    map[string]any
}

// harnessFor is the harness's run for the environment's repo_config:
// with ignore, the harness reads its own configuration (the image's home)
// but not the repos' (AGENTS.md, CLAUDE.md, .claude/ settings, commands
// and agents).
func harnessFor(name, repoConfig string) (harness, error) {
	ignore := repoConfig == hosting.RepoConfigIgnore
	switch name {
	case hosting.HarnessRness:
		h := harness{command: []string{"rness", "--acp"}}
		if ignore {
			// rness reads no project config but the instruction files
			// (its project skill dirs stay: no flag turns them off)
			h.command = append(h.command, "--instructions", "none")
		}
		return h, nil
	case hosting.HarnessClaudeCode:
		// Its default mode (auto) asks Anthropic's classifier, which the
		// gateway doesn't serve: "default" asks permission instead (the
		// profile's approve answers).
		h := harness{command: []string{"claude-agent-acp"}, mode: "default"}
		if ignore {
			// user only: no project or local settings, CLAUDE.md, skills,
			// commands or agents of the repo (managed settings still apply)
			h.meta = map[string]any{"claudeCode": map[string]any{
				"options": map[string]any{"settingSources": []string{"user"}},
			}}
		}
		return h, nil
	}
	return harness{}, errx.Validation("unknown harness " + name)
}

func fetchWorkspaceRetrying(ctx context.Context, cfg HostedConfig) (hosting.Workspace, error) {
	backoff := time.Second
	for {
		ws, err := fetchWorkspace(ctx, cfg)
		if err == nil || errx.IsType(err, errx.TypeAuthorization) || ctx.Err() != nil {
			return ws, err
		}
		slog.Warn("could not read the slot's workspace, retrying", "err", err, "in", backoff)
		select {
		case <-time.After(backoff):
		case <-ctx.Done():
			return ws, ctx.Err()
		}
		backoff = min(backoff*2, 30*time.Second)
	}
}

// fetchWorkspace reads GET /crew/workspace (repo credentials resolved:
// only this slot's token gets them).
func fetchWorkspace(ctx context.Context, cfg HostedConfig) (hosting.Workspace, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, cfg.Server+"/crew/workspace", nil)
	if err != nil {
		return hosting.Workspace{}, errx.Wrap(err, "workspace request", errx.TypeValidation)
	}
	req.Header = authHeader(cfg.Token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return hosting.Workspace{}, errx.Wrap(err, "read the slot's workspace", errx.TypeExternal)
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return hosting.Workspace{}, errx.Wrap(err, "read the slot's workspace", errx.TypeExternal)
	}
	switch {
	case resp.StatusCode == http.StatusOK:
	case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden || resp.StatusCode == http.StatusNotFound:
		return hosting.Workspace{}, errx.Unauthorized("the port refused the slot's workspace: " + portMessage(body, resp.Status))
	default:
		return hosting.Workspace{}, errx.External("read the slot's workspace: " + portMessage(body, resp.Status))
	}
	var ws hosting.Workspace
	if err := json.Unmarshal(body, &ws); err != nil {
		return ws, errx.Wrap(err, "decode the slot's workspace", errx.TypeExternal)
	}
	return ws, nil
}

// statePath is a path of the home kept in the state volume across
// restarts (the home itself is thrown away at stop).
type statePath struct {
	home, state string
	dir         bool
	// seed: copy the image's files of the dir in at each start, but the
	// top-level names in keep.
	seed bool
	keep []string
}

var harnessState = map[string][]statePath{
	hosting.HarnessRness: {
		{home: ".rness/sessions", state: "rness/sessions", dir: true},
		{home: ".rness/credentials.json", state: "rness/credentials.json"},
	},
	// Claude Code's whole config dir: it replaces (or refuses) a linked
	// credentials file but writes through a linked dir, so a sign-out
	// really signs out. What the image ships there is copied in at each
	// start, its sessions and sign-in kept.
	hosting.HarnessClaudeCode: {
		{home: ".claude", state: "claude-code/config", dir: true, seed: true,
			keep: []string{"projects", ".credentials.json"}},
		{home: ".claude.json", state: "claude-code/claude.json"},
	},
}

// linkState links the harness's sessions and sign-in into the state
// volume: a file the image ships is copied there on first use, a seeded
// dir's files at every start (but keep).
func linkState(cfg HostedConfig) error {
	for _, p := range harnessState[cfg.Harness] {
		home, state := filepath.Join(cfg.Home, p.home), filepath.Join(cfg.State, p.state)
		if err := os.MkdirAll(filepath.Dir(home), 0o755); err != nil {
			return errx.Wrap(err, "prepare the home", errx.TypeInternal)
		}
		if err := os.MkdirAll(filepath.Dir(state), 0o700); err != nil {
			return errx.Wrap(err, "prepare the state volume", errx.TypeInternal)
		}
		if p.dir {
			if err := os.MkdirAll(state, 0o700); err != nil {
				return errx.Wrap(err, "prepare the state volume", errx.TypeInternal)
			}
			if p.seed {
				if err := seedDir(home, state, p.keep); err != nil {
					return err
				}
			}
		} else if _, err := os.Stat(state); os.IsNotExist(err) {
			if raw, rerr := os.ReadFile(home); rerr == nil {
				if err := os.WriteFile(state, raw, 0o600); err != nil {
					return errx.Wrap(err, "seed the state volume", errx.TypeInternal)
				}
			}
		}
		if err := os.RemoveAll(home); err != nil {
			return errx.Wrap(err, "prepare the home", errx.TypeInternal)
		}
		if err := os.Symlink(state, home); err != nil {
			return errx.Wrap(err, "link the slot's state", errx.TypeInternal)
		}
	}
	return os.MkdirAll(filepath.Join(cfg.State, "connector"), 0o700)
}

// seedDir copies the regular files under from (the image's) into to,
// replacing what's there, except the top-level names in keep (the
// harness's own: sessions, sign-in). from missing or already a link is no
// error.
func seedDir(from, to string, keep []string) error {
	if fi, err := os.Lstat(from); err != nil || !fi.IsDir() {
		return nil
	}
	err := filepath.WalkDir(from, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(from, p)
		if rel == "." {
			return nil
		}
		if top, _, _ := strings.Cut(rel, string(filepath.Separator)); slices.Contains(keep, top) {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		dst := filepath.Join(to, rel)
		switch {
		case d.IsDir():
			return os.MkdirAll(dst, 0o700)
		case d.Type().IsRegular():
			raw, err := os.ReadFile(p)
			if err != nil {
				return err
			}
			return os.WriteFile(dst, raw, 0o600)
		}
		return nil // links and the like aren't copied
	})
	if err != nil {
		return errx.Wrap(err, "seed the state volume", errx.TypeInternal)
	}
	return nil
}

// configureGit makes this binary git's credential helper for the repos
// with a credential (it asks the port each time: no token on disk) and
// names the slot as the commit author.
func configureGit(ctx context.Context, cfg HostedConfig, ws hosting.Workspace) error {
	set := func(args ...string) error {
		return git(ctx, cfg.Home, nil, append([]string{"config", "--global"}, args...)...)
	}
	if err := set("user.name", ws.Slot); err != nil {
		return err
	}
	if err := set("user.email", ws.Slot+"@agents.seaport.invalid"); err != nil {
		return err
	}
	if err := set("init.defaultBranch", "main"); err != nil {
		return err
	}
	seen := map[string]bool{}
	for _, r := range ws.Repos {
		u, err := url.Parse(r.URL)
		if err != nil || r.Token == "" || seen[u.Host] {
			continue
		}
		seen[u.Host] = true
		prefix := "credential.https://" + u.Host
		if err := set(prefix+".helper", "!"+shellQuote(cfg.Self)+" git-credential"); err != nil {
			return err
		}
		if err := set(prefix+".useHttpPath", "true"); err != nil {
			return err
		}
	}
	return nil
}

// prepareRepo clones a repo into the workspace, or fetches it when the
// (persistent) workspace has it already, leaving the agent's work as it
// is; then runs its on_start.
func prepareRepo(ctx context.Context, cfg HostedConfig, r hosting.WorkspaceRepo) error {
	dir := filepath.Join(cfg.Workspace, r.Path)
	if _, err := os.Stat(filepath.Join(dir, ".git")); err == nil {
		if err := git(ctx, dir, nil, "fetch", "--prune", "origin"); err != nil {
			slog.Warn("could not fetch a repo; working on what the workspace has", "repo", r.URL, "err", err)
		}
	} else {
		args := []string{"clone"}
		if r.Ref != "" {
			args = append(args, "--branch", r.Ref)
		}
		if err := git(ctx, cfg.Workspace, nil, append(args, "--", r.URL, dir)...); err != nil {
			return errx.Wrap(err, "clone "+r.URL, errx.TypeExternal)
		}
	}
	if strings.TrimSpace(r.OnStart) == "" {
		return nil
	}
	cmd := exec.CommandContext(ctx, "bash", "-lc", r.OnStart)
	cmd.Dir, cmd.Stdout, cmd.Stderr = dir, os.Stderr, os.Stderr
	if err := cmd.Run(); err != nil {
		slog.Warn("a repo's on_start failed; going on", "repo", r.URL, "err", err)
	}
	return nil
}

// git runs git in dir, its output to stderr (the container's log).
func git(ctx context.Context, dir string, env []string, args ...string) error {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir, cmd.Stdout, cmd.Stderr = dir, os.Stderr, os.Stderr
	cmd.Env = append(append(os.Environ(), "GIT_TERMINAL_PROMPT=0"), env...)
	if err := cmd.Run(); err != nil {
		return errx.Wrap(err, "git "+args[0], errx.TypeExternal)
	}
	return nil
}

func shellQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }

// worktrees gives each conversation its own checkout of the slot's repos
// (<workspace>/wt/<conversation>/<path>, branch seaport/<conversation>),
// so conversations worked at once don't edit one tree. Without repos
// every conversation works in the workspace itself.
type worktrees struct {
	root  string
	repos []hosting.WorkspaceRepo
	mu    sync.Mutex // git locks a repo's worktree list
}

func (w *worktrees) dir(ctx context.Context, conv identity.ConversationID) (string, error) {
	if len(w.repos) == 0 {
		return w.root, nil
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	dir := filepath.Join(w.root, "wt", conv.String())
	for _, r := range w.repos {
		tree := filepath.Join(dir, r.Path)
		if _, err := os.Stat(filepath.Join(tree, ".git")); err == nil {
			continue
		}
		repo := filepath.Join(w.root, r.Path)
		_ = git(ctx, repo, nil, "worktree", "prune")
		if err := git(ctx, repo, nil, "worktree", "add", "-B", "seaport/"+conv.String(), tree); err != nil {
			return "", err
		}
	}
	return dir, nil
}

// GitCredential answers git's credential helper protocol (`seaport
// git-credential get`, set up by Hosted): the token of the slot's repo
// matching the request, read from the port now.
func GitCredential(ctx context.Context, op string, in io.Reader, out io.Writer) error {
	if op != "get" {
		return nil // store/erase: nothing is kept
	}
	req := map[string]string{}
	sc := bufio.NewScanner(in)
	for sc.Scan() {
		k, v, ok := strings.Cut(sc.Text(), "=")
		if ok {
			req[k] = v
		}
	}
	if req["protocol"] != "https" {
		return nil
	}
	cfg, err := HostedFromEnv()
	if err != nil {
		return err
	}
	ws, err := fetchWorkspace(ctx, cfg)
	if err != nil {
		return err
	}
	want := strings.TrimSuffix(strings.Trim(req["path"], "/"), ".git")
	for _, r := range ws.Repos {
		u, err := url.Parse(r.URL)
		if err != nil || r.Token == "" || !strings.EqualFold(u.Host, req["host"]) {
			continue
		}
		if want != "" && strings.TrimSuffix(strings.Trim(u.Path, "/"), ".git") != want {
			continue
		}
		_, err = fmt.Fprintf(out, "username=x-access-token\npassword=%s\n", r.Token)
		return err
	}
	return nil
}
