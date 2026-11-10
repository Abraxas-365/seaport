package ship

import (
	"context"
	"time"

	"github.com/Abraxas-365/seaport/internal/agent"
	"github.com/Abraxas-365/seaport/internal/conversation"
	"github.com/Abraxas-365/seaport/internal/identity"
	"github.com/Abraxas-365/seaport/internal/query"
)

// Every tenant method takes the organization right after ctx: a crew
// connection only claims its organization's ships, and a conversation or
// delivery of another organization is NotFound. ReleaseExpired is
// system-wide (the sweeper), see TestTenantFirstPorts.
type Commands interface {
	// Receive stores an inbound ship (idempotent on its channel message id)
	// and wakes the crew when customs lets it through. created is false for
	// a duplicate (including the echo of our own outbound message).
	Receive(ctx context.Context, organization identity.OrganizationID, input Inbound) (s Ship, created bool, err error)
	// Claim hands the agent (input.Agent) the oldest conversation whose
	// oldest queued ship is routed to it and that has nothing in flight:
	// its ships queued for that agent, up to the first one routed to
	// another agent (a conversation is worked in order, by one agent at a
	// time). ok is false when there is nothing to do.
	Claim(ctx context.Context, organization identity.OrganizationID, input Claim) (d Delivery, ok bool, err error)
	// Join adds the ships queued since in the delivery's conversation for
	// the delivery's agent to that still-claimed delivery and returns them
	// (same delivery ID, only the new ships; not past a ship queued for
	// another agent). ok is false when there are none or the delivery is
	// closed.
	Join(ctx context.Context, organization identity.OrganizationID, input Join) (d Delivery, ok bool, err error)
	// Extend renews the lease of a delivery still being worked on.
	Extend(ctx context.Context, organization identity.OrganizationID, delivery identity.DeliveryID, lease time.Duration) error
	// Ack marks the named ships of a delivery handled and requeues the rest
	// of its claimed ships.
	Ack(ctx context.Context, organization identity.OrganizationID, input Ack) error
	// Release puts a delivery's ships back in the queue.
	Release(ctx context.Context, organization identity.OrganizationID, delivery identity.DeliveryID) error
	// ReleaseSession puts back every ship claimed by a connection
	// (Claim.Session) that is gone, and says how many.
	ReleaseSession(ctx context.Context, organization identity.OrganizationID, session string) (int, error)
	// ReleaseHeld routes a now-allowed direct conversation's ships held
	// while it was pending (conversation.HoldPending, within
	// ReleaseWindow) to the crew, as if they had just arrived, and says
	// how many it queued (conversation.Gate).
	ReleaseHeld(ctx context.Context, organization identity.OrganizationID, conversation identity.ConversationID) (int, error)
	// ReleaseExpired requeues every delivery whose lease ended, in every
	// organization (system: sweeper).
	ReleaseExpired(ctx context.Context, before time.Time) (int, error)
	// Send delivers a reply to an allowed conversation.
	Send(ctx context.Context, organization identity.OrganizationID, input Outbound) (Ship, error)
	// SweepTrash deletes the blobs whose attachment is gone (or never
	// came), in every organization (system: sweeper), and says how many.
	SweepTrash(ctx context.Context, now time.Time) (int, error)
}

type Queries interface {
	// History lists a conversation's ships, newest first.
	History(ctx context.Context, organization identity.OrganizationID, conversation identity.ConversationID, page query.Pagination) (query.Paginated[Ship], error)
	// Inbox lists conversations (newest activity first) with previews.
	Inbox(ctx context.Context, organization identity.OrganizationID, filter conversation.Filter, page query.Pagination) (query.Paginated[InboxItem], error)
	// OpenAttachment reads an attachment's bytes (unsealed); an agent
	// reads only those of conversations on its docks (else NotFound).
	OpenAttachment(ctx context.Context, organization identity.OrganizationID, input OpenAttachment) (AttachmentFile, error)
}

// Notifier wakes the crew of an organization when ships are waiting
// (infrastructure): the connections of one agent, or of every agent when
// agent is zero (a requeue may concern any of them).
type Notifier interface {
	Notify(organization identity.OrganizationID, agent identity.AgentID)
}

