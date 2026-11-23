package shipsvc

import (
	"context"
	"log/slog"
	"strconv"

	"github.com/Abraxas-365/seaport/internal/conversation"
	"github.com/Abraxas-365/seaport/internal/identity"
	"github.com/Abraxas-365/seaport/internal/ship"
)

// mirror opens the dock's mirror (dock.Mirror) for the first message of a
// new thread and reports whether it did. It runs in Receive before the
// message is stored, so the crew wakes to a thread of two doors and its
// answer goes to the mirror, where people answer too.
//
// It opens only for a dock that doesn't reply (a dock that does answers in
// place) and only when the conversation is a thread of one: the first
// message of that chat, or the first after an unlink. The check and the
// opening happen under a per-conversation lock, so two alerts of a new
// thread open one mirror. A message already stored in the conversation (a
// retry of one whose mirror failed) never opens late. Nothing here fails
// the message: a mirror that can't be opened is logged and the message is
// stored anyway.
func (s *Service) mirror(ctx context.Context, org identity.OrganizationID, conv conversation.Conversation, input ship.Inbound) bool {
	if s.docks == nil {
		return false
	}
	d, err := s.docks.Find(ctx, org, conv.Dock)
	if err != nil {
		slog.Error("mirror: find dock", "dock", conv.Dock, "conversation", conv.ID, "err", err)
		return false
	}
	if d.Mirror == nil || d.CanReply() {
		return false
	}
	ctx, cancel := context.WithTimeout(ctx, ship.MirrorWithin)
	defer cancel()
	if s.mirrors != nil {
		unlock, err := s.mirrors.Lock(ctx, org, conv.ID)
		if err != nil {
			slog.Error("mirror: lock conversation", "dock", d.ID, "conversation", conv.ID, "err", err)
			return false
		}
		defer unlock()
	}
	// Read after the lock: another message may have opened it meanwhile.
	doors, err := s.convs.Thread(ctx, org, conv.ID)
	if err != nil {
		slog.Error("mirror: read thread", "dock", d.ID, "conversation", conv.ID, "err", err)
		return false
	}
	if len(doors) > 1 {
		return false
	}
	if _, found, err := s.repo.FindExternal(ctx, org, conv.ID, input.ExternalID); err != nil {
		slog.Error("mirror: find duplicate ship", "dock", d.ID, "conversation", conv.ID, "err", err)
		return false
	} else if found {
		return false
	}
	body := input.Text
	if n := len(input.Media); n > 0 {
		note := "(" + strconv.Itoa(n) + " file(s) attached; see the conversation in Seaport)"
		if body != "" {
			body += "\n"
		}
		body += note
	}
	if _, err := s.Open(ctx, org, ship.Open{Dock: d.Mirror.Dock, Destination: d.Mirror.Destination, Text: body, ThreadOf: conv.ID}); err != nil {
		slog.Error("mirror: open", "dock", d.ID, "mirror", d.Mirror.Dock, "destination", d.Mirror.Destination,
			"conversation", conv.ID, "err", err)
		return false
	}
	return true
}
