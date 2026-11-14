package docksvc

import (
	"context"

	"github.com/Abraxas-365/seaport/internal/dock"
	"github.com/Abraxas-365/seaport/internal/errx"
	"github.com/Abraxas-365/seaport/internal/identity"
	"github.com/Abraxas-365/seaport/internal/text"
)

// Destinations: the places the owner lets agents open conversations at
// (dock.Destination). Named by the owner, opened by the crew through
// Messenger.Open (the ship service stores the conversation and the ship).

func (s *Service) PutDestination(ctx context.Context, org identity.OrganizationID, input dock.AddDestination) (dock.Destination, error) {
	if err := input.Validate(); err != nil {
		return dock.Destination{}, err
	}
	input.Target = text.Clean(input.Target)
	input.Title = text.Clean(input.Title)
	d, err := s.repo.Find(ctx, org, input.Dock)
	if err != nil {
		return dock.Destination{}, errx.Wrap(err, "find dock", errx.TypeInternal)
	}
	// A destination is somewhere to post: a dock that can't reply has none.
	if err := replies(d); err != nil {
		return dock.Destination{}, err
	}
	out, err := s.repo.PutDestination(ctx, org, input, dock.MaxDestinations)
	if err != nil {
		return dock.Destination{}, errx.Wrap(err, "put destination", errx.TypeInternal)
	}
	out.Channel = d.Channel
	s.announce(d)
	return out, nil
}

func (s *Service) RemoveDestination(ctx context.Context, org identity.OrganizationID, id identity.DockID, name string) error {
	if err := dock.ValidDestinationName(name); err != nil {
		return err
	}
	if err := s.repo.RemoveDestination(ctx, org, id, name); err != nil {
		return errx.Wrap(err, "remove destination", errx.TypeInternal)
	}
	if d, err := s.repo.Find(ctx, org, id); err == nil {
		s.announce(d)
	}
	return nil
}

func (s *Service) Destinations(ctx context.Context, org identity.OrganizationID, id identity.DockID) ([]dock.Destination, error) {
	d, err := s.repo.Find(ctx, org, id)
	if err != nil {
		return nil, errx.Wrap(err, "find dock", errx.TypeInternal)
	}
	out, err := s.repo.Destinations(ctx, org, id)
	if err != nil {
		return nil, errx.Wrap(err, "list destinations", errx.TypeInternal)
	}
	for i := range out {
		out[i].Channel = d.Channel
	}
	return out, nil
}

func (s *Service) Destination(ctx context.Context, org identity.OrganizationID, id identity.DockID, name string) (dock.Destination, error) {
	if err := dock.ValidDestinationName(name); err != nil {
		return dock.Destination{}, err
	}
	d, err := s.repo.Find(ctx, org, id)
	if err != nil {
		return dock.Destination{}, errx.Wrap(err, "find dock", errx.TypeInternal)
	}
	out, err := s.repo.Destination(ctx, org, id, name)
	if err != nil {
		return dock.Destination{}, errx.Wrap(err, "find destination", errx.TypeInternal)
	}
	out.Channel = d.Channel
	return out, nil
}

// Open posts the first message of a new conversation at a place of the
// dock's channel, through the dock's process like Send.
func (s *Service) Open(ctx context.Context, org identity.OrganizationID, opening dock.Opening) (dock.Opened, error) {
	if err := opening.Validate(); err != nil {
		return dock.Opened{}, err
	}
	d, err := s.repo.Find(ctx, org, opening.Dock)
	if err != nil {
		return dock.Opened{}, errx.Wrap(err, "find dock", errx.TypeInternal)
	}
	if err := replies(d); err != nil {
		return dock.Opened{}, err
	}
	if d.Status != dock.StatusLinked {
		return dock.Opened{}, errx.Business("dock is not linked (status " + string(d.Status) + ")")
	}
	g, err := s.gateway(d.Channel)
	if err != nil {
		return dock.Opened{}, err
	}
	reply, err := s.route(ctx, d, g, dock.Request{Dock: d.ID, Kind: dock.RequestOpen, Opening: &opening})
	if err != nil {
		return dock.Opened{}, err
	}
	if reply.Opened == nil {
		return dock.Opened{}, errx.Internal("the dock's process answered no conversation")
	}
	return *reply.Opened, nil
}

// openHere opens through this process's connection of the dock (the
// owner's side of Open; see sendHere).
func (s *Service) openHere(ctx context.Context, org identity.OrganizationID, opening dock.Opening) (dock.Opened, error) {
	d, err := s.repo.Find(ctx, org, opening.Dock)
	if err != nil {
		return dock.Opened{}, errx.Wrap(err, "find dock", errx.TypeInternal)
	}
	if d.Status != dock.StatusLinked {
		return dock.Opened{}, errx.Business("dock is not linked (status " + string(d.Status) + ")")
	}
	g, err := s.gateway(d.Channel)
	if err != nil {
		return dock.Opened{}, err
	}
	if s.clustered(g) && !g.Running(d.ID) {
		if err := s.ensureRunning(ctx, d, g); err != nil {
			return dock.Opened{}, err
		}
	}
	opened, err := g.Open(ctx, opening)
	if err != nil {
		return dock.Opened{}, errx.Wrap(err, "open through dock", errx.TypeExternal)
	}
	return opened, nil
}
