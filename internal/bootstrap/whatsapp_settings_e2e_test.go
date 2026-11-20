package bootstrap_test

import (
	"net/http"
	"testing"

	"github.com/Abraxas-365/seaport/internal/bootstrap"
	"github.com/Abraxas-365/seaport/internal/config"
	"github.com/Abraxas-365/seaport/internal/dock"
	"github.com/Abraxas-365/seaport/internal/dock/adapters/dockwa"
)

// A WhatsApp dock's settings (what it does to a message an agent has: the
// reaction and the read receipt) are off with 👀 until the owner changes
// them; a PUT changes only what it names and answers them all; a bad emoji
// or an empty body is 400 and another channel's dock is 422.
func TestWhatsAppDockSettings(t *testing.T) {
	w := newWorld(t)
	p := startPortWith(t, func(*config.Server) {},
		bootstrap.Options{Gateways: []dock.Gateway{exclusiveGateway{w: w, name: "a"}}})
	var d dock.Dock
	if code := p.admin(http.MethodPost, "/api/v1/docks", map[string]string{"channel": "whatsapp", "name": "wa"}, &d); code != http.StatusCreated {
		t.Fatalf("create dock: %d", code)
	}
	path := "/api/v1/docks/" + d.ID.String() + "/whatsapp"

	var got dockwa.Settings
	if code := p.admin(http.MethodGet, path, nil, &got); code != http.StatusOK || got != dockwa.DefaultSettings() {
		t.Fatalf("defaults: %d %+v", code, got)
	}
	if code := p.admin(http.MethodPut, path, map[string]any{"react": true, "mark_read": true}, &got); code != http.StatusOK ||
		got != (dockwa.Settings{React: true, Emoji: "👀", MarkRead: true}) {
		t.Fatalf("both on: %d %+v", code, got)
	}
	if code := p.admin(http.MethodPut, path, map[string]any{"emoji": "✅"}, &got); code != http.StatusOK ||
		got != (dockwa.Settings{React: true, Emoji: "✅", MarkRead: true}) {
		t.Fatalf("emoji: %d %+v", code, got)
	}
	for _, bad := range []map[string]any{{}, {"emoji": "ok"}, {"emoji": "👀👀"}, {"emoji": ""}} {
		if code := p.admin(http.MethodPut, path, bad, nil); code != http.StatusBadRequest {
			t.Errorf("PUT %v: %d, want 400", bad, code)
		}
	}
	if code := p.admin(http.MethodGet, path, nil, &got); code != http.StatusOK || got.Emoji != "✅" || !got.React || !got.MarkRead {
		t.Fatalf("refused PUTs changed nothing: %+v", got)
	}

	api := p.apiDock("api")
	if code := p.admin(http.MethodGet, "/api/v1/docks/"+api.ID.String()+"/whatsapp", nil, nil); code != http.StatusUnprocessableEntity {
		t.Fatalf("an api dock: %d", code)
	}
	if code := p.admin(http.MethodPut, "/api/v1/docks/"+api.ID.String()+"/whatsapp", map[string]any{"react": true}, nil); code != http.StatusUnprocessableEntity {
		t.Fatalf("PUT on an api dock: %d", code)
	}
}
