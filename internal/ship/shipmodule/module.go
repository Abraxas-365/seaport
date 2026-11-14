// Package shipmodule wires the ship adapters to its service.
package shipmodule

import (
	"database/sql"
	"time"

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
	DB *sqlx.DB
	// LockDB is the pool of session advisory locks (pglock), apart from DB.
	LockDB        *sql.DB
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
	// ApprovalTTL is how long an approval waits for its answer (zero: the
	// default).
	ApprovalTTL time.Duration
}

type Module struct {
	Service *shipsvc.Service
	http    *shiphttp.Handler
}

func New(d Deps) (*Module, error) {
	svc, err := shipsvc.New(shipsvc.Deps{Repo: shippg.New(d.DB), Conversations: d.Conversations,
		Messenger: d.Messenger, Notifier: d.Notifier, Events: d.Events, Limits: &d.Limits, Agents: d.Agents, Docks: d.Docks,
		Media: shipsvc.Media{Blobs: d.Blobs, SecretKey: d.SecretKey}, ApprovalTTL: d.ApprovalTTL,
		ApprovalLocks: shippg.NewApprovalLocker(d.LockDB)})
	if err != nil {
		return nil, err
	}
	return &Module{Service: svc, http: shiphttp.New(svc, svc, d.Docks)}, nil
}

func (m *Module) Register(r httpx.Router) { m.http.Register(r) }
