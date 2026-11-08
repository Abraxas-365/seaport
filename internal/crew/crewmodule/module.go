// Package crewmodule wires the crew adapters: the WebSocket hub agents dial
// into and the MCP tools they work with.
package crewmodule

import (
	"net/http"
	"time"

	"github.com/Abraxas-365/puerto/internal/conversation"
	"github.com/Abraxas-365/puerto/internal/crew/adapters/crewmcp"
	"github.com/Abraxas-365/puerto/internal/crew/adapters/crewws"
	"github.com/Abraxas-365/puerto/internal/event"
	"github.com/Abraxas-365/puerto/internal/ship"
)

type Module struct {
	// Hub is the ship domain's Notifier.
	Hub     *crewws.Hub
	mcp     http.Handler
	version string
}

func New(lease time.Duration, mcpURL, version string, events event.Publisher) *Module {
	return &Module{Hub: crewws.New(lease, mcpURL, events), version: version}
}

// Register mounts the admin routes (GET /api/v1/crew: connected agents).
func (m *Module) Register(mux *http.ServeMux) {
	mux.Handle("GET /api/v1/crew", m.Hub.StatusHandler())
}

// Bind connects the crew to the ship and conversation domains (built after
// the hub, which they use as Notifier).
func (m *Module) Bind(ships interface {
	ship.Commands
	ship.Queries
}, convs conversation.Queries) {
	m.mcp = crewmcp.Handler(crewmcp.Deps{Ships: ships, History: ships, Conversations: convs, Version: m.version})
	m.Hub.Bind(ships)
}

// WS is the /crew/ws handler; MCP the /mcp handler.
func (m *Module) WS() http.Handler  { return m.Hub }
func (m *Module) MCP() http.Handler { return m.mcp }
