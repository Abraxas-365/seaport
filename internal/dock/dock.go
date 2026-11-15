package dock

import (
	"strings"
	"time"

	"github.com/Abraxas-365/seaport/internal/errx"
	"github.com/Abraxas-365/seaport/internal/identity"
	"github.com/Abraxas-365/seaport/internal/text"
)

// Channel is the network a dock connects to.
type Channel string

const (
	ChannelWhatsApp Channel = "whatsapp"
	// ChannelAPI is a generic channel fed through the admin API (your own
	// app, scripts, tests). Replies are stored, not delivered anywhere.
	ChannelAPI Channel = "api"
	// ChannelGitHub is a GitHub App: issues and pull requests that mention
	// an agent arrive; replies are comments.
	ChannelGitHub Channel = "github"
	// ChannelJira is a Jira Cloud site (a service account): issue comments
	// that mention the account or an agent arrive; replies are comments.
	ChannelJira Channel = "jira"
	// ChannelSlack is a Slack App: mentions, DMs and thread follow-ups
	// arrive; replies are messages in the thread.
	ChannelSlack Channel = "slack"
	// ChannelWebhook is a generic inbound webhook: any system that can POST
	// JSON (alerts, CI, payments) wakes the crew. It only triggers: nothing
	// is posted back (see Replies).
	ChannelWebhook Channel = "webhook"
	// ChannelEmail is a mailbox (IMAP in, SMTP out): new mail wakes the
	// crew and replies go out as replies in the same email thread.
	ChannelEmail Channel = "email"
	// ChannelDatadog is a Datadog monitor webhook: alerts wake the crew,
	// one conversation per monitor and scope, and recoveries land in it.
	// It only triggers; the alert is mirrored to a destination of another
	// dock (the dock's mirror) where people and the agent talk.
	ChannelDatadog Channel = "datadog"
)

func (c Channel) Valid() bool {
	switch c {
	case ChannelWhatsApp, ChannelAPI, ChannelGitHub, ChannelJira, ChannelSlack, ChannelWebhook, ChannelEmail, ChannelDatadog:
		return true
	}
	return false
}

// Replies: the channel carries messages back to where they came from (a
// conversational channel). A channel that only triggers (alerts coming
// in by webhook, nothing to post to) answers false: its docks are read
// only whatever their switch says, and the crew answers on another door
// of the thread. Every channel replies except webhook and datadog.
func (c Channel) Replies() bool {
	switch c {
	case ChannelWhatsApp, ChannelAPI, ChannelGitHub, ChannelJira, ChannelSlack, ChannelEmail:
		return true
	}
	return false
}

// Status is the link state of a dock.
type Status string

const (
	StatusLinking   Status = "linking"
	StatusLinked    Status = "linked"
	StatusLoggedOut Status = "logged_out"
	StatusFailed    Status = "failed"
)

func (s Status) Valid() bool {
	switch s {
	case StatusLinking, StatusLinked, StatusLoggedOut, StatusFailed:
		return true
	}
	return false
}

// CanLink: a dock that is not linked can (re)start pairing.
func (s Status) CanLink() bool { return s != StatusLinked }

// MaxAddress bounds a channel account address (a WhatsApp JID...).
const MaxAddress = 256

// Dock is a linked channel account: ships arrive and leave through it.
type Dock struct {
	ID           identity.DockID         `db:"id" json:"id"`
	Organization identity.OrganizationID `db:"organization_id" json:"-"`
	Channel      Channel                 `db:"channel" json:"channel"`
	Name         string                  `db:"name" json:"name"`
	// Address is the account on the channel (WhatsApp: the device JID).
	Address string `db:"address" json:"address"`
	Status  Status `db:"status" json:"status"`
	// Replies is the owner's switch: whether the crew may answer on this
	// dock (default on). Off, the dock is a trigger: its messages wake the
	// agent as before, but a reply to one of its conversations is refused
	// (Business) and the agent is told to answer elsewhere, or not at all.
	Replies bool `db:"replies" json:"replies"`
	// Tools are the owner's switches of what agents may read on this dock.
	Tools     Tools     `db:"tools" json:"tools"`
	CreatedAt time.Time `db:"created_at" json:"created_at"`
	UpdatedAt time.Time `db:"updated_at" json:"updated_at"`
	// QRCode is the current pairing code while linking (never stored).
	QRCode string `db:"-" json:"qr_code,omitempty"`
}

