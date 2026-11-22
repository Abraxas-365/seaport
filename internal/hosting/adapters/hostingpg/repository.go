// Package hostingpg is the Postgres repository of hosted agents (their
// slots), image builds, homes, sign-ins and network records. What a hosted
// agent runs is its current config's harness and machine (agent_profiles).
package hostingpg

import (
	"context"
	"strconv"
	"time"

	"github.com/Abraxas-365/seaport/internal/errx"
	"github.com/Abraxas-365/seaport/internal/hosting"
	"github.com/Abraxas-365/seaport/internal/identity"
	"github.com/Abraxas-365/seaport/internal/pgerr"
	"github.com/jmoiron/sqlx"
)

// hostingLock serializes one organization's slot changes while they are
// counted, and home naming against the sweep (second key:
// hashtext(organization)).
const hostingLock = 7_401_009

type Repository struct {
	db *sqlx.DB
}

func New(db *sqlx.DB) *Repository { return &Repository{db: db} }

var _ hosting.Repository = (*Repository)(nil)

func lock(ctx context.Context, tx *sqlx.Tx, org identity.OrganizationID) error {
	_, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock($1::int, hashtext($2::text))`, hostingLock, org)
	return pgerr.Map(err, "lock hosting")
}

// liveTokens counts the agent's live people's tokens (slot 0: not a
// slot's own token).
const liveTokens = `SELECT count(*) FROM crew_tokens
	WHERE organization_id = $1 AND agent_id = $2 AND slot = 0 AND revoked_at IS NULL
	  AND (expires_at IS NULL OR expires_at > now())`

func (r *Repository) Host(ctx context.Context, org identity.OrganizationID, agentID identity.AgentID, in hosting.Host, maxSlots int) (hosting.Hosted, error) {
	slots := in.SlotsOrDefault()
	tx, err := r.db.BeginTxx(ctx, nil)
	if err != nil {
		return hosting.Hosted{}, pgerr.Map(err, "begin host agent")
	}
	defer func() { _ = tx.Rollback() }()
	if err := lock(ctx, tx, org); err != nil {
		return hosting.Hosted{}, err
	}
	// The agent's row, locked: a person's token minted for it (crewpg
	// locks it too) and this never cross.
	var cfg struct {
		Harness   string `db:"harness"`
		Machine   bool   `db:"machine"`
		NoBrowser bool   `db:"no_browser"`
	}
	err = tx.GetContext(ctx, &cfg, `SELECT COALESCE(p.harness, '') AS harness, (p.machine IS NOT NULL) AS machine,
		  COALESCE(p.browser AND COALESCE(p.machine->'browser'->>'engine', '') = '', false) AS no_browser
		FROM agents a LEFT JOIN agent_profiles p
		  ON p.organization_id = a.organization_id AND p.agent_id = a.id AND p.version = a.profile_version
		WHERE a.organization_id = $1 AND a.id = $2 AND a.deleted_at IS NULL FOR UPDATE OF a`, org, agentID)
	if pgerr.IsNoRows(err) {
		return hosting.Hosted{}, errx.NotFound("agent not found")
	}
	if err != nil {
		return hosting.Hosted{}, pgerr.Map(err, "find agent")
	}
	if cfg.Harness == "" || !cfg.Machine {
		return hosting.Hosted{}, errx.Business("the agent's config has no harness and machine: set where it runs (seaport agents config apply, or the web's Dónde corre) before hosting it")
	}
	if cfg.NoBrowser {
		return hosting.Hosted{}, errx.Business("the agent uses the browser and its machine has none: set the machine's browser engine (chromium) or turn the agent's browser off")
	}
	var tokens int
	if err := tx.GetContext(ctx, &tokens, liveTokens, org, agentID); err != nil {
		return hosting.Hosted{}, pgerr.Map(err, "count the agent's tokens")
	}
	if tokens > 0 {
		return hosting.Hosted{}, errx.Conflict("the agent has " + strconv.Itoa(tokens) +
			" live token(s) for people's machines; a hosted agent works only through its slots: revoke them first")
	}
	if maxSlots > 0 {
		var others int
		if err := tx.GetContext(ctx, &others, `SELECT count(*) FROM agent_slots
			WHERE organization_id = $1 AND agent_id <> $2`, org, agentID); err != nil {
			return hosting.Hosted{}, pgerr.Map(err, "count slots")
		}
		if others+slots > maxSlots {
			return hosting.Hosted{}, errx.QuotaExceeded("slots", maxSlots,
				"an organization can run at most "+strconv.Itoa(maxSlots)+" slots across its hosted agents")
		}
	}
	var busy int
	if err := tx.GetContext(ctx, &busy, `SELECT count(*) FROM agent_slots
		WHERE organization_id = $1 AND agent_id = $2 AND idx > $3 AND state <> 'stopped'`, org, agentID, slots); err != nil {
		return hosting.Hosted{}, pgerr.Map(err, "check dropped slots")
	}
	if busy > 0 {
		return hosting.Hosted{}, errx.Conflict("stop the slots above " + strconv.Itoa(slots) + " first")
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO agent_hosting (organization_id, agent_id, updated_by)
		VALUES ($1, $2, $3)
		ON CONFLICT (organization_id, agent_id) DO UPDATE
		SET updated_by = EXCLUDED.updated_by, updated_at = now()`,
		org, agentID, in.By); err != nil {
		return hosting.Hosted{}, pgerr.Map(err, "host agent")
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM agent_slots WHERE organization_id = $1 AND agent_id = $2 AND idx > $3`,
		org, agentID, slots); err != nil {
		return hosting.Hosted{}, pgerr.Map(err, "drop slots")
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO agent_slots (organization_id, agent_id, idx)
		SELECT $1, $2, g FROM generate_series(1, $3::int) g
		ON CONFLICT (organization_id, agent_id, idx) DO NOTHING`, org, agentID, slots); err != nil {
		return hosting.Hosted{}, pgerr.Map(err, "add slots")
	}
	h, err := hosted(ctx, tx, org, agentID)
	if err != nil {
		return h, err
	}
	return h, pgerr.Map(tx.Commit(), "commit host agent")
}

