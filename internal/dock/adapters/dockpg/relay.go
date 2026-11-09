package dockpg

import (
	"context"
	"encoding/json"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Abraxas-365/seaport/internal/dock"
	"github.com/Abraxas-365/seaport/internal/errx"
	"github.com/Abraxas-365/seaport/internal/identity"
	"github.com/Abraxas-365/seaport/internal/pgerr"
	"github.com/Abraxas-365/seaport/internal/pglisten"
	"github.com/jmoiron/sqlx"
)

const (
	// relayChannel carries "r:<process>" (requests for process) and
	// "d:<id>" (request id answered).
	relayChannel = "seaport_docks"
	// relayPoll is how often a caller rechecks its request and a server
	// looks for requests without a notification (lost ones, no listener).
	relayPoll = 500 * time.Millisecond
	// relayDeadline bounds a request whose caller set no deadline.
	relayDeadline = 2 * time.Minute
	// relayKeep is how long a request is kept after its deadline (its
	// caller is gone) before the sweep deletes it.
	relayKeep = 5 * time.Minute
	// relayBatch is how many requests a server takes at once.
	relayBatch = 20
)

// Relay hands dock work to its owner through dock_requests (migration
// 00011) and NOTIFY seaport_docks. Callers also poll, so a lost
// notification only delays.
type Relay struct {
	db *sqlx.DB

	mu      sync.Mutex
	waiters map[int64]chan struct{}
}

func NewRelay(db *sqlx.DB) *Relay { return &Relay{db: db, waiters: map[int64]chan struct{}{}} }

var _ dock.Relay = (*Relay)(nil)

func (r *Relay) Call(ctx context.Context, org identity.OrganizationID, process string, req dock.Request) (dock.Reply, error) {
	if err := req.Validate(); err != nil {
		return dock.Reply{}, err
	}
	raw, err := json.Marshal(req)
	if err != nil {
		return dock.Reply{}, errx.Wrap(err, "encode dock request", errx.TypeInternal)
	}
	// The caller waits until the deadline at most (the owner may die
	// mid-request); its process gets the same deadline for the work.
	ctx, cancel := context.WithTimeout(ctx, relayDeadline)
	defer cancel()
	deadline, _ := ctx.Deadline()
	id, err := r.insert(ctx, org, req.Dock, process, raw, deadline)
	if err != nil {
		return dock.Reply{}, err
	}
	wake := make(chan struct{}, 1)
	r.mu.Lock()
	r.waiters[id] = wake
	r.mu.Unlock()
	defer func() {
		r.mu.Lock()
		delete(r.waiters, id)
		r.mu.Unlock()
	}()
	tick := time.NewTicker(relayPoll)
	defer tick.Stop()
	for {
		reply, done, err := r.check(ctx, id, req.Dock, process)
		if err != nil || done {
			return reply, err
		}
		select {
		case <-ctx.Done():
			r.forget(id)
			// Running: the work may have happened (a send is not retried).
			return dock.Reply{}, errx.Wrap(ctx.Err(), "the dock's process did not answer in time", errx.TypeExternal)
		case <-wake:
		case <-tick.C:
		}
	}
}

func (r *Relay) insert(ctx context.Context, org identity.OrganizationID, d identity.DockID, process string, raw []byte, deadline time.Time) (int64, error) {
	tx, err := r.db.BeginTxx(ctx, nil)
	if err != nil {
		return 0, pgerr.Map(err, "begin dock request")
	}
	defer func() { _ = tx.Rollback() }()
	var id int64
	if err := tx.GetContext(ctx, &id, `INSERT INTO dock_requests (organization_id, dock_id, process, request, deadline)
		VALUES ($1, $2, $3, $4, $5) RETURNING id`, org, d, process, raw, deadline); err != nil {
		return 0, pgerr.MapWith(err, "store dock request", ownerMessages)
	}
	if _, err := tx.ExecContext(ctx, `SELECT pg_notify($1, $2)`, relayChannel, "r:"+process); err != nil {
		return 0, pgerr.Map(err, "notify dock request")
	}
	return id, pgerr.Map(tx.Commit(), "commit dock request")
}

// check reads the request: answered (deleted, reply returned), or still
// pending while its process lost the dock (withdrawn, Moved).
func (r *Relay) check(ctx context.Context, id int64, d identity.DockID, process string) (dock.Reply, bool, error) {
	var row struct {
		Status string  `db:"status"`
		Reply  *[]byte `db:"reply"`
		Owned  bool    `db:"owned"`
	}
	err := r.db.GetContext(ctx, &row, `SELECT q.status, q.reply, EXISTS (SELECT 1 FROM dock_owners o
			WHERE o.dock_id = q.dock_id AND o.process = q.process AND o.expires_at > now()) AS owned
		FROM dock_requests q WHERE q.id = $1`, id)
	if pgerr.IsNoRows(err) {
		// The dock was deleted (cascade), or the request expired.
		return dock.Reply{}, true, errx.External("the dock request was dropped (dock deleted, or request expired)")
	}
	if err != nil {
		return dock.Reply{}, false, pgerr.Map(err, "read dock request")
	}
	switch {
	case row.Status == "done":
		r.forget(id)
		var reply dock.Reply
		if row.Reply != nil {
			if err := json.Unmarshal(*row.Reply, &reply); err != nil {
				return dock.Reply{}, true, errx.Wrap(err, "decode dock reply", errx.TypeInternal)
			}
		}
		return reply, true, nil
	case row.Status == "pending" && !row.Owned:
		res, err := r.db.ExecContext(ctx, `DELETE FROM dock_requests WHERE id = $1 AND status = 'pending'`, id)
		if err != nil {
			return dock.Reply{}, false, pgerr.Map(err, "withdraw dock request")
		}
		if n, _ := res.RowsAffected(); n == 1 {
			slog.Info("dock request withdrawn: its process lost the dock", "dock", d, "process", process)
			return dock.Reply{}, true, dock.Moved()
		}
	}
	// Pending at a live owner, or running (an owner that lost the lease
	// mid-work still answers; one that died lets ctx end the wait).
	return dock.Reply{}, false, nil
}

