// Package vaultpg is the Postgres repository of vaults: entries, their
// versions and the grants to agents and docks. Counting a vault's entries
// and writing one happen under the vault row's lock, and counting an
// organization's vaults under an advisory lock, so concurrent writers can't
// overshoot a limit or both save the same version.
package vaultpg

import (
	"context"
	"strconv"
	"strings"
	"time"

	"github.com/Abraxas-365/seaport/internal/errx"
	"github.com/Abraxas-365/seaport/internal/identity"
	"github.com/Abraxas-365/seaport/internal/pgerr"
	"github.com/Abraxas-365/seaport/internal/query"
	"github.com/Abraxas-365/seaport/internal/vault"
	"github.com/jmoiron/sqlx"
)

// createLock serializes one organization's vault creations while its
// vaults are counted (second key: hashtext(organization)).
const createLock = 7_401_011

const vaultColumns = `v.id, v.organization_id, v.name, v.title, v.description, v.created_at, v.updated_at,
	(SELECT count(*) FROM vault_entries e WHERE e.organization_id = v.organization_id AND e.vault_id = v.id) AS entries`

var messages = pgerr.Messages{NotFound: "vault not found", Unique: "a vault with this name already exists",
	ForeignKey: "vault, agent or dock not found"}

type Repository struct{ db *sqlx.DB }

func New(db *sqlx.DB) *Repository { return &Repository{db: db} }

var _ vault.Repository = (*Repository)(nil)

func (r *Repository) Create(ctx context.Context, org identity.OrganizationID, v vault.Vault, limit int) (vault.Vault, error) {
	var out vault.Vault
	tx, err := r.db.BeginTxx(ctx, nil)
	if err != nil {
		return out, pgerr.Map(err, "begin create vault")
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock($1::int, hashtext($2::text))`, createLock, org); err != nil {
		return out, pgerr.Map(err, "lock vaults")
	}
	if limit > 0 {
		var n int
		if err := tx.GetContext(ctx, &n, `SELECT count(*) FROM vaults WHERE organization_id = $1`, org); err != nil {
			return out, pgerr.Map(err, "count vaults")
		}
		if n >= limit {
			return out, errx.QuotaExceeded("vaults", limit, "an organization can have at most "+strconv.Itoa(limit)+" vaults; delete one first")
		}
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO vaults (id, organization_id, name, title, description) VALUES ($1, $2, $3, $4, $5)`,
		v.ID, org, v.Name, v.Title, v.Description); err != nil {
		return out, pgerr.MapWith(err, "create vault", messages)
	}
	if err := tx.GetContext(ctx, &out, `SELECT `+vaultColumns+` FROM vaults v WHERE v.organization_id = $1 AND v.id = $2`, org, v.ID); err != nil {
		return out, pgerr.Map(err, "read vault")
	}
	if err := tx.Commit(); err != nil {
		return out, pgerr.Map(err, "commit vault")
	}
	return out, nil
}

func (r *Repository) Find(ctx context.Context, org identity.OrganizationID, id identity.VaultID) (vault.Vault, error) {
	var v vault.Vault
	if err := r.db.GetContext(ctx, &v, `SELECT `+vaultColumns+` FROM vaults v WHERE v.organization_id = $1 AND v.id = $2`, org, id); err != nil {
		return v, pgerr.MapWith(err, "find vault", messages)
	}
	return v, nil
}

func (r *Repository) FindMany(ctx context.Context, org identity.OrganizationID, ids []identity.VaultID) ([]vault.Vault, error) {
	out := []vault.Vault{}
	if err := r.db.SelectContext(ctx, &out, `SELECT `+vaultColumns+` FROM vaults v
		WHERE v.organization_id = $1 AND v.id = ANY($2::uuid[]) ORDER BY v.name`, org, uuids(ids)); err != nil {
		return nil, pgerr.Map(err, "find vaults")
	}
	return out, nil
}

func (r *Repository) List(ctx context.Context, org identity.OrganizationID, page query.Pagination) (query.Paginated[vault.Vault], error) {
	out := query.Paginated[vault.Vault]{Items: []vault.Vault{}, Page: query.Page{Limit: page.Limit, Offset: page.Offset}}
	if err := r.db.GetContext(ctx, &out.Page.Total, `SELECT count(*) FROM vaults WHERE organization_id = $1`, org); err != nil {
		return out, pgerr.Map(err, "count vaults")
	}
	if err := r.db.SelectContext(ctx, &out.Items, `SELECT `+vaultColumns+` FROM vaults v WHERE v.organization_id = $1
		ORDER BY v.name LIMIT $2 OFFSET $3`, org, page.Limit, page.Offset); err != nil {
		return out, pgerr.Map(err, "list vaults")
	}
	return out, nil
}

