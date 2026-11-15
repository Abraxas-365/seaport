// Package acptest is a test-only ACP agent: an echo agent the connector's
// tests (and the port's connector e2e tests) run as the connector's
// harness. It lives under internal/connector because the ACP SDK is
// confined to the connector (vendorSDKs in TestLayerDependencies); only
// _test.go files import this package.
//
// A test binary becomes the agent by re-executing itself:
//
//	env SEAPORT_TEST_ACP_AGENT=1 <test binary> -test.run=^TestACPAgentMain$   // acptest.Command()
//
// its TestACPAgentMain calls RunFromEnv, which serves ACP on stdio until the connector closes it.
//
// What it does, configured by environment variables the test sets:
//
//   - EnvCaps: comma separated capabilities to advertise: load
//     (session/load), resume (session/resume), image (promptCapabilities),
//     systemprompt (sessionCapabilities.systemPrompt), steer (the rness
//     extension _meta.rness.steer, answered by _rness/steer), http (HTTP
//     MCP servers), modes and options (a session mode and a model option the
//     connector may set).
//   - session/prompt echoes the last chat message of the prompt as
//     "echo: <text>" (an agent_message_chunk) and ends with end_turn. The
//     prompt's own last line is the connector's boilerplate, so the echo
//     reads the "[time] sender: text" lines the connector writes for each
//     ship instead. Directives inside a message: "hold:" blocks the turn
//     until the gate file (EnvLog + ".gate") exists, "tool:" emits one tool
//     call, "ask: question|A|B" asks the people for an approval over
//     POST /crew/approvals (as `seaport ask` does), "fail:" fails the turn.
//   - Every request that matters is appended to the JSONL file EnvLog (see
//     Record), the way a test asserts what the agent was told.
package acptest

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	acp "github.com/coder/acp-go-sdk"
)

// Environment variables the test sets (the connector passes its own
// environment on to the agent it starts).
const (
	// EnvAgent = "1" makes TestACPAgentMain serve instead of skip.
	EnvAgent = "SEAPORT_TEST_ACP_AGENT"
	// EnvLog is the JSONL file every Record is appended to.
	EnvLog = "SEAPORT_TEST_ACP_LOG"
	// EnvCaps lists the capabilities to advertise (see the package doc).
	EnvCaps = "SEAPORT_TEST_ACP_CAPS"
	// EnvEnvNames lists environment variables whose values the start
	// record carries (the kit's env reaches the agent through it).
	EnvEnvNames = "SEAPORT_TEST_ACP_ENV"
)

// Record is one line of the log.
type Record struct {
	// Kind: start, setup (a session/new|load|resume as it arrived on the
	// wire, with the systemPrompt the SDK has no field for), new, load,
	// resume, prompt, steer, cancel, mode, config, ask.
	Kind    string `json:"kind"`
	Method  string `json:"method,omitempty"`
	Session string `json:"sessionId,omitempty"`
	// Text: the prompt or steer (all text blocks), the mode or option set,
	// the answer of an ask.
	Text       string            `json:"text,omitempty"`
	Images     int               `json:"images,omitempty"`
	ImageTypes []string          `json:"imageTypes,omitempty"`
	System     string            `json:"systemPrompt,omitempty"`
	MCP        []string          `json:"mcp,omitempty"`
	Cwd        string            `json:"cwd,omitempty"`
	Env        map[string]string `json:"env,omitempty"`
}

// Command is what a connector runs to start the agent from a test binary:
// `env` sets EnvAgent and the variables given (NAME=value; the agent's
// configuration: EnvLog, EnvCaps...) for the agent only, so a test never
// changes its own process environment.
func Command(vars ...string) []string {
	exe, err := os.Executable()
	if err != nil {
		exe = os.Args[0]
	}
	out := []string{"env", EnvAgent + "=1"}
	out = append(out, vars...)
	return append(out, exe, "-test.run=^TestACPAgentMain$")
}

// Active says this process was started as the agent.
func Active() bool { return os.Getenv(EnvAgent) == "1" }

// RunFromEnv serves ACP on stdio until the peer closes it, then exits the
// process (a test binary would otherwise print its PASS line to the
// protocol's stdout).
func RunFromEnv() {
	_ = Serve(context.Background(), os.Stdin, os.Stdout, os.Getenv)
	os.Exit(0)
}

