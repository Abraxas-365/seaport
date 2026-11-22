package hostingpg

import (
	"context"
	"time"

	"github.com/Abraxas-365/seaport/internal/hosting"
	"github.com/Abraxas-365/seaport/internal/identity"
	"github.com/Abraxas-365/seaport/internal/pgerr"
)

// alertConditions lists, for every hosted agent with alerts on, the
// conditions that keep an alert open (hosting.AlertKind + key) and whether
// each raises one ($1 BuildSlow and $2 the time, in seconds/now): a slot
// failed (kept while failed or starting again), the agent's wanted build
// failed (kept while failed or building again) or has been building
// BuildSlow (kept while building), a conversation waited the agent's
// alert wait (kept while one waits). The wanted build counts only while
// it was worked out for the current config's machine.
const alertConditions = `WITH threads AS (
		SELECT sh.organization_id, sh.agent_id,
		  count(*) FILTER (WHERE sh.status = 'claimed') AS claimed,
		  min(sh.created_at) FILTER (WHERE sh.status = 'queued') AS since
		FROM ships sh JOIN conversations c ON c.organization_id = sh.organization_id AND c.id = sh.conversation_id
		WHERE sh.status IN ('queued', 'claimed') AND sh.agent_id IS NOT NULL
		GROUP BY sh.organization_id, sh.agent_id, c.thread_id),
	waits AS (
		SELECT organization_id, agent_id, count(*) AS waiting, min(since) AS since
		FROM threads WHERE claimed = 0 GROUP BY organization_id, agent_id),
	alerting AS (
		SELECT h.organization_id, h.agent_id, h.alert_wait_seconds, h.build_digest, h.build_machine,
		  COALESCE(p.machine_digest, '') AS machine_digest
		FROM agent_hosting h
		JOIN agents a ON a.organization_id = h.organization_id AND a.id = h.agent_id AND a.deleted_at IS NULL
		LEFT JOIN agent_profiles p ON p.organization_id = a.organization_id AND p.agent_id = a.id AND p.version = a.profile_version
		WHERE h.alert_dock IS NOT NULL)
	SELECT al.organization_id, al.agent_id, 'slot_failed' AS kind, s.idx::text AS key,
	  s.state = 'failed' AS raise, s.detail, 0 AS waiting, s.updated_at AS since
	FROM alerting al JOIN agent_slots s ON s.organization_id = al.organization_id AND s.agent_id = al.agent_id
	WHERE s.state IN ('failed', 'starting')
	UNION ALL
	SELECT al.organization_id, al.agent_id, 'build_failed', b.digest,
	  b.state = 'failed', b.detail, 0, COALESCE(b.finished_at, b.updated_at)
	FROM alerting al JOIN hosting_builds b ON b.organization_id = al.organization_id AND b.digest = al.build_digest
	WHERE al.build_machine <> '' AND al.build_machine = al.machine_digest AND b.state IN ('failed', 'building')
	UNION ALL
	SELECT al.organization_id, al.agent_id, 'build_slow', b.digest,
	  b.started_at <= $2::timestamptz - make_interval(secs => $1::float8), '', 0, b.started_at
	FROM alerting al JOIN hosting_builds b ON b.organization_id = al.organization_id AND b.digest = al.build_digest
	WHERE al.build_machine <> '' AND al.build_machine = al.machine_digest AND b.state = 'building'
	UNION ALL
	SELECT al.organization_id, al.agent_id, 'waiting', '',
	  w.since <= $2::timestamptz - make_interval(secs => al.alert_wait_seconds::float8), '', w.waiting::int, w.since
	FROM alerting al JOIN waits w ON w.organization_id = al.organization_id AND w.agent_id = al.agent_id`

