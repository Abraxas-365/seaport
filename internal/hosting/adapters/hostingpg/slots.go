package hostingpg

import (
	"context"
	"time"

	"github.com/Abraxas-365/seaport/internal/errx"
	"github.com/Abraxas-365/seaport/internal/hosting"
	"github.com/Abraxas-365/seaport/internal/identity"
	"github.com/Abraxas-365/seaport/internal/pgerr"
)

// Slot runs: every transition is one conditional UPDATE, so processes
// sharing the database (and the Docker engine) never both start or stop
// one slot.

func (r *Repository) BeginStart(ctx context.Context, org identity.OrganizationID, agentID identity.AgentID, slot int, mode hosting.StartMode) (hosting.SlotStart, bool, error) {
	tx, err := r.db.BeginTxx(ctx, nil)
	if err != nil {
		return hosting.SlotStart{}, false, pgerr.Map(err, "begin start slot")
	}
	defer func() { _ = tx.Rollback() }()
	run := identity.NewCrewTokenID().String() // any random id
	var row struct {
		OldToken  *identity.CrewTokenID `db:"old_token"`
		AgentName string                `db:"name"`
		Version   int                   `db:"version"`
		Harness   string                `db:"harness"`
		Machine   []byte                `db:"machine"`
		Digest    string                `db:"machine_digest"`
		Previous  string                `db:"previous"`
	}
	// cur locks the row first: a concurrent start waits, then sees it
	// starting and claims nothing. What it runs is the agent's current
	// config (a hosted agent's config always has a machine: Host refuses
	// one without, and saving a config can't drop it while hosted).
	// previous is the image a slot of the agent last ran on this machine
	// (a start whose build isn't ready runs on it: SlotStart.Previous).
	// $6: the other slots' states that keep this one from starting (a wake
	// needs none up, autoscale none starting; a person's start, none).
	var others []string
	switch mode {
	case hosting.StartWake:
		others = []string{hosting.SlotStarting, hosting.SlotRunning}
	case hosting.StartScale:
		others = []string{hosting.SlotStarting}
	default:
		others = []string{}
	}
	err = tx.GetContext(ctx, &row, `WITH cur AS (
			SELECT token_id FROM agent_slots WHERE organization_id = $1 AND agent_id = $2 AND idx = $3 FOR UPDATE)
		UPDATE agent_slots s SET state = 'starting', detail = 'starting', run = $4, token_id = NULL, viewer_at = NULL, updated_at = now()
		FROM cur, agent_hosting h, agents a, agent_profiles p
		WHERE s.organization_id = $1 AND s.agent_id = $2 AND s.idx = $3
		  AND h.organization_id = s.organization_id AND h.agent_id = s.agent_id
		  AND a.organization_id = s.organization_id AND a.id = s.agent_id AND a.deleted_at IS NULL
		  AND p.organization_id = a.organization_id AND p.agent_id = a.id AND p.version = a.profile_version
		  AND (s.state IN ('stopped', 'failed') OR (s.state = 'starting' AND s.updated_at < now() - make_interval(secs => $5::float8)))
		  AND NOT EXISTS (SELECT 1 FROM agent_slots o WHERE o.organization_id = s.organization_id
		        AND o.agent_id = s.agent_id AND o.idx <> s.idx AND o.state = ANY($6::text[]))
		RETURNING cur.token_id AS old_token, a.name, p.version, p.harness, p.machine, p.machine_digest,
		  COALESCE((SELECT o.image FROM agent_slots o WHERE o.organization_id = s.organization_id AND o.agent_id = s.agent_id
		    AND o.image <> '' AND o.machine_digest <> '' AND o.machine_digest = p.machine_digest
		    ORDER BY o.started_at DESC NULLS LAST LIMIT 1), '') AS previous`,
		org, agentID, slot, run, hosting.StartStale.Seconds(), others)
	if pgerr.IsNoRows(err) {
		h, herr := hosted(ctx, tx, org, agentID)
		if herr != nil {
			return hosting.SlotStart{}, false, herr
		}
		for _, s := range h.Slots {
			if s.Index == slot {
				return hosting.SlotStart{}, false, nil
			}
		}
		return hosting.SlotStart{}, false, errx.NotFound("slot not found")
	}
	if err != nil {
		return hosting.SlotStart{}, false, pgerr.Map(err, "start slot")
	}
	spec, err := hosting.SpecOf(row.Harness, row.Machine)
	if err != nil {
		return hosting.SlotStart{}, false, err
	}
	out := hosting.SlotStart{Run: run, AgentName: row.AgentName, Spec: hosting.Normalize(spec),
		ConfigVersion: row.Version, Digest: row.Digest, Previous: row.Previous}
	if row.OldToken != nil {
		out.OldToken = *row.OldToken
	}
	return out, true, pgerr.Map(tx.Commit(), "commit start slot")
}

