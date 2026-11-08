// Package shipsvc implements the ship ports: customs, the delivery queue
// and replies.
package shipsvc

import (
	"context"
	"log/slog"
	"time"

	"github.com/Abraxas-365/puerto/internal/conversation"
	"github.com/Abraxas-365/puerto/internal/dock"
	"github.com/Abraxas-365/puerto/internal/errx"
	"github.com/Abraxas-365/puerto/internal/event"
	"github.com/Abraxas-365/puerto/internal/identity"
	"github.com/Abraxas-365/puerto/internal/query"
	"github.com/Abraxas-365/puerto/internal/ship"
	"github.com/Abraxas-365/puerto/internal/text"
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
	Now       func() time.Time
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
	now       func() time.Time
}

func New(d Deps) *Service {
	now := d.Now
	if now == nil {
		now = time.Now
	}
	events := d.Events
	if events == nil {
		events = event.Nop{}
	}
	return &Service{repo: d.Repo, convs: d.Conversations, messenger: d.Messenger, notifier: d.Notifier, events: events, now: now}
}

var (
	_ ship.Commands = (*Service)(nil)
	_ ship.Queries  = (*Service)(nil)
)

func (s *Service) Receive(ctx context.Context, input ship.Inbound) (ship.Ship, bool, error) {
	if err := input.Validate(); err != nil {
		return ship.Ship{}, false, err
	}
	input.Text = text.Clean(input.Text)
	input.SenderName = text.Clean(input.SenderName)
	arrival, err := s.convs.Arrive(ctx, input.Arrival())
	if err != nil {
		return ship.Ship{}, false, errx.Wrap(err, "customs", errx.TypeInternal)
	}
	conv := arrival.Conversation
	st, created, err := s.repo.CreateInbound(ctx, identity.NewShipID(), conv.ID, input, ship.InitialInbound(arrival.Hold))
	if created && st.Status == ship.StatusHeld {
		slog.Debug("ship held by customs", "conversation", conv.ID, "reason", arrival.Hold)
	}
	if err != nil {
		return ship.Ship{}, false, errx.Wrap(err, "store inbound ship", errx.TypeInternal)
	}
	if created {
		s.events.Publish(ship.EventCreated, st)
	}
	if created && st.Status == ship.StatusQueued {
		s.notifier.Notify()
	}
	return st, created, nil
}

func (s *Service) Claim(ctx context.Context, input ship.Claim) (ship.Delivery, bool, error) {
	if err := input.Validate(); err != nil {
		return ship.Delivery{}, false, err
	}
	id := identity.NewDeliveryID()
	convID, ships, err := s.repo.Claim(ctx, id, s.now().Add(input.Lease))
	if err != nil {
		return ship.Delivery{}, false, errx.Wrap(err, "claim delivery", errx.TypeInternal)
	}
	if len(ships) == 0 {
		return ship.Delivery{}, false, nil
	}
	conv, err := s.convs.Find(ctx, convID)
	if err != nil {
		_ = s.repo.Release(ctx, id)
		return ship.Delivery{}, false, errx.Wrap(err, "find claimed conversation", errx.TypeInternal)
	}
	slog.Info("delivery claimed", "delivery", id, "conversation", convID, "ships", len(ships), "owner", input.Owner)
	s.events.Publish(ship.EventDelivery, ship.DeliveryEvent{State: ship.DeliveryClaimed, Delivery: &id,
		Conversation: &convID, Ships: idsOf(ships), Owner: input.Owner})
	return ship.Delivery{ID: id, Conversation: conv, Ships: ships}, true, nil
}

func (s *Service) Join(ctx context.Context, input ship.Join) (ship.Delivery, bool, error) {
	if err := input.Validate(); err != nil {
		return ship.Delivery{}, false, err
	}
	convID, ships, err := s.repo.Join(ctx, input.Delivery, s.now().Add(input.Lease))
	if err != nil {
		return ship.Delivery{}, false, errx.Wrap(err, "join delivery", errx.TypeInternal)
	}
	if len(ships) == 0 {
		return ship.Delivery{}, false, nil
	}
	conv, err := s.convs.Find(ctx, convID)
	if err != nil {
		return ship.Delivery{}, false, errx.Wrap(err, "find joined conversation", errx.TypeInternal)
	}
	slog.Info("ships joined delivery", "delivery", input.Delivery, "conversation", convID, "ships", len(ships))
	s.events.Publish(ship.EventDelivery, ship.DeliveryEvent{State: ship.DeliveryJoined, Delivery: &input.Delivery,
		Conversation: &convID, Ships: idsOf(ships)})
	return ship.Delivery{ID: input.Delivery, Conversation: conv, Ships: ships}, true, nil
}

func (s *Service) Extend(ctx context.Context, delivery identity.DeliveryID, lease time.Duration) error {
	return errx.Wrap(s.repo.Extend(ctx, delivery, s.now().Add(lease)), "extend delivery", errx.TypeInternal)
}

