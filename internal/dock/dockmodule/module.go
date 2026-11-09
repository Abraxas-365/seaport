// Package dockmodule wires the dock adapters (Postgres, gateways, HTTP) to
// its service.
package dockmodule

import (
	"context"
	"net/http"
	"time"

	"github.com/Abraxas-365/seaport/internal/dock"
	"github.com/Abraxas-365/seaport/internal/dock/adapters/dockapi"
	"github.com/Abraxas-365/seaport/internal/dock/adapters/dockhttp"
	"github.com/Abraxas-365/seaport/internal/dock/adapters/dockpg"
	"github.com/Abraxas-365/seaport/internal/dock/adapters/dockwa"
	"github.com/Abraxas-365/seaport/internal/dock/docksvc"
	"github.com/Abraxas-365/seaport/internal/event"
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
	// Gateways are extra gateways (tests: a fake exclusive channel).
	Gateways []dock.Gateway
}

type Module struct {
	Service *docksvc.Service
	http    *dockhttp.Handler
	wa      *dockwa.Gateway
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
	cluster := docksvc.Cluster{Owners: dockpg.NewOwners(d.DB), Relay: dockpg.NewRelay(d.DB), Process: d.Process, TTL: d.DockLease}
	svc := docksvc.New(dockpg.New(d.DB), d.Events, d.MaxDocks, cluster, gateways...)
	if wa != nil {
		wa.Bind(svc, nil)
	}
	return &Module{Service: svc, http: dockhttp.New(svc, svc), wa: wa}, nil
}

// BindReceiver hands inbound messages to the ship domain (built later).
func (m *Module) BindReceiver(r interface {
	Receive(ctx context.Context, organization identity.OrganizationID, input ship.Inbound) (ship.Ship, bool, error)
}) {
	if m.wa != nil {
		m.wa.Bind(m.Service, r)
	}
}

func (m *Module) Register(mux *http.ServeMux) { m.http.Register(mux) }

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
