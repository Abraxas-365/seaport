package shiphttp_test

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Abraxas-365/seaport/internal/conversation"
	"github.com/Abraxas-365/seaport/internal/dock"
	"github.com/Abraxas-365/seaport/internal/httpx"
	"github.com/Abraxas-365/seaport/internal/identity"
	"github.com/Abraxas-365/seaport/internal/permission"
	"github.com/Abraxas-365/seaport/internal/query"
	"github.com/Abraxas-365/seaport/internal/ship"
	"github.com/Abraxas-365/seaport/internal/ship/adapters/shiphttp"
	"github.com/gofiber/fiber/v3"
)

var org = identity.MustParseOrganizationID("00000000-0000-4000-8000-000000000001")

type fake struct {
	ship.Commands
	ship.Queries
	docks   *fakeDocks
	last    ship.Inbound
	created bool
	file    ship.AttachmentFile
}

type fakeDocks struct {
	dock.Queries
	channel dock.Channel
}

func (f *fakeDocks) Find(_ context.Context, _ identity.OrganizationID, id identity.DockID) (dock.Dock, error) {
	return dock.Dock{ID: id, Channel: f.channel}, nil
}

func (f *fake) Receive(_ context.Context, _ identity.OrganizationID, in ship.Inbound) (ship.Ship, bool, error) {
	f.last = in
	return ship.Ship{ID: identity.NewShipID(), Text: in.Text}, f.created, nil
}

func (f *fake) Send(_ context.Context, _ identity.OrganizationID, out ship.Outbound) (ship.Ship, error) {
	return ship.Ship{ID: identity.NewShipID(), Conversation: out.Conversation, Text: out.Text}, nil
}

func (f *fake) Inbox(context.Context, identity.OrganizationID, conversation.Filter, query.Pagination) (query.Paginated[ship.InboxItem], error) {
	last := ship.Ship{Text: "secret preview"}
	return query.Paginated[ship.InboxItem]{Items: []ship.InboxItem{{Summary: ship.Summary{Last: &last}}}}, nil
}

func (f *fake) OpenAttachment(context.Context, identity.OrganizationID, ship.OpenAttachment) (ship.AttachmentFile, error) {
	return f.file, nil
}

func newApp(f *fake, perms []string) *fiber.App {
	app := fiber.New(fiber.Config{ErrorHandler: httpx.ErrorHandler, Immutable: true})
	app.Use(func(c fiber.Ctx) error {
		httpx.SetPrincipal(c, httpx.Principal{Organization: org, Role: httpx.RoleAdmin, Subject: "t", Permissions: perms})
		return c.Next()
	})
	shiphttp.New(f, f, f.docks).Register(app)
	return app
}

