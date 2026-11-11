package connector

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/Abraxas-365/seaport/internal/agent"
	"github.com/Abraxas-365/seaport/internal/crew"
	"github.com/Abraxas-365/seaport/internal/errx"
	"github.com/Abraxas-365/seaport/internal/hosting"
	"github.com/Abraxas-365/seaport/internal/identity"
	acp "github.com/coder/acp-go-sdk"
)

// Agent is an ACP agent process with one session per conversation,
// running one kit of its agent (nil: no profile, or an older port).
type Agent struct {
	cfg  Config
	kit  *agent.Kit
	cmd  *exec.Cmd
	conn *acp.ClientSideConnection
	caps acp.AgentCapabilities
	wire *wire
	done chan struct{}
	// approve answers permission requests: -approve, else the profile's,
	// else reject.
	approve string

	mu       sync.Mutex
	mcpURL   string
	sessions map[string]savedSession  // conversation id → agent session (persisted)
	open     map[string]acp.SessionId // opened on this connection
	opening  map[string]*sync.Mutex
	answers  map[acp.SessionId]*strings.Builder
	// feeds: the running turns whose activity goes to the port.
	feeds map[acp.SessionId]*feed
	// briefs: sessions whose next prompt must carry the profile's
	// instructions (agents without systemPrompt) or the news that they
	// changed; kept until a turn with it succeeds.
	briefs map[acp.SessionId]brief
	// failures counts turns failed in a row (the release backoff).
	failures int
}

// failed records a failed turn and answers how long to wait before
// releasing its delivery: 1 s, doubling, at most 30 s.
func (a *Agent) failed() time.Duration {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.failures++
	return min(time.Second<<min(a.failures-1, 5), 30*time.Second)
}

func (a *Agent) succeeded() {
	a.mu.Lock()
	a.failures = 0
	a.mu.Unlock()
}

