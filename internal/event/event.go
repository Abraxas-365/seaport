// Package event is the port's in-process event bus: services publish what
// changed (a ship arrived, a conversation was allowed, a dock linked, an
// agent connected) and live clients (the web UI over SSE) subscribe. It is
// a notification channel, not a source of truth: a client that misses
// events (slow, reconnected after a restart) is told to reset and refetch.
package event

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Event is one published change. ID is "<boot>-<seq>": unique per process
// run, ordered within it.
type Event struct {
	ID   string          `json:"id"`
	Type string          `json:"type"`
	At   time.Time       `json:"at"`
	Data json.RawMessage `json:"data"`
}

// Publisher is what services depend on.
type Publisher interface {
	Publish(typ string, data any)
}

// Nop discards events (tests, tools).
type Nop struct{}

func (Nop) Publish(string, any) {}

const (
	// history is how many recent events a reconnecting client can replay.
	history = 512
	// buffer is a subscriber's queue; a subscriber that falls this far
	// behind is closed (it reconnects and replays or resets).
	buffer = 256
)

// Bus fans events out to subscribers and keeps a short history for replay.
type Bus struct {
	boot string

	mu     sync.Mutex
	seq    uint64
	recent []Event // ring of the last `history` events, oldest first
	subs   map[*Subscription]struct{}
}

func NewBus() *Bus {
	var b [4]byte
	_, _ = rand.Read(b[:])
	return &Bus{boot: hex.EncodeToString(b[:]), subs: map[*Subscription]struct{}{}}
}

var _ Publisher = (*Bus)(nil)

// Publish marshals data once and delivers it to every subscriber.
func (b *Bus) Publish(typ string, data any) {
	raw, err := json.Marshal(data)
	if err != nil {
		slog.Error("marshal event", "type", typ, "err", err)
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.seq++
	e := Event{ID: b.boot + "-" + strconv.FormatUint(b.seq, 10), Type: typ, At: time.Now().UTC(), Data: raw}
	if len(b.recent) == history {
		copy(b.recent, b.recent[1:])
		b.recent = b.recent[:history-1]
	}
	b.recent = append(b.recent, e)
	for s := range b.subs {
		select {
		case s.c <- e:
		default:
			b.dropLocked(s)
		}
	}
}

// Subscription is one live client. C closes when the bus drops it.
type Subscription struct {
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

// Subscribe registers a client. lastID is the last event it saw (SSE
// Last-Event-ID, "" for a fresh client): the events after it are returned
// for replay, or reset=true when they are no longer known (another process
// run, or too old) and the client must refetch its state.
func (b *Bus) Subscribe(lastID string) (s *Subscription, replay []Event, reset bool) {
	c := make(chan Event, buffer)
	s = &Subscription{c: c, C: c, bus: b}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.subs[s] = struct{}{}
	if lastID == "" {
		return s, nil, false
	}
	boot, rawSeq, ok := strings.Cut(lastID, "-")
	seq, err := strconv.ParseUint(rawSeq, 10, 64)
	if !ok || err != nil || boot != b.boot || seq > b.seq {
		return s, nil, true
	}
	if seq == b.seq {
		return s, nil, false
	}
	oldest := b.seq - uint64(len(b.recent)) + 1
	if seq+1 < oldest {
		return s, nil, true
	}
	return s, append([]Event(nil), b.recent[seq+1-oldest:]...), false
}

// Boot identifies this process run (the prefix of every event ID).
func (b *Bus) Boot() string { return b.boot }

// Subscribers reports how many clients are listening.
func (b *Bus) Subscribers() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.subs)
}
