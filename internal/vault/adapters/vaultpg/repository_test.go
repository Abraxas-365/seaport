package vaultpg_test

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/jmoiron/sqlx"

	"github.com/Abraxas-365/seaport/internal/bootstrap"
	"github.com/Abraxas-365/seaport/internal/errx"
	"github.com/Abraxas-365/seaport/internal/identity"
	"github.com/Abraxas-365/seaport/internal/query"
	"github.com/Abraxas-365/seaport/internal/testdb"
	"github.com/Abraxas-365/seaport/internal/vault"
	"github.com/Abraxas-365/seaport/internal/vault/adapters/vaultpg"
)

var (
	org   = identity.MustParseOrganizationID("00000000-0000-4000-8000-000000000001")
	other = identity.MustParseOrganizationID("00000000-0000-4000-8000-000000000002")
	by    = vault.Provenance{Kind: vault.WrittenByUser, By: "Ana"}
)

func freshDB(t *testing.T) *sqlx.DB {
	t.Helper()
	url := testdb.URL(t)
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

func newVault(t *testing.T, r *vaultpg.Repository, o identity.OrganizationID, name string) vault.Vault {
	t.Helper()
	v, err := r.Create(context.Background(), o, vault.Vault{ID: identity.NewVaultID(), Name: name}, 0)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func isType(err error, typ errx.Type) bool { return errx.IsType(err, typ) }

func TestVaultsAreCountedAndUniquePerOrganization(t *testing.T) {
	r := vaultpg.New(freshDB(t))
	ctx := context.Background()
	if _, err := r.Create(ctx, org, vault.Vault{ID: identity.NewVaultID(), Name: "a"}, 2); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Create(ctx, org, vault.Vault{ID: identity.NewVaultID(), Name: "a"}, 2); !isType(err, errx.TypeConflict) {
		t.Fatalf("duplicate name: %v", err)
	}
	if _, err := r.Create(ctx, other, vault.Vault{ID: identity.NewVaultID(), Name: "a"}, 2); err != nil {
		t.Fatalf("same name in another organization: %v", err)
	}
	if _, err := r.Create(ctx, org, vault.Vault{ID: identity.NewVaultID(), Name: "b"}, 2); err != nil {
		t.Fatal(err)
	}
	_, err := r.Create(ctx, org, vault.Vault{ID: identity.NewVaultID(), Name: "c"}, 2)
	var e *errx.Error
	if !errors.As(err, &e) || e.HTTPStatus != 422 {
		t.Fatalf("over the limit: %v", err)
	}
	// Concurrent creations never overshoot the limit.
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(name string) {
			defer wg.Done()
			_, _ = r.Create(ctx, other, vault.Vault{ID: identity.NewVaultID(), Name: name}, 4)
		}("n" + string(rune('a'+i)))
	}
	wg.Wait()
	page, err := r.List(ctx, other, query.Pagination{Limit: 50})
	if err != nil || page.Page.Total != 4 {
		t.Fatalf("concurrent creations: %v total %d, want 4", err, page.Page.Total)
	}
	if _, err := r.Find(ctx, other, mustFirst(t, r, org)); !isType(err, errx.TypeNotFound) {
		t.Fatalf("another organization's vault: %v", err)
	}
}

func mustFirst(t *testing.T, r *vaultpg.Repository, o identity.OrganizationID) identity.VaultID {
	t.Helper()
	p, err := r.List(context.Background(), o, query.Pagination{Limit: 1})
	if err != nil || len(p.Items) == 0 {
		t.Fatal("no vault", err)
	}
	return p.Items[0].ID
}

func TestEntriesAreVersionedAndOptimistic(t *testing.T) {
	r := vaultpg.New(freshDB(t))
	ctx := context.Background()
	v := newVault(t, r, org, "notes")
	save := func(name, body string, base int) (vault.Entry, error) {
		return r.Save(ctx, org, vault.SaveEntry{Vault: v.ID, Name: name, Body: body, Base: base, Provenance: by}, 0)
	}
	e, err := save("faq", "# FAQ\nfirst", 0)
	if err != nil || e.Version != 1 || e.Provenance.By != "Ana" {
		t.Fatalf("create: %v %+v", err, e)
	}
	if _, err := save("faq", "again", 0); !isType(err, errx.TypeConflict) {
		t.Fatalf("create over existing: %v", err)
	}
	if _, err := save("faq", "x", 5); !isType(err, errx.TypeConflict) {
		t.Fatalf("stale base: %v", err)
	}
	if _, err := save("ghost", "x", 1); !isType(err, errx.TypeNotFound) {
		t.Fatalf("edit of a missing entry: %v", err)
	}
	conv := identity.NewConversationID()
	if _, err := r.Save(ctx, org, vault.SaveEntry{Vault: v.ID, Name: "faq", Body: "second", Base: 1,
		Provenance: vault.Provenance{Kind: vault.WrittenByAgent, By: "bot", Conversation: &conv}}, 0); err != nil {
		t.Fatal(err)
	}
	vs, err := r.Versions(ctx, org, v.ID, e.ID, 10)
	if err != nil || len(vs) != 2 || vs[0].Version != 2 || vs[0].Provenance.Conversation == nil || vs[1].Body != "# FAQ\nfirst" {
		t.Fatalf("versions: %v %+v", err, vs)
	}
	// Two writers on the same base: exactly one wins.
	var wg sync.WaitGroup
	wins := make(chan bool, 6)
	for i := 0; i < 6; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := save("faq", "race", 2)
			wins <- err == nil
		}()
	}
	wg.Wait()
	close(wins)
	n := 0
	for w := range wins {
		if w {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("%d writers won the same base, want 1", n)
	}
	// Deleting keeps the versions; a new entry of the same name starts again.
	if _, err := r.DropEntry(ctx, org, v.ID, e.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Entry(ctx, org, v.ID, e.ID); !isType(err, errx.TypeNotFound) {
		t.Fatalf("deleted entry: %v", err)
	}
	if vs, err := r.Versions(ctx, org, v.ID, e.ID, 10); err != nil || len(vs) != 3 {
		t.Fatalf("versions of a deleted entry: %v %d", err, len(vs))
	}
	// An entry limit holds.
	if _, err := r.Save(ctx, org, vault.SaveEntry{Vault: v.ID, Name: "a", Body: "x", Provenance: by}, 1); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Save(ctx, org, vault.SaveEntry{Vault: v.ID, Name: "b", Body: "x", Provenance: by}, 1); err == nil {
		t.Fatal("entry over the limit was saved")
	}
	// Another organization can't write into the vault.
	if _, err := r.Save(ctx, other, vault.SaveEntry{Vault: v.ID, Name: "z", Body: "x", Provenance: by}, 0); !isType(err, errx.TypeNotFound) {
		t.Fatalf("other organization's write: %v", err)
	}
}

func TestSearchFindsWordsInOrganizationVaultsOnly(t *testing.T) {
	r := vaultpg.New(freshDB(t))
	ctx := context.Background()
	a, b, o := newVault(t, r, org, "a"), newVault(t, r, org, "b"), newVault(t, r, other, "a")
	put := func(o identity.OrganizationID, v vault.Vault, name, body string) {
		if _, err := r.Save(ctx, o, vault.SaveEntry{Vault: v.ID, Name: name, Body: body, Provenance: by}, 0); err != nil {
			t.Fatal(err)
		}
	}
	put(org, a, "hours", "We open at nine in the morning")
	put(org, b, "returns", "Returns are accepted within 30 days (c++ gear: ask)")
	put(other, o, "hours", "We open at nine at the other place")
	hits, err := r.Search(ctx, org, []identity.VaultID{a.ID, b.ID}, "open morning", 10)
	if err != nil || len(hits) != 1 || hits[0].Name != "hours" || hits[0].VaultName != "a" || hits[0].Snippet == "" {
		t.Fatalf("search: %v %+v", err, hits)
	}
	// Only the vaults asked for, and never another organization's.
	if hits, _ := r.Search(ctx, org, []identity.VaultID{b.ID}, "open", 10); len(hits) != 0 {
		t.Fatalf("search in b: %+v", hits)
	}
	if hits, _ := r.Search(ctx, org, []identity.VaultID{o.ID}, "open", 10); len(hits) != 0 {
		t.Fatalf("search in another organization's vault: %+v", hits)
	}
	// A query with no words falls back to the text.
	if hits, err := r.Search(ctx, org, []identity.VaultID{b.ID}, "c++", 10); err != nil || len(hits) != 1 {
		t.Fatalf("symbol search: %v %+v", err, hits)
	}
	if hits, err := r.Search(ctx, org, []identity.VaultID{a.ID, b.ID}, "%", 10); err != nil || len(hits) != 0 {
		t.Fatalf("wildcard search: %v %+v", err, hits)
	}
}

func TestGrantsAreCompositeAndCascade(t *testing.T) {
	db := freshDB(t)
	r := vaultpg.New(db)
	ctx := context.Background()
	v := newVault(t, r, org, "g")
	agent, dockID := identity.NewAgentID(), identity.NewDockID()
	if _, err := db.Exec(`INSERT INTO agents (id, organization_id, name) VALUES ($1, $2, 'ag')`, agent, org); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO docks (id, organization_id, channel, name, address, status) VALUES ($1, $2, 'api', 'dk', '', 'linked')`, dockID, org); err != nil {
		t.Fatal(err)
	}
	if err := r.SetAgentGrant(ctx, org, v.ID, agent, vault.ModeRead); err != nil {
		t.Fatal(err)
	}
	if err := r.SetAgentGrant(ctx, org, v.ID, agent, vault.ModeWrite); err != nil {
		t.Fatalf("a grant changes mode: %v", err)
	}
	if err := r.SetDockGrant(ctx, org, v.ID, dockID, vault.ModeRead); err != nil {
		t.Fatal(err)
	}
	// An agent or dock of another organization can't be granted.
	if err := r.SetAgentGrant(ctx, other, v.ID, agent, vault.ModeRead); err == nil {
		t.Fatal("grant across organizations")
	}
	if err := r.SetDockGrant(ctx, other, v.ID, dockID, vault.ModeRead); err == nil {
		t.Fatal("dock grant across organizations")
	}
	g, err := r.Grants(ctx, org, v.ID)
	if err != nil || len(g.Agents) != 1 || g.Agents[0].Mode != vault.ModeWrite || len(g.Docks) != 1 {
		t.Fatalf("grants: %v %+v", err, g)
	}
	if any, err := r.AnyForAgent(ctx, org, agent); err != nil || !any {
		t.Fatalf("any for agent: %v %v", any, err)
	}
	if err := r.DeleteAgentGrant(ctx, org, v.ID, agent); err != nil {
		t.Fatal(err)
	}
	if err := r.DeleteAgentGrant(ctx, org, v.ID, agent); !isType(err, errx.TypeNotFound) {
		t.Fatalf("revoking twice: %v", err)
	}
	if any, _ := r.AnyForAgent(ctx, org, agent); any {
		t.Fatal("an agent with no grant and no dock has a vault")
	}
	// Through a dock the agent works.
	if _, err := db.Exec(`INSERT INTO dock_agents (organization_id, dock_id, agent_id, is_default) VALUES ($1, $2, $3, true)`, org, dockID, agent); err != nil {
		t.Fatal(err)
	}
	if any, err := r.AnyForAgent(ctx, org, agent); err != nil || !any {
		t.Fatalf("any through a dock: %v %v", any, err)
	}
	// Deleting a vault removes everything of it.
	if _, err := r.Save(ctx, org, vault.SaveEntry{Vault: v.ID, Name: "e", Body: "x", Provenance: by}, 0); err != nil {
		t.Fatal(err)
	}
	if err := r.Delete(ctx, other, v.ID); !isType(err, errx.TypeNotFound) {
		t.Fatalf("another organization's delete: %v", err)
	}
	if err := r.Delete(ctx, org, v.ID); err != nil {
		t.Fatal(err)
	}
	for _, table := range []string{"vault_entries", "vault_entry_versions", "vault_agents", "vault_docks"} {
		var n int
		if err := db.Get(&n, `SELECT count(*) FROM `+table); err != nil || n != 0 {
			t.Fatalf("%s after deleting the vault: %d %v", table, n, err)
		}
	}
}
