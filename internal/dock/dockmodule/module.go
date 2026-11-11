// Package dockmodule wires the dock adapters (Postgres, gateways, HTTP) to
// its service.
package dockmodule

import (
	"context"
	"net/http"
	"time"

	"github.com/Abraxas-365/seaport/internal/dock"
	"github.com/Abraxas-365/seaport/internal/dock/adapters/dockapi"
	"github.com/Abraxas-365/seaport/internal/dock/adapters/dockgithub"
	"github.com/Abraxas-365/seaport/internal/dock/adapters/dockhttp"
	"github.com/Abraxas-365/seaport/internal/dock/adapters/dockpg"
	"github.com/Abraxas-365/seaport/internal/dock/adapters/dockslack"
	"github.com/Abraxas-365/seaport/internal/dock/adapters/dockwa"
	"github.com/Abraxas-365/seaport/internal/dock/docksvc"
	"github.com/Abraxas-365/seaport/internal/errx"
	"github.com/Abraxas-365/seaport/internal/event"
	"github.com/Abraxas-365/seaport/internal/httpx"
	"github.com/Abraxas-365/seaport/internal/identity"
	"github.com/Abraxas-365/seaport/internal/ship"
	"github.com/jmoiron/sqlx"
)

type Deps struct {
	DB *sqlx.DB
	// SecretKey (SEAPORT_SECRET_KEY) seals the WhatsApp store.
	SecretKey string
	// WhatsApp enables the WhatsApp gateway (off in tests).
	WhatsApp bool
	Events   event.Publisher
	// MaxDocks bounds each organization's docks (≤ 0: unlimited).
	MaxDocks int
	// Process names this process among those serving the database; it
	// owns docks for DockLease at a time (renewed while it lives).
	Process   string
	DockLease time.Duration
	// Gateways are extra gateways (tests: a fake exclusive channel); one
	// with a BindReceiver(Receiver) method gets inbound messages' receiver.
	Gateways []dock.Gateway
	// GitHub configures GitHub docks (on with a secret key: it seals the
	// Apps' credentials).
	GitHub dockgithub.Config
	// Slack configures Slack docks (on with a secret key, like GitHub).
	Slack dockslack.Config
}

// Receiver takes inbound messages (the ship domain, bound late).
type Receiver interface {
	Receive(ctx context.Context, organization identity.OrganizationID, input ship.Inbound) (ship.Ship, bool, error)
}

type Module struct {
	Service *docksvc.Service
	http    *dockhttp.Handler
	wa      *dockwa.Gateway
	gh      *dockgithub.Gateway
	ghHTTP  *dockgithub.Handler
	sl      *dockslack.Gateway
	slHTTP  *dockslack.Handler
	extra   []dock.Gateway
}

func New(ctx context.Context, d Deps) (*Module, error) {
	gateways := append([]dock.Gateway{dockapi.New()}, d.Gateways...)
	var wa *dockwa.Gateway
	if d.WhatsApp {
		var err error
		if wa, err = dockwa.Open(ctx, d.DB, d.SecretKey); err != nil {
			return nil, err
		}
		gateways = append(gateways, wa)
	}
	var gh *dockgithub.Gateway
	if d.SecretKey != "" {
		var err error
		if gh, err = dockgithub.Open(ctx, d.DB, d.SecretKey, d.GitHub); err != nil {
			return nil, err
		}
		gateways = append(gateways, gh)
	}
	var sl *dockslack.Gateway
	if d.SecretKey != "" {
		var err error
		if sl, err = dockslack.Open(ctx, d.DB, d.SecretKey, d.Slack); err != nil {
			return nil, err
		}
		gateways = append(gateways, sl)
	}
	cluster := docksvc.Cluster{Owners: dockpg.NewOwners(d.DB), Relay: dockpg.NewRelay(d.DB), Process: d.Process, TTL: d.DockLease}
	svc := docksvc.New(dockpg.New(d.DB), d.Events, d.MaxDocks, cluster, gateways...)
	if wa != nil {
		wa.Bind(svc, nil)
	}
	m := &Module{Service: svc, http: dockhttp.New(svc, svc), wa: wa, gh: gh, sl: sl, extra: d.Gateways}
	if gh != nil {
		gh.Bind(svc, nil, nil)
		m.ghHTTP = dockgithub.NewHandler(gh, svc)
	}
	if sl != nil {
		sl.Bind(svc, nil, nil)
		m.slHTTP = dockslack.NewHandler(sl, svc)
	}
	return m, nil
}

// BindReceiver hands inbound messages to the ship domain (built later).
func (m *Module) BindReceiver(r Receiver) {
	if m.wa != nil {
		m.wa.Bind(m.Service, r)
	}
	if m.gh != nil {
		m.gh.Bind(nil, r, nil)
	}
	if m.sl != nil {
		m.sl.Bind(nil, r, nil)
	}
	for _, g := range m.extra {
		if b, ok := g.(interface{ BindReceiver(Receiver) }); ok {
			b.BindReceiver(r)
		}
	}
}

// BindTeams tells GitHub docks which agents work on a dock (a mention of
// one is for the crew; the agent domain is built later).
func (m *Module) BindTeams(t dockgithub.Teams) {
	if m.gh != nil {
		m.gh.Bind(nil, nil, t)
	}
	if m.sl != nil {
		m.sl.Bind(nil, nil, t)
	}
}

func (m *Module) Register(mux *http.ServeMux) {
	m.http.Register(mux)
	if m.ghHTTP != nil {
		m.ghHTTP.Register(mux)
	}
	if m.slHTTP != nil {
		m.slHTTP.Register(mux)
	}
}

// RegisterPublic mounts the routes GitHub calls without a Seaport
// credential (App flows, webhooks; each checks its own proof).
func (m *Module) RegisterPublic(mux *http.ServeMux) {
	if m.ghHTTP != nil {
		m.ghHTTP.RegisterPublic(mux)
	}
	if m.slHTTP != nil {
		m.slHTTP.RegisterPublic(mux)
	}
}

// GitHubToken serves POST /crew/github/token (mounted behind crew auth):
// App tokens for the agent's repos. Without GitHub docks (no secret key)
// it answers 404.
func (m *Module) GitHubToken() http.Handler {
	if m.ghHTTP == nil {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			httpx.WriteError(w, r, errx.NotFound("GitHub docks are off on this port (no secret key)"))
		})
	}
	return m.ghHTTP.Token()
}

// Start runs dock ownership (serving other processes' requests, renewing
// leases, taking over the docks of stopped processes).
func (m *Module) Start(ctx context.Context) { m.Service.Start(ctx) }

// Close hands the docks over (other processes take them at once) and
// disconnects the gateways.
func (m *Module) Close() {
	m.Service.Stop()
	if m.wa != nil {
		m.wa.Close()
	}
}
