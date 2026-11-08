package ship

import (
	"strconv"
	"time"

	"github.com/Abraxas-365/seaport/internal/conversation"
	"github.com/Abraxas-365/seaport/internal/dock"
	"github.com/Abraxas-365/seaport/internal/errx"
	"github.com/Abraxas-365/seaport/internal/identity"
	"github.com/Abraxas-365/seaport/internal/text"
)

// Direction says whether a ship arrived at the port or left it.
type Direction string

const (
	DirectionIn  Direction = "in"
	DirectionOut Direction = "out"
)

// Status is where a ship is in its journey.
//
//	inbound:  held (customs) | queued → claimed → handled
//	outbound: pending → sent | failed
type Status string

const (
	StatusHeld    Status = "held"
	StatusQueued  Status = "queued"
	StatusClaimed Status = "claimed"
	StatusHandled Status = "handled"
	StatusPending Status = "pending"
	StatusSent    Status = "sent"
	StatusFailed  Status = "failed"
)

// InitialInbound is the status of a new inbound ship given the customs
// hold reason (conversation.HoldNone: deliver it).
func InitialInbound(hold string) Status {
	if hold == conversation.HoldNone {
		return StatusQueued
	}
	return StatusHeld
}

const (
	// MaxSenderID bounds a channel's sender identifier.
	MaxSenderID = 256
	// MaxHistory is the most ships History returns per page.
	MaxHistory = 100
)

// Ship is one message, inbound or outbound.
type Ship struct {
	ID           identity.ShipID         `db:"id" json:"id"`
	Organization identity.OrganizationID `db:"organization_id" json:"-"`
	Conversation identity.ConversationID `db:"conversation_id" json:"conversation_id"`
	Direction    Direction               `db:"direction" json:"direction"`
	ExternalID   string                  `db:"external_id" json:"external_id"`
	SenderID     string                  `db:"sender_id" json:"sender_id"`
	SenderName   string                  `db:"sender_name" json:"sender_name"`
	Text         string                  `db:"text" json:"text"`
	Status       Status                  `db:"status" json:"status"`
	Delivery     *identity.DeliveryID    `db:"delivery_id" json:"delivery_id,omitempty"`
	ClaimedUntil *time.Time              `db:"claimed_until" json:"-"`
	Error        string                  `db:"error" json:"error,omitempty"`
	SentAt       time.Time               `db:"sent_at" json:"sent_at"`
	CreatedAt    time.Time               `db:"created_at" json:"created_at"`
}

// Summary is the inbox preview of one conversation's ships.
type Summary struct {
	Last *Ship `json:"last_ship"`
	// Held: inbound ships customs kept (never delivered).
	Held   int `json:"held"`
	Queued int `json:"queued"`
	// InFlight: a delivery of this conversation is open (agent working).
	InFlight bool `json:"in_flight"`
}

// InboxItem is a conversation with its preview.
type InboxItem struct {
	conversation.Conversation
	Summary
}

// Inbound is a message that arrived at a dock.
type Inbound struct {
	Dock       identity.DockID
	Channel    dock.Channel
	ChatID     string
	ChatKind   conversation.Kind
	ChatTitle  string
	ExternalID string
	SenderID   string
	SenderName string
	Text       string
	SentAt     time.Time
	// ChatHandles / SenderHandles are the identities customs rules match
	// (see conversation/customs.go); ChatID is always a chat handle.
	ChatHandles   []string
	SenderHandles []string
	// Addressed: the message mentions the account or replies to it
	// (groups in mentions mode only wake the crew then).
	Addressed bool
}

func (in Inbound) Validate() error {
	if err := in.Arrival().Validate(); err != nil {
		return err
	}
	if err := text.Required("external_id", in.ExternalID, conversation.MaxExternalID); err != nil {
		return err
	}
	if err := text.Optional("sender_id", in.SenderID, MaxSenderID); err != nil {
		return err
	}
	if err := text.Optional("sender_name", in.SenderName, text.MaxName); err != nil {
		return err
	}
	if err := text.Required("text", in.Text, text.MaxMessage); err != nil {
		return err
	}
	if in.SentAt.IsZero() {
		return errx.InvalidField("sent_at", "sent_at is required")
	}
	return nil
}

// Conversation returns the conversation the inbound ship belongs to.
// Arrival is what customs needs to know about this message.
func (in Inbound) Arrival() conversation.Arrive {
	senders := in.SenderHandles
	if len(senders) == 0 && in.SenderID != "" {
		senders = []string{in.SenderID}
	}
	return conversation.Arrive{
		Chat: conversation.Ensure{Dock: in.Dock, Channel: in.Channel, ExternalID: in.ChatID,
			Kind: in.ChatKind, Title: in.ChatTitle, Handles: in.ChatHandles},
		Senders: senders, Addressed: in.Addressed,
	}
}

// Outbound is a reply the crew sends to a conversation.
type Outbound struct {
	Conversation identity.ConversationID
	Text         string
}

