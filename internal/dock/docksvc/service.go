// Package docksvc implements the dock ports over one Gateway per channel,
// running each exclusive dock in the process that owns it (cluster.go).
package docksvc

import (
	"context"
	"log/slog"

	"github.com/Abraxas-365/seaport/internal/dock"
	"github.com/Abraxas-365/seaport/internal/errx"
	"github.com/Abraxas-365/seaport/internal/event"
	"github.com/Abraxas-365/seaport/internal/identity"
	"github.com/Abraxas-365/seaport/internal/query"
	"github.com/Abraxas-365/seaport/internal/text"
)

type Service struct {
	repo     dock.Repository
	events   event.Publisher
	gateways map[dock.Channel]dock.Gateway
	// maxDocks bounds each organization's docks (≤ 0: unlimited).
	maxDocks int
	cluster  Cluster
	own      ownership
}

func New(repo dock.Repository, events event.Publisher, maxDocks int, cluster Cluster, gateways ...dock.Gateway) *Service {
	if events == nil {
		events = event.Nop{}
	}
	s := &Service{repo: repo, events: events, gateways: map[dock.Channel]dock.Gateway{}, maxDocks: maxDocks, cluster: cluster,
		own: ownership{owned: map[identity.DockID]struct{}{}, retry: map[identity.DockID]backoff{}, locks: map[identity.DockID]*dockLock{}}}
	for _, g := range gateways {
		s.gateways[g.Channel()] = g
	}
	return s
}

var (
	_ dock.Commands  = (*Service)(nil)
	_ dock.Queries   = (*Service)(nil)
	_ dock.Messenger = (*Service)(nil)
)

func (s *Service) gateway(c dock.Channel) (dock.Gateway, error) {
	g, ok := s.gateways[c]
	if !ok {
		return nil, errx.Business("channel " + string(c) + " is not available on this port")
	}
	return g, nil
}

func (s *Service) Create(ctx context.Context, org identity.OrganizationID, input dock.Create) (dock.Dock, error) {
	if err := input.Validate(); err != nil {
		return dock.Dock{}, err
	}
	input.Name = text.Clean(input.Name)
	if _, err := s.gateway(input.Channel); err != nil {
		return dock.Dock{}, err
	}
	d, err := s.repo.Create(ctx, org, identity.NewDockID(), input, dock.StatusLinking, s.maxDocks)
	if err != nil {
		return dock.Dock{}, errx.Wrap(err, "create dock", errx.TypeInternal)
	}
	return s.link(ctx, d)
}

func (s *Service) Relink(ctx context.Context, org identity.OrganizationID, id identity.DockID) (dock.Dock, error) {
	d, err := s.repo.Find(ctx, org, id)
	if err != nil {
		return dock.Dock{}, errx.Wrap(err, "find dock", errx.TypeInternal)
	}
	if !d.Status.CanLink() {
		return dock.Dock{}, errx.Business("dock is already linked")
	}
	return s.link(ctx, d)
}

func (s *Service) link(ctx context.Context, d dock.Dock) (dock.Dock, error) {
	g, err := s.gateway(d.Channel)
	if err != nil {
		return dock.Dock{}, err
	}
	// The dock's process stores the status (failed when linking fails).
	if _, err := s.route(ctx, d, g, dock.Request{Dock: d.ID, Kind: dock.RequestLink}); err != nil {
		s.publish(ctx, d.ID)
		return dock.Dock{}, errx.Wrap(err, "start linking", errx.TypeExternal)
	}
	out, err := s.Find(ctx, d.Organization, d.ID)
	if err == nil {
		s.announce(out)
	}
	return out, err
}

func (s *Service) Update(ctx context.Context, org identity.OrganizationID, id identity.DockID, input dock.Update) (dock.Dock, error) {
	if err := input.Validate(); err != nil {
		return dock.Dock{}, err
	}
	name := text.Clean(*input.Name)
	input.Name = &name
	d, err := s.repo.Update(ctx, org, id, input)
	if err != nil {
		return dock.Dock{}, errx.Wrap(err, "update dock", errx.TypeInternal)
	}
	s.announce(d)
	return s.withQR(ctx, d), nil
}

func (s *Service) Unlink(ctx context.Context, org identity.OrganizationID, id identity.DockID) (dock.Dock, error) {
	d, err := s.repo.Find(ctx, org, id)
	if err != nil {
		return dock.Dock{}, errx.Wrap(err, "find dock", errx.TypeInternal)
	}
	if d.Channel == dock.ChannelAPI {
		return dock.Dock{}, errx.Business("api docks have no link; delete the dock instead")
	}
	g, err := s.gateway(d.Channel)
	if err != nil {
		return dock.Dock{}, err
	}
	// The dock's process stores logged_out.
	if _, err := s.route(ctx, d, g, dock.Request{Dock: d.ID, Kind: dock.RequestUnlink}); err != nil {
		return dock.Dock{}, errx.Wrap(err, "unlink dock", errx.TypeExternal)
	}
	s.publish(ctx, id)
	return s.Find(ctx, org, id)
}

