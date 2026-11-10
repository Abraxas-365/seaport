package crewhttp

import (
	"net/http"

	"github.com/Abraxas-365/seaport/internal/errx"
	"github.com/Abraxas-365/seaport/internal/httpx"
	"github.com/Abraxas-365/seaport/internal/identity"
	"github.com/Abraxas-365/seaport/internal/ship"
)

// Attachment serves GET /crew/attachments/{id} (behind crew auth): the
// bytes of a ship's attachment, for the caller's agent only when the
// conversation is on one of its docks (else 404; the local owner of an
// open port reads any).
type Attachment struct {
	Ships ship.Queries
}

func (a Attachment) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	httpx.Handle(func(w http.ResponseWriter, r *http.Request) error {
		p, ok := httpx.PrincipalFrom(r.Context())
		if !ok {
			return errx.Unauthorized("missing crew principal")
		}
		id, err := identity.ParseAttachmentID(r.PathValue("id"))
		if err != nil {
			return err
		}
		in := ship.OpenAttachment{ID: id}
		if !p.Agent.IsZero() {
			in.Agent = &p.Agent
		}
		f, err := a.Ships.OpenAttachment(r.Context(), p.Organization, in)
		if err != nil {
			return err
		}
		return httpx.File(w, f.MIME, f.Data)
	}).ServeHTTP(w, r)
}
