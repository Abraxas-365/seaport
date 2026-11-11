package connector

import (
	"strings"
	"sync"
	"testing"

	"github.com/Abraxas-365/seaport/internal/crew"
	"github.com/Abraxas-365/seaport/internal/identity"
	acp "github.com/coder/acp-go-sdk"
)

func textUpdate(thought bool, s string) acp.SessionUpdate {
	c := acp.ContentBlock{Text: &acp.ContentBlockText{Text: s, Type: "text"}}
	if thought {
		return acp.SessionUpdate{AgentThoughtChunk: &acp.SessionUpdateAgentThoughtChunk{Content: c}}
	}
	return acp.SessionUpdate{AgentMessageChunk: &acp.SessionUpdateAgentMessageChunk{Content: c}}
}

// TestFeedCoalesces: chunks of one kind join, a tool's updates merge into
// its latest state, and every frame passes the port's checks.
func TestFeedCoalesces(t *testing.T) {
	var mu sync.Mutex
	var got []crew.Frame
	d := identity.NewDeliveryID()
	f := newFeed(d, func(fr crew.Frame) { mu.Lock(); got = append(got, fr); mu.Unlock() })
	f.update(textUpdate(true, "Let me "))
	f.update(textUpdate(true, "look.\x07"))
	f.update(acp.SessionUpdate{ToolCall: &acp.SessionUpdateToolCall{ToolCallId: "t1", Title: "Bash: ls", Kind: acp.ToolKindExecute,
		Status: acp.ToolCallStatusPending, RawInput: map[string]any{"command": "cat secret"}}})
	done := acp.ToolCallStatusCompleted
	f.update(acp.SessionUpdate{ToolCallUpdate: &acp.SessionToolCallUpdate{ToolCallId: "t1", Status: &done,
		RawOutput: "TOKEN=abc"}})
	f.update(textUpdate(false, "Done."))
	f.close()

	if len(got) != 1 {
		t.Fatalf("frames: %d", len(got))
	}
	fr := got[0]
	if fr.Type != crew.FrameActivity || fr.DeliveryID == nil || *fr.DeliveryID != d {
		t.Fatalf("frame: %+v", fr)
	}
	want := []crew.ActivityItem{
		{Kind: crew.ActivityThought, Text: "Let me look."},
		{Kind: crew.ActivityTool, Tool: "t1", Title: "Bash: ls", ToolKind: "execute", Status: crew.ToolCompleted},
		{Kind: crew.ActivityMessage, Text: "Done."},
	}
	if len(fr.Items) != len(want) {
		t.Fatalf("items: %+v", fr.Items)
	}
	for i := range want {
		if fr.Items[i] != want[i] {
			t.Fatalf("item %d: %+v, want %+v", i, fr.Items[i], want[i])
		}
	}
	if err := (crew.Activity{Items: fr.Items}).Validate(); err != nil {
		t.Fatal(err)
	}
}

// TestFramesSplitLongText: a long thought (with characters JSON escapes)
// and many tools split into frames the port accepts, losing no text.
func TestFramesSplitLongText(t *testing.T) {
	long := strings.Repeat("ñ\"<\\\n", 3000)
	items := []crew.ActivityItem{{Kind: crew.ActivityThought, Text: long}}
	for i := range 50 {
		items = append(items, crew.ActivityItem{Kind: crew.ActivityTool, Tool: "t" + strings.Repeat("x", i), Title: "Read"})
	}
	var text strings.Builder
	tools := 0
	for _, fr := range frames(items) {
		if err := (crew.Activity{Items: fr}).Validate(); err != nil {
			t.Fatalf("frame refused: %v (%d items, %d bytes)", err, len(fr), crew.ActivitySize(fr))
		}
		for _, it := range fr {
			if it.Kind == crew.ActivityThought {
				text.WriteString(it.Text)
			} else {
				tools++
			}
		}
	}
	if text.String() != long || tools != 50 {
		t.Fatalf("lost content: %d of %d bytes, %d tools", text.Len(), len(long), tools)
	}
}

func TestCleanAndCut(t *testing.T) {
	if got := clean("a\x00b\tc\n\u0085d\xff"); got != "ab\tc\nd\uFFFD" {
		t.Fatalf("clean: %q", got)
	}
	if got := cut("añb", 2); got != "a" {
		t.Fatalf("cut: %q", got)
	}
}
