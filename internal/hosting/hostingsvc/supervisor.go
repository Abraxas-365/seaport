package hostingsvc

import (
	"context"
	"log/slog"
	"time"

	"github.com/Abraxas-365/seaport/internal/hosting"
	"github.com/Abraxas-365/seaport/internal/identity"
)

// failedRetry: wake starts a failed slot again only this long after it
// failed (a broken environment doesn't restart every tick).
const failedRetry = 5 * time.Minute

// Wake asks the supervisor to look now: the agent has new work (the ships'
// Notifier). It never blocks.
func (s *Service) Wake(identity.OrganizationID, identity.AgentID) {
	select {
	case s.slots.poke <- struct{}{}:
	default:
	}
}

// Supervise runs the supervisor until ctx ends: every SupervisePeriod (and
// on Wake) it wakes hosted agents with work and no slot up, stops idle
// slots, restarts outdated idle ones, fails slots whose container died
// or whose start or stop was interrupted, and removes what the runtime
// keeps of slots that no longer exist. Every process runs one: each step
// is a conditional transition, so two never act on one slot.
func (s *Service) Supervise(ctx context.Context) {
	if s.runtime() == nil {
		return
	}
	period := s.slots.cfg.Period
	if period <= 0 {
		period = hosting.SupervisePeriod
	}
	t := time.NewTicker(period)
	defer t.Stop()
	orphans := map[string]bool{}
	for {
		if err := s.supervise(ctx, orphans); err != nil && ctx.Err() == nil {
			slog.Warn("supervise slots", "err", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		case <-s.slots.poke:
		}
	}
}

// supervise is one round. orphans are the runtime slots with no slot row
// seen last round: removed when still orphan now (a slot hosted between
// the two reads isn't taken for one).
func (s *Service) supervise(ctx context.Context, orphans map[string]bool) error {
	port, err := s.portID(ctx)
	if err != nil {
		return err
	}
	rows, err := s.repo.Supervise(ctx)
	if err != nil {
		return err
	}
	// Read after the rows: a slot started since isn't in rows as running.
	have, err := s.runtime().Slots(ctx, port)
	if err != nil {
		return err
	}
	runtime := make(map[string]hosting.RuntimeSlot, len(have))
	for _, r := range have {
		runtime[r.Slot.Key()] = r
	}
	now := time.Now()
	known := map[string]bool{}
	agents := map[identity.AgentID][]hosting.Supervised{}
	for _, row := range rows {
		known[row.Ref().Key()] = true
		agents[row.Agent] = append(agents[row.Agent], row)
		s.superviseSlot(ctx, row, runtime[row.Ref().Key()], now)
	}
	for _, slots := range agents {
		s.wake(ctx, slots, now)
	}
	for key, r := range runtime {
		if known[key] {
			delete(orphans, key)
			continue
		}
		if !orphans[key] {
			orphans[key] = true
			continue
		}
		delete(orphans, key)
		slog.Info("removing a slot that no longer exists", "agent", r.Slot.Agent, "slot", r.Slot.Index)
		if err := s.runtime().Remove(ctx, r.Slot); err != nil {
			slog.Warn("remove orphan slot", "agent", r.Slot.Agent, "slot", r.Slot.Index, "err", err)
		}
	}
	for key := range orphans {
		if _, ok := runtime[key]; !ok {
			delete(orphans, key)
		}
	}
	return nil
}

func (s *Service) superviseSlot(ctx context.Context, row hosting.Supervised, rs hosting.RuntimeSlot, now time.Time) {
	org, agentID, idx := row.Organization, row.Agent, row.Index
	switch row.State {
	case hosting.SlotRunning:
		if !rs.Container || !rs.Running || rs.Run != row.Run {
			detail := "the agent's container is gone"
			if rs.Container && rs.Run == row.Run {
				detail = rs.Detail
			}
			s.endRun(ctx, org, agentID, idx, hosting.EndRun{From: []string{hosting.SlotRunning}, Run: row.Run,
				State: hosting.SlotFailed, Detail: detail})
			return
		}
		idle := row.Queued == 0 && row.Claimed == 0
		if !idle {
			return
		}
		if row.Outdated {
			s.stopRun(ctx, row, true)
			return
		}
		limit, err := hosting.ParseIdleStop(row.IdleStop)
		if err != nil || limit == 0 {
			return
		}
		last := row.UpdatedAt
		if row.ActiveAt != nil {
			last = *row.ActiveAt
		}
		if now.Sub(last) >= limit {
			s.stopRun(ctx, row, false)
		}
	case hosting.SlotStarting:
		if now.Sub(row.UpdatedAt) > hosting.StartStale {
			s.endRun(ctx, org, agentID, idx, hosting.EndRun{From: []string{hosting.SlotStarting}, Run: row.Run,
				State: hosting.SlotFailed, Detail: "the start was interrupted (its process stopped); start it again",
				StaleFor: hosting.StartStale})
		}
	case hosting.SlotStopping:
		if now.Sub(row.UpdatedAt) > hosting.StartStale { // its stopper died
			s.background(func(ctx context.Context) { s.finishStop(ctx, org, agentID, idx, "") })
		}
	}
}

// stopRun stops a running slot's run (idle or outdated); restart starts
// it again once stopped (on the agent's current environment version).
func (s *Service) stopRun(ctx context.Context, row hosting.Supervised, restart bool) {
	h, claimed, err := s.repo.BeginStop(ctx, row.Organization, row.Agent, row.Index, row.Run)
	if err != nil || !claimed {
		return
	}
	s.events.Publish(row.Organization, hosting.EventSlotsUpdated, h)
	detail := "stopped after idle_stop without work"
	if restart {
		detail = "restarting on the environment's new version"
	}
	slog.Info("stopping slot", "agent", row.Agent, "slot", row.Index, "reason", detail)
	s.background(func(ctx context.Context) {
		s.finishStop(ctx, row.Organization, row.Agent, row.Index, detail)
		if !restart || ctx.Err() != nil {
			return
		}
		start, claimed, err := s.repo.BeginStart(ctx, row.Organization, row.Agent, row.Index, false)
		if err != nil {
			slog.Warn("restart slot", "agent", row.Agent, "slot", row.Index, "err", err)
			return
		}
		if claimed {
			s.announceQuiet(ctx, row.Organization, row.Agent)
			s.runStart(ctx, row.Organization, row.Agent, row.Index, start)
		}
	})
}

// wake starts one slot of an agent that has ships waiting and no slot
// starting or running: the lowest stopped one, else the lowest failed one
// that failed failedRetry ago.
func (s *Service) wake(ctx context.Context, slots []hosting.Supervised, now time.Time) {
	if len(slots) == 0 || slots[0].Queued == 0 {
		return
	}
	var pick *hosting.Supervised
	for i := range slots {
		switch slots[i].State {
		case hosting.SlotStarting, hosting.SlotRunning:
			return
		case hosting.SlotStopped:
			if pick == nil || pick.State != hosting.SlotStopped {
				pick = &slots[i]
			}
		case hosting.SlotFailed:
			if pick == nil && now.Sub(slots[i].UpdatedAt) > failedRetry {
				pick = &slots[i]
			}
		}
	}
	if pick == nil {
		return
	}
	row := *pick
	start, claimed, err := s.repo.BeginStart(ctx, row.Organization, row.Agent, row.Index, true)
	if err != nil {
		slog.Warn("wake slot", "agent", row.Agent, "slot", row.Index, "err", err)
		return
	}
	if !claimed {
		return
	}
	slog.Info("waking slot: its agent has work", "agent", row.Agent, "slot", row.Index, "queued", row.Queued)
	s.announceQuiet(ctx, row.Organization, row.Agent)
	s.launch(row.Organization, row.Agent, row.Index, start)
}

// endRun ends a run and revokes its token.
func (s *Service) endRun(ctx context.Context, org identity.OrganizationID, agentID identity.AgentID, idx int, end hosting.EndRun) {
	h, tok, ended, err := s.repo.EndRun(ctx, org, agentID, idx, end)
	if err != nil {
		slog.Warn("end slot run", "agent", agentID, "slot", idx, "err", err)
		return
	}
	s.revoke(ctx, org, tok)
	if ended {
		slog.Info("slot run ended", "agent", agentID, "slot", idx, "state", end.State, "detail", end.Detail)
		s.events.Publish(org, hosting.EventSlotsUpdated, h)
	}
}

// announceQuiet publishes the agent's hosting, logging a failure.
func (s *Service) announceQuiet(ctx context.Context, org identity.OrganizationID, agentID identity.AgentID) {
	if _, err := s.announce(ctx, org, agentID); err != nil {
		slog.Warn("announce slots", "agent", agentID, "err", err)
	}
}
