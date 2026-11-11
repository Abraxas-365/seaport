package crewws

import (
	"strconv"
	"testing"
	"time"

	"github.com/Abraxas-365/seaport/internal/crew"
	"github.com/Abraxas-365/seaport/internal/ship"
)

func TestTurnLogMergesAndCaps(t *testing.T) {
	var l turnLog
	t0 := time.Unix(100, 0)
	l.note([]crew.ActivityItem{
		{Kind: crew.ActivityThought, Text: "ignored"},
		{Kind: crew.ActivityTool, Tool: "a", Title: "Read: x", ToolKind: "read", Status: crew.ToolInProgress},
	}, t0)
	l.note([]crew.ActivityItem{{Kind: crew.ActivityTool, Tool: "a", Status: crew.ToolCompleted}}, t0.Add(2*time.Second))
	if len(l.tools) != 1 {
		t.Fatalf("tools: %+v", l.tools)
	}
	a := l.tools[0]
	if a.Title != "Read: x" || a.Kind != "read" || a.Status != crew.ToolCompleted || a.EndedAt == nil || a.EndedAt.Sub(a.StartedAt) != 2*time.Second {
		t.Fatalf("a: %+v", a)
	}
	for i := 0; i < ship.MaxTurnTools+5; i++ {
		l.note([]crew.ActivityItem{{Kind: crew.ActivityTool, Tool: "t" + strconv.Itoa(i), Title: "x"}}, t0)
	}
	// Updates of an omitted call aren't counted twice.
	l.note([]crew.ActivityItem{{Kind: crew.ActivityTool, Tool: "t" + strconv.Itoa(ship.MaxTurnTools+4), Status: crew.ToolCompleted}}, t0)
	if len(l.tools) != ship.MaxTurnTools || l.extra != 6 {
		t.Fatalf("kept %d, omitted %d", len(l.tools), l.extra)
	}
}