func (r *Repository) Update(ctx context.Context, org identity.OrganizationID, id identity.VaultID, in vault.Update) (vault.Vault, error) {
	res, err := r.db.ExecContext(ctx, `UPDATE vaults SET title = COALESCE($3, title), description = COALESCE($4, description), updated_at = now()
		WHERE organization_id = $1 AND id = $2`, org, id, in.Title, in.Description)
	if err != nil {
		return vault.Vault{}, pgerr.MapWith(err, "update vault", messages)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return vault.Vault{}, pgerr.MapWith(pgerr.ErrNoRows, "update vault", messages)
	}
	return r.Find(ctx, org, id)
}

func (r *Repository) Delete(ctx context.Context, org identity.OrganizationID, id identity.VaultID) error {
	res, err := r.db.ExecContext(ctx, `DELETE FROM vaults WHERE organization_id = $1 AND id = $2`, org, id)
	if err != nil {
		return pgerr.MapWith(err, "delete vault", messages)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return pgerr.MapWith(pgerr.ErrNoRows, "delete vault", messages)
	}
	return nil
}

func (r *Repository) SetAgentGrant(ctx context.Context, org identity.OrganizationID, id identity.VaultID, agent identity.AgentID, mode vault.Mode) error {
	if _, err := r.db.ExecContext(ctx, `INSERT INTO vault_agents (organization_id, vault_id, agent_id, mode) VALUES ($1, $2, $3, $4)
		ON CONFLICT (organization_id, vault_id, agent_id) DO UPDATE SET mode = EXCLUDED.mode`, org, id, agent, string(mode)); err != nil {
		return pgerr.MapWith(err, "grant vault to agent", messages)
	}
	return nil
}

func (r *Repository) DeleteAgentGrant(ctx context.Context, org identity.OrganizationID, id identity.VaultID, agent identity.AgentID) error {
	res, err := r.db.ExecContext(ctx, `DELETE FROM vault_agents WHERE organization_id = $1 AND vault_id = $2 AND agent_id = $3`, org, id, agent)
	if err != nil {
		return pgerr.Map(err, "revoke vault from agent")
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return pgerr.MapWith(pgerr.ErrNoRows, "revoke vault from agent", pgerr.Messages{NotFound: "the agent has no grant on this vault"})
	}
	return nil
}

func (r *Repository) SetDockGrant(ctx context.Context, org identity.OrganizationID, id identity.VaultID, dock identity.DockID, mode vault.Mode) error {
	if _, err := r.db.ExecContext(ctx, `INSERT INTO vault_docks (organization_id, vault_id, dock_id, mode) VALUES ($1, $2, $3, $4)
		ON CONFLICT (organization_id, vault_id, dock_id) DO UPDATE SET mode = EXCLUDED.mode`, org, id, dock, string(mode)); err != nil {
		return pgerr.MapWith(err, "grant vault to dock", messages)
	}
	return nil
}

func (r *Repository) DeleteDockGrant(ctx context.Context, org identity.OrganizationID, id identity.VaultID, dock identity.DockID) error {
	res, err := r.db.ExecContext(ctx, `DELETE FROM vault_docks WHERE organization_id = $1 AND vault_id = $2 AND dock_id = $3`, org, id, dock)
	if err != nil {
		return pgerr.Map(err, "revoke vault from dock")
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return pgerr.MapWith(pgerr.ErrNoRows, "revoke vault from dock", pgerr.Messages{NotFound: "the dock has no grant on this vault"})
	}
	return nil
}

func (r *Repository) Grants(ctx context.Context, org identity.OrganizationID, id identity.VaultID) (vault.Grants, error) {
	out := vault.Grants{Agents: []vault.AgentGrant{}, Docks: []vault.DockGrant{}}
	if err := r.db.SelectContext(ctx, &out.Agents, `SELECT g.agent_id, a.name, g.mode, g.created_at FROM vault_agents g
		JOIN agents a ON a.organization_id = g.organization_id AND a.id = g.agent_id AND a.deleted_at IS NULL
		WHERE g.organization_id = $1 AND g.vault_id = $2 ORDER BY a.name`, org, id); err != nil {
		return out, pgerr.Map(err, "list agent grants")
	}
	if err := r.db.SelectContext(ctx, &out.Docks, `SELECT g.dock_id, d.name, d.channel, g.mode, g.created_at FROM vault_docks g
		JOIN docks d ON d.organization_id = g.organization_id AND d.id = g.dock_id
		WHERE g.organization_id = $1 AND g.vault_id = $2 ORDER BY d.name, d.id`, org, id); err != nil {
		return out, pgerr.Map(err, "list dock grants")
	}
	return out, nil
}