// StartAgent spawns the agent command with the kit (its secrets in the
// environment) and runs the ACP handshake.
func StartAgent(ctx context.Context, cfg Config, kit *agent.Kit) (*Agent, error) {
	cmd := exec.Command(cfg.Command[0], cfg.Command[1:]...)
	cmd.Dir = cfg.Cwd
	cmd.Stderr = os.Stderr
	cmd.Env = os.Environ()
	// The agent's own tools (`seaport reply -file`, `seaport github-token`)
	// reach the port as the connector does.
	if os.Getenv(hosting.EnvURL) == "" && cfg.Server != "" {
		cmd.Env = append(cmd.Env, hosting.EnvURL+"="+cfg.Server)
	}
	if os.Getenv(hosting.EnvToken) == "" && cfg.Token != "" {
		cmd.Env = append(cmd.Env, hosting.EnvToken+"="+cfg.Token)
	}
	if kit != nil {
		for k, v := range kit.Env {
			if slices.Contains(cfg.Protect, k) {
				slog.Warn("the kit's env can't replace a variable the slot sets", "name", k)
				continue
			}
			cmd.Env = append(cmd.Env, k+"="+v)
		}
	}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, errx.Wrap(err, "agent stdin", errx.TypeInternal)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, errx.Wrap(err, "agent stdout", errx.TypeInternal)
	}
	if err := cmd.Start(); err != nil {
		return nil, errx.Wrap(err, "start agent "+cfg.Command[0], errx.TypeValidation)
	}
	a := &Agent{cfg: cfg, kit: kit, cmd: cmd, done: make(chan struct{}), wire: &wire{},
		sessions: map[string]savedSession{}, open: map[string]acp.SessionId{},
		opening: map[string]*sync.Mutex{}, answers: map[acp.SessionId]*strings.Builder{},
		feeds: map[acp.SessionId]*feed{}, briefs: map[acp.SessionId]brief{}}
	a.approve = cfg.Approve
	if kit != nil {
		a.wire.prompt = strings.TrimSpace(kit.Instructions)
		if a.approve == "" {
			a.approve = kit.Approve
		}
	}
	if a.approve == "" {
		a.approve = agent.ApproveReject
	}
	go func() {
		_ = cmd.Wait()
		close(a.done)
	}()
	a.conn = acp.NewClientSideConnection(&client{a: a}, injector{w: stdin, wire: a.wire}, &sniffer{r: stdout, wire: a.wire})
	a.conn.SetLogger(slog.Default().With("component", "acp"))
	a.loadSessions()

	init, err := a.conn.Initialize(ctx, acp.InitializeRequest{
		ProtocolVersion: acp.ProtocolVersionNumber,
		ClientInfo:      &acp.Implementation{Name: "seaport", Version: "0.1"},
	})
	if err != nil {
		a.Close()
		return nil, errx.Wrap(err, "ACP initialize (is the command an ACP agent? e.g. rness --acp)", errx.TypeExternal)
	}
	a.caps = init.AgentCapabilities
	slog.Info("agent ready", "command", strings.Join(cfg.Command, " "),
		"resume", a.caps.SessionCapabilities.Resume != nil, "load", a.caps.LoadSession, "mcp_http", a.caps.McpCapabilities.Http,
		"system_prompt", a.wire.Supported())
	if !a.caps.McpCapabilities.Http && !cfg.AutoReply {
		slog.Warn("agent does not support HTTP MCP servers; it can't use the reply tool. Use --auto-reply.")
	}
	if kit != nil {
		slog.Info("agent profile", "agent", kit.Name, "version", kit.Version, "skills", len(kit.Skills),
			"mcp_servers", len(kit.MCPServers), "env", len(kit.Env), "approve", a.approve)
		if !a.caps.McpCapabilities.Http && (len(kit.Skills) > 0 || len(kit.MCPServers) > 0) {
			slog.Warn("the agent can't use HTTP MCP servers: the profile's skills and MCP servers are not available to it")
		}
		if a.wire.prompt != "" && !a.wire.Supported() {
			slog.Info("the agent doesn't take a system prompt; the profile's instructions go with the first prompt of each session")
		}
	}
	return a, nil
}

// Version is the profile version it runs (0: none).
func (a *Agent) Version() int {
	if a.kit == nil {
		return 0
	}
	return a.kit.Version
}

// Revision names the kit it runs ("" without one).
func (a *Agent) Revision() string { return revision(a.kit) }

// Profiled reports whether the port served a kit (it has profiles).
func (a *Agent) Profiled() bool { return a.kit != nil }

// Done is closed when the agent process exits.
func (a *Agent) Done() <-chan struct{} { return a.done }

func (a *Agent) Close() {
	if a.cmd.Process != nil {
		_ = a.cmd.Process.Signal(os.Interrupt)
	}
}

// Stop closes the agent and waits for it to exit (killing it after 10 s).
func (a *Agent) Stop() {
	a.Close()
	select {
	case <-a.done:
	case <-time.After(10 * time.Second):
		_ = a.cmd.Process.Kill()
		<-a.done
	}
}

func (a *Agent) SetMCPURL(u string) {
	a.mu.Lock()
	a.mcpURL = u
	a.mu.Unlock()
}

