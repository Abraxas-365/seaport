package dockpg_test

import (
	"context"
	"testing"

	"github.com/Abraxas-365/seaport/internal/dock"
	"github.com/Abraxas-365/seaport/internal/dock/adapters/dockpg"
	"github.com/Abraxas-365/seaport/internal/errx"
)

// The database keeps a mirror on trigger-only docks whatever the service
// checked before: a dock whose replies are on can't get one, and replies
// can't be turned on while one is set (the race of the two requests).
// Webhook docks can't reply by channel, so their stored switch doesn't
// matter. Removing the destination clears the mirror.
func TestMirrorOnlyOnTriggerDocks(t *testing.T) {
	db := freshDB(t)
	r := dockpg.New(db)
	ctx := context.Background()
	slack := newDock(t, db, dock.ChannelSlack, dock.StatusLinked)
	if _, err := r.PutDestination(ctx, org, dock.AddDestination{Dock: slack.ID, Name: "oncall", Target: "C0ONCALL"}, 0); err != nil {
		t.Fatal(err)
	}
	m := &dock.Mirror{Dock: slack.ID, Destination: "oncall"}

	api := newDock(t, db, dock.ChannelAPI, dock.StatusLinked)
	if _, err := r.SetMirror(ctx, org, api.ID, m); !errx.IsType(err, errx.TypeValidation) {
		t.Fatalf("mirror on a dock that replies: %v", err)
	}
	off, on := false, true
	if _, err := r.Update(ctx, org, api.ID, dock.Update{Replies: &off}); err != nil {
		t.Fatal(err)
	}
	if d, err := r.SetMirror(ctx, org, api.ID, m); err != nil || d.Mirror == nil || *d.Mirror != *m {
		t.Fatalf("mirror on a trigger dock: %+v %v", d.Mirror, err)
	}
	if _, err := r.Update(ctx, org, api.ID, dock.Update{Replies: &on}); !errx.IsType(err, errx.TypeValidation) {
		t.Fatalf("replies on with a mirror: %v", err)
	}

	hook := newDock(t, db, dock.ChannelWebhook, dock.StatusLinked)
	if d, err := r.SetMirror(ctx, org, hook.ID, m); err != nil || d.Mirror == nil {
		t.Fatalf("mirror on a webhook dock: %+v %v", d.Mirror, err)
	}

	if err := r.RemoveDestination(ctx, org, slack.ID, "oncall"); err != nil {
		t.Fatal(err)
	}
	for _, id := range []dock.Dock{api, hook} {
		if d, err := r.Find(ctx, org, id.ID); err != nil || d.Mirror != nil {
			t.Fatalf("mirror after its destination was removed: %+v %v", d.Mirror, err)
		}
	}
}
