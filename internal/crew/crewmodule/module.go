// Package crewmodule wires the crew adapters: the WebSocket hub agents dial
// into and the MCP tools they work with.
package crewmodule

import (
	"net/http"
	"time"

	"github.com/Abraxas-365/seaport/internal/agent"
	"github.com/Abraxas-365/seaport/internal/conversation"
	"github.com/Abraxas-365/seaport/internal/crew/adapters/crewhttp"
	"github.com/Abraxas-365/seaport/internal/crew/adapters/crewmcp"
	"github.com/Abraxas-365/seaport/internal/crew/adapters/crewpg"
	"github.com/Abraxas-365/seaport/internal/crew/adapters/crewws"
	"github.com/Abraxas-365/seaport/internal/crew/crewsvc"
	"github.com/Abraxas-365/seaport/internal/event"
	"github.com/Abraxas-365/seaport/internal/httpx"
	"github.com/Abraxas-365/seaport/internal/permission"
	"github.com/Abraxas-365/seaport/internal/ship"
	"github.com/jmoiron/sqlx"
)

type Module struct {
	// Hub is the ship domain's Notifier.
	Hub *crewws.Hub
	// Tokens are the organizations' crew tokens.
	Tokens *crewsvc.Service
	// Resolver authenticates agents with crew tokens.
	Resolver httpx.CrewResolver
	tokens   *crewhttp.Handler
	mcp      http.Handler
	kit      crewhttp.Kit
	media    crewhttp.Attachment
	reply    crewhttp.Reply
	version  string
}

// New builds the crew module; maxConns bounds each organization's live
// agent connections (≤ 0: unlimited).
func New(db *sqlx.DB, process string, lease time.Duration, mcpURL, version string, events event.Stream, maxConns int) *Module {
	hub := crewws.New(process, lease, mcpURL, events, maxConns, crewpg.NewPresence(db))
	svc := crewsvc.New(crewpg.New(db), hub)
	return &Module{Hub: hub, Tokens: svc, Resolver: crewhttp.Resolver{Tokens: svc},
		tokens: crewhttp.New(svc, svc), version: version}
}

// Register mounts the admin routes (GET /api/v1/crew: connected agents;
// /api/v1/crew/tokens).
func (m *Module) Register(r httpx.Router) {
	r.Get("/api/v1/crew", httpx.RequireFunc(permission.CrewRead, crewhttp.ServeStatus(m.Hub)))
	m.tokens.Register(r)
}

// Bind connects the crew to the ship, conversation and agent domains
// (built after the hub, which they use as Notifier and Revoker).
func (m *Module) Bind(ships interface {
	ship.Commands
	ship.Queries
}, convs conversation.Queries, agents interface {
	agent.Queries
	agent.Kits
}) {
	m.mcp = crewmcp.Handler(crewmcp.Deps{Ships: ships, History: ships, Conversations: convs, Agents: agents, Version: m.version})
	m.kit = crewhttp.Kit{Kits: agents, Agents: agents}
	m.media = crewhttp.Attachment{Ships: ships}
	m.reply = crewhttp.Reply{Ships: ships}
	m.Hub.Bind(ships, agents)
}

// WS is the /crew/ws handler, MCP the /mcp handler, Kit GET /crew/profile,
// Attachment GET /crew/attachments/{id}, Reply POST /crew/reply.
func (m *Module) WS() http.Handler          { return m.Hub }
func (m *Module) MCP() http.Handler         { return m.mcp }
func (m *Module) Kit() httpx.Handler        { return m.kit.Handle }
func (m *Module) Attachment() httpx.Handler { return m.media.Handle }
func (m *Module) Reply() httpx.Handler      { return m.reply.Handle }
