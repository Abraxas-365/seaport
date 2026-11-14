package shipsvc

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/Abraxas-365/seaport/internal/agent"
	"github.com/Abraxas-365/seaport/internal/errx"
	"github.com/Abraxas-365/seaport/internal/identity"
	"github.com/Abraxas-365/seaport/internal/query"
	"github.com/Abraxas-365/seaport/internal/ship"
	"github.com/Abraxas-365/seaport/internal/text"
)

// deadlineMargin is what a blocked ask leaves of its request's deadline,
// to answer before the server cuts the request off.
const deadlineMargin = 5 * time.Second

func (s *Service) AskApproval(ctx context.Context, org identity.OrganizationID, input ship.AskApproval) (ship.Approval, error) {
	if err := input.Validate(); err != nil {
		return ship.Approval{}, err
	}
	input.Text = text.Clean(input.Text)
	options := make([]string, len(input.Options))
	for i, o := range input.Options {
		options[i] = text.Clean(o)
	}
	conv, err := s.convs.Find(ctx, org, input.Conversation)
	if err != nil {
		return ship.Approval{}, errx.Wrap(err, "find conversation", errx.TypeInternal)
	}
	if err := s.onDock(ctx, org, input.Agent, conv.Dock); err != nil {
		return ship.Approval{}, err
	}
	a, err := s.ask(ctx, org, conv.ID, input, options)
	if err != nil {
		return ship.Approval{}, err
	}
	if wait := s.waitFor(ctx, input.Wait); wait > 0 {
		return s.await(ctx, org, a, wait)
	}
	return a, nil
}

// ask checks, sends the card and stores the approval under the
// conversation's lock: two asks can't both pass the check and both send
// a card. The lock isn't held while the caller waits for the answer.
func (s *Service) ask(ctx context.Context, org identity.OrganizationID, conv identity.ConversationID, input ship.AskApproval, options []string) (ship.Approval, error) {
	if s.locks != nil {
		unlock, err := s.locks.Lock(ctx, org, conv)
		if err != nil {
			return ship.Approval{}, errx.Wrap(err, "lock approvals", errx.TypeInternal)
		}
		defer unlock()
	}
	now := s.now()
	// What this agent left unanswered past its time doesn't block it.
	expired, err := s.repo.ExpireAgentApprovals(ctx, org, conv, input.Agent, now)
	if err != nil {
		return ship.Approval{}, errx.Wrap(err, "expire approvals", errx.TypeInternal)
	}
	for _, e := range expired {
		s.events.Publish(org, ship.EventApproval, e)
	}
	pending, err := s.repo.PendingApprovals(ctx, org, conv, now)
	if err != nil {
		return ship.Approval{}, errx.Wrap(err, "pending approvals", errx.TypeInternal)
	}
	for _, p := range pending {
		if p.Agent == input.Agent {
			return ship.Approval{}, errx.Business("this agent already has a pending approval in the conversation (id " + p.ID.String() + ")")
		}
	}
	// The card goes out as the agent's message (Send checks the
	// conversation is allowed and its dock takes replies).
	card, err := s.Send(ctx, org, ship.Outbound{Conversation: conv, Agent: &input.Agent, Text: ship.ApprovalCard(input.Text, options)})
	if err != nil {
		return ship.Approval{}, err
	}
	a := ship.Approval{ID: identity.NewApprovalID(), Organization: org, Conversation: conv, Agent: input.Agent,
		Ship: card.ID, Text: input.Text, Options: options, Status: ship.ApprovalPending,
		ExpiresAt: now.Add(s.approvalTTL), CreatedAt: now}
	a, err = s.repo.CreateApproval(ctx, org, a)
	if errx.IsType(err, errx.TypeConflict) {
		return ship.Approval{}, errx.Business("this agent already has a pending approval in the conversation")
	}
	if err != nil {
		return ship.Approval{}, errx.Wrap(err, "store approval", errx.TypeInternal)
	}
	s.events.Publish(org, ship.EventApproval, a)
	return a, nil
}

