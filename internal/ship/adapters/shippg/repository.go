// Package shippg is the Postgres repository of ships and the delivery queue.
package shippg

import (
	"context"
	"time"

	"github.com/Abraxas-365/seaport/internal/identity"
	"github.com/Abraxas-365/seaport/internal/pgerr"
	"github.com/Abraxas-365/seaport/internal/query"
	"github.com/Abraxas-365/seaport/internal/ship"
	"github.com/jmoiron/sqlx"
)

const columns = `id, organization_id, conversation_id, direction, external_id, sender_id, sender_name, text, status,
	delivery_id, claimed_until, error, sent_at, created_at`

// claimLock serializes the claims of one organization so two connectors
// never get the same conversation (the "nothing in flight" check needs
// it). The second key is hashtext(organization).
const claimLock = 7_401_001

// outboundLock serializes one dock's replies while their rate limits are
// counted (second key: hashtext(dock)).
const outboundLock = 7_401_002

var messages = pgerr.Messages{NotFound: "ship not found", ForeignKey: "conversation not found"}

type Repository struct{ db *sqlx.DB }

func New(db *sqlx.DB) *Repository { return &Repository{db: db} }

var _ ship.Repository = (*Repository)(nil)

func (r *Repository) CreateInbound(ctx context.Context, org identity.OrganizationID, id identity.ShipID, conv identity.ConversationID, in ship.Inbound, status ship.Status) (ship.Ship, bool, error) {
	var s ship.Ship
	err := r.db.GetContext(ctx, &s, `
		INSERT INTO ships (id, organization_id, conversation_id, direction, external_id, sender_id, sender_name, text, status, sent_at)
		VALUES ($1, $9, $2, 'in', $3, $4, $5, $6, $7, $8)
		ON CONFLICT (conversation_id, external_id) DO NOTHING
		RETURNING `+columns,
		id, conv, in.ExternalID, in.SenderID, in.SenderName, in.Text, status, in.SentAt, org)
	if pgerr.IsNoRows(err) {
		err = r.db.GetContext(ctx, &s, `SELECT `+columns+` FROM ships
			WHERE organization_id = $1 AND conversation_id = $2 AND external_id = $3`, org, conv, in.ExternalID)
		return s, false, pgerr.MapWith(err, "find duplicate ship", messages)
	}
	return s, err == nil, pgerr.MapWith(err, "create inbound ship", messages)
}

