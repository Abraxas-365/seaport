// Package shippg is the Postgres repository of ships and the delivery queue.
package shippg

import (
	"context"
	"time"

	"github.com/Abraxas-365/seaport/internal/agent"
	"github.com/Abraxas-365/seaport/internal/conversation"
	"github.com/Abraxas-365/seaport/internal/identity"
	"github.com/Abraxas-365/seaport/internal/pgerr"
	"github.com/Abraxas-365/seaport/internal/query"
	"github.com/Abraxas-365/seaport/internal/ship"
	"github.com/jmoiron/sqlx"
)

const columns = `id, organization_id, conversation_id, direction, external_id, sender_id, sender_name, text, status,
	agent_id, hold_reason, delivery_id, claimed_until, error, sent_at, created_at`

const attachmentColumns = `organization_id, id, ship_id, position, kind, mime, size, name, sha256, blob_key`

// claimLock serializes the claims of one organization so two connectors
// never get the same conversation (the "nothing in flight" check needs
// it). The second key is hashtext(organization).
const claimLock = 7_401_001

// outboundLock serializes one organization's replies while their rate
// limits (dock, conversation, organization) are counted; the send itself
// happens after the commit. The second key is hashtext(organization).
const outboundLock = 7_401_002

var messages = pgerr.Messages{NotFound: "ship not found", ForeignKey: "conversation not found"}

type Repository struct{ db *sqlx.DB }

func New(db *sqlx.DB) *Repository { return &Repository{db: db} }

var _ ship.Repository = (*Repository)(nil)

func (r *Repository) CreateInbound(ctx context.Context, org identity.OrganizationID, id identity.ShipID, conv conversation.Conversation,
	in ship.Inbound, attachments []ship.Attachment, hold string, route ship.Router) (ship.Ship, bool, error) {
	var s ship.Ship
	tx, err := r.db.BeginTxx(ctx, nil)
	if err != nil {
		return s, false, pgerr.Map(err, "begin inbound")
	}
	defer func() { _ = tx.Rollback() }()
	status := ship.StatusHeld
	var target *identity.AgentID
	if hold == "" {
		// The team's rows stay locked until commit: unassigning or deleting
		// an agent (agentpg) waits, then holds this ship if it was its.
		team := []agent.Member{}
		if err := tx.SelectContext(ctx, &team, `SELECT d.agent_id, a.name, d.is_default FROM dock_agents d
			JOIN agents a ON a.organization_id = d.organization_id AND a.id = d.agent_id
			WHERE d.organization_id = $1 AND d.dock_id = $2 ORDER BY d.created_at, a.name FOR SHARE OF d`,
			org, conv.Dock); err != nil {
			return s, false, pgerr.Map(err, "dock team")
		}
		var previous identity.AgentID
		// The agent the conversation last talked with: its last routed
		// inbound ship or the last reply an agent sent.
		err := tx.GetContext(ctx, &previous, `SELECT agent_id FROM ships
			WHERE organization_id = $1 AND conversation_id = $2 AND agent_id IS NOT NULL
			ORDER BY created_at DESC LIMIT 1`, org, conv.ID)
		if err != nil && !pgerr.IsNoRows(err) {
			return s, false, pgerr.Map(err, "previous agent")
		}
		picked, reason := route(team, previous)
		if picked.IsZero() {
			hold = reason
		} else {
			status, target = ship.StatusQueued, &picked
		}
	}
	err = tx.GetContext(ctx, &s, `
		INSERT INTO ships (id, organization_id, conversation_id, direction, external_id, sender_id, sender_name, text,
			status, agent_id, hold_reason, sent_at)
		VALUES ($1, $2, $3, 'in', $4, $5, $6, $7, $8, $9, $10, $11)
		ON CONFLICT (conversation_id, external_id) DO NOTHING
		RETURNING `+columns,
		id, org, conv.ID, in.ExternalID, in.SenderID, in.SenderName, in.Text, status, target, hold, in.SentAt)
	if pgerr.IsNoRows(err) {
		_ = tx.Rollback()
		s, found, err := r.FindExternal(ctx, org, conv.ID, in.ExternalID)
		if err == nil && !found {
			err = pgerr.MapWith(pgerr.ErrNoRows, "find duplicate ship", messages)
		}
		return s, false, err
	}
	if err != nil {
		return s, false, pgerr.MapWith(err, "create inbound ship", messages)
	}
	for i, a := range attachments {
		if _, err := tx.ExecContext(ctx, `INSERT INTO ship_attachments
			(organization_id, id, ship_id, position, kind, mime, size, name, sha256, blob_key)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)`,
			org, a.ID, id, i, a.Kind, a.MIME, a.Size, a.Name, a.SHA256, a.Key); err != nil {
			return s, false, pgerr.Map(err, "store attachment")
		}
		a.Organization, a.Ship, a.Position = org, id, i
		s.Attachments = append(s.Attachments, a)
	}
	if len(attachments) > 0 {
		// Their blobs are in use now: out of the trash.
		if _, err := tx.ExecContext(ctx, `DELETE FROM blob_trash WHERE blob_key = ANY($1::text[])`, keysOf(attachments)); err != nil {
			return s, false, pgerr.Map(err, "keep attachment blobs")
		}
	}
	return s, true, pgerr.Map(tx.Commit(), "commit inbound ship")
}

