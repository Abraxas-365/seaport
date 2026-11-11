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
)

func (c Channel) Valid() bool { return c == ChannelWhatsApp || c == ChannelAPI || c == ChannelGitHub }

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
	Address   string    `db:"address" json:"address"`
	Status    Status    `db:"status" json:"status"`
	CreatedAt time.Time `db:"created_at" json:"created_at"`
	UpdatedAt time.Time `db:"updated_at" json:"updated_at"`
	// QRCode is the current pairing code while linking (never stored).
	QRCode string `db:"-" json:"qr_code,omitempty"`
}

type Create struct {
	Channel Channel
	Name    string
}

func (c Create) Validate() error {
	if !c.Channel.Valid() {
		return errx.InvalidField("channel", "channel must be whatsapp, api or github")
	}
	return text.Required("name", c.Name, text.MaxName)
}

// Update renames a dock.
type Update struct {
	Name *string
}

func (u Update) Validate() error {
	if u.Name == nil {
		return errx.Validation("nothing to update")
	}
	return text.Required("name", *u.Name, text.MaxName)
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
