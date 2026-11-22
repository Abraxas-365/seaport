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
	// ReleaseHeld routes a now-allowed conversation's ships held while it
	// was pending (conversation.HoldPending, within ReleaseWindow) to the
	// crew, as if they had just arrived (customs' message-level checks
	// included), and says how many it queued (conversation.Gate).
	ReleaseHeld(ctx context.Context, organization identity.OrganizationID, conversation identity.ConversationID) (int, error)
	// ReleaseRouted routes again a dock's ships held for routing
	// (agent.RoutingHolds) within ReleaseWindow, in the allowed chats that
	// haven't moved past them (Release.Fresh; customs' message-level
	// checks included), after an agent was assigned to it
	// (agent.Releaser), and says how many it queued.
	ReleaseRouted(ctx context.Context, organization identity.OrganizationID, dock identity.DockID) (int, error)
	// ReleaseExpired requeues every delivery whose lease ended, in every
	// organization (system: sweeper).
	ReleaseExpired(ctx context.Context, before time.Time) (int, error)
	// Send delivers a reply to an allowed conversation.
	Send(ctx context.Context, organization identity.OrganizationID, input Outbound) (Ship, error)
	// Open starts a conversation at a destination of a dock (the owner's
	// named places; the agent must work on that dock): sends the first
	// message there, stores the conversation the channel made (allowed,
	// linked to the thread of Open.ThreadOf) and the ship, sent.
	Open(ctx context.Context, organization identity.OrganizationID, input Open) (Ship, error)
	// SweepTrash deletes the blobs whose attachment is gone (or never
	// came), in every organization (system: sweeper), and says how many.
	SweepTrash(ctx context.Context, now time.Time) (int, error)
	// RecordTurn stores the summary of an ended delivery (the crew hub,
	// from the agent's activity) and publishes EventTurn. A second
	// record of one delivery changes nothing.
	RecordTurn(ctx context.Context, organization identity.OrganizationID, turn Turn) error
	// AskApproval puts a question with options to a conversation's people
	// (an outbound card, sent as the agent) and stores it pending; with
	// input.Wait it blocks for the answer. One pending approval per
	// conversation and agent (Business).
	AskApproval(ctx context.Context, organization identity.OrganizationID, input AskApproval) (Approval, error)
	// AnswerApproval records a choice made on the web or the API (Business
	// when the approval is no longer pending) and wakes the agent with a
	// system message (idempotent: an agent still blocked on the question
	// gets the answer from that call too).
	AnswerApproval(ctx context.Context, organization identity.OrganizationID, id identity.ApprovalID, answer Answer) (Approval, error)
	// CancelApproval withdraws a pending approval (Business otherwise).
	CancelApproval(ctx context.Context, organization identity.OrganizationID, id identity.ApprovalID) (Approval, error)
	// AwaitApproval blocks up to wait (cut to the context's deadline and
	// ship.MaxApprovalWait) while the approval is pending and returns it as
	// it is then.
	AwaitApproval(ctx context.Context, organization identity.OrganizationID, id identity.ApprovalID, wait time.Duration) (Approval, error)
	// SweepApprovals expires the pending approvals past their time, wakes
	// the agents whose wake failed before, in every organization (system:
	// sweeper), and says how many it expired.
	SweepApprovals(ctx context.Context, now time.Time) (int, error)
}

type Queries interface {
	// History lists a conversation's ships, newest first.
	History(ctx context.Context, organization identity.OrganizationID, conversation identity.ConversationID, page query.Pagination) (query.Paginated[Ship], error)
	// ThreadHistory lists the ships of every door of a conversation's
	// thread, newest first (each ship names its conversation).
	ThreadHistory(ctx context.Context, organization identity.OrganizationID, conversation identity.ConversationID, page query.Pagination) (query.Paginated[Ship], error)
	// Inbox lists conversations (newest activity first) with previews.
	Inbox(ctx context.Context, organization identity.OrganizationID, filter conversation.Filter, page query.Pagination) (query.Paginated[InboxItem], error)
	// OpenAttachment reads an attachment's bytes (unsealed); an agent
	// reads only those of conversations on its docks (else NotFound) whose
	// dock lets agents open files (else Forbidden).
	OpenAttachment(ctx context.Context, organization identity.OrganizationID, input OpenAttachment) (AttachmentFile, error)
	// AgentHistory is History (or ThreadHistory) as an agent reads it:
	// scoped to its docks and their tool switches (see AgentHistory). The
	// crew's adapters read history only through it.
	AgentHistory(ctx context.Context, organization identity.OrganizationID, input AgentHistory) (query.Paginated[Ship], error)
	// Turns lists a conversation's turn summaries, newest first.
	Turns(ctx context.Context, organization identity.OrganizationID, conversation identity.ConversationID, page query.Pagination) (query.Paginated[Turn], error)
	// FindApproval reads one approval (NotFound in another organization).
	FindApproval(ctx context.Context, organization identity.OrganizationID, id identity.ApprovalID) (Approval, error)
	// Approvals lists the organization's approvals, newest first.
	Approvals(ctx context.Context, organization identity.OrganizationID, filter ApprovalFilter, page query.Pagination) (query.Paginated[Approval], error)
	// ConversationApprovals lists a conversation's approvals, newest first.
	ConversationApprovals(ctx context.Context, organization identity.OrganizationID, conversation identity.ConversationID, page query.Pagination) (query.Paginated[Approval], error)
}

