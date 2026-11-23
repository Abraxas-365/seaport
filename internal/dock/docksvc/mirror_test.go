package docksvc_test

import (
	"context"
	"testing"

	"github.com/Abraxas-365/seaport/internal/dock"
	"github.com/Abraxas-365/seaport/internal/dock/docksvc"
	"github.com/Abraxas-365/seaport/internal/errx"
	"github.com/Abraxas-365/seaport/internal/identity"
)

// mirrorFixture: a webhook dock (trigger-only), a Slack-like dock that
// replies with a destination "oncall".
func mirrorFixture(t *testing.T) (s *docksvc.Service, hook, chat dock.Dock) {
	t.Helper()
	repo := &memRepo{docks: map[identity.DockID]dock.Dock{}, dests: map[identity.DockID]map[string]dock.Destination{}}
	s = docksvc.New(repo, nil, 0, docksvc.Cluster{}, qrGateway{})
	hook = dock.Dock{ID: identity.NewDockID(), Organization: org, Channel: dock.ChannelWebhook, Name: "alerts", Status: dock.StatusLinked, Replies: true}
	chat = dock.Dock{ID: identity.NewDockID(), Organization: org, Channel: dock.ChannelSlack, Name: "slack", Status: dock.StatusLinked, Replies: true}
	repo.docks[hook.ID], repo.docks[chat.ID] = hook, chat
	repo.dests[chat.ID] = map[string]dock.Destination{"oncall": {Dock: chat.ID, Name: "oncall", Target: "C1"}}
	return s, hook, chat
}

func wantType(t *testing.T, err error, typ errx.Type, what string) {
	t.Helper()
	if err == nil || !errx.IsType(err, typ) {
		t.Fatalf("%s: want %v, got %v", what, typ, err)
	}
}

func TestSetMirror(t *testing.T) {
	ctx := context.Background()
	s, hook, chat := mirrorFixture(t)
	set := func(on dock.Dock, m *dock.Mirror) (dock.Dock, error) {
		return s.SetMirror(ctx, org, on.ID, dock.SetMirror{Mirror: m})
	}
	good := &dock.Mirror{Dock: chat.ID, Destination: "oncall"}

	// A webhook dock never replies, whatever its switch says.
	got, err := set(hook, good)
	if err != nil || got.Mirror == nil || *got.Mirror != *good {
		t.Fatalf("set: %+v %v", got, err)
	}
	// Clearing.
	if got, err = set(hook, nil); err != nil || got.Mirror != nil {
		t.Fatalf("clear: %+v %v", got, err)
	}
	// Validation.
	_, err = set(hook, &dock.Mirror{Destination: "oncall"})
	wantType(t, err, errx.TypeValidation, "no dock id")
	_, err = set(hook, &dock.Mirror{Dock: chat.ID, Destination: "Not A Name"})
	wantType(t, err, errx.TypeValidation, "bad destination name")
	// The dock itself, a dock that replies, a target that can't reply, a missing destination or dock.
	_, err = set(chat, good)
	wantType(t, err, errx.TypeBusiness, "a dock that replies has no mirror")
	_, err = set(hook, &dock.Mirror{Dock: hook.ID, Destination: "oncall"})
	wantType(t, err, errx.TypeBusiness, "own mirror")
	_, err = set(hook, &dock.Mirror{Dock: chat.ID, Destination: "nowhere"})
	wantType(t, err, errx.TypeNotFound, "missing destination")
	_, err = set(hook, &dock.Mirror{Dock: identity.NewDockID(), Destination: "oncall"})
	wantType(t, err, errx.TypeNotFound, "missing dock")
}

func TestSetMirrorTargetMustReply(t *testing.T) {
	ctx := context.Background()
	s, hook, chat := mirrorFixture(t)
	off := false
	// The target's owner turned replies off: it can't be somewhere to post.
	if _, err := s.Update(ctx, org, chat.ID, dock.Update{Replies: &off}); err != nil {
		t.Fatal(err)
	}
	_, err := s.SetMirror(ctx, org, hook.ID, dock.SetMirror{Mirror: &dock.Mirror{Dock: chat.ID, Destination: "oncall"}})
	wantType(t, err, errx.TypeBusiness, "target that can't reply")
}

func TestRepliesOnRefusedWithAMirror(t *testing.T) {
	ctx := context.Background()
	s, hook, chat := mirrorFixture(t)
	on, off := true, false
	// An api-like dock with replies off is trigger-only too.
	if _, err := s.Update(ctx, org, hook.ID, dock.Update{Replies: &off}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SetMirror(ctx, org, hook.ID, dock.SetMirror{Mirror: &dock.Mirror{Dock: chat.ID, Destination: "oncall"}}); err != nil {
		t.Fatal(err)
	}
	_, err := s.Update(ctx, org, hook.ID, dock.Update{Replies: &on})
	wantType(t, err, errx.TypeBusiness, "replies on with a mirror")
	// Without the mirror it is the channel that refuses, not the mirror.
	if _, err := s.SetMirror(ctx, org, hook.ID, dock.SetMirror{}); err != nil {
		t.Fatal(err)
	}
}
