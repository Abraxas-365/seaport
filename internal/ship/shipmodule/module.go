// Package shipmodule wires the ship adapters to its service.
package shipmodule

import (
	"net/http"

	"github.com/Abraxas-365/puerto/internal/conversation"
	"github.com/Abraxas-365/puerto/internal/dock"
	"github.com/Abraxas-365/puerto/internal/event"
	"github.com/Abraxas-365/puerto/internal/ship"
	"github.com/Abraxas-365/puerto/internal/ship/adapters/shiphttp"
	"github.com/Abraxas-365/puerto/internal/ship/adapters/shippg"
	"github.com/Abraxas-365/puerto/internal/ship/shipsvc"
	"github.com/jmoiron/sqlx"
)

type Deps struct {
	DB            *sqlx.DB
	Conversations interface {
		conversation.Commands
		conversation.Queries
	}
	Docks     dock.Queries
	Messenger dock.Messenger
	Notifier  ship.Notifier
	Events    event.Publisher
}

type Module struct {
	Service *shipsvc.Service
	http    *shiphttp.Handler
}

func New(d Deps) *Module {
	svc := shipsvc.New(shipsvc.Deps{Repo: shippg.New(d.DB), Conversations: d.Conversations,
		Messenger: d.Messenger, Notifier: d.Notifier, Events: d.Events})
	return &Module{Service: svc, http: shiphttp.New(svc, svc, d.Docks)}
}

func (m *Module) Register(mux *http.ServeMux) { m.http.Register(mux) }
