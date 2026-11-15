package bootstrap_test

import (
	"net/http"
	"strings"
	"testing"

	"github.com/Abraxas-365/seaport/internal/auth"
	"github.com/Abraxas-365/seaport/internal/permission"
	"github.com/Abraxas-365/seaport/internal/query"
)

// membersPage is what a member-administration response lists.
type membersList = query.Paginated[auth.Member]

func memberNamed(t *testing.T, list membersList, id string) auth.Member {
	t.Helper()
	for _, m := range list.Items {
		if m.ID == id {
			return m
		}
	}
	t.Fatalf("member %s not listed: %+v", id, list.Items)
	return auth.Member{}
}

func listed(list membersList, id string) bool {
	for _, m := range list.Items {
		if m.ID == id {
			return true
		}
	}
	return false
}

// TestIAMMembersLifecycle: an organization's administrator invites a person
// (the accept link comes back because no email delivery is configured),
// the person accepts and signs in, their role changes, an organization API
// key is issued, used and revoked, and the person is removed (their token
// stops at once). Everything runs as the administrator's own token.
func TestIAMMembersLifecycle(t *testing.T) {
	p, _ := startIAMPort(t)
	org := newIAMOrg(t)
	grantSeaport(t, org)
	owner := newIAMMemberAdmin(t, org, "Admin", true)
	ownerTok := accessToken(t, owner, org)

	var access auth.Access
	if code := p.call(http.MethodGet, "/api/v1/members/access", ownerTok, nil, &access); code != http.StatusOK {
		t.Fatalf("access: %d", code)
	}
	if access != (auth.Access{Members: true, Invite: true, AssignRoles: true, Remove: true, KeysRead: true, KeysWrite: true}) {
		t.Fatalf("administrator's access = %+v", access)
	}

	var list membersList
	if code := p.call(http.MethodGet, "/api/v1/members", ownerTok, nil, &list); code != http.StatusOK {
		t.Fatalf("members: %d", code)
	}
	me := memberNamed(t, list, owner.ID)
	if !me.You || me.Role != "Admin" || !me.ManagesMembers || me.Email != owner.Email {
		t.Fatalf("the administrator as listed: %+v", me)
	}

	// Invite: no delivery configured, so the link is handed back.
	inviteeEmail := "nueva@example.com"
	var inv auth.Invitation
	if code := p.call(http.MethodPost, "/api/v1/members/invitations", ownerTok,
		map[string]any{"email": inviteeEmail, "role": "Operator"}, &inv); code != http.StatusCreated {
		t.Fatalf("invite: %d", code)
	}
	if inv.ID == "" || inv.Link == "" || inv.Role != "Operator" || inv.Status != "pending" {
		t.Fatalf("invitation = %+v", inv)
	}
	var pending query.Paginated[auth.Invitation]
	if code := p.call(http.MethodGet, "/api/v1/members/invitations", ownerTok, nil, &pending); code != http.StatusOK ||
		len(pending.Items) != 1 || pending.Items[0].ID != inv.ID || pending.Items[0].Role != "Operator" || pending.Items[0].Link != "" {
		t.Fatalf("invitations: %d %+v (a listing never carries the link)", code, pending.Items)
	}
	// Resend gives a fresh link; the first token stops working.
	var again auth.Invitation
	if code := p.call(http.MethodPost, "/api/v1/members/invitations/"+inv.ID+"/resend", ownerTok, nil, &again); code != http.StatusOK ||
		again.Link == "" || again.Link == inv.Link {
		t.Fatalf("resend: %d %+v", code, again)
	}

	// The invitee accepts and signs in with the Operator role.
	invitee := acceptInvitation(t, again.Link, inviteeEmail)
	inviteeTok := accessToken(t, invitee, org)
	if code := p.call(http.MethodGet, "/api/v1/docks", inviteeTok, nil, nil); code != http.StatusOK {
		t.Fatalf("the invitee reads docks: %d", code)
	}
	if code := p.call(http.MethodGet, "/api/v1/members", inviteeTok, nil, nil); code != http.StatusForbidden {
		t.Fatalf("an Operator without the companion role lists members: %d, want 403", code)
	}
	if code := p.call(http.MethodGet, "/api/v1/members", ownerTok, nil, &list); code != http.StatusOK {
		t.Fatalf("members: %d", code)
	}
	if m := memberNamed(t, list, invitee.ID); m.Role != "Operator" || m.ManagesMembers || m.You || !m.Active {
		t.Fatalf("the invitee as listed: %+v", m)
	}

	// Role change: Viewer who can also manage members.
	var changed auth.Member
	if code := p.call(http.MethodPut, "/api/v1/members/"+invitee.ID, ownerTok,
		map[string]any{"role": "Viewer", "manages_members": true}, &changed); code != http.StatusOK {
		t.Fatalf("set access: %d", code)
	}
	if changed.Role != "Viewer" || !changed.ManagesMembers {
		t.Fatalf("changed = %+v", changed)
	}
	// It reaches them at their next sign-in.
	fresh := accessToken(t, invitee, org)
	if code := p.call(http.MethodPost, "/api/v1/docks", fresh, map[string]string{"channel": "api", "name": "x"}, nil); code != http.StatusForbidden {
		t.Fatalf("a Viewer creates a dock: %d, want 403", code)
	}
	if code := p.call(http.MethodGet, "/api/v1/members/access", fresh, nil, &access); code != http.StatusOK || !access.Members || !access.Invite {
		t.Fatalf("the new member admin's access: %d %+v", code, access)
	}
	// Switching the companion role off closes the page again.
	if code := p.call(http.MethodPut, "/api/v1/members/"+invitee.ID, ownerTok, map[string]any{"role": "Viewer"}, &changed); code != http.StatusOK || changed.ManagesMembers {
		t.Fatalf("drop companion: %d %+v", code, changed)
	}

	// Nobody changes or removes themselves.
	if code := p.call(http.MethodPut, "/api/v1/members/"+owner.ID, ownerTok, map[string]any{"role": "Viewer"}, nil); code != http.StatusUnprocessableEntity {
		t.Fatalf("change own role: %d, want 422", code)
	}
	if code := p.call(http.MethodDelete, "/api/v1/members/"+owner.ID, ownerTok, nil, nil); code != http.StatusUnprocessableEntity {
		t.Fatalf("remove self: %d, want 422", code)
	}
	if code := p.call(http.MethodPut, "/api/v1/members/"+invitee.ID, ownerTok, map[string]any{"role": "Owner"}, nil); code != http.StatusBadRequest {
		t.Fatalf("unknown role: %d, want 400", code)
	}

	// Organization API keys: issued, shown once, used by the CLI, revoked.
	var key auth.IssuedAPIKey
	if code := p.call(http.MethodPost, "/api/v1/members/api-keys", ownerTok,
		map[string]any{"name": "ci", "role": "Viewer", "expires_in": "24h"}, &key); code != http.StatusCreated {
		t.Fatalf("create key: %d", code)
	}
	if !strings.HasPrefix(key.Value, "ik_key_") || key.Role != "Viewer" || key.ExpiresAt == nil {
		t.Fatalf("key = %+v", key)
	}
	var keys query.Paginated[auth.APIKey]
	if code := p.call(http.MethodGet, "/api/v1/members/api-keys", ownerTok, nil, &keys); code != http.StatusOK ||
		len(keys.Items) != 1 || keys.Items[0].ID != key.ID || keys.Items[0].Hint == "" || strings.Contains(keys.Items[0].Hint, key.Value) {
		t.Fatalf("keys: %d %+v", code, keys.Items)
	}
	if code := p.call(http.MethodGet, "/api/v1/docks", key.Value, nil, nil); code != http.StatusOK {
		t.Fatalf("the key reads docks: %d", code)
	}
	if code := p.call(http.MethodPost, "/api/v1/docks", key.Value, map[string]string{"channel": "api", "name": "x"}, nil); code != http.StatusForbidden {
		t.Fatalf("a Viewer key creates a dock: %d, want 403", code)
	}
	// A key is the organization's credential, not a person: no administration.
	for _, path := range []string{"/api/v1/members/access", "/api/v1/members", "/api/v1/members/api-keys"} {
		if code := p.call(http.MethodGet, path, key.Value, nil, nil); code != http.StatusForbidden {
			t.Errorf("API key on %s: %d, want 403", path, code)
		}
	}
	if code := p.call(http.MethodDelete, "/api/v1/members/api-keys/"+key.ID, ownerTok, nil, nil); code != http.StatusNoContent {
		t.Fatalf("revoke key: %d", code)
	}
	if code := p.call(http.MethodGet, "/api/v1/docks", key.Value, nil, nil); code != http.StatusUnauthorized {
		t.Fatalf("a revoked key reads docks: %d, want 401", code)
	}
	// Only organization keys on Seaport's resource are Seaport's to revoke.
	if code := p.call(http.MethodDelete, "/api/v1/members/api-keys/"+key.ID, ownerTok, nil, nil); code != http.StatusNotFound && code != http.StatusNoContent {
		t.Fatalf("revoke again: %d", code)
	}

	// An invitation that is revoked can't be used.
	var second auth.Invitation
	if code := p.call(http.MethodPost, "/api/v1/members/invitations", ownerTok, map[string]any{"email": "otra@example.com", "role": "Viewer"}, &second); code != http.StatusCreated {
		t.Fatalf("second invite: %d", code)
	}
	if code := p.call(http.MethodDelete, "/api/v1/members/invitations/"+second.ID, ownerTok, nil, nil); code != http.StatusNoContent {
		t.Fatalf("revoke invitation: %d", code)
	}
	if code := p.call(http.MethodGet, "/api/v1/members/invitations", ownerTok, nil, &pending); code != http.StatusOK {
		t.Fatalf("invitations: %d", code)
	}
	for _, i := range pending.Items {
		if i.ID == second.ID && i.Status == "pending" {
			t.Fatalf("revoked invitation still pending: %+v", i)
		}
	}

	// Removal is immediate: their token stops introspecting.
	if code := p.call(http.MethodGet, "/api/v1/docks", fresh, nil, nil); code != http.StatusOK {
		t.Fatalf("before removal: %d", code)
	}
	if code := p.call(http.MethodDelete, "/api/v1/members/"+invitee.ID, ownerTok, nil, nil); code != http.StatusNoContent {
		t.Fatalf("remove: %d", code)
	}
	if code := p.call(http.MethodGet, "/api/v1/docks", fresh, nil, nil); code != http.StatusUnauthorized && code != http.StatusForbidden {
		t.Fatalf("a removed member's token: %d, want 401/403", code)
	}
	if code := p.call(http.MethodGet, "/api/v1/members", ownerTok, nil, &list); code != http.StatusOK {
		t.Fatalf("members: %d", code)
	}
	if listed(list, invitee.ID) && memberNamed(t, list, invitee.ID).Active {
		t.Fatalf("the removed member is still active: %+v", memberNamed(t, list, invitee.ID))
	}
}