// ReadLog reads the log (none yet: empty); a half-written last line is
// skipped.
func ReadLog(path string) []Record {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var out []Record
	for _, line := range bytes.Split(raw, []byte("\n")) {
		var r Record
		if len(line) > 0 && json.Unmarshal(line, &r) == nil {
			out = append(out, r)
		}
	}
	return out
}

// Of keeps the records of a kind.
func Of(recs []Record, kind string) []Record {
	var out []Record
	for _, r := range recs {
		if r.Kind == kind {
			out = append(out, r)
		}
	}
	return out
}

// GatePath is the file a "hold:" turn waits for.
func GatePath(logPath string) string { return logPath + ".gate" }

type caps map[string]bool

func parseCaps(s string) caps {
	c := caps{}
	for _, f := range strings.Split(s, ",") {
		if f = strings.TrimSpace(f); f != "" {
			c[f] = true
		}
	}
	return c
}

type turn struct {
	mu     sync.Mutex
	steers []string
}

type agent struct {
	caps   caps
	getenv func(string) string
	log    string
	conn   *acp.AgentSideConnection

	logMu sync.Mutex
	seq   atomic.Int64

	mu    sync.Mutex
	turns map[acp.SessionId]*turn
}

var (
	_ acp.Agent                  = (*agent)(nil)
	_ acp.AgentLoader            = (*agent)(nil)
	_ acp.ExtensionMethodHandler = (*agent)(nil)
)

// Serve runs the agent over in/out until the peer closes in (or ctx ends).
func Serve(ctx context.Context, in io.Reader, out io.Writer, getenv func(string) string) error {
	a := &agent{caps: parseCaps(getenv(EnvCaps)), getenv: getenv, log: getenv(EnvLog), turns: map[acp.SessionId]*turn{}}
	// The start record is written before the connection can answer anything:
	// a test reads it as soon as StartAgent returns (right after initialize).
	start := Record{Kind: "start", Env: map[string]string{}}
	for _, name := range strings.Split(getenv(EnvEnvNames), ",") {
		if name = strings.TrimSpace(name); name != "" {
			start.Env[name] = getenv(name)
		}
	}
	a.write(start)
	tapped, feed := io.Pipe()
	go a.tap(in, feed)
	a.conn = acp.NewAgentSideConnection(a, &capsWriter{w: out, systemPrompt: a.caps["systemprompt"]}, tapped)
	select {
	case <-a.conn.Done():
	case <-ctx.Done():
	}
	return nil
}

func (a *agent) write(r Record) {
	if a.log == "" {
		return
	}
	raw, _ := json.Marshal(r)
	a.logMu.Lock()
	defer a.logMu.Unlock()
	f, err := os.OpenFile(a.log, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return
	}
	_, _ = f.Write(append(raw, '\n'))
	_ = f.Close()
}

// tap forwards the connector's messages to the SDK, logging the session
// setup requests on the way: the SDK has no systemPrompt field to read.
func (a *agent) tap(in io.Reader, out *io.PipeWriter) {
	r := bufio.NewReaderSize(in, 1<<20)
	for {
		line, err := r.ReadBytes('\n')
		if len(line) > 0 {
			a.inspect(line)
			if _, werr := out.Write(line); werr != nil {
				return
			}
		}
		if err != nil {
			_ = out.Close()
			return
		}
	}
}

func (a *agent) inspect(line []byte) {
	var msg struct {
		Method string `json:"method"`
		Params struct {
			SessionID    string                  `json:"sessionId"`
			Cwd          string                  `json:"cwd"`
			McpServers   []struct{ Name string } `json:"mcpServers"`
			SystemPrompt struct {
				Mode    string                  `json:"mode"`
				Content []struct{ Text string } `json:"content"`
			} `json:"systemPrompt"`
		} `json:"params"`
	}
	if json.Unmarshal(line, &msg) != nil || (msg.Method != "session/new" && msg.Method != "session/load" && msg.Method != "session/resume") {
		return
	}
	r := Record{Kind: "setup", Method: msg.Method, Session: msg.Params.SessionID, Cwd: msg.Params.Cwd}
	for _, s := range msg.Params.McpServers {
		r.MCP = append(r.MCP, s.Name)
	}
	var sp []string
	for _, c := range msg.Params.SystemPrompt.Content {
		sp = append(sp, c.Text)
	}
	r.System = strings.Join(sp, "\n")
	a.write(r)
}

