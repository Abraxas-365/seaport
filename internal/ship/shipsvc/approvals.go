package shipsvc

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

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
	now := s.now()
	pending, err := s.repo.PendingApprovals(ctx, org, conv.ID, now)
	if err != nil {
		return ship.Approval{}, errx.Wrap(err, "pending approvals", errx.TypeInternal)
	}
	for _, p := range pending {
		if p.Agent == input.Agent {
			return ship.Approval{}, errx.Conflict("this agent already has a pending approval in the conversation (id " + p.ID.String() + ")")
		}
	}
	// The card goes out as the agent's message (Send checks the
	// conversation is allowed and its dock takes replies).
	card, err := s.Send(ctx, org, ship.Outbound{Conversation: conv.ID, Agent: &input.Agent, Text: ship.ApprovalCard(input.Text, options)})
	if err != nil {
		return ship.Approval{}, err
	}
	a := ship.Approval{ID: identity.NewApprovalID(), Organization: org, Conversation: conv.ID, Agent: input.Agent,
		Ship: card.ID, Text: input.Text, Options: options, Status: ship.ApprovalPending,
		ExpiresAt: now.Add(s.approvalTTL), CreatedAt: now}
	var waitUntil *time.Time
	wait := s.waitFor(ctx, input.Wait)
	if wait > 0 {
		until := now.Add(wait + 2*ship.ApprovalPoll)
		waitUntil = &until
	}
	a, err = s.repo.CreateApproval(ctx, org, a, waitUntil)
	if err != nil {
		return ship.Approval{}, errx.Wrap(err, "store approval", errx.TypeInternal)
	}
	s.events.Publish(org, ship.EventApproval, a)
	if wait <= 0 {
		return a, nil
	}
	return s.await(ctx, org, a, wait)
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
	until := s.now().Add(wait + 2*ship.ApprovalPoll)
	if err := s.repo.WaitApproval(ctx, org, id, &until); err != nil {
		return ship.Approval{}, errx.Wrap(err, "wait approval", errx.TypeInternal)
	}
	return s.await(ctx, org, a, wait)
}

// await polls the approval until it ends, wait passes or ctx is done,
// then stops being "waited on" (so an answer from then on wakes the agent
// with a system message) and reads the approval once more.
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
	if err := s.repo.WaitApproval(bg, org, a.ID, nil); err != nil {
		slog.Warn("stop waiting on approval", "approval", a.ID, "err", err)
	}
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
	a, waited, found, err := s.repo.AnswerApproval(ctx, org, id, answer, s.now())
	if err != nil {
		return ship.Approval{}, errx.Wrap(err, "answer approval", errx.TypeInternal)
	}
	if !found {
		return ship.Approval{}, errx.Business("the approval is no longer pending")
	}
	s.events.Publish(org, ship.EventApproval, a)
	// From the channel the answer is a ship the agent is handed anyway.
	if answer.Ship == nil && !waited {
		s.resume(ctx, org, a)
	}
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
	s.resume(ctx, org, a)
	return a, nil
}

func (s *Service) SweepApprovals(ctx context.Context, now time.Time) (int, error) {
	expired, err := s.repo.ExpireApprovals(ctx, now)
	if err != nil {
		return 0, errx.Wrap(err, "expire approvals", errx.TypeInternal)
	}
	for _, a := range expired {
		s.events.Publish(a.Organization, ship.EventApproval, a)
		s.resume(ctx, a.Organization, a)
	}
	return len(expired), nil
}

// resume tells the agent how its approval ended: a system message in the
// conversation that routes to it (by mention) and wakes it, so it picks up
// where it left. Best effort: the approval itself is already stored.
func (s *Service) resume(ctx context.Context, org identity.OrganizationID, a ship.Approval) {
	ctx = context.WithoutCancel(ctx)
	conv, err := s.convs.Find(ctx, org, a.Conversation)
	if err != nil {
		slog.Warn("resume approval: conversation", "approval", a.ID, "err", err)
		return
	}
	ag, err := s.agents.Find(ctx, org, a.Agent)
	if err != nil {
		slog.Warn("resume approval: agent", "approval", a.ID, "err", err)
		return
	}
	_, _, err = s.Receive(ctx, org, ship.Inbound{
		Dock: conv.Dock, Channel: conv.Channel, ChatID: conv.ExternalID, ChatKind: conv.Kind, ChatTitle: conv.Title,
		ExternalID: "approval-" + a.ID.String(), SenderID: ship.SystemApproval, SenderName: "Seaport",
		Text: resumeText(ag.Name, a), SentAt: s.now(), Addressed: true,
	})
	if err != nil {
		slog.Warn("resume approval", "approval", a.ID, "err", err)
	}
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

// answerFromChat reads an allowed inbound ship as the answer to the
// pending approval its agent asked of that conversation.
func (s *Service) answerFromChat(ctx context.Context, org identity.OrganizationID, st ship.Ship, in ship.Inbound) {
	if st.Status != ship.StatusQueued || st.Agent == nil || in.Own || strings.HasPrefix(in.SenderID, ship.SystemSenderPrefix) {
		return
	}
	pending, err := s.repo.PendingApprovals(ctx, org, st.Conversation, s.now())
	if err != nil {
		slog.Warn("pending approvals", "conversation", st.Conversation, "err", err)
		return
	}
	for _, p := range pending {
		if p.Agent != *st.Agent {
			continue
		}
		option, ok := p.Match(in.Text)
		if !ok {
			return
		}
		by := in.SenderName
		if by == "" {
			by = in.SenderID
		}
		if by == "" {
			by = "chat"
		}
		if _, err := s.AnswerApproval(ctx, org, p.ID, ship.Answer{Option: option, By: by, Ship: &st.ID}); err != nil {
			slog.Warn("answer approval from chat", "approval", p.ID, "err", err)
		}
		return
	}
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
