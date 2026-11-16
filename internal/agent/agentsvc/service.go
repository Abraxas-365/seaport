// Package agentsvc implements the agent ports.
package agentsvc

import (
	"context"
	"log/slog"
	"strings"
	"time"

	"github.com/Abraxas-365/seaport/internal/agent"
	"github.com/Abraxas-365/seaport/internal/errx"
	"github.com/Abraxas-365/seaport/internal/event"
	"github.com/Abraxas-365/seaport/internal/identity"
	"github.com/Abraxas-365/seaport/internal/query"
	"github.com/Abraxas-365/seaport/internal/text"
)

type Service struct {
	repo    agent.Repository
	revoker agent.Revoker
	events  event.Publisher
	// assistant tells people about takeovers and wakes the agent (bound
	// late: the ship service is built after this one; nil: nobody is told).
	assistant agent.Assistant
	now       func() time.Time
	// poll is how often a blocked ask_takeover looks for its end.
	poll time.Duration
}

func New(repo agent.Repository, revoker agent.Revoker, events event.Publisher) *Service {
	if events == nil {
		events = event.Nop{}
	}
	return &Service{repo: repo, revoker: revoker, events: events, now: func() time.Time { return time.Now().UTC() }, poll: takeoverPoll}
}

// Bind sets the assistant (the ship service).
func (s *Service) Bind(a agent.Assistant) { s.assistant = a }

const (
	takeoverPoll = time.Second
	// deadlineMargin is what a blocked ask leaves of its request's
	// deadline, so the answer gets out before the server cuts it.
	deadlineMargin = 5 * time.Second
)

var (
	_ agent.Commands = (*Service)(nil)
	_ agent.Queries  = (*Service)(nil)
)

func (s *Service) Create(ctx context.Context, org identity.OrganizationID, input agent.Create) (agent.Agent, error) {
	if err := input.Validate(); err != nil {
		return agent.Agent{}, err
	}
	input.Description, input.CreatedBy = text.Clean(input.Description), text.Clean(input.CreatedBy)
	a, err := s.repo.Create(ctx, org, identity.NewAgentID(), input, agent.MaxAgents)
	if err != nil {
		return agent.Agent{}, errx.Wrap(err, "create agent", errx.TypeInternal)
	}
	s.events.Publish(org, agent.EventUpdated, a)
	return a, nil
}

func (s *Service) Update(ctx context.Context, org identity.OrganizationID, id identity.AgentID, input agent.Update) (agent.Agent, error) {
	if err := input.Validate(); err != nil {
		return agent.Agent{}, err
	}
	if input.Description != nil {
		d := text.Clean(*input.Description)
		input.Description = &d
	}
	a, err := s.repo.Update(ctx, org, id, input)
	if err != nil {
		return agent.Agent{}, errx.Wrap(err, "update agent", errx.TypeInternal)
	}
	s.events.Publish(org, agent.EventUpdated, a)
	return a, nil
}

func (s *Service) RequestTakeover(ctx context.Context, org identity.OrganizationID, id identity.AgentID, input agent.AskTakeover) (agent.Agent, error) {
	if err := input.Validate(); err != nil {
		return agent.Agent{}, err
	}
	host := strings.ToLower(strings.TrimSpace(input.Host))
	var news bool
	a, err := s.repo.SetTakeovers(ctx, org, id, func(l agent.Takeovers) agent.Takeovers {
		old, open := l.Find(host)
		out := input.Add(l, s.now())
		if t, ok := out.Find(host); ok {
			news = !open || old.Kind != t.Kind
		}
		return out
	})
	if err != nil {
		return agent.Agent{}, errx.Wrap(err, "request takeover", errx.TypeInternal)
	}
	s.events.Publish(org, agent.EventUpdated, a)
	t, ok := a.Takeovers.Find(host)
	if ok && news {
		s.events.Publish(org, agent.EventTakeover, t.Event(a.ID, agent.TakeoverStateAsked, ""))
	}
	if ok && t.NotifiedAt == nil {
		a = s.notify(ctx, org, a, t)
	}
	if input.Wait > 0 {
		return s.await(ctx, org, a, host, input.Wait)
	}
	return a, nil
}

