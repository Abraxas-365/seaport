package connector

import (
	"sync"
	"time"
	"unicode/utf8"

	"github.com/Abraxas-365/seaport/internal/crew"
	"github.com/Abraxas-365/seaport/internal/identity"
	acp "github.com/coder/acp-go-sdk"
)

// activityEvery is how often a turn's activity goes to the port.
const activityEvery = 300 * time.Millisecond

// maxPendingText bounds the thought/message text a feed holds between two
// flushes (a port that can't keep up loses the excess, not the turn).
const maxPendingText = 64 << 10

// feed gathers what the agent does in one turn (ACP session updates) and
// sends it to the port as activity frames of the delivery: text chunks of
// one kind coalesce, a tool's updates merge into its latest state. Adding
// never blocks (it runs in the ACP reader); a goroutine sends.
type feed struct {
	delivery identity.DeliveryID
	send     func(crew.Frame)

	mu      sync.Mutex
	pending []crew.ActivityItem
	text    int
	stop    chan struct{}
	done    chan struct{}
}

func newFeed(delivery identity.DeliveryID, send func(crew.Frame)) *feed {
	f := &feed{delivery: delivery, send: send, stop: make(chan struct{}), done: make(chan struct{})}
	go f.loop()
	return f
}

func (f *feed) loop() {
	defer close(f.done)
	t := time.NewTicker(activityEvery)
	defer t.Stop()
	for {
		select {
		case <-f.stop:
			f.flush()
			return
		case <-t.C:
			f.flush()
		}
	}
}

// close sends what is left and stops; frames sent after it returns (the
// ack) come after the last activity.
func (f *feed) close() {
	close(f.stop)
	<-f.done
}

// update records one ACP session update.
func (f *feed) update(u acp.SessionUpdate) {
	switch {
	case u.AgentThoughtChunk != nil && u.AgentThoughtChunk.Content.Text != nil:
		f.addText(crew.ActivityThought, u.AgentThoughtChunk.Content.Text.Text)
	case u.AgentMessageChunk != nil && u.AgentMessageChunk.Content.Text != nil:
		f.addText(crew.ActivityMessage, u.AgentMessageChunk.Content.Text.Text)
	case u.ToolCall != nil:
		f.addTool(crew.ActivityItem{Kind: crew.ActivityTool, Tool: string(u.ToolCall.ToolCallId),
			Title: u.ToolCall.Title, ToolKind: string(u.ToolCall.Kind), Status: string(u.ToolCall.Status)})
	case u.ToolCallUpdate != nil:
		it := crew.ActivityItem{Kind: crew.ActivityTool, Tool: string(u.ToolCallUpdate.ToolCallId)}
		if u.ToolCallUpdate.Title != nil {
			it.Title = *u.ToolCallUpdate.Title
		}
		if u.ToolCallUpdate.Kind != nil {
			it.ToolKind = string(*u.ToolCallUpdate.Kind)
		}
		if u.ToolCallUpdate.Status != nil {
			it.Status = string(*u.ToolCallUpdate.Status)
		}
		f.addTool(it)
	}
}

func (f *feed) addText(kind, s string) {
	s = clean(s)
	if s == "" {
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if room := maxPendingText - f.text; len(s) > room {
		s = cut(s, max(room, 0))
		if s == "" {
			return
		}
	}
	f.text += len(s)
	if n := len(f.pending); n > 0 && f.pending[n-1].Kind == kind {
		f.pending[n-1].Text += s
		return
	}
	f.pending = append(f.pending, crew.ActivityItem{Kind: kind, Text: s})
}

func (f *feed) addTool(it crew.ActivityItem) {
	if it.Tool == "" || len(it.Tool) > crew.MaxToolID {
		return
	}
	it.Title = cut(clean(it.Title), 300)
	it.ToolKind = cut(clean(it.ToolKind), 32)
	if !validStatus(it.Status) {
		it.Status = ""
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	for i := range f.pending {
		p := &f.pending[i]
		if p.Kind == crew.ActivityTool && p.Tool == it.Tool {
			if it.Title != "" {
				p.Title = it.Title
			}
			if it.ToolKind != "" {
				p.ToolKind = it.ToolKind
			}
			if it.Status != "" {
				p.Status = it.Status
			}
			return
		}
	}
	f.pending = append(f.pending, it)
}

func validStatus(s string) bool {
	switch s {
	case "", crew.ToolPending, crew.ToolInProgress, crew.ToolCompleted, crew.ToolFailed:
		return true
	}
	return false
}

// flush sends the pending items, split into frames within the protocol's
// limits (long texts in several items).
func (f *feed) flush() {
	f.mu.Lock()
	items := f.pending
	f.pending, f.text = nil, 0
	f.mu.Unlock()
	for _, frame := range frames(items) {
		id := f.delivery
		f.send(crew.Frame{Type: crew.FrameActivity, DeliveryID: &id, Items: frame})
	}
}

// textPiece is the most text one item carries (well under
// crew.MaxActivityBytes even when every character needs escaping).
const textPiece = 1200

// frames splits items into batches that pass crew.Activity.Validate.
func frames(items []crew.ActivityItem) [][]crew.ActivityItem {
	var out [][]crew.ActivityItem
	var cur []crew.ActivityItem
	push := func(it crew.ActivityItem) {
		if len(cur) > 0 && (len(cur) >= crew.MaxActivityItems ||
			crew.ActivitySize(append(cur[:len(cur):len(cur)], it)) > crew.MaxActivityBytes) {
			out = append(out, cur)
			cur = nil
		}
		cur = append(cur, it)
	}
	for _, it := range items {
		if it.Kind == crew.ActivityTool {
			push(it)
			continue
		}
		for s := it.Text; s != ""; {
			piece := cut(s, textPiece)
			s = s[len(piece):]
			push(crew.ActivityItem{Kind: it.Kind, Text: piece})
		}
	}
	if len(cur) > 0 {
		out = append(out, cur)
	}
	return out
}

// clean makes s valid UTF-8 without the control characters the port
// refuses (keeps newlines and tabs).
func clean(s string) string {
	if !utf8.ValidString(s) {
		s = string([]rune(s))
	}
	ok := true
	for _, r := range s {
		if forbidden(r) {
			ok = false
			break
		}
	}
	if ok {
		return s
	}
	b := make([]rune, 0, len(s))
	for _, r := range s {
		if !forbidden(r) {
			b = append(b, r)
		}
	}
	return string(b)
}

func forbidden(r rune) bool {
	return (r < 0x20 && r != '\n' && r != '\t' && r != '\r') || (r >= 0x7f && r < 0xa0)
}

// cut is the longest prefix of s of at most n bytes that ends on a rune.
func cut(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
}
