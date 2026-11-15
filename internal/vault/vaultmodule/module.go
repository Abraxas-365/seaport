// Package vaultmodule wires the vault adapters to its service.
package vaultmodule

import (
	"github.com/Abraxas-365/seaport/internal/agent"
	"github.com/Abraxas-365/seaport/internal/conversation"
	"github.com/Abraxas-365/seaport/internal/dock"
	"github.com/Abraxas-365/seaport/internal/event"
	"github.com/Abraxas-365/seaport/internal/httpx"
	"github.com/Abraxas-365/seaport/internal/vault/adapters/vaulthttp"
	"github.com/Abraxas-365/seaport/internal/vault/adapters/vaultpg"
	"github.com/Abraxas-365/seaport/internal/vault/vaultsvc"
	"github.com/jmoiron/sqlx"
)

type Module struct {
	Service *vaultsvc.Service
	http    *vaulthttp.Handler
}

// New builds the module; max bounds an organization's vaults (≤ 0:
// unlimited).
func New(db *sqlx.DB, agents agent.Queries, docks dock.Queries, convs conversation.Queries, events event.Publisher, max int) *Module {
	svc := vaultsvc.New(vaultpg.New(db), agents, docks, convs, events, max)
	return &Module{Service: svc, http: vaulthttp.New(svc, svc)}
}

// Register mounts /api/v1/vaults and the grant listings of agents and docks.
func (m *Module) Register(r httpx.Router) { m.http.Register(r) }
