// Package shipsvc implements the ship ports: customs, the delivery queue
// and replies.
package shipsvc

import (
	"context"
	"log/slog"
	"time"

	"github.com/Abraxas-365/seaport/internal/agent"
	"github.com/Abraxas-365/seaport/internal/conversation"
	"github.com/Abraxas-365/seaport/internal/dock"
	"github.com/Abraxas-365/seaport/internal/errx"
	"github.com/Abraxas-365/seaport/internal/event"
	"github.com/Abraxas-365/seaport/internal/identity"
	"github.com/Abraxas-365/seaport/internal/query"
	"github.com/Abraxas-365/seaport/internal/ship"
	"github.com/Abraxas-365/seaport/internal/text"
)

// sendTimeout bounds one outbound send through a dock.
const sendTimeout = 90 * time.Second

type Deps struct {
	Repo          ship.Repository
	Conversations interface {
		conversation.Commands
		conversation.Queries
	}
	Messenger dock.Messenger
	Notifier  ship.Notifier
	Events    event.Publisher
	// Agents tells whether a replying agent works on the conversation's
	// dock.
	Agents agent.Queries
	Now    func() time.Time
	// Limits cap outbound messages (zero value: ship.DefaultLimits).
	Limits *ship.Limits
	// Media keeps message media (zero value: off, media become notes).
	Media Media
}

type Service struct {
	repo  ship.Repository
	convs interface {
		conversation.Commands
		conversation.Queries
	}
	messenger dock.Messenger
	notifier  ship.Notifier
	events    event.Publisher
	agents    agent.Queries
	now       func() time.Time
	limits    ship.Limits
	media     media
}

func New(d Deps) (*Service, error) {
	now := d.Now
	if now == nil {
		now = time.Now
	}
	events := d.Events
	if events == nil {
		events = event.Nop{}
	}
	limits := ship.DefaultLimits
	if d.Limits != nil {
		limits = *d.Limits
	}
	m, err := newMedia(d.Media)
	if err != nil {
		return nil, err
	}
	return &Service{repo: d.Repo, convs: d.Conversations, messenger: d.Messenger, notifier: d.Notifier,
		events: events, agents: d.Agents, now: now, limits: limits, media: m}, nil
}

var (
	_ ship.Commands = (*Service)(nil)
	_ ship.Queries  = (*Service)(nil)
)

func (s *Service) Receive(ctx context.Context, org identity.OrganizationID, input ship.Inbound) (ship.Ship, bool, error) {
	if err := input.Validate(); err != nil {
		return ship.Ship{}, false, err
	}
	input.Text = text.Clean(input.Text)
	input.SenderName = text.Clean(input.SenderName)
	arrival, err := s.convs.Arrive(ctx, org, input.Arrival())
	if err != nil {
		return ship.Ship{}, false, errx.Wrap(err, "customs", errx.TypeInternal)
	}
	conv := arrival.Conversation
	said := input.Text // routing reads what the sender wrote, not our notes
	route := func(team []agent.Member, previous identity.AgentID) (identity.AgentID, string) {
		return agent.Route(team, said, previous)
	}
	if len(input.Media) > 0 {
		// A redelivery's media isn't downloaded and stored again.
		if st, found, err := s.repo.FindExternal(ctx, org, conv.ID, input.ExternalID); err != nil {
			return ship.Ship{}, false, errx.Wrap(err, "find duplicate ship", errx.TypeInternal)
		} else if found {
			return st, false, nil
		}
	}
	attachments, body, err := s.keep(ctx, org, input, arrival.Hold)
	if err != nil {
		return ship.Ship{}, false, err
	}
	input.Text = body
	st, created, err := s.repo.CreateInbound(ctx, org, identity.NewShipID(), conv, input, attachments, arrival.Hold, route)
	if err != nil {
		return ship.Ship{}, false, errx.Wrap(err, "store inbound ship", errx.TypeInternal)
	}
	if !created {
		return st, false, nil
	}
	if st.Status == ship.StatusHeld {
		slog.Debug("ship held", "conversation", conv.ID, "reason", st.HoldReason)
	}
	s.events.Publish(org, ship.EventCreated, st)
	if st.Status == ship.StatusQueued && st.Agent != nil {
		s.notifier.Notify(org, *st.Agent)
	}
	if st.HoldReason == conversation.HoldPending {
		// The owner may have allowed the chat between customs and the
		// insert (its release didn't see this ship yet).
		if now, err := s.convs.Find(ctx, org, conv.ID); err == nil && now.Allowed() {
			if _, err := s.ReleaseHeld(ctx, org, conv.ID); err != nil {
				slog.Error("deliver what waited at customs", "conversation", conv.ID, "err", err)
			}
		}
	}
	return st, true, nil
}

