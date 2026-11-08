// Package conversationmodule wires the conversation adapters to its service.
package conversationmodule

import (
	"net/http"

	"github.com/Abraxas-365/puerto/internal/conversation/adapters/conversationhttp"
	"github.com/Abraxas-365/puerto/internal/conversation/adapters/conversationpg"
	"github.com/Abraxas-365/puerto/internal/conversation/conversationsvc"
	"github.com/Abraxas-365/puerto/internal/event"
	"github.com/jmoiron/sqlx"
)

type Module struct {
	Service *conversationsvc.Service
	http    *conversationhttp.Handler
}

func New(db *sqlx.DB, events event.Publisher) *Module {
	svc := conversationsvc.New(conversationpg.New(db), conversationpg.NewCustoms(db), events)
	return &Module{Service: svc, http: conversationhttp.New(svc, svc, svc, svc)}
}

func (m *Module) Register(mux *http.ServeMux) { m.http.Register(mux) }
