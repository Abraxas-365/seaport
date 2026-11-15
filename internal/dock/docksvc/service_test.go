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
	dests map[identity.DockID]map[string]dock.Destination
}

func (r *memRepo) Create(_ context.Context, o identity.OrganizationID, id identity.DockID, in dock.Create, st dock.Status, _ int) (dock.Dock, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	d := dock.Dock{ID: id, Organization: o, Channel: in.Channel, Name: in.Name, Status: st, Replies: true, Tools: dock.AllTools}
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
	if in.Name != nil {
		d.Name = *in.Name
	}
	if in.Replies != nil {
		d.Replies = *in.Replies
	}
	if in.Tools != nil {
		d.Tools = in.Tools.Apply(d.Tools)
	}
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

func (r *memRepo) PutDestination(_ context.Context, o identity.OrganizationID, in dock.AddDestination, _ int) (dock.Destination, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if d, ok := r.docks[in.Dock]; !ok || d.Organization != o {
		return dock.Destination{}, errx.NotFound("dock not found")
	}
	if r.dests == nil {
		r.dests = map[identity.DockID]map[string]dock.Destination{}
	}
	if r.dests[in.Dock] == nil {
		r.dests[in.Dock] = map[string]dock.Destination{}
	}
	d := dock.Destination{Organization: o, Dock: in.Dock, Name: in.Name, Target: in.Target, Title: in.Title}
	r.dests[in.Dock][in.Name] = d
	return d, nil
}

func (r *memRepo) RemoveDestination(_ context.Context, _ identity.OrganizationID, id identity.DockID, name string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.dests[id][name]; !ok {
		return errx.NotFound("destination not found")
	}
	delete(r.dests[id], name)
	return nil
}

func (r *memRepo) Destinations(_ context.Context, _ identity.OrganizationID, id identity.DockID) ([]dock.Destination, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := []dock.Destination{}
	for _, d := range r.dests[id] {
		out = append(out, d)
	}
	return out, nil
}

func (r *memRepo) Destination(_ context.Context, _ identity.OrganizationID, id identity.DockID, name string) (dock.Destination, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	d, ok := r.dests[id][name]
	if !ok {
		return dock.Destination{}, errx.NotFound("destination not found")
	}
	return d, nil
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
func (qrGateway) Open(_ context.Context, o dock.Opening) (dock.Opened, error) {
	return dock.Opened{ChatID: o.Target, Kind: "direct", Handles: []string{o.Target}}, nil
}

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

// With replies off a dock is a trigger: a message id for an outbound
// (what shipsvc asks before storing a reply) and a send are refused with
// a Business error; on again, both work.
func TestRepliesOffRefusesOutbound(t *testing.T) {
	ctx := context.Background()
	s := docksvc.New(&memRepo{docks: map[identity.DockID]dock.Dock{}}, &recorder{}, 0, docksvc.Cluster{}, qrGateway{})
	d, err := s.Create(ctx, org, dock.Create{Channel: dock.ChannelWhatsApp, Name: "gh"})
	if err != nil {
		t.Fatal(err)
	}
	if !d.Replies || !d.CanReply() {
		t.Fatalf("a new dock takes replies: %+v", d)
	}
	if _, err := s.Update(ctx, org, d.ID, dock.Update{}); !errx.IsType(err, errx.TypeValidation) {
		t.Fatalf("an empty update is refused: %v", err)
	}
	off := false
	if d, err = s.Update(ctx, org, d.ID, dock.Update{Replies: &off}); err != nil || d.Replies {
		t.Fatalf("replies off: %+v %v", d, err)
	}
	if _, err := s.NewMessageID(ctx, org, d.ID); !errx.IsType(err, errx.TypeBusiness) {
		t.Fatalf("NewMessageID on a trigger dock: %v", err)
	}
	if err := s.Send(ctx, org, dock.Outgoing{Dock: d.ID, ChatID: "c", MessageID: "m", Text: "hola"}); !errx.IsType(err, errx.TypeBusiness) {
		t.Fatalf("Send on a trigger dock: %v", err)
	}
	on := true
	if d, err = s.Update(ctx, org, d.ID, dock.Update{Replies: &on}); err != nil || !d.Replies {
		t.Fatalf("replies on: %+v %v", d, err)
	}
	if _, err := s.NewMessageID(ctx, org, d.ID); err != nil {
		t.Fatalf("NewMessageID after replies on: %v", err)
	}
}

// The agent tool switches change only what a patch names; an empty patch
// is no update.
func TestDockToolsPatch(t *testing.T) {
	ctx := context.Background()
	s := docksvc.New(&memRepo{docks: map[identity.DockID]dock.Dock{}}, &recorder{}, 0, docksvc.Cluster{}, qrGateway{})
	d, err := s.Create(ctx, org, dock.Create{Channel: dock.ChannelWhatsApp, Name: "wa"})
	if err != nil || d.Tools != dock.AllTools {
		t.Fatalf("a new dock: %+v %v", d.Tools, err)
	}
	if _, err := s.Update(ctx, org, d.ID, dock.Update{Tools: &dock.ToolsPatch{}}); !errx.IsType(err, errx.TypeValidation) {
		t.Fatalf("an empty tools patch is refused: %v", err)
	}
	off := false
	if d, err = s.Update(ctx, org, d.ID, dock.Update{Tools: &dock.ToolsPatch{Conversations: &off}}); err != nil ||
		d.Tools != (dock.Tools{History: true, Attachments: true}) || !d.Replies {
		t.Fatalf("conversations off: %+v %v", d, err)
	}
}
