// Package connector is the agent side of seaport (`seaport connect`): it dials
// the port's /crew/ws, spawns an ACP agent (rness --acp, ...) and turns each
// delivery into a prompt on that conversation's agent session. The agent
// answers through the seaport MCP tools, or, with AutoReply, the connector
// sends the agent's final text.
package connector

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/Abraxas-365/seaport/internal/crew"
	"github.com/Abraxas-365/seaport/internal/errx"
	"github.com/Abraxas-365/seaport/internal/identity"
	"github.com/Abraxas-365/seaport/internal/ship"
	"github.com/coder/websocket"
)

// Config of a connector.
type Config struct {
	Server      string // http(s)://host:port of the port
	Token       string // crew token ("" for a port in SEAPORT_AUTH=none)
	AgentName   string
	Concurrency int
	AutoReply   bool
	// Approve answers agent permission requests: "reject" (default) or "allow".
	Approve string
	// Deliver says what happens to messages that arrive while the agent is
	// working on the conversation: "steer" (default) hands them to the
	// running turn when the agent supports it (rness); "queue" sends them
	// as the next prompt after the turn ends.
	Deliver      string
	Cwd          string
	Command      []string
	SessionsFile string
}

// Run connects to the port and keeps the agent working until ctx ends,
// reconnecting with backoff.
func Run(ctx context.Context, cfg Config) error {
	if len(cfg.Command) == 0 {
		return errx.Validation("an agent command is required: seaport connect -- rness --acp")
	}
	agent, err := StartAgent(ctx, cfg)
	if err != nil {
		return err
	}
	defer agent.Close()

	backoff := time.Second
	for {
		started := time.Now()
		err := session(ctx, cfg, agent)
		if ctx.Err() != nil {
			return nil
		}
		select {
		case <-agent.Done():
			return errx.Internal("agent process exited")
		default:
		}
		if time.Since(started) > time.Minute {
			backoff = time.Second
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

// session runs one WebSocket connection.
func session(ctx context.Context, cfg Config, agent *Agent) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	ws, resp, err := websocket.Dial(ctx, wsURL(cfg.Server), &websocket.DialOptions{HTTPHeader: authHeader(cfg.Token)})
	if err != nil {
		if resp != nil && resp.StatusCode == http.StatusUnauthorized {
			if cfg.Token == "" {
				return errx.Unauthorized("the port requires a crew token (-token or SEAPORT_CREW_TOKEN)")
			}
			return errx.Unauthorized("the port rejected the crew token")
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
	send(crew.Frame{Type: crew.FrameHello, Agent: cfg.AgentName, Concurrency: cfg.Concurrency, Steer: steering})

	var wg sync.WaitGroup
	defer wg.Wait()
	jobs := map[identity.DeliveryID]*job{}
	var jobsMu sync.Mutex
	for {
		_, raw, err := ws.Read(ctx)
		if err != nil {
			return err
		}
		f, err := crew.Decode(raw)
		if err != nil {
			continue
		}
		switch f.Type {
		case crew.FrameWelcome:
			agent.SetMCPURL(f.MCPURL)
			slog.Info("connected to port", "server", cfg.Server, "mcp", f.MCPURL)
		case crew.FrameDelivery:
			if f.Delivery == nil {
				continue
			}
			d := *f.Delivery
			j := newJob(d)
			jobsMu.Lock()
			jobs[d.ID] = j
			jobsMu.Unlock()
			wg.Add(2)
			go func() {
				defer wg.Done()
				j.steerLoop(ctx, agent)
			}()
			go func() {
				defer wg.Done()
				work(ctx, cfg, agent, j, send)
				jobsMu.Lock()
				delete(jobs, d.ID)
				jobsMu.Unlock()
			}()
		case crew.FrameSteer:
			if f.Delivery == nil {
				continue
			}
			jobsMu.Lock()
			j := jobs[f.Delivery.ID]
			jobsMu.Unlock()
			if j == nil {
				continue // already acked: the port requeues these ships
			}
			j.offer(*f.Delivery)
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
	stop   chan struct{} // closed when the turn is over

	mu      sync.Mutex
	handled []identity.ShipID
	done    bool
}

func newJob(d ship.Delivery) *job {
	j := &job{d: d, steers: make(chan ship.Delivery, 64), stop: make(chan struct{})}
	for _, s := range d.Ships {
		j.handled = append(j.handled, s.ID)
	}
	return j
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
func (j *job) steerLoop(ctx context.Context, agent *Agent) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-j.stop:
			return
		case d := <-j.steers:
			j.mu.Lock()
			if j.done {
				j.mu.Unlock()
				return
			}
			// Holding mu: the ack waits for this steer's outcome.
			steered, err := agent.Steer(ctx, j.d.Conversation.ID, SteerPrompt(d))
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
	log := slog.With("delivery", id, "conversation", d.Conversation.ID, "title", d.Conversation.Title)
	log.Info("delivery received", "ships", len(d.Ships))
	answer, err := agent.Prompt(ctx, d.Conversation.ID, Prompt(d, cfg.AutoReply))
	handled := j.finish()
	if err != nil {
		log.Error("agent turn failed; releasing", "err", err)
		send(crew.Frame{Type: crew.FrameRelease, DeliveryID: &id})
		return
	}
	if cfg.AutoReply && strings.TrimSpace(answer) != "" {
		conv := d.Conversation.ID
		send(crew.Frame{Type: crew.FrameReply, ConversationID: &conv, Text: strings.TrimSpace(answer)})
	}
	send(crew.Frame{Type: crew.FrameAck, DeliveryID: &id, ShipIDs: handled})
	log.Info("delivery handled", "ships", len(handled))
}

// SteerPrompt renders ships that arrived while the agent was working.
func SteerPrompt(d ship.Delivery) string {
	var b strings.Builder
	fmt.Fprintf(&b, "[seaport] More message(s) in conversation %s, sent while you were working:\n", d.Conversation.ID)
	writeShips(&b, d.Ships)
	b.WriteString("\nTake them into account; reply to the conversation as before.")
	return b.String()
}

func writeShips(b *strings.Builder, ships []ship.Ship) {
	for _, s := range ships {
		from := s.SenderName
		if from == "" {
			from = s.SenderID
		}
		fmt.Fprintf(b, "[%s] %s: %s\n", s.SentAt.Local().Format("2006-01-02 15:04"), from, s.Text)
	}
}

// Prompt renders a delivery as the agent's prompt.
func Prompt(d ship.Delivery, autoReply bool) string {
	c := d.Conversation
	var b strings.Builder
	fmt.Fprintf(&b, "[seaport] New %s message(s)\n", c.Channel)
	fmt.Fprintf(&b, "conversation_id: %s\n", c.ID)
	title := c.Title
	if title == "" {
		title = c.ExternalID
	}
	fmt.Fprintf(&b, "conversation: %s (%s)\n\n", title, c.Kind)
	writeShips(&b, d.Ships)
	b.WriteString("\n")
	if autoReply {
		b.WriteString("Your final answer will be sent to this conversation as a message. " +
			"Keep it short and conversational.")
	} else {
		b.WriteString("Reply with the seaport `reply` tool (conversation_id above) when a reply is needed; " +
			"use `history` for earlier context. Your final text is not sent to anyone.")
	}
	return b.String()
}

// mustConversation parses a conversation id kept in the sessions file.
func mustConversation(raw string) (identity.ConversationID, bool) {
	id, err := identity.ParseConversationID(raw)
	return id, err == nil
}

// authHeader carries the crew token; none for an open (local) port.
func authHeader(token string) http.Header {
	if token == "" {
		return http.Header{}
	}
	return http.Header{"Authorization": {"Bearer " + token}}
}