type Repository interface {
	// CreateInbound stores an inbound ship (idempotent on its channel
	// message id). With a hold reason (customs) it is held; else route
	// picks its agent from the dock's team, read in the same transaction
	// and locked against unassigning, so a ship is never queued for an
	// agent that just left the dock (no agent: held with route's reason).
	// Its attachments (blobs already stored) are inserted with it and
	// their keys leave the trash; a duplicate stores none of them.
	CreateInbound(ctx context.Context, organization identity.OrganizationID, ship identity.ShipID, conversation conversation.Conversation, input Inbound, attachments []Attachment, hold string, route Router) (s Ship, created bool, err error)
	// FindExternal finds the conversation's ship with that channel message
	// id (in or out: our own sends come back as echoes), so a redelivery's
	// media isn't fetched again.
	FindExternal(ctx context.Context, organization identity.OrganizationID, conversation identity.ConversationID, externalID string) (s Ship, found bool, err error)
	// CreateOutbound stores a reply only if admit accepts the dock's and the
	// conversation's recent outbound counts; counting and inserting are
	// atomic per dock, so concurrent replies can't overshoot a limit.
	CreateOutbound(ctx context.Context, organization identity.OrganizationID, ship identity.ShipID, input Outbound, dock identity.DockID, externalID string, admit func(OutboundCounts) error) (Ship, error)
	SetOutcome(ctx context.Context, organization identity.OrganizationID, ship identity.ShipID, status Status, errMsg string) (Ship, error)
	// Claim records session as the ships' holder; Join gives joined ships
	// the delivery's holder.
	Claim(ctx context.Context, organization identity.OrganizationID, agent identity.AgentID, delivery identity.DeliveryID, session string, until time.Time) (conversation identity.ConversationID, ships []Ship, err error)
	Join(ctx context.Context, organization identity.OrganizationID, delivery identity.DeliveryID, until time.Time) (conversation identity.ConversationID, ships []Ship, err error)
	Extend(ctx context.Context, organization identity.OrganizationID, delivery identity.DeliveryID, until time.Time) error
	// Ack returns how many unnamed claimed ships went back to the queue.
	Ack(ctx context.Context, organization identity.OrganizationID, delivery identity.DeliveryID, handled []identity.ShipID) (requeued int, err error)
	Release(ctx context.Context, organization identity.OrganizationID, delivery identity.DeliveryID) error
	ReleaseSession(ctx context.Context, organization identity.OrganizationID, session string) (int, error)
	// ReleaseHeld routes the conversation's ships held with
	// conversation.HoldPending since since, oldest first (each sees the
	// agent the one before went to), if the conversation is an allowed
	// direct chat; the team is locked as in CreateInbound. It returns the
	// ships it changed (queued, or held again with route's reason).
	ReleaseHeld(ctx context.Context, organization identity.OrganizationID, conversation identity.ConversationID, since time.Time, route func(team []agent.Member, text string, previous identity.AgentID) (identity.AgentID, string)) ([]Ship, error)
	// ReleaseExpired spans every organization and reports the requeued
	// ships of each (system: sweeper).
	ReleaseExpired(ctx context.Context, before time.Time) (map[identity.OrganizationID]int, error)
	History(ctx context.Context, organization identity.OrganizationID, conversation identity.ConversationID, page query.Pagination) (query.Paginated[Ship], error)
	Summaries(ctx context.Context, organization identity.OrganizationID, conversations []identity.ConversationID) (map[identity.ConversationID]Summary, error)
	// Attachment finds an attachment and its ship's conversation.
	Attachment(ctx context.Context, organization identity.OrganizationID, id identity.AttachmentID) (Attachment, identity.ConversationID, error)
	// StageBlobs puts keys in the trash, due at due, before their blobs
	// are written: a blob whose row never comes is deleted then.
	StageBlobs(ctx context.Context, organization identity.OrganizationID, keys []string, due time.Time) error
	// DueTrash lists blobs due for deletion, of every organization
	// (system: sweeper).
	DueTrash(ctx context.Context, now time.Time, limit int) ([]string, error)
	// DropTrash forgets deleted blobs (system: sweeper; keys staged again
	// since stay).
	DropTrash(ctx context.Context, keys []string, now time.Time) error
}
