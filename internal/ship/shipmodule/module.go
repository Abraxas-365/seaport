// Package shipmodule wires the ship adapters to its service.
package shipmodule

import (
	"github.com/Abraxas-365/seaport/internal/agent"
	"github.com/Abraxas-365/seaport/internal/blob"
	"github.com/Abraxas-365/seaport/internal/conversation"
	"github.com/Abraxas-365/seaport/internal/dock"
	"github.com/Abraxas-365/seaport/internal/event"
	"github.com/Abraxas-365/seaport/internal/httpx"
	"github.com/Abraxas-365/seaport/internal/ship"
	"github.com/Abraxas-365/seaport/internal/ship/adapters/shiphttp"
	"github.com/Abraxas-365/seaport/internal/ship/adapters/shippg"
	"github.com/Abraxas-365/seaport/internal/ship/shipsvc"
	"github.com/jmoiron/sqlx"
)

type Deps struct {
	DB            *sqlx.DB
	Conversations interface {
		conversation.Commands
		conversation.Queries
		// CustomsQueries: what customs says of a held message on release.
		conversation.CustomsQueries
	}
	Docks     dock.Queries
	Messenger dock.Messenger
	Notifier  ship.Notifier
	Events    event.Publisher
	Limits    ship.Limits
	Agents    agent.Queries
	// Blobs and SecretKey keep message media sealed (either missing: off).
	Blobs     blob.Store
	SecretKey string
}

type Module struct {
	Service *shipsvc.Service
	http    *shiphttp.Handler
}

func New(d Deps) (*Module, error) {
	svc, err := shipsvc.New(shipsvc.Deps{Repo: shippg.New(d.DB), Conversations: d.Conversations,
		Messenger: d.Messenger, Notifier: d.Notifier, Events: d.Events, Limits: &d.Limits, Agents: d.Agents,
		Media: shipsvc.Media{Blobs: d.Blobs, SecretKey: d.SecretKey}})
	if err != nil {
		return nil, err
	}
	return &Module{Service: svc, http: shiphttp.New(svc, svc, d.Docks)}, nil
}

func (m *Module) Register(r httpx.Router) { m.http.Register(r) }