func (s *Service) Ack(ctx context.Context, input ship.Ack) error {
	if err := input.Validate(); err != nil {
		return err
	}
	requeued, err := s.repo.Ack(ctx, input.Delivery, input.Ships)
	if err != nil {
		return errx.Wrap(err, "ack delivery", errx.TypeInternal)
	}
	if requeued > 0 {
		slog.Info("unhandled ships requeued on ack", "delivery", input.Delivery, "ships", requeued)
	}
	s.events.Publish(ship.EventDelivery, ship.DeliveryEvent{State: ship.DeliveryAcked, Delivery: &input.Delivery,
		Ships: input.Ships, Count: requeued})
	// The conversation may have more ships queued behind this delivery.
	s.notifier.Notify()
	return nil
}

func (s *Service) Release(ctx context.Context, delivery identity.DeliveryID) error {
	if err := s.repo.Release(ctx, delivery); err != nil {
		return errx.Wrap(err, "release delivery", errx.TypeInternal)
	}
	s.events.Publish(ship.EventDelivery, ship.DeliveryEvent{State: ship.DeliveryReleased, Delivery: &delivery})
	s.notifier.Notify()
	return nil
}

func (s *Service) ReleaseExpired(ctx context.Context, before time.Time) (int, error) {
	n, err := s.repo.ReleaseExpired(ctx, before)
	if err != nil {
		return 0, errx.Wrap(err, "release expired deliveries", errx.TypeInternal)
	}
	if n > 0 {
		slog.Info("released expired deliveries", "ships", n)
		s.events.Publish(ship.EventDelivery, ship.DeliveryEvent{State: ship.DeliveryExpired, Count: n})
		s.notifier.Notify()
	}
	return n, nil
}

func (s *Service) Send(ctx context.Context, input ship.Outbound) (ship.Ship, error) {
	if err := input.Validate(); err != nil {
		return ship.Ship{}, err
	}
	input.Text = text.Clean(input.Text)
	conv, err := s.convs.Find(ctx, input.Conversation)
	if err != nil {
		return ship.Ship{}, errx.Wrap(err, "find conversation", errx.TypeInternal)
	}
	if !conv.Allowed() {
		return ship.Ship{}, errx.Forbidden("conversation is not allowed; the port owner must allow it first")
	}
	msgID, err := s.messenger.NewMessageID(ctx, conv.Dock)
	if err != nil {
		return ship.Ship{}, errx.Wrap(err, "new message id", errx.TypeInternal)
	}
	// Stored before sending so the channel's echo of it is a duplicate.
	out, err := s.repo.CreateOutbound(ctx, identity.NewShipID(), input, msgID)
	if err != nil {
		return ship.Ship{}, errx.Wrap(err, "store outbound ship", errx.TypeInternal)
	}
	s.events.Publish(ship.EventCreated, out)
	if err := s.convs.Touch(ctx, conv.ID); err != nil {
		slog.Warn("touch conversation", "conversation", conv.ID, "err", err)
	}
	sendCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), sendTimeout)
	defer cancel()
	sendErr := s.messenger.Send(sendCtx, dock.Outgoing{Dock: conv.Dock, ChatID: conv.ExternalID, MessageID: msgID, Text: input.Text})
	status, msg := ship.StatusSent, ""
	if sendErr != nil {
		status, msg = ship.StatusFailed, sendErr.Error()
	}
	out, err = s.repo.SetOutcome(context.WithoutCancel(ctx), out.ID, status, msg)
	if err != nil {
		return ship.Ship{}, errx.Wrap(err, "store send outcome", errx.TypeInternal)
	}
	s.events.Publish(ship.EventUpdated, out)
	if sendErr != nil {
		return out, errx.Wrap(sendErr, "send reply", errx.TypeExternal)
	}
	return out, nil
}

func (s *Service) History(ctx context.Context, conv identity.ConversationID, page query.Pagination) (query.Paginated[ship.Ship], error) {
	if _, err := s.convs.Find(ctx, conv); err != nil {
		return query.Paginated[ship.Ship]{}, errx.Wrap(err, "find conversation", errx.TypeInternal)
	}
	out, err := s.repo.History(ctx, conv, page.Normalize())
	if err != nil {
		return out, errx.Wrap(err, "list ships", errx.TypeInternal)
	}
	return out, nil
}

func (s *Service) Inbox(ctx context.Context, filter conversation.Filter, page query.Pagination) (query.Paginated[ship.InboxItem], error) {
	convs, err := s.convs.List(ctx, filter, page)
	if err != nil {
		return query.Paginated[ship.InboxItem]{}, errx.Wrap(err, "list conversations", errx.TypeInternal)
	}
	out := query.Paginated[ship.InboxItem]{Items: make([]ship.InboxItem, len(convs.Items)), Page: convs.Page}
	ids := make([]identity.ConversationID, len(convs.Items))
	for i, c := range convs.Items {
		ids[i] = c.ID
	}
	sums, err := s.repo.Summaries(ctx, ids)
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
