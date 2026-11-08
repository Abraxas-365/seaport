package ship

import (
	"context"
	"time"

	"github.com/Abraxas-365/puerto/internal/conversation"
	"github.com/Abraxas-365/puerto/internal/identity"
	"github.com/Abraxas-365/puerto/internal/query"
)

type Commands interface {
	// Receive stores an inbound ship (idempotent on its channel message id)
	// and wakes the crew when customs lets it through. created is false for
	// a duplicate (including the echo of our own outbound message).
	Receive(ctx context.Context, input Inbound) (s Ship, created bool, err error)
	// Claim hands the oldest conversation with queued ships and nothing in
	// flight to the caller. ok is false when there is nothing to do.
	Claim(ctx context.Context, input Claim) (d Delivery, ok bool, err error)
	// Join adds the ships queued since in the delivery's conversation to that
	// still-claimed delivery and returns them (same delivery ID, only the new
	// ships). ok is false when there are none or the delivery is closed.
	Join(ctx context.Context, input Join) (d Delivery, ok bool, err error)
	// Extend renews the lease of a delivery still being worked on.
	Extend(ctx context.Context, delivery identity.DeliveryID, lease time.Duration) error
	// Ack marks the named ships of a delivery handled and requeues the rest
	// of its claimed ships.
	Ack(ctx context.Context, input Ack) error
	// Release puts a delivery's ships back in the queue.
	Release(ctx context.Context, delivery identity.DeliveryID) error
	// ReleaseExpired requeues every delivery whose lease ended (all of them
	// when before is far in the future, e.g. on start).
	ReleaseExpired(ctx context.Context, before time.Time) (int, error)
	// Send delivers a reply to an allowed conversation.
	Send(ctx context.Context, input Outbound) (Ship, error)
}

type Queries interface {
	// History lists a conversation's ships, newest first.
	History(ctx context.Context, conversation identity.ConversationID, page query.Pagination) (query.Paginated[Ship], error)
	// Inbox lists conversations (newest activity first) with previews.
	Inbox(ctx context.Context, filter conversation.Filter, page query.Pagination) (query.Paginated[InboxItem], error)
}

// Notifier wakes the crew when ships are waiting (infrastructure).
type Notifier interface {
	Notify()
}

type Repository interface {
	CreateInbound(ctx context.Context, ship identity.ShipID, conversation identity.ConversationID, input Inbound, status Status) (s Ship, created bool, err error)
	CreateOutbound(ctx context.Context, ship identity.ShipID, input Outbound, externalID string) (Ship, error)
	SetOutcome(ctx context.Context, ship identity.ShipID, status Status, errMsg string) (Ship, error)
	Claim(ctx context.Context, delivery identity.DeliveryID, until time.Time) (conversation identity.ConversationID, ships []Ship, err error)
	Join(ctx context.Context, delivery identity.DeliveryID, until time.Time) (conversation identity.ConversationID, ships []Ship, err error)
	Extend(ctx context.Context, delivery identity.DeliveryID, until time.Time) error
	// Ack returns how many unnamed claimed ships went back to the queue.
	Ack(ctx context.Context, delivery identity.DeliveryID, handled []identity.ShipID) (requeued int, err error)
	Release(ctx context.Context, delivery identity.DeliveryID) error
	ReleaseExpired(ctx context.Context, before time.Time) (int, error)
	History(ctx context.Context, conversation identity.ConversationID, page query.Pagination) (query.Paginated[Ship], error)
	Summaries(ctx context.Context, conversations []identity.ConversationID) (map[identity.ConversationID]Summary, error)
}