// TestIAMMembersCookieSession: the web app's path: the session cookie's own
// access token is what acts, and a write passes the CSRF check.
func TestIAMMembersCookieSession(t *testing.T) {
	p, _ := startIAMPort(t)
	org := newIAMOrg(t)
	grantSeaport(t, org)
	admin := newIAMMemberAdmin(t, org, "Admin", true)
	b := newBrowser(t)
	signIn(t, b, p, admin, "", nil)

	var access auth.Access
	if code := b.api(http.MethodGet, p.srv.URL+"/api/v1/members/access", nil, &access); code != http.StatusOK || !access.Members || !access.KeysWrite {
		t.Fatalf("access by cookie: %d %+v", code, access)
	}
	var inv auth.Invitation
	if code := b.api(http.MethodPost, p.srv.URL+"/api/v1/members/invitations", map[string]any{"email": "web@example.com", "role": "Viewer"}, &inv); code != http.StatusCreated || inv.Link == "" {
		t.Fatalf("invite by cookie: %d %+v", code, inv)
	}
	var list membersList
	if code := b.api(http.MethodGet, p.srv.URL+"/api/v1/members", nil, &list); code != http.StatusOK || !memberNamed(t, list, admin.ID).You {
		t.Fatalf("members by cookie: %d %+v", code, list.Items)
	}
}

