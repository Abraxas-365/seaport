package connector

import (
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"

	"github.com/Abraxas-365/puerto/internal/errx"
	"github.com/Abraxas-365/puerto/internal/identity"
	acp "github.com/coder/acp-go-sdk"
)

// Agent is an ACP agent process with one session per conversation.
type Agent struct {
	cfg  Config
	cmd  *exec.Cmd
	conn *acp.ClientSideConnection
	caps acp.AgentCapabilities
	done chan struct{}

	mu       sync.Mutex
	mcpURL   string
	sessions map[string]acp.SessionId // conversation id → agent session (persisted)
	open     map[string]acp.SessionId // opened on this connection
	opening  map[string]*sync.Mutex
	answers  map[acp.SessionId]*strings.Builder
}

// StartAgent spawns the agent command and runs the ACP handshake.
func StartAgent(ctx context.Context, cfg Config) (*Agent, error) {
	cmd := exec.Command(cfg.Command[0], cfg.Command[1:]...)
	cmd.Dir = cfg.Cwd
	cmd.Stderr = os.Stderr
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
	a := &Agent{cfg: cfg, cmd: cmd, done: make(chan struct{}),
		sessions: map[string]acp.SessionId{}, open: map[string]acp.SessionId{},
		opening: map[string]*sync.Mutex{}, answers: map[acp.SessionId]*strings.Builder{}}
	go func() {
		_ = cmd.Wait()
		close(a.done)
	}()
	a.conn = acp.NewClientSideConnection(&client{a: a}, stdin, stdout)
	a.conn.SetLogger(slog.Default().With("component", "acp"))
	a.loadSessions()

	init, err := a.conn.Initialize(ctx, acp.InitializeRequest{
		ProtocolVersion: acp.ProtocolVersionNumber,
		ClientInfo:      &acp.Implementation{Name: "puerto", Version: "0.1"},
	})
	if err != nil {
		a.Close()
		return nil, errx.Wrap(err, "ACP initialize (is the command an ACP agent? e.g. rness --acp)", errx.TypeExternal)
	}
	a.caps = init.AgentCapabilities
	slog.Info("agent ready", "command", strings.Join(cfg.Command, " "),
		"resume", a.caps.SessionCapabilities.Resume != nil, "load", a.caps.LoadSession, "mcp_http", a.caps.McpCapabilities.Http)
	if !a.caps.McpCapabilities.Http && !cfg.AutoReply {
		slog.Warn("agent does not support HTTP MCP servers; it can't use the reply tool. Use --auto-reply.")
	}
	return a, nil
}

// Done is closed when the agent process exits.
func (a *Agent) Done() <-chan struct{} { return a.done }

func (a *Agent) Close() {
	if a.cmd.Process != nil {
		_ = a.cmd.Process.Signal(os.Interrupt)
	}
}

func (a *Agent) SetMCPURL(u string) {
	a.mu.Lock()
	a.mcpURL = u
	a.mu.Unlock()
}

