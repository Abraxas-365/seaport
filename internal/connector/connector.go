// Package connector is the agent side of seaport (`seaport connect`): it dials
// the port's /crew/ws, spawns an ACP agent (rness --acp, ...) and turns each
// delivery into a prompt on that conversation's agent session. The agent
// answers through the seaport MCP tools, or, with AutoReply, the connector
// sends the agent's final text.
package connector

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/Abraxas-365/seaport/internal/agent"
	"github.com/Abraxas-365/seaport/internal/conversation"
	"github.com/Abraxas-365/seaport/internal/crew"
	"github.com/Abraxas-365/seaport/internal/dock"
	"github.com/Abraxas-365/seaport/internal/errx"
	"github.com/Abraxas-365/seaport/internal/identity"
	"github.com/Abraxas-365/seaport/internal/ship"
	"github.com/coder/websocket"
)

// Config of a connector.
type Config struct {
	Server string // http(s)://host:port of the port
	Token  string // crew token ("" for a port in SEAPORT_AUTH=none)
	// As is the agent to work as: optional with a crew token (it belongs
	// to one; a different name is refused), required without one.
	As          string
	AgentName   string // label of this connection in the port's crew view
	Concurrency int
	AutoReply   bool
	// Approve answers agent permission requests: "reject" or "allow"; ""
	// takes the agent profile's (reject without one).
	Approve string
	// Deliver says what happens to messages that arrive while the agent is
	// working on the conversation: "steer" (default) hands them to the
	// running turn when the agent supports it (rness); "queue" sends them
	// as the next prompt after the turn ends.
	Deliver string
	// Mode is the ACP session mode to put each session in, when the agent
	// offers it ("": the agent's own default).
	Mode    string
	Cwd     string
	Command []string
	// Workdir, when set, gives each thread's session its working
	// directory (a hosted slot's worktree); else every session works in
	// Cwd.
	Workdir func(context.Context, identity.ThreadID) (string, error)
	// MCPURL replaces the port's MCP address from the welcome (its public
	// URL), for agents that reach the port by another address (a hosted
	// slot: SEAPORT_URL).
	MCPURL       string
	SessionsFile string
	// Protect names environment variables the kit's env may not replace (a
	// hosted slot's gateway and proxy settings).
	Protect []string
	// KitEnv gives the harness the kit's env secrets as environment
	// variables. A hosted slot always does (the slot is the port's); on a
	// person's machine `seaport connect` does only with -env, since the
	// machine's own environment is that person's to set.
	KitEnv bool
	// SessionMeta goes as _meta of every session setup request (agent
	// specific options, e.g. Claude Code's settingSources).
	SessionMeta map[string]any
	// Reload, when it fires, restarts the harness like a changed profile
	// does (work in flight first): a hosted slot's harness signed in.
	Reload <-chan struct{}
	// Browser, when set, is the engine to start when the kit asks for a
	// browser (a hosted slot's environment has one); "" with a kit that
	// asks for one logs a warning (a local `seaport connect` has none yet).
	Browser string
	// State is where the browser's profile lives (hosting.BrowserDir).
	State string
}

// Run fetches the agent's kit (profile and secrets), starts the harness
// with it, connects to the port and keeps the agent working until ctx
// ends, reconnecting with backoff. When the profile changes it finishes
// the work in flight, then starts the harness again with the new kit. A
// refused credential ends it: a revoked, expired or wrong crew token, or
// a hello the port refuses (an agent the token isn't for, or none named),
// never starts working again.
func Run(ctx context.Context, cfg Config) error {
	if len(cfg.Command) == 0 {
		return errx.Validation("an agent command is required: seaport connect -- rness --acp")
	}
	for {
		kit, err := fetchKitRetrying(ctx, cfg)
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return err
		}
		browser, err := browserFor(ctx, cfg, kit)
		if err != nil {
			return err
		}
		agent, err := StartAgent(ctx, cfg, kit, browser)
		if err != nil {
			browser.Stop()
			return err
		}
		if browser != nil && kit.BrowserDetect {
			// The detector lives with the browser (Browser.Done ends it).
			det := newDetector(cfg, kit, agent.table())
			go watchPages(ctx, browser, func(h pageHit) { det.hit(ctx, h) })
		}
		err = keepConnected(ctx, agent.Done(), time.Second, func(ctx context.Context) error {
			return session(ctx, cfg, agent)
		})
		agent.Stop()
		browser.Stop()
		if !errors.Is(err, errRestart) {
			return err
		}
		slog.Info("agent profile changed; restarting the agent with it")
	}
}

