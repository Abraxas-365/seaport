package connector

import (
	"strings"
	"testing"
	"time"

	"github.com/Abraxas-365/seaport/internal/conversation"
	"github.com/Abraxas-365/seaport/internal/dock"
	"github.com/Abraxas-365/seaport/internal/identity"
	"github.com/Abraxas-365/seaport/internal/ship"
)

// A delivery of a thread with several doors names the other doors, tags
// each message with its door and tells the agent to answer where each
// came from; a thread of one reads as before.
func TestPromptNamesTheThreadsDoors(t *testing.T) {
	gh := conversation.Conversation{ID: identity.NewConversationID(), Channel: dock.ChannelGitHub, Kind: conversation.KindGroup, Title: "acme/web#7", ExternalID: "acme/web#7"}
	slack := conversation.Conversation{ID: identity.NewConversationID(), Channel: dock.ChannelSlack, Kind: conversation.KindGroup, Title: "#dev", ExternalID: "C1/1.0"}
	when := time.Date(2026, 1, 2, 15, 4, 0, 0, time.UTC)
	d := ship.Delivery{Conversation: slack, Thread: []conversation.Conversation{gh, slack}, Ships: []ship.Ship{
		{Conversation: slack.ID, SenderName: "Ana", Text: "¿cómo va el PR?", SentAt: when},
		{Conversation: gh.ID, SenderName: "bob", Text: "lgtm", SentAt: when},
	}}
	got := Prompt(d, false, nil)
	for _, want := range []string{
		"conversation_id: " + slack.ID.String(),
		"one door of a thread",
		"- github acme/web#7 (group): conversation_id " + gh.ID.String(),
		"(slack #dev) Ana: ¿cómo va el PR?",
		"(github acme/web#7) bob: lgtm",
		"Answer on the door each message came from",
		"`thread: true`",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("prompt lacks %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "- slack #dev") {
		t.Fatalf("the delivery's own door is not listed among the others:\n%s", got)
	}
	steer := SteerPrompt(d, nil)
	if !strings.Contains(steer, "in this thread") || !strings.Contains(steer, "(github acme/web#7) bob: lgtm") {
		t.Fatalf("steer = %s", steer)
	}

	alone := ship.Delivery{Conversation: slack, Thread: []conversation.Conversation{slack}, Ships: d.Ships[:1]}
	got = Prompt(alone, false, nil)
	if strings.Contains(got, "door") || strings.Contains(got, "(slack #dev)") {
		t.Fatalf("a thread of one names no doors:\n%s", got)
	}
	if steer := SteerPrompt(alone, nil); !strings.Contains(steer, "in conversation "+slack.ID.String()) {
		t.Fatalf("steer alone = %s", steer)
	}
}

// A read-only door (its dock takes no replies) is named as such, the agent
// is sent to a door that does, and an auto-reply goes there too; with no
// door taking replies the agent is told nothing is sent.
func TestPromptNamesReadOnlyDoors(t *testing.T) {
	gh := conversation.Conversation{ID: identity.NewConversationID(), Channel: dock.ChannelGitHub, Kind: conversation.KindGroup, Title: "acme/web#7", ExternalID: "acme/web#7"}
	slack := conversation.Conversation{ID: identity.NewConversationID(), Channel: dock.ChannelSlack, Kind: conversation.KindGroup, Title: "#dev", ExternalID: "C1/1.0"}
	when := time.Date(2026, 1, 2, 15, 4, 0, 0, time.UTC)
	d := ship.Delivery{Conversation: gh, Thread: []conversation.Conversation{gh, slack}, ReadOnly: []identity.ConversationID{gh.ID},
		Ships: []ship.Ship{{Conversation: gh.ID, SenderName: "bob", Text: "lgtm", SentAt: when}}}
	got := Prompt(d, false, nil)
	for _, want := range []string{
		"This conversation is read-only",
		"A reply to this conversation is refused",
		"conversation_id " + slack.ID.String(),
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("prompt lacks %q:\n%s", want, got)
		}
	}
	if door, ok := answerDoor(d); !ok || door != slack.ID {
		t.Fatalf("answerDoor = %v %v, want %v", door, ok, slack.ID)
	}
	if got := Prompt(d, true, nil); !strings.Contains(got, "sent as a message to conversation "+slack.ID.String()) {
		t.Fatalf("auto-reply prompt = %s", got)
	}

	// The other door, read-only among the listed ones.
	d2 := ship.Delivery{Conversation: slack, Thread: d.Thread, ReadOnly: d.ReadOnly, Ships: d.Ships}
	if got := Prompt(d2, false, nil); !strings.Contains(got, "- github acme/web#7 (group, read-only)") {
		t.Fatalf("other door not marked read-only:\n%s", got)
	}

	// Nowhere to answer.
	alone := ship.Delivery{Conversation: gh, Thread: []conversation.Conversation{gh}, ReadOnly: []identity.ConversationID{gh.ID}, Ships: d.Ships}
	if _, ok := answerDoor(alone); ok {
		t.Fatal("answerDoor must find no door")
	}
	for _, auto := range []bool{false, true} {
		if got := Prompt(alone, auto, nil); !strings.Contains(got, "No conversation of this thread takes replies") {
			t.Fatalf("auto=%v prompt = %s", auto, got)
		}
	}
	if steer := SteerPrompt(alone, nil); !strings.Contains(steer, "no door of this thread takes replies") {
		t.Fatalf("steer = %s", steer)
	}
}
