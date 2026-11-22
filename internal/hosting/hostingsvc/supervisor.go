package hostingsvc

import (
	"context"
	"log/slog"
	"time"

	"github.com/Abraxas-365/seaport/internal/hosting"
	"github.com/Abraxas-365/seaport/internal/identity"
)

// failedRetry: wake starts a failed slot again only this long after it
// failed (a broken machine doesn't restart every tick).
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
// on Wake) it builds the images hosted agents need ahead of their work
// (warm), wakes hosted agents with work and no slot up, stops idle
// slots, restarts outdated idle ones once their image is built, fails
// slots whose container died or whose start or stop was interrupted, and
// removes what the runtime keeps of slots that no longer exist. Every
// process runs one: each step is a conditional transition, so two never
// act on one slot.
func (s *Service) Supervise(ctx context.Context) {
	if s.runtime() == nil {
		return
	}
	t := time.NewTicker(s.supervisePeriod())
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
	// Build the hosted agents' images ahead of their work first, so this
	// round already sees a build that was ready.
	s.warm(ctx, time.Now())
	s.collectImages(time.Now())
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
	}
	for _, slots := range agents {
		first := lowestRunning(slots)
		for _, row := range slots {
			// With autoscale, every running slot but the lowest is an extra.
			extra := row.Autoscale && row.State == hosting.SlotRunning && row.Index != first
			s.superviseSlot(ctx, row, runtime[row.Ref().Key()], extra, now)
		}
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

// superviseSlot acts on one slot. extra: autoscale started it beside the
// agent's lowest running slot, so it stops after ScaleDownAfter idle
// instead of idle_stop.
func (s *Service) superviseSlot(ctx context.Context, row hosting.Supervised, rs hosting.RuntimeSlot, extra bool, now time.Time) {
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
		if !row.Idle() {
			return
		}
		if row.Outdated && row.BuildReady {
			// Restart once the image it needs is built (the supervisor builds
			// it ahead: warm), so it never sits starting through a build;
			// until then idle_stop still applies.
			s.stopRun(ctx, row, true)
			return
		}
		last := row.UpdatedAt
		if row.ActiveAt != nil {
			last = *row.ActiveAt
		}
		if extra {
			if now.Sub(last) >= hosting.ScaleDownAfter {
				s.stopRunWith(ctx, row, false, "stopped: autoscale, no conversation needed it")
			}
			return
		}
		limit, err := hosting.ParseIdleStop(row.IdleStop)
		if err != nil || limit == 0 {
			return
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
// it again once stopped (on the agent's current machine).
func (s *Service) stopRun(ctx context.Context, row hosting.Supervised, restart bool) {
	detail := "stopped after idle_stop without work"
	if restart {
		detail = "restarting on the agent's new image"
	}
	s.stopRunWith(ctx, row, restart, detail)
}

func (s *Service) stopRunWith(ctx context.Context, row hosting.Supervised, restart bool, detail string) {
	h, claimed, err := s.repo.BeginStop(ctx, row.Organization, row.Agent, row.Index, row.Run)
	if err != nil || !claimed {
		return
	}
	s.events.Publish(row.Organization, hosting.EventSlotsUpdated, h)
	slog.Info("stopping slot", "agent", row.Agent, "slot", row.Index, "reason", detail)
	s.background(func(ctx context.Context) {
		s.finishStop(ctx, row.Organization, row.Agent, row.Index, detail)
		if !restart || ctx.Err() != nil {
			return
		}
		start, claimed, err := s.repo.BeginStart(ctx, row.Organization, row.Agent, row.Index, hosting.StartManual)
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
// that failed failedRetry ago. With autoscale it also starts another slot
// when the running ones are full (scaleUp).
func (s *Service) wake(ctx context.Context, slots []hosting.Supervised, now time.Time) {
	if len(slots) == 0 {
		return
	}
	running := 0
	for _, sl := range slots {
		switch sl.State {
		case hosting.SlotStarting:
			return // one start at a time per agent
		case hosting.SlotRunning:
			running++
		}
	}
	if running > 0 {
		s.scaleUp(ctx, slots, running, now)
		return
	}
	if slots[0].Queued == 0 {
		return
	}
	pick := pickStart(slots, now, false)
	if pick == nil {
		return
	}
	row := *pick
	start, claimed, err := s.repo.BeginStart(ctx, row.Organization, row.Agent, row.Index, hosting.StartWake)
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

// scaleUp starts another slot of an autoscaled agent when a conversation
// has waited ScaleAfter and the running slots work all they can
// (Working >= running × concurrency: a conversation that waits for
// another reason, as an older message owed to another agent, starts
// nothing). Only a slot that can work unattended starts (a subscription
// machine's slot needs a person to sign it in once).
func (s *Service) scaleUp(ctx context.Context, slots []hosting.Supervised, running int, now time.Time) {
	a := slots[0]
	if !a.Autoscale || a.Waiting == 0 || a.WaitingSince == nil || now.Sub(*a.WaitingSince) < hosting.ScaleAfter ||
		a.Working < running*a.SlotConcurrency() {
		return
	}
	pick := pickStart(slots, now, true)
	if pick == nil {
		return
	}
	row := *pick
	start, claimed, err := s.repo.BeginStart(ctx, row.Organization, row.Agent, row.Index, hosting.StartScale)
	if err != nil {
		slog.Warn("autoscale slot", "agent", row.Agent, "slot", row.Index, "err", err)
		return
	}
	if !claimed {
		return
	}
	slog.Info("starting another slot: conversations wait with every running slot full", "agent", row.Agent,
		"slot", row.Index, "working", a.Working, "waiting", a.Waiting, "running", running)
	s.announceQuiet(ctx, row.Organization, row.Agent)
	s.launch(row.Organization, row.Agent, row.Index, start)
}

// pickStart is the slot to start: the lowest stopped one, else the lowest
// failed one that failed failedRetry ago (unattended: only slots that can
// work without a person signing them in).
func pickStart(slots []hosting.Supervised, now time.Time, unattended bool) *hosting.Supervised {
	var pick *hosting.Supervised
	for i := range slots {
		if unattended && !slots[i].Unattended {
			continue
		}
		switch slots[i].State {
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
	return pick
}

// lowestRunning is the index of the agent's lowest running slot (0: none).
func lowestRunning(slots []hosting.Supervised) int {
	for _, sl := range slots {
		if sl.State == hosting.SlotRunning {
			return sl.Index
		}
	}
	return 0
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