// Prompt runs one turn on the conversation's session and returns the
// agent's final text; f (optional) gets the turn's session updates.
func (a *Agent) Prompt(ctx context.Context, conv identity.ConversationID, text string, images []image, f *feed) (string, error) {
	sid, err := a.sessionFor(ctx, conv)
	if err != nil {
		return "", err
	}
	buf := &strings.Builder{}
	a.mu.Lock()
	a.answers[sid] = buf
	if f != nil {
		a.feeds[sid] = f
	}
	brief, briefed := a.briefs[sid]
	if briefed {
		text = brief.text + text
	}
	a.mu.Unlock()
	defer func() {
		a.mu.Lock()
		delete(a.answers, sid)
		delete(a.feeds, sid)
		a.mu.Unlock()
	}()
	resp, err := a.conn.Prompt(ctx, acp.PromptRequest{SessionId: sid, Prompt: blocks(text, images)})
	if err != nil {
		if ctx.Err() != nil {
			_ = a.conn.Cancel(context.WithoutCancel(ctx), acp.CancelNotification{SessionId: sid})
		}
		return "", errx.Wrap(err, "agent prompt", errx.TypeExternal)
	}
	if resp.StopReason == acp.StopReasonCancelled || resp.StopReason == acp.StopReasonRefusal {
		return "", errx.External("agent turn ended: " + string(resp.StopReason))
	}
	if briefed {
		a.briefDone(sid, brief)
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	return buf.String(), nil
}

// TakesImages reports whether the agent takes images in prompts
// (promptCapabilities.image; rness refuses them otherwise).
func (a *Agent) TakesImages() bool { return a.caps.PromptCapabilities.Image }

// steerMethod is rness's ACP extension that adds input to the turn an
// in-flight session/prompt is running (advertised as
// agentCapabilities._meta.rness.steer).
const steerMethod = "_rness/steer"

// CanSteer reports whether the agent takes input mid-turn.
func (a *Agent) CanSteer() bool {
	ext, _ := a.caps.Meta["rness"].(map[string]any)
	steer, _ := ext["steer"].(bool)
	return steer
}

// Steer hands text to the turn running on the conversation's session.
// false means no turn is running there (the input was not taken).
func (a *Agent) Steer(ctx context.Context, conv identity.ConversationID, text string, images []image) (bool, error) {
	a.mu.Lock()
	sid, ok := a.open[conv.String()]
	a.mu.Unlock()
	if !ok {
		return false, nil
	}
	raw, err := a.conn.CallExtension(ctx, steerMethod, map[string]any{
		"sessionId": sid, "prompt": blocks(text, images),
	})
	if err != nil {
		return false, errx.Wrap(err, "agent steer", errx.TypeExternal)
	}
	var resp struct {
		Steered bool `json:"steered"`
	}
	if err := json.Unmarshal(raw, &resp); err != nil {
		return false, errx.Wrap(err, "decode steer response", errx.TypeExternal)
	}
	return resp.Steered, nil
}

// brief is what a session's next prompt carries before the message: the
// profile's instructions, or the news that they changed; hash names the
// instructions it hands over.
type brief struct{ text, hash string }

// briefing carries the profile's instructions in a prompt, for agents
// that don't take a system prompt.
func briefing(instructions string) string {
	return "[seaport] Your instructions (agent profile). Follow them in this conversation:\n\n" +
		instructions + "\n\n---\n\n"
}

// changedBriefing tells a resumed session its instructions changed since
// it last saw them (a system prompt sent on resume doesn't outweigh the
// conversation's history: the agent keeps following the old ones).
func changedBriefing(instructions string) string {
	if instructions == "" {
		return "[seaport] Your agent profile no longer has instructions: disregard the ones given earlier in this conversation.\n\n---\n\n"
	}
	return "[seaport] Your instructions (agent profile) changed since earlier in this conversation. From now on follow these, not the earlier ones:\n\n" +
		instructions + "\n\n---\n\n"
}

// instructionsHash names a version of the instructions ("" for none).
func instructionsHash(instructions string) string {
	if instructions == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(instructions))
	return hex.EncodeToString(sum[:8])
}

