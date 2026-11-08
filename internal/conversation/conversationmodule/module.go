// Package conversationmodule wires the conversation adapters to its service.
package conversationmodule

import (
	"database/sql"
	"net/http"

	"github.com/Abraxas-365/seaport/internal/conversation/adapters/conversationhttp"
	"github.com/Abraxas-365/seaport/internal/conversation/adapters/conversationpg"
	"github.com/Abraxas-365/seaport/internal/conversation/conversationsvc"
	"github.com/Abraxas-365/seaport/internal/event"
	"github.com/jmoiron/sqlx"
)

type Module struct {
	Service *conversationsvc.Service
	http    *conversationhttp.Handler
}

// New builds the module; lockDB is a pool of its own for the per-dock
// customs locks (they span the work done on db).
func New(db *sqlx.DB, lockDB *sql.DB, events event.Publisher) *Module {
	svc := conversationsvc.New(conversationpg.New(db), conversationpg.NewCustoms(db),
		conversationpg.NewDockLocker(lockDB), events)
	return &Module{Service: svc, http: conversationhttp.New(svc, svc, svc, svc)}
}

func (m *Module) Register(mux *http.ServeMux) { m.http.Register(mux) }