func (s *Service) Claim(ctx context.Context, org identity.OrganizationID, input ship.Claim) (ship.Delivery, bool, error) {
	if err := input.Validate(); err != nil {
		return ship.Delivery{}, false, err
	}
	id := identity.NewDeliveryID()
	convID, ships, err := s.repo.Claim(ctx, org, input.Agent, id, input.Session, s.now().Add(input.Lease))
	if err != nil {
		return ship.Delivery{}, false, errx.Wrap(err, "claim delivery", errx.TypeInternal)
	}
	if len(ships) == 0 {
		return ship.Delivery{}, false, nil
	}
	conv, err := s.convs.Find(ctx, org, convID)
	if err != nil {
		_ = s.repo.Release(ctx, org, id)
		return ship.Delivery{}, false, errx.Wrap(err, "find claimed conversation", errx.TypeInternal)
	}
	slog.Info("delivery claimed", "delivery", id, "conversation", convID, "ships", len(ships), "owner", input.Owner)
	s.events.Publish(org, ship.EventDelivery, ship.DeliveryEvent{State: ship.DeliveryClaimed, Delivery: &id,
		Conversation: &convID, Ships: idsOf(ships), Owner: input.Owner})
	return ship.Delivery{ID: id, Conversation: conv, Ships: ships}, true, nil
}

func (s *Service) Join(ctx context.Context, org identity.OrganizationID, input ship.Join) (ship.Delivery, bool, error) {
	if err := input.Validate(); err != nil {
		return ship.Delivery{}, false, err
	}
	convID, ships, err := s.repo.Join(ctx, org, input.Delivery, s.now().Add(input.Lease))
	if err != nil {
		return ship.Delivery{}, false, errx.Wrap(err, "join delivery", errx.TypeInternal)
	}
	if len(ships) == 0 {
		return ship.Delivery{}, false, nil
	}
	conv, err := s.convs.Find(ctx, org, convID)
	if err != nil {
		return ship.Delivery{}, false, errx.Wrap(err, "find joined conversation", errx.TypeInternal)
	}
	slog.Info("ships joined delivery", "delivery", input.Delivery, "conversation", convID, "ships", len(ships))
	s.events.Publish(org, ship.EventDelivery, ship.DeliveryEvent{State: ship.DeliveryJoined, Delivery: &input.Delivery,
		Conversation: &convID, Ships: idsOf(ships)})
	return ship.Delivery{ID: input.Delivery, Conversation: conv, Ships: ships}, true, nil
}

func (s *Service) Extend(ctx context.Context, org identity.OrganizationID, delivery identity.DeliveryID, lease time.Duration) error {
	if delivery.IsZero() {
		return errx.InvalidField("delivery_id", "delivery_id is required")
	}
	if lease <= 0 {
		return errx.InvalidField("lease", "lease must be positive")
	}
	return errx.Wrap(s.repo.Extend(ctx, org, delivery, s.now().Add(lease)), "extend delivery", errx.TypeInternal)
}

func (s *Service) Ack(ctx context.Context, org identity.OrganizationID, input ship.Ack) error {
	if err := input.Validate(); err != nil {
		return err
	}
	requeued, err := s.repo.Ack(ctx, org, input.Delivery, input.Ships)
	if err != nil {
		return errx.Wrap(err, "ack delivery", errx.TypeInternal)
	}
	if requeued > 0 {
		slog.Info("unhandled ships requeued on ack", "delivery", input.Delivery, "ships", requeued)
	}
	s.events.Publish(org, ship.EventDelivery, ship.DeliveryEvent{State: ship.DeliveryAcked, Delivery: &input.Delivery,
		Ships: input.Ships, Count: requeued})
	// The conversation may have more ships queued behind this delivery.
	s.notifier.Notify(org, identity.AgentID{})
	return nil
}

