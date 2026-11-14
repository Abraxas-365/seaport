package shippg

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"
	"time"

	"github.com/Abraxas-365/seaport/internal/errx"
	"github.com/Abraxas-365/seaport/internal/identity"
	"github.com/Abraxas-365/seaport/internal/pgerr"
	"github.com/Abraxas-365/seaport/internal/query"
	"github.com/Abraxas-365/seaport/internal/ship"
)

const approvalColumns = `id, organization_id, conversation_id, agent_id, delivery_id, ship_id, text, options, status,
	answer, answered_by, answered_ship_id, expires_at, created_at, answered_at`

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

var approvalMessages = pgerr.Messages{
	Unique:     "this agent already has a pending approval in the conversation",
	ForeignKey: "conversation or ship not found",
	NotFound:   "approval not found",
}

func (r *Repository) CreateApproval(ctx context.Context, org identity.OrganizationID, a ship.Approval, waitUntil *time.Time) (ship.Approval, error) {
	raw, err := json.Marshal(a.Options)
	if err != nil {
		return a, errx.Wrap(err, "approval options", errx.TypeInternal)
	}
	var row approvalRow
	// The delivery the agent is working on in the thread, if any.
	err = r.db.GetContext(ctx, &row, `
		INSERT INTO ship_approvals (organization_id, id, conversation_id, agent_id, delivery_id, ship_id, text, options,
			waiting_until, expires_at, created_at)
		VALUES ($1, $2, $3, $4,
			(SELECT delivery_id FROM ships WHERE organization_id = $1 AND agent_id = $4 AND status = 'claimed'
				AND `+inThread("$1", "$3")+` LIMIT 1),
			$5, $6, $7::jsonb, $8, $9, $10)
		RETURNING `+approvalColumns,
		org, a.ID, a.Conversation, a.Agent, a.Ship, a.Text, string(raw), waitUntil, a.ExpiresAt, a.CreatedAt)
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

func (r *Repository) AnswerApproval(ctx context.Context, org identity.OrganizationID, id identity.ApprovalID, answer ship.Answer, now time.Time) (ship.Approval, bool, bool, error) {
	var row struct {
		approvalRow
		Waited bool `db:"waited"`
	}
	// old is the row as it was: was an ask blocked on it?
	err := r.db.GetContext(ctx, &row, `
		WITH old AS (SELECT id, (waiting_until IS NOT NULL AND waiting_until > $5) AS waited FROM ship_approvals
			WHERE organization_id = $1 AND id = $2 AND status = 'pending' AND expires_at > $5 FOR UPDATE)
		UPDATE ship_approvals a SET status = 'answered', answer = $3, answered_by = $4, answered_ship_id = $6,
			answered_at = $5, waiting_until = NULL
		FROM old WHERE a.organization_id = $1 AND a.id = old.id
		RETURNING `+qualified(approvalColumns)+`, old.waited AS waited`,
		org, id, answer.Option, answer.By, now, answer.Ship)
	if pgerr.IsNoRows(err) {
		return ship.Approval{}, false, false, nil
	}
	if err != nil {
		return ship.Approval{}, false, false, pgerr.MapWith(err, "answer approval", approvalMessages)
	}
	a, err := row.approval()
	return a, row.Waited, true, err
}

func (r *Repository) CancelApproval(ctx context.Context, org identity.OrganizationID, id identity.ApprovalID) (ship.Approval, bool, error) {
	var row approvalRow
	err := r.db.GetContext(ctx, &row, `UPDATE ship_approvals SET status = 'cancelled', waiting_until = NULL
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

func (r *Repository) WaitApproval(ctx context.Context, org identity.OrganizationID, id identity.ApprovalID, until *time.Time) error {
	_, err := r.db.ExecContext(ctx, `UPDATE ship_approvals SET waiting_until = $3
		WHERE organization_id = $1 AND id = $2 AND status = 'pending'`, org, id, until)
	return pgerr.Map(err, "wait approval")
}

func (r *Repository) ExpireApprovals(ctx context.Context, now time.Time) ([]ship.Approval, error) {
	var rows []approvalRow
	// An ask still blocked on one ends it first (its wait is short).
	if err := r.db.SelectContext(ctx, &rows, `UPDATE ship_approvals SET status = 'expired', waiting_until = NULL
		WHERE status = 'pending' AND expires_at <= $1 AND (waiting_until IS NULL OR waiting_until <= $1)
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
