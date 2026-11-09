// Package agentmodule wires the agent adapters to its service.
package agentmodule

import (
	"net/http"

	"github.com/Abraxas-365/seaport/internal/agent"
	"github.com/Abraxas-365/seaport/internal/agent/adapters/agenthttp"
	"github.com/Abraxas-365/seaport/internal/agent/adapters/agentpg"
	"github.com/Abraxas-365/seaport/internal/agent/agentsvc"
	"github.com/Abraxas-365/seaport/internal/event"
	"github.com/jmoiron/sqlx"
)

type Module struct {
	Service *agentsvc.Service
	http    *agenthttp.Handler
}

// New builds the agent module; revoker closes a deleted agent's
// connections (the crew hub).
func New(db *sqlx.DB, revoker agent.Revoker, events event.Publisher) *Module {
	svc := agentsvc.New(agentpg.New(db), revoker, events)
	return &Module{Service: svc, http: agenthttp.New(svc, svc)}
}

// Register mounts /api/v1/agents and /api/v1/docks/{dock}/agents.
func (m *Module) Register(mux *http.ServeMux) { m.http.Register(mux) }