// notify tells a person where the profile's browser_assist says, once per
// host; a notice that can't go out is logged (the takeover stays in the
// web, where the viewer is).
func (s *Service) notify(ctx context.Context, org identity.OrganizationID, a agent.Agent, t agent.Takeover) agent.Agent {
	if s.assistant == nil {
		return a
	}
	p, err := s.repo.Profile(ctx, org, a.ID, 0)
	if err != nil || p.BrowserAssist == "" {
		return a
	}
	if p.BrowserAssist == agent.AssistConversation && t.Conversation == nil {
		slog.Info("takeover not announced: the call named no conversation", "agent", a.Name, "host", t.Host)
		return a
	}
	in, err := s.assistant.Notify(ctx, org, a, t, p.BrowserAssist)
	if err != nil {
		slog.Warn("announce takeover", "agent", a.Name, "host", t.Host, "where", p.BrowserAssist, "err", err)
		return a
	}
	now := s.now()
	marked, err := s.repo.SetTakeovers(ctx, org, a.ID, func(l agent.Takeovers) agent.Takeovers { return l.Notified(t.Host, in, now) })
	if err != nil {
		slog.Warn("record takeover notice", "agent", a.Name, "host", t.Host, "err", err)
		return a
	}
	s.events.Publish(org, agent.EventUpdated, marked)
	return marked
}

// await polls until the host's takeover ends, or wait (bounded by
// agent.MaxTakeoverWait and the request's deadline) is up.
func (s *Service) await(ctx context.Context, org identity.OrganizationID, a agent.Agent, host string, wait time.Duration) (agent.Agent, error) {
	if wait > agent.MaxTakeoverWait {
		wait = agent.MaxTakeoverWait
	}
	if d, ok := ctx.Deadline(); ok {
		if left := time.Until(d) - deadlineMargin; left < wait {
			wait = left
		}
	}
	timer := time.NewTimer(wait)
	defer timer.Stop()
	for {
		if _, open := a.Takeovers.Find(host); !open {
			return a, nil
		}
		select {
		case <-ctx.Done():
			return a, errx.Wrap(ctx.Err(), "wait for the takeover", errx.TypeExternal)
		case <-timer.C:
			return a, nil
		case <-time.After(s.poll):
		}
		fresh, err := s.repo.Find(ctx, org, a.ID)
		if err != nil {
			return a, errx.Wrap(err, "find agent", errx.TypeInternal)
		}
		a = fresh
	}
}

func (s *Service) EndTakeover(ctx context.Context, org identity.OrganizationID, id identity.AgentID, input agent.EndTakeover) (agent.Agent, error) {
	if err := input.Validate(); err != nil {
		return agent.Agent{}, err
	}
	host := strings.ToLower(strings.TrimSpace(input.Host))
	var ended *agent.Takeover
	a, err := s.repo.SetTakeovers(ctx, org, id, func(l agent.Takeovers) agent.Takeovers {
		ended = nil
		if t, ok := l.Find(host); ok {
			ended = &t
		}
		return l.Without(host)
	})
	if err != nil {
		return agent.Agent{}, errx.Wrap(err, "end takeover", errx.TypeInternal)
	}
	s.events.Publish(org, agent.EventUpdated, a)
	if ended != nil {
		s.events.Publish(org, agent.EventTakeover, ended.Event(a.ID, agent.TakeoverDone.State(), text.Clean(input.By)))
		s.wake(ctx, org, a, *ended, agent.TakeoverDone)
	}
	return a, nil
}