// waitFor is how long a call may block: what it asked, within
// ship.MaxApprovalWait and its context's deadline.
func (s *Service) waitFor(ctx context.Context, wait time.Duration) time.Duration {
	if wait > ship.MaxApprovalWait {
		wait = ship.MaxApprovalWait
	}
	if d, ok := ctx.Deadline(); ok {
		if left := time.Until(d) - deadlineMargin; left < wait {
			wait = left
		}
	}
	if wait < 0 {
		return 0
	}
	return wait
}

func (s *Service) AwaitApproval(ctx context.Context, org identity.OrganizationID, id identity.ApprovalID, wait time.Duration) (ship.Approval, error) {
	if id.IsZero() {
		return ship.Approval{}, errx.InvalidField("approval_id", "approval_id is required")
	}
	if wait < 0 || wait > ship.MaxApprovalWait {
		return ship.Approval{}, errx.InvalidField("wait_seconds", "wait_seconds is out of range")
	}
	a, err := s.repo.FindApproval(ctx, org, id)
	if err != nil {
		return ship.Approval{}, errx.Wrap(err, "find approval", errx.TypeInternal)
	}
	wait = s.waitFor(ctx, wait)
	if a.Status != ship.ApprovalPending || wait <= 0 {
		return a, nil
	}
	return s.await(ctx, org, a, wait)
}

// await polls the approval until it ends, wait passes or ctx is done, and
// reads it once more. An answer from the web also wakes the agent with a
// system message (the caller may be gone), which an agent still blocked
// just reads as a repeat of what its call returned.
func (s *Service) await(ctx context.Context, org identity.OrganizationID, a ship.Approval, wait time.Duration) (ship.Approval, error) {
	timer := time.NewTimer(wait)
	defer timer.Stop()
	tick := time.NewTicker(s.poll)
	defer tick.Stop()
loop:
	for a.Status == ship.ApprovalPending {
		select {
		case <-ctx.Done():
			break loop
		case <-timer.C:
			break loop
		case <-tick.C:
			cur, err := s.repo.FindApproval(ctx, org, a.ID)
			if err != nil {
				break loop
			}
			a = cur
		}
	}
	bg := context.WithoutCancel(ctx)
	cur, err := s.repo.FindApproval(bg, org, a.ID)
	if err != nil {
		return a, nil
	}
	return cur, nil
}

func (s *Service) AnswerApproval(ctx context.Context, org identity.OrganizationID, id identity.ApprovalID, answer ship.Answer) (ship.Approval, error) {
	if id.IsZero() {
		return ship.Approval{}, errx.InvalidField("approval_id", "approval_id is required")
	}
	if err := answer.Validate(); err != nil {
		return ship.Approval{}, err
	}
	cur, err := s.repo.FindApproval(ctx, org, id)
	if err != nil {
		return ship.Approval{}, errx.Wrap(err, "find approval", errx.TypeInternal)
	}
	option, ok := cur.Option(answer.Option)
	if !ok {
		return ship.Approval{}, errx.InvalidField("option", "option must be one of the approval's options")
	}
	answer.Option, answer.By = option, text.Clean(answer.By)
	a, found, err := s.repo.AnswerApproval(ctx, org, id, answer, s.now())
	if err != nil {
		return ship.Approval{}, errx.Wrap(err, "answer approval", errx.TypeInternal)
	}
	if !found {
		return ship.Approval{}, errx.Business("the approval is no longer pending")
	}
	s.events.Publish(org, ship.EventApproval, a)
	s.wake(ctx, org, a)
	return a, nil
}

func (s *Service) CancelApproval(ctx context.Context, org identity.OrganizationID, id identity.ApprovalID) (ship.Approval, error) {
	if id.IsZero() {
		return ship.Approval{}, errx.InvalidField("approval_id", "approval_id is required")
	}
	a, found, err := s.repo.CancelApproval(ctx, org, id)
	if err != nil {
		return ship.Approval{}, errx.Wrap(err, "cancel approval", errx.TypeInternal)
	}
	if !found {
		if _, err := s.repo.FindApproval(ctx, org, id); err != nil {
			return ship.Approval{}, errx.Wrap(err, "find approval", errx.TypeInternal)
		}
		return ship.Approval{}, errx.Business("the approval is no longer pending")
	}
	s.events.Publish(org, ship.EventApproval, a)
	s.wake(ctx, org, a)
	return a, nil
}

