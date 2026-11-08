// Package pglisten holds a Postgres LISTEN on a dedicated connection of a
// database/sql pool (pgx driver), reconnecting when it breaks.
package pglisten

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"fmt"
	"log/slog"
	"time"

	"github.com/Abraxas-365/seaport/internal/errx"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
)

// retry is the pause before listening again after the connection broke.
const retry = time.Second

// Listen calls fn with the payload of every notification on channel, and
// connected after each (re)connect (notifications may have been missed
// meanwhile), until ctx ends. The connection is taken from db and never
// returned to it. A silently dead connection may block it for long:
// callers that can't miss anything also poll.
func Listen(ctx context.Context, db *sql.DB, channel string, connected func(), fn func(payload string)) {
	for ctx.Err() == nil {
		err := listenOnce(ctx, db, channel, connected, fn)
		if ctx.Err() != nil {
			return
		}
		slog.Warn("listener reconnecting", "channel", channel, "err", err)
		select {
		case <-ctx.Done():
			return
		case <-time.After(retry):
		}
	}
}

func listenOnce(ctx context.Context, db *sql.DB, channel string, connected func(), fn func(string)) error {
	conn, err := db.Conn(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = conn.Close() }()
	return conn.Raw(func(driverConn any) error {
		sc, ok := driverConn.(*stdlib.Conn)
		if !ok {
			return errx.Internal("listening needs the pgx driver")
		}
		pc := sc.Conn()
		// Never hand a listening (or interrupted) connection back to the
		// pool: an error wrapping ErrBadConn makes database/sql discard it.
		if _, err := pc.Exec(ctx, "LISTEN "+pgx.Identifier{channel}.Sanitize()); err != nil {
			return fmt.Errorf("listen: %w (%w)", err, driver.ErrBadConn)
		}
		connected()
		for {
			n, err := pc.WaitForNotification(ctx)
			if err != nil {
				return fmt.Errorf("wait for notification: %w (%w)", err, driver.ErrBadConn)
			}
			fn(n.Payload)
		}
	})
}
