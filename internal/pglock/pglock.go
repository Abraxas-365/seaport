// Package pglock takes Postgres advisory locks that span several
// statements and processes, each on its own connection of a pool kept
// apart from the one the work runs on (a holder waiting for a work
// connection can't starve the pool it holds a lock connection of).
package pglock

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"log/slog"
	"strconv"
	"sync"
	"time"

	"github.com/Abraxas-365/seaport/internal/pgerr"
)

// unlockTimeout bounds giving a lock back (the caller's context may be
// done by then).
const unlockTimeout = 5 * time.Second

type Locker struct {
	db *sql.DB

	// local queues this process's waiters of one key before they take a
	// connection, so a busy key holds one lock connection, not the pool.
	mu    sync.Mutex
	local map[string]*waiters
}

type waiters struct {
	turn chan struct{} // capacity 1: holding it is this process's turn
	refs int
}

// New takes locks on db's connections; db must be a pool of its own, with
// no statement_timeout (waiting for a lock is a statement).
func New(db *sql.DB) *Locker { return &Locker{db: db, local: map[string]*waiters{}} }

// Lock waits for the advisory lock (class, key) — key is hashed, like
// the transaction locks elsewhere — and returns its release. Until then
// every other holder of (class, key), in any process, waits.
func (l *Locker) Lock(ctx context.Context, class int32, key string) (unlock func(), err error) {
	local, err := l.localTurn(ctx, strconv.Itoa(int(class))+":"+key)
	if err != nil {
		return nil, err
	}
	conn, err := l.db.Conn(ctx)
	if err != nil {
		local()
		return nil, pgerr.Map(err, "lock connection")
	}
	if _, err := conn.ExecContext(ctx, `SELECT pg_advisory_lock($1::int, hashtext($2::text))`, class, key); err != nil {
		discard(conn)
		local()
		return nil, pgerr.Map(err, "take lock")
	}
	return func() {
		defer local()
		uctx, cancel := context.WithTimeout(context.Background(), unlockTimeout)
		defer cancel()
		var released bool
		err := conn.QueryRowContext(uctx, `SELECT pg_advisory_unlock($1::int, hashtext($2::text))`, class, key).Scan(&released)
		if err != nil || !released {
			// Never return a connection that may still hold the lock:
			// closing the session releases it.
			slog.Warn("release lock", "class", class, "released", released, "err", err)
			discard(conn)
			return
		}
		_ = conn.Close()
	}, nil
}

// localTurn waits for this process's turn on k.
func (l *Locker) localTurn(ctx context.Context, k string) (release func(), err error) {
	l.mu.Lock()
	w, ok := l.local[k]
	if !ok {
		w = &waiters{turn: make(chan struct{}, 1)}
		l.local[k] = w
	}
	w.refs++
	l.mu.Unlock()
	done := func() {
		l.mu.Lock()
		if w.refs--; w.refs == 0 {
			delete(l.local, k)
		}
		l.mu.Unlock()
	}
	select {
	case w.turn <- struct{}{}:
		return func() { <-w.turn; done() }, nil
	case <-ctx.Done():
		done()
		return nil, pgerr.Map(ctx.Err(), "wait for lock")
	}
}

// discard closes conn's session instead of returning it to the pool.
func discard(conn *sql.Conn) {
	_ = conn.Raw(func(any) error { return driver.ErrBadConn })
	_ = conn.Close()
}
