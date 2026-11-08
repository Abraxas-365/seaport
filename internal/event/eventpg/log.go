// Package eventpg is the Postgres event log: appends are serialized (an
// advisory lock held to commit, so ids become visible in id order and a
// reader that remembers the last id never skips one) and announced with
// NOTIFY; every process LISTENs (pglisten). Payloads are
// stored in clear: publishers keep secrets (pairing QRs) out of events.
package eventpg

import (
	"context"
	"encoding/json"
	"strconv"
	"time"

	"github.com/Abraxas-365/seaport/internal/event"
	"github.com/Abraxas-365/seaport/internal/identity"
	"github.com/Abraxas-365/seaport/internal/pgerr"
	"github.com/Abraxas-365/seaport/internal/pglisten"
	"github.com/jmoiron/sqlx"
)

// appendLock serializes appends (and Bounds) across processes.
const appendLock = 7_401_005

const channel = "seaport_events"

// sweepBatch bounds one retention DELETE (statement_timeout).
const sweepBatch = 5000

type Log struct{ db *sqlx.DB }

func New(db *sqlx.DB) *Log { return &Log{db: db} }

var _ event.Log = (*Log)(nil)

// Append is one statement (its own transaction): the lock is taken before
// the id is drawn (the row comes from the lock's CTE) and held to commit.
func (l *Log) Append(ctx context.Context, org identity.OrganizationID, typ string, data json.RawMessage) error {
	_, err := l.db.ExecContext(ctx, `
		WITH locked AS (SELECT pg_advisory_xact_lock($1)),
		     added AS (INSERT INTO events (organization_id, type, data)
		               SELECT $2, $3, $4 FROM locked RETURNING id)
		SELECT pg_notify($5, '') FROM added`, appendLock, org, typ, string(data), channel)
	return pgerr.Map(err, "append event")
}

type row struct {
	Seq          int64                   `db:"id"`
	Organization identity.OrganizationID `db:"organization_id"`
	Type         string                  `db:"type"`
	Data         string                  `db:"data"`
	At           time.Time               `db:"at"`
}

func (l *Log) Read(ctx context.Context, after, through int64, org *identity.OrganizationID, limit int) ([]event.Event, error) {
	var rows []row
	var err error
	if org == nil {
		err = l.db.SelectContext(ctx, &rows, `SELECT id, organization_id, type, data, at FROM events
			WHERE id > $1 AND id <= $2 ORDER BY id LIMIT $3`, after, through, limit)
	} else {
		err = l.db.SelectContext(ctx, &rows, `SELECT id, organization_id, type, data, at FROM events
			WHERE organization_id = $4 AND id > $1 AND id <= $2 ORDER BY id LIMIT $3`, after, through, limit, *org)
	}
	if err != nil {
		return nil, pgerr.Map(err, "read events")
	}
	out := make([]event.Event, len(rows))
	for i, r := range rows {
		out[i] = event.Event{ID: itoa(r.Seq), Seq: r.Seq, Organization: r.Organization, Type: r.Type,
			At: r.At.UTC(), Data: json.RawMessage(r.Data)}
	}
	return out, nil
}

// issuedSQL is the sequence's last value: every id drawn so far, committed
// or not (rolled back: a gap), is at most this.
const issuedSQL = `SELECT CASE WHEN is_called THEN last_value ELSE 0 END FROM events_id_seq`

// Head takes the append lock, so no append is in flight: every id up to
// the sequence's last value is committed or never will be.
func (l *Log) Head(ctx context.Context) (int64, error) {
	tx, err := l.db.BeginTxx(ctx, nil)
	if err != nil {
		return 0, pgerr.Map(err, "begin event head")
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock($1)`, appendLock); err != nil {
		return 0, pgerr.Map(err, "lock events")
	}
	var head int64
	if err := tx.GetContext(ctx, &head, issuedSQL); err != nil {
		return 0, pgerr.Map(err, "event head")
	}
	return head, nil
}

// Bounds doesn't wait for appends (clients call it on every reconnect).
func (l *Log) Bounds(ctx context.Context) (int64, int64, error) {
	var b struct {
		Issued int64  `db:"issued"`
		Oldest *int64 `db:"oldest"`
	}
	if err := l.db.GetContext(ctx, &b, `SELECT (`+issuedSQL+`) AS issued,
		(SELECT min(id) FROM events) AS oldest`); err != nil {
		return 0, 0, pgerr.Map(err, "event bounds")
	}
	if b.Oldest == nil {
		return b.Issued, b.Issued + 1, nil
	}
	return b.Issued, *b.Oldest, nil
}

// Sweep deletes in batches (every replica sweeps; a backlog after an
// outage stays under statement_timeout).
func (l *Log) Sweep(ctx context.Context, before time.Time) (int64, error) {
	var total int64
	for {
		res, err := l.db.ExecContext(ctx, `DELETE FROM events WHERE id IN
			(SELECT id FROM events WHERE at < $1 ORDER BY id LIMIT $2)`, before, sweepBatch)
		if err != nil {
			return total, pgerr.Map(err, "sweep events")
		}
		n, _ := res.RowsAffected()
		total += n
		if n < sweepBatch {
			return total, nil
		}
	}
}

// Listen wakes on every append of any process (and after reconnecting).
func (l *Log) Listen(ctx context.Context, wake func()) {
	pglisten.Listen(ctx, l.db.DB, channel, wake, func(string) { wake() })
}

func itoa(n int64) string { return strconv.FormatInt(n, 10) }