// browserFor starts the browser when the kit asks for one and the run has
// an engine; it is nil otherwise. It lives as long as the harness (a kit
// change restarts both; the profile on disk stays).
func browserFor(ctx context.Context, cfg Config, kit *agent.Kit) (*Browser, error) {
	if kit == nil || !kit.Browser {
		return nil, nil
	}
	if cfg.Browser == "" {
		slog.Warn("the agent's profile asks for a browser, but this connector has none (hosted slots of an environment with browser.engine do)")
		return nil, nil
	}
	return StartBrowser(ctx, cfg.Browser, cfg.State)
}

// keepConnected runs connect again after each lost connection, waiting
// first (doubling from backoff up to 30 s; back to backoff after a
// connection that lasted a minute). It returns nil when ctx ends, and an
// error when the agent exits or the port refuses the credential.
func keepConnected(ctx context.Context, agentDone <-chan struct{}, backoff time.Duration, connect func(context.Context) error) error {
	first := backoff
	for {
		started := time.Now()
		err := connect(ctx)
		if ctx.Err() != nil {
			return nil
		}
		select {
		case <-agentDone:
			return errx.Internal("agent process exited")
		default:
		}
		if errx.IsType(err, errx.TypeAuthorization) || errors.Is(err, errRestart) {
			return err
		}
		if time.Since(started) > time.Minute {
			backoff = first
		}
		slog.Warn("port connection lost, reconnecting", "err", err, "in", backoff)
		select {
		case <-time.After(backoff):
		case <-ctx.Done():
			return nil
		}
		backoff = min(backoff*2, 30*time.Second)
	}
}

func wsURL(server string) string {
	u := strings.TrimRight(server, "/")
	switch {
	case strings.HasPrefix(u, "https://"):
		u = "wss://" + strings.TrimPrefix(u, "https://")
	case strings.HasPrefix(u, "http://"):
		u = "ws://" + strings.TrimPrefix(u, "http://")
	}
	return u + "/crew/ws"
}

// refused turns the port closing the connection for a policy reason (the
// hello names an agent the credential can't work as) into an
// authorization error, which ends the connector; other errors are
// returned as they are (retried).
func refused(err error) error {
	var ce websocket.CloseError
	if errors.As(err, &ce) && ce.Code == websocket.StatusPolicyViolation {
		return errx.Unauthorized("the port refused the connection: " + ce.Reason)
	}
	return err
}

// restartFrame, queued after the last ack, makes the writer end the
// connection with errRestart once everything before it is written.
const restartFrame crew.FrameType = ""

