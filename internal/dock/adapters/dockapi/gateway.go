// Package dockapi is the gateway of the generic "api" channel: no pairing,
// messages come in through the admin API and replies are only stored.
package dockapi

import (
	"context"

	"github.com/Abraxas-365/puerto/internal/dock"
	"github.com/Abraxas-365/puerto/internal/identity"
	"github.com/google/uuid"
)

type Gateway struct{}

func New() *Gateway { return &Gateway{} }

var _ dock.Gateway = (*Gateway)(nil)

func (*Gateway) Channel() dock.Channel { return dock.ChannelAPI }

func (*Gateway) Link(context.Context, dock.Dock) (dock.Status, error) { return dock.StatusLinked, nil }

func (*Gateway) QR(identity.DockID) string { return "" }

func (*Gateway) Restore(context.Context, dock.Dock) error { return nil }

func (*Gateway) Unlink(context.Context, dock.Dock) error { return nil }

func (*Gateway) NewMessageID(identity.DockID) (string, error) { return "api-" + uuid.NewString(), nil }

func (*Gateway) Send(context.Context, dock.Outgoing) error { return nil }
