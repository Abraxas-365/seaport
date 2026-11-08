// Package event is the port's event bus: services publish what changed (a
// ship arrived, a conversation was allowed, a dock linked, an agent
// connected) and live clients (the web UI over SSE) subscribe. Events go
// through a Log (Postgres in the port: every process sees every event, and
// ids are global, so a client reconnecting to another process resumes where
// it was). It is a notification channel, not a source of truth: a client
// that misses events (slow, or reconnecting after the retention window) is
// told to reset and refetch.
package event

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"log/slog"
	"strconv"
	"sync"
	"time"

	"github.com/Abraxas-365/seaport/internal/errx"
	"github.com/Abraxas-365/seaport/internal/identity"
)

// Event is one published change of one organization. Seq is its position
// in the log (global, increasing in commit order; an organization sees
// gaps where other organizations' events were); ID is Seq as text (SSE).
type Event struct {
	ID           string                  `json:"id"`
	Seq          int64                   `json:"-"`
	Organization identity.OrganizationID `json:"-"`
	Type         string                  `json:"type"`
	At           time.Time               `json:"at"`
	Data         json.RawMessage         `json:"data"`
}

// Publisher is what services depend on. Every event belongs to the
// organization whose data changed; only its subscribers receive it.
type Publisher interface {
	Publish(organization identity.OrganizationID, typ string, data any)
}

// Nop discards events (tests, tools).
type Nop struct{}

func (Nop) Publish(identity.OrganizationID, string, any) {}

// Log stores events in order and tells its listeners when there are new
// ones. Implementations: MemoryLog (one process), eventpg (Postgres).
type Log interface {
	// Append stores an event; its Seq is greater than every Seq already
	// readable, so readers that remember the last Seq miss nothing.
	Append(ctx context.Context, organization identity.OrganizationID, typ string, data json.RawMessage) error
	// Read returns up to limit events with after < Seq <= through in Seq
	// order, of one organization or (nil) of all.
	Read(ctx context.Context, after, through int64, organization *identity.OrganizationID, limit int) ([]Event, error)
	// Head returns the newest committed Seq (0 when none), with no append
	// in flight below it: reading after it misses nothing.
	Head(ctx context.Context) (int64, error)
	// Bounds returns an upper bound of every Seq issued so far and the
	// oldest Seq still kept (above the bound when empty). Cheap: no
	// waiting for appends.
	Bounds(ctx context.Context) (issued, oldest int64, err error)
	// Sweep deletes events older than before (oldest first).
	Sweep(ctx context.Context, before time.Time) (int64, error)
	// Listen calls wake whenever events may have been appended (by any
	// process), and once after (re)connecting; it returns when ctx ends.
	Listen(ctx context.Context, wake func())
}

const (
	// buffer is a subscriber's queue; a subscriber that falls this far
	// behind is closed (it reconnects and replays or resets).
	buffer = 256
	// batch is how many events one read fans out.
	batch = 500
	// poll catches up even if a notification is lost.
	poll = 2 * time.Second
	// publishTimeout bounds one Append (Publish has no context).
	publishTimeout = 5 * time.Second
	// maxReplay is the most events a reconnecting client replays; further
	// behind, it resets (refetching is cheaper than streaming that much).
	maxReplay = 1000
)

// Bus fans the log's events out to this process's subscribers.
type Bus struct {
	log  Log
	boot string
	wake chan struct{}

	mu   sync.Mutex
	head int64 // last Seq fanned out
	subs map[*Subscription]struct{}

	started bool
	stop    context.CancelFunc
	done    chan struct{}
}

func NewBus(log Log) *Bus {
	var b [4]byte
	_, _ = rand.Read(b[:])
	return &Bus{log: log, boot: hex.EncodeToString(b[:]), wake: make(chan struct{}, 1),
		subs: map[*Subscription]struct{}{}, done: make(chan struct{})}
}

var _ Publisher = (*Bus)(nil)