func (r *Repository) Unhost(ctx context.Context, org identity.OrganizationID, agentID identity.AgentID) error {
	tx, err := r.db.BeginTxx(ctx, nil)
	if err != nil {
		return pgerr.Map(err, "begin unhost agent")
	}
	defer func() { _ = tx.Rollback() }()
	if err := lock(ctx, tx, org); err != nil {
		return err
	}
	var busy int
	if err := tx.GetContext(ctx, &busy, `SELECT count(*) FROM agent_slots
		WHERE organization_id = $1 AND agent_id = $2 AND state <> 'stopped'`, org, agentID); err != nil {
		return pgerr.Map(err, "check slots")
	}
	if busy > 0 {
		return errx.Conflict("stop the agent's slots first")
	}
	res, err := tx.ExecContext(ctx, `DELETE FROM agent_hosting WHERE organization_id = $1 AND agent_id = $2`, org, agentID)
	if err != nil {
		return pgerr.Map(err, "unhost agent")
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return errx.NotFound("the agent is not hosted")
	}
	return pgerr.Map(tx.Commit(), "commit unhost agent")
}

type hostedRow struct {
	Agent     identity.AgentID `db:"agent_id"`
	UpdatedBy string           `db:"updated_by"`
	UpdatedAt time.Time        `db:"updated_at"`
}

// slotColumns read a slot (alias s) with outdated (slotOutdated).
const slotColumns = `s.agent_id, s.idx, s.state, s.detail, s.image, s.config_version, s.started_at, s.active_at, s.updated_at,
	` + slotOutdated + ` AS outdated`

// slotOutdated: the slot (s) runs another machine digest than its agent's
// current config (p), or that machine on another image than the build
// the agent's config needs on this port (b, recorded in its hosting h by
// the supervisor), once that build is ready (a new release's base).
const slotOutdated = `(s.state = 'running' AND (s.machine_digest IS DISTINCT FROM COALESCE(p.machine_digest, '')
	OR COALESCE(h.build_machine = s.machine_digest AND b.state = 'ready' AND s.image <> b.image, false)))`

// buildReady: the build the agent's current config (p) needs on this
// port (b, of its hosting h) is recorded and ready.
const buildReady = `(h.build_machine <> '' AND h.build_machine = COALESCE(p.machine_digest, '')
	AND COALESCE(b.state = 'ready', false))`

// slotFrom joins a slot (s) to its agent's current config (p), its
// hosting (h) and the build that hosting wants (b).
const slotFrom = ` FROM agent_slots s
	JOIN agents a ON a.organization_id = s.organization_id AND a.id = s.agent_id
	JOIN agent_hosting h ON h.organization_id = s.organization_id AND h.agent_id = s.agent_id
	LEFT JOIN agent_profiles p ON p.organization_id = a.organization_id AND p.agent_id = a.id AND p.version = a.profile_version
	LEFT JOIN hosting_builds b ON b.organization_id = h.organization_id AND b.digest = h.build_digest`

