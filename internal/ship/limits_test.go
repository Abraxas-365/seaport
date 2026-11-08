package ship_test

import (
	"testing"
	"time"

	"github.com/Abraxas-365/puerto/internal/errx"
	"github.com/Abraxas-365/puerto/internal/ship"
)

func TestLimitsCheck(t *testing.T) {
	now := time.Date(2026, 11, 8, 12, 0, 0, 0, time.UTC)
	l := ship.Limits{PerMinute: 3, PerHour: 10, ConversationPerMinute: 2}
	cases := []struct {
		name  string
		c     ship.OutboundCounts
		retry int // 0: allowed
	}{
		{"empty", ship.OutboundCounts{}, 0},
		{"under every limit", ship.OutboundCounts{DockLastMinute: 2, DockLastHour: 9, ConversationLastMinute: 1}, 0},
		{"conversation full", ship.OutboundCounts{DockLastMinute: 2, DockLastHour: 2, ConversationLastMinute: 2,
			OldestConversationMinute: now.Add(-50 * time.Second)}, 11},
		{"dock minute full", ship.OutboundCounts{DockLastMinute: 3, DockLastHour: 3,
			OldestDockMinute: now.Add(-30 * time.Second)}, 31},
		{"dock hour full", ship.OutboundCounts{DockLastMinute: 0, DockLastHour: 10,
			OldestDockHour: now.Add(-59 * time.Minute)}, 61},
		{"window just freed", ship.OutboundCounts{DockLastMinute: 3, OldestDockMinute: now.Add(-2 * time.Minute)}, 1},
		{"longest wait wins", ship.OutboundCounts{ConversationLastMinute: 2, OldestConversationMinute: now.Add(-50 * time.Second),
			DockLastHour: 10, OldestDockHour: now.Add(-30 * time.Minute)}, 1801},
		{"limit lowered below the count", ship.OutboundCounts{DockLastMinute: 7, OldestDockMinute: now.Add(-10 * time.Second)}, 51},
	}
	for _, c := range cases {
		c.c.At = now
		err := l.Check(c.c)
		if c.retry == 0 {
			if err != nil {
				t.Errorf("%s: %v", c.name, err)
			}
			continue
		}
		var e *errx.Error
		if !errx.As(err, &e) || e.HTTPStatus != 429 || e.Details["retry_after_seconds"] != c.retry {
			t.Errorf("%s: got %v (details %v), want 429 retry %d", c.name, err, e.Details, c.retry)
		}
	}
	// Zero disables a limit.
	if err := (ship.Limits{}).Check(ship.OutboundCounts{DockLastMinute: 1000, DockLastHour: 1000, ConversationLastMinute: 1000, At: now}); err != nil {
		t.Errorf("zero limits: %v", err)
	}
}