// CanReply: the crew may answer on this dock, both the channel's way and
// the owner's switch allowing it. What Send and the reply tools check.
func (d Dock) CanReply() bool { return d.Channel.Replies() && d.Replies }

type Create struct {
	Channel Channel
	Name    string
}

func (c Create) Validate() error {
	if !c.Channel.Valid() {
		return errx.InvalidField("channel", "channel must be whatsapp, api, github, jira, slack, webhook, email or datadog")
	}
	return text.Required("name", c.Name, text.MaxName)
}

// Tools are a dock's switches of what the agents working it may read
// besides the messages they are handed (all on by default). They bind
// agents only: people (the admin API) read everything their role allows.
// Every agent read goes through the ship service, which checks them
// (history, attachments) whatever asks: MCP tools, crew routes, the
// connector.
type Tools struct {
	// History: agents may read earlier messages of the dock's
	// conversations (the history tool; a thread read skips doors whose
	// dock has it off).
	History bool `db:"history" json:"history"`
	// Conversations: the conversations tool lists the dock's chats.
	Conversations bool `db:"conversations" json:"conversations"`
	// Attachments: agents may open the files people send on the dock
	// (images in their prompt, the attachment tool); off, they are told a
	// file came, not what it holds.
	Attachments bool `db:"attachments" json:"attachments"`
}

// AllTools is a new dock's switches.
var AllTools = Tools{History: true, Conversations: true, Attachments: true}

// ToolsPatch changes some switches (nil: unchanged).
type ToolsPatch struct {
	History       *bool `json:"history,omitempty"`
	Conversations *bool `json:"conversations,omitempty"`
	Attachments   *bool `json:"attachments,omitempty"`
}

// Empty: the patch changes nothing.
func (p ToolsPatch) Empty() bool {
	return p.History == nil && p.Conversations == nil && p.Attachments == nil
}

// Apply returns t with the patch's switches set.
func (p ToolsPatch) Apply(t Tools) Tools {
	if p.History != nil {
		t.History = *p.History
	}
	if p.Conversations != nil {
		t.Conversations = *p.Conversations
	}
	if p.Attachments != nil {
		t.Attachments = *p.Attachments
	}
	return t
}

// Update renames a dock or flips its switches (at least one change).
type Update struct {
	Name    *string
	Replies *bool
	Tools   *ToolsPatch
}

func (u Update) Validate() error {
	if u.Name == nil && u.Replies == nil && (u.Tools == nil || u.Tools.Empty()) {
		return errx.Validation("nothing to update")
	}
	if u.Name != nil {
		return text.Required("name", *u.Name, text.MaxName)
	}
	return nil
}

// Outgoing is one message sent through a dock: text, files or both.
type Outgoing struct {
	Dock      identity.DockID
	ChatID    string
	MessageID string
	Text      string
	// Files go with the text, each the channel's way (GitHub: committed
	// to the repository and linked; WhatsApp: image or document messages).
	Files []File `json:",omitempty"`
}

// File is a file sent with a message; MIME was sniffed from the bytes.
type File struct {
	Name string
	MIME string
	Data []byte
}

// IsImage: shown inline where the channel can (an image message, an
// embedded image).
func (f File) IsImage() bool { return strings.HasPrefix(f.MIME, "image/") }

func (o Outgoing) Validate() error {
	if o.Dock.IsZero() {
		return errx.InvalidField("dock_id", "dock_id is required")
	}
	if err := text.Required("chat_id", o.ChatID, MaxAddress); err != nil {
		return err
	}
	if err := text.Required("message_id", o.MessageID, MaxAddress); err != nil {
		return err
	}
	if len(o.Files) > 0 {
		for _, f := range o.Files {
			if err := text.Required("files.name", f.Name, MaxAddress); err != nil {
				return err
			}
			if len(f.Data) == 0 || f.MIME == "" {
				return errx.InvalidField("files", "a file needs its bytes and type")
			}
		}
		return text.Optional("text", o.Text, text.MaxMessage)
	}
	return text.Required("text", o.Text, text.MaxMessage)
}
