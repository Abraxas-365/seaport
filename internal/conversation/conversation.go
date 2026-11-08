package conversation

import (
	"time"

	"github.com/Abraxas-365/puerto/internal/dock"
	"github.com/Abraxas-365/puerto/internal/errx"
	"github.com/Abraxas-365/puerto/internal/identity"
	"github.com/Abraxas-365/puerto/internal/text"
)

// Kind is the shape of a conversation.
type Kind string

const (
	KindDirect Kind = "direct"
	KindGroup  Kind = "group"
	// KindSelf is the account owner's chat with themselves ("Message
	// yourself"): a personal channel to the agent.
	KindSelf Kind = "self"
)

func (k Kind) Valid() bool { return k == KindDirect || k == KindGroup || k == KindSelf }

// MaxExternalID bounds a channel's chat identifier.
const MaxExternalID = 256

// Conversation is a chat on a dock. Only allowed conversations (Status)
// wake the crew and accept replies; see customs.go.
type Conversation struct {
	ID           identity.ConversationID `db:"id" json:"id"`
	Organization identity.OrganizationID `db:"organization_id" json:"-"`
	Dock         identity.DockID         `db:"dock_id" json:"dock_id"`
	Channel      dock.Channel            `db:"channel" json:"channel"`
	ExternalID   string                  `db:"external_id" json:"external_id"`
	Kind         Kind                    `db:"kind" json:"kind"`
	Title        string                  `db:"title" json:"title"`
	// Access is the owner's override; Status the customs outcome.
	Access Access `db:"access" json:"access"`
	// GroupMode overrides the dock's mode for a group (GroupInherit: none).
	GroupMode GroupMode `db:"group_mode" json:"group_mode,omitempty"`
	Status    Status    `db:"status" json:"status"`
	// Handles are the identities the chat is known by (see customs.go).
	Handles []string `db:"-" json:"handles"`
	// LastActivityAt: the last message in or out (inbox order).
	LastActivityAt time.Time `db:"last_activity_at" json:"last_activity_at"`
	CreatedAt      time.Time `db:"created_at" json:"created_at"`
	UpdatedAt      time.Time `db:"updated_at" json:"updated_at"`
}

func (c Conversation) Allowed() bool { return c.Status == StatusAllowed }

// Ensure finds the conversation of (dock, external id) or creates it. A
// non-empty Title refreshes the stored one; Handles are added to the
// chat's known handles (the external id always is one).
type Ensure struct {
	Dock       identity.DockID
	Channel    dock.Channel
	ExternalID string
	Kind       Kind
	Title      string
	Handles    []string
}

func (e Ensure) Validate() error {
	if e.Dock.IsZero() {
		return errx.InvalidField("dock_id", "dock_id is required")
	}
	if !e.Channel.Valid() {
		return errx.InvalidField("channel", "channel is not supported")
	}
	if !e.Kind.Valid() {
		return errx.InvalidField("kind", "kind must be direct, group or self")
	}
	if err := text.Required("external_id", e.ExternalID, MaxExternalID); err != nil {
		return err
	}
	if err := validateHandles("chat_handles", e.Handles); err != nil {
		return err
	}
	return text.Optional("title", e.Title, text.MaxName)
}

// Arrive records that a message reached a chat: it ensures the
// conversation (re-deciding its customs status) and says whether this
// message may wake the crew.
type Arrive struct {
	Chat Ensure
	// Senders are the sender's handles (group members, the direct peer).
	Senders []string
	// Addressed: the message mentions the account or replies to it.
	Addressed bool
}

func (a Arrive) Validate() error {
	if err := a.Chat.Validate(); err != nil {
		return err
	}
	return validateHandles("sender_handles", a.Senders)
}

// Arrival is the customs verdict for one message.
type Arrival struct {
	Conversation Conversation
	// Hold is why the message must not wake the crew (HoldNone: deliver).
	Hold string
}

// Update changes the owner's override, a group's mode or the title.
type Update struct {
	Access    *Access
	GroupMode *GroupMode
	Title     *string
}

func (u Update) Validate() error {
	if u.Access == nil && u.GroupMode == nil && u.Title == nil {
		return errx.Validation("nothing to update")
	}
	if u.Access != nil && !u.Access.Valid() {
		return errx.InvalidField("access", "access must be auto, allow or deny")
	}
	if u.GroupMode != nil && *u.GroupMode != GroupInherit && !u.GroupMode.Valid() {
		return errx.InvalidField("group_mode", `group_mode must be mentions, all or "" (dock default)`)
	}
	if u.Title != nil {
		return text.Optional("title", *u.Title, text.MaxName)
	}
	return nil
}

// Filter narrows List. Every field is optional.
type Filter struct {
	Dock   *identity.DockID
	Status *Status
}
