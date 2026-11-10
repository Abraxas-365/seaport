package hostingpg

import (
	"context"

	"github.com/Abraxas-365/seaport/internal/errx"
	"github.com/Abraxas-365/seaport/internal/hosting"
	"github.com/Abraxas-365/seaport/internal/identity"
	"github.com/Abraxas-365/seaport/internal/pgerr"
)

// Subscription sign-ins (slot_logins): one row per slot, its latest
// attempt; every relay write matches the attempt's id.

const loginColumns = `id, idx, state, url, detail, created_by, created_at, updated_at, run`

func (r *Repository) BeginLogin(ctx context.Context, org identity.OrganizationID, agentID identity.AgentID, slot int, id, by string) (hosting.Login, bool, error) {
	tx, err := r.db.BeginTxx(ctx, nil)
	if err != nil {
		return hosting.Login{}, false, pgerr.Map(err, "begin sign-in")
	}
	defer func() { _ = tx.Rollback() }()
	var s struct {
		State string `db:"state"`
		Run   string `db:"run"`
	}
	err = tx.GetContext(ctx, &s, `SELECT state, run FROM agent_slots WHERE organization_id = $1 AND agent_id = $2 AND idx = $3 FOR UPDATE`,
		org, agentID, slot)
	if pgerr.IsNoRows(err) {
		return hosting.Login{}, false, errx.NotFound("slot not found")
	}
	if err != nil {
		return hosting.Login{}, false, pgerr.Map(err, "read slot")
	}
	if s.State != hosting.SlotRunning {
		return hosting.Login{}, false, errx.Business("the slot isn't running: start it, then sign in")
	}
	var cur hosting.Login
	err = tx.GetContext(ctx, &cur, `SELECT `+loginColumns+` FROM slot_logins
		WHERE organization_id = $1 AND agent_id = $2 AND idx = $3
		  AND state IN ('starting', 'waiting', 'verifying') AND run = $4
		  AND updated_at > now() - make_interval(secs => $5::float8)`,
		org, agentID, slot, s.Run, hosting.LoginStale.Seconds())
	if err == nil {
		return cur, false, nil // one is going: show it
	}
	if !pgerr.IsNoRows(err) {
		return hosting.Login{}, false, pgerr.Map(err, "read sign-in")
	}
	var out hosting.Login
	if err := tx.GetContext(ctx, &out, `INSERT INTO slot_logins (organization_id, agent_id, idx, id, run, state, created_by)
		VALUES ($1, $2, $3, $4, $5, 'starting', $6)
		ON CONFLICT (organization_id, agent_id, idx) DO UPDATE SET id = EXCLUDED.id, run = EXCLUDED.run,
			state = 'starting', url = '', code = '', detail = '', created_by = EXCLUDED.created_by,
			created_at = now(), updated_at = now()
		RETURNING `+loginColumns, org, agentID, slot, id, s.Run, by); err != nil {
		return hosting.Login{}, false, pgerr.Map(err, "start sign-in")
	}
	return out, true, pgerr.Map(tx.Commit(), "commit sign-in")
}

func (r *Repository) LoginProgress(ctx context.Context, org identity.OrganizationID, agentID identity.AgentID, slot int, id string, p hosting.LoginUpdate) (hosting.Login, error) {
	var out hosting.Login
	err := r.db.GetContext(ctx, &out, `UPDATE slot_logins SET
			state = COALESCE(NULLIF($5, ''), state), url = COALESCE(NULLIF($6, ''), url),
			detail = COALESCE(NULLIF($7, ''), detail),
			code = CASE WHEN $5 IN ('done', 'failed') THEN '' ELSE code END, updated_at = now()
		WHERE organization_id = $1 AND agent_id = $2 AND idx = $3 AND id = $4 AND state IN ('starting', 'waiting', 'verifying')
		RETURNING `+loginColumns, org, agentID, slot, id, p.State, p.URL, p.Detail)
	if pgerr.IsNoRows(err) {
		return hosting.Login{}, errx.Conflict("the sign-in is over")
	}
	return out, pgerr.Map(err, "sign-in progress")
}

func (r *Repository) TakeLoginCode(ctx context.Context, org identity.OrganizationID, agentID identity.AgentID, slot int, id string) (string, error) {
	var code string
	err := r.db.GetContext(ctx, &code, `WITH old AS (
			SELECT code FROM slot_logins WHERE organization_id = $1 AND agent_id = $2 AND idx = $3 AND id = $4
			  AND state = 'waiting' AND code <> '' FOR UPDATE)
		UPDATE slot_logins l SET code = '', state = 'verifying', updated_at = now()
		FROM old WHERE l.organization_id = $1 AND l.agent_id = $2 AND l.idx = $3
		RETURNING old.code`, org, agentID, slot, id)
	if pgerr.IsNoRows(err) {
		return "", nil
	}
	return code, pgerr.Map(err, "take sign-in code")
}

func (r *Repository) SubmitLoginCode(ctx context.Context, org identity.OrganizationID, agentID identity.AgentID, slot int, code string) (hosting.Login, error) {
	var out hosting.Login
	err := r.db.GetContext(ctx, &out, `UPDATE slot_logins SET code = $4, updated_at = now()
		WHERE organization_id = $1 AND agent_id = $2 AND idx = $3 AND state = 'waiting' AND code = ''
		  AND updated_at > now() - make_interval(secs => $5::float8)
		RETURNING `+loginColumns, org, agentID, slot, code, hosting.LoginStale.Seconds())
	if pgerr.IsNoRows(err) {
		return hosting.Login{}, errx.Conflict("no sign-in of this slot is waiting for a code: start one")
	}
	return out, pgerr.Map(err, "submit sign-in code")
}

func (r *Repository) Login(ctx context.Context, org identity.OrganizationID, agentID identity.AgentID, slot int) (hosting.Login, error) {
	var out hosting.Login
	err := r.db.GetContext(ctx, &out, `SELECT `+loginColumns+` FROM slot_logins
		WHERE organization_id = $1 AND agent_id = $2 AND idx = $3`, org, agentID, slot)
	if pgerr.IsNoRows(err) {
		return hosting.Login{}, errx.NotFound("the slot was never signed in from Seaport")
	}
	return out, pgerr.Map(err, "read sign-in")
}