// TestIAMMembersNeedTheCompanionRole: a person with Seaport access but not
// the companion role sees nothing to administer (access all false, 403 on
// everything else); a member admin can't hand out more than they hold.
func TestIAMMembersNeedTheCompanionRole(t *testing.T) {
	p, _ := startIAMPort(t)
	org := newIAMOrg(t)
	grantSeaport(t, org)
	viewer := accessToken(t, newIAMMemberAdmin(t, org, "Viewer", false), org)

	var access auth.Access
	if code := p.call(http.MethodGet, "/api/v1/members/access", viewer, nil, &access); code != http.StatusOK || access != (auth.Access{}) {
		t.Fatalf("a Viewer's access: %d %+v, want all false", code, access)
	}
	for _, r := range membersRoutes("u1", "i1", "k1") {
		if r.path == "/api/v1/members/access" {
			continue
		}
		if code := p.call(r.method, r.path, viewer, r.body, nil); code != http.StatusForbidden {
			t.Errorf("Viewer %s %s: %d, want 403", r.method, r.path, code)
		}
	}

	// An Operator who manages members can't give the Admin role or an Admin key.
	op := newIAMMemberAdmin(t, org, "Operator", true)
	opTok := accessToken(t, op, org)
	target := newIAMMemberAdmin(t, org, "Viewer", false)
	if code := p.call(http.MethodPut, "/api/v1/members/"+target.ID, opTok, map[string]any{"role": "Admin"}, nil); code != http.StatusForbidden {
		t.Fatalf("Operator gives Admin: %d, want 403", code)
	}
	if code := p.call(http.MethodPost, "/api/v1/members/invitations", opTok, map[string]any{"email": "x@example.com", "role": "Admin"}, nil); code != http.StatusForbidden {
		t.Fatalf("Operator invites an Admin: %d, want 403", code)
	}
	if code := p.call(http.MethodPost, "/api/v1/members/api-keys", opTok, map[string]any{"name": "k", "role": "Admin"}, nil); code != http.StatusForbidden {
		t.Fatalf("Operator issues an Admin key: %d, want 403", code)
	}
	if code := p.call(http.MethodPut, "/api/v1/members/"+target.ID, opTok, map[string]any{"role": "Operator"}, nil); code != http.StatusOK {
		t.Fatalf("Operator gives Operator: %d", code)
	}
}