func (r *Repository) CreateOutbound(ctx context.Context, org identity.OrganizationID, id identity.ShipID, in ship.Outbound,
	dock identity.DockID, externalID string, admit func(ship.OutboundCounts) error) (ship.Ship, error) {
	var s ship.Ship
	tx, err := r.db.BeginTxx(ctx, nil)
	if err != nil {
		return s, pgerr.Map(err, "begin outbound")
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock($1::int, hashtext($2::text))`, outboundLock, dock); err != nil {
		return s, pgerr.Map(err, "lock outbound")
	}
	// Failed sends count too (conservative: most failures did reach the
	// channel, and an agent retrying in a loop must still be slowed down).
	var c struct {
		At          time.Time  `db:"at"`
		DockMinute  int        `db:"dock_minute"`
		DockHour    int        `db:"dock_hour"`
		ConvMinute  int        `db:"conv_minute"`
		OldDockMin  *time.Time `db:"old_dock_minute"`
		OldDockHour *time.Time `db:"old_dock_hour"`
		OldConvMin  *time.Time `db:"old_conv_minute"`
	}
	err = tx.GetContext(ctx, &c, `
		SELECT now() AS at,
		       count(*) FILTER (WHERE s.created_at > now() - interval '1 minute') AS dock_minute,
		       count(*) AS dock_hour,
		       count(*) FILTER (WHERE s.created_at > now() - interval '1 minute' AND s.conversation_id = $3) AS conv_minute,
		       min(s.created_at) FILTER (WHERE s.created_at > now() - interval '1 minute') AS old_dock_minute,
		       min(s.created_at) AS old_dock_hour,
		       min(s.created_at) FILTER (WHERE s.created_at > now() - interval '1 minute' AND s.conversation_id = $3) AS old_conv_minute
		FROM ships s JOIN conversations c ON c.organization_id = s.organization_id AND c.id = s.conversation_id
		WHERE s.organization_id = $1 AND c.dock_id = $2 AND s.direction = 'out'
		  AND s.created_at > now() - interval '1 hour'`, org, dock, in.Conversation)
	if err != nil {
		return s, pgerr.Map(err, "count outbound")
	}
	counts := ship.OutboundCounts{At: c.At, DockLastMinute: c.DockMinute, DockLastHour: c.DockHour, ConversationLastMinute: c.ConvMinute}
	for dst, src := range map[*time.Time]*time.Time{&counts.OldestDockMinute: c.OldDockMin,
		&counts.OldestDockHour: c.OldDockHour, &counts.OldestConversationMinute: c.OldConvMin} {
		if src != nil {
			*dst = *src
		}
	}
	if err := admit(counts); err != nil {
		return s, err
	}
	err = tx.GetContext(ctx, &s, `
		INSERT INTO ships (id, organization_id, conversation_id, direction, external_id, text, status, sent_at)
		VALUES ($1, $5, $2, 'out', $3, $4, 'pending', now())
		RETURNING `+columns, id, in.Conversation, externalID, in.Text, org)
	if err != nil {
		return s, pgerr.MapWith(err, "create outbound ship", messages)
	}
	return s, pgerr.Map(tx.Commit(), "commit outbound")
}

func (r *Repository) SetOutcome(ctx context.Context, org identity.OrganizationID, id identity.ShipID, status ship.Status, errMsg string) (ship.Ship, error) {
	var s ship.Ship
	err := r.db.GetContext(ctx, &s, `UPDATE ships SET status = $2, error = $3
		WHERE id = $1 AND organization_id = $4 RETURNING `+columns, id, status, errMsg, org)
	return s, pgerr.MapWith(err, "set ship outcome", messages)
}

func (r *Repository) Claim(ctx context.Context, org identity.OrganizationID, delivery identity.DeliveryID, until time.Time) (identity.ConversationID, []ship.Ship, error) {
	var conv identity.ConversationID
	tx, err := r.db.BeginTxx(ctx, nil)
	if err != nil {
		return conv, nil, pgerr.Map(err, "begin claim")
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock($1::int, hashtext($2::text))`, claimLock, org); err != nil {
		return conv, nil, pgerr.Map(err, "lock claims")
	}
	err = tx.GetContext(ctx, &conv, `
		SELECT q.conversation_id FROM ships q
		WHERE q.organization_id = $1 AND q.status = 'queued'
		  AND NOT EXISTS (SELECT 1 FROM ships c WHERE c.organization_id = q.organization_id
		                    AND c.conversation_id = q.conversation_id AND c.status = 'claimed')
		ORDER BY q.created_at, q.id
		LIMIT 1`, org)
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
			WHERE organization_id = $4 AND conversation_id = $1 AND status = 'queued'
			RETURNING `+columns+`)
		SELECT * FROM claimed ORDER BY sent_at, created_at, id`, conv, delivery, until, org)
	if err != nil {
		return conv, nil, pgerr.Map(err, "claim ships")
	}
	if err := tx.Commit(); err != nil {
		return conv, nil, pgerr.Map(err, "commit claim")
	}
	return conv, ships, nil
}

func (r *Repository) Join(ctx context.Context, org identity.OrganizationID, delivery identity.DeliveryID, until time.Time) (identity.ConversationID, []ship.Ship, error) {
	var conv identity.ConversationID
	tx, err := r.db.BeginTxx(ctx, nil)
	if err != nil {
		return conv, nil, pgerr.Map(err, "begin join")
	}
	defer func() { _ = tx.Rollback() }()
	// Same lock as Claim: a conversation's queued ships go to its open
	// delivery or to a new one, never both.
	if _, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock($1::int, hashtext($2::text))`, claimLock, org); err != nil {
		return conv, nil, pgerr.Map(err, "lock claims")
	}
	err = tx.GetContext(ctx, &conv, `SELECT conversation_id FROM ships
		WHERE organization_id = $1 AND delivery_id = $2 AND status = 'claimed' LIMIT 1`, org, delivery)
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
			WHERE organization_id = $4 AND conversation_id = $1 AND status = 'queued'
			RETURNING `+columns+`)
		SELECT * FROM joined ORDER BY sent_at, created_at, id`, conv, delivery, until, org)
	if err != nil {
		return conv, nil, pgerr.Map(err, "join ships")
	}
	if len(ships) > 0 {
		// One lease for the whole delivery.
		if _, err := tx.ExecContext(ctx, `UPDATE ships SET claimed_until = $2
			WHERE organization_id = $3 AND delivery_id = $1 AND status = 'claimed'`, delivery, until, org); err != nil {
			return conv, nil, pgerr.Map(err, "extend joined delivery")
		}
	}
	if err := tx.Commit(); err != nil {
		return conv, nil, pgerr.Map(err, "commit join")
	}
	return conv, ships, nil
}

func (r *Repository) Extend(ctx context.Context, org identity.OrganizationID, delivery identity.DeliveryID, until time.Time) error {
	return r.execDelivery(ctx, "extend delivery", `UPDATE ships SET claimed_until = $2
		WHERE delivery_id = $1 AND status = 'claimed' AND organization_id = $3`, delivery, until, org)
}

func (r *Repository) Ack(ctx context.Context, org identity.OrganizationID, delivery identity.DeliveryID, handled []identity.ShipID) (int, error) {
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
		WHERE delivery_id = $1 AND status = 'claimed' AND id = ANY($2::uuid[]) AND organization_id = $3`, delivery, ids, org)
	if err != nil {
		return 0, pgerr.Map(err, "ack ships")
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return 0, pgerr.MapWith(pgerr.ErrNoRows, "ack delivery", pgerr.Messages{NotFound: "delivery not found or no longer claimed"})
	}
	// Ships joined after the agent's turn ended were never seen: requeue.
	res, err = tx.ExecContext(ctx, `UPDATE ships SET status = 'queued', delivery_id = NULL, claimed_until = NULL
		WHERE organization_id = $2 AND delivery_id = $1 AND status = 'claimed'`, delivery, org)
	if err != nil {
		return 0, pgerr.Map(err, "requeue unhandled ships")
	}
	requeued, _ := res.RowsAffected()
	if err := tx.Commit(); err != nil {
		return 0, pgerr.Map(err, "commit ack")
	}
	return int(requeued), nil
}

