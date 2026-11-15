package hostingsvc

import (
	"bufio"
	"context"
	"errors"
	"io"
	"log/slog"
	"slices"
	"strconv"
	"sync"
	"time"

	"github.com/Abraxas-365/seaport/internal/crew"
	"github.com/Abraxas-365/seaport/internal/errx"
	"github.com/Abraxas-365/seaport/internal/hosting"
	"github.com/Abraxas-365/seaport/internal/identity"
	"github.com/Abraxas-365/seaport/internal/text"
)

// The browser viewer (hosting/browser.go): one per slot per process, run
// in the slot's container through the runtime (Runtime.Exec of
// BrowserRelayCommand), lines in and out.

// browserOpenTimeout is how long the relay gets to say hello.
const browserOpenTimeout = 20 * time.Second

// viewers are this process's open viewers, by slot.
type viewers struct {
	mu   sync.Mutex
	open map[hosting.SlotRef]bool
}

func (v *viewers) claim(ref hosting.SlotRef) bool {
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.open == nil {
		v.open = map[hosting.SlotRef]bool{}
	}
	if v.open[ref] {
		return false
	}
	v.open[ref] = true
	return true
}

func (v *viewers) release(ref hosting.SlotRef) {
	v.mu.Lock()
	delete(v.open, ref)
	v.mu.Unlock()
}

func (s *Service) OpenBrowser(ctx context.Context, org identity.OrganizationID, input hosting.SlotAction) (hosting.BrowserConn, error) {
	if err := input.Validate(); err != nil {
		return nil, err
	}
	h, err := s.repo.Hosted(ctx, org, input.Agent)
	if err != nil {
		return nil, err
	}
	i := slices.IndexFunc(h.Slots, func(sl hosting.Slot) bool { return sl.Index == input.Index })
	if i < 0 {
		return nil, errx.NotFound("the agent has no slot " + strconv.Itoa(input.Index))
	}
	if s.runtime() == nil {
		return nil, errx.Business("hosted agents are off on this port (SEAPORT_RUNTIME=off)")
	}
	env, err := s.repo.Environment(ctx, org, h.Environment, 0)
	if err != nil {
		return nil, err
	}
	if !env.Spec.Browser.Enabled() {
		return nil, errx.Business("environment " + env.Name + " has no browser: set browser.engine to " + hosting.BrowserChromium)
	}
	p, err := s.agents.Profile(ctx, org, input.Agent, 0)
	if err != nil {
		return nil, err
	}
	if !p.Browser {
		return nil, errx.Business("the agent's profile has its browser off: turn it on first (the slot starts the browser with it)")
	}
	if h.Slots[i].State != hosting.SlotRunning {
		return nil, errx.Business("the slot isn't running: start it first")
	}
	ref := hosting.SlotRef{Organization: org, Agent: input.Agent, Index: input.Index}
	if !s.viewers.claim(ref) {
		return nil, errx.Conflict("someone is already viewing this slot's browser; one viewer at a time")
	}
	by := text.Clean(input.By)
	c := s.startViewer(ref, by)
	// The relay takes a moment to connect and says hello (its first
	// line); a failure before that is the open's error (the browser isn't
	// running, no container…).
	octx, cancel := context.WithTimeout(ctx, browserOpenTimeout)
	defer cancel()
	first, err := c.Read(octx)
	if err != nil {
		c.Close()
		if errors.Is(err, io.EOF) || errors.Is(err, context.DeadlineExceeded) {
			return nil, errx.Business("the slot's browser did not answer: " + c.lastLine())
		}
		return nil, err
	}
	if string(first) != hosting.BrowserHello {
		c.Close()
		return nil, errx.Business("the slot's browser is not reachable: " + text.Clean(string(first)))
	}
	s.events.Publish(org, hosting.EventSlotBrowser, hosting.BrowserEvent{Agent: input.Agent, Slot: input.Index, State: hosting.BrowserOpened, By: by})
	s.viewerChanged(org, input.Agent, input.Index, crew.ViewerOn)
	return c, nil
}

// viewerChanged tells the slot's connection a person is in its browser,
// or left it (a hint for the agent; nothing waits on it).
func (s *Service) viewerChanged(org identity.OrganizationID, agent identity.AgentID, slot int, viewer string) {
	if v := s.slots.cfg.Viewers; v != nil {
		v.ViewerChanged(org, agent, slot, viewer)
	}
}