// capsWriter adds sessionCapabilities.systemPrompt, which the SDK's types
// lack, to the initialize response.
type capsWriter struct {
	w            io.Writer
	systemPrompt bool
}

func (c *capsWriter) Write(p []byte) (int, error) {
	if !c.systemPrompt || !bytes.Contains(p, []byte(`"agentCapabilities"`)) {
		return c.w.Write(p)
	}
	var msg map[string]json.RawMessage
	if json.Unmarshal(p, &msg) != nil {
		return c.w.Write(p)
	}
	var result map[string]json.RawMessage
	if json.Unmarshal(msg["result"], &result) != nil {
		return c.w.Write(p)
	}
	var capabilities map[string]json.RawMessage
	if json.Unmarshal(result["agentCapabilities"], &capabilities) != nil {
		return c.w.Write(p)
	}
	session := map[string]json.RawMessage{}
	_ = json.Unmarshal(capabilities["sessionCapabilities"], &session)
	session["systemPrompt"] = json.RawMessage(`{}`)
	capabilities["sessionCapabilities"], _ = json.Marshal(session)
	result["agentCapabilities"], _ = json.Marshal(capabilities)
	msg["result"], _ = json.Marshal(result)
	b, err := json.Marshal(msg)
	if err != nil {
		return c.w.Write(p)
	}
	if _, err := c.w.Write(append(b, '\n')); err != nil {
		return 0, err
	}
	return len(p), nil
}

func (a *agent) Initialize(context.Context, acp.InitializeRequest) (acp.InitializeResponse, error) {
	c := acp.AgentCapabilities{
		LoadSession:        a.caps["load"],
		McpCapabilities:    acp.McpCapabilities{Http: a.caps["http"]},
		PromptCapabilities: acp.PromptCapabilities{Image: a.caps["image"]},
	}
	if a.caps["resume"] {
		c.SessionCapabilities.Resume = &acp.SessionResumeCapabilities{}
	}
	if a.caps["steer"] {
		c.Meta = map[string]any{"rness": map[string]any{"steer": true}}
	}
	return acp.InitializeResponse{
		ProtocolVersion:   acp.ProtocolVersionNumber,
		AgentCapabilities: c,
		AgentInfo:         &acp.Implementation{Name: "seaport-echo", Version: "0"},
		AuthMethods:       []acp.AuthMethod{},
	}, nil
}

func (a *agent) Authenticate(context.Context, acp.AuthenticateRequest) (acp.AuthenticateResponse, error) {
	return acp.AuthenticateResponse{}, nil
}

func (a *agent) Logout(context.Context, acp.LogoutRequest) (acp.LogoutResponse, error) {
	return acp.LogoutResponse{}, acp.NewMethodNotFound(acp.AgentMethodLogout)
}

func (a *agent) CloseSession(context.Context, acp.CloseSessionRequest) (acp.CloseSessionResponse, error) {
	return acp.CloseSessionResponse{}, nil
}

func (a *agent) ListSessions(context.Context, acp.ListSessionsRequest) (acp.ListSessionsResponse, error) {
	return acp.ListSessionsResponse{Sessions: []acp.SessionInfo{}}, nil
}

// modes and options are what a session offers when the capabilities ask.
func (a *agent) modes() *acp.SessionModeState {
	if !a.caps["modes"] {
		return nil
	}
	return &acp.SessionModeState{CurrentModeId: "default", AvailableModes: []acp.SessionMode{
		{Id: "default", Name: "Default"}, {Id: "bypass", Name: "Bypass"}}}
}

func (a *agent) options() []acp.SessionConfigOption {
	if !a.caps["options"] {
		return nil
	}
	return []acp.SessionConfigOption{{Select: &acp.SessionConfigOptionSelect{
		Id: "model", Name: "Model", Type: "select", Category: acp.Ptr(acp.SessionConfigOptionCategoryModel),
		CurrentValue: "echo-small",
		Options: acp.SessionConfigSelectOptions{Ungrouped: &acp.SessionConfigSelectOptionsUngrouped{
			{Name: "Echo small", Value: "echo-small"}, {Name: "Echo large", Value: "echo-large"}}},
	}}}
}

func (a *agent) NewSession(_ context.Context, p acp.NewSessionRequest) (acp.NewSessionResponse, error) {
	id := acp.SessionId(fmt.Sprintf("echo-%d-%d", os.Getpid(), a.seq.Add(1)))
	a.write(Record{Kind: "new", Session: string(id), Cwd: p.Cwd})
	return acp.NewSessionResponse{SessionId: id, Modes: a.modes(), ConfigOptions: a.options()}, nil
}