func (r *Repository) StartProgress(ctx context.Context, org identity.OrganizationID, agentID identity.AgentID, slot int, run, detail string, token identity.CrewTokenID) error {
	var tok any
	if !token.IsZero() {
		tok = token
	}
	res, err := r.db.ExecContext(ctx, `UPDATE agent_slots SET detail = $5, token_id = COALESCE($6, token_id), updated_at = now()
		WHERE organization_id = $1 AND agent_id = $2 AND idx = $3 AND state = 'starting' AND run = $4`,
		org, agentID, slot, run, detail, tok)
	if err != nil {
		return pgerr.Map(err, "slot start progress")
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return errx.Conflict("the slot is no longer starting")
	}
	return nil
}

func (r *Repository) StartDone(ctx context.Context, org identity.OrganizationID, agentID identity.AgentID, slot int, run string, started hosting.SlotRun) (hosting.Hosted, error) {
	res, err := r.db.ExecContext(ctx, `UPDATE agent_slots SET state = 'running', detail = '', image = $5,
		machine_digest = $6, config_version = $7, started_at = now(), active_at = now(), updated_at = now()
		WHERE organization_id = $1 AND agent_id = $2 AND idx = $3 AND state = 'starting' AND run = $4`,
		org, agentID, slot, run, started.Image, started.Digest, started.ConfigVersion)
	if err != nil {
		return hosting.Hosted{}, pgerr.Map(err, "slot started")
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return hosting.Hosted{}, errx.Conflict("the slot is no longer starting")
	}
	return hosted(ctx, r.db, org, agentID)
}

func (r *Repository) BeginStop(ctx context.Context, org identity.OrganizationID, agentID identity.AgentID, slot int, run string) (hosting.Hosted, bool, error) {
	res, err := r.db.ExecContext(ctx, `UPDATE agent_slots SET state = 'stopping', detail = 'stopping', run = '', updated_at = now()
		WHERE organization_id = $1 AND agent_id = $2 AND idx = $3 AND state <> 'stopped'
		  AND ($4::text = '' OR (state = 'running' AND run = $4))`, org, agentID, slot, run)
	if err != nil {
		return hosting.Hosted{}, false, pgerr.Map(err, "stop slot")
	}
	h, err := hosted(ctx, r.db, org, agentID)
	if err != nil {
		return h, false, err
	}
	n, _ := res.RowsAffected()
	if n == 0 && !hasSlot(h, slot) {
		return h, false, errx.NotFound("slot not found")
	}
	return h, n > 0, nil
}

func hasSlot(h hosting.Hosted, slot int) bool {
	for _, s := range h.Slots {
		if s.Index == slot {
			return true
		}
	}
	return false
}

func (r *Repository) EndRun(ctx context.Context, org identity.OrganizationID, agentID identity.AgentID, slot int, end hosting.EndRun) (hosting.Hosted, identity.CrewTokenID, bool, error) {
	var token *identity.CrewTokenID
	err := r.db.GetContext(ctx, &token, `WITH cur AS (
			SELECT token_id FROM agent_slots WHERE organization_id = $1 AND agent_id = $2 AND idx = $3 FOR UPDATE)
		UPDATE agent_slots s SET state = $4, detail = $5, run = '', token_id = NULL, updated_at = now()
		FROM cur
		WHERE s.organization_id = $1 AND s.agent_id = $2 AND s.idx = $3 AND s.state = ANY($6::text[])
		  AND ($7::text = '' OR s.run = $7) AND ($8::float8 = 0 OR s.updated_at < now() - make_interval(secs => $8))
		RETURNING cur.token_id`, org, agentID, slot, end.State, end.Detail, end.From, end.Run, end.StaleFor.Seconds())
	if pgerr.IsNoRows(err) {
		return hosting.Hosted{}, identity.CrewTokenID{}, false, nil
	}
	if err != nil {
		return hosting.Hosted{}, identity.CrewTokenID{}, false, pgerr.Map(err, "end slot run")
	}
	var tok identity.CrewTokenID
	if token != nil {
		tok = *token
	}
	h, err := hosted(ctx, r.db, org, agentID)
	if errx.IsType(err, errx.TypeNotFound) { // unhosted meanwhile
		return hosting.Hosted{}, tok, true, nil
	}
	return h, tok, true, err
}

