package ship_test

import (
	"strings"
	"testing"
	"time"

	"github.com/Abraxas-365/seaport/internal/identity"
	"github.com/Abraxas-365/seaport/internal/ship"
	"github.com/Abraxas-365/seaport/internal/text"
)

func TestToolTitle(t *testing.T) {
	got := ship.ToolTitle("  Bash: export KEY=sk-ant-" + strings.Repeat("x", 30) + "\x00 ok ")
	if got != "Bash: export KEY="+text.Redacted+" ok" {
		t.Fatalf("got %q", got)
	}
	long := ship.ToolTitle(strings.Repeat("é", ship.MaxToolTitle+10))
	if n := len([]rune(long)); n != ship.MaxToolTitle || !strings.HasSuffix(long, "…") {
		t.Fatalf("long title: %d runes", n)
	}
}

func TestTurnValidate(t *testing.T) {
	now := time.Now()
	ok := ship.Turn{Delivery: identity.NewDeliveryID(), Conversation: identity.NewConversationID(), Agent: identity.NewAgentID(),
		AgentName: "qa", Outcome: ship.TurnAcked, StartedAt: now, EndedAt: now.Add(time.Second),
		Tools: []ship.TurnTool{{Title: "Read", Status: "completed", StartedAt: now}}}
	if err := ok.Validate(); err != nil {
		t.Fatal(err)
	}
	for name, mut := range map[string]func(*ship.Turn){
		"no tools":    func(t *ship.Turn) { t.Tools = nil },
		"bad outcome": func(t *ship.Turn) { t.Outcome = "x" },
		"bad status":  func(t *ship.Turn) { t.Tools = []ship.TurnTool{{Title: "R", Status: "done", StartedAt: now}} },
		"backwards":   func(t *ship.Turn) { t.EndedAt = now.Add(-time.Second) },
		"no title":    func(t *ship.Turn) { t.Tools = []ship.TurnTool{{Status: "completed", StartedAt: now}} },
	} {
		bad := ok
		mut(&bad)
		if bad.Validate() == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}