func (a *agent) LoadSession(_ context.Context, p acp.LoadSessionRequest) (acp.LoadSessionResponse, error) {
	a.write(Record{Kind: "load", Session: string(p.SessionId), Cwd: p.Cwd})
	return acp.LoadSessionResponse{Modes: a.modes(), ConfigOptions: a.options()}, nil
}

func (a *agent) ResumeSession(_ context.Context, p acp.ResumeSessionRequest) (acp.ResumeSessionResponse, error) {
	a.write(Record{Kind: "resume", Session: string(p.SessionId), Cwd: p.Cwd})
	return acp.ResumeSessionResponse{Modes: a.modes(), ConfigOptions: a.options()}, nil
}

func (a *agent) SetSessionMode(_ context.Context, p acp.SetSessionModeRequest) (acp.SetSessionModeResponse, error) {
	a.write(Record{Kind: "mode", Session: string(p.SessionId), Text: string(p.ModeId)})
	return acp.SetSessionModeResponse{}, nil
}

func (a *agent) SetSessionConfigOption(_ context.Context, p acp.SetSessionConfigOptionRequest) (acp.SetSessionConfigOptionResponse, error) {
	if p.ValueId != nil {
		a.write(Record{Kind: "config", Session: string(p.ValueId.SessionId), Text: string(p.ValueId.ConfigId) + "=" + string(p.ValueId.Value)})
	}
	return acp.SetSessionConfigOptionResponse{ConfigOptions: []acp.SessionConfigOption{}}, nil
}

func (a *agent) Cancel(_ context.Context, p acp.CancelNotification) error {
	a.write(Record{Kind: "cancel", Session: string(p.SessionId)})
	return nil
}

// flatten reads a prompt's blocks: the text, and the images' types.
func flatten(blocks []acp.ContentBlock) (string, []string) {
	var texts, types []string
	for _, b := range blocks {
		switch {
		case b.Text != nil:
			texts = append(texts, b.Text.Text)
		case b.Image != nil:
			types = append(types, b.Image.MimeType)
		}
	}
	return strings.Join(texts, "\n"), types
}

// A chat message as the connector writes it: "[2006-01-02 15:04] sender: text",
// with "(channel title) " before the sender in a thread of several doors.
var messageLine = regexp.MustCompile(`(?m)^\[\d{4}-\d{2}-\d{2} \d{2}:\d{2}\] (?:\([^)\n]*\) )?[^:\n]*: (.*)$`)

// messages are the texts of the chat messages in a prompt or steer.
func messages(prompt string) []string {
	var out []string
	for _, m := range messageLine.FindAllStringSubmatch(prompt, -1) {
		out = append(out, m[1])
	}
	return out
}

// lastLine is the last non-empty line of s.
func lastLine(s string) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	return strings.TrimSpace(lines[len(lines)-1])
}

func (a *agent) turn(id acp.SessionId) *turn {
	a.mu.Lock()
	defer a.mu.Unlock()
	t := a.turns[id]
	if t == nil {
		t = &turn{}
		a.turns[id] = t
	}
	return t
}

// running is the set of sessions with a turn in flight.
func (a *agent) running(id acp.SessionId) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	_, ok := a.turns[id]
	return ok
}

func (a *agent) Prompt(ctx context.Context, p acp.PromptRequest) (acp.PromptResponse, error) {
	text, types := flatten(p.Prompt)
	t := a.turn(p.SessionId)
	// A turn is in flight from here: a steer that arrives after the log
	// line below is taken.
	defer func() {
		a.mu.Lock()
		delete(a.turns, p.SessionId)
		a.mu.Unlock()
	}()
	a.write(Record{Kind: "prompt", Session: string(p.SessionId), Text: text, Images: len(types), ImageTypes: types})

	msgs := messages(text)
	said := strings.Join(msgs, "\n")
	reply := lastLine(text)
	if len(msgs) > 0 {
		reply = msgs[len(msgs)-1]
	}
	if strings.Contains(said, "fail:") {
		return acp.PromptResponse{}, acp.NewInternalError(map[string]any{"error": "the echo agent was told to fail"})
	}
	if strings.Contains(said, "hold:") && !a.hold(ctx) {
		return acp.PromptResponse{StopReason: acp.StopReasonCancelled}, nil
	}
	if strings.Contains(said, "tool:") {
		a.tool(ctx, p.SessionId)
	}
	if i := strings.Index(said, "ask:"); i >= 0 {
		a.ask(ctx, text, strings.TrimSpace(strings.SplitN(said[i+len("ask:"):], "\n", 2)[0]))
	}
	t.mu.Lock()
	if n := len(t.steers); n > 0 {
		if m := messages(t.steers[n-1]); len(m) > 0 {
			reply = m[len(m)-1]
		} else {
			reply = lastLine(t.steers[n-1])
		}
	}
	t.mu.Unlock()
	err := a.conn.SessionUpdate(ctx, acp.SessionNotification{SessionId: p.SessionId, Update: acp.UpdateAgentMessageText("echo: " + reply)})
	if err != nil {
		return acp.PromptResponse{}, err
	}
	return acp.PromptResponse{StopReason: acp.StopReasonEndTurn}, nil
}