func (r *Repository) AgentGrants(ctx context.Context, org identity.OrganizationID, agent identity.AgentID) ([]vault.Grant, error) {
	out := []vault.Grant{}
	if err := r.db.SelectContext(ctx, &out, `SELECT vault_id, mode FROM vault_agents
		WHERE organization_id = $1 AND agent_id = $2 ORDER BY created_at, vault_id`, org, agent); err != nil {
		return nil, pgerr.Map(err, "agent grants")
	}
	return out, nil
}

func (r *Repository) DockGrants(ctx context.Context, org identity.OrganizationID, dock identity.DockID) ([]vault.Grant, error) {
	out := []vault.Grant{}
	if err := r.db.SelectContext(ctx, &out, `SELECT vault_id, mode FROM vault_docks
		WHERE organization_id = $1 AND dock_id = $2 ORDER BY created_at, vault_id`, org, dock); err != nil {
		return nil, pgerr.Map(err, "dock grants")
	}
	return out, nil
}

func (r *Repository) AnyForAgent(ctx context.Context, org identity.OrganizationID, agent identity.AgentID) (bool, error) {
	var any bool
	if err := r.db.GetContext(ctx, &any, `SELECT EXISTS (SELECT 1 FROM vault_agents WHERE organization_id = $1 AND agent_id = $2)
		OR EXISTS (SELECT 1 FROM vault_docks vd JOIN dock_agents da ON da.organization_id = vd.organization_id AND da.dock_id = vd.dock_id
			WHERE vd.organization_id = $1 AND da.agent_id = $2)`, org, agent); err != nil {
		return false, pgerr.Map(err, "any vault for agent")
	}
	return any, nil
}

// entryRow is vault_entries as stored (the provenance is flat there).
type entryRow struct {
	ID           identity.EntryID         `db:"id"`
	Organization identity.OrganizationID  `db:"organization_id"`
	Vault        identity.VaultID         `db:"vault_id"`
	Name         string                   `db:"name"`
	Body         string                   `db:"body"`
	Version      int                      `db:"version"`
	Kind         string                   `db:"written_by_kind"`
	By           string                   `db:"written_by"`
	Conversation *identity.ConversationID `db:"conversation_id"`
	Dock         *identity.DockID         `db:"dock_id"`
	CreatedAt    time.Time                `db:"created_at"`
	UpdatedAt    time.Time                `db:"updated_at"`
}

const entryColumns = `id, organization_id, vault_id, name, body, version, written_by_kind, written_by, conversation_id, dock_id, created_at, updated_at`

func (e entryRow) entry() vault.Entry {
	return vault.Entry{ID: e.ID, Organization: e.Organization, Vault: e.Vault, Name: e.Name, Body: e.Body, Version: e.Version,
		Provenance: provenance(e.Kind, e.By, e.Conversation, e.Dock), CreatedAt: e.CreatedAt, UpdatedAt: e.UpdatedAt}
}

func provenance(kind, by string, conv *identity.ConversationID, dock *identity.DockID) vault.Provenance {
	return vault.Provenance{Kind: vault.ProvenanceKind(kind), By: by, Conversation: conv, Dock: dock}
}

