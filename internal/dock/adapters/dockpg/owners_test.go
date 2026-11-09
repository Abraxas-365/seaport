package dockpg_test

import (
	"context"
	"os"
	"slices"
	"testing"
	"time"

	"github.com/Abraxas-365/seaport/internal/bootstrap"
	"github.com/Abraxas-365/seaport/internal/dock"
	"github.com/Abraxas-365/seaport/internal/dock/adapters/dockpg"
	"github.com/Abraxas-365/seaport/internal/errx"
	"github.com/Abraxas-365/seaport/internal/identity"
	"github.com/jmoiron/sqlx"
)

var org = identity.MustParseOrganizationID("00000000-0000-4000-8000-000000000001")

func freshDB(t *testing.T) *sqlx.DB {
	t.Helper()
	url := os.Getenv("SEAPORT_TEST_DATABASE_URL")
	if url == "" {
		if os.Getenv("CI") != "" {
			t.Fatal("SEAPORT_TEST_DATABASE_URL not set in CI")
		}
		t.Skip("SEAPORT_TEST_DATABASE_URL not set")
	}
	ctx := context.Background()
	db, err := bootstrap.OpenDB(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err := db.ExecContext(ctx, `DROP SCHEMA public CASCADE; CREATE SCHEMA public`); err != nil {
		t.Fatal(err)
	}
	if err := bootstrap.Migrate(ctx, db.DB); err != nil {
		t.Fatal(err)
	}
	return db
}

func newDock(t *testing.T, db *sqlx.DB, ch dock.Channel, status dock.Status) dock.Dock {
	t.Helper()
	d, err := dockpg.New(db).Create(context.Background(), org, identity.NewDockID(), dock.Create{Channel: ch, Name: "d"}, status, 0)
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func expire(t *testing.T, db *sqlx.DB, d identity.DockID) {
	t.Helper()
	if _, err := db.Exec(`UPDATE dock_owners SET expires_at = now() - interval '1 second' WHERE dock_id = $1`, d); err != nil {
		t.Fatal(err)
	}
}

// One live owner per dock: others see it; an expired lease is taken; only
// held, live leases renew; Claim takes ownerless docks of the asked
// channels and statuses only.
func TestOwners(t *testing.T) {
	db := freshDB(t)
	o := dockpg.NewOwners(db)
	ctx := context.Background()
	ttl := time.Minute
	a := newDock(t, db, dock.ChannelWhatsApp, dock.StatusLinked)

	if owner, err := o.Acquire(ctx, org, a.ID, "p1", ttl); err != nil || owner != "p1" {
		t.Fatalf("first acquire: %q %v", owner, err)
	}
	if owner, err := o.Acquire(ctx, org, a.ID, "p2", ttl); err != nil || owner != "p1" {
		t.Fatalf("live owner wins: %q %v", owner, err)
	}
	if owner, err := o.Acquire(ctx, org, a.ID, "p1", ttl); err != nil || owner != "p1" {
		t.Fatalf("owner renews: %q %v", owner, err)
	}
	expire(t, db, a.ID)
	if got, err := o.Renew(ctx, "p1", []identity.DockID{a.ID}, ttl); err != nil || len(got) != 0 {
		t.Fatalf("an expired lease must not renew: %v %v", got, err)
	}
	if owner, err := o.Acquire(ctx, org, a.ID, "p2", ttl); err != nil || owner != "p2" {
		t.Fatalf("expired lease taken: %q %v", owner, err)
	}
	if got, err := o.Renew(ctx, "p2", []identity.DockID{a.ID}, ttl); err != nil || !slices.Equal(got, []identity.DockID{a.ID}) {
		t.Fatalf("renew: %v %v", got, err)
	}
	other := identity.MustParseOrganizationID("00000000-0000-4000-8000-000000000002")
	if _, err := o.Acquire(ctx, other, a.ID, "p3", ttl); !errx.IsType(err, errx.TypeNotFound) {
		t.Fatalf("another organization's dock: %v", err)
	}

	b := newDock(t, db, dock.ChannelWhatsApp, dock.StatusLinking)
	newDock(t, db, dock.ChannelWhatsApp, dock.StatusLoggedOut)
	newDock(t, db, dock.ChannelAPI, dock.StatusLinked)
	statuses := []dock.Status{dock.StatusLinked, dock.StatusLinking}
	got, err := o.Claim(ctx, "p3", ttl, []dock.Channel{dock.ChannelWhatsApp}, statuses, 10)
	if err != nil || len(got) != 1 || got[0].ID != b.ID {
		t.Fatalf("claim takes only the ownerless linking/linked whatsapp dock: %+v %v", got, err)
	}
	if got, err := o.Claim(ctx, "p4", ttl, []dock.Channel{dock.ChannelWhatsApp}, statuses, 10); err != nil || len(got) != 0 {
		t.Fatalf("nothing left to claim: %+v %v", got, err)
	}

	if err := o.Release(ctx, org, b.ID, "p4"); err != nil {
		t.Fatal(err)
	}
	if owner, _ := o.Acquire(ctx, org, b.ID, "p4", ttl); owner != "p3" {
		t.Fatalf("release by a non-owner must not free the dock: %q", owner)
	}
	if err := o.ReleaseAll(ctx, "p3"); err != nil {
		t.Fatal(err)
	}
	if owner, _ := o.Acquire(ctx, org, b.ID, "p4", ttl); owner != "p4" {
		t.Fatalf("after release all: %q", owner)
	}
}

// A request reaches its process and the reply (or typed error) comes
// back; a request for a process that lost the dock comes back Moved.
func TestRelay(t *testing.T) {
	db := freshDB(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	o := dockpg.NewOwners(db)
	d := newDock(t, db, dock.ChannelWhatsApp, dock.StatusLinked)
	if _, err := o.Acquire(ctx, org, d.ID, "owner", time.Minute); err != nil {
		t.Fatal(err)
	}
	served := make(chan dock.Request, 4)
	go dockpg.NewRelay(db).Serve(ctx, "owner", func(_ context.Context, got identity.OrganizationID, req dock.Request) dock.Reply {
		if got != org {
			return dock.Reply{Error: errx.Internal("wrong organization")}
		}
		served <- req
		if req.Kind == dock.RequestUnlink {
			return dock.Reply{Error: errx.Business("not now")}
		}
		return dock.Reply{Status: dock.StatusLinking}
	})
	caller := dockpg.NewRelay(db)
	callCtx, done := context.WithTimeout(ctx, 10*time.Second)
	defer done()
	reply, err := caller.Call(callCtx, org, "owner", dock.Request{Dock: d.ID, Kind: dock.RequestLink})
	if err != nil || reply.Status != dock.StatusLinking || reply.Err() != nil {
		t.Fatalf("link: %+v %v", reply, err)
	}
	msg := dock.Outgoing{Dock: d.ID, ChatID: "c", MessageID: "m", Text: "hola"}
	if _, err := caller.Call(callCtx, org, "owner", dock.Request{Dock: d.ID, Kind: dock.RequestSend, Outgoing: &msg}); err != nil {
		t.Fatal(err)
	}
	reply, err = caller.Call(callCtx, org, "owner", dock.Request{Dock: d.ID, Kind: dock.RequestUnlink})
	if err != nil || !errx.IsType(reply.Err(), errx.TypeBusiness) || reply.Error.Message != "not now" {
		t.Fatalf("typed error crosses: %+v %v", reply, err)
	}
	if got := <-served; got.Kind != dock.RequestLink {
		t.Fatalf("first request %+v", got)
	}
	if got := <-served; got.Outgoing == nil || got.Outgoing.Text != "hola" {
		t.Fatalf("send request %+v", got)
	}

	// Nobody serves "gone", whose lease ran out: the caller gets Moved.
	e := newDock(t, db, dock.ChannelWhatsApp, dock.StatusLinked)
	if _, err := o.Acquire(ctx, org, e.ID, "gone", time.Minute); err != nil {
		t.Fatal(err)
	}
	expire(t, db, e.ID)
	if _, err := caller.Call(callCtx, org, "gone", dock.Request{Dock: e.ID, Kind: dock.RequestLink}); !dock.IsMoved(err) {
		t.Fatalf("moved: %v", err)
	}
	var left int
	if err := db.Get(&left, `SELECT count(*) FROM dock_requests`); err != nil || left != 0 {
		t.Fatalf("requests left behind: %d %v", left, err)
	}
}
