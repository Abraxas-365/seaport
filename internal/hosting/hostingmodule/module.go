// Package hostingmodule wires the hosting adapters to its service.
package hostingmodule

import (
	"context"
	"net/http"

	"github.com/Abraxas-365/seaport/internal/agent"
	"github.com/Abraxas-365/seaport/internal/event"
	"github.com/Abraxas-365/seaport/internal/hosting"
	"github.com/Abraxas-365/seaport/internal/hosting/adapters/hostingdocker"
	"github.com/Abraxas-365/seaport/internal/hosting/adapters/hostinghttp"
	"github.com/Abraxas-365/seaport/internal/hosting/adapters/hostingpg"
	"github.com/Abraxas-365/seaport/internal/hosting/hostingsvc"
	"github.com/jmoiron/sqlx"
)

type Module struct {
	Service *hostingsvc.Service
	http    *hostinghttp.Handler
	docker  *hostingdocker.Runtime
}

// Config is what the module needs besides the database.
type Config struct {
	Limits hosting.Limits
	// Docker connects the Docker runtime (SEAPORT_RUNTIME=docker; the
	// engine at DOCKER_HOST must answer). Runtime, when set, is used
	// instead (tests). Neither: hosted agents are off.
	Docker  bool
	Runtime hosting.Runtime
	// Bases maps a harness to its base image.
	Bases map[string]string
}

// New builds the hosting module over the agent library (its secrets'
// names).
func New(ctx context.Context, db *sqlx.DB, agents agent.Queries, cfg Config, events event.Publisher) (*Module, error) {
	m := &Module{}
	runtime := cfg.Runtime
	if runtime == nil && cfg.Docker {
		d, err := hostingdocker.New(ctx)
		if err != nil {
			return nil, err
		}
		m.docker, runtime = d, d
	}
	m.Service = hostingsvc.New(hostingpg.New(db), agents, cfg.Limits, events,
		hostingsvc.Builder{Runtime: runtime, Bases: cfg.Bases})
	m.http = hostinghttp.New(m.Service, m.Service)
	return m, nil
}

// Register mounts /api/v1/environments, /api/v1/hosting and
// /api/v1/agents/{id}/hosting.
func (m *Module) Register(mux *http.ServeMux) { m.http.Register(mux) }

// Close cancels the builds in progress (waiting for them to record it) and
// closes the runtime's connection.
func (m *Module) Close() {
	m.Service.Close()
	if m.docker != nil {
		_ = m.docker.Close()
	}
}