func (s *Service) Release(ctx context.Context, org identity.OrganizationID, delivery identity.DeliveryID) error {
	if delivery.IsZero() {
		return errx.InvalidField("delivery_id", "delivery_id is required")
	}
	if err := s.repo.Release(ctx, org, delivery); err != nil {
		return errx.Wrap(err, "release delivery", errx.TypeInternal)
	}
	s.events.Publish(org, ship.EventDelivery, ship.DeliveryEvent{State: ship.DeliveryReleased, Delivery: &delivery})
	s.notifier.Notify(org, identity.AgentID{})
	return nil
}

func (s *Service) ReleaseSession(ctx context.Context, org identity.OrganizationID, session string) (int, error) {
	if session == "" {
		return 0, errx.InvalidField("session", "session is required")
	}
	n, err := s.repo.ReleaseSession(ctx, org, session)
	if err != nil {
		return 0, errx.Wrap(err, "release session", errx.TypeInternal)
	}
	if n > 0 {
		slog.Info("released deliveries of a closed connection", "organization", org, "session", session, "ships", n)
		s.events.Publish(org, ship.EventDelivery, ship.DeliveryEvent{State: ship.DeliveryReleased, Count: n})
		s.notifier.Notify(org, identity.AgentID{})
	}
	return n, nil
}

func (s *Service) ReleaseHeld(ctx context.Context, org identity.OrganizationID, conv identity.ConversationID) (int, error) {
	if conv.IsZero() {
		return 0, errx.InvalidField("conversation", "conversation is required")
	}
	changed, err := s.repo.ReleaseHeld(ctx, org, conv, s.now().Add(-ship.ReleaseWindow), agent.Route)
	if err != nil {
		return 0, errx.Wrap(err, "release held ships", errx.TypeInternal)
	}
	queued := 0
	agents := map[identity.AgentID]bool{}
	for _, st := range changed {
		s.events.Publish(org, ship.EventUpdated, st)
		if st.Status == ship.StatusQueued && st.Agent != nil {
			queued++
			agents[*st.Agent] = true
		}
	}
	for a := range agents {
		s.notifier.Notify(org, a)
	}
	if len(changed) > 0 {
		slog.Info("released ships held while the conversation was pending", "organization", org,
			"conversation", conv, "queued", queued, "held", len(changed)-queued)
	}
	return queued, nil
}

func (s *Service) ReleaseExpired(ctx context.Context, before time.Time) (int, error) {
	byOrg, err := s.repo.ReleaseExpired(ctx, before)
	if err != nil {
		return 0, errx.Wrap(err, "release expired deliveries", errx.TypeInternal)
	}
	total := 0
	for org, n := range byOrg {
		total += n
		slog.Info("released expired deliveries", "organization", org, "ships", n)
		s.events.Publish(org, ship.EventDelivery, ship.DeliveryEvent{State: ship.DeliveryExpired, Count: n})
		s.notifier.Notify(org, identity.AgentID{})
	}
	return total, nil
}

