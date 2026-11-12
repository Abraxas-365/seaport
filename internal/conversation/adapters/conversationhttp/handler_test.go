package conversationhttp_test

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Abraxas-365/seaport/internal/conversation"
	"github.com/Abraxas-365/seaport/internal/conversation/adapters/conversationhttp"
	"github.com/Abraxas-365/seaport/internal/httpx"
	"github.com/Abraxas-365/seaport/internal/identity"
	"github.com/Abraxas-365/seaport/internal/permission"
	"github.com/Abraxas-365/seaport/internal/query"
	"github.com/gofiber/fiber/v3"
)

var org = identity.MustParseOrganizationID("00000000-0000-4000-8000-000000000001")

// fake records what the handler asked of the ports.
type fake struct {
	conversation.Commands
	conversation.Queries
	conversation.CustomsCommands
	conversation.CustomsQueries
	calls []string
	list  conversation.Filter
}

func (f *fake) List(_ context.Context, o identity.OrganizationID, filter conversation.Filter, p query.Pagination) (query.Paginated[conversation.Conversation], error) {
	f.list = filter
	f.calls = append(f.calls, "list "+o.String())
	return query.Paginated[conversation.Conversation]{Items: []conversation.Conversation{}}, nil
}

func (f *fake) Find(_ context.Context, _ identity.OrganizationID, id identity.ConversationID) (conversation.Conversation, error) {
	f.calls = append(f.calls, "find "+id.String())
	return conversation.Conversation{ID: id}, nil
}

func (f *fake) AddRule(_ context.Context, _ identity.OrganizationID, in conversation.AddRule) (conversation.Rule, error) {
	f.calls = append(f.calls, "addrule "+in.Pattern)
	return conversation.Rule{ID: identity.NewRuleID(), Dock: in.Dock, Effect: in.Effect, Pattern: in.Pattern}, nil
}

func (f *fake) RemoveRule(_ context.Context, _ identity.OrganizationID, id identity.RuleID) error {
	f.calls = append(f.calls, "removerule "+id.String())
	return nil
}

func newApp(f *fake, perms []string) *fiber.App {
	app := fiber.New(fiber.Config{ErrorHandler: httpx.ErrorHandler, Immutable: true})
	app.Use(func(c fiber.Ctx) error {
		httpx.SetPrincipal(c, httpx.Principal{Organization: org, Role: httpx.RoleAdmin, Subject: "t", Permissions: perms})
		return c.Next()
	})
	conversationhttp.New(f, f, f, f).Register(app)
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

func TestConversationRoutes(t *testing.T) {
	f := &fake{}
	app := newApp(f, permission.All)
	dock := identity.NewDockID()

	resp, body := call(t, app, "GET", "/api/v1/conversations?status=pending&dock_id="+dock.String(), "")
	if resp.StatusCode != 200 || !strings.Contains(body, `"items":[]`) || f.list.Status == nil || *f.list.Status != "pending" || *f.list.Dock != dock {
		t.Errorf("list: %d %s %+v", resp.StatusCode, body, f.list)
	}
	if resp, body := call(t, app, "GET", "/api/v1/conversations?dock_id=nope", ""); resp.StatusCode != 400 || !strings.Contains(body, `"error"`) {
		t.Errorf("bad dock id: %d %s", resp.StatusCode, body)
	}
	if resp, _ := call(t, app, "GET", "/api/v1/conversations?limit=0", ""); resp.StatusCode != 400 {
		t.Errorf("bad limit: %d", resp.StatusCode)
	}
	id := identity.NewConversationID()
	if resp, body := call(t, app, "GET", "/api/v1/conversations/"+id.String(), ""); resp.StatusCode != 200 || !strings.Contains(body, id.String()) {
		t.Errorf("find: %d %s", resp.StatusCode, body)
	}
	if resp, _ := call(t, app, "GET", "/api/v1/conversations/not-an-id", ""); resp.StatusCode != 400 {
		t.Errorf("bad conversation id: %d", resp.StatusCode)
	}

	resp, body = call(t, app, "POST", "/api/v1/docks/"+dock.String()+"/customs/rules", `{"effect":"deny","pattern":"+51999"}`)
	if resp.StatusCode != 201 || !strings.HasPrefix(resp.Header.Get("Location"), "/api/v1/customs/rules/") || !strings.Contains(body, `"pattern":"+51999"`) {
		t.Errorf("add rule: %d %q %s", resp.StatusCode, resp.Header.Get("Location"), body)
	}
	if resp, body := call(t, app, "POST", "/api/v1/docks/"+dock.String()+"/customs/rules", `{"effect":"deny","bogus":1}`); resp.StatusCode != 400 {
		t.Errorf("unknown field: %d %s", resp.StatusCode, body)
	}
	if resp, _ := call(t, app, "DELETE", "/api/v1/customs/rules/"+identity.NewRuleID().String(), ""); resp.StatusCode != 204 {
		t.Errorf("remove rule: %d", resp.StatusCode)
	}
	if resp, _ := call(t, app, "PUT", "/api/v1/conversations", ""); resp.StatusCode != 405 {
		t.Errorf("wrong method: %d", resp.StatusCode)
	}
}

func TestConversationRoutesRequirePermissions(t *testing.T) {
	f := &fake{}
	app := newApp(f, []string{permission.ConversationsRead})
	if resp, _ := call(t, app, "GET", "/api/v1/conversations", ""); resp.StatusCode != 200 {
		t.Errorf("read with read: %d", resp.StatusCode)
	}
	for _, c := range []struct{ method, path string }{
		{"PATCH", "/api/v1/conversations/" + identity.NewConversationID().String()},
		{"GET", "/api/v1/docks/" + identity.NewDockID().String() + "/customs"},
		{"DELETE", "/api/v1/customs/rules/" + identity.NewRuleID().String()},
	} {
		if resp, _ := call(t, app, c.method, c.path, `{}`); resp.StatusCode != 403 {
			t.Errorf("%s %s without its permission: %d", c.method, c.path, resp.StatusCode)
		}
	}
	if len(f.calls) != 1 {
		t.Errorf("a refused call reached the service: %v", f.calls)
	}
}