// TestIAMMembersStayInTheirOrganization: an administrator of organization B
// can't see or touch organization A's members, invitations or keys.
func TestIAMMembersStayInTheirOrganization(t *testing.T) {
	p, _ := startIAMPort(t)
	orgA, orgB := newIAMOrg(t), newIAMOrg(t)
	grantSeaport(t, orgA)
	grantSeaport(t, orgB)
	adminA := accessToken(t, newIAMMemberAdmin(t, orgA, "Admin", true), orgA)
	adminB := accessToken(t, newIAMMemberAdmin(t, orgB, "Admin", true), orgB)
	memberA := newIAMMemberAdmin(t, orgA, "Viewer", false)

	var inv auth.Invitation
	if code := p.call(http.MethodPost, "/api/v1/members/invitations", adminA, map[string]any{"email": "a@example.com", "role": "Viewer"}, &inv); code != http.StatusCreated {
		t.Fatalf("invite in A: %d", code)
	}
	var key auth.IssuedAPIKey
	if code := p.call(http.MethodPost, "/api/v1/members/api-keys", adminA, map[string]any{"name": "a", "role": "Viewer"}, &key); code != http.StatusCreated {
		t.Fatalf("key in A: %d", code)
	}

	for _, r := range membersRoutes(memberA.ID, inv.ID, key.ID) {
		if r.path == "/api/v1/members/access" || r.method == http.MethodGet || r.path == "/api/v1/members/invitations" && r.method == http.MethodPost ||
			r.path == "/api/v1/members/api-keys" && r.method == http.MethodPost {
			continue
		}
		if code := p.call(r.method, r.path, adminB, r.body, nil); code != http.StatusNotFound && code != http.StatusForbidden {
			t.Errorf("org B %s %s: %d, want 404/403", r.method, r.path, code)
		}
	}
	var list membersList
	if code := p.call(http.MethodGet, "/api/v1/members", adminB, nil, &list); code != http.StatusOK || listed(list, memberA.ID) {
		t.Fatalf("org B lists org A's member: %d %+v", code, list.Items)
	}
	var keys query.Paginated[auth.APIKey]
	if code := p.call(http.MethodGet, "/api/v1/members/api-keys", adminB, nil, &keys); code != http.StatusOK || len(keys.Items) != 0 {
		t.Fatalf("org B lists org A's keys: %d %+v", code, keys.Items)
	}
	var pending query.Paginated[auth.Invitation]
	if code := p.call(http.MethodGet, "/api/v1/members/invitations", adminB, nil, &pending); code != http.StatusOK || len(pending.Items) != 0 {
		t.Fatalf("org B lists org A's invitations: %d %+v", code, pending.Items)
	}
	// Org A's objects are all still there.
	if code := p.call(http.MethodGet, "/api/v1/members", adminA, nil, &list); code != http.StatusOK || !listed(list, memberA.ID) || memberNamed(t, list, memberA.ID).Role != "Viewer" {
		t.Fatalf("org A after org B's attempts: %d %+v", code, list.Items)
	}
	if code := p.call(http.MethodGet, "/api/v1/members/api-keys", adminA, nil, &keys); code != http.StatusOK || len(keys.Items) != 1 {
		t.Fatalf("org A keys after org B's attempts: %d %+v", code, keys.Items)
	}
}