// session runs one WebSocket connection. It returns errRestart when the
// agent's kit changed and the work in flight is done.
func session(ctx context.Context, cfg Config, agent *Agent) error {
	ctx, cancelCause := context.WithCancelCause(ctx)
	cancel := func() { cancelCause(nil) }
	defer cancel()
	ws, resp, err := websocket.Dial(ctx, wsURL(cfg.Server), &websocket.DialOptions{HTTPHeader: authHeader(cfg.Token)})
	if err != nil {
		if resp != nil && resp.StatusCode == http.StatusUnauthorized {
			if cfg.Token == "" {
				return errx.Unauthorized("the port requires a crew token (-token or SEAPORT_CREW_TOKEN)")
			}
			return errx.Unauthorized("the port rejected the crew token (revoked, expired or not of this port); create one with `seaport crew tokens create` or in the web app")
		}
		if resp != nil && resp.StatusCode == http.StatusUnprocessableEntity {
			return errx.Business("the port refused the connection: this organization already has as many agents connected as it allows")
		}
		return errx.Wrap(err, "dial port", errx.TypeExternal)
	}
	defer ws.CloseNow()
	ws.SetReadLimit(crew.MaxFrame)

	out := make(chan crew.Frame, 16)
	send := func(f crew.Frame) {
		select {
		case out <- f:
		case <-ctx.Done():
		}
	}
	go func() {
		ping := time.NewTicker(crew.PingInterval)
		defer ping.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case f := <-out:
				if f.Type == restartFrame {
					cancelCause(errRestart)
					return
				}
				raw, _ := json.Marshal(f)
				if err := ws.Write(ctx, websocket.MessageText, raw); err != nil {
					cancel()
					return
				}
			case <-ping.C:
				pctx, done := context.WithTimeout(ctx, 15*time.Second)
				err := ws.Ping(pctx)
				done()
				if err != nil {
					cancel()
					return
				}
			}
		}
	}()
	steering := cfg.Deliver != DeliverQueue && agent.CanSteer()
	if cfg.Deliver != DeliverQueue && !agent.CanSteer() {
		slog.Warn("the agent can't take messages mid-turn; they will queue for the next turn")
	}
	send(crew.Frame{Type: crew.FrameHello, Agent: cfg.AgentName, As: cfg.As, Concurrency: cfg.Concurrency, Steer: steering,
		ProfileVersion: agent.Version()})

	var wg sync.WaitGroup
	defer wg.Wait()
	// jobs outlive the connection: the harness's, so a reconnect keeps the
	// browser's state and the detector's asks.
	jobs := agent.table()
	var stateMu sync.Mutex
	draining, checking := false, false
	// check fetches the kit and, when it isn't the one the agent runs,
	// drains: no new deliveries, restart once the last one is acked.
	// drain stops new deliveries and restarts once the last one is acked
	// (stateMu held; it unlocks).
	drain := func() {
		draining = true
		idle := jobs.count() == 0
		stateMu.Unlock()
		send(crew.Frame{Type: crew.FrameDrain})
		if idle {
			send(crew.Frame{Type: restartFrame})
		}
	}
	check := func() {
		stateMu.Lock()
		if draining || checking || !agent.Profiled() {
			stateMu.Unlock()
			return
		}
		checking = true
		stateMu.Unlock()
		wg.Add(1)
		go func() {
			defer wg.Done()
			kit, err := fetchKit(ctx, cfg)
			stateMu.Lock()
			checking = false
			if err != nil || revision(kit) == agent.Revision() {
				stateMu.Unlock()
				if err != nil && ctx.Err() == nil {
					slog.Warn("could not fetch the changed agent profile; keeping the current one", "err", err)
				}
				return
			}
			slog.Info("agent profile changed; finishing the work in flight before restarting", "version", kit.Version, "revision", revision(kit))
			drain()
		}()
	}
	if cfg.Reload != nil {
		// Not in wg: it ends with ctx (cancelled after wg.Wait).
		go func() {
			select {
			case <-ctx.Done():
			case <-cfg.Reload:
				stateMu.Lock()
				if draining {
					stateMu.Unlock()
					return
				}
				slog.Info("the agent signed in; finishing the work in flight before restarting it")
				drain()
			}
		}()
	}
	for {
		_, raw, err := ws.Read(ctx)
		if err != nil {
			if errors.Is(context.Cause(ctx), errRestart) {
				return errRestart
			}
			return refused(err)
		}
		f, err := crew.Decode(raw)
		if err != nil {
			continue
		}
		switch f.Type {
		case crew.FrameWelcome:
			mcpURL := f.MCPURL
			if cfg.MCPURL != "" {
				mcpURL = cfg.MCPURL
			}
			agent.SetMCPURL(mcpURL)
			slog.Info("connected to port", "server", cfg.Server, "mcp", mcpURL)
			check() // a change while disconnected sent no signal
		case crew.FrameProfile:
			check()
		case crew.FrameDelivery:
			if f.Delivery == nil {
				continue
			}
			d := *f.Delivery
			j := newJob(d)
			jobs.add(j, time.Now())
			wg.Add(2)
			go func() {
				defer wg.Done()
				j.steerLoop(ctx, cfg, agent)
			}()
			go func() {
				defer wg.Done()
				work(ctx, cfg, agent, j, send)
				jobs.remove(d.ID)
				stateMu.Lock()
				idle := draining && jobs.count() == 0
				stateMu.Unlock()
				if idle {
					send(crew.Frame{Type: restartFrame})
				}
			}()
		case crew.FrameSteer:
			if f.Delivery == nil {
				continue
			}
			j := jobs.get(f.Delivery.ID)
			if j == nil {
				continue // already acked: the port requeues these ships
			}
			j.offer(*f.Delivery)
		case crew.FrameBrowser:
			// A person took the slot's browser, or gave it back: every
			// running turn hears it, and new turns start knowing it.
			on := f.Viewer == crew.ViewerOn
			changed, running := jobs.viewer(on)
			if !changed {
				continue
			}
			slog.Info("browser viewer", "state", f.Viewer)
			for _, j := range running {
				j.note(viewerNote(on))
			}
		case crew.FrameError:
			slog.Warn("port error", "message", f.Message)
		}
	}
}