// startViewer runs the relay in the slot in the service's background and
// hands back its pipe. The relay answers a first line (a hello) so the
// caller knows it is connected.
func (s *Service) startViewer(ref hosting.SlotRef, by string) *viewer {
	ctx, cancel := context.WithTimeout(context.Background(), hosting.BrowserMax)
	stdin, feed := io.Pipe()
	outR, outW := io.Pipe()
	v := &viewer{cancel: cancel, feed: feed, msgs: make(chan []byte, 8),
		idle: time.AfterFunc(hosting.BrowserIdle, cancel), failed: make(chan error, 1)}
	go v.readLines(bufio.NewReaderSize(outR, 64<<10)) // ends with outW
	s.background(func(sctx context.Context) {
		defer s.viewers.release(ref)
		defer outW.Close()
		go func() { // the service closing ends the viewer
			select {
			case <-sctx.Done():
				cancel()
			case <-ctx.Done():
			}
		}()
		exit, err := s.runtime().Exec(ctx, ref, "", hosting.BrowserRelayCommand, stdin, &relayOut{w: outW, v: v})
		switch {
		case err != nil && ctx.Err() == nil:
			v.fail(err)
		case err == nil && exit != 0 && ctx.Err() == nil:
			v.fail(errx.Business("the browser relay ended (exit " + strconv.Itoa(exit) + "): " + v.lastLine()))
		default:
			v.fail(io.EOF)
		}
		cancel()
		s.events.Publish(ref.Organization, hosting.EventSlotBrowser, hosting.BrowserEvent{Agent: ref.Agent, Slot: ref.Index, State: hosting.BrowserClosed, By: by})
		s.viewerChanged(ref.Organization, ref.Agent, ref.Index, crew.ViewerOff)
		slog.Info("browser viewer closed", "agent", ref.Agent, "slot", ref.Index, "by", by)
	})
	return v
}

// viewer is a BrowserConn over the exec's stdin/stdout.
type viewer struct {
	cancel context.CancelFunc
	feed   *io.PipeWriter
	// msgs carries the relay's lines; closed when it ends.
	msgs   chan []byte
	idle   *time.Timer
	failed chan error
	mu     sync.Mutex
	last   string
	done   bool
}

// readLines is the one reader of the relay's output.
func (v *viewer) readLines(r *bufio.Reader) {
	defer close(v.msgs)
	for {
		line, err := readLine(r, hosting.MaxBrowserMessage)
		if err != nil {
			if !errors.Is(err, io.EOF) && !errors.Is(err, io.ErrClosedPipe) {
				v.fail(err)
			}
			return
		}
		v.msgs <- line
	}
}

func (v *viewer) fail(err error) {
	select {
	case v.failed <- err:
	default:
	}
}

func (v *viewer) lastLine() string {
	v.mu.Lock()
	defer v.mu.Unlock()
	return v.last
}

// Read answers the next line from the relay. When the relay ended, the
// error is the exec's typed one (or io.EOF for a clean end).
func (v *viewer) Read(ctx context.Context) ([]byte, error) {
	select {
	case line, ok := <-v.msgs:
		if !ok {
			select {
			case err := <-v.failed:
				return nil, err
			default:
				return nil, io.EOF
			}
		}
		v.idle.Reset(hosting.BrowserIdle)
		return line, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (v *viewer) Write(ctx context.Context, msg []byte) error {
	if len(msg) > hosting.MaxBrowserMessage {
		return errx.Validation("CDP message too big")
	}
	v.idle.Reset(hosting.BrowserIdle)
	done := make(chan error, 1)
	go func() {
		_, err := v.feed.Write(append(slices.Clone(msg), '\n'))
		done <- err
	}()
	select {
	case err := <-done:
		if err != nil {
			return io.EOF
		}
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (v *viewer) Close() error {
	v.mu.Lock()
	if v.done {
		v.mu.Unlock()
		return nil
	}
	v.done = true
	v.mu.Unlock()
	v.idle.Stop()
	_ = v.feed.Close()
	v.cancel()
	return nil
}

// relayOut takes the relay's output (stdout and stderr, demuxed by the
// runtime): lines reach the reader; the last short line is kept for the
// failure message.
type relayOut struct {
	w io.Writer
	v *viewer
}

func (r *relayOut) Write(p []byte) (int, error) {
	if len(p) < 300 {
		r.v.mu.Lock()
		r.v.last = text.Clean(string(p))
		r.v.mu.Unlock()
	}
	return r.w.Write(p)
}

// readLine reads one newline-terminated message of at most max bytes.
func readLine(r *bufio.Reader, max int) ([]byte, error) {
	var out []byte
	for {
		chunk, err := r.ReadSlice('\n')
		out = append(out, chunk...)
		if len(out) > max {
			return nil, errx.Validation("CDP message too big")
		}
		if err == nil {
			return out[:len(out)-1], nil
		}
		if err != bufio.ErrBufferFull {
			if len(out) > 0 && err == io.EOF {
				return out, nil
			}
			return nil, err
		}
	}
}
