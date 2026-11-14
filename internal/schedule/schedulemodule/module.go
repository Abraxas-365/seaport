// Package schedulemodule wires the schedule adapters to its service and
// owns the runner's lifetime.
package schedulemodule

import (
	"context"
	"sync"
	"time"

	"github.com/Abraxas-365/seaport/internal/agent"
	"github.com/Abraxas-365/seaport/internal/conversation"
	"github.com/Abraxas-365/seaport/internal/event"
	"github.com/Abraxas-365/seaport/internal/httpx"
	"github.com/Abraxas-365/seaport/internal/schedule"
	"github.com/Abraxas-365/seaport/internal/schedule/adapters/schedulehttp"
	"github.com/Abraxas-365/seaport/internal/schedule/adapters/schedulepg"
	"github.com/Abraxas-365/seaport/internal/schedule/schedulesvc"
	"github.com/jmoiron/sqlx"
)

type Module struct {
	Service *schedulesvc.Service
	http    *schedulehttp.Handler

	mu     sync.Mutex
	cancel context.CancelFunc
	wg     sync.WaitGroup
}

// New builds the module; max bounds an organization's schedules (≤ 0:
// unlimited).
func New(db *sqlx.DB, convs conversation.Queries, agents agent.Queries, events event.Publisher, max int) *Module {
	svc := schedulesvc.New(schedulepg.New(db), convs, agents, events, max)
	return &Module{Service: svc, http: schedulehttp.New(svc, svc)}
}

// Bind hands the ship service in (built after this module).
func (m *Module) Bind(d schedule.Deliverer) { m.Service.Bind(d) }

// Register mounts /api/v1/schedules and /api/v1/conversations/{id}/schedules.
func (m *Module) Register(r httpx.Router) { m.http.Register(r) }

// Start runs the runner every period until ctx ends or Close.
func (m *Module) Start(ctx context.Context, period time.Duration) {
	m.mu.Lock()
	defer m.mu.Unlock()
	ctx, m.cancel = context.WithCancel(ctx)
	m.wg.Add(1)
	go func() {
		defer m.wg.Done()
		m.Service.Runner(ctx, period)
	}()
}

// Close stops the runner and waits for the round in progress.
func (m *Module) Close() {
	m.mu.Lock()
	cancel := m.cancel
	m.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	m.wg.Wait()
}
