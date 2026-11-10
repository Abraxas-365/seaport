// Package hostingmodule wires the hosting adapters to its service.
package hostingmodule

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strconv"
	"time"

	"github.com/Abraxas-365/seaport/internal/agent"
	"github.com/Abraxas-365/seaport/internal/blob"
	"github.com/Abraxas-365/seaport/internal/errx"
	"github.com/Abraxas-365/seaport/internal/event"
	"github.com/Abraxas-365/seaport/internal/hosting"
	"github.com/Abraxas-365/seaport/internal/hosting/adapters/hostingdocker"
	"github.com/Abraxas-365/seaport/internal/hosting/adapters/hostingegress"
	"github.com/Abraxas-365/seaport/internal/hosting/adapters/hostinggateway"
	"github.com/Abraxas-365/seaport/internal/hosting/adapters/hostinghttp"
	"github.com/Abraxas-365/seaport/internal/hosting/adapters/hostingpg"
	"github.com/Abraxas-365/seaport/internal/hosting/hostingsvc"
	"github.com/Abraxas-365/seaport/internal/httpx"
	"github.com/Abraxas-365/seaport/internal/identity"
	"github.com/jmoiron/sqlx"
)

type Module struct {
	Service *hostingsvc.Service
	http    *hostinghttp.Handler
	docker  *hostingdocker.Runtime
	gateway *hostinggateway.Gateway
	egress  *http.Server
	egressL net.Listener
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
	// Network and ExtraHosts wire slot containers (hostingdocker.Config).
	Network    string
	ExtraHosts []string
	// Slot tokens (crew), repo credentials (agent secrets) and the URL
	// slots reach the port at; without them slots can't start.
	Credentials hosting.Credentials
	Kits        agent.Kits
	AgentURL    string
	// SupervisePeriod overrides hosting.SupervisePeriod (tests).
	SupervisePeriod time.Duration
	// GatewayAnthropic is where the model gateway forwards Anthropic
	// calls (default its API).
	GatewayAnthropic string
	// Egress runs the slots' egress proxy on EgressAddr ("": off), which
	// slots reach at EgressURL (default AgentURL's host with the proxy's
	// port) and sign in to with their crew token (Tokens resolves it).
	// EgressPrivate lets allowed hosts resolve to private addresses;
	// EgressLookup resolves them (tests; default the system's resolver).
	EgressAddr, EgressURL string
	EgressPrivate         bool
	EgressLookup          func(ctx context.Context, host string) ([]netip.Addr, error)
	Tokens                httpx.CrewResolver
	// Blobs and SecretKey keep harness homes sealed (either missing:
	// uploads refused).
	Blobs     blob.Store
	SecretKey string
}

// New builds the hosting module over the agent library (its secrets'
// names).
func New(ctx context.Context, db *sqlx.DB, agents agent.Queries, cfg Config, events event.Publisher) (*Module, error) {
	m := &Module{}
	runtime := cfg.Runtime
	if runtime == nil && cfg.Docker {
		d, err := hostingdocker.New(ctx, hostingdocker.Config{Network: cfg.Network, ExtraHosts: cfg.ExtraHosts, Egress: cfg.EgressAddr != ""})
		if err != nil {
			return nil, err
		}
		m.docker, runtime = d, d
	}
	svc, err := hostingsvc.New(hostingpg.New(db), agents, cfg.Limits, events,
		hostingsvc.Builder{Runtime: runtime, Bases: cfg.Bases},
		hostingsvc.Slots{Credentials: cfg.Credentials, Kits: cfg.Kits, URL: cfg.AgentURL, Period: cfg.SupervisePeriod},
		hostingsvc.Homes{Blobs: cfg.Blobs, SecretKey: cfg.SecretKey})
	if err != nil {
		if m.docker != nil {
			_ = m.docker.Close()
		}
		return nil, err
	}
	m.Service = svc
	m.http = hostinghttp.New(m.Service, m.Service, runtime != nil)
	gw, err := hostinggateway.New(m.Service, hostinggateway.Config{Anthropic: cfg.GatewayAnthropic})
	if err != nil {
		m.Close()
		return nil, err
	}
	m.gateway = gw
	if cfg.EgressAddr != "" && cfg.Tokens != nil {
		// listen now: a taken address fails the start
		if m.egressL, err = net.Listen("tcp", cfg.EgressAddr); err != nil {
			m.Close()
			return nil, errx.Wrap(err, "egress proxy: listen on SEAPORT_EGRESS_ADDR", errx.TypeInternal)
		}
		m.egress = hostingegress.New(m.Service, cfg.Tokens, hostingegress.Config{Private: cfg.EgressPrivate, Lookup: cfg.EgressLookup}).Server(cfg.EgressAddr)
		u := cfg.EgressURL
		if u == "" {
			u = defaultEgressURL(cfg.AgentURL, m.egressL.Addr())
		}
		m.Service.ProxyAt(u)
	}
	return m, nil
}

// defaultEgressURL is the agent URL's host with the proxy's port.
func defaultEgressURL(agentURL string, addr net.Addr) string {
	u, err := url.Parse(agentURL)
	ta, ok := addr.(*net.TCPAddr)
	if err != nil || !ok || u.Hostname() == "" {
		return ""
	}
	return "http://" + net.JoinHostPort(u.Hostname(), strconv.Itoa(ta.Port))
}

// Register mounts /api/v1/environments, /api/v1/hosting,
// /api/v1/agents/{id}/hosting and /api/v1/agents/{id}/usage.
func (m *Module) Register(mux *http.ServeMux) { m.http.Register(mux) }

// Workspace is GET /crew/workspace (mount behind crew auth).
func (m *Module) Workspace() http.Handler { return m.http.Workspace() }

// Gateway is the model gateway (mount at hosting.GatewayPath+"/" behind
// crew auth wrapped in APIKeyAsBearer).
func (m *Module) Gateway() http.Handler { return m.gateway }

// APIKeyAsBearer lets crew auth read an x-api-key header (the gateway's
// callers send their slot token so).
func APIKeyAsBearer(next http.Handler) http.Handler { return hostinggateway.APIKeyAsBearer(next) }

// Notify pokes the slot supervisor: the agent has new work (a
// ship.Notifier).
func (m *Module) Notify(org identity.OrganizationID, agent identity.AgentID) {
	m.Service.Wake(org, agent)
}

// Start runs the slot supervisor until ctx ends (nothing while the
// runtime is off) and the egress proxy (when on).
func (m *Module) Start(ctx context.Context) {
	go m.Service.Supervise(ctx)
	if m.egress != nil {
		slog.Info("egress proxy listening", "addr", m.egressL.Addr().String())
		go func() {
			if err := m.egress.Serve(m.egressL); err != nil && !errors.Is(err, http.ErrServerClosed) {
				slog.Error("egress proxy", "err", err)
			}
		}()
	}
}

// Close cancels the builds in progress (waiting for them to record it) and
// closes the runtime's connection.
func (m *Module) Close() {
	if m.egress != nil {
		_ = m.egress.Close() // tunnels are hijacked: they end with the slots
		_ = m.egressL.Close()
	}
	m.Service.Close()
	if m.docker != nil {
		_ = m.docker.Close()
	}
}