func (r *Repository) Supervise(ctx context.Context) ([]hosting.Supervised, error) {
	tx, err := r.db.BeginTxx(ctx, nil)
	if err != nil {
		return nil, pgerr.Map(err, "begin supervise")
	}
	defer func() { _ = tx.Rollback() }()
	out := []hosting.Supervised{}
	// threads: the agents' conversations with work (a ship claimed: in
	// work; only queued: waiting, since its oldest queued ship). mine: the
	// ships each running slot's own connections claimed (crew_connections.
	// credential is the token id). Signing: a sign-in under way on the
	// slot's run (it takes no work until signed in); viewing: a person in
	// its browser viewer. Unattended: its machine doesn't sign in to a
	// subscription, a sign-in on the slot finished (the tokens stay in its
	// state volume), or the agent has its harness's fallback env secret.
	if err := tx.SelectContext(ctx, &out, `WITH threads AS (
			SELECT sh.organization_id, sh.agent_id,
			  count(*) FILTER (WHERE sh.status = 'queued') AS queued,
			  count(*) FILTER (WHERE sh.status = 'claimed') AS claimed,
			  min(sh.created_at) FILTER (WHERE sh.status = 'queued') AS since
			FROM ships sh JOIN conversations c ON c.organization_id = sh.organization_id AND c.id = sh.conversation_id
			WHERE sh.status IN ('queued', 'claimed') AND sh.agent_id IS NOT NULL
			GROUP BY sh.organization_id, sh.agent_id, c.thread_id),
		work AS (
			SELECT organization_id, agent_id, sum(queued) AS queued, sum(claimed) AS claimed,
			  count(*) FILTER (WHERE claimed > 0) AS working, count(*) FILTER (WHERE claimed = 0) AS waiting,
			  min(since) FILTER (WHERE claimed = 0) AS waiting_since
			FROM threads GROUP BY organization_id, agent_id),
		mine AS (
			SELECT sl.organization_id, sl.agent_id, sl.idx, count(*) AS n
			FROM agent_slots sl
			JOIN crew_connections cc ON cc.organization_id = sl.organization_id AND cc.credential = sl.token_id::text
			JOIN ships sh ON sh.organization_id = sl.organization_id AND sh.claimed_by = cc.session AND sh.status = 'claimed'
			WHERE sl.state = 'running' AND sl.token_id IS NOT NULL
			GROUP BY sl.organization_id, sl.agent_id, sl.idx)
		SELECT s.organization_id, s.agent_id, s.idx, s.state, s.run, s.updated_at, s.active_at, s.started_at,
		  COALESCE(p.machine->>'idle_stop', '') AS idle_stop,
		  `+slotOutdated+` AS outdated, `+buildReady+` AS build_ready,
		  COALESCE(w.queued, 0) AS queued, COALESCE(w.claimed, 0) AS claimed,
		  COALESCE(w.working, 0) AS working, COALESCE(w.waiting, 0) AS waiting, w.waiting_since,
		  COALESCE(m.n, 0) AS slot_claimed,
		  COALESCE((p.machine->>'concurrency')::int, 0) AS concurrency,
		  h.autoscale,
		  (COALESCE(p.machine->'models'->>'anthropic', '') <> $3
		    OR EXISTS (SELECT 1 FROM slot_logins l WHERE l.organization_id = s.organization_id
		      AND l.agent_id = s.agent_id AND l.idx = s.idx AND l.state = $4)
		    OR EXISTS (SELECT 1 FROM agent_secrets x WHERE x.organization_id = s.organization_id
		      AND x.agent_id = s.agent_id AND x.env
		      AND x.name = CASE p.harness WHEN $5 THEN $6 WHEN $7 THEN $8 END)) AS unattended,
		  (s.state = 'running' AND EXISTS (SELECT 1 FROM slot_logins l WHERE l.organization_id = s.organization_id
		    AND l.agent_id = s.agent_id AND l.idx = s.idx AND l.run = s.run
		    AND l.state IN ('starting', 'waiting', 'verifying')
		    AND l.updated_at > now() - make_interval(secs => $1::float8))) AS signing,
		  (s.state = 'running' AND s.viewer_at IS NOT NULL
		    AND s.viewer_at > now() - make_interval(secs => $2::float8)) AS viewing
		FROM agent_slots s
		JOIN agent_hosting h ON h.organization_id = s.organization_id AND h.agent_id = s.agent_id
		JOIN agents a ON a.organization_id = s.organization_id AND a.id = s.agent_id AND a.deleted_at IS NULL
		LEFT JOIN agent_profiles p ON p.organization_id = a.organization_id AND p.agent_id = a.id AND p.version = a.profile_version
		LEFT JOIN hosting_builds b ON b.organization_id = h.organization_id AND b.digest = h.build_digest
		LEFT JOIN work w ON w.organization_id = s.organization_id AND w.agent_id = s.agent_id
		LEFT JOIN mine m ON m.organization_id = s.organization_id AND m.agent_id = s.agent_id AND m.idx = s.idx
		ORDER BY s.organization_id, s.agent_id, s.idx`, hosting.LoginStale.Seconds(), hosting.ViewerStale.Seconds(),
		hosting.Subscription, hosting.LoginDone,
		hosting.HarnessClaudeCode, hosting.SignInFallbacks[hosting.HarnessClaudeCode],
		hosting.HarnessRness, hosting.SignInFallbacks[hosting.HarnessRness]); err != nil {
		return nil, pgerr.Map(err, "list slots to supervise")
	}
	// A busy slot stays active (idle_stop and ScaleDownAfter count from
	// its last busy round): Supervised.Idle.
	var orgs, agents []string
	var idxs []int
	for _, row := range out {
		if row.State == hosting.SlotRunning && !row.Idle() {
			orgs = append(orgs, row.Organization.String())
			agents = append(agents, row.Agent.String())
			idxs = append(idxs, row.Index)
		}
	}
	if len(idxs) > 0 {
		if _, err := tx.ExecContext(ctx, `UPDATE agent_slots s SET active_at = now()
			FROM unnest($1::uuid[], $2::uuid[], $3::int[]) AS b(org, agent, idx)
			WHERE s.organization_id = b.org AND s.agent_id = b.agent AND s.idx = b.idx AND s.state = 'running'`,
			orgs, agents, idxs); err != nil {
			return nil, pgerr.Map(err, "touch active slots")
		}
		now := time.Now()
		for i := range out {
			if out[i].State == hosting.SlotRunning && !out[i].Idle() {
				out[i].ActiveAt = &now
			}
		}
	}
	return out, pgerr.Map(tx.Commit(), "commit supervise")
}