// Notifier wakes the crew of an organization when ships are waiting
// (infrastructure): the connections of one agent, or of every agent when
// agent is zero (a requeue may concern any of them).
type Notifier interface {
	Notify(organization identity.OrganizationID, agent identity.AgentID)
}

// ApprovalLocker serializes the asks of one conversation across
// processes: the check for a pending approval, the card's send and the
// insert happen under it, so two asks can't both send a card.
type ApprovalLocker interface {
	// Lock waits for the conversation's lock and returns its release.
	Lock(ctx context.Context, organization identity.OrganizationID, conversation identity.ConversationID) (unlock func(), err error)
}

type Repository interface {
	// CreateInbound stores an inbound ship (idempotent on its channel
	// message id). With a hold reason (customs) it is held; else route
	// picks its agent from the dock's team, read in the same transaction
	// and locked against unassigning, so a ship is never queued for an
	// agent that just left the dock (no agent: held with route's reason).
	// Its attachments (blobs already stored) are inserted with it and
	// their keys leave the trash; a duplicate stores none of them.
	// A message that is no hold's (customs) and that answer takes for the
	// reply to a pending approval of the conversation goes to the
	// approval's agent, and the answer is stored in the same transaction
	// (answered: the approval, nil otherwise; answer may be nil).
	CreateInbound(ctx context.Context, organization identity.OrganizationID, ship identity.ShipID, conversation conversation.Conversation, input Inbound, attachments []Attachment, hold string, route Router, answer Answerer) (s Ship, created bool, answered *Approval, err error)
	// FindExternal finds the conversation's ship with that channel message
	// id (in or out: our own sends come back as echoes), so a redelivery's
	// media isn't fetched again.
	FindExternal(ctx context.Context, organization identity.OrganizationID, conversation identity.ConversationID, externalID string) (s Ship, found bool, err error)
	// CreateOutbound stores a reply only if admit accepts the dock's and the
	// conversation's recent outbound counts; counting and inserting are
	// atomic per dock, so concurrent replies can't overshoot a limit. Its
	// attachments (blobs already stored) are inserted with it and leave
	// the trash.
	CreateOutbound(ctx context.Context, organization identity.OrganizationID, ship identity.ShipID, input Outbound, dock identity.DockID, externalID string, attachments []Attachment, admit func(OutboundCounts) error) (Ship, error)
	// OutboundCounts reads the counts CreateOutbound would hand admit, for
	// a send that must go out before its conversation exists (Open);
	// conversation may be zero.
	OutboundCounts(ctx context.Context, organization identity.OrganizationID, dock identity.DockID, conversation identity.ConversationID) (OutboundCounts, error)
	SetOutcome(ctx context.Context, organization identity.OrganizationID, ship identity.ShipID, status Status, errMsg string) (Ship, error)
	// Record stores the dock's own message sent outside Seaport
	// (Inbound.Own) as an outbound ship already sent, by the agent
	// working the conversation when one is (its claimed ships' agent).
	// created is false when the conversation already has a ship with
	// that external id (Seaport's own send, or a redelivery).
	Record(ctx context.Context, organization identity.OrganizationID, ship identity.ShipID, conversation identity.ConversationID, input Inbound) (s Ship, created bool, err error)
	// Claim records session as the ships' holder; Join gives joined ships
	// the delivery's holder.
	Claim(ctx context.Context, organization identity.OrganizationID, agent identity.AgentID, delivery identity.DeliveryID, session string, until time.Time) (conversation identity.ConversationID, ships []Ship, err error)
	Join(ctx context.Context, organization identity.OrganizationID, delivery identity.DeliveryID, until time.Time) (conversation identity.ConversationID, ships []Ship, err error)
	Extend(ctx context.Context, organization identity.OrganizationID, delivery identity.DeliveryID, until time.Time) error
	// Ack returns how many unnamed claimed ships went back to the queue.
	Ack(ctx context.Context, organization identity.OrganizationID, delivery identity.DeliveryID, handled []identity.ShipID) (requeued int, err error)
	Release(ctx context.Context, organization identity.OrganizationID, delivery identity.DeliveryID) error
	ReleaseSession(ctx context.Context, organization identity.OrganizationID, session string) (int, error)
	// ReleaseHeld routes the conversation's held ships that release names
	// (ship.Release), oldest first (each sees the
	// agent the one before went to), if the conversation is allowed; the
	// team is locked as in CreateInbound. admit gives each ship's
	// message-level customs decision from what it stored at arrival
	// (addressed, sender handles; conversation.Customs.Admit): a non-empty
	// reason holds it again with that reason, unrouted. It returns the
	// ships it changed (queued, or held again with admit's or route's
	// reason).
	ReleaseHeld(ctx context.Context, organization identity.OrganizationID, conversation identity.ConversationID, release Release, admit func(addressed bool, senders []string) string, route func(team []agent.Member, text string, previous identity.AgentID) (identity.AgentID, string)) ([]Ship, error)
	// Rerouting lists the dock's conversations with ships held with one
	// of reasons since since, those with the newest first, at most limit.
	Rerouting(ctx context.Context, organization identity.OrganizationID, dock identity.DockID, reasons []string, since time.Time, limit int) ([]identity.ConversationID, error)
	// ReleaseExpired spans every organization and reports the requeued
	// ships of each (system: sweeper).
	ReleaseExpired(ctx context.Context, before time.Time) (map[identity.OrganizationID]int, error)
	History(ctx context.Context, organization identity.OrganizationID, conversation identity.ConversationID, page query.Pagination) (query.Paginated[Ship], error)
	ThreadHistory(ctx context.Context, organization identity.OrganizationID, conversation identity.ConversationID, page query.Pagination) (query.Paginated[Ship], error)
	// HistoryOf lists the ships of these conversations, newest first.
	HistoryOf(ctx context.Context, organization identity.OrganizationID, conversations []identity.ConversationID, page query.Pagination) (query.Paginated[Ship], error)
	// Earlier lists the newest ships of these conversations a delivery
	// shows as context (Delivery.Earlier), newest first, at most limit:
	// handled inbound, sent outbound and inbound held with one of the
	// held reasons (EarlierHolds). Queued and claimed ones (the delivery's
	// own) never are.
	Earlier(ctx context.Context, organization identity.OrganizationID, conversations []identity.ConversationID, held []string, limit int) ([]Ship, error)
	// SaveTurn inserts a turn (created false when its delivery has one).
	SaveTurn(ctx context.Context, organization identity.OrganizationID, turn Turn) (created bool, err error)
	Turns(ctx context.Context, organization identity.OrganizationID, conversation identity.ConversationID, page query.Pagination) (query.Paginated[Turn], error)
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
	// CreateApproval stores a pending approval, noting the delivery the
	// agent is working on in the conversation's thread.
	CreateApproval(ctx context.Context, organization identity.OrganizationID, approval Approval) (Approval, error)
	// ExpireAgentApprovals expires the agent's pending approvals of the
	// conversation that are past now (they would block a new one; the
	// agent is the one asking again, so it needs no wake) and returns them.
	ExpireAgentApprovals(ctx context.Context, organization identity.OrganizationID, conversation identity.ConversationID, agent identity.AgentID, now time.Time) ([]Approval, error)
	FindApproval(ctx context.Context, organization identity.OrganizationID, id identity.ApprovalID) (Approval, error)
	// PendingApprovals lists the conversation's pending approvals (not
	// past now), oldest first: at most one per agent.
	PendingApprovals(ctx context.Context, organization identity.OrganizationID, conversation identity.ConversationID, now time.Time) ([]Approval, error)
	// AnswerApproval answers a pending approval not past now; found is
	// false when it isn't pending.
	AnswerApproval(ctx context.Context, organization identity.OrganizationID, id identity.ApprovalID, answer Answer, now time.Time) (a Approval, found bool, err error)
	// CancelApproval cancels a pending approval; found is false otherwise.
	CancelApproval(ctx context.Context, organization identity.OrganizationID, id identity.ApprovalID) (a Approval, found bool, err error)
	// WakeApproval records that the agent was told how the approval ended.
	WakeApproval(ctx context.Context, organization identity.OrganizationID, id identity.ApprovalID, now time.Time) error
	// UnwokenApprovals lists approvals that ended in (since, before] and
	// whose agent wasn't told, of every organization (system: sweeper).
	UnwokenApprovals(ctx context.Context, since, before time.Time, limit int) ([]Approval, error)
	// ExpireApprovals expires the pending approvals past now, in every
	// organization (system: sweeper), and returns them.
	ExpireApprovals(ctx context.Context, now time.Time) ([]Approval, error)
	Approvals(ctx context.Context, organization identity.OrganizationID, filter ApprovalFilter, page query.Pagination) (query.Paginated[Approval], error)
	ConversationApprovals(ctx context.Context, organization identity.OrganizationID, conversation identity.ConversationID, page query.Pagination) (query.Paginated[Approval], error)
	// DeliveryApprovals lists the agent's approvals of the thread that
	// are pending (not past now), or ended by one of ships (the message
	// that answered, or the system message that woke the agent).
	DeliveryApprovals(ctx context.Context, organization identity.OrganizationID, conversation identity.ConversationID, agent identity.AgentID, ships []identity.ShipID, now time.Time) ([]Approval, error)
}
