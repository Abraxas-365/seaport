// Package shippg is the Postgres repository of ships and the delivery queue.
package shippg

import (
	"context"
	"time"

	"github.com/Abraxas-365/puerto/internal/identity"
	"github.com/Abraxas-365/puerto/internal/pgerr"
	"github.com/Abraxas-365/puerto/internal/query"
	"github.com/Abraxas-365/puerto/internal/ship"
	"github.com/jmoiron/sqlx"
)

const columns = `id, conversation_id, direction, external_id, sender_id, sender_name, text, status,
	delivery_id, claimed_until, error, sent_at, created_at`

// claimLock serializes claims so two connectors never get the same
// conversation (the "nothing in flight" check needs it).
const claimLock = 7_401_001

var messages = pgerr.Messages{NotFound: "ship not found", ForeignKey: "conversation not found"}

type Repository struct{ db *sqlx.DB }

func New(db *sqlx.DB) *Repository { return &Repository{db: db} }

var _ ship.Repository = (*Repository)(nil)

func (r *Repository) CreateInbound(ctx context.Context, id identity.ShipID, conv identity.ConversationID, in ship.Inbound, status ship.Status) (ship.Ship, bool, error) {
	var s ship.Ship
	err := r.db.GetContext(ctx, &s, `
		INSERT INTO ships (id, conversation_id, direction, external_id, sender_id, sender_name, text, status, sent_at)
		VALUES ($1, $2, 'in', $3, $4, $5, $6, $7, $8)
		ON CONFLICT (conversation_id, external_id) DO NOTHING
		RETURNING `+columns,
		id, conv, in.ExternalID, in.SenderID, in.SenderName, in.Text, status, in.SentAt)
	if pgerr.IsNoRows(err) {
		err = r.db.GetContext(ctx, &s, `SELECT `+columns+` FROM ships WHERE conversation_id = $1 AND external_id = $2`,
			conv, in.ExternalID)
		return s, false, pgerr.MapWith(err, "find duplicate ship", messages)
	}
	return s, err == nil, pgerr.MapWith(err, "create inbound ship", messages)
}

func (r *Repository) CreateOutbound(ctx context.Context, id identity.ShipID, in ship.Outbound, externalID string) (ship.Ship, error) {
	var s ship.Ship
	err := r.db.GetContext(ctx, &s, `
		INSERT INTO ships (id, conversation_id, direction, external_id, text, status, sent_at)
		VALUES ($1, $2, 'out', $3, $4, 'pending', now())
		RETURNING `+columns, id, in.Conversation, externalID, in.Text)
	return s, pgerr.MapWith(err, "create outbound ship", messages)
}

func (r *Repository) SetOutcome(ctx context.Context, id identity.ShipID, status ship.Status, errMsg string) (ship.Ship, error) {
	var s ship.Ship
	err := r.db.GetContext(ctx, &s, `UPDATE ships SET status = $2, error = $3 WHERE id = $1 RETURNING `+columns,
		id, status, errMsg)
	return s, pgerr.MapWith(err, "set ship outcome", messages)
}