// Start begins fanning out from the log's current head (older events are
// replayed from the log on demand) until Close. Subscribe needs it.
func (b *Bus) Start(ctx context.Context) error {
	head, err := b.log.Head(ctx)
	if err != nil {
		return errx.Wrap(err, "start event bus", errx.TypeInternal)
	}
	b.mu.Lock()
	if b.started {
		b.mu.Unlock()
		return errx.Internal("event bus already started")
	}
	b.started, b.head = true, head
	b.mu.Unlock()
	ctx, b.stop = context.WithCancel(context.WithoutCancel(ctx))
	listening := make(chan struct{})
	go func() {
		defer close(listening)
		b.log.Listen(ctx, b.poke)
	}()
	go func() {
		defer close(b.done)
		defer func() { <-listening }()
		t := time.NewTicker(poll)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-b.wake:
			case <-t.C:
			}
			if err := b.catchUp(ctx); err != nil && ctx.Err() == nil {
				slog.Error("read events", "err", err)
			}
		}
	}()
	return nil
}

// Close stops fanning out (subscribers stay open; the server closes them).
func (b *Bus) Close() {
	b.mu.Lock()
	stop := b.stop
	b.stop = nil
	b.mu.Unlock()
	if stop != nil {
		stop()
		<-b.done
	}
}

func (b *Bus) poke() {
	select {
	case b.wake <- struct{}{}:
	default:
	}
}

// Publish marshals data and appends it to the log; subscribers of every
// process get it from there. A failure is logged: events are
// notifications, the change itself is already stored.
func (b *Bus) Publish(organization identity.OrganizationID, typ string, data any) {
	raw, err := json.Marshal(data)
	if err != nil {
		slog.Error("marshal event", "type", typ, "err", err)
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), publishTimeout)
	defer cancel()
	if err := b.log.Append(ctx, organization, typ, raw); err != nil {
		slog.Error("publish event", "type", typ, "err", err)
		return
	}
	b.poke()
}

// catchUp fans out everything after head.
func (b *Bus) catchUp(ctx context.Context) error {
	for {
		b.mu.Lock()
		after := b.head
		b.mu.Unlock()
		events, err := b.log.Read(ctx, after, 1<<62, nil, batch)
		if err != nil {
			return err
		}
		b.mu.Lock()
		for _, e := range events {
			for s := range b.subs {
				if s.organization != e.Organization || e.Seq <= s.skip {
					continue
				}
				select {
				case s.c <- e:
				default:
					b.dropLocked(s)
				}
			}
			b.head = e.Seq
		}
		b.mu.Unlock()
		if len(events) < batch {
			return nil
		}
	}
}

// Subscription is one live client of one organization. C closes when the
// bus drops it.
type Subscription struct {
	organization identity.OrganizationID
	// skip: events up to here were seen already (the client came from a
	// process that was ahead of this one).
	skip int64
	c    chan Event
	C    <-chan Event
	once sync.Once
	bus  *Bus
}

// Close unsubscribes.
func (s *Subscription) Close() {
	s.bus.mu.Lock()
	defer s.bus.mu.Unlock()
	s.bus.dropLocked(s)
}

func (b *Bus) dropLocked(s *Subscription) {
	if _, ok := b.subs[s]; ok {
		delete(b.subs, s)
		s.once.Do(func() { close(s.c) })
	}
}