func (s *Service) SweepApprovals(ctx context.Context, now time.Time) (int, error) {
	expired, err := s.repo.ExpireApprovals(ctx, now)
	if err != nil {
		return 0, errx.Wrap(err, "expire approvals", errx.TypeInternal)
	}
	for _, a := range expired {
		s.events.Publish(a.Organization, ship.EventApproval, a)
		s.wake(ctx, a.Organization, a)
	}
	// Wakes that failed (here or in another process) are tried again;
	// the grace leaves the ones just made alone.
	late, err := s.repo.UnwokenApprovals(ctx, now.Add(-ship.ApprovalWakeWindow), now.Add(-ship.ApprovalWakeRetry), 100)
	if err != nil {
		return len(expired), errx.Wrap(err, "unwoken approvals", errx.TypeInternal)
	}
	for _, a := range late {
		s.wake(ctx, a.Organization, a)
	}
	return len(expired), nil
}

// wake tells the agent how its approval ended and records that; a failure
// is logged and left for the sweeper (the approval itself is stored).
func (s *Service) wake(ctx context.Context, org identity.OrganizationID, a ship.Approval) {
	ctx = context.WithoutCancel(ctx)
	if err := s.resume(ctx, org, a); err != nil {
		slog.Warn("wake agent of approval (retried by the sweeper)", "approval", a.ID, "err", err)
		return
	}
	if err := s.repo.WakeApproval(ctx, org, a.ID, s.now()); err != nil {
		slog.Warn("record approval wake", "approval", a.ID, "err", err)
	}
}

// resume posts a system message in the conversation that routes to the
// agent by mention and wakes it, so it picks up where it left. Its
// external id is the approval's, so doing it twice is one message. An
// agent that left the dock's team is not woken (the mention would fall to
// another agent): the approval stays as it ended.
func (s *Service) resume(ctx context.Context, org identity.OrganizationID, a ship.Approval) error {
	conv, err := s.convs.Find(ctx, org, a.Conversation)
	if err != nil {
		if errx.IsType(err, errx.TypeNotFound) {
			return nil
		}
		return errx.Wrap(err, "find conversation", errx.TypeInternal)
	}
	ag, err := s.agents.Find(ctx, org, a.Agent)
	if err != nil {
		if errx.IsType(err, errx.TypeNotFound) {
			slog.Info("approval ended; its agent is gone", "approval", a.ID)
			return nil
		}
		return errx.Wrap(err, "find agent", errx.TypeInternal)
	}
	if err := s.onDock(ctx, org, a.Agent, conv.Dock); err != nil {
		if errx.IsType(err, errx.TypeNotFound) {
			slog.Info("approval ended; its agent left the dock's team, not woken", "approval", a.ID, "agent", ag.Name)
			return nil
		}
		return err
	}
	st, _, err := s.Receive(ctx, org, ship.Inbound{
		Dock: conv.Dock, Channel: conv.Channel, ChatID: conv.ExternalID, ChatKind: conv.Kind, ChatTitle: conv.Title,
		ExternalID: "approval-" + a.ID.String(), SenderID: ship.SystemApproval, SenderName: "Seaport",
		Text: resumeText(ag.Name, a), SentAt: s.now(), Addressed: true,
	})
	if err != nil {
		return err
	}
	if st.Status == ship.StatusHeld {
		slog.Info("approval wake held", "approval", a.ID, "reason", st.HoldReason)
	}
	return nil
}

func resumeText(agent string, a ship.Approval) string {
	var b strings.Builder
	fmt.Fprintf(&b, "@%s [approval %s] ", agent, a.ID)
	switch a.Status {
	case ship.ApprovalAnswered:
		answer := ""
		if a.Answer != nil {
			answer = *a.Answer
		}
		fmt.Fprintf(&b, "answered %q by %s.", answer, a.AnsweredBy)
	case ship.ApprovalExpired:
		b.WriteString("expired with no answer: do not do what it asked.")
	default:
		b.WriteString("cancelled: do not do what it asked.")
	}
	fmt.Fprintf(&b, " Question: %s", a.Text)
	return b.String()
}