// Deliver modes (Config.Deliver).
const (
	DeliverSteer = "steer"
	DeliverQueue = "queue"
)

// job is one delivery being worked: the ships the agent has seen so far
// (the delivery's, then every steer it took) and the steers waiting.
type job struct {
	d      ship.Delivery
	steers chan ship.Delivery
	// notes are one-line system notes for the running turn (a person took
	// the browser, gave it back); steered like messages, never acked.
	notes chan string
	stop  chan struct{} // closed when the turn is over
	// prelude goes before the delivery's prompt (the browser's state when
	// the turn starts; set by jobTable.add).
	prelude string

	mu      sync.Mutex
	handled []identity.ShipID
	done    bool
}

func newJob(d ship.Delivery) *job {
	j := &job{d: d, steers: make(chan ship.Delivery, 64), notes: make(chan string, 8), stop: make(chan struct{})}
	for _, s := range d.Ships {
		j.handled = append(j.handled, s.ID)
	}
	return j
}

// note queues a system note for the running turn; one that can't be
// queued (turn over, queue full) is dropped: it is a hint.
func (j *job) note(text string) {
	select {
	case <-j.stop:
		return
	default:
	}
	select {
	case j.notes <- text:
	default:
	}
}

// offer queues a steer without blocking the frame reader; a steer that
// can't be queued (turn over, queue full) stays unhandled and the port
// requeues its ships on ack.
func (j *job) offer(d ship.Delivery) {
	select {
	case <-j.stop:
		return
	default:
	}
	select {
	case j.steers <- d:
	default:
		slog.Warn("too many pending steers; leaving ships for the next turn", "delivery", d.ID)
	}
}

// steerLoop hands steers to the running turn in arrival order until the
// job ends. A steer the agent takes counts as handled; one it can't (the
// turn just ended) stays out of the ack and the port queues it again.
func (j *job) steerLoop(ctx context.Context, cfg Config, agent *Agent) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-j.stop:
			return
		case d := <-j.steers:
			// Fetched before mu, so an ack never waits on downloads.
			images, ok := gather(ctx, cfg, agent.TakesImages(), d.Ships)
			j.mu.Lock()
			if j.done {
				j.mu.Unlock()
				return
			}
			// Holding mu: the ack waits for this steer's outcome.
			steered, err := agent.Steer(ctx, j.d.Conversation.Thread, SteerPrompt(d, ok), images)
			if err != nil {
				slog.Warn("steer failed; ships stay for the next turn", "delivery", d.ID, "err", err)
			}
			if steered {
				for _, s := range d.Ships {
					j.handled = append(j.handled, s.ID)
				}
				slog.Info("steered into the running turn", "delivery", d.ID, "ships", len(d.Ships))
			}
			j.mu.Unlock()
		case text := <-j.notes:
			j.mu.Lock()
			if j.done {
				j.mu.Unlock()
				return
			}
			if _, err := agent.Steer(ctx, j.d.Conversation.Thread, text, nil); err != nil {
				slog.Warn("browser note not steered", "delivery", j.d.ID, "err", err)
			}
			j.mu.Unlock()
		}
	}
}

// finish ends the job and returns the ships the agent saw.
func (j *job) finish() []identity.ShipID {
	j.mu.Lock()
	defer j.mu.Unlock()
	if !j.done {
		j.done = true
		close(j.stop)
	}
	return append([]identity.ShipID(nil), j.handled...)
}