func (o Outbound) Validate() error {
	if o.Conversation.IsZero() {
		return errx.InvalidField("conversation_id", "conversation_id is required")
	}
	return text.Required("text", o.Text, text.MaxMessage)
}

// Delivery is a batch of queued ships of one conversation handed to a crew
// member. Its ID is the claim token.
type Delivery struct {
	ID           identity.DeliveryID       `json:"id"`
	Conversation conversation.Conversation `json:"conversation"`
	Ships        []Ship                    `json:"ships"`
}

// Claim asks for the next delivery.
type Claim struct {
	// Owner names the claimer (logs, debugging).
	Owner string
	Lease time.Duration
}

func (c Claim) Validate() error {
	if c.Lease < time.Second {
		return errx.InvalidField("lease", "lease must be at least one second")
	}
	return text.Optional("owner", c.Owner, text.MaxName)
}

// Join asks for the ships queued in a delivery's conversation after it was
// claimed, so a crew member still working on it can take them mid-turn
// (steering) instead of in a later delivery.
type Join struct {
	Delivery identity.DeliveryID
	Lease    time.Duration
}

func (j Join) Validate() error {
	if j.Delivery.IsZero() {
		return errx.InvalidField("delivery_id", "delivery_id is required")
	}
	if j.Lease < time.Second {
		return errx.InvalidField("lease", "lease must be at least one second")
	}
	return nil
}

// MaxAckShips bounds the ships one ack names.
const MaxAckShips = 10_000

// Ack closes a delivery: Ships are the ships the crew actually worked on
// (handled); any other ship still claimed by the delivery — joined after the
// agent's turn ended — goes back to the queue.
type Ack struct {
	Delivery identity.DeliveryID
	Ships    []identity.ShipID
}

func (a Ack) Validate() error {
	if a.Delivery.IsZero() {
		return errx.InvalidField("delivery_id", "delivery_id is required")
	}
	if len(a.Ships) == 0 || len(a.Ships) > MaxAckShips {
		return errx.InvalidField("ship_ids", "ship_ids must name the handled ships (1 to 10000)")
	}
	for _, id := range a.Ships {
		if id.IsZero() {
			return errx.InvalidField("ship_ids", "ship_ids must not contain empty ids")
		}
	}
	return nil
}

// Limits cap outbound messages (WhatsApp bans numbers that send in bursts
// or to many fresh chats; a hosted port also caps each organization).
// Zero disables a limit.
type Limits struct {
	// PerMinute and PerHour count all of a dock's outbound messages.
	PerMinute int
	PerHour   int
	// ConversationPerMinute caps one chat (a looping agent).
	ConversationPerMinute int
	// OrganizationPerHour counts the organization's outbound messages
	// across all its docks.
	OrganizationPerHour int
}

// DefaultLimits are conservative for a personal WhatsApp number.
var DefaultLimits = Limits{PerMinute: 20, PerHour: 300, ConversationPerMinute: 8, OrganizationPerHour: 1000}

// OutboundCounts are the outbound messages already sent (or being sent)
// in each window, for one dock, one conversation and the organization.
type OutboundCounts struct {
	DockLastMinute         int
	DockLastHour           int
	ConversationLastMinute int
	OrganizationLastHour   int
	// Oldest* are when the oldest message inside each window went out:
	// the window frees a slot at Oldest + window.
	OldestDockMinute         time.Time
	OldestDockHour           time.Time
	OldestConversationMinute time.Time
	OldestOrganizationHour   time.Time
	// At is when the counts were taken, on the same clock as Oldest*.
	At time.Time
}

// Check refuses one more message when a window is full, saying when to
// retry: the longest wait among the full windows (after a limit is
// lowered below the current count it may take more than one retry).
func (l Limits) Check(c OutboundCounts) error {
	type window struct {
		limit, used int
		oldest      time.Time
		span        time.Duration
		what        string
	}
	worst, what := 0, ""
	for _, w := range []window{
		{l.ConversationPerMinute, c.ConversationLastMinute, c.OldestConversationMinute, time.Minute, "this conversation (per minute)"},
		{l.PerMinute, c.DockLastMinute, c.OldestDockMinute, time.Minute, "this dock (per minute)"},
		{l.PerHour, c.DockLastHour, c.OldestDockHour, time.Hour, "this dock (per hour)"},
		{l.OrganizationPerHour, c.OrganizationLastHour, c.OldestOrganizationHour, time.Hour, "this organization (per hour)"},
	} {
		if w.limit <= 0 || w.used < w.limit {
			continue
		}
		retry := max(int(w.oldest.Add(w.span).Sub(c.At).Seconds())+1, 1)
		if retry > worst {
			worst, what = retry, w.what
		}
	}
	if worst == 0 {
		return nil
	}
	return errx.TooManyRequests("outbound limit reached for "+what+"; retry in "+
		strconv.Itoa(worst)+"s").WithDetail("retry_after_seconds", worst)
}
