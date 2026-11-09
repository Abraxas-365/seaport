package crewpg_test

import (
	"context"
	"os"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/Abraxas-365/seaport/internal/bootstrap"
	"github.com/Abraxas-365/seaport/internal/crew"
	"github.com/Abraxas-365/seaport/internal/crew/adapters/crewpg"
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

// newAgent stores an agent row (crewpg reads agents, it doesn't make them).
func newAgent(t *testing.T, db *sqlx.DB, org identity.OrganizationID, name string) identity.AgentID {
	t.Helper()
	id := identity.NewAgentID()
	if _, err := db.Exec(`INSERT INTO agents (id, organization_id, name) VALUES ($1, $2, $3)`, id, org, name); err != nil {
		t.Fatal(err)
	}
	return id
}

func connection(session, process, credential string) crew.Connection {
	return crew.Connection{Session: session, Process: process, Credential: credential, ConnectedAt: time.Now().UTC()}
}

// Concurrent opens (from any process) never pass the limit, and closing
// frees a slot; opening rows count but only ready ones are listed.
func TestOpenQuota(t *testing.T) {
	db := freshDB(t)
	p := crewpg.NewPresence(db)
	ctx := context.Background()
	var wg sync.WaitGroup
	var mu sync.Mutex
	var opened []string
	for i := range 12 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			s := "s" + string(rune('a'+i))
			err := p.Open(ctx, org, connection(s, "p"+string(rune('0'+i%3)), ""), 5)
			if err == nil {
				mu.Lock()
				opened = append(opened, s)
				mu.Unlock()
			} else if !errx.IsType(err, errx.TypeBusiness) {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if len(opened) != 5 {
		t.Fatalf("opened %d with limit 5", len(opened))
	}
	if err := p.Open(ctx, org, connection("extra", "p0", ""), 5); !errx.IsType(err, errx.TypeBusiness) {
		t.Fatalf("over the limit: %v", err)
	}
	other := identity.MustParseOrganizationID("00000000-0000-4000-8000-000000000002")
	if err := p.Open(ctx, other, connection("theirs", "p0", ""), 5); err != nil {
		t.Fatalf("other organization: %v", err)
	}
	if err := p.Close(ctx, org, opened[0]); err != nil {
		t.Fatal(err)
	}
	if err := p.Open(ctx, org, connection("extra", "p0", ""), 5); err != nil {
		t.Fatalf("after close: %v", err)
	}

	pm := newAgent(t, db, org, "pm")
	if err := p.Ready(ctx, org, "extra", crew.Agent{Name: "rness", AgentID: pm, Concurrency: 2}); err != nil {
		t.Fatal(err)
	}
	d := crew.AgentDelivery{ID: identity.NewDeliveryID(), Conversation: identity.NewConversationID(), Ships: 2, Since: time.Now().UTC().Truncate(time.Microsecond)}
	if err := p.SetDeliveries(ctx, org, "extra", []crew.AgentDelivery{d}); err != nil {
		t.Fatal(err)
	}
	st, err := p.Status(ctx, org)
	if err != nil {
		t.Fatal(err)
	}
	if len(st.Agents) != 1 || st.Agents[0].Name != "rness" || st.Agents[0].AgentID != pm || st.Agents[0].AgentName != "pm" ||
		len(st.Agents[0].Deliveries) != 1 || st.Agents[0].Deliveries[0] != d {
		t.Fatalf("status: %+v", st)
	}
}

// Touch keeps a process's rows, tells it which to close (reaped, a revoked
// or expired token, or a deleted agent) and never touches another
// process's rows;
// Reap takes only rows untouched for the stale period.
func TestTouchAndReap(t *testing.T) {
	db := freshDB(t)
	p := crewpg.NewPresence(db)
	tokens := crewpg.New(db)
	ctx := context.Background()
	pm, gone := newAgent(t, db, org, "pm"), newAgent(t, db, org, "gone")
	newToken := func(expires *time.Time) identity.CrewTokenID {
		_, hash, hint := crew.NewTokenValue()
		tok, err := tokens.Create(ctx, org, identity.NewCrewTokenID(), crew.CreateToken{Name: "t", Agent: pm, ExpiresAt: expires}, hash, hint, 10)
		if err != nil {
			t.Fatal(err)
		}
		return tok.ID
	}
	live, revoked := newToken(nil), newToken(nil)
	soon := time.Now().Add(time.Hour)
	expiring := newToken(&soon)
	for s, cred := range map[string]string{"live": live.String(), "open": "", "revoked": revoked.String(),
		"expired": expiring.String(), "deleted": ""} {
		if err := p.Open(ctx, org, connection(s, "a", cred), 0); err != nil {
			t.Fatal(err)
		}
		as := pm
		if s == "deleted" {
			as = gone
		}
		if err := p.Ready(ctx, org, s, crew.Agent{Name: s, AgentID: as, Concurrency: 1}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.Exec(`UPDATE agents SET deleted_at = now() WHERE id = $1`, gone); err != nil {
		t.Fatal(err)
	}
	if err := p.Open(ctx, org, connection("elsewhere", "b", revoked.String()), 0); err != nil {
		t.Fatal(err)
	}
	if err := tokens.Revoke(ctx, org, revoked); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE crew_tokens SET expires_at = now() - interval '1 second' WHERE id = $1`, expiring); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE crew_connections SET seen_at = now() - interval '1 hour'`); err != nil {
		t.Fatal(err)
	}

	closing, err := p.Touch(ctx, "a", []string{"live", "open", "revoked", "expired", "deleted", "gone"})
	if err != nil {
		t.Fatal(err)
	}
	slices.Sort(closing)
	if !slices.Equal(closing, []string{"deleted", "expired", "gone", "revoked"}) {
		t.Fatalf("closing %v", closing)
	}
	reaped, err := p.Reap(ctx, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if len(reaped) != 1 || reaped[0] != (crew.Reaped{Organization: org, Session: "elsewhere"}) {
		t.Fatalf("reaped %+v (b stopped touching; a's rows are fresh)", reaped)
	}
	if closing, err := p.Touch(ctx, "b", []string{"elsewhere"}); err != nil || !slices.Equal(closing, []string{"elsewhere"}) {
		t.Fatalf("b's touch after the reap: %v %v", closing, err)
	}
}

// A signal reaches every listener, after connected was called.
func TestSignals(t *testing.T) {
	db := freshDB(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	got := make(chan crew.Signal, 4)
	connected := make(chan struct{}, 4)
	go crewpg.NewPresence(db).Listen(ctx, func() { connected <- struct{}{} }, func(s crew.Signal) { got <- s })
	select {
	case <-connected:
	case <-time.After(5 * time.Second):
		t.Fatal("not listening")
	}
	want := crew.Signal{Kind: crew.SignalRevoke, Organization: org, Token: identity.NewCrewTokenID(), From: "p1"}
	if err := crewpg.NewPresence(db).Signal(ctx, want); err != nil {
		t.Fatal(err)
	}
	select {
	case s := <-got:
		if s != want {
			t.Fatalf("signal %+v", s)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("signal lost")
	}
}