func (r *Relay) forget(id int64) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := r.db.ExecContext(ctx, `DELETE FROM dock_requests WHERE id = $1`, id); err != nil {
		slog.Warn("delete dock request", "id", id, "err", err)
	}
}

type taken struct {
	ID           int64                   `db:"id"`
	Organization identity.OrganizationID `db:"organization_id"`
	Request      []byte                  `db:"request"`
	Deadline     time.Time               `db:"deadline"`
}

func (r *Relay) Serve(ctx context.Context, process string, handle func(context.Context, identity.OrganizationID, dock.Request) dock.Reply) {
	kick := make(chan struct{}, 1)
	poke := func() {
		select {
		case kick <- struct{}{}:
		default:
		}
	}
	var wg sync.WaitGroup
	defer wg.Wait()
	wg.Add(1)
	go func() {
		defer wg.Done()
		pglisten.Listen(ctx, r.db.DB, relayChannel, func() {
			poke()
			r.wakeAll()
		}, func(payload string) {
			switch {
			case payload == "r:"+process:
				poke()
			case strings.HasPrefix(payload, "d:"):
				if id, err := strconv.ParseInt(payload[2:], 10, 64); err == nil {
					r.wake(id)
				}
			}
		})
	}()
	tick := time.NewTicker(relayPoll)
	defer tick.Stop()
	sweep := time.NewTicker(time.Minute)
	defer sweep.Stop()
	for {
		for _, t := range r.take(ctx, process) { // what is taken always runs
			wg.Add(1)
			go func() {
				defer wg.Done()
				r.run(ctx, t, handle)
			}()
		}
		select {
		case <-ctx.Done():
			return
		case <-kick:
		case <-tick.C:
		case <-sweep.C:
			if _, err := r.db.ExecContext(ctx, `DELETE FROM dock_requests WHERE deadline < now() - make_interval(secs => $1)`,
				relayKeep.Seconds()); err != nil && ctx.Err() == nil {
				slog.Warn("sweep dock requests", "err", err)
			}
		}
	}
}

func (r *Relay) take(ctx context.Context, process string) []taken {
	if ctx.Err() != nil {
		return nil
	}
	// Not cancelled by a stopping server: rows marked running must reach run.
	tctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	var out []taken
	err := r.db.SelectContext(tctx, &out, `UPDATE dock_requests SET status = 'running'
		WHERE id IN (SELECT id FROM dock_requests WHERE process = $1 AND status = 'pending' AND deadline > now()
			ORDER BY id LIMIT $2 FOR UPDATE SKIP LOCKED)
		RETURNING id, organization_id, request, deadline`, process, relayBatch)
	if err != nil {
		slog.Warn("take dock requests", "err", err)
	}
	return out
}

func (r *Relay) run(ctx context.Context, t taken, handle func(context.Context, identity.OrganizationID, dock.Request) dock.Reply) {
	var reply dock.Reply
	var req dock.Request
	if err := json.Unmarshal(t.Request, &req); err != nil {
		reply.Error = errx.Internal("bad dock request")
	} else if err := req.Validate(); err != nil {
		reply.Error = errx.Internal("bad dock request")
	} else {
		// The work outlives a stopping server (its caller waits for it),
		// up to the caller's deadline.
		workCtx, cancel := context.WithDeadline(context.WithoutCancel(ctx), t.Deadline)
		reply = handle(workCtx, t.Organization, req)
		cancel()
	}
	raw, err := json.Marshal(reply)
	if err != nil {
		raw, _ = json.Marshal(dock.Reply{Error: errx.Internal("dock reply does not encode")})
	}
	answer, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	tx, err := r.db.BeginTxx(answer, nil)
	if err == nil {
		defer func() { _ = tx.Rollback() }()
		if _, err = tx.ExecContext(answer, `UPDATE dock_requests SET status = 'done', reply = $2 WHERE id = $1`, t.ID, raw); err == nil {
			if _, err = tx.ExecContext(answer, `SELECT pg_notify($1, $2)`, relayChannel, "d:"+strconv.FormatInt(t.ID, 10)); err == nil {
				err = tx.Commit()
			}
		}
	}
	if err != nil {
		slog.Error("answer dock request", "id", t.ID, "dock", req.Dock, "err", err)
	}
}

func (r *Relay) wake(id int64) {
	r.mu.Lock()
	w := r.waiters[id]
	r.mu.Unlock()
	if w != nil {
		select {
		case w <- struct{}{}:
		default:
		}
	}
}

func (r *Relay) wakeAll() {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, w := range r.waiters {
		select {
		case w <- struct{}{}:
		default:
		}
	}
}