func (r *Repository) Save(ctx context.Context, org identity.OrganizationID, in vault.SaveEntry, limit int) (vault.Entry, error) {
	tx, err := r.db.BeginTxx(ctx, nil)
	if err != nil {
		return vault.Entry{}, pgerr.Map(err, "begin save entry")
	}
	defer func() { _ = tx.Rollback() }()
	// The vault row serializes the vault's writers (and is the tenant check).
	var locked identity.VaultID
	if err := tx.GetContext(ctx, &locked, `SELECT id FROM vaults WHERE organization_id = $1 AND id = $2 FOR UPDATE`, org, in.Vault); err != nil {
		return vault.Entry{}, pgerr.MapWith(err, "lock vault", messages)
	}
	var cur entryRow
	err = tx.GetContext(ctx, &cur, `SELECT `+entryColumns+` FROM vault_entries WHERE organization_id = $1 AND vault_id = $2 AND name = $3`, org, in.Vault, in.Name)
	found := err == nil
	if err != nil && !pgerr.IsNoRows(err) {
		return vault.Entry{}, pgerr.Map(err, "find entry")
	}
	p := in.Provenance
	var out entryRow
	switch {
	case !found && in.Base > 0:
		return vault.Entry{}, errx.NotFound("entry " + in.Name + " not found")
	case found && in.Base == 0:
		return vault.Entry{}, errx.Conflict("entry " + in.Name + " already exists: read it and save again with its current version as base")
	case found && cur.Version != in.Base:
		return vault.Entry{}, errx.Conflict("entry " + in.Name + " changed (now at version " + strconv.Itoa(cur.Version) + "): read it again and redo the edit")
	case found:
		err = tx.GetContext(ctx, &out, `UPDATE vault_entries SET body = $4, version = version + 1, written_by_kind = $5, written_by = $6,
			conversation_id = $7, dock_id = $8, updated_at = now()
			WHERE organization_id = $1 AND vault_id = $2 AND id = $3 RETURNING `+entryColumns,
			org, in.Vault, cur.ID, in.Body, string(p.Kind), p.By, p.Conversation, p.Dock)
	default:
		if limit > 0 {
			var n int
			if err := tx.GetContext(ctx, &n, `SELECT count(*) FROM vault_entries WHERE organization_id = $1 AND vault_id = $2`, org, in.Vault); err != nil {
				return vault.Entry{}, pgerr.Map(err, "count entries")
			}
			if n >= limit {
				return vault.Entry{}, errx.QuotaExceeded("vault_entries", limit, "a vault can have at most "+strconv.Itoa(limit)+" entries; delete one first")
			}
		}
		err = tx.GetContext(ctx, &out, `INSERT INTO vault_entries (id, organization_id, vault_id, name, body, version, written_by_kind, written_by, conversation_id, dock_id)
			VALUES ($1, $2, $3, $4, $5, 1, $6, $7, $8, $9) RETURNING `+entryColumns,
			identity.NewEntryID(), org, in.Vault, in.Name, in.Body, string(p.Kind), p.By, p.Conversation, p.Dock)
	}
	if err != nil {
		return vault.Entry{}, pgerr.MapWith(err, "save entry", messages)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO vault_entry_versions (organization_id, vault_id, entry_id, version, name, body, written_by_kind, written_by, conversation_id, dock_id)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)`,
		org, in.Vault, out.ID, out.Version, out.Name, out.Body, out.Kind, out.By, out.Conversation, out.Dock); err != nil {
		return vault.Entry{}, pgerr.Map(err, "append version")
	}
	if _, err := tx.ExecContext(ctx, `UPDATE vaults SET updated_at = now() WHERE organization_id = $1 AND id = $2`, org, in.Vault); err != nil {
		return vault.Entry{}, pgerr.Map(err, "touch vault")
	}
	if err := tx.Commit(); err != nil {
		return vault.Entry{}, pgerr.Map(err, "commit entry")
	}
	return out.entry(), nil
}

func (r *Repository) DropEntry(ctx context.Context, org identity.OrganizationID, id identity.VaultID, entry identity.EntryID) (vault.Entry, error) {
	tx, err := r.db.BeginTxx(ctx, nil)
	if err != nil {
		return vault.Entry{}, pgerr.Map(err, "begin delete entry")
	}
	defer func() { _ = tx.Rollback() }()
	// Under the vault row's lock like Save: a save never finds an entry
	// that is gone by the time it updates it.
	var locked identity.VaultID
	if err := tx.GetContext(ctx, &locked, `SELECT id FROM vaults WHERE organization_id = $1 AND id = $2 FOR UPDATE`, org, id); err != nil {
		return vault.Entry{}, pgerr.MapWith(err, "lock vault", messages)
	}
	var row entryRow
	if err := tx.GetContext(ctx, &row, `DELETE FROM vault_entries WHERE organization_id = $1 AND vault_id = $2 AND id = $3 RETURNING `+entryColumns,
		org, id, entry); err != nil {
		return vault.Entry{}, pgerr.MapWith(err, "delete entry", pgerr.Messages{NotFound: "entry not found"})
	}
	if _, err := tx.ExecContext(ctx, `UPDATE vaults SET updated_at = now() WHERE organization_id = $1 AND id = $2`, org, id); err != nil {
		return vault.Entry{}, pgerr.Map(err, "touch vault")
	}
	if err := tx.Commit(); err != nil {
		return vault.Entry{}, pgerr.Map(err, "commit delete entry")
	}
	return row.entry(), nil
}

func (r *Repository) Entry(ctx context.Context, org identity.OrganizationID, id identity.VaultID, entry identity.EntryID) (vault.Entry, error) {
	var row entryRow
	if err := r.db.GetContext(ctx, &row, `SELECT `+entryColumns+` FROM vault_entries WHERE organization_id = $1 AND vault_id = $2 AND id = $3`,
		org, id, entry); err != nil {
		return vault.Entry{}, pgerr.MapWith(err, "find entry", pgerr.Messages{NotFound: "entry not found"})
	}
	return row.entry(), nil
}

func (r *Repository) EntryByName(ctx context.Context, org identity.OrganizationID, id identity.VaultID, name string) (vault.Entry, error) {
	var row entryRow
	if err := r.db.GetContext(ctx, &row, `SELECT `+entryColumns+` FROM vault_entries WHERE organization_id = $1 AND vault_id = $2 AND name = $3`,
		org, id, name); err != nil {
		return vault.Entry{}, pgerr.MapWith(err, "find entry", pgerr.Messages{NotFound: "no entry named " + name})
	}
	return row.entry(), nil
}

// summaryRow is an entry without its body, with the start of it.
type summaryRow struct {
	ID           identity.EntryID         `db:"id"`
	Name         string                   `db:"name"`
	Head         string                   `db:"head"`
	Version      int                      `db:"version"`
	Kind         string                   `db:"written_by_kind"`
	By           string                   `db:"written_by"`
	Conversation *identity.ConversationID `db:"conversation_id"`
	Dock         *identity.DockID         `db:"dock_id"`
	UpdatedAt    time.Time                `db:"updated_at"`
}

func (r *Repository) Entries(ctx context.Context, org identity.OrganizationID, id identity.VaultID, page query.Pagination) (query.Paginated[vault.Summary], error) {
	out := query.Paginated[vault.Summary]{Items: []vault.Summary{}, Page: query.Page{Limit: page.Limit, Offset: page.Offset}}
	if err := r.db.GetContext(ctx, &out.Page.Total, `SELECT count(*) FROM vault_entries WHERE organization_id = $1 AND vault_id = $2`, org, id); err != nil {
		return out, pgerr.Map(err, "count entries")
	}
	var rows []summaryRow
	if err := r.db.SelectContext(ctx, &rows, `SELECT id, name, left(btrim(body), 400) AS head, version, written_by_kind, written_by, conversation_id, dock_id, updated_at
		FROM vault_entries WHERE organization_id = $1 AND vault_id = $2 ORDER BY updated_at DESC, name LIMIT $3 OFFSET $4`,
		org, id, page.Limit, page.Offset); err != nil {
		return out, pgerr.Map(err, "list entries")
	}
	for _, s := range rows {
		line := vault.FirstLine(s.Head)
		out.Items = append(out.Items, vault.Summary{ID: s.ID, Name: s.Name, FirstLine: line, Version: s.Version,
			Provenance: provenance(s.Kind, s.By, s.Conversation, s.Dock), UpdatedAt: s.UpdatedAt})
	}
	return out, nil
}

type versionRow struct {
	Entry        identity.EntryID         `db:"entry_id"`
	Version      int                      `db:"version"`
	Name         string                   `db:"name"`
	Body         string                   `db:"body"`
	Kind         string                   `db:"written_by_kind"`
	By           string                   `db:"written_by"`
	Conversation *identity.ConversationID `db:"conversation_id"`
	Dock         *identity.DockID         `db:"dock_id"`
	CreatedAt    time.Time                `db:"created_at"`
}

const versionColumns = `entry_id, version, name, body, written_by_kind, written_by, conversation_id, dock_id, created_at`

func (v versionRow) version() vault.Version {
	return vault.Version{Entry: v.Entry, Version: v.Version, Name: v.Name, Body: v.Body,
		Provenance: provenance(v.Kind, v.By, v.Conversation, v.Dock), CreatedAt: v.CreatedAt}
}

func (r *Repository) Versions(ctx context.Context, org identity.OrganizationID, id identity.VaultID, entry identity.EntryID, limit int) ([]vault.Version, error) {
	var rows []versionRow
	if err := r.db.SelectContext(ctx, &rows, `SELECT `+versionColumns+` FROM vault_entry_versions
		WHERE organization_id = $1 AND vault_id = $2 AND entry_id = $3 ORDER BY version DESC LIMIT $4`, org, id, entry, limit); err != nil {
		return nil, pgerr.Map(err, "list versions")
	}
	if len(rows) == 0 {
		return nil, errx.NotFound("entry not found")
	}
	out := make([]vault.Version, len(rows))
	for i, v := range rows {
		out[i] = v.version()
	}
	return out, nil
}

func (r *Repository) Version(ctx context.Context, org identity.OrganizationID, id identity.VaultID, entry identity.EntryID, version int) (vault.Version, error) {
	var row versionRow
	if err := r.db.GetContext(ctx, &row, `SELECT `+versionColumns+` FROM vault_entry_versions
		WHERE organization_id = $1 AND vault_id = $2 AND entry_id = $3 AND version = $4`, org, id, entry, version); err != nil {
		return vault.Version{}, pgerr.MapWith(err, "find version", pgerr.Messages{NotFound: "version not found"})
	}
	return row.version(), nil
}

type hitRow struct {
	Vault        identity.VaultID         `db:"vault_id"`
	VaultName    string                   `db:"vault_name"`
	Entry        identity.EntryID         `db:"id"`
	Name         string                   `db:"name"`
	Snippet      string                   `db:"snippet"`
	Version      int                      `db:"version"`
	Kind         string                   `db:"written_by_kind"`
	By           string                   `db:"written_by"`
	Conversation *identity.ConversationID `db:"conversation_id"`
	Dock         *identity.DockID         `db:"dock_id"`
	UpdatedAt    time.Time                `db:"updated_at"`
	Rank         float64                  `db:"rank"`
}

func (r *Repository) Search(ctx context.Context, org identity.OrganizationID, vaults []identity.VaultID, q string, limit int) ([]vault.Hit, error) {
	var words int
	if err := r.db.GetContext(ctx, &words, `SELECT numnode(websearch_to_tsquery('simple', $1))`, q); err != nil {
		return nil, pgerr.Map(err, "parse search")
	}
	const cols = `e.vault_id, v.name AS vault_name, e.id, e.name, e.version, e.written_by_kind, e.written_by, e.conversation_id, e.dock_id, e.updated_at`
	var rows []hitRow
	var err error
	if words > 0 {
		err = r.db.SelectContext(ctx, &rows, `SELECT `+cols+`,
			ts_headline('simple', e.body, websearch_to_tsquery('simple', $3), 'StartSel=**, StopSel=**, MaxFragments=2, MaxWords=30, MinWords=8, FragmentDelimiter=" … "') AS snippet,
			ts_rank(e.search, websearch_to_tsquery('simple', $3)) AS rank
			FROM vault_entries e JOIN vaults v ON v.organization_id = e.organization_id AND v.id = e.vault_id
			WHERE e.organization_id = $1 AND e.vault_id = ANY($2::uuid[]) AND e.search @@ websearch_to_tsquery('simple', $3)
			ORDER BY rank DESC, e.updated_at DESC LIMIT $4`, org, uuids(vaults), q, limit)
	} else {
		// Nothing to tokenize (punctuation, an operator): match the text.
		like := "%" + strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(q) + "%"
		err = r.db.SelectContext(ctx, &rows, `SELECT `+cols+`,
			substr(e.body, greatest(position(lower($3) in lower(e.body)) - 60, 1), 200) AS snippet, 0::float8 AS rank
			FROM vault_entries e JOIN vaults v ON v.organization_id = e.organization_id AND v.id = e.vault_id
			WHERE e.organization_id = $1 AND e.vault_id = ANY($2::uuid[]) AND (e.body ILIKE $4 OR e.name ILIKE $4)
			ORDER BY e.updated_at DESC LIMIT $5`, org, uuids(vaults), q, like, limit)
	}
	if err != nil {
		return nil, pgerr.Map(err, "search entries")
	}
	out := make([]vault.Hit, len(rows))
	for i, h := range rows {
		out[i] = vault.Hit{Vault: h.Vault, VaultName: h.VaultName, Entry: h.Entry, Name: h.Name, Snippet: h.Snippet, Version: h.Version,
			Provenance: provenance(h.Kind, h.By, h.Conversation, h.Dock), UpdatedAt: h.UpdatedAt, Rank: h.Rank}
	}
	return out, nil
}

func uuids(ids []identity.VaultID) []string {
	out := make([]string, len(ids))
	for i, id := range ids {
		out[i] = id.String()
	}
	return out
}
