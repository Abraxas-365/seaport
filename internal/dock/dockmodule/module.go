// Package dockmodule wires the dock adapters (Postgres, gateways, HTTP) to
// its service.
package dockmodule

import (
	"context"
	"net/http"

	"github.com/Abraxas-365/puerto/internal/dock"
	"github.com/Abraxas-365/puerto/internal/dock/adapters/dockapi"
	"github.com/Abraxas-365/puerto/internal/dock/adapters/dockhttp"
	"github.com/Abraxas-365/puerto/internal/dock/adapters/dockpg"
	"github.com/Abraxas-365/puerto/internal/dock/adapters/dockwa"
	"github.com/Abraxas-365/puerto/internal/dock/docksvc"
	"github.com/Abraxas-365/puerto/internal/event"
	"github.com/Abraxas-365/puerto/internal/identity"
	"github.com/Abraxas-365/puerto/internal/ship"
	"github.com/jmoiron/sqlx"
)

type Deps struct {
	DB          *sqlx.DB
	DatabaseURL string
	// WhatsApp enables the WhatsApp gateway (off in tests).
	WhatsApp bool
	Events   event.Publisher
}

type Module struct {
	Service *docksvc.Service
	http    *dockhttp.Handler
	wa      *dockwa.Gateway
}

func New(ctx context.Context, d Deps) (*Module, error) {
	gateways := []dock.Gateway{dockapi.New()}
	var wa *dockwa.Gateway
	if d.WhatsApp {
		var err error
		if wa, err = dockwa.Open(ctx, d.DatabaseURL); err != nil {
			return nil, err
		}
		gateways = append(gateways, wa)
	}
	svc := docksvc.New(dockpg.New(d.DB), d.Events, gateways...)
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

// Close disconnects the gateways.
func (m *Module) Close() {
	if m.wa != nil {
		m.wa.Close()
	}
}