func call(t *testing.T, app *fiber.App, method, path, body string) (*http.Response, string) {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := app.Test(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp, string(b)
}

func TestReceiveInjectsIntoAPIDocks(t *testing.T) {
	f := &fake{docks: &fakeDocks{channel: dock.ChannelAPI}, created: true}
	app := newApp(f, permission.All)
	path := "/api/v1/docks/" + identity.NewDockID().String() + "/ships"
	resp, body := call(t, app, "POST", path, `{"chat_id":"c1","message_id":"m1","sender_id":"s1","text":"hola","images":[{"name":"a.png","data":"aGk="}]}`)
	if resp.StatusCode != 201 || !strings.Contains(body, `"duplicate":false`) {
		t.Fatalf("receive: %d %s", resp.StatusCode, body)
	}
	if f.last.ChatKind != conversation.KindDirect || f.last.ExternalID != "m1" || len(f.last.Media) != 1 || string(f.last.Media[0].Data) != "hi" {
		t.Errorf("inbound: %+v", f.last)
	}
	f.created = false
	if resp, body := call(t, app, "POST", path, `{"chat_id":"c1","message_id":"m1","sender_id":"s1","text":"hola"}`); resp.StatusCode != 200 || !strings.Contains(body, `"duplicate":true`) {
		t.Errorf("duplicate: %d %s", resp.StatusCode, body)
	}
	f.docks.channel = dock.ChannelWhatsApp
	if resp, body := call(t, app, "POST", path, `{"chat_id":"c1"}`); resp.StatusCode != 422 && resp.StatusCode != 409 && resp.StatusCode != 400 || !strings.Contains(body, "api docks") {
		t.Errorf("not an api dock: %d %s", resp.StatusCode, body)
	}
}

func TestSendCreatedLocation(t *testing.T) {
	app := newApp(&fake{docks: &fakeDocks{}}, permission.All)
	conv := identity.NewConversationID().String()
	resp, body := call(t, app, "POST", "/api/v1/conversations/"+conv+"/ships", `{"text":"hi"}`)
	if resp.StatusCode != 201 || !strings.HasPrefix(resp.Header.Get("Location"), "/api/v1/conversations/"+conv+"/ships/") {
		t.Errorf("send: %d %q %s", resp.StatusCode, resp.Header.Get("Location"), body)
	}
}

func TestInboxHidesPreviewsFromCallersWhoCannotReadShips(t *testing.T) {
	_, body := call(t, newApp(&fake{docks: &fakeDocks{}}, []string{permission.ConversationsRead}), "GET", "/api/v1/inbox", "")
	if strings.Contains(body, "secret preview") || !strings.Contains(body, `"last_ship":null`) {
		t.Errorf("preview leaked: %s", body)
	}
	_, body = call(t, newApp(&fake{docks: &fakeDocks{}}, permission.All), "GET", "/api/v1/inbox", "")
	if !strings.Contains(body, "secret preview") {
		t.Errorf("preview missing for a reader: %s", body)
	}
}

func TestAttachmentHeaders(t *testing.T) {
	f := &fake{docks: &fakeDocks{}, file: ship.AttachmentFile{Attachment: ship.Attachment{Kind: ship.AttachmentImage, MIME: "image/png"}, Data: []byte("png")}}
	app := newApp(f, permission.All)
	path := "/api/v1/attachments/" + identity.NewAttachmentID().String()
	resp, body := call(t, app, "GET", path, "")
	if resp.StatusCode != 200 || body != "png" || resp.Header.Get("Content-Type") != "image/png" ||
		resp.Header.Get("X-Content-Type-Options") != "nosniff" || resp.Header.Get("Cache-Control") != "private, no-store" {
		t.Errorf("image: %d %q %v", resp.StatusCode, body, resp.Header)
	}
	f.file = ship.AttachmentFile{Attachment: ship.Attachment{Kind: ship.AttachmentDocument, MIME: "application/octet-stream", Name: "r.pdf"}, Data: []byte("doc")}
	resp, _ = call(t, app, "GET", path, "")
	if !strings.HasPrefix(resp.Header.Get("Content-Disposition"), "attachment") {
		t.Errorf("document disposition: %q", resp.Header.Get("Content-Disposition"))
	}
}

func TestShipRoutesRequirePermissions(t *testing.T) {
	app := newApp(&fake{docks: &fakeDocks{}}, []string{permission.ConversationsRead})
	conv := identity.NewConversationID().String()
	for _, c := range []struct{ method, path string }{
		{"GET", "/api/v1/conversations/" + conv + "/ships"},
		{"GET", "/api/v1/conversations/" + conv + "/turns"},
		{"POST", "/api/v1/conversations/" + conv + "/ships"},
		{"POST", "/api/v1/docks/" + identity.NewDockID().String() + "/ships"},
		{"GET", "/api/v1/attachments/" + identity.NewAttachmentID().String()},
	} {
		if resp, _ := call(t, app, c.method, c.path, `{}`); resp.StatusCode != 403 {
			t.Errorf("%s %s: %d", c.method, c.path, resp.StatusCode)
		}
	}
}
