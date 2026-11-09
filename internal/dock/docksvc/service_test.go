package docksvc_test

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"

	"github.com/Abraxas-365/seaport/internal/dock"
	"github.com/Abraxas-365/seaport/internal/dock/docksvc"
	"github.com/Abraxas-365/seaport/internal/errx"
	"github.com/Abraxas-365/seaport/internal/identity"
	"github.com/Abraxas-365/seaport/internal/query"
)

const secretQR = "2@pairing-secret"

var org = identity.MustParseOrganizationID("00000000-0000-4000-8000-000000000001")

// memRepo is a dock.Repository in memory (one organization).
type memRepo struct {
	mu    sync.Mutex
	docks map[identity.DockID]dock.Dock
}

func (r *memRepo) Create(_ context.Context, o identity.OrganizationID, id identity.DockID, in dock.Create, st dock.Status, _ int) (dock.Dock, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	d := dock.Dock{ID: id, Organization: o, Channel: in.Channel, Name: in.Name, Status: st}
	r.docks[id] = d
	return d, nil
}

func (r *memRepo) Find(ctx context.Context, _ identity.OrganizationID, id identity.DockID) (dock.Dock, error) {
	return r.Get(ctx, id)
}

func (r *memRepo) List(context.Context, identity.OrganizationID, query.Pagination) (query.Paginated[dock.Dock], error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out query.Paginated[dock.Dock]
	for _, d := range r.docks {
		out.Items = append(out.Items, d)
	}
	return out, nil
}

func (r *memRepo) Update(_ context.Context, _ identity.OrganizationID, id identity.DockID, in dock.Update) (dock.Dock, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	d := r.docks[id]
	d.Name = *in.Name
	r.docks[id] = d
	return d, nil
}

func (r *memRepo) Delete(_ context.Context, _ identity.OrganizationID, id identity.DockID) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.docks, id)
	return nil
}

func (r *memRepo) Get(_ context.Context, id identity.DockID) (dock.Dock, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	d, ok := r.docks[id]
	if !ok {
		return d, errx.NotFound("dock not found")
	}
	return d, nil
}

func (r *memRepo) SetStatus(_ context.Context, id identity.DockID, st dock.Status) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	d := r.docks[id]
	d.Status = st
	r.docks[id] = d
	return nil
}

func (r *memRepo) SetLinked(_ context.Context, id identity.DockID, address string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	d := r.docks[id]
	d.Status, d.Address = dock.StatusLinked, address
	r.docks[id] = d
	return nil
}

// qrGateway is a WhatsApp-like gateway that always has a pairing code.
type qrGateway struct{}

func (qrGateway) Channel() dock.Channel { return dock.ChannelWhatsApp }
func (qrGateway) Exclusive() bool       { return true }
func (qrGateway) Link(context.Context, dock.Dock) (dock.Status, error) {
	return dock.StatusLinking, nil
}
func (qrGateway) QR(context.Context, identity.DockID) (string, error) { return secretQR, nil }
func (qrGateway) Restore(context.Context, dock.Dock) error            { return nil }
func (qrGateway) Running(identity.DockID) bool                        { return false }
func (qrGateway) RunningDocks() []identity.DockID                     { return nil }
func (qrGateway) Stop(identity.DockID)                                {}
func (qrGateway) Unlink(context.Context, dock.Dock) error             { return nil }
func (qrGateway) NewMessageID(identity.DockID) (string, error)        { return "m", nil }
func (qrGateway) Send(context.Context, dock.Outgoing) error           { return nil }

type recorder struct {
	mu       sync.Mutex
	payloads []string
}

func (r *recorder) Publish(_ identity.OrganizationID, typ string, data any) {
	raw, _ := json.Marshal(data)
	r.mu.Lock()
	defer r.mu.Unlock()
	r.payloads = append(r.payloads, typ+" "+string(raw))
}

// TestEventsNeverCarryTheQR: events are stored in clear (the event log),
// so the pairing code only comes back from the API.
func TestEventsNeverCarryTheQR(t *testing.T) {
	ctx := context.Background()
	events := &recorder{}
	s := docksvc.New(&memRepo{docks: map[identity.DockID]dock.Dock{}}, events, 0, docksvc.Cluster{}, qrGateway{})

	d, err := s.Create(ctx, org, dock.Create{Channel: dock.ChannelWhatsApp, Name: "ventas"})
	if err != nil {
		t.Fatal(err)
	}
	if d.QRCode != secretQR {
		t.Fatalf("Create must return the QR: %+v", d)
	}
	name := "ventas 2"
	if d, err = s.Update(ctx, org, d.ID, dock.Update{Name: &name}); err != nil || d.QRCode != secretQR {
		t.Fatalf("Update must return the QR: %+v %v", d, err)
	}
	if got, err := s.Find(ctx, org, d.ID); err != nil || got.QRCode != secretQR {
		t.Fatalf("Find must return the QR: %+v %v", got, err)
	}
	s.PairingChanged(ctx, d.ID)
	if err := s.SetStatus(ctx, d.ID, dock.StatusLinking); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Relink(ctx, org, d.ID); err != nil {
		t.Fatal(err)
	}

	if len(events.payloads) < 5 {
		t.Fatalf("expected an event per change, got %d", len(events.payloads))
	}
	for _, p := range events.payloads {
		if !strings.HasPrefix(p, dock.EventUpdated+" ") {
			t.Fatalf("unexpected event %s", p)
		}
		if strings.Contains(p, secretQR) || strings.Contains(p, "qr_code") {
			t.Fatalf("an event carries the pairing code: %s", p)
		}
	}
}
