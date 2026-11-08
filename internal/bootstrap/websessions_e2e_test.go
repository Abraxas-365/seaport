package bootstrap_test

import (
	"context"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Abraxas-365/seaport/internal/auth"
	"github.com/Abraxas-365/seaport/internal/auth/adapters/authpg"
	"github.com/Abraxas-365/seaport/internal/bootstrap"
	"github.com/Abraxas-365/seaport/internal/errx"
	"github.com/Abraxas-365/seaport/internal/identity"
)

// TestWebSessionStore: iam mode's sessions in Postgres — tokens sealed at
// rest, Update serialized by the row lock (one refresh among concurrent
// requests), unreadable rows (other SEAPORT_SECRET_KEY) are gone, expired
// rows swept.
func TestWebSessionStore(t *testing.T) {
	startPort(t) // wiped and migrated database
	ctx := context.Background()
	db, err := bootstrap.OpenDB(ctx, testDatabaseURL(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	sealer, _ := auth.NewSealer(strings.Repeat("s", 32))
	repo := authpg.New(db, sealer)
	org := identity.MustParseOrganizationID("11111111-1111-4111-8111-111111111111")
	now := time.Now().UTC().Truncate(time.Second)
	s := auth.Session{IDHash: auth.HashSessionID("id-1"), Organization: org, Subject: "user-1",
		Profile:   auth.Profile{Name: "Ana", OrganizationName: "Acme"},
		Tokens:    auth.Tokens{Access: "access-secret", Refresh: "refresh-secret", IDToken: "id-token", ExpiresAt: now.Add(time.Minute)},
		ExpiresAt: now.Add(time.Hour), CreatedAt: now}
	if err := repo.Create(ctx, s); err != nil {
		t.Fatal(err)
	}
	var stored string
	if err := db.GetContext(ctx, &stored, `SELECT tokens FROM web_sessions WHERE id_hash = $1`, s.IDHash); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(stored, "secret") {
		t.Fatal("tokens stored in clear")
	}
	got, err := repo.Find(ctx, s.IDHash)
	if err != nil || got.Tokens.Refresh != "refresh-secret" || got.Profile.OrganizationName != "Acme" || got.Organization != org {
		t.Fatalf("find = %+v, %v", got, err)
	}

	var calls atomic.Int32
	var wg sync.WaitGroup
	for range 6 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := repo.Update(ctx, s.IDHash, func(cur auth.Session) (auth.Session, error) {
				if cur.Tokens.Refresh != "refresh-secret" {
					return cur, nil // another request already refreshed
				}
				calls.Add(1)
				time.Sleep(20 * time.Millisecond)
				cur.Tokens = auth.Tokens{Access: "a2", Refresh: "r2", IDToken: "i2", ExpiresAt: now.Add(15 * time.Minute)}
				return cur, nil
			})
			if err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if calls.Load() != 1 {
		t.Fatalf("refreshed %d times under the row lock", calls.Load())
	}
	if got, _ = repo.Find(ctx, s.IDHash); got.Tokens.Refresh != "r2" {
		t.Fatalf("tokens = %+v", got.Tokens)
	}
	if _, err := repo.Update(ctx, s.IDHash, func(cur auth.Session) (auth.Session, error) {
		return cur, errx.Unauthorized("refused")
	}); !errx.IsType(err, errx.TypeAuthorization) {
		t.Fatalf("fn error not returned: %v", err)
	}
	if got, _ = repo.Find(ctx, s.IDHash); got.Tokens.Refresh != "r2" {
		t.Fatal("a failed update changed the row")
	}

	otherKey, _ := auth.NewSealer(strings.Repeat("o", 32))
	if _, err := authpg.New(db, otherKey).Find(ctx, s.IDHash); !errx.IsType(err, errx.TypeNotFound) {
		t.Fatalf("other key: %v, want not found", err)
	}
	if _, err := repo.Find(ctx, "missing"); !errx.IsType(err, errx.TypeNotFound) {
		t.Fatalf("missing: %v", err)
	}

	old := s
	old.IDHash, old.ExpiresAt = auth.HashSessionID("id-2"), now.Add(-time.Minute)
	if err := repo.Create(ctx, old); err != nil {
		t.Fatal(err)
	}
	if n, err := repo.DeleteExpired(ctx, now); err != nil || n != 1 {
		t.Fatalf("swept %d, %v", n, err)
	}
	if err := repo.Delete(ctx, s.IDHash); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.Find(ctx, s.IDHash); !errx.IsType(err, errx.TypeNotFound) {
		t.Fatal("deleted session still found")
	}
}
