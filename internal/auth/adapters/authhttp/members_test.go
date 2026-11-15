package authhttp_test

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gofiber/fiber/v3"

	"github.com/Abraxas-365/seaport/internal/auth"
	"github.com/Abraxas-365/seaport/internal/auth/adapters/authhttp"
	"github.com/Abraxas-365/seaport/internal/errx"
	"github.com/Abraxas-365/seaport/internal/httpx"
	"github.com/Abraxas-365/seaport/internal/identity"
	"github.com/Abraxas-365/seaport/internal/permission"
)

var org = identity.MustParseOrganizationID("00000000-0000-4000-8000-000000000001")

// fakeSessions resolves the cookie "sid" to a session holding "session-token".
type fakeSessions struct{ auth.Commands }

func (fakeSessions) Authenticate(_ context.Context, id string) (auth.Identity, auth.Session, error) {
	if id != "sid" {
		return auth.Identity{}, auth.Session{}, errx.Unauthorized("no session")
	}
	return auth.Identity{}, auth.Session{Tokens: auth.Tokens{Access: "session-token"}}, nil
}

// fakeMembers records the Actor and the input of every call.
type fakeMembers struct {
	actor    auth.Actor
	org      identity.OrganizationID
	user     string
	access   auth.SetAccess
	invite   auth.Invite
	key      auth.NewAPIKey
	removed  string
	revoked  string
	resent   string
	revokedK string
}

func (f *fakeMembers) see(o identity.OrganizationID, a auth.Actor) { f.org, f.actor = o, a }

func (f *fakeMembers) SetAccess(_ context.Context, o identity.OrganizationID, a auth.Actor, user string, in auth.SetAccess) (auth.Member, error) {
	f.see(o, a)
	f.user, f.access = user, in
	return auth.Member{ID: user, Role: in.Role}, nil
}
func (f *fakeMembers) Remove(_ context.Context, o identity.OrganizationID, a auth.Actor, user string) error {
	f.see(o, a)
	f.removed = user
	return nil
}
func (f *fakeMembers) Invite(_ context.Context, o identity.OrganizationID, a auth.Actor, in auth.Invite) (auth.Invitation, error) {
	f.see(o, a)
	f.invite = in
	return auth.Invitation{ID: "inv-1", Email: in.Email, Link: "https://iam.example.com/accept"}, nil
}
func (f *fakeMembers) Resend(_ context.Context, o identity.OrganizationID, a auth.Actor, id string) (auth.Invitation, error) {
	f.see(o, a)
	f.resent = id
	return auth.Invitation{ID: id}, nil
}
func (f *fakeMembers) RevokeInvitation(_ context.Context, o identity.OrganizationID, a auth.Actor, id string) error {
	f.see(o, a)
	f.revoked = id
	return nil
}
func (f *fakeMembers) CreateAPIKey(_ context.Context, o identity.OrganizationID, a auth.Actor, in auth.NewAPIKey) (auth.IssuedAPIKey, error) {
	f.see(o, a)
	f.key = in
	return auth.IssuedAPIKey{APIKey: auth.APIKey{ID: "key-1", Name: in.Name}, Value: "ik_key_example"}, nil
}
func (f *fakeMembers) RevokeAPIKey(_ context.Context, o identity.OrganizationID, a auth.Actor, id string) error {
	f.see(o, a)
	f.revokedK = id
	return nil
}
func (f *fakeMembers) Access(_ context.Context, o identity.OrganizationID, a auth.Actor) (auth.Access, error) {
	f.see(o, a)
	return auth.Access{Members: true}, nil
}
func (f *fakeMembers) Members(_ context.Context, o identity.OrganizationID, a auth.Actor) ([]auth.Member, error) {
	f.see(o, a)
	return nil, nil
}
func (f *fakeMembers) Invitations(_ context.Context, o identity.OrganizationID, a auth.Actor) ([]auth.Invitation, error) {
	f.see(o, a)
	return nil, nil
}
func (f *fakeMembers) APIKeys(_ context.Context, o identity.OrganizationID, a auth.Actor) ([]auth.APIKey, error) {
	f.see(o, a)
	return nil, nil
}

func newApp(f *fakeMembers, p httpx.Principal) *fiber.App {
	app := fiber.New(fiber.Config{ErrorHandler: httpx.ErrorHandler, Immutable: true})
	app.Use(func(c fiber.Ctx) error {
		if p.Subject != "" {
			p.Organization = org
			httpx.SetPrincipal(c, p)
		}
		return c.Next()
	})
	authhttp.NewMembers(fakeSessions{}, f, f).Register(app)
	return app
}

func person() httpx.Principal {
	return httpx.Principal{Role: httpx.RoleAdmin, Subject: "user-1", Permissions: []string{permission.DocksRead}}
}

func do(t *testing.T, app *fiber.App, method, path, body string, hdr map[string]string) (*http.Response, string) {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	res, err := app.Test(req)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(res.Body)
	_ = res.Body.Close()
	return res, string(b)
}

var cookie = map[string]string{"Cookie": httpx.SessionCookie + "=sid"}

func TestMembersActsAsTheSessionsPerson(t *testing.T) {
	f := &fakeMembers{}
	app := newApp(f, person())
	res, _ := do(t, app, "GET", "/api/v1/members/access", "", cookie)
	if res.StatusCode != 200 {
		t.Fatalf("status = %d", res.StatusCode)
	}
	if f.actor.Token != "session-token" || f.actor.Subject != "user-1" || f.org != org ||
		len(f.actor.Permissions) != 1 || f.actor.Permissions[0] != permission.DocksRead {
		t.Fatalf("actor = %+v, org %v", f.actor, f.org)
	}
}

