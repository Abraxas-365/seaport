// Package dockapi is the gateway of the generic "api" channel: no pairing,
// messages come in through the admin API and replies are only stored.
package dockapi

import (
	"context"

	"github.com/Abraxas-365/seaport/internal/dock"
	"github.com/Abraxas-365/seaport/internal/identity"
	"github.com/google/uuid"
)

type Gateway struct{}

func New() *Gateway { return &Gateway{} }

var _ dock.Gateway = (*Gateway)(nil)

func (*Gateway) Channel() dock.Channel { return dock.ChannelAPI }

func (*Gateway) Link(context.Context, dock.Dock) (dock.Status, error) { return dock.StatusLinked, nil }

// Exclusive: an api dock has no connection; any process serves it.
func (*Gateway) Exclusive() bool { return false }

func (*Gateway) QR(context.Context, identity.DockID) (string, error) { return "", nil }

func (*Gateway) Restore(context.Context, dock.Dock) error { return nil }

func (*Gateway) Running(identity.DockID) bool { return false }

func (*Gateway) RunningDocks() []identity.DockID { return nil }

func (*Gateway) Stop(identity.DockID) {}

func (*Gateway) Unlink(context.Context, dock.Dock) error { return nil }

func (*Gateway) NewMessageID(identity.DockID) (string, error) { return "api-" + uuid.NewString(), nil }

func (*Gateway) Send(context.Context, dock.Outgoing) error { return nil }