// mcpServers are seaport's MCP (tools and skills; it names the agent, so
// an open port scopes the tools to it) and the profile's.
func (a *Agent) mcpServers() []acp.McpServer {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.mcpURL == "" || !a.caps.McpCapabilities.Http {
		return []acp.McpServer{}
	}
	headers := []acp.HttpHeader{}
	if a.cfg.Token != "" {
		headers = append(headers, acp.HttpHeader{Name: "Authorization", Value: "Bearer " + a.cfg.Token})
	}
	if a.cfg.As != "" {
		headers = append(headers, acp.HttpHeader{Name: crew.AgentHeader, Value: a.cfg.As})
	}
	out := []acp.McpServer{{Http: &acp.McpServerHttpInline{
		Name: "seaport", Type: "http", Url: a.mcpURL, Headers: headers,
	}}}
	if a.kit != nil {
		for _, s := range a.kit.MCPServers {
			hs := make([]acp.HttpHeader, 0, len(s.Headers))
			for _, h := range s.Headers {
				hs = append(hs, acp.HttpHeader{Name: h.Name, Value: h.Value})
			}
			out = append(out, acp.McpServer{Http: &acp.McpServerHttpInline{Name: s.Name, Type: "http", Url: s.URL, Headers: hs}})
		}
	}
	return out
}

// sessionFor opens (resume/load) or creates the conversation's session.
func (a *Agent) sessionFor(ctx context.Context, conv identity.ConversationID) (acp.SessionId, error) {
	key := conv.String()
	a.mu.Lock()
	lock, ok := a.opening[key]
	if !ok {
		lock = &sync.Mutex{}
		a.opening[key] = lock
	}
	a.mu.Unlock()
	lock.Lock()
	defer lock.Unlock()

	a.mu.Lock()
	if sid, ok := a.open[key]; ok {
		a.mu.Unlock()
		return sid, nil
	}
	saved, hasSaved := a.sessions[key]
	a.mu.Unlock()

	servers := a.mcpServers()
	cwd := a.cfg.Cwd
	if a.cfg.Workdir != nil {
		dir, err := a.cfg.Workdir(ctx, conv)
		if err != nil {
			return "", errx.Wrap(err, "conversation working directory", errx.TypeInternal)
		}
		cwd = dir
	}
	if hasSaved {
		var err error
		var options []acp.SessionConfigOption
		var modes *acp.SessionModeState
		switch {
		case a.caps.SessionCapabilities.Resume != nil:
			var r acp.ResumeSessionResponse
			r, err = a.conn.ResumeSession(ctx, acp.ResumeSessionRequest{SessionId: saved.ID, Cwd: cwd, McpServers: servers, Meta: a.cfg.SessionMeta})
			options, modes = r.ConfigOptions, r.Modes
		case a.caps.LoadSession:
			var r acp.LoadSessionResponse
			r, err = a.conn.LoadSession(ctx, acp.LoadSessionRequest{SessionId: saved.ID, Cwd: cwd, McpServers: servers, Meta: a.cfg.SessionMeta})
			options, modes = r.ConfigOptions, r.Modes
		default:
			err = errx.Business("agent can't resume sessions")
		}
		if err == nil {
			a.remember(key, savedSession{ID: saved.ID, Instructions: saved.Instructions}, false)
			a.setMode(ctx, saved.ID, modes)
			a.configure(ctx, saved.ID, options)
			a.planBrief(saved.ID, saved.Instructions)
			slog.Info("agent session resumed", "conversation", key, "session", saved.ID)
			return saved.ID, nil
		}
		slog.Warn("could not resume agent session; starting a new one", "conversation", key, "err", err)
	}
	resp, err := a.conn.NewSession(ctx, acp.NewSessionRequest{Cwd: cwd, McpServers: servers, Meta: a.cfg.SessionMeta})
	if err != nil {
		return "", errx.Wrap(err, "agent new session", errx.TypeExternal)
	}
	fresh := savedSession{ID: resp.SessionId}
	if a.kit != nil && (a.wire.prompt == "" || a.wire.Supported()) {
		// the system prompt (or nothing) hands the instructions over
		h := instructionsHash(a.wire.prompt)
		fresh.Instructions = &h
	}
	a.remember(key, fresh, true)
	a.setMode(ctx, resp.SessionId, resp.Modes)
	a.configure(ctx, resp.SessionId, resp.ConfigOptions)
	if fresh.Instructions == nil && a.kit != nil {
		a.mu.Lock()
		a.briefs[resp.SessionId] = brief{text: briefing(a.wire.prompt), hash: instructionsHash(a.wire.prompt)}
		a.mu.Unlock()
	}
	slog.Info("agent session created", "conversation", key, "session", resp.SessionId)
	return resp.SessionId, nil
}