func TestMembersActsAsTheBearersPerson(t *testing.T) {
	f := &fakeMembers{}
	app := newApp(f, person())
	res, _ := do(t, app, "GET", "/api/v1/members", "", map[string]string{"Authorization": "Bearer user-access-token"})
	if res.StatusCode != 200 || f.actor.Token != "user-access-token" {
		t.Fatalf("status %d, actor %+v", res.StatusCode, f.actor)
	}
}

func TestMembersRefuseAPIKeysAndAnonymous(t *testing.T) {
	f := &fakeMembers{}
	key := person()
	key.Subject = auth.APIKeySubjectPrefix + "key-1"
	res, body := do(t, newApp(f, key), "GET", "/api/v1/members", "", map[string]string{"Authorization": "Bearer ik_key_example"})
	if res.StatusCode != 403 || !strings.Contains(body, "sign in as a person") {
		t.Fatalf("API key: %d %s", res.StatusCode, body)
	}
	if f.actor.Token != "" {
		t.Fatal("an API key reached the service")
	}
	res, _ = do(t, newApp(f, httpx.Principal{}), "GET", "/api/v1/members", "", nil)
	if res.StatusCode != 401 {
		t.Fatalf("anonymous: %d", res.StatusCode)
	}
	// Not an administrator of Seaport (a crew principal): 403 before anything runs.
	crew := person()
	crew.Role = httpx.Role("crew")
	res, _ = do(t, newApp(f, crew), "GET", "/api/v1/members", "", cookie)
	if res.StatusCode != 403 {
		t.Fatalf("non-admin: %d", res.StatusCode)
	}
}

func TestMembersRoutesPassTheirInput(t *testing.T) {
	f := &fakeMembers{}
	app := newApp(f, person())
	if res, _ := do(t, app, "PUT", "/api/v1/members/user-9", `{"role":"Operator","manages_members":true}`, cookie); res.StatusCode != 200 ||
		f.user != "user-9" || f.access.Role != "Operator" || !f.access.ManagesMembers {
		t.Fatalf("set access: %d %+v %q", res.StatusCode, f.access, f.user)
	}
	if res, _ := do(t, app, "DELETE", "/api/v1/members/user-9", "", cookie); res.StatusCode != 204 || f.removed != "user-9" {
		t.Fatalf("remove: %d %q", res.StatusCode, f.removed)
	}
	res, body := do(t, app, "POST", "/api/v1/members/invitations", `{"email":"ana@example.com","role":"Viewer"}`, cookie)
	if res.StatusCode != 201 || f.invite.Email != "ana@example.com" || !strings.Contains(body, "https://iam.example.com/accept") {
		t.Fatalf("invite: %d %s", res.StatusCode, body)
	}
	if res, _ := do(t, app, "POST", "/api/v1/members/invitations/inv-1/resend", "", cookie); res.StatusCode != 200 || f.resent != "inv-1" {
		t.Fatalf("resend: %d", res.StatusCode)
	}
	if res, _ := do(t, app, "DELETE", "/api/v1/members/invitations/inv-1", "", cookie); res.StatusCode != 204 || f.revoked != "inv-1" {
		t.Fatalf("revoke invitation: %d", res.StatusCode)
	}
	res, body = do(t, app, "POST", "/api/v1/members/api-keys", `{"name":"ci","role":"Viewer","expires_in":"24h"}`, cookie)
	if res.StatusCode != 201 || f.key.ExpiresIn != "24h" || !strings.Contains(body, "ik_key_example") || res.Header.Get("Cache-Control") != "no-store" {
		t.Fatalf("create key: %d %s cache=%q", res.StatusCode, body, res.Header.Get("Cache-Control"))
	}
	if res, _ := do(t, app, "DELETE", "/api/v1/members/api-keys/key-1", "", cookie); res.StatusCode != 204 || f.revokedK != "key-1" {
		t.Fatalf("revoke key: %d", res.StatusCode)
	}
	for _, path := range []string{"/api/v1/members", "/api/v1/members/invitations", "/api/v1/members/api-keys"} {
		res, body := do(t, app, "GET", path, "", cookie)
		if res.StatusCode != 200 || !strings.Contains(body, `"items":[]`) {
			t.Errorf("GET %s: %d %s (an empty list is [], not null)", path, res.StatusCode, body)
		}
	}
}

func TestMembersRejectOddIDsAndBadBodies(t *testing.T) {
	f := &fakeMembers{}
	app := newApp(f, person())
	for _, path := range []string{"/api/v1/members/a%2Fb", "/api/v1/members/" + strings.Repeat("a", 200), "/api/v1/members/a.b"} {
		if res, _ := do(t, app, "DELETE", path, "", cookie); res.StatusCode != 404 {
			t.Errorf("DELETE %s: %d, want 404", path, res.StatusCode)
		}
	}
	if f.removed != "" {
		t.Fatalf("service reached with %q", f.removed)
	}
	if res, _ := do(t, app, "POST", "/api/v1/members/invitations", `{"email":`, cookie); res.StatusCode != 400 {
		t.Fatalf("bad JSON: %d", res.StatusCode)
	}
}