func (r *Repository) Claim(ctx context.Context, delivery identity.DeliveryID, until time.Time) (identity.ConversationID, []ship.Ship, error) {
	var conv identity.ConversationID
	tx, err := r.db.BeginTxx(ctx, nil)
	if err != nil {
		return conv, nil, pgerr.Map(err, "begin claim")
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock($1)`, claimLock); err != nil {
		return conv, nil, pgerr.Map(err, "lock claims")
	}
	err = tx.GetContext(ctx, &conv, `
		SELECT q.conversation_id FROM ships q
		WHERE q.status = 'queued'
		  AND NOT EXISTS (SELECT 1 FROM ships c WHERE c.conversation_id = q.conversation_id AND c.status = 'claimed')
		ORDER BY q.created_at, q.id
		LIMIT 1`)
	if pgerr.IsNoRows(err) {
		return conv, nil, nil
	}
	if err != nil {
		return conv, nil, pgerr.Map(err, "pick conversation")
	}
	ships := []ship.Ship{}
	err = tx.SelectContext(ctx, &ships, `
		WITH claimed AS (
			UPDATE ships SET status = 'claimed', delivery_id = $2, claimed_until = $3
			WHERE conversation_id = $1 AND status = 'queued'
			RETURNING `+columns+`)
		SELECT * FROM claimed ORDER BY sent_at, created_at, id`, conv, delivery, until)
	if err != nil {
		return conv, nil, pgerr.Map(err, "claim ships")
	}
	if err := tx.Commit(); err != nil {
		return conv, nil, pgerr.Map(err, "commit claim")
	}
	return conv, ships, nil
}

func (r *Repository) Join(ctx context.Context, delivery identity.DeliveryID, until time.Time) (identity.ConversationID, []ship.Ship, error) {
	var conv identity.ConversationID
	tx, err := r.db.BeginTxx(ctx, nil)
	if err != nil {
		return conv, nil, pgerr.Map(err, "begin join")
	}
	defer func() { _ = tx.Rollback() }()
	// Same lock as Claim: a conversation's queued ships go to its open
	// delivery or to a new one, never both.
	if _, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock($1)`, claimLock); err != nil {
		return conv, nil, pgerr.Map(err, "lock claims")
	}
	err = tx.GetContext(ctx, &conv, `SELECT conversation_id FROM ships
		WHERE delivery_id = $1 AND status = 'claimed' LIMIT 1`, delivery)
	if pgerr.IsNoRows(err) {
		return conv, nil, nil // closed (acked, released or expired)
	}
	if err != nil {
		return conv, nil, pgerr.Map(err, "find delivery")
	}
	ships := []ship.Ship{}
	err = tx.SelectContext(ctx, &ships, `
		WITH joined AS (
			UPDATE ships SET status = 'claimed', delivery_id = $2, claimed_until = $3
			WHERE conversation_id = $1 AND status = 'queued'
			RETURNING `+columns+`)
		SELECT * FROM joined ORDER BY sent_at, created_at, id`, conv, delivery, until)
	if err != nil {
		return conv, nil, pgerr.Map(err, "join ships")
	}
	if len(ships) > 0 {
		// One lease for the whole delivery.
		if _, err := tx.ExecContext(ctx, `UPDATE ships SET claimed_until = $2
			WHERE delivery_id = $1 AND status = 'claimed'`, delivery, until); err != nil {
			return conv, nil, pgerr.Map(err, "extend joined delivery")
		}
	}
	if err := tx.Commit(); err != nil {
		return conv, nil, pgerr.Map(err, "commit join")
	}
	return conv, ships, nil
}

func (r *Repository) Extend(ctx context.Context, delivery identity.DeliveryID, until time.Time) error {
	return r.execDelivery(ctx, "extend delivery",
		`UPDATE ships SET claimed_until = $2 WHERE delivery_id = $1 AND status = 'claimed'`, delivery, until)
}

func (r *Repository) Ack(ctx context.Context, delivery identity.DeliveryID, handled []identity.ShipID) (int, error) {
	ids := make([]string, len(handled))
	for i, id := range handled {
		ids[i] = id.String()
	}
	tx, err := r.db.BeginTxx(ctx, nil)
	if err != nil {
		return 0, pgerr.Map(err, "begin ack")
	}
	defer func() { _ = tx.Rollback() }()
	res, err := tx.ExecContext(ctx, `UPDATE ships SET status = 'handled', claimed_until = NULL
		WHERE delivery_id = $1 AND status = 'claimed' AND id = ANY($2::uuid[])`, delivery, ids)
	if err != nil {
		return 0, pgerr.Map(err, "ack ships")
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return 0, pgerr.MapWith(pgerr.ErrNoRows, "ack delivery", pgerr.Messages{NotFound: "delivery not found or no longer claimed"})
	}
	// Ships joined after the agent's turn ended were never seen: requeue.
	res, err = tx.ExecContext(ctx, `UPDATE ships SET status = 'queued', delivery_id = NULL, claimed_until = NULL
		WHERE delivery_id = $1 AND status = 'claimed'`, delivery)
	if err != nil {
		return 0, pgerr.Map(err, "requeue unhandled ships")
	}
	requeued, _ := res.RowsAffected()
	if err := tx.Commit(); err != nil {
		return 0, pgerr.Map(err, "commit ack")
	}
	return int(requeued), nil
}

