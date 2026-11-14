package dock

import (
	"regexp"
	"time"

	"github.com/Abraxas-365/seaport/internal/errx"
	"github.com/Abraxas-365/seaport/internal/identity"
	"github.com/Abraxas-365/seaport/internal/text"
)

// A destination is a place on a dock's channel the owner lets an agent
// open a new conversation at: a Slack channel, a WhatsApp chat, the api
// channel's named feed. The agent sees them by name (MCP `destinations`)
// and opens one with `open`: Seaport posts the first message there, makes
// the conversation (allowed: the owner chose the place) and links it to
// the thread the agent is working on, so the people there and the agent
// talk in that thread from then on. Only the owner names destinations;
// the model picks among them, never a raw address.

// MaxDestinations bounds the destinations of one dock.
const MaxDestinations = 64

// destinationName: short, lowercase, usable in a prompt and a shell.
var destinationName = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,63}$`)

// Destination is one owner-named place of a dock.
type Destination struct {
	Organization identity.OrganizationID `db:"organization_id" json:"-"`
	Dock         identity.DockID         `db:"dock_id" json:"dock_id"`
	// Name is what the agent calls it (unique per dock).
	Name string `db:"name" json:"name"`
	// Target is the channel's address of the place: Slack channel id
	// (C…), WhatsApp chat JID, api feed name. Checked by the gateway.
	Target string `db:"target" json:"target"`
	// Title describes the place to the agent (optional; "#alerts, the
	// on-call channel").
	Title     string    `db:"title" json:"title,omitempty"`
	CreatedAt time.Time `db:"created_at" json:"created_at"`
	// Channel is the dock's (filled when listed for an agent).
	Channel Channel `db:"-" json:"channel,omitempty"`
}

// AddDestination is the input of Commands.AddDestination.
type AddDestination struct {
	Dock   identity.DockID
	Name   string
	Target string
	Title  string
}

func (a AddDestination) Validate() error {
	if a.Dock.IsZero() {
		return errx.InvalidField("dock_id", "dock_id is required")
	}
	if err := ValidDestinationName(a.Name); err != nil {
		return err
	}
	if err := text.Required("target", a.Target, MaxAddress); err != nil {
		return err
	}
	return text.Optional("title", a.Title, text.MaxName)
}

// ValidDestinationName checks a destination name.
func ValidDestinationName(name string) error {
	if !destinationName.MatchString(name) {
		return errx.InvalidField("name", "name must be 1-64 lowercase letters, digits, - or _, starting with a letter or digit")
	}
	return nil
}

// Opening is one new conversation an agent opens at a destination: the
// first message goes out, then the conversation exists. The gateway
// answers the chat id the channel gave the new thread (Slack: channel
// and the message's ts; WhatsApp: the chat JID) and the message's id.
type Opening struct {
	Dock   identity.DockID
	Target string
	// MessageID is the id the stored ship carries (NewMessageID), so the
	// channel's echo of the message is a duplicate.
	MessageID string
	Text      string
	Files     []File `json:",omitempty"`
}

func (o Opening) Validate() error {
	return Outgoing{Dock: o.Dock, ChatID: o.Target, MessageID: o.MessageID, Text: o.Text, Files: o.Files}.Validate()
}

// Opened is what the gateway made: the conversation's external id, kind
// and title, and its handles (for customs; the place itself at least).
type Opened struct {
	ChatID  string
	Kind    string
	Title   string
	Handles []string
}