// planBrief decides what a resumed session's next prompt carries, from
// the instructions it last saw (seen; nil: unknown, e.g. a sessions file
// of an older connector): nothing when they're current, the news when
// they changed, all of them when it had none or may not have seen them.
func (a *Agent) planBrief(sid acp.SessionId, seen *string) {
	if a.kit == nil {
		return
	}
	cur := instructionsHash(a.wire.prompt)
	var b brief
	switch {
	case seen != nil && *seen == cur:
		return
	case seen != nil && *seen != "":
		b = brief{text: changedBriefing(a.wire.prompt), hash: cur}
	case cur != "":
		// it had none, or may never have seen them (a system prompt sent
		// on resume doesn't outweigh the history)
		b = brief{text: briefing(a.wire.prompt), hash: cur}
	default:
		a.briefDone(sid, brief{hash: cur})
		return
	}
	a.mu.Lock()
	a.briefs[sid] = b
	a.mu.Unlock()
}

// briefDone records that the session has seen the instructions b hands
// over (a turn carrying it succeeded), so it isn't sent again.
func (a *Agent) briefDone(sid acp.SessionId, b brief) {
	a.mu.Lock()
	if cur, ok := a.briefs[sid]; ok && cur == b {
		delete(a.briefs, sid)
	}
	changed := false
	for k, s := range a.sessions {
		if s.ID == sid {
			h := b.hash
			s.Instructions = &h
			a.sessions[k] = s
			changed = true
		}
	}
	a.mu.Unlock()
	if changed {
		a.saveSessions()
	}
}

// setMode puts a session this process opened in Config.Mode when the
// agent offers that mode and isn't in it (Claude Code's default mode,
// auto, asks Anthropic's classifier, which a gateway doesn't serve).
func (a *Agent) setMode(ctx context.Context, sid acp.SessionId, modes *acp.SessionModeState) {
	want := acp.SessionModeId(a.cfg.Mode)
	if want == "" || modes == nil || modes.CurrentModeId == want {
		return
	}
	for _, m := range modes.AvailableModes {
		if m.Id == want {
			if _, err := a.conn.SetSessionMode(ctx, acp.SetSessionModeRequest{SessionId: sid, ModeId: want}); err != nil {
				slog.Warn("could not set the agent's session mode", "mode", want, "err", err)
			}
			return
		}
	}
	slog.Warn("the agent doesn't offer the session mode", "mode", want)
}

// configure applies the profile's model and effort to a session this
// process opened, among its config options.
func (a *Agent) configure(ctx context.Context, sid acp.SessionId, options []acp.SessionConfigOption) {
	if a.kit == nil {
		return
	}
	for _, want := range []struct {
		value    string
		category acp.SessionConfigOptionCategory
		ids      []string
	}{
		{a.kit.Model, acp.SessionConfigOptionCategoryModel, []string{"model"}},
		{a.kit.Effort, acp.SessionConfigOptionCategoryThoughtLevel, []string{"reasoning_effort", "effort", "thought_level"}},
	} {
		if want.value == "" {
			continue
		}
		opt, value, ok := pickOption(options, want.category, want.ids, want.value)
		if !ok {
			slog.Warn("the agent has no such session option; using its default", "option", want.category, "value", want.value)
			continue
		}
		if opt.CurrentValue == value {
			continue
		}
		_, err := a.conn.SetSessionConfigOption(ctx, acp.SetSessionConfigOptionRequest{ValueId: &acp.SetSessionConfigOptionValueId{
			SessionId: sid, ConfigId: opt.Id, Value: value}})
		if err != nil {
			slog.Warn("could not set the session option", "option", opt.Id, "value", value, "err", err)
		}
	}
}

