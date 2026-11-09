package crewpg

import (
	"context"
	"encoding/json"
	"log/slog"
	"strconv"
	"time"

	"github.com/Abraxas-365/seaport/internal/crew"
	"github.com/Abraxas-365/seaport/internal/errx"
	"github.com/Abraxas-365/seaport/internal/identity"
	"github.com/Abraxas-365/seaport/internal/pgerr"
	"github.com/Abraxas-365/seaport/internal/pglisten"
	"github.com/jmoiron/sqlx"
)

// connectionLock serializes one organization's connection openings while
// its live connections are counted (second key: hashtext(organization)).
const connectionLock = 7_401_006

// signals is the NOTIFY channel of crew.Signal.
const signals = "seaport_crew"

type Presence struct{ db *sqlx.DB }

func NewPresence(db *sqlx.DB) *Presence { return &Presence{db: db} }

var _ crew.Presence = (*Presence)(nil)

func (p *Presence) Open(ctx context.Context, org identity.OrganizationID, c crew.Connection, limit int) error {
	tx, err := p.db.BeginTxx(ctx, nil)
	if err != nil {
		return pgerr.Map(err, "begin open crew connection")
	}
	defer func() { _ = tx.Rollback() }()
	if limit > 0 {
		if _, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock($1::int, hashtext($2::text))`, connectionLock, org); err != nil {
			return pgerr.Map(err, "lock crew connections")
		}
		var n int
		if err := tx.GetContext(ctx, &n, `SELECT count(*) FROM crew_connections WHERE organization_id = $1`, org); err != nil {
			return pgerr.Map(err, "count crew connections")
		}
		if n >= limit {
			return errx.QuotaExceeded("crew_connections", limit, "this organization already has "+
				strconv.Itoa(limit)+" agents connected; disconnect one first")
		}
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO crew_connections
		(session, organization_id, process, credential, remote, connected_at) VALUES ($1, $2, $3, $4, $5, $6)`,
		c.Session, org, c.Process, c.Credential, c.Remote, c.ConnectedAt); err != nil {
		return pgerr.Map(err, "open crew connection")
	}
	return pgerr.Map(tx.Commit(), "commit crew connection")
}

func (p *Presence) Ready(ctx context.Context, org identity.OrganizationID, session string, a crew.Agent) error {
	_, err := p.db.ExecContext(ctx, `UPDATE crew_connections SET ready = true, agent = $3, concurrency = $4, steer = $5,
		agent_id = $6 WHERE organization_id = $1 AND session = $2`, org, session, a.Name, a.Concurrency, a.Steer, a.AgentID)
	return pgerr.Map(err, "crew connection ready")
}

func (p *Presence) SetDeliveries(ctx context.Context, org identity.OrganizationID, session string, ds []crew.AgentDelivery) error {
	if ds == nil {
		ds = []crew.AgentDelivery{}
	}
	raw, err := json.Marshal(ds)
	if err != nil {
		return errx.Wrap(err, "encode deliveries", errx.TypeInternal)
	}
	_, err = p.db.ExecContext(ctx, `UPDATE crew_connections SET deliveries = $3
		WHERE organization_id = $1 AND session = $2`, org, session, string(raw))
	return pgerr.Map(err, "set crew connection deliveries")
}

func (p *Presence) Close(ctx context.Context, org identity.OrganizationID, session string) error {
	_, err := p.db.ExecContext(ctx, `DELETE FROM crew_connections WHERE organization_id = $1 AND session = $2`, org, session)
	return pgerr.Map(err, "close crew connection")
}

type connectionRow struct {
	Organization identity.OrganizationID `db:"organization_id"`
	Session      string                  `db:"session"`
	Agent        string                  `db:"agent"`
	AgentID      identity.AgentID        `db:"agent_id"`
	AgentName    string                  `db:"agent_name"`
	Concurrency  int                     `db:"concurrency"`
	Steer        bool                    `db:"steer"`
	Remote       string                  `db:"remote"`
	Deliveries   string                  `db:"deliveries"`
	ConnectedAt  time.Time               `db:"connected_at"`
}

func (r connectionRow) deliveries() []crew.AgentDelivery {
	ds := []crew.AgentDelivery{}
	if err := json.Unmarshal([]byte(r.Deliveries), &ds); err != nil {
		slog.Warn("bad crew connection deliveries", "session", r.Session, "err", err)
	}
	return ds
}

