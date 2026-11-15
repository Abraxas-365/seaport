package shipsvc

import (
	"context"
	"log/slog"

	"github.com/Abraxas-365/seaport/internal/conversation"
	"github.com/Abraxas-365/seaport/internal/dock"
	"github.com/Abraxas-365/seaport/internal/errx"
	"github.com/Abraxas-365/seaport/internal/identity"
	"github.com/Abraxas-365/seaport/internal/ship"
	"github.com/Abraxas-365/seaport/internal/text"
)

// Open sends the first message of a new conversation to a destination of
// a dock, then stores what the channel made: the conversation (allowed —
// the owner named the place — and linked to the thread of ThreadOf) and
// the ship, already sent. The send goes first because the conversation's
// id on the channel (a Slack thread's ts) exists only once it is posted;
// the limits are checked before it (OutboundCounts) and the ship is
// stored whatever they say afterwards, so what went out is on record.
func (s *Service) Open(ctx context.Context, org identity.OrganizationID, input ship.Open) (ship.Ship, error) {
	if err := input.Validate(); err != nil {
		return ship.Ship{}, err
	}
	input.Text = text.Clean(input.Text)
	if s.docks == nil {
		return ship.Ship{}, errx.Business("this port has no destinations")
	}
	if input.Agent != nil {
		if err := s.onDock(ctx, org, *input.Agent, input.Dock); err != nil {
			return ship.Ship{}, errx.NotFound("destination not found")
		}
	}
	dest, err := s.docks.Destination(ctx, org, input.Dock, input.Destination)
	if err != nil {
		return ship.Ship{}, errx.Wrap(err, "find destination", errx.TypeInternal)
	}
	if !input.ThreadOf.IsZero() {
		// The thread to join must be one the caller may see (an agent:
		// one of its docks' conversations).
		other, err := s.convs.Find(ctx, org, input.ThreadOf)
		if err != nil {
			return ship.Ship{}, errx.Wrap(err, "find thread", errx.TypeInternal)
		}
		if input.Agent != nil {
			if err := s.onDock(ctx, org, *input.Agent, other.Dock); err != nil {
				return ship.Ship{}, err
			}
		}
	}
	counts, err := s.repo.OutboundCounts(ctx, org, input.Dock, identity.ConversationID{})
	if err != nil {
		return ship.Ship{}, errx.Wrap(err, "count outbound", errx.TypeInternal)
	}
	if err := s.limits.Check(counts); err != nil {
		slog.Warn("open refused", "organization", org, "dock", input.Dock, "destination", dest.Name, "err", err)
		return ship.Ship{}, err
	}
	msgID, err := s.messenger.NewMessageID(ctx, org, input.Dock)
	if err != nil {
		return ship.Ship{}, errx.Wrap(err, "new message id", errx.TypeInternal)
	}
	attachments, files, err := s.keepFiles(ctx, org, input.Files)
	if err != nil {
		return ship.Ship{}, err
	}
	sendCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), sendTimeout)
	defer cancel()
	opened, err := s.messenger.Open(sendCtx, org, dock.Opening{Dock: input.Dock, Target: dest.Target, MessageID: msgID, Text: input.Text, Files: files})
	if err != nil {
		return ship.Ship{}, errx.Wrap(err, "open conversation", errx.TypeExternal)
	}
	// Sent: from here on nothing fails quietly; the message is out there.
	keep := context.WithoutCancel(ctx)
	conv, err := s.convs.Open(keep, org, conversation.Open{
		Chat: conversation.Ensure{Dock: input.Dock, Channel: dest.Channel, ExternalID: opened.ChatID,
			Kind: conversation.Kind(opened.Kind), Title: opened.Title, Handles: opened.Handles},
		ThreadOf: input.ThreadOf,
	})
	if err != nil {
		slog.Error("opened conversation not stored (message sent)", "organization", org, "dock", input.Dock,
			"chat", opened.ChatID, "message", msgID, "err", err)
		return ship.Ship{}, errx.Wrap(err, "store opened conversation", errx.TypeInternal)
	}
	out, err := s.repo.CreateOutbound(keep, org, identity.NewShipID(), ship.Outbound{Conversation: conv.ID, Text: input.Text, Agent: input.Agent},
		conv.Dock, msgID, attachments, func(ship.OutboundCounts) error { return nil })
	if err != nil {
		slog.Error("opened ship not stored (message sent)", "organization", org, "conversation", conv.ID, "message", msgID, "err", err)
		return ship.Ship{}, errx.Wrap(err, "store opened ship", errx.TypeInternal)
	}
	s.events.Publish(org, ship.EventCreated, out)
	if err := s.convs.Touch(keep, org, conv.ID); err != nil {
		slog.Warn("touch conversation", "conversation", conv.ID, "err", err)
	}
	kept := out.Attachments
	out, err = s.repo.SetOutcome(keep, org, out.ID, ship.StatusSent, "")
	if err != nil {
		return ship.Ship{}, errx.Wrap(err, "store send outcome", errx.TypeInternal)
	}
	out.Attachments = kept
	s.events.Publish(org, ship.EventUpdated, out)
	return out, nil
}
