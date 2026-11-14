package dockemail_test

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/Abraxas-365/seaport/internal/bootstrap"
	"github.com/Abraxas-365/seaport/internal/dock"
	"github.com/Abraxas-365/seaport/internal/dock/adapters/dockemail"
	"github.com/Abraxas-365/seaport/internal/dock/adapters/dockpg"
	"github.com/Abraxas-365/seaport/internal/identity"
	"github.com/Abraxas-365/seaport/internal/seal"
	"github.com/jmoiron/sqlx"
)

var org = identity.MustParseOrganizationID("00000000-0000-4000-8000-000000000001")

func freshStore(t *testing.T) (*dockemail.Store, *sqlx.DB, dock.Dock) {
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
	d, err := dockpg.New(db).Create(ctx, org, identity.NewDockID(), dock.Create{Channel: dock.ChannelEmail, Name: "mail"}, dock.StatusLinking, 0)
	if err != nil {
		t.Fatal(err)
	}
	sealer, err := seal.New("store-test-secret-key-0123456789abcdef", dockemail.SealInfo)
	if err != nil {
		t.Fatal(err)
	}
	return dockemail.NewStore(db, sealer), db, d
}

func account(d dock.Dock) dockemail.Account {
	ep := dockemail.Endpoint{Host: "imap.example.com", Port: 993, TLS: dockemail.TLS}
	return dockemail.Account{Dock: d.ID, Organization: d.Organization, Address: "bot@example.com", Username: "bot@example.com",
		Folder: "INBOX", IMAP: ep, SMTP: dockemail.Endpoint{Host: "smtp.example.com", Port: 465, TLS: dockemail.TLS}}
}

// A message that names a thread's Message-ID joins it only when its sender
// already takes part: headers are the sender's say-so.
func TestThreadJoinNeedsAParticipant(t *testing.T) {
	s, _, d := freshStore(t)
	ctx := context.Background()
	if err := s.Note(ctx, org, d.ID, dockemail.Thread{Chat: "m1", Subject: "Pedido", Key: "pedido", Participants: []string{"ana@example.com"}}, "m1@example.com"); err != nil {
		t.Fatal(err)
	}
	ids := []string{"x@evil.example", "m1@example.com"}
	if chat, ok, err := s.ThreadOf(ctx, d.ID, ids, []string{"eve@example.net"}); err != nil || ok {
		t.Fatalf("a stranger joined %q (%v)", chat, err)
	}
	if chat, ok, err := s.ThreadOf(ctx, d.ID, ids, []string{"eve@example.net", "ana@example.com"}); err != nil || !ok || chat != "m1" {
		t.Fatalf("a participant did not join: %q %v %v", chat, ok, err)
	}
	if _, ok, _ := s.ThreadBySubject(ctx, d.ID, "pedido", []string{"eve@example.net"}, time.Hour); ok {
		t.Fatal("a stranger joined by subject")
	}
	if chat, ok, _ := s.ThreadBySubject(ctx, d.ID, "pedido", []string{"ana@example.com"}, time.Hour); !ok || chat != "m1" {
		t.Fatalf("a participant did not join by subject: %q", chat)
	}
}

// A session that outlived a new link can't overwrite the new cursor.
func TestStaleSessionCannotMoveTheCursor(t *testing.T) {
	s, _, d := freshStore(t)
	ctx := context.Background()
	a := account(d)
	if err := s.Save(ctx, a, dockemail.Cursor{Validity: 7, UID: 10}); err != nil {
		t.Fatal(err)
	}
	old, err := s.Find(ctx, d.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SetCursor(ctx, old, dockemail.Cursor{Validity: 7, UID: 11}); err != nil {
		t.Fatal(err)
	}
	a.Folder = "Other"
	if err := s.Save(ctx, a, dockemail.Cursor{Validity: 9, UID: 3}); err != nil {
		t.Fatal(err)
	}
	if err := s.SetCursor(ctx, old, dockemail.Cursor{Validity: 7, UID: 12}); !errors.Is(err, dockemail.ErrStaleAccount) {
		t.Fatalf("stale SetCursor = %v", err)
	}
	cur, err := s.Find(ctx, d.ID)
	if err != nil || cur.UIDValidity != 9 || cur.LastUID != 3 {
		t.Fatalf("cursor = %d/%d, %v", cur.UIDValidity, cur.LastUID, err)
	}
	if err := s.SetCursor(ctx, cur, dockemail.Cursor{Validity: 9, UID: 4}); err != nil {
		t.Fatal(err)
	}
}

// Delete forgets the account, its threads and their message ids.
func TestDeleteForgetsThreads(t *testing.T) {
	s, db, d := freshStore(t)
	ctx := context.Background()
	if err := s.Save(ctx, account(d), dockemail.Cursor{Validity: 1}); err != nil {
		t.Fatal(err)
	}
	if err := s.Note(ctx, org, d.ID, dockemail.Thread{Chat: "m1", Key: "k", Participants: []string{"ana@example.com"}}, "m1@example.com"); err != nil {
		t.Fatal(err)
	}
	if err := s.Delete(ctx, d.ID); err != nil {
		t.Fatal(err)
	}
	for _, table := range []string{"email_accounts", "email_threads", "email_messages"} {
		var n int
		if err := db.GetContext(ctx, &n, `SELECT count(*) FROM `+table); err != nil || n != 0 {
			t.Fatalf("%s has %d rows (%v)", table, n, err)
		}
	}
}