func (r *Repository) Release(ctx context.Context, delivery identity.DeliveryID) error {
	return r.execDelivery(ctx, "release delivery",
		`UPDATE ships SET status = 'queued', delivery_id = NULL, claimed_until = NULL
		 WHERE delivery_id = $1 AND status = 'claimed'`, delivery)
}

func (r *Repository) ReleaseExpired(ctx context.Context, before time.Time) (int, error) {
	res, err := r.db.ExecContext(ctx, `UPDATE ships SET status = 'queued', delivery_id = NULL, claimed_until = NULL
		WHERE status = 'claimed' AND claimed_until < $1`, before)
	if err != nil {
		return 0, pgerr.Map(err, "release expired")
	}
	n, _ := res.RowsAffected()
	return int(n), nil
}

func (r *Repository) History(ctx context.Context, conv identity.ConversationID, page query.Pagination) (query.Paginated[ship.Ship], error) {
	out := query.Paginated[ship.Ship]{Items: []ship.Ship{}, Page: query.Page{Limit: page.Limit, Offset: page.Offset}}
	if err := r.db.GetContext(ctx, &out.Page.Total, `SELECT count(*) FROM ships WHERE conversation_id = $1`, conv); err != nil {
		return out, pgerr.Map(err, "count ships")
	}
	err := r.db.SelectContext(ctx, &out.Items, `SELECT `+columns+` FROM ships WHERE conversation_id = $1
		ORDER BY sent_at DESC, created_at DESC, id DESC LIMIT $2 OFFSET $3`, conv, page.Limit, page.Offset)
	return out, pgerr.Map(err, "list ships")
}

func (r *Repository) Summaries(ctx context.Context, convs []identity.ConversationID) (map[identity.ConversationID]ship.Summary, error) {
	out := make(map[identity.ConversationID]ship.Summary, len(convs))
	if len(convs) == 0 {
		return out, nil
	}
	ids := make([]string, len(convs))
	for i, id := range convs {
		ids[i] = id.String()
	}
	var counts []struct {
		Conversation identity.ConversationID `db:"conversation_id"`
		Held         int                     `db:"held"`
		Queued       int                     `db:"queued"`
		Claimed      int                     `db:"claimed"`
	}
	if err := r.db.SelectContext(ctx, &counts, `
		SELECT conversation_id,
		       count(*) FILTER (WHERE status = 'held')    AS held,
		       count(*) FILTER (WHERE status = 'queued')  AS queued,
		       count(*) FILTER (WHERE status = 'claimed') AS claimed
		FROM ships WHERE conversation_id = ANY($1::uuid[]) AND status IN ('held', 'queued', 'claimed')
		GROUP BY conversation_id`, ids); err != nil {
		return nil, pgerr.Map(err, "ship counts")
	}
	for _, c := range counts {
		out[c.Conversation] = ship.Summary{Held: c.Held, Queued: c.Queued, InFlight: c.Claimed > 0}
	}
	last := []ship.Ship{}
	if err := r.db.SelectContext(ctx, &last, `
		SELECT l.* FROM unnest($1::uuid[]) AS c(id)
		CROSS JOIN LATERAL (
			SELECT `+columns+` FROM ships WHERE conversation_id = c.id
			ORDER BY sent_at DESC, created_at DESC, id DESC LIMIT 1) l`, ids); err != nil {
		return nil, pgerr.Map(err, "last ships")
	}
	for i := range last {
		s := out[last[i].Conversation]
		s.Last = &last[i]
		out[last[i].Conversation] = s
	}
	return out, nil
}

// execDelivery runs an update over a delivery's claimed ships; zero rows
// means the delivery is unknown, already acked or expired.
func (r *Repository) execDelivery(ctx context.Context, op, q string, args ...any) error {
	res, err := r.db.ExecContext(ctx, q, args...)
	if err != nil {
		return pgerr.Map(err, op)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return pgerr.MapWith(pgerr.ErrNoRows, op, pgerr.Messages{NotFound: "delivery not found or no longer claimed"})
	}
	return nil
}