// answerer reads a person's message as the answer to a pending approval
// of its conversation (see ship.Answerer). Only a message that goes to
// the crew for a person counts: not the dock's own, not Seaport's.
//
// Which approval: the one of the agent the message names (@name), else
// the one of the agent routing picked, else the oldest; the message must
// be one of its options (ship.MatchOption). Its agent gets the message
// whoever routing picked, if it is still on the team.
func answerer(said string, in ship.Inbound) ship.Answerer {
	if in.Own || strings.HasPrefix(in.SenderID, ship.SystemSenderPrefix) {
		return nil
	}
	return func(pending []ship.Approval, team []agent.Member, routed identity.AgentID) (ship.Answering, bool) {
		onTeam := make(map[identity.AgentID]bool, len(team))
		for _, m := range team {
			onTeam[m.Agent] = true
		}
		var mentioned identity.AgentID
		if m, ok := agent.Mentioned(team, said); ok {
			mentioned = m.Agent
		}
		rank := func(p ship.Approval) int {
			switch {
			case !mentioned.IsZero() && p.Agent == mentioned:
				return 0
			case !routed.IsZero() && p.Agent == routed:
				return 1
			}
			return 2
		}
		best, bestRank := -1, 3
		for i, p := range pending {
			if !onTeam[p.Agent] {
				continue
			}
			if _, ok := p.Match(said); !ok {
				continue
			}
			if r := rank(p); r < bestRank {
				best, bestRank = i, r
			}
		}
		if best < 0 {
			return ship.Answering{}, false
		}
		p := pending[best]
		option, _ := p.Match(said)
		return ship.Answering{Approval: p.ID, Agent: p.Agent, Option: option, By: answeredBy(in)}, true
	}
}

// answeredBy names who answered from a channel: the sender's handle (what
// customs knows it by) with the display name, which anyone can choose,
// only as a note.
func answeredBy(in ship.Inbound) string {
	switch {
	case in.SenderID == "" && in.SenderName == "":
		return "chat"
	case in.SenderID == "":
		return in.SenderName
	case in.SenderName == "" || in.SenderName == in.SenderID:
		return in.SenderID
	}
	return in.SenderID + " (" + in.SenderName + ")"
}

func (s *Service) FindApproval(ctx context.Context, org identity.OrganizationID, id identity.ApprovalID) (ship.Approval, error) {
	if id.IsZero() {
		return ship.Approval{}, errx.InvalidField("approval_id", "approval_id is required")
	}
	a, err := s.repo.FindApproval(ctx, org, id)
	if err != nil {
		return ship.Approval{}, errx.Wrap(err, "find approval", errx.TypeInternal)
	}
	return a, nil
}

func (s *Service) Approvals(ctx context.Context, org identity.OrganizationID, filter ship.ApprovalFilter, page query.Pagination) (query.Paginated[ship.Approval], error) {
	if err := filter.Validate(); err != nil {
		return query.Paginated[ship.Approval]{}, err
	}
	out, err := s.repo.Approvals(ctx, org, filter, page.Normalize())
	if err != nil {
		return out, errx.Wrap(err, "list approvals", errx.TypeInternal)
	}
	return out, nil
}

func (s *Service) ConversationApprovals(ctx context.Context, org identity.OrganizationID, conv identity.ConversationID, page query.Pagination) (query.Paginated[ship.Approval], error) {
	if _, err := s.convs.Find(ctx, org, conv); err != nil {
		return query.Paginated[ship.Approval]{}, errx.Wrap(err, "find conversation", errx.TypeInternal)
	}
	out, err := s.repo.ConversationApprovals(ctx, org, conv, page.Normalize())
	if err != nil {
		return out, errx.Wrap(err, "list approvals", errx.TypeInternal)
	}
	return out, nil
}