// Subscribe registers a client of an organization. lastID is the last
// event it saw (SSE Last-Event-ID, "" for a fresh client): the
// organization's events after it are returned for replay (C carries the
// ones after those), or reset=true when they are no longer known (swept,
// an id this log never issued, or too far behind) and the client must
// refetch its state.
func (b *Bus) Subscribe(ctx context.Context, organization identity.OrganizationID, lastID string) (s *Subscription, replay []Event, reset bool, err error) {
	c := make(chan Event, buffer)
	s = &Subscription{organization: organization, c: c, C: c, bus: b}
	var last int64
	if lastID != "" {
		if last, err = strconv.ParseInt(lastID, 10, 64); err != nil || last < 0 {
			last, lastID, reset = 0, "", true // not an id of this log
		}
	}
	if lastID != "" {
		issued, oldest, err := b.log.Bounds(ctx)
		if err != nil {
			return nil, nil, false, errx.Wrap(err, "event bounds", errx.TypeInternal)
		}
		// A rolled-back append leaves a gap that can look swept: a rare
		// extra reset, harmless.
		if last+1 < oldest || last > issued {
			lastID, reset = "", true // swept since, or never issued
		}
	}
	b.mu.Lock()
	if !b.started {
		b.mu.Unlock()
		return nil, nil, false, errx.Internal("event bus not started")
	}
	head := b.head
	if lastID != "" && last > head {
		s.skip = last // another process was ahead: this one will get there
	}
	b.subs[s] = struct{}{}
	b.mu.Unlock()
	if lastID == "" || last >= head {
		return s, nil, reset, nil
	}
	// (last, head] from the log; C carries everything after head.
	replay, err = b.log.Read(ctx, last, head, &organization, maxReplay+1)
	if err != nil {
		s.Close()
		return nil, nil, false, errx.Wrap(err, "replay events", errx.TypeInternal)
	}
	if len(replay) > maxReplay {
		return s, nil, true, nil
	}
	return s, replay, false, nil
}

// Sweep deletes events older than before from the log.
func (b *Bus) Sweep(ctx context.Context, before time.Time) (int64, error) {
	return b.log.Sweep(ctx, before)
}

// Boot identifies this process run.
func (b *Bus) Boot() string { return b.boot }

// Subscribers reports how many clients are listening.
func (b *Bus) Subscribers() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.subs)
}

// MemoryLog is a Log for one process (tests).
type MemoryLog struct {
	mu        sync.Mutex
	events    []Event
	seq       int64
	listeners map[int]func()
	next      int
}

func NewMemoryLog() *MemoryLog { return &MemoryLog{listeners: map[int]func(){}} }

func (m *MemoryLog) Append(_ context.Context, organization identity.OrganizationID, typ string, data json.RawMessage) error {
	m.mu.Lock()
	m.seq++
	m.events = append(m.events, Event{ID: strconv.FormatInt(m.seq, 10), Seq: m.seq, Organization: organization,
		Type: typ, At: time.Now().UTC(), Data: data})
	wakes := make([]func(), 0, len(m.listeners))
	for _, w := range m.listeners {
		wakes = append(wakes, w)
	}
	m.mu.Unlock()
	for _, w := range wakes {
		w()
	}
	return nil
}

func (m *MemoryLog) Read(_ context.Context, after, through int64, organization *identity.OrganizationID, limit int) ([]Event, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []Event
	for _, e := range m.events {
		if e.Seq > after && e.Seq <= through && (organization == nil || e.Organization == *organization) {
			out = append(out, e)
			if len(out) == limit {
				break
			}
		}
	}
	return out, nil
}

func (m *MemoryLog) Head(context.Context) (int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.seq, nil
}

func (m *MemoryLog) Bounds(context.Context) (int64, int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.events) == 0 {
		return m.seq, m.seq + 1, nil
	}
	return m.seq, m.events[0].Seq, nil
}

func (m *MemoryLog) Sweep(_ context.Context, before time.Time) (int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	n := 0
	for n < len(m.events) && m.events[n].At.Before(before) {
		n++
	}
	m.events = m.events[n:]
	return int64(n), nil
}

func (m *MemoryLog) Listen(ctx context.Context, wake func()) {
	m.mu.Lock()
	id := m.next
	m.next++
	m.listeners[id] = wake
	m.mu.Unlock()
	wake()
	<-ctx.Done()
	m.mu.Lock()
	delete(m.listeners, id)
	m.mu.Unlock()
}