func (p *Presence) Status(ctx context.Context, org identity.OrganizationID) (crew.Status, error) {
	var rows []connectionRow
	if err := p.db.SelectContext(ctx, &rows, `SELECT c.organization_id, c.session, c.agent, c.agent_id, a.name AS agent_name,
		c.concurrency, c.steer, c.remote, c.deliveries, c.connected_at
		FROM crew_connections c JOIN agents a ON a.organization_id = c.organization_id AND a.id = c.agent_id
		WHERE c.organization_id = $1 AND c.ready ORDER BY a.name, c.connected_at, c.session`, org); err != nil {
		return crew.Status{}, pgerr.Map(err, "crew status")
	}
	out := crew.Status{Agents: make([]crew.Agent, 0, len(rows))}
	for _, r := range rows {
		out.Agents = append(out.Agents, crew.Agent{Session: r.Session, AgentID: r.AgentID, AgentName: r.AgentName,
			Name: r.Agent, Concurrency: r.Concurrency,
			Steer: r.Steer, Remote: r.Remote, ConnectedAt: r.ConnectedAt.UTC(), Deliveries: r.deliveries()})
	}
	return out, nil
}

func (p *Presence) Touch(ctx context.Context, process string, sessions []string) ([]string, error) {
	if len(sessions) == 0 {
		return nil, nil
	}
	var alive []string
	if err := p.db.SelectContext(ctx, &alive, `UPDATE crew_connections SET seen_at = now()
		WHERE process = $1 AND session = ANY($2) RETURNING session`, process, sessions); err != nil {
		return nil, pgerr.Map(err, "touch crew connections")
	}
	// Also closed: connections of a revoked or expired token, or working
	// as a deleted agent (open mode has no token).
	var revoked []string
	if err := p.db.SelectContext(ctx, &revoked, `SELECT c.session FROM crew_connections c
		JOIN crew_tokens t ON t.organization_id = c.organization_id
		  AND t.id = CASE WHEN c.credential ~ '^[0-9a-f-]{36}$' THEN c.credential::uuid END
		WHERE c.process = $1 AND (t.revoked_at IS NOT NULL OR t.expires_at <= now())
		UNION
		SELECT c.session FROM crew_connections c
		JOIN agents a ON a.organization_id = c.organization_id AND a.id = c.agent_id
		WHERE c.process = $1 AND a.deleted_at IS NOT NULL`, process); err != nil {
		return nil, pgerr.Map(err, "find revoked crew connections")
	}
	seen := make(map[string]bool, len(alive))
	for _, s := range alive {
		seen[s] = true
	}
	var out []string
	for _, s := range sessions {
		if !seen[s] {
			out = append(out, s) // reaped: its deliveries went back to the queue
		}
	}
	return append(out, revoked...), nil
}

func (p *Presence) Reap(ctx context.Context, stale time.Duration) ([]crew.Reaped, error) {
	var rows []connectionRow
	if err := p.db.SelectContext(ctx, &rows, `DELETE FROM crew_connections
		WHERE seen_at < now() - make_interval(secs => $1)
		RETURNING organization_id, session`,
		stale.Seconds()); err != nil {
		return nil, pgerr.Map(err, "reap crew connections")
	}
	out := make([]crew.Reaped, 0, len(rows))
	for _, r := range rows {
		out = append(out, crew.Reaped{Organization: r.Organization, Session: r.Session})
	}
	return out, nil
}

func (p *Presence) Signal(ctx context.Context, s crew.Signal) error {
	raw, err := json.Marshal(s)
	if err != nil {
		return errx.Wrap(err, "encode crew signal", errx.TypeInternal)
	}
	_, err = p.db.ExecContext(ctx, `SELECT pg_notify($1, $2)`, signals, string(raw))
	return pgerr.Map(err, "send crew signal")
}

func (p *Presence) Listen(ctx context.Context, connected func(), fn func(crew.Signal)) {
	pglisten.Listen(ctx, p.db.DB, signals, connected, func(payload string) {
		var s crew.Signal
		if err := json.Unmarshal([]byte(payload), &s); err != nil {
			slog.Warn("bad crew signal", "err", err)
			return
		}
		fn(s)
	})
}