func (r *Repository) Release(ctx context.Context, org identity.OrganizationID, delivery identity.DeliveryID) error {
	return r.execDelivery(ctx, "release delivery",
		`UPDATE ships SET status = 'queued', delivery_id = NULL, claimed_until = NULL
		 WHERE delivery_id = $1 AND status = 'claimed' AND organization_id = $2`, delivery, org)
}

func (r *Repository) ReleaseExpired(ctx context.Context, before time.Time) (map[identity.OrganizationID]int, error) {
	var rows []struct {
		Organization identity.OrganizationID `db:"organization_id"`
		N            int                     `db:"n"`
	}
	err := r.db.SelectContext(ctx, &rows, `
		WITH released AS (
			UPDATE ships SET status = 'queued', delivery_id = NULL, claimed_until = NULL
			WHERE status = 'claimed' AND claimed_until < $1
			RETURNING organization_id)
		SELECT organization_id, count(*) AS n FROM released GROUP BY organization_id`, before)
	if err != nil {
		return nil, pgerr.Map(err, "release expired")
	}
	out := make(map[identity.OrganizationID]int, len(rows))
	for _, row := range rows {
		out[row.Organization] = row.N
	}
	return out, nil
}

func (r *Repository) History(ctx context.Context, org identity.OrganizationID, conv identity.ConversationID, page query.Pagination) (query.Paginated[ship.Ship], error) {
	out := query.Paginated[ship.Ship]{Items: []ship.Ship{}, Page: query.Page{Limit: page.Limit, Offset: page.Offset}}
	if err := r.db.GetContext(ctx, &out.Page.Total, `SELECT count(*) FROM ships
		WHERE organization_id = $1 AND conversation_id = $2`, org, conv); err != nil {
		return out, pgerr.Map(err, "count ships")
	}
	err := r.db.SelectContext(ctx, &out.Items, `SELECT `+columns+` FROM ships WHERE organization_id = $1 AND conversation_id = $2
		ORDER BY sent_at DESC, created_at DESC, id DESC LIMIT $3 OFFSET $4`, org, conv, page.Limit, page.Offset)
	return out, pgerr.Map(err, "list ships")
}

func (r *Repository) Summaries(ctx context.Context, org identity.OrganizationID, convs []identity.ConversationID) (map[identity.ConversationID]ship.Summary, error) {
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
		FROM ships WHERE organization_id = $2 AND conversation_id = ANY($1::uuid[]) AND status IN ('held', 'queued', 'claimed')
		GROUP BY conversation_id`, ids, org); err != nil {
		return nil, pgerr.Map(err, "ship counts")
	}
	for _, c := range counts {
		out[c.Conversation] = ship.Summary{Held: c.Held, Queued: c.Queued, InFlight: c.Claimed > 0}
	}
	last := []ship.Ship{}
	if err := r.db.SelectContext(ctx, &last, `
		SELECT l.* FROM unnest($1::uuid[]) AS c(id)
		CROSS JOIN LATERAL (
			SELECT `+columns+` FROM ships WHERE organization_id = $2 AND conversation_id = c.id
			ORDER BY sent_at DESC, created_at DESC, id DESC LIMIT 1) l`, ids, org); err != nil {
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