func (r *Repository) CreateOutbound(ctx context.Context, org identity.OrganizationID, id identity.ShipID, in ship.Outbound,
	dock identity.DockID, externalID string, admit func(ship.OutboundCounts) error) (ship.Ship, error) {
	var s ship.Ship
	tx, err := r.db.BeginTxx(ctx, nil)
	if err != nil {
		return s, pgerr.Map(err, "begin outbound")
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock($1::int, hashtext($2::text))`, outboundLock, org); err != nil {
		return s, pgerr.Map(err, "lock outbound")
	}
	// Failed sends count too (conservative: most failures did reach the
	// channel, and an agent retrying in a loop must still be slowed down).
	var c struct {
		At          time.Time  `db:"at"`
		DockMinute  int        `db:"dock_minute"`
		DockHour    int        `db:"dock_hour"`
		ConvMinute  int        `db:"conv_minute"`
		OrgHour     int        `db:"org_hour"`
		OldDockMin  *time.Time `db:"old_dock_minute"`
		OldDockHour *time.Time `db:"old_dock_hour"`
		OldConvMin  *time.Time `db:"old_conv_minute"`
		OldOrgHour  *time.Time `db:"old_org_hour"`
	}
	err = tx.GetContext(ctx, &c, `
		SELECT now() AS at,
		       count(*) FILTER (WHERE c.dock_id = $2 AND s.created_at > now() - interval '1 minute') AS dock_minute,
		       count(*) FILTER (WHERE c.dock_id = $2) AS dock_hour,
		       count(*) FILTER (WHERE s.created_at > now() - interval '1 minute' AND s.conversation_id = $3) AS conv_minute,
		       count(*) AS org_hour,
		       min(s.created_at) FILTER (WHERE c.dock_id = $2 AND s.created_at > now() - interval '1 minute') AS old_dock_minute,
		       min(s.created_at) FILTER (WHERE c.dock_id = $2) AS old_dock_hour,
		       min(s.created_at) FILTER (WHERE s.created_at > now() - interval '1 minute' AND s.conversation_id = $3) AS old_conv_minute,
		       min(s.created_at) AS old_org_hour
		FROM ships s JOIN conversations c ON c.organization_id = s.organization_id AND c.id = s.conversation_id
		WHERE s.organization_id = $1 AND s.direction = 'out'
		  AND s.created_at > now() - interval '1 hour'`, org, dock, in.Conversation)
	if err != nil {
		return s, pgerr.Map(err, "count outbound")
	}
	counts := ship.OutboundCounts{At: c.At, DockLastMinute: c.DockMinute, DockLastHour: c.DockHour,
		ConversationLastMinute: c.ConvMinute, OrganizationLastHour: c.OrgHour}
	for dst, src := range map[*time.Time]*time.Time{&counts.OldestDockMinute: c.OldDockMin,
		&counts.OldestDockHour: c.OldDockHour, &counts.OldestConversationMinute: c.OldConvMin,
		&counts.OldestOrganizationHour: c.OldOrgHour} {
		if src != nil {
			*dst = *src
		}
	}
	if err := admit(counts); err != nil {
		return s, err
	}
	err = tx.GetContext(ctx, &s, `
		INSERT INTO ships (id, organization_id, conversation_id, direction, external_id, text, status, agent_id, sent_at)
		VALUES ($1, $5, $2, 'out', $3, $4, 'pending', $6, now())
		RETURNING `+columns, id, in.Conversation, externalID, in.Text, org, in.Agent)
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

// claimable limits an UPDATE of a conversation's queued ships of agent $5
// to those before the first one queued for another agent: a conversation
// is worked in arrival order, one agent at a time.
const claimable = `organization_id = $4 AND conversation_id = $1 AND status = 'queued' AND agent_id = $5
	AND NOT EXISTS (SELECT 1 FROM ships o WHERE o.organization_id = $4 AND o.conversation_id = $1
	                  AND o.status = 'queued' AND o.agent_id <> $5
	                  AND (o.created_at, o.id) < (ships.created_at, ships.id))`

func (r *Repository) Claim(ctx context.Context, org identity.OrganizationID, agentID identity.AgentID, delivery identity.DeliveryID, session string, until time.Time) (identity.ConversationID, []ship.Ship, error) {
	var conv identity.ConversationID
	tx, err := r.db.BeginTxx(ctx, nil)
	if err != nil {
		return conv, nil, pgerr.Map(err, "begin claim")
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock($1::int, hashtext($2::text))`, claimLock, org); err != nil {
		return conv, nil, pgerr.Map(err, "lock claims")
	}
	// The agent's oldest ship that is also its conversation's oldest
	// queued one, in a conversation with nothing in flight.
	err = tx.GetContext(ctx, &conv, `
		SELECT q.conversation_id FROM ships q
		WHERE q.organization_id = $1 AND q.status = 'queued' AND q.agent_id = $2
		  AND NOT EXISTS (SELECT 1 FROM ships e WHERE e.organization_id = q.organization_id
		                    AND e.conversation_id = q.conversation_id AND e.status = 'queued'
		                    AND (e.created_at, e.id) < (q.created_at, q.id))
		  AND NOT EXISTS (SELECT 1 FROM ships c WHERE c.organization_id = q.organization_id
		                    AND c.conversation_id = q.conversation_id AND c.status = 'claimed')
		ORDER BY q.created_at, q.id
		LIMIT 1`, org, agentID)
	if pgerr.IsNoRows(err) {
		return conv, nil, nil
	}
	if err != nil {
		return conv, nil, pgerr.Map(err, "pick conversation")
	}
	ships := []ship.Ship{}
	err = tx.SelectContext(ctx, &ships, `
		WITH claimed AS (
			UPDATE ships SET status = 'claimed', delivery_id = $2, claimed_until = $3, claimed_by = $6
			WHERE `+claimable+`
			RETURNING `+columns+`)
		SELECT * FROM claimed ORDER BY sent_at, created_at, id`, conv, delivery, until, org, agentID, session)
	if err != nil {
		return conv, nil, pgerr.Map(err, "claim ships")
	}
	if err := r.attach(ctx, tx, org, ships); err != nil {
		return conv, nil, err
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
	var open struct {
		Conversation identity.ConversationID `db:"conversation_id"`
		Session      string                  `db:"claimed_by"`
		Agent        identity.AgentID        `db:"agent_id"`
	}
	err = tx.GetContext(ctx, &open, `SELECT conversation_id, claimed_by, agent_id FROM ships
		WHERE organization_id = $1 AND delivery_id = $2 AND status = 'claimed' LIMIT 1`, org, delivery)
	conv = open.Conversation
	if pgerr.IsNoRows(err) {
		return conv, nil, nil // closed (acked, released or expired)
	}
	if err != nil {
		return conv, nil, pgerr.Map(err, "find delivery")
	}
	ships := []ship.Ship{}
	err = tx.SelectContext(ctx, &ships, `
		WITH joined AS (
			UPDATE ships SET status = 'claimed', delivery_id = $2, claimed_until = $3, claimed_by = $6
			WHERE `+claimable+`
			RETURNING `+columns+`)
		SELECT * FROM joined ORDER BY sent_at, created_at, id`, conv, delivery, until, org, open.Agent, open.Session)
	if err != nil {
		return conv, nil, pgerr.Map(err, "join ships")
	}
	if err := r.attach(ctx, tx, org, ships); err != nil {
		return conv, nil, err
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
	res, err := tx.ExecContext(ctx, `UPDATE ships SET status = 'handled', claimed_until = NULL, claimed_by = NULL
		WHERE delivery_id = $1 AND status = 'claimed' AND id = ANY($2::uuid[]) AND organization_id = $3`, delivery, ids, org)
	if err != nil {
		return 0, pgerr.Map(err, "ack ships")
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return 0, pgerr.MapWith(pgerr.ErrNoRows, "ack delivery", pgerr.Messages{NotFound: "delivery not found or no longer claimed"})
	}
	// Ships joined after the agent's turn ended were never seen: requeue.
	res, err = tx.ExecContext(ctx, `UPDATE ships SET status = 'queued', delivery_id = NULL, claimed_until = NULL, claimed_by = NULL
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
		`UPDATE ships SET status = 'queued', delivery_id = NULL, claimed_until = NULL, claimed_by = NULL
		 WHERE delivery_id = $1 AND status = 'claimed' AND organization_id = $2`, delivery, org)
}

func (r *Repository) ReleaseSession(ctx context.Context, org identity.OrganizationID, session string) (int, error) {
	res, err := r.db.ExecContext(ctx, `UPDATE ships SET status = 'queued', delivery_id = NULL, claimed_until = NULL, claimed_by = NULL
		WHERE organization_id = $1 AND claimed_by = $2 AND status = 'claimed'`, org, session)
	if err != nil {
		return 0, pgerr.Map(err, "release session")
	}
	n, _ := res.RowsAffected()
	return int(n), nil
}

func (r *Repository) ReleaseHeld(ctx context.Context, org identity.OrganizationID, convID identity.ConversationID, since time.Time,
	route func(team []agent.Member, text string, previous identity.AgentID) (identity.AgentID, string)) ([]ship.Ship, error) {
	tx, err := r.db.BeginTxx(ctx, nil)
	if err != nil {
		return nil, pgerr.Map(err, "begin release held")
	}
	defer func() { _ = tx.Rollback() }()
	// The conversation must still be an allowed direct chat (FOR SHARE: a
	// status change waits for this release, so a chat denied meanwhile
	// releases nothing).
	var conv struct {
		Dock   identity.DockID     `db:"dock_id"`
		Kind   conversation.Kind   `db:"kind"`
		Status conversation.Status `db:"status"`
	}
	err = tx.GetContext(ctx, &conv, `SELECT dock_id, kind, status FROM conversations
		WHERE organization_id = $1 AND id = $2 FOR SHARE`, org, convID)
	if pgerr.IsNoRows(err) {
		return nil, nil
	}
	if err != nil {
		return nil, pgerr.Map(err, "find released conversation")
	}
	if conv.Status != conversation.StatusAllowed || conv.Kind != conversation.KindDirect {
		return nil, nil
	}
	held := []ship.Ship{}
	if err := tx.SelectContext(ctx, &held, `SELECT `+columns+` FROM ships
		WHERE organization_id = $1 AND conversation_id = $2 AND direction = 'in' AND status = 'held'
		  AND hold_reason = $3 AND created_at >= $4
		ORDER BY created_at DESC, id DESC LIMIT $5 FOR UPDATE`,
		org, convID, conversation.HoldPending, since, ship.MaxRelease); err != nil {
		return nil, pgerr.Map(err, "held ships")
	}
	if len(held) == 0 {
		return nil, nil
	}
	// As in CreateInbound: the team stays locked until commit.
	team := []agent.Member{}
	if err := tx.SelectContext(ctx, &team, `SELECT d.agent_id, a.name, d.is_default FROM dock_agents d
		JOIN agents a ON a.organization_id = d.organization_id AND a.id = d.agent_id
		WHERE d.organization_id = $1 AND d.dock_id = $2 ORDER BY d.created_at, a.name FOR SHARE OF d`,
		org, conv.Dock); err != nil {
		return nil, pgerr.Map(err, "dock team")
	}
	var previous identity.AgentID
	err = tx.GetContext(ctx, &previous, `SELECT agent_id FROM ships
		WHERE organization_id = $1 AND conversation_id = $2 AND agent_id IS NOT NULL
		ORDER BY created_at DESC LIMIT 1`, org, convID)
	if err != nil && !pgerr.IsNoRows(err) {
		return nil, pgerr.Map(err, "previous agent")
	}
	out := make([]ship.Ship, 0, len(held))
	for i := len(held) - 1; i >= 0; i-- { // oldest first
		s := held[i]
		picked, reason := route(team, s.Text, previous)
		var updated ship.Ship
		if picked.IsZero() {
			err = tx.GetContext(ctx, &updated, `UPDATE ships SET hold_reason = $3
				WHERE organization_id = $1 AND id = $2 RETURNING `+columns, org, s.ID, reason)
		} else {
			previous = picked
			err = tx.GetContext(ctx, &updated, `UPDATE ships SET status = 'queued', agent_id = $3, hold_reason = ''
				WHERE organization_id = $1 AND id = $2 RETURNING `+columns, org, s.ID, picked)
		}
		if err != nil {
			return nil, pgerr.Map(err, "release held ship")
		}
		out = append(out, updated)
	}
	if err := r.attach(ctx, tx, org, out); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, pgerr.Map(err, "commit release held")
	}
	return out, nil
}

func (r *Repository) ReleaseExpired(ctx context.Context, before time.Time) (map[identity.OrganizationID]int, error) {
	var rows []struct {
		Organization identity.OrganizationID `db:"organization_id"`
		N            int                     `db:"n"`
	}
	err := r.db.SelectContext(ctx, &rows, `
		WITH released AS (
			UPDATE ships SET status = 'queued', delivery_id = NULL, claimed_until = NULL, claimed_by = NULL
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
	if err != nil {
		return out, pgerr.Map(err, "list ships")
	}
	return out, r.attach(ctx, r.db, org, out.Items)
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
	if err := r.attach(ctx, r.db, org, last); err != nil {
		return nil, err
	}
	for i := range last {
		s := out[last[i].Conversation]
		s.Last = &last[i]
		out[last[i].Conversation] = s
	}
	return out, nil
}

// attach fills the ships' attachments (one query for all of them).
func (r *Repository) FindExternal(ctx context.Context, org identity.OrganizationID, conv identity.ConversationID, externalID string) (ship.Ship, bool, error) {
	var s ship.Ship
	err := r.db.GetContext(ctx, &s, `SELECT `+columns+` FROM ships
		WHERE organization_id = $1 AND conversation_id = $2 AND external_id = $3`, org, conv, externalID)
	if pgerr.IsNoRows(err) {
		return s, false, nil
	}
	if err != nil {
		return s, false, pgerr.MapWith(err, "find ship by channel id", messages)
	}
	one := []ship.Ship{s}
	if err := r.attach(ctx, r.db, org, one); err != nil {
		return s, false, err
	}
	return one[0], true, nil
}

func (r *Repository) attach(ctx context.Context, q sqlx.QueryerContext, org identity.OrganizationID, ships []ship.Ship) error {
	if len(ships) == 0 {
		return nil
	}
	ids := make([]string, len(ships))
	at := make(map[identity.ShipID]int, len(ships))
	for i, s := range ships {
		ids[i], at[s.ID] = s.ID.String(), i
	}
	rows := []ship.Attachment{}
	if err := sqlx.SelectContext(ctx, q, &rows, `SELECT `+attachmentColumns+` FROM ship_attachments
		WHERE organization_id = $1 AND ship_id = ANY($2::uuid[]) ORDER BY ship_id, position`, org, ids); err != nil {
		return pgerr.Map(err, "ship attachments")
	}
	for _, a := range rows {
		if i, ok := at[a.Ship]; ok {
			ships[i].Attachments = append(ships[i].Attachments, a)
		}
	}
	return nil
}

func keysOf(attachments []ship.Attachment) []string {
	out := make([]string, len(attachments))
	for i, a := range attachments {
		out[i] = a.Key
	}
	return out
}

func (r *Repository) Attachment(ctx context.Context, org identity.OrganizationID, id identity.AttachmentID) (ship.Attachment, identity.ConversationID, error) {
	var row struct {
		ship.Attachment
		Conversation identity.ConversationID `db:"conversation_id"`
	}
	err := r.db.GetContext(ctx, &row, `SELECT a.organization_id, a.id, a.ship_id, a.position, a.kind, a.mime, a.size, a.name,
			a.sha256, a.blob_key, s.conversation_id
		FROM ship_attachments a JOIN ships s ON s.organization_id = a.organization_id AND s.id = a.ship_id
		WHERE a.organization_id = $1 AND a.id = $2`, org, id)
	return row.Attachment, row.Conversation, pgerr.MapWith(err, "find attachment", pgerr.Messages{NotFound: "attachment not found"})
}

func (r *Repository) StageBlobs(ctx context.Context, org identity.OrganizationID, keys []string, due time.Time) error {
	_, err := r.db.ExecContext(ctx, `INSERT INTO blob_trash (blob_key, organization_id, due_at)
		SELECT k, $1, $3 FROM unnest($2::text[]) AS k
		ON CONFLICT (blob_key) DO UPDATE SET due_at = excluded.due_at`, org, keys, due)
	return pgerr.Map(err, "stage blobs")
}

func (r *Repository) DueTrash(ctx context.Context, now time.Time, limit int) ([]string, error) {
	keys := []string{}
	err := r.db.SelectContext(ctx, &keys, `SELECT blob_key FROM blob_trash WHERE due_at <= $1
		ORDER BY due_at LIMIT $2`, now, limit)
	return keys, pgerr.Map(err, "due blobs")
}

// DropTrash forgets deleted blobs; a key staged again meanwhile (due
// later than now) stays.
func (r *Repository) DropTrash(ctx context.Context, keys []string, now time.Time) error {
	_, err := r.db.ExecContext(ctx, `DELETE FROM blob_trash WHERE blob_key = ANY($1::text[]) AND due_at <= $2`, keys, now)
	return pgerr.Map(err, "drop deleted blobs")
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
