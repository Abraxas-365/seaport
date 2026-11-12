package crewhttp

import (
	"github.com/gofiber/fiber/v3"

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

// Handle is the route handler.
func (a Attachment) Handle(c fiber.Ctx) error {
	p, ok := httpx.PrincipalFrom(c.Context())
	if !ok {
		return errx.Unauthorized("missing crew principal")
	}
	id, err := identity.ParseAttachmentID(httpx.Param(c, "id"))
	if err != nil {
		return err
	}
	in := ship.OpenAttachment{ID: id}
	if !p.Agent.IsZero() {
		in.Agent = &p.Agent
	}
	f, err := a.Ships.OpenAttachment(c.Context(), p.Organization, in)
	if err != nil {
		return err
	}
	if f.Kind == ship.AttachmentDocument {
		return httpx.Download(c, f.MIME, f.Name, f.Data)
	}
	return httpx.File(c, f.MIME, f.Data)
}