// pickOption finds the select option of a category (or one of ids) and
// its value matching want (by value or name, any case).
func pickOption(options []acp.SessionConfigOption, category acp.SessionConfigOptionCategory, ids []string, want string) (*acp.SessionConfigOptionSelect, acp.SessionConfigValueId, bool) {
	for _, o := range options {
		s := o.Select
		if s == nil || !((s.Category != nil && *s.Category == category) || slices.Contains(ids, string(s.Id))) {
			continue
		}
		var values []acp.SessionConfigSelectOption
		if s.Options.Ungrouped != nil {
			values = *s.Options.Ungrouped
		}
		if s.Options.Grouped != nil {
			for _, g := range *s.Options.Grouped {
				values = append(values, g.Options...)
			}
		}
		for _, v := range values {
			if strings.EqualFold(string(v.Value), want) || strings.EqualFold(v.Name, want) {
				return s, v.Value, true
			}
		}
	}
	return nil, "", false
}

func (a *Agent) remember(key string, s savedSession, save bool) {
	a.mu.Lock()
	a.open[key] = s.ID
	a.sessions[key] = s
	a.mu.Unlock()
	if save {
		a.saveSessions()
	}
}

// sessionsKey scopes saved sessions to the port, the agent's workspace
// and (with a profile) the library agent: two agents in one directory
// keep their own sessions.
func (a *Agent) sessionsKey() string {
	key := strings.TrimRight(a.cfg.Server, "/") + " " + a.cfg.Cwd + " " + filepath.Base(a.cfg.Command[0])
	if a.kit != nil {
		key += " " + a.kit.Name
	}
	return key
}

// savedSession is a conversation's agent session and the instructions it
// has seen (instructionsHash; nil: unknown). Older connectors saved the
// bare session id, which still reads.
type savedSession struct {
	ID           acp.SessionId `json:"id"`
	Instructions *string       `json:"instructions,omitempty"`
}

func (s *savedSession) UnmarshalJSON(b []byte) error {
	var id string
	if json.Unmarshal(b, &id) == nil {
		*s = savedSession{ID: acp.SessionId(id)}
		return nil
	}
	type plain savedSession
	return json.Unmarshal(b, (*plain)(s))
}

type sessionsFile map[string]map[string]savedSession

func (a *Agent) loadSessions() {
	if a.cfg.SessionsFile == "" {
		return
	}
	raw, err := os.ReadFile(a.cfg.SessionsFile)
	if err != nil {
		return
	}
	var f sessionsFile
	if json.Unmarshal(raw, &f) != nil {
		return
	}
	for conv, s := range f[a.sessionsKey()] {
		if _, ok := mustConversation(conv); ok && s.ID != "" {
			a.sessions[conv] = s
		}
	}
}

func (a *Agent) saveSessions() {
	if a.cfg.SessionsFile == "" {
		return
	}
	f := sessionsFile{}
	if raw, err := os.ReadFile(a.cfg.SessionsFile); err == nil {
		_ = json.Unmarshal(raw, &f)
	}
	a.mu.Lock()
	cur := map[string]savedSession{}
	for k, v := range a.sessions {
		cur[k] = v
	}
	a.mu.Unlock()
	f[a.sessionsKey()] = cur
	raw, _ := json.MarshalIndent(f, "", "  ")
	if err := os.MkdirAll(filepath.Dir(a.cfg.SessionsFile), 0o700); err != nil {
		slog.Warn("save sessions", "err", err)
		return
	}
	tmp := a.cfg.SessionsFile + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o600); err == nil {
		_ = os.Rename(tmp, a.cfg.SessionsFile)
	}
}

// client answers the agent's requests. The connector offers no filesystem
// or terminal: the agent works with its own tools.
type client struct{ a *Agent }