func (s *Service) Delete(ctx context.Context, org identity.OrganizationID, id identity.DockID) error {
	d, err := s.repo.Find(ctx, org, id)
	if err != nil {
		return errx.Wrap(err, "find dock", errx.TypeInternal)
	}
	if g, ok := s.gateways[d.Channel]; ok {
		if _, err := s.route(ctx, d, g, dock.Request{Dock: d.ID, Kind: dock.RequestUnlink}); err != nil {
			// The dock goes anyway; the phone may still list the device.
			slog.Warn("unlink dock being deleted", "dock", id, "err", err)
		}
	}
	if err := s.repo.Delete(ctx, org, id); err != nil {
		return errx.Wrap(err, "delete dock", errx.TypeInternal)
	}
	slog.Info("dock deleted", "dock", id, "organization", org, "channel", d.Channel, "name", d.Name)
	s.events.Publish(org, dock.EventDeleted, dock.Deleted{ID: id})
	return nil
}

func (s *Service) PairingChanged(ctx context.Context, id identity.DockID) {
	s.publish(ctx, id)
}

// announce publishes a dock's state, never with its QR: events are stored
// in clear; clients fetch the dock (GET returns the QR) when an event says
// it is linking. Every dock.updated goes through here.
func (s *Service) announce(d dock.Dock) {
	d.QRCode = ""
	s.events.Publish(d.Organization, dock.EventUpdated, d)
}

// publish announces a dock's current state.
func (s *Service) publish(ctx context.Context, id identity.DockID) {
	d, err := s.repo.Get(ctx, id)
	if err != nil {
		if !errx.IsType(err, errx.TypeNotFound) {
			slog.Warn("publish dock", "dock", id, "err", err)
		}
		return
	}
	s.announce(d)
}

func (s *Service) SetLinked(ctx context.Context, id identity.DockID, address string) error {
	if err := text.Required("address", address, dock.MaxAddress); err != nil {
		return err
	}
	if err := s.repo.SetLinked(ctx, id, address); err != nil {
		return errx.Wrap(err, "set dock linked", errx.TypeInternal)
	}
	s.publish(ctx, id)
	return nil
}

func (s *Service) SetStatus(ctx context.Context, id identity.DockID, status dock.Status) error {
	if !status.Valid() {
		return errx.InvalidField("status", "status is not valid")
	}
	if err := s.repo.SetStatus(ctx, id, status); err != nil {
		return errx.Wrap(err, "set dock status", errx.TypeInternal)
	}
	s.publish(ctx, id)
	return nil
}

func (s *Service) Find(ctx context.Context, org identity.OrganizationID, id identity.DockID) (dock.Dock, error) {
	d, err := s.repo.Find(ctx, org, id)
	if err != nil {
		return dock.Dock{}, errx.Wrap(err, "find dock", errx.TypeInternal)
	}
	return s.withQR(ctx, d), nil
}

func (s *Service) List(ctx context.Context, org identity.OrganizationID, page query.Pagination) (query.Paginated[dock.Dock], error) {
	out, err := s.repo.List(ctx, org, page.Normalize())
	if err != nil {
		return out, errx.Wrap(err, "list docks", errx.TypeInternal)
	}
	for i := range out.Items {
		out.Items[i] = s.withQR(ctx, out.Items[i])
	}
	return out, nil
}

// withQR adds the pairing code of a linking dock (whatever process pairs
// it); a code that can't be read is left out (the client retries).
func (s *Service) withQR(ctx context.Context, d dock.Dock) dock.Dock {
	if d.Status == dock.StatusLinking {
		if g, ok := s.gateways[d.Channel]; ok {
			qr, err := g.QR(ctx, d.ID)
			if err != nil {
				slog.Warn("read pairing code", "dock", d.ID, "err", err)
			}
			d.QRCode = qr
		}
	}
	return d
}

func (s *Service) NewMessageID(ctx context.Context, org identity.OrganizationID, id identity.DockID) (string, error) {
	d, err := s.repo.Find(ctx, org, id)
	if err != nil {
		return "", errx.Wrap(err, "find dock", errx.TypeInternal)
	}
	g, err := s.gateway(d.Channel)
	if err != nil {
		return "", err
	}
	return g.NewMessageID(id)
}

func (s *Service) Send(ctx context.Context, org identity.OrganizationID, msg dock.Outgoing) error {
	if err := msg.Validate(); err != nil {
		return err
	}
	d, err := s.repo.Find(ctx, org, msg.Dock)
	if err != nil {
		return errx.Wrap(err, "find dock", errx.TypeInternal)
	}
	if d.Status != dock.StatusLinked {
		return errx.Business("dock is not linked (status " + string(d.Status) + ")")
	}
	g, err := s.gateway(d.Channel)
	if err != nil {
		return err
	}
	_, err = s.route(ctx, d, g, dock.Request{Dock: d.ID, Kind: dock.RequestSend, Outgoing: &msg})
	return err
}