func (s *Service) Send(ctx context.Context, org identity.OrganizationID, input ship.Outbound) (ship.Ship, error) {
	if err := input.Validate(); err != nil {
		return ship.Ship{}, err
	}
	input.Text = text.Clean(input.Text)
	conv, err := s.convs.Find(ctx, org, input.Conversation)
	if err != nil {
		return ship.Ship{}, errx.Wrap(err, "find conversation", errx.TypeInternal)
	}
	if !conv.Allowed() {
		return ship.Ship{}, errx.Forbidden("conversation is not allowed; the port owner must allow it first")
	}
	if input.Agent != nil {
		if err := s.onDock(ctx, org, *input.Agent, conv.Dock); err != nil {
			return ship.Ship{}, err
		}
	}
	msgID, err := s.messenger.NewMessageID(ctx, org, conv.Dock)
	if err != nil {
		return ship.Ship{}, errx.Wrap(err, "new message id", errx.TypeInternal)
	}
	// Stored before sending so the channel's echo of it is a duplicate;
	// refused (429) when the dock or the conversation sent too much lately.
	out, err := s.repo.CreateOutbound(ctx, org, identity.NewShipID(), input, conv.Dock, msgID, s.limits.Check)
	if err != nil {
		if errx.IsType(err, errx.TypeBusiness) {
			slog.Warn("outbound refused", "organization", org, "dock", conv.Dock, "conversation", conv.ID, "err", err)
		}
		return ship.Ship{}, errx.Wrap(err, "store outbound ship", errx.TypeInternal)
	}
	s.events.Publish(org, ship.EventCreated, out)
	if err := s.convs.Touch(ctx, org, conv.ID); err != nil {
		slog.Warn("touch conversation", "conversation", conv.ID, "err", err)
	}
	sendCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), sendTimeout)
	defer cancel()
	sendErr := s.messenger.Send(sendCtx, org, dock.Outgoing{Dock: conv.Dock, ChatID: conv.ExternalID, MessageID: msgID, Text: input.Text})
	status, msg := ship.StatusSent, ""
	if sendErr != nil {
		status, msg = ship.StatusFailed, sendErr.Error()
	}
	out, err = s.repo.SetOutcome(context.WithoutCancel(ctx), org, out.ID, status, msg)
	if err != nil {
		return ship.Ship{}, errx.Wrap(err, "store send outcome", errx.TypeInternal)
	}
	s.events.Publish(org, ship.EventUpdated, out)
	if sendErr != nil {
		return out, errx.Wrap(sendErr, "send reply", errx.TypeExternal)
	}
	return out, nil
}

func (s *Service) History(ctx context.Context, org identity.OrganizationID, conv identity.ConversationID, page query.Pagination) (query.Paginated[ship.Ship], error) {
	if _, err := s.convs.Find(ctx, org, conv); err != nil {
		return query.Paginated[ship.Ship]{}, errx.Wrap(err, "find conversation", errx.TypeInternal)
	}
	out, err := s.repo.History(ctx, org, conv, page.Normalize())
	if err != nil {
		return out, errx.Wrap(err, "list ships", errx.TypeInternal)
	}
	return out, nil
}

func (s *Service) Inbox(ctx context.Context, org identity.OrganizationID, filter conversation.Filter, page query.Pagination) (query.Paginated[ship.InboxItem], error) {
	convs, err := s.convs.List(ctx, org, filter, page)
	if err != nil {
		return query.Paginated[ship.InboxItem]{}, errx.Wrap(err, "list conversations", errx.TypeInternal)
	}
	out := query.Paginated[ship.InboxItem]{Items: make([]ship.InboxItem, len(convs.Items)), Page: convs.Page}
	ids := make([]identity.ConversationID, len(convs.Items))
	for i, c := range convs.Items {
		ids[i] = c.ID
	}
	sums, err := s.repo.Summaries(ctx, org, ids)
	if err != nil {
		return out, errx.Wrap(err, "summarize conversations", errx.TypeInternal)
	}
	for i, c := range convs.Items {
		out.Items[i] = ship.InboxItem{Conversation: c, Summary: sums[c.ID]}
	}
	return out, nil
}

func idsOf(ships []ship.Ship) []identity.ShipID {
	out := make([]identity.ShipID, len(ships))
	for i, s := range ships {
		out[i] = s.ID
	}
	return out
}

// onDock: an agent replies only to conversations of docks it works on
// (another dock's conversation is NotFound, as if it did not exist).
func (s *Service) onDock(ctx context.Context, org identity.OrganizationID, id identity.AgentID, d identity.DockID) error {
	team, err := s.agents.Team(ctx, org, d)
	if err != nil {
		return errx.Wrap(err, "dock team", errx.TypeInternal)
	}
	for _, m := range team {
		if m.Agent == id {
			return nil
		}
	}
	return errx.NotFound("conversation not found")
}
