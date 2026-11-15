package conversation

import (
	"github.com/Abraxas-365/seaport/internal/errx"
	"github.com/Abraxas-365/seaport/internal/identity"
)

// A thread is one matter seen through several doors: the pull request
// where an agent reviews, the Slack thread where people discuss the
// review, the WhatsApp group that asked for it. Each door is a
// conversation with its own dock, customs status and channel message ids;
// the thread is what the crew works as one: one agent session, one
// delivery in flight, the agent who last answered on any door, a history
// read across doors. Every conversation belongs to a thread; one linked
// to no other is a thread of one (Thread = ThreadOf(ID)).
//
// Customs stays per door: linking never lets a denied or pending door's
// messages in, and an agent answers on a door only when it is on that
// dock's team.

// MaxDoors bounds the conversations of one thread.
const MaxDoors = 16

// Link joins the thread of one conversation with another's: every door
// of both gets one new thread id.
type Link struct {
	Conversation identity.ConversationID
	// With is a conversation of the thread to join.
	With identity.ConversationID
}

func (l Link) Validate() error {
	if l.Conversation.IsZero() {
		return errx.InvalidField("conversation_id", "conversation_id is required")
	}
	if l.With.IsZero() {
		return errx.InvalidField("with", "with is required")
	}
	if l.Conversation == l.With {
		return errx.InvalidField("with", "a conversation can't be linked with itself")
	}
	return nil
}

// Open is a conversation the crew opened at a destination (ship.Commands.
// Open): the chat the channel made, allowed by the owner's choice of the
// place (Access = AccessAllow), and linked to the thread of ThreadOf when
// set.
type Open struct {
	Chat Ensure
	// ThreadOf is a door of the thread to join (zero: a thread of one).
	ThreadOf identity.ConversationID
}

func (o Open) Validate() error { return o.Chat.Validate() }
