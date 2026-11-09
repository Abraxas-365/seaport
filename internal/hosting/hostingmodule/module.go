// Package hostingmodule wires the hosting adapters to its service.
package hostingmodule

import (
	"net/http"

	"github.com/Abraxas-365/seaport/internal/agent"
	"github.com/Abraxas-365/seaport/internal/event"
	"github.com/Abraxas-365/seaport/internal/hosting"
	"github.com/Abraxas-365/seaport/internal/hosting/adapters/hostinghttp"
	"github.com/Abraxas-365/seaport/internal/hosting/adapters/hostingpg"
	"github.com/Abraxas-365/seaport/internal/hosting/hostingsvc"
	"github.com/jmoiron/sqlx"
)

type Module struct {
	Service *hostingsvc.Service
	http    *hostinghttp.Handler
}

// New builds the hosting module over the agent library (its secrets'
// names) with the operator's limits.
func New(db *sqlx.DB, agents agent.Queries, limits hosting.Limits, events event.Publisher) *Module {
	svc := hostingsvc.New(hostingpg.New(db), agents, limits, events)
	return &Module{Service: svc, http: hostinghttp.New(svc, svc)}
}

// Register mounts /api/v1/environments, /api/v1/hosting and
// /api/v1/agents/{id}/hosting.
func (m *Module) Register(mux *http.ServeMux) { m.http.Register(mux) }