// hold waits for the gate file, false when the turn is cancelled first.
func (a *agent) hold(ctx context.Context) bool {
	deadline := time.Now().Add(time.Minute)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(GatePath(a.log)); err == nil {
			return true
		}
		select {
		case <-ctx.Done():
			return false
		case <-time.After(10 * time.Millisecond):
		}
	}
	return false
}

func (a *agent) tool(ctx context.Context, sid acp.SessionId) {
	_ = a.conn.SessionUpdate(ctx, acp.SessionNotification{SessionId: sid, Update: acp.StartToolCall("echo-tool-1", "Run echo tool",
		acp.WithStartKind(acp.ToolKindExecute), acp.WithStartStatus(acp.ToolCallStatusInProgress))})
	_ = a.conn.SessionUpdate(ctx, acp.SessionNotification{SessionId: sid, Update: acp.UpdateToolCall("echo-tool-1",
		acp.WithUpdateStatus(acp.ToolCallStatusCompleted))})
}

var conversationLine = regexp.MustCompile(`(?m)^conversation_id: (\S+)`)

// ask posts an approval the way `seaport ask` does: with the connector's
// SEAPORT_URL and SEAPORT_CREW_TOKEN. spec is "question|option|option".
func (a *agent) ask(ctx context.Context, prompt, spec string) {
	m := conversationLine.FindStringSubmatch(prompt)
	parts := strings.Split(spec, "|")
	if m == nil || len(parts) < 3 {
		a.write(Record{Kind: "ask", Text: "bad ask"})
		return
	}
	body, _ := json.Marshal(map[string]any{"conversation_id": m[1], "text": parts[0], "options": parts[1:]})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(a.getenv("SEAPORT_URL"), "/")+"/crew/approvals", bytes.NewReader(body))
	if err != nil {
		a.write(Record{Kind: "ask", Text: err.Error()})
		return
	}
	req.Header.Set("Authorization", "Bearer "+a.getenv("SEAPORT_CREW_TOKEN"))
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		a.write(Record{Kind: "ask", Text: err.Error()})
		return
	}
	defer func() { _ = resp.Body.Close() }()
	raw, _ := io.ReadAll(resp.Body)
	a.write(Record{Kind: "ask", Text: fmt.Sprintf("%d %s", resp.StatusCode, strings.TrimSpace(string(raw)))})
}

// HandleExtensionMethod answers _rness/steer: input for the turn in flight
// on a session (steered false when none runs).
func (a *agent) HandleExtensionMethod(_ context.Context, method string, params json.RawMessage) (any, error) {
	if method != "_rness/steer" || !a.caps["steer"] {
		return nil, acp.NewMethodNotFound(method)
	}
	var in struct {
		SessionID acp.SessionId      `json:"sessionId"`
		Prompt    []acp.ContentBlock `json:"prompt"`
	}
	if err := json.Unmarshal(params, &in); err != nil {
		return nil, acp.NewInvalidParams(map[string]any{"error": err.Error()})
	}
	text, types := flatten(in.Prompt)
	a.mu.Lock()
	t := a.turns[in.SessionID]
	a.mu.Unlock()
	if t == nil {
		return map[string]bool{"steered": false}, nil
	}
	t.mu.Lock()
	t.steers = append(t.steers, text)
	t.mu.Unlock()
	a.write(Record{Kind: "steer", Session: string(in.SessionID), Text: text, Images: len(types), ImageTypes: types})
	return map[string]bool{"steered": true}, nil
}