type slotRow struct {
	Agent identity.AgentID `db:"agent_id"`
	hosting.Slot
}

func hosted(ctx context.Context, q sqlx.QueryerContext, org identity.OrganizationID, agentID identity.AgentID) (hosting.Hosted, error) {
	all, err := hostedWhere(ctx, q, org, &agentID)
	if err != nil {
		return hosting.Hosted{}, err
	}
	if len(all) == 0 {
		return hosting.Hosted{}, errx.NotFound("the agent is not hosted")
	}
	return all[0], nil
}

// hostedWhere reads the organization's hosted agents (one when agentID is
// set), live agents only, with their slots.
func hostedWhere(ctx context.Context, q sqlx.QueryerContext, org identity.OrganizationID, agentID *identity.AgentID) ([]hosting.Hosted, error) {
	var rows []hostedRow
	if err := sqlx.SelectContext(ctx, q, &rows, `SELECT h.agent_id, h.updated_by, h.updated_at
		FROM agent_hosting h JOIN agents a ON a.organization_id = h.organization_id AND a.id = h.agent_id
		WHERE h.organization_id = $1 AND ($2::uuid IS NULL OR h.agent_id = $2) AND a.deleted_at IS NULL
		ORDER BY a.name`, org, agentID); err != nil {
		return nil, pgerr.Map(err, "list hosted agents")
	}
	out := make([]hosting.Hosted, 0, len(rows))
	if len(rows) == 0 {
		return out, nil
	}
	var slots []slotRow
	if err := sqlx.SelectContext(ctx, q, &slots, `SELECT `+slotColumns+slotFrom+`
		WHERE s.organization_id = $1 AND ($2::uuid IS NULL OR s.agent_id = $2) ORDER BY s.agent_id, s.idx`, org, agentID); err != nil {
		return nil, pgerr.Map(err, "list slots")
	}
	byAgent := map[identity.AgentID][]hosting.Slot{}
	for _, s := range slots {
		byAgent[s.Agent] = append(byAgent[s.Agent], s.Slot)
	}
	// Conversations (threads) in work and waiting, per agent: a thread with
	// a ship claimed is being worked; one with only queued ships waits.
	var work []struct {
		Agent   identity.AgentID `db:"agent_id"`
		Working int              `db:"working"`
		Waiting int              `db:"waiting"`
	}
	if err := sqlx.SelectContext(ctx, q, &work, `WITH t AS (
			SELECT s.agent_id, bool_or(s.status = 'claimed') AS working
			FROM ships s JOIN conversations c ON c.organization_id = s.organization_id AND c.id = s.conversation_id
			WHERE s.organization_id = $1 AND ($2::uuid IS NULL OR s.agent_id = $2) AND s.agent_id IS NOT NULL
			  AND s.status IN ('queued', 'claimed')
			GROUP BY s.agent_id, c.thread_id)
		SELECT agent_id, count(*) FILTER (WHERE working) AS working, count(*) FILTER (WHERE NOT working) AS waiting
		FROM t GROUP BY agent_id`, org, agentID); err != nil {
		return nil, pgerr.Map(err, "count hosted agents' conversations")
	}
	counts := map[identity.AgentID][2]int{}
	for _, w := range work {
		counts[w.Agent] = [2]int{w.Working, w.Waiting}
	}
	for _, row := range rows {
		h := hosting.Hosted{Agent: row.Agent, UpdatedBy: row.UpdatedBy, UpdatedAt: row.UpdatedAt, Slots: byAgent[row.Agent],
			Working: counts[row.Agent][0], Waiting: counts[row.Agent][1]}
		if h.Slots == nil {
			h.Slots = []hosting.Slot{}
		}
		out = append(out, h)
	}
	return out, nil
}

func (r *Repository) Hosted(ctx context.Context, org identity.OrganizationID, agentID identity.AgentID) (hosting.Hosted, error) {
	return hosted(ctx, r.db, org, agentID)
}

func (r *Repository) HostedAgents(ctx context.Context, org identity.OrganizationID) ([]hosting.Hosted, error) {
	return hostedWhere(ctx, r.db, org, nil)
}