// Alerts raises, resolves, prunes and claims the hosted agents' alerts in
// one transaction (system: the supervisor). Two processes may run it at
// once: raising is ON CONFLICT DO NOTHING on the open alert's unique index,
// resolving is idempotent and claiming skips rows another one locked.
func (r *Repository) Alerts(ctx context.Context) ([]hosting.Alert, error) {
	tx, err := r.db.BeginTxx(ctx, nil)
	if err != nil {
		return nil, pgerr.Map(err, "begin alerts")
	}
	defer func() { _ = tx.Rollback() }()
	now := time.Now()
	slow := hosting.BuildSlow.Seconds()
	if _, err := tx.ExecContext(ctx, `INSERT INTO hosting_alerts (organization_id, agent_id, kind, key, detail, waiting, since)
		SELECT organization_id, agent_id, kind, key, left(detail, 1000), waiting, since FROM (`+alertConditions+`) c WHERE raise
		ON CONFLICT (organization_id, agent_id, kind, key) WHERE resolved_at IS NULL DO NOTHING`, slow, now); err != nil {
		return nil, pgerr.Map(err, "raise alerts")
	}
	// A cleared alert is resolved; one never told (its notice didn't go)
	// or whose agent's alerts are off is done at once: nobody hears of it.
	if _, err := tx.ExecContext(ctx, `UPDATE hosting_alerts a SET resolved_at = $2,
		  done_at = CASE WHEN a.sent_at IS NULL OR h.alert_dock IS NULL THEN $2 END, send_after = NULL
		FROM agent_hosting h
		WHERE h.organization_id = a.organization_id AND h.agent_id = a.agent_id AND a.resolved_at IS NULL
		  AND NOT EXISTS (SELECT 1 FROM (`+alertConditions+`) c WHERE c.organization_id = a.organization_id
		    AND c.agent_id = a.agent_id AND c.kind = a.kind AND c.key = a.key)`, slow, now); err != nil {
		return nil, pgerr.Map(err, "resolve alerts")
	}
	// Alerts turned off: resolved ones still to be told are dropped too.
	if _, err := tx.ExecContext(ctx, `UPDATE hosting_alerts a SET done_at = $1, send_after = NULL
		FROM agent_hosting h
		WHERE h.organization_id = a.organization_id AND h.agent_id = a.agent_id
		  AND h.alert_dock IS NULL AND a.done_at IS NULL AND a.resolved_at IS NOT NULL`, now); err != nil {
		return nil, pgerr.Map(err, "drop alerts")
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM hosting_alerts WHERE done_at < $1`, now.Add(-hosting.AlertKeep)); err != nil {
		return nil, pgerr.Map(err, "prune alerts")
	}
	out := []hosting.Alert{}
	if err := tx.SelectContext(ctx, &out, `WITH due AS (
			SELECT a.id FROM hosting_alerts a
			JOIN agent_hosting h ON h.organization_id = a.organization_id AND h.agent_id = a.agent_id
			WHERE h.alert_dock IS NOT NULL AND a.done_at IS NULL
			  AND (a.send_after IS NULL OR a.send_after <= $1)
			  AND ((a.resolved_at IS NULL AND a.sent_at IS NULL) OR (a.resolved_at IS NOT NULL AND a.sent_at IS NOT NULL))
			ORDER BY a.id
			FOR UPDATE OF a SKIP LOCKED),
		claimed AS (
			UPDATE hosting_alerts a SET send_after = $2 FROM due WHERE a.id = due.id
			RETURNING a.*)
		SELECT c.id, c.organization_id, c.agent_id, ag.name AS agent_name, c.kind, c.key, c.detail, c.waiting,
		  c.since, c.raised_at, c.resolved_at, h.alert_dock AS dock_id, h.alert_destination AS destination, c.conversation_id
		FROM claimed c
		JOIN agent_hosting h ON h.organization_id = c.organization_id AND h.agent_id = c.agent_id
		JOIN agents ag ON ag.organization_id = c.organization_id AND ag.id = c.agent_id
		ORDER BY c.id`, now, now.Add(hosting.AlertRetry)); err != nil {
		return nil, pgerr.Map(err, "claim alerts")
	}
	return out, pgerr.Map(tx.Commit(), "commit alerts")
}

// AlertSent records how a notice went. A notice that went clears the
// hosting's alert error; a raise notice that went after its alert was
// resolved (and dropped as never told) is due again, as resolved.
func (r *Repository) AlertSent(ctx context.Context, org identity.OrganizationID, in hosting.AlertSent) (bool, error) {
	tx, err := r.db.BeginTxx(ctx, nil)
	if err != nil {
		return false, pgerr.Map(err, "begin alert sent")
	}
	defer func() { _ = tx.Rollback() }()
	var agentID identity.AgentID
	switch {
	case in.Err != "":
		err = tx.GetContext(ctx, &agentID, `UPDATE hosting_alerts SET send_after = $3
			WHERE organization_id = $1 AND id = $2 RETURNING agent_id`, org, in.ID, time.Now().Add(hosting.AlertRetry))
	case in.Resolved:
		err = tx.GetContext(ctx, &agentID, `UPDATE hosting_alerts SET done_at = now(), send_after = NULL
			WHERE organization_id = $1 AND id = $2 RETURNING agent_id`, org, in.ID)
	default:
		err = tx.GetContext(ctx, &agentID, `UPDATE hosting_alerts a SET sent_at = now(), conversation_id = $3, send_after = NULL,
			  done_at = CASE WHEN a.resolved_at IS NOT NULL AND h.alert_dock IS NOT NULL THEN NULL ELSE a.done_at END
			FROM agent_hosting h
			WHERE h.organization_id = a.organization_id AND h.agent_id = a.agent_id AND a.organization_id = $1 AND a.id = $2
			RETURNING a.agent_id`, org, in.ID, in.Conversation)
	}
	if err != nil {
		return false, pgerr.MapWith(err, "record alert sent", pgerr.Messages{NotFound: "the alert is gone"})
	}
	res, err := tx.ExecContext(ctx, `UPDATE agent_hosting SET alert_error = $3
		WHERE organization_id = $1 AND agent_id = $2 AND alert_dock IS NOT NULL AND alert_error <> $3`, org, agentID, in.Err)
	if err != nil {
		return false, pgerr.Map(err, "record alert error")
	}
	n, _ := res.RowsAffected()
	return n > 0, pgerr.Map(tx.Commit(), "commit alert sent")
}