var _ acp.Client = (*client)(nil)

func (c *client) SessionUpdate(_ context.Context, n acp.SessionNotification) error {
	u := n.Update
	c.a.mu.Lock()
	f := c.a.feeds[n.SessionId]
	if u.AgentMessageChunk != nil && u.AgentMessageChunk.Content.Text != nil {
		if buf, ok := c.a.answers[n.SessionId]; ok {
			buf.WriteString(u.AgentMessageChunk.Content.Text.Text)
		}
	}
	c.a.mu.Unlock()
	if u.ToolCall != nil {
		slog.Info("agent tool", "session", n.SessionId, "title", u.ToolCall.Title)
	}
	if f != nil {
		f.update(u)
	}
	return nil
}

// portTool says a permission request is for one of the port's own tools
// (the "seaport" MCP server the connector gives every session): reply,
// history, skill… are how the agent works, and the port checks them
// itself, so they are allowed whatever approve says (Claude Code asks for
// MCP tools too: rejecting reply loses the answer).
func portTool(title string) bool {
	return strings.HasPrefix(title, "mcp__seaport__")
}

func (c *client) RequestPermission(_ context.Context, p acp.RequestPermissionRequest) (acp.RequestPermissionResponse, error) {
	title := ""
	if p.ToolCall.Title != nil {
		title = *p.ToolCall.Title
	}
	want := map[bool][]acp.PermissionOptionKind{
		true:  {acp.PermissionOptionKindAllowOnce, acp.PermissionOptionKindAllowAlways},
		false: {acp.PermissionOptionKindRejectOnce, acp.PermissionOptionKindRejectAlways},
	}[c.a.approve == agent.ApproveAllow || portTool(title)]
	for _, kind := range want {
		for _, o := range p.Options {
			if o.Kind == kind {
				slog.Info("agent permission", "tool", title, "answer", o.Kind)
				return acp.RequestPermissionResponse{Outcome: acp.NewRequestPermissionOutcomeSelected(o.OptionId)}, nil
			}
		}
	}
	return acp.RequestPermissionResponse{Outcome: acp.NewRequestPermissionOutcomeCancelled()}, nil
}

func (c *client) ReadTextFile(context.Context, acp.ReadTextFileRequest) (acp.ReadTextFileResponse, error) {
	return acp.ReadTextFileResponse{}, acp.NewMethodNotFound("fs/read_text_file")
}

func (c *client) WriteTextFile(context.Context, acp.WriteTextFileRequest) (acp.WriteTextFileResponse, error) {
	return acp.WriteTextFileResponse{}, acp.NewMethodNotFound("fs/write_text_file")
}

func (c *client) CreateTerminal(context.Context, acp.CreateTerminalRequest) (acp.CreateTerminalResponse, error) {
	return acp.CreateTerminalResponse{}, acp.NewMethodNotFound("terminal/create")
}

func (c *client) KillTerminal(context.Context, acp.KillTerminalRequest) (acp.KillTerminalResponse, error) {
	return acp.KillTerminalResponse{}, acp.NewMethodNotFound("terminal/kill")
}

func (c *client) TerminalOutput(context.Context, acp.TerminalOutputRequest) (acp.TerminalOutputResponse, error) {
	return acp.TerminalOutputResponse{}, acp.NewMethodNotFound("terminal/output")
}

func (c *client) ReleaseTerminal(context.Context, acp.ReleaseTerminalRequest) (acp.ReleaseTerminalResponse, error) {
	return acp.ReleaseTerminalResponse{}, acp.NewMethodNotFound("terminal/release")
}

func (c *client) WaitForTerminalExit(context.Context, acp.WaitForTerminalExitRequest) (acp.WaitForTerminalExitResponse, error) {
	return acp.WaitForTerminalExitResponse{}, acp.NewMethodNotFound("terminal/wait_for_exit")
}
