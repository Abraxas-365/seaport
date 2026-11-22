package bootstrap

import (
	"context"

	"github.com/Abraxas-365/seaport/internal/dock"
	"github.com/Abraxas-365/seaport/internal/errx"
	"github.com/Abraxas-365/seaport/internal/identity"
	"github.com/Abraxas-365/seaport/internal/ship"
)

// alerter is hosting.Alerter over the dock and ship services: an alert's
// notice is an outbound ship of the port (no agent), so it is kept in the
// conversation's history and counted against the send limits like any.
type alerter struct {
	docks dock.Queries
	ships ship.Commands
}

func (a alerter) Check(ctx context.Context, org identity.OrganizationID, dockID identity.DockID, destination string) error {
	d, err := a.docks.Find(ctx, org, dockID)
	if err != nil {
		return err
	}
	if !d.Channel.Opens() {
		return errx.Business("a " + string(d.Channel) + " dock can't open conversations: pick a WhatsApp, Slack or api dock")
	}
	if !d.CanReply() {
		return errx.Business("the dock's replies are off: alerts need a dock that can send")
	}
	_, err = a.docks.Destination(ctx, org, dockID, destination)
	return err
}

func (a alerter) Open(ctx context.Context, org identity.OrganizationID, dockID identity.DockID, destination, text string) (identity.ConversationID, error) {
	s, err := a.ships.Open(ctx, org, ship.Open{Dock: dockID, Destination: destination, Text: text})
	if err != nil {
		return identity.ConversationID{}, err
	}
	return s.Conversation, nil
}

func (a alerter) Reply(ctx context.Context, org identity.OrganizationID, conversation identity.ConversationID, text string) error {
	_, err := a.ships.Send(ctx, org, ship.Outbound{Conversation: conversation, Text: text})
	return err
}
