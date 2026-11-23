package docksvc

import (
	"context"

	"github.com/Abraxas-365/seaport/internal/dock"
	"github.com/Abraxas-365/seaport/internal/errx"
	"github.com/Abraxas-365/seaport/internal/identity"
)

// SetMirror sets (or clears) the mirror of a dock that only triggers: a
// destination of another dock of the organization that replies. The ship
// service opens it when a new thread arrives (see dock.Mirror).
func (s *Service) SetMirror(ctx context.Context, org identity.OrganizationID, id identity.DockID, input dock.SetMirror) (dock.Dock, error) {
	if err := input.Validate(); err != nil {
		return dock.Dock{}, err
	}
	d, err := s.repo.Find(ctx, org, id)
	if err != nil {
		return dock.Dock{}, errx.Wrap(err, "find dock", errx.TypeInternal)
	}
	if input.Mirror != nil {
		if d.CanReply() {
			return dock.Dock{}, errx.Business("only a dock that doesn't reply can have a mirror; turn its replies off first")
		}
		if input.Mirror.Dock == id {
			return dock.Dock{}, errx.Business("a dock can't be its own mirror")
		}
		target, err := s.repo.Find(ctx, org, input.Mirror.Dock)
		if err != nil {
			return dock.Dock{}, errx.Wrap(err, "find mirror dock", errx.TypeInternal)
		}
		if !target.CanReply() {
			return dock.Dock{}, errx.Business("the mirror must be a dock that replies")
		}
		if _, err := s.repo.Destination(ctx, org, input.Mirror.Dock, input.Mirror.Destination); err != nil {
			return dock.Dock{}, errx.Wrap(err, "find mirror destination", errx.TypeInternal)
		}
	}
	out, err := s.repo.SetMirror(ctx, org, id, input.Mirror)
	if err != nil {
		return dock.Dock{}, errx.Wrap(err, "set dock mirror", errx.TypeInternal)
	}
	s.announce(out)
	return out, nil
}
