package crewws

import (
	"context"
	"log/slog"
	"time"

	"github.com/Abraxas-365/seaport/internal/crew"
	"github.com/Abraxas-365/seaport/internal/identity"
	"github.com/Abraxas-365/seaport/internal/ship"
)

// maxOmittedIDs bounds the tool ids remembered past ship.MaxTurnTools
// (so their updates aren't counted again); beyond it every new id counts.
const maxOmittedIDs = 1000

// turnLog gathers the tool calls of one delivery from its activity
// frames, timed by the hub, for the turn summary stored when it ends.
type turnLog struct {
	tools   []ship.TurnTool
	index   map[string]int
	omitted map[string]struct{}
	extra   int
}

// note applies a frame's tool items.
func (l *turnLog) note(items []crew.ActivityItem, now time.Time) {
	for _, it := range items {
		if it.Kind != crew.ActivityTool {
			continue
		}
		if l.index == nil {
			l.index = map[string]int{}
		}
		i, ok := l.index[it.Tool]
		if !ok {
			if len(l.tools) >= ship.MaxTurnTools {
				if _, seen := l.omitted[it.Tool]; !seen {
					l.extra++
					if l.omitted == nil {
						l.omitted = map[string]struct{}{}
					}
					if len(l.omitted) < maxOmittedIDs {
						l.omitted[it.Tool] = struct{}{}
					}
				}
				continue
			}
			l.tools = append(l.tools, ship.TurnTool{Title: "Tool", Status: crew.ToolPending, StartedAt: now})
			i = len(l.tools) - 1
			l.index[it.Tool] = i
		}
		t := &l.tools[i]
		if it.Title != "" {
			t.Title = it.Title
		}
		if it.ToolKind != "" {
			t.Kind = it.ToolKind
		}
		if it.Status != "" {
			t.Status = it.Status
		}
		if (t.Status == crew.ToolCompleted || t.Status == crew.ToolFailed) && t.EndedAt == nil {
			end := now
			t.EndedAt = &end
		}
	}
}

// recordTurn stores what the agent did on a delivery that ended (best
// effort: a summary that can't be stored is logged, never an error of the
// ack). Turns without tool calls are not stored.
func (h *Hub) recordTurn(ctx context.Context, c *conn, id identity.DeliveryID, t *tracked, outcome ship.TurnOutcome) {
	if t == nil || len(t.turn.tools) == 0 {
		return
	}
	turn := ship.Turn{Delivery: id, Conversation: t.conversation, Agent: c.agentID, AgentName: c.agentName,
		Outcome: outcome, StartedAt: t.since, EndedAt: time.Now().UTC(), Tools: t.turn.tools, Omitted: t.turn.extra}
	if err := h.commands().RecordTurn(ctx, c.organization, turn); err != nil {
		slog.Warn("record turn", "delivery", id, "err", err)
	}
}