// SweepTakeovers ends the takeovers nobody attended within
// agent.TakeoverTTL, agent by agent (its row lock; a concurrent Listo
// wins: the sweep only drops what is still there), and wakes each agent
// once per host. One agent's failure doesn't stop the others.
func (s *Service) SweepTakeovers(ctx context.Context, now time.Time) (int, error) {
	refs, err := s.repo.StaleTakeovers(ctx, now.Add(-agent.TakeoverTTL))
	if err != nil {
		return 0, errx.Wrap(err, "list stale takeovers", errx.TypeInternal)
	}
	n := 0
	var firstErr error
	for _, ref := range refs {
		var expired agent.Takeovers
		a, err := s.repo.SetTakeovers(ctx, ref.Organization, ref.Agent, func(l agent.Takeovers) agent.Takeovers {
			var live agent.Takeovers
			live, expired = l.Expired(now)
			return live
		})
		if err != nil {
			if errx.IsType(err, errx.TypeNotFound) {
				continue // deleted meanwhile
			}
			slog.Error("expire takeovers", "agent", ref.Agent, "err", err)
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		if len(expired) == 0 {
			continue
		}
		s.events.Publish(ref.Organization, agent.EventUpdated, a)
		for _, t := range expired {
			slog.Info("takeover expired: nobody came", "agent", a.Name, "host", t.Host, "asked", t.At)
			s.events.Publish(ref.Organization, agent.EventTakeover, t.Event(a.ID, agent.TakeoverExpired.State(), ""))
			s.wake(ctx, ref.Organization, a, t, agent.TakeoverExpired)
			n++
		}
	}
	return n, firstErr
}

// wake tells the agent how the takeover ended, where the notice went
// (else in the conversation the call named); nowhere, nothing: the agent
// finds out when it retries.
func (s *Service) wake(ctx context.Context, org identity.OrganizationID, a agent.Agent, t agent.Takeover, end agent.TakeoverEnd) {
	in := t.NotifiedIn
	if in == nil {
		in = t.Conversation
	}
	if s.assistant == nil || in == nil {
		return
	}
	if err := s.assistant.Wake(context.WithoutCancel(ctx), org, a, t, end, *in); err != nil {
		slog.Warn("wake agent after takeover", "agent", a.Name, "host", t.Host, "end", end, "err", err)
	}
}

func (s *Service) Delete(ctx context.Context, org identity.OrganizationID, id identity.AgentID) error {
	if id.IsZero() {
		return errx.InvalidField("agent_id", "agent_id is required")
	}
	if err := s.repo.Delete(ctx, org, id); err != nil {
		return errx.Wrap(err, "delete agent", errx.TypeInternal)
	}
	if s.revoker != nil {
		s.revoker.DisconnectAgent(org, id)
	}
	s.events.Publish(org, agent.EventDeleted, agent.Deleted{ID: id})
	return nil
}

func (s *Service) Assign(ctx context.Context, org identity.OrganizationID, input agent.Assign) (agent.Agent, error) {
	if err := input.Validate(); err != nil {
		return agent.Agent{}, err
	}
	if err := s.repo.Assign(ctx, org, input); err != nil {
		return agent.Agent{}, errx.Wrap(err, "assign agent", errx.TypeInternal)
	}
	return s.announce(ctx, org, input.Agent)
}

func (s *Service) Unassign(ctx context.Context, org identity.OrganizationID, dock identity.DockID, id identity.AgentID) (agent.Agent, error) {
	if dock.IsZero() {
		return agent.Agent{}, errx.InvalidField("dock_id", "dock_id is required")
	}
	if id.IsZero() {
		return agent.Agent{}, errx.InvalidField("agent_id", "agent_id is required")
	}
	if err := s.repo.Unassign(ctx, org, dock, id); err != nil {
		return agent.Agent{}, errx.Wrap(err, "unassign agent", errx.TypeInternal)
	}
	return s.announce(ctx, org, id)
}

// announce publishes the agent as it is now. Assigning a default demotes
// another agent: the web refetches the list on every agent.updated.
func (s *Service) announce(ctx context.Context, org identity.OrganizationID, id identity.AgentID) (agent.Agent, error) {
	a, err := s.repo.Find(ctx, org, id)
	if err != nil {
		return agent.Agent{}, errx.Wrap(err, "find agent", errx.TypeInternal)
	}
	s.events.Publish(org, agent.EventUpdated, a)
	return a, nil
}

func (s *Service) Find(ctx context.Context, org identity.OrganizationID, id identity.AgentID) (agent.Agent, error) {
	if id.IsZero() {
		return agent.Agent{}, errx.InvalidField("agent_id", "agent_id is required")
	}
	return s.repo.Find(ctx, org, id)
}

func (s *Service) FindByName(ctx context.Context, org identity.OrganizationID, name string) (agent.Agent, error) {
	if err := agent.ValidName(name); err != nil {
		return agent.Agent{}, err
	}
	return s.repo.FindByName(ctx, org, name)
}

func (s *Service) List(ctx context.Context, org identity.OrganizationID, page query.Pagination) (query.Paginated[agent.Agent], error) {
	return s.repo.List(ctx, org, page.Normalize())
}

func (s *Service) Team(ctx context.Context, org identity.OrganizationID, dock identity.DockID) ([]agent.Member, error) {
	if dock.IsZero() {
		return nil, errx.InvalidField("dock_id", "dock_id is required")
	}
	return s.repo.Team(ctx, org, dock)
}