// TestIAMMembersOffWithoutTheServerClient: without IAMKIT_SERVER_CLIENT_ID
// the routes do not exist (404) and the rest of iam mode is unchanged.
func TestIAMMembersOffWithoutTheServerClient(t *testing.T) {
	p, _ := startIAMPortWith(t, false)
	org := newIAMOrg(t)
	tok := accessToken(t, newIAMUser(t, permission.All, org), org)
	for _, path := range []string{"/api/v1/members/access", "/api/v1/members", "/api/v1/members/invitations", "/api/v1/members/api-keys"} {
		if code := p.call(http.MethodGet, path, tok, nil, nil); code != http.StatusNotFound {
			t.Errorf("GET %s: %d, want 404", path, code)
		}
	}
	if code := p.call(http.MethodGet, "/api/v1/docks", tok, nil, nil); code != http.StatusOK {
		t.Fatalf("docks: %d", code)
	}
}

// membersRoute is one members-API request.
type membersRoute struct {
	method, path, pattern string
	body                  any
}

// membersRoutes is every members route, as requests naming the given ids.
// TestIAMOrganizationsAndPermissions checks the list against the code.
func membersRoutes(user, invitation, key string) []membersRoute {
	return []membersRoute{
		{"GET", "/api/v1/members/access", "GET /api/v1/members/access", nil},
		{"GET", "/api/v1/members", "GET /api/v1/members", nil},
		{"PUT", "/api/v1/members/" + user, "PUT /api/v1/members/{user}", map[string]any{"role": "Viewer"}},
		{"DELETE", "/api/v1/members/" + user, "DELETE /api/v1/members/{user}", nil},
		{"GET", "/api/v1/members/invitations", "GET /api/v1/members/invitations", nil},
		{"POST", "/api/v1/members/invitations", "POST /api/v1/members/invitations", map[string]any{"email": "nadie@example.com", "role": "Viewer"}},
		{"POST", "/api/v1/members/invitations/" + invitation + "/resend", "POST /api/v1/members/invitations/{id}/resend", nil},
		{"DELETE", "/api/v1/members/invitations/" + invitation, "DELETE /api/v1/members/invitations/{id}", nil},
		{"GET", "/api/v1/members/api-keys", "GET /api/v1/members/api-keys", nil},
		{"POST", "/api/v1/members/api-keys", "POST /api/v1/members/api-keys", map[string]any{"name": "nadie", "role": "Viewer"}},
		{"DELETE", "/api/v1/members/api-keys/" + key, "DELETE /api/v1/members/api-keys/{id}", nil},
	}
}
