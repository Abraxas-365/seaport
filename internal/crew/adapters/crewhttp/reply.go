package crewhttp

import (
	"net/http"

	"github.com/Abraxas-365/seaport/internal/crew"
	"github.com/Abraxas-365/seaport/internal/errx"
	"github.com/Abraxas-365/seaport/internal/httpx"
	"github.com/Abraxas-365/seaport/internal/ship"
)

// Reply serves POST /crew/reply (behind crew auth): the MCP reply tool
// over plain HTTP, with files (base64) up to crew.MaxReplyBody, for
// `seaport reply -file`. Agent tokens only: the reply is the agent's.
type Reply struct {
	Ships ship.Commands
}

func (h Reply) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	httpx.Handle(func(w http.ResponseWriter, r *http.Request) error {
		p, ok := httpx.PrincipalFrom(r.Context())
		if !ok {
			return errx.Unauthorized("missing crew principal")
		}
		if p.Agent.IsZero() {
			return errx.Forbidden("replies need an agent's crew token")
		}
		var in crew.ReplyRequest
		if err := httpx.DecodeJSONMax(r, &in, crew.MaxReplyBody); err != nil {
			return err
		}
		if err := in.Validate(); err != nil {
			return err
		}
		s, err := h.Ships.Send(r.Context(), p.Organization, ship.Outbound{Conversation: in.ConversationID, Text: in.Text, Agent: &p.Agent, Files: in.Files})
		if err != nil {
			return err
		}
		return httpx.JSON(w, http.StatusCreated, crew.ReplyAnswer{ShipID: s.ID.String(), Status: string(s.Status)})
	}).ServeHTTP(w, r)
}
