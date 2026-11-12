// Package agentmodule wires the agent adapters to its service.
package agentmodule

import (
	"github.com/Abraxas-365/seaport/internal/agent"
	"github.com/Abraxas-365/seaport/internal/agent/adapters/agenthttp"
	"github.com/Abraxas-365/seaport/internal/agent/adapters/agentpg"
	"github.com/Abraxas-365/seaport/internal/agent/agentsvc"
	"github.com/Abraxas-365/seaport/internal/event"
	"github.com/Abraxas-365/seaport/internal/httpx"
	"github.com/Abraxas-365/seaport/internal/seal"
	"github.com/jmoiron/sqlx"
)

// secretsInfo derives the agent secrets' key from the port's secret key.
const secretsInfo = "seaport agent secrets v1"

type Module struct {
	Service *agentsvc.Service
	http    *agenthttp.Handler
}

// New builds the agent module; secretKey seals agent secrets ("" refuses
// them), revoker closes a deleted agent's connections and tells them of
// profile changes (the crew hub).
func New(db *sqlx.DB, secretKey string, revoker agent.Revoker, events event.Publisher) (*Module, error) {
	var sealer *seal.Sealer
	if secretKey != "" {
		s, err := seal.New(secretKey, secretsInfo)
		if err != nil {
			return nil, err
		}
		sealer = &s
	}
	svc := agentsvc.New(agentpg.New(db, sealer), revoker, events)
	return &Module{Service: svc, http: agenthttp.New(svc, svc)}, nil
}

// Register mounts /api/v1/agents and /api/v1/docks/{dock}/agents.
func (m *Module) Register(r httpx.Router) { m.http.Register(r) }