// work prompts the agent with one delivery and acks the ships it saw, or
// releases the delivery when the turn fails so the ships are retried.
func work(ctx context.Context, cfg Config, agent *Agent, j *job, send func(crew.Frame)) {
	d := j.d
	id := d.ID
	log := slog.With("delivery", id, "conversation", d.Conversation.ID, "thread", d.Conversation.Thread, "title", d.Conversation.Title)
	log.Info("delivery received", "ships", len(d.Ships))
	images, ok := gather(ctx, cfg, agent.TakesImages(), d.Ships)
	// What the agent does goes to the port's live view as it happens; the
	// last of it is sent before the ack or release.
	f := newFeed(id, send)
	answer, err := agent.Prompt(ctx, d.Conversation.Thread, j.prelude+Prompt(d, cfg.AutoReply, ok), images, f)
	handled := j.finish()
	f.close()
	if err != nil {
		// Turns failing in a row (the agent can't open sessions, its model
		// refuses...) wait longer each time before the ships come back.
		pause := agent.failed()
		log.Error("agent turn failed; releasing", "err", err, "in", pause)
		select {
		case <-time.After(pause):
		case <-ctx.Done():
		}
		send(crew.Frame{Type: crew.FrameRelease, DeliveryID: &id})
		return
	}
	agent.succeeded()
	if cfg.AutoReply && strings.TrimSpace(answer) != "" {
		if conv, ok := answerDoor(d); ok {
			send(crew.Frame{Type: crew.FrameReply, ConversationID: &conv, Text: strings.TrimSpace(answer)})
		} else {
			log.Info("answer not sent: no door of the thread takes replies")
		}
	}
	send(crew.Frame{Type: crew.FrameAck, DeliveryID: &id, ShipIDs: handled})
	log.Info("delivery handled", "ships", len(handled))
}

// Browser notes: what the agent is told when a person opens the viewer
// of its browser (they drive it now: a sign-in, a captcha) and when they
// close it.
const (
	viewerOnNote  = "[seaport] A person is using your browser now (the viewer is open), likely to sign in or solve a challenge you asked a takeover for. Do not drive the browser meanwhile: wait for them, or work on what doesn't need it. You will be told when they are done."
	viewerOffNote = "[seaport] The person closed the browser viewer: the browser is yours again. Check the page before going on (a sign-in or challenge may be done)."
)

// viewerNote is the one-line note steered into a running turn when the
// viewer opens (on) or closes.
func viewerNote(on bool) string {
	if on {
		return viewerOnNote
	}
	return viewerOffNote
}

// viewerPrelude goes before a new turn's prompt while a person is in the
// browser ("" otherwise).
func viewerPrelude(on bool) string {
	if !on {
		return ""
	}
	return viewerOnNote + "\n\n"
}

// SteerPrompt renders ships that arrived while the agent was working
// (ok: the attachments that go with it as images).
func SteerPrompt(d ship.Delivery, ok shown) string {
	var b strings.Builder
	if len(d.Thread) > 1 {
		fmt.Fprintf(&b, "[seaport] More message(s) in this thread, sent while you were working:\n")
	} else {
		fmt.Fprintf(&b, "[seaport] More message(s) in conversation %s, sent while you were working:\n", d.Conversation.ID)
	}
	writeShips(&b, d.Ships, doorLabels(d), ok)
	writeApprovals(&b, d.Approvals)
	if _, ok := answerDoor(d); ok {
		b.WriteString("\nTake them into account; reply to the conversation as before.")
	} else {
		b.WriteString("\nTake them into account; no door of this thread takes replies.")
	}
	return b.String()
}

// answerDoor is where an auto-reply goes: the delivery's conversation,
// else the first door of the thread that takes replies (none: nowhere).
func answerDoor(d ship.Delivery) (identity.ConversationID, bool) {
	if d.CanReply(d.Conversation.ID) {
		return d.Conversation.ID, true
	}
	for _, c := range d.Thread {
		if d.CanReply(c.ID) {
			return c.ID, true
		}
	}
	return identity.ConversationID{}, false
}

// doorLabels names each door of a multi-door thread for the ships' lines
// (none for a thread of one: the conversation is named once).
func doorLabels(d ship.Delivery) map[identity.ConversationID]string {
	if len(d.Thread) <= 1 {
		return nil
	}
	out := make(map[identity.ConversationID]string, len(d.Thread))
	for _, c := range d.Thread {
		out[c.ID] = doorName(c)
	}
	return out
}

// doorName is how a door is shown to the agent: its channel and title.
func doorName(c conversation.Conversation) string {
	title := c.Title
	if title == "" {
		title = c.ExternalID
	}
	return string(c.Channel) + " " + title
}

