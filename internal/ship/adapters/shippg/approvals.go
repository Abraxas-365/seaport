package shippg

import (
	"context"
	"database/sql"
	"encoding/json"
	"strconv"
	"strings"
	"time"

	"github.com/Abraxas-365/seaport/internal/errx"
	"github.com/Abraxas-365/seaport/internal/identity"
	"github.com/Abraxas-365/seaport/internal/pgerr"
	"github.com/Abraxas-365/seaport/internal/pglock"
	"github.com/Abraxas-365/seaport/internal/query"
	"github.com/Abraxas-365/seaport/internal/ship"
)

const approvalColumns = `id, organization_id, conversation_id, agent_id, delivery_id, ship_id, text, options, status,
	answer, answered_by, answered_ship_id, expires_at, created_at, answered_at, ended_at, woken_at`

// approvalRow reads options as the JSON they are stored in.
type approvalRow struct {
	ship.Approval
	Raw []byte `db:"options"`
}

func (r approvalRow) approval() (ship.Approval, error) {
	a := r.Approval
	if err := json.Unmarshal(r.Raw, &a.Options); err != nil {
		return a, errx.Wrap(err, "approval options", errx.TypeInternal)
	}
	return a, nil
}

func approvals(rows []approvalRow) ([]ship.Approval, error) {
	out := make([]ship.Approval, 0, len(rows))
	for _, row := range rows {
		a, err := row.approval()
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, nil
}

// approvalLock is the class of the per-conversation lock of asks (second
// key: hashtext(conversation id)).
const approvalLock = 7_401_015

// ApprovalLocker is ship.ApprovalLocker over Postgres session advisory
// locks, so every process shares them.
type ApprovalLocker struct{ locks *pglock.Locker }

// NewApprovalLocker takes the locks on lockDB, a pool of its own (see
// pglock).
func NewApprovalLocker(lockDB *sql.DB) *ApprovalLocker {
	return &ApprovalLocker{locks: pglock.New(lockDB)}
}

var _ ship.ApprovalLocker = (*ApprovalLocker)(nil)

func (l *ApprovalLocker) Lock(ctx context.Context, _ identity.OrganizationID, conv identity.ConversationID) (func(), error) {
	return l.locks.Lock(ctx, approvalLock, conv.String())
}

var approvalMessages = pgerr.Messages{
	Unique:     "this agent already has a pending approval in the conversation",
	ForeignKey: "conversation or ship not found",
	NotFound:   "approval not found",
}

func (r *Repository) CreateApproval(ctx context.Context, org identity.OrganizationID, a ship.Approval) (ship.Approval, error) {
	raw, err := json.Marshal(a.Options)
	if err != nil {
		return a, errx.Wrap(err, "approval options", errx.TypeInternal)
	}
	var row approvalRow
	// The delivery the agent is working on in the thread, if any.
	err = r.db.GetContext(ctx, &row, `
		INSERT INTO ship_approvals (organization_id, id, conversation_id, agent_id, delivery_id, ship_id, text, options,
			expires_at, created_at)
		VALUES ($1, $2, $3, $4,
			(SELECT delivery_id FROM ships WHERE organization_id = $1 AND agent_id = $4 AND status = 'claimed'
				AND `+inThread("$1", "$3")+` LIMIT 1),
			$5, $6, $7::jsonb, $8, $9)
		RETURNING `+approvalColumns,
		org, a.ID, a.Conversation, a.Agent, a.Ship, a.Text, string(raw), a.ExpiresAt, a.CreatedAt)
	if err != nil {
		return a, pgerr.MapWith(err, "create approval", approvalMessages)
	}
	return row.approval()
}

func (r *Repository) FindApproval(ctx context.Context, org identity.OrganizationID, id identity.ApprovalID) (ship.Approval, error) {
	var row approvalRow
	if err := r.db.GetContext(ctx, &row, `SELECT `+approvalColumns+` FROM ship_approvals
		WHERE organization_id = $1 AND id = $2`, org, id); err != nil {
		return ship.Approval{}, pgerr.MapWith(err, "find approval", approvalMessages)
	}
	return row.approval()
}

func (r *Repository) PendingApprovals(ctx context.Context, org identity.OrganizationID, conv identity.ConversationID, now time.Time) ([]ship.Approval, error) {
	var rows []approvalRow
	if err := r.db.SelectContext(ctx, &rows, `SELECT `+approvalColumns+` FROM ship_approvals
		WHERE organization_id = $1 AND conversation_id = $2 AND status = 'pending' AND expires_at > $3
		ORDER BY created_at, id`, org, conv, now); err != nil {
		return nil, pgerr.Map(err, "pending approvals")
	}
	return approvals(rows)
}

func (r *Repository) ExpireAgentApprovals(ctx context.Context, org identity.OrganizationID, conv identity.ConversationID, agent identity.AgentID, now time.Time) ([]ship.Approval, error) {
	var rows []approvalRow
	// The agent asks again: nobody needs waking.
	if err := r.db.SelectContext(ctx, &rows, `UPDATE ship_approvals SET status = 'expired', ended_at = $4, woken_at = $4
		WHERE organization_id = $1 AND conversation_id = $2 AND agent_id = $3 AND status = 'pending' AND expires_at <= $4
		RETURNING `+approvalColumns, org, conv, agent, now); err != nil {
		return nil, pgerr.Map(err, "expire agent approvals")
	}
	return approvals(rows)
}

func (r *Repository) AnswerApproval(ctx context.Context, org identity.OrganizationID, id identity.ApprovalID, answer ship.Answer, now time.Time) (ship.Approval, bool, error) {
	var row approvalRow
	err := r.db.GetContext(ctx, &row, `
		UPDATE ship_approvals SET status = 'answered', answer = $3, answered_by = $4, answered_at = $5, ended_at = $5
		WHERE organization_id = $1 AND id = $2 AND status = 'pending' AND expires_at > $5
		RETURNING `+approvalColumns,
		org, id, answer.Option, answer.By, now)
	if pgerr.IsNoRows(err) {
		return ship.Approval{}, false, nil
	}
	if err != nil {
		return ship.Approval{}, false, pgerr.MapWith(err, "answer approval", approvalMessages)
	}
	a, err := row.approval()
	return a, true, err
}

func (r *Repository) CancelApproval(ctx context.Context, org identity.OrganizationID, id identity.ApprovalID) (ship.Approval, bool, error) {
	var row approvalRow
	err := r.db.GetContext(ctx, &row, `UPDATE ship_approvals SET status = 'cancelled', ended_at = now()
		WHERE organization_id = $1 AND id = $2 AND status = 'pending'
		RETURNING `+approvalColumns, org, id)
	if pgerr.IsNoRows(err) {
		return ship.Approval{}, false, nil
	}
	if err != nil {
		return ship.Approval{}, false, pgerr.MapWith(err, "cancel approval", approvalMessages)
	}
	a, err := row.approval()
	return a, true, err
}

func (r *Repository) WakeApproval(ctx context.Context, org identity.OrganizationID, id identity.ApprovalID, now time.Time) error {
	_, err := r.db.ExecContext(ctx, `UPDATE ship_approvals SET woken_at = $3
		WHERE organization_id = $1 AND id = $2 AND woken_at IS NULL`, org, id, now)
	return pgerr.Map(err, "wake approval")
}

func (r *Repository) UnwokenApprovals(ctx context.Context, since, before time.Time, limit int) ([]ship.Approval, error) {
	var rows []approvalRow
	if err := r.db.SelectContext(ctx, &rows, `SELECT `+approvalColumns+` FROM ship_approvals
		WHERE status <> 'pending' AND woken_at IS NULL AND ended_at > $1 AND ended_at <= $2
		ORDER BY ended_at LIMIT $3`, since, before, limit); err != nil {
		return nil, pgerr.Map(err, "unwoken approvals")
	}
	return approvals(rows)
}

func (r *Repository) ExpireApprovals(ctx context.Context, now time.Time) ([]ship.Approval, error) {
	var rows []approvalRow
	if err := r.db.SelectContext(ctx, &rows, `UPDATE ship_approvals SET status = 'expired', ended_at = $1
		WHERE status = 'pending' AND expires_at <= $1
		RETURNING `+approvalColumns, now); err != nil {
		return nil, pgerr.Map(err, "expire approvals")
	}
	return approvals(rows)
}

func (r *Repository) Approvals(ctx context.Context, org identity.OrganizationID, filter ship.ApprovalFilter, page query.Pagination) (query.Paginated[ship.Approval], error) {
	return r.listApprovals(ctx, `organization_id = $1 AND ($2 = '' OR status = $2)`, page, org, string(filter.Status))
}

func (r *Repository) ConversationApprovals(ctx context.Context, org identity.OrganizationID, conv identity.ConversationID, page query.Pagination) (query.Paginated[ship.Approval], error) {
	return r.listApprovals(ctx, `organization_id = $1 AND conversation_id = $2`, page, org, conv)
}

func (r *Repository) listApprovals(ctx context.Context, where string, page query.Pagination, args ...any) (query.Paginated[ship.Approval], error) {
	out := query.Paginated[ship.Approval]{Items: []ship.Approval{}, Page: query.Page{Limit: page.Limit, Offset: page.Offset}}
	if err := r.db.GetContext(ctx, &out.Page.Total, `SELECT count(*) FROM ship_approvals WHERE `+where, args...); err != nil {
		return out, pgerr.Map(err, "count approvals")
	}
	var rows []approvalRow
	n := len(args)
	q := `SELECT ` + approvalColumns + ` FROM ship_approvals WHERE ` + where +
		` ORDER BY created_at DESC, id LIMIT $` + strconv.Itoa(n+1) + ` OFFSET $` + strconv.Itoa(n+2)
	if err := r.db.SelectContext(ctx, &rows, q, append(args, page.Limit, page.Offset)...); err != nil {
		return out, pgerr.Map(err, "list approvals")
	}
	items, err := approvals(rows)
	if err != nil {
		return out, err
	}
	out.Items = items
	return out, nil
}

func (r *Repository) DeliveryApprovals(ctx context.Context, org identity.OrganizationID, conv identity.ConversationID, agent identity.AgentID, ships []identity.ShipID, now time.Time) ([]ship.Approval, error) {
	var rows []approvalRow
	err := r.db.SelectContext(ctx, &rows, `SELECT `+approvalColumns+` FROM ship_approvals a
		WHERE organization_id = $1 AND agent_id = $2 AND `+inThread("$1", "$3")+`
			AND ((status = 'pending' AND expires_at > $4)
			  OR answered_ship_id = ANY($5::uuid[])
			  OR EXISTS (SELECT 1 FROM ships s WHERE s.organization_id = a.organization_id AND s.id = ANY($5::uuid[])
					AND s.external_id = 'approval-' || a.id::text))
		ORDER BY created_at, id`, org, agent, conv, now, shipIDs(ships))
	if err != nil {
		return nil, pgerr.Map(err, "delivery approvals")
	}
	return approvals(rows)
}

func shipIDs(ids []identity.ShipID) []string {
	out := make([]string, len(ids))
	for i, id := range ids {
		out[i] = id.String()
	}
	return out
}

// qualified prefixes a column list with the alias a.
func qualified(columns string) string {
	cols := strings.Split(columns, ",")
	for i, c := range cols {
		cols[i] = "a." + strings.TrimSpace(c)
	}
	return strings.Join(cols, ", ")
}