func (r *Repository) HostedMachines(ctx context.Context) ([]hosting.HostedMachine, error) {
	out := []hosting.HostedMachine{}
	err := r.db.SelectContext(ctx, &out, `SELECT h.organization_id, h.agent_id, p.harness, p.machine, p.machine_digest,
		  h.build_digest, h.build_machine
		FROM agent_hosting h
		JOIN agents a ON a.organization_id = h.organization_id AND a.id = h.agent_id AND a.deleted_at IS NULL
		JOIN agent_profiles p ON p.organization_id = a.organization_id AND p.agent_id = a.id AND p.version = a.profile_version
		WHERE p.machine IS NOT NULL AND p.machine <> 'null'::jsonb AND p.harness <> ''
		ORDER BY h.organization_id, h.agent_id`)
	return out, pgerr.Map(err, "list hosted machines")
}

func (r *Repository) WantBuild(ctx context.Context, org identity.OrganizationID, agentID identity.AgentID, digest, machine string) error {
	_, err := r.db.ExecContext(ctx, `UPDATE agent_hosting SET build_digest = $3, build_machine = $4
		WHERE organization_id = $1 AND agent_id = $2 AND (build_digest, build_machine) IS DISTINCT FROM ($3, $4)`,
		org, agentID, digest, machine)
	return pgerr.Map(err, "record the agent's build")
}

func (r *Repository) MarkViewer(ctx context.Context, org identity.OrganizationID, agentID identity.AgentID, slot int, open bool) error {
	_, err := r.db.ExecContext(ctx, `UPDATE agent_slots SET viewer_at = CASE WHEN $4::bool THEN now() END
		WHERE organization_id = $1 AND agent_id = $2 AND idx = $3 AND (state = 'running' OR NOT $4::bool)`,
		org, agentID, slot, open)
	return pgerr.Map(err, "mark slot viewer")
}

func (r *Repository) PortID(ctx context.Context) (string, error) {
	var id string
	err := r.db.GetContext(ctx, &id, `SELECT id::text FROM port_identity`)
	return id, pgerr.Map(err, "read port id")
}