func writeShips(b *strings.Builder, ships []ship.Ship, doors map[identity.ConversationID]string, ok shown) {
	for _, s := range ships {
		from := s.SenderName
		if from == "" {
			from = s.SenderID
		}
		if door, found := doors[s.Conversation]; found {
			fmt.Fprintf(b, "[%s] (%s) %s: %s\n", s.SentAt.Local().Format("2006-01-02 15:04"), door, from, s.Text)
		} else {
			fmt.Fprintf(b, "[%s] %s: %s\n", s.SentAt.Local().Format("2006-01-02 15:04"), from, s.Text)
		}
		writeAttachments(b, s, ok)
	}
}

// Prompt renders a delivery as the agent's prompt (ok: the attachments
// that go with it as images).
func Prompt(d ship.Delivery, autoReply bool, ok shown) string {
	c := d.Conversation
	var b strings.Builder
	fmt.Fprintf(&b, "[seaport] New %s message(s)\n", c.Channel)
	fmt.Fprintf(&b, "conversation_id: %s\n", c.ID)
	title := c.Title
	if title == "" {
		title = c.ExternalID
	}
	fmt.Fprintf(&b, "conversation: %s (%s)\n", title, c.Kind)
	if !d.CanReply(c.ID) {
		b.WriteString("This conversation is read-only: nothing can be posted there (its dock only triggers, or replies are off).\n")
	}
	if len(d.Thread) > 1 {
		b.WriteString("This conversation is one door of a thread; the others, where the same matter is discussed:\n")
		for _, door := range d.Thread {
			if door.ID == c.ID {
				continue
			}
			note := ""
			if !d.CanReply(door.ID) {
				note = ", read-only"
			}
			fmt.Fprintf(&b, "- %s (%s%s): conversation_id %s\n", doorName(door), door.Kind, note, door.ID)
		}
	}
	b.WriteString("\n")
	writeShips(&b, d.Ships, doorLabels(d), ok)
	writeApprovals(&b, d.Approvals)
	b.WriteString("\n")
	if c.Channel == dock.ChannelGitHub {
		b.WriteString(githubHint(c.ExternalID))
	}
	if c.Channel == dock.ChannelJira {
		b.WriteString(jiraHint(c.ExternalID))
	}
	if c.Channel == dock.ChannelSlack {
		b.WriteString(slackHint(c.ExternalID, title))
	}
	answer, answerable := answerDoor(d)
	switch {
	case autoReply && !answerable:
		b.WriteString("No conversation of this thread takes replies: your final answer is not sent to anyone. " +
			"Do what the message asks and keep the answer short.")
	case autoReply && answer != c.ID:
		fmt.Fprintf(&b, "Your final answer will be sent as a message to conversation %s (the first door of the "+
			"thread that takes replies). Keep it short and conversational.", answer)
	case autoReply:
		b.WriteString("Your final answer will be sent to this conversation as a message. " +
			"Keep it short and conversational.")
	case !answerable:
		b.WriteString("No conversation of this thread takes replies: the `reply` tool is refused everywhere here. " +
			"Do what the message asks (its dock is a trigger) and stay silent, or use `history` for earlier context. " +
			"Your final text is not sent to anyone.")
	case !d.CanReply(c.ID):
		fmt.Fprintf(&b, "A reply to this conversation is refused; answer with the seaport `reply` tool on a door that "+
			"takes replies (conversation_id %s, or another listed above) when a reply is needed; use `history` for "+
			"earlier context (`thread: true` reads every door together). Your final text is not sent to anyone.", answer)
	default:
		b.WriteString("Reply with the seaport `reply` tool (conversation_id above) when a reply is needed; " +
			"use `history` for earlier context. Your final text is not sent to anyone.")
		if len(d.Thread) > 1 {
			b.WriteString(" Answer on the door each message came from (its conversation_id) unless asked to " +
				"post elsewhere, or it is read-only; `history` with `thread: true` reads every door of the thread together.")
		}
		b.WriteString("\n" + approvalHint)
	}
	if autoReply && answerable {
		// The final answer is a message, but a question to ask first isn't
		// the final answer.
		b.WriteString("\n" + approvalHint)
	}
	return b.String()
}

// mustThread parses a thread id kept in the sessions file.
func mustThread(raw string) (identity.ThreadID, bool) {
	id, err := identity.ParseThreadID(raw)
	return id, err == nil
}

// authHeader carries the crew token; none for an open (local) port.
func authHeader(token string) http.Header {
	if token == "" {
		return http.Header{}
	}
	return http.Header{"Authorization": {"Bearer " + token}}
}
