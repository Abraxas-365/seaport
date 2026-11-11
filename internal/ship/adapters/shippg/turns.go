package shippg

import (
	"context"
	"encoding/json"

	"github.com/Abraxas-365/seaport/internal/identity"
	"github.com/Abraxas-365/seaport/internal/pgerr"
	"github.com/Abraxas-365/seaport/internal/query"
	"github.com/Abraxas-365/seaport/internal/ship"
)

const turnColumns = `id, organization_id, conversation_id, agent_id, agent_name, outcome, started_at, ended_at, tools, omitted`

// turnRow is a ship_turns row (tools as JSON).
type turnRow struct {
	ship.Turn
	Tools []byte `db:"tools"`
}

func (r *Repository) SaveTurn(ctx context.Context, org identity.OrganizationID, t ship.Turn) (bool, error) {
	tools, err := json.Marshal(t.Tools)
	if err != nil {
		return false, pgerr.Map(err, "encode turn tools")
	}
	res, err := r.db.ExecContext(ctx, `INSERT INTO ship_turns (`+turnColumns+`)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10) ON CONFLICT (organization_id, id) DO NOTHING`,
		t.Delivery, org, t.Conversation, t.Agent, t.AgentName, t.Outcome, t.StartedAt, t.EndedAt, tools, t.Omitted)
	if err != nil {
		return false, pgerr.Map(err, "save turn")
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

func (r *Repository) Turns(ctx context.Context, org identity.OrganizationID, conv identity.ConversationID, page query.Pagination) (query.Paginated[ship.Turn], error) {
	out := query.Paginated[ship.Turn]{Items: []ship.Turn{}, Page: query.Page{Limit: page.Limit, Offset: page.Offset}}
	if err := r.db.GetContext(ctx, &out.Page.Total, `SELECT count(*) FROM ship_turns
		WHERE organization_id = $1 AND conversation_id = $2`, org, conv); err != nil {
		return out, pgerr.Map(err, "count turns")
	}
	var rows []turnRow
	if err := r.db.SelectContext(ctx, &rows, `SELECT `+turnColumns+` FROM ship_turns
		WHERE organization_id = $1 AND conversation_id = $2
		ORDER BY ended_at DESC, id DESC LIMIT $3 OFFSET $4`, org, conv, page.Limit, page.Offset); err != nil {
		return out, pgerr.Map(err, "list turns")
	}
	for _, row := range rows {
		t := row.Turn
		if err := json.Unmarshal(row.Tools, &t.Tools); err != nil {
			return out, pgerr.Map(err, "decode turn tools")
		}
		out.Items = append(out.Items, t)
	}
	return out, nil
}