// Prompt runs one turn on the conversation's session and returns the
// agent's final text.
func (a *Agent) Prompt(ctx context.Context, conv identity.ConversationID, text string) (string, error) {
	sid, err := a.sessionFor(ctx, conv)
	if err != nil {
		return "", err
	}
	buf := &strings.Builder{}
	a.mu.Lock()
	a.answers[sid] = buf
	a.mu.Unlock()
	defer func() {
		a.mu.Lock()
		delete(a.answers, sid)
		a.mu.Unlock()
	}()
	resp, err := a.conn.Prompt(ctx, acp.PromptRequest{SessionId: sid, Prompt: []acp.ContentBlock{acp.TextBlock(text)}})
	if err != nil {
		if ctx.Err() != nil {
			_ = a.conn.Cancel(context.WithoutCancel(ctx), acp.CancelNotification{SessionId: sid})
		}
		return "", errx.Wrap(err, "agent prompt", errx.TypeExternal)
	}
	if resp.StopReason == acp.StopReasonCancelled || resp.StopReason == acp.StopReasonRefusal {
		return "", errx.External("agent turn ended: " + string(resp.StopReason))
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	return buf.String(), nil
}

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
func (a *Agent) Steer(ctx context.Context, conv identity.ConversationID, text string) (bool, error) {
	a.mu.Lock()
	sid, ok := a.open[conv.String()]
	a.mu.Unlock()
	if !ok {
		return false, nil
	}
	raw, err := a.conn.CallExtension(ctx, steerMethod, map[string]any{
		"sessionId": sid, "prompt": []acp.ContentBlock{acp.TextBlock(text)},
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

func (a *Agent) mcpServers() []acp.McpServer {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.mcpURL == "" || !a.caps.McpCapabilities.Http {
		return []acp.McpServer{}
	}
	return []acp.McpServer{{Http: &acp.McpServerHttpInline{
		Name: "puerto", Type: "http", Url: a.mcpURL,
		Headers: []acp.HttpHeader{{Name: "Authorization", Value: "Bearer " + a.cfg.Token}},
	}}}
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
	if hasSaved {
		var err error
		switch {
		case a.caps.SessionCapabilities.Resume != nil:
			_, err = a.conn.ResumeSession(ctx, acp.ResumeSessionRequest{SessionId: saved, Cwd: a.cfg.Cwd, McpServers: servers})
		case a.caps.LoadSession:
			_, err = a.conn.LoadSession(ctx, acp.LoadSessionRequest{SessionId: saved, Cwd: a.cfg.Cwd, McpServers: servers})
		default:
			err = errx.Business("agent can't resume sessions")
		}
		if err == nil {
			a.remember(key, saved, false)
			slog.Info("agent session resumed", "conversation", key, "session", saved)
			return saved, nil
		}
		slog.Warn("could not resume agent session; starting a new one", "conversation", key, "err", err)
	}
	resp, err := a.conn.NewSession(ctx, acp.NewSessionRequest{Cwd: a.cfg.Cwd, McpServers: servers})
	if err != nil {
		return "", errx.Wrap(err, "agent new session", errx.TypeExternal)
	}
	a.remember(key, resp.SessionId, true)
	slog.Info("agent session created", "conversation", key, "session", resp.SessionId)
	return resp.SessionId, nil
}

func (a *Agent) remember(key string, sid acp.SessionId, save bool) {
	a.mu.Lock()
	a.open[key] = sid
	a.sessions[key] = sid
	a.mu.Unlock()
	if save {
		a.saveSessions()
	}
}

// sessionsKey scopes saved sessions to the port and the agent's workspace.
func (a *Agent) sessionsKey() string {
	return strings.TrimRight(a.cfg.Server, "/") + " " + a.cfg.Cwd + " " + filepath.Base(a.cfg.Command[0])
}

type sessionsFile map[string]map[string]acp.SessionId

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
	for conv, sid := range f[a.sessionsKey()] {
		if _, ok := mustConversation(conv); ok {
			a.sessions[conv] = sid
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
	cur := map[string]acp.SessionId{}
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
	switch {
	case u.AgentMessageChunk != nil && u.AgentMessageChunk.Content.Text != nil:
		c.a.mu.Lock()
		if buf, ok := c.a.answers[n.SessionId]; ok {
			buf.WriteString(u.AgentMessageChunk.Content.Text.Text)
		}
		c.a.mu.Unlock()
	case u.ToolCall != nil:
		slog.Info("agent tool", "session", n.SessionId, "title", u.ToolCall.Title)
	}
	return nil
}

func (c *client) RequestPermission(_ context.Context, p acp.RequestPermissionRequest) (acp.RequestPermissionResponse, error) {
	want := map[bool][]acp.PermissionOptionKind{
		true:  {acp.PermissionOptionKindAllowOnce, acp.PermissionOptionKindAllowAlways},
		false: {acp.PermissionOptionKindRejectOnce, acp.PermissionOptionKindRejectAlways},
	}[c.a.cfg.Approve == "allow"]
	title := ""
	if p.ToolCall.Title != nil {
		title = *p.ToolCall.Title
	}
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
