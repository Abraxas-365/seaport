package authsvc

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Abraxas-365/seaport/internal/auth"
	"github.com/Abraxas-365/seaport/internal/errx"
	"github.com/Abraxas-365/seaport/internal/identity"
	"github.com/Abraxas-365/seaport/internal/permission"
)

const (
	roleAdmin     = "role-admin"
	roleOperator  = "role-operator"
	roleViewer    = "role-viewer"
	roleCompanion = "role-companion"
	roleOwner     = "role-owner"  // IAMKit's own role, never touched
	roleCustom    = "role-custom" // a role of Seaport's resource Seaport doesn't know
)

// fakeDirectory is an in-memory IAMKit organization: it records what the
// service asked it to change.
type fakeDirectory struct {
	perms    []string
	roles    []auth.DirRole
	members  map[string]*auth.DirMember
	invites  []auth.DirInvitation
	keys     []auth.DirAPIKey
	calls    []string
	invited  []string // role ids of the last invitation
	keyAsked auth.DirKey
}

func newDirectory() *fakeDirectory {
	d := &fakeDirectory{
		perms: []string{auth.OrgMembersRead, auth.OrgMembersWrite, auth.OrgRolesRead, auth.OrgRolesAssign,
			auth.OrgInvitationsWrite, auth.OrgAPIKeysRead, auth.OrgAPIKeysWrite},
		roles: []auth.DirRole{
			{ID: roleAdmin, Name: "Admin", Seaport: true},
			{ID: roleOperator, Name: "Operator", Seaport: true},
			{ID: roleViewer, Name: "Viewer", Seaport: true},
			{ID: roleCustom, Name: "Auditor", Seaport: true},
			{ID: roleCompanion, Name: "Seaport member admins"},
			{ID: roleOwner, Name: "Organization owner"},
			// A Viewer-named role of another resource must never count as Seaport's.
			{ID: "role-impostor", Name: "Viewer"},
		},
		members: map[string]*auth.DirMember{
			"me":  {ID: "me", Name: "Ada Lopez", Email: "ada@example.com", Active: true, Roles: []auth.DirRoleRef{{ID: roleAdmin, Name: "Admin"}, {ID: roleCompanion, Name: "Seaport member admins"}}},
			"ana": {ID: "ana", Name: "Ana Torres", Email: "ana@example.com", Active: true, Roles: []auth.DirRoleRef{{ID: roleViewer, Name: "Viewer"}, {ID: roleOwner, Name: "Organization owner"}}},
		},
	}
	return d
}

func (d *fakeDirectory) log(s string) { d.calls = append(d.calls, s) }

func (d *fakeDirectory) Permissions(context.Context, identity.OrganizationID, auth.Actor) ([]string, error) {
	return d.perms, nil
}
func (d *fakeDirectory) Roles(context.Context, identity.OrganizationID, auth.Actor) ([]auth.DirRole, error) {
	return d.roles, nil
}
func (d *fakeDirectory) Members(context.Context, identity.OrganizationID, auth.Actor) ([]auth.DirMember, error) {
	var out []auth.DirMember
	for _, m := range d.members {
		out = append(out, *m)
	}
	return out, nil
}
func (d *fakeDirectory) Member(_ context.Context, _ identity.OrganizationID, _ auth.Actor, user string) (auth.DirMember, error) {
	m, ok := d.members[user]
	if !ok {
		return auth.DirMember{}, errx.NotFound("member not found")
	}
	return *m, nil
}
func (d *fakeDirectory) AssignRole(_ context.Context, _ identity.OrganizationID, _ auth.Actor, user, role string) error {
	d.log("assign " + user + " " + role)
	m := d.members[user]
	m.Roles = append(m.Roles, auth.DirRoleRef{ID: role, Name: d.name(role)})
	return nil
}
func (d *fakeDirectory) UnassignRole(_ context.Context, _ identity.OrganizationID, _ auth.Actor, user, role string) error {
	d.log("unassign " + user + " " + role)
	m := d.members[user]
	m.Roles = slices.DeleteFunc(m.Roles, func(r auth.DirRoleRef) bool { return r.ID == role })
	return nil
}
func (d *fakeDirectory) name(id string) string {
	for _, r := range d.roles {
		if r.ID == id {
			return r.Name
		}
	}
	return id
}
func (d *fakeDirectory) RemoveMember(_ context.Context, _ identity.OrganizationID, _ auth.Actor, user string) error {
	d.log("remove " + user)
	return nil
}
func (d *fakeDirectory) Invitations(context.Context, identity.OrganizationID, auth.Actor) ([]auth.DirInvitation, error) {
	return d.invites, nil
}
func (d *fakeDirectory) Invite(_ context.Context, _ identity.OrganizationID, _ auth.Actor, email string, roles []string) (auth.DirInvitation, error) {
	d.log("invite " + email)
	d.invited = roles
	return auth.DirInvitation{ID: "inv-1", Email: email, Status: "pending", RoleIDs: roles, Link: "https://iam.example.com/accept?t=1"}, nil
}
func (d *fakeDirectory) Resend(_ context.Context, _ identity.OrganizationID, _ auth.Actor, id string) (auth.DirInvitation, error) {
	d.log("resend " + id)
	return auth.DirInvitation{ID: id, Email: "ana@example.com", Status: "pending", RoleIDs: []string{roleViewer}, Link: "https://iam.example.com/accept?t=2"}, nil
}
func (d *fakeDirectory) RevokeInvitation(_ context.Context, _ identity.OrganizationID, _ auth.Actor, id string) error {
	d.log("revoke-invitation " + id)
	return nil
}
func (d *fakeDirectory) APIKeys(context.Context, identity.OrganizationID, auth.Actor) ([]auth.DirAPIKey, error) {
	return d.keys, nil
}
func (d *fakeDirectory) APIKey(_ context.Context, _ identity.OrganizationID, _ auth.Actor, id string) (auth.DirAPIKey, error) {
	for _, k := range d.keys {
		if k.ID == id {
			return k, nil
		}
	}
	return auth.DirAPIKey{}, errx.NotFound("API key not found")
}
func (d *fakeDirectory) CreateAPIKey(_ context.Context, _ identity.OrganizationID, _ auth.Actor, in auth.DirKey) (auth.DirIssuedKey, error) {
	d.log("create-key " + in.Name)
	d.keyAsked = in
	return auth.DirIssuedKey{DirAPIKey: auth.DirAPIKey{ID: "key-1", Name: in.Name, Permissions: in.Permissions, Hint: "…wxyz", State: "active"}, Value: "ik_key_example"}, nil
}
func (d *fakeDirectory) RevokeAPIKey(_ context.Context, _ identity.OrganizationID, _ auth.Actor, id string) error {
	d.log("revoke-key " + id)
	return nil
}

func adminActor() auth.Actor {
	return auth.Actor{Token: "tok", Subject: "me", Permissions: permission.All}
}

func operatorActor() auth.Actor {
	var perms []string
	for _, r := range permission.Roles {
		if r.Name == "Operator" {
			perms = r.Permissions
		}
	}
	return auth.Actor{Token: "tok", Subject: "me", Permissions: perms}
}

func newMembers(d *fakeDirectory) *Members { return NewMembers(d, roleCompanion) }

// wantStatus: err is an errx error that reaches the client with this status.
func wantStatus(t *testing.T, err error, status int) {
	t.Helper()
	var typed *errx.Error
	if !errx.As(err, &typed) || typed.HTTPStatus != status {
		t.Fatalf("error = %v, want HTTP %d", err, status)
	}
}

func TestMembersShowSeaportRolesAndLeaveTheRestAlone(t *testing.T) {
	d := newDirectory()
	got, err := newMembers(d).Members(context.Background(), orgA, adminActor())
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].Name != "Ada Lopez" || got[1].Name != "Ana Torres" {
		t.Fatalf("members = %+v", got)
	}
	ada, ana := got[0], got[1]
	if !ada.You || ada.Role != "Admin" || !ada.ManagesMembers || len(ada.OtherRoles) != 0 {
		t.Errorf("ada = %+v", ada)
	}
	if ana.You || ana.Role != "Viewer" || ana.ManagesMembers || !slices.Equal(ana.OtherRoles, []string{"Organization owner"}) {
		t.Errorf("ana = %+v", ana)
	}
}

func TestMembersNeedPermission(t *testing.T) {
	d := newDirectory()
	d.perms = nil
	m := newMembers(d)
	ctx := context.Background()
	if _, err := m.Members(ctx, orgA, adminActor()); err == nil {
		t.Error("listed members without permission")
	}
	if a, err := m.Access(ctx, orgA, adminActor()); err != nil || a != (auth.Access{}) {
		t.Errorf("access = %+v, %v (want all false, no error)", a, err)
	}
	wantStatus(t, m.Remove(ctx, orgA, adminActor(), "ana"), 403)
	_, err := m.Invite(ctx, orgA, adminActor(), auth.Invite{Email: "bo@example.com", Role: "Viewer"})
	wantStatus(t, err, 403)
	_, err = m.CreateAPIKey(ctx, orgA, adminActor(), auth.NewAPIKey{Name: "ci", Role: "Viewer"})
	wantStatus(t, err, 403)
	if len(d.calls) != 0 {
		t.Fatalf("IAMKit was changed: %v", d.calls)
	}
}

func TestSetAccessSwapsOnlyOurRoles(t *testing.T) {
	d := newDirectory()
	got, err := newMembers(d).SetAccess(context.Background(), orgA, adminActor(), "ana", auth.SetAccess{Role: "Operator", ManagesMembers: true})
	if err != nil {
		t.Fatal(err)
	}
	// Assigned before unassigned; the owner role stays.
	want := []string{"assign ana " + roleOperator, "assign ana " + roleCompanion, "unassign ana " + roleViewer}
	if !slices.Equal(d.calls, want) {
		t.Fatalf("calls = %v, want %v", d.calls, want)
	}
	if got.Role != "Operator" || !got.ManagesMembers || !slices.Equal(got.OtherRoles, []string{"Organization owner"}) {
		t.Fatalf("member = %+v", got)
	}
}

func TestSetAccessDropsTheCompanionWhenSwitchedOff(t *testing.T) {
	d := newDirectory()
	d.members["ana"].Roles = append(d.members["ana"].Roles, auth.DirRoleRef{ID: roleCompanion, Name: "Seaport member admins"})
	got, err := newMembers(d).SetAccess(context.Background(), orgA, adminActor(), "ana", auth.SetAccess{Role: "Viewer"})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(d.calls, []string{"unassign ana " + roleCompanion}) || got.ManagesMembers {
		t.Fatalf("calls = %v, member = %+v", d.calls, got)
	}
}

func TestSetAccessRefusesSelfLockout(t *testing.T) {
	d := newDirectory()
	m := newMembers(d)
	_, err := m.SetAccess(context.Background(), orgA, adminActor(), "me", auth.SetAccess{Role: "Viewer"})
	wantStatus(t, err, 422)
	wantStatus(t, m.Remove(context.Background(), orgA, adminActor(), "me"), 422)
	if len(d.calls) != 0 {
		t.Fatalf("IAMKit was changed: %v", d.calls)
	}
}

func TestRoleEscalationIsRefused(t *testing.T) {
	d := newDirectory()
	m := newMembers(d)
	ctx := context.Background()
	op := operatorActor() // holds no seaport:docks:write, crew-tokens:write, …: not Admin's permissions

	_, err := m.SetAccess(ctx, orgA, op, "ana", auth.SetAccess{Role: "Admin"})
	wantStatus(t, err, 403)
	_, err = m.Invite(ctx, orgA, op, auth.Invite{Email: "bo@example.com", Role: "Admin"})
	wantStatus(t, err, 403)
	_, err = m.CreateAPIKey(ctx, orgA, op, auth.NewAPIKey{Name: "ci", Role: "Admin"})
	wantStatus(t, err, 403)
	if len(d.calls) != 0 {
		t.Fatalf("IAMKit was changed: %v", d.calls)
	}

	// What the operator does hold, it may hand out.
	if _, err := m.Invite(ctx, orgA, op, auth.Invite{Email: "bo@example.com", Role: "Viewer"}); err != nil {
		t.Fatalf("operator invites a viewer: %v", err)
	}
	if _, err := m.CreateAPIKey(ctx, orgA, op, auth.NewAPIKey{Name: "ci", Role: "Operator"}); err != nil {
		t.Fatalf("operator key with its own role: %v", err)
	}
}

func TestInviteCarriesTheRoleAndShowsTheLink(t *testing.T) {
	d := newDirectory()
	got, err := newMembers(d).Invite(context.Background(), orgA, adminActor(), auth.Invite{Email: " bo@example.com ", Role: "Operator", ManagesMembers: true})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(d.invited, []string{roleOperator, roleCompanion}) {
		t.Fatalf("roles asked = %v", d.invited)
	}
	if got.Email != "bo@example.com" || got.Role != "Operator" || !got.ManagesMembers || !strings.HasPrefix(got.Link, "https://") {
		t.Fatalf("invitation = %+v", got)
	}
}

func TestInviteNeedsTheRolesSetUp(t *testing.T) {
	d := newDirectory()
	// Without the companion role in IAMKit "can manage members" is refused, not silently dropped.
	m := NewMembers(d, "")
	_, err := m.Invite(context.Background(), orgA, adminActor(), auth.Invite{Email: "bo@example.com", Role: "Viewer", ManagesMembers: true})
	wantStatus(t, err, 422)
	if _, err := m.Invite(context.Background(), orgA, adminActor(), auth.Invite{Email: "bo@example.com", Role: "Viewer"}); err != nil {
		t.Fatalf("plain invite: %v", err)
	}
	// A Seaport role missing from IAMKit is a setup problem, not a 500.
	d.roles = d.roles[:0]
	_, err = newMembers(d).Invite(context.Background(), orgA, adminActor(), auth.Invite{Email: "bo@example.com", Role: "Viewer"})
	wantStatus(t, err, 422)
}

func TestInvitationsNameTheirRoles(t *testing.T) {
	d := newDirectory()
	exp := time.Now().Add(time.Hour)
	d.invites = []auth.DirInvitation{{ID: "i1", Email: "bo@example.com", Status: "pending", RoleIDs: []string{roleOwner, roleOperator, roleCompanion}, ExpiresAt: &exp}}
	got, err := newMembers(d).Invitations(context.Background(), orgA, adminActor())
	if err != nil || len(got) != 1 {
		t.Fatalf("invitations = %+v, %v", got, err)
	}
	if got[0].Role != "Operator" || !got[0].ManagesMembers {
		t.Fatalf("invitation = %+v", got[0])
	}
}

func TestCreateAPIKeyHoldsTheRolesPermissions(t *testing.T) {
	d := newDirectory()
	got, err := newMembers(d).CreateAPIKey(context.Background(), orgA, adminActor(), auth.NewAPIKey{Name: " ci ", Role: "Viewer", ExpiresIn: "never"})
	if err != nil {
		t.Fatal(err)
	}
	var viewer []string
	for _, r := range permission.Roles {
		if r.Name == "Viewer" {
			viewer = r.Permissions
		}
	}
	if d.keyAsked.Name != "ci" || d.keyAsked.ExpiresIn != "" || !slices.Equal(d.keyAsked.Permissions, viewer) {
		t.Fatalf("asked = %+v", d.keyAsked)
	}
	if got.Value != "ik_key_example" || got.Role != "Viewer" {
		t.Fatalf("key = %+v", got)
	}
}

func TestAPIKeysShowTheirRoleNewestFirst(t *testing.T) {
	d := newDirectory()
	var admin []string
	for _, r := range permission.Roles {
		if r.Name == "Admin" {
			admin = r.Permissions
		}
	}
	now := time.Now()
	d.keys = []auth.DirAPIKey{
		{ID: "old", Name: "old", Permissions: admin, CreatedAt: now.Add(-time.Hour)},
		{ID: "new", Name: "new", Permissions: []string{permission.DocksRead}, CreatedAt: now},
	}
	got, err := newMembers(d).APIKeys(context.Background(), orgA, adminActor())
	if err != nil || len(got) != 2 {
		t.Fatalf("keys = %+v, %v", got, err)
	}
	if got[0].ID != "new" || got[0].Role != "" || got[1].Role != "Admin" {
		t.Fatalf("keys = %+v", got)
	}
}

func TestRevokeAPIKeyOnlyOursAndInvitationsNeedAccess(t *testing.T) {
	d := newDirectory()
	d.keys = []auth.DirAPIKey{{ID: "k1"}}
	m := newMembers(d)
	ctx := context.Background()
	wantStatus(t, m.RevokeAPIKey(ctx, orgA, adminActor(), "other-resource-key"), 404)
	if err := m.RevokeAPIKey(ctx, orgA, adminActor(), "k1"); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(d.calls, []string{"revoke-key k1"}) {
		t.Fatalf("calls = %v", d.calls)
	}
	d.perms = []string{auth.OrgAPIKeysRead}
	wantStatus(t, m.RevokeAPIKey(ctx, orgA, adminActor(), "k1"), 403)
	wantStatus(t, m.RevokeInvitation(ctx, orgA, adminActor(), "i1"), 403)
	_, err := m.Resend(ctx, orgA, adminActor(), "i1")
	wantStatus(t, err, 403)
}

func TestMembersValidateInputsFirst(t *testing.T) {
	d := newDirectory()
	m := newMembers(d)
	ctx := context.Background()
	_, err := m.Invite(ctx, orgA, adminActor(), auth.Invite{Email: "nope", Role: "Viewer"})
	wantStatus(t, err, 400)
	_, err = m.SetAccess(ctx, orgA, adminActor(), "ana", auth.SetAccess{Role: "Owner"})
	wantStatus(t, err, 400)
	_, err = m.CreateAPIKey(ctx, orgA, adminActor(), auth.NewAPIKey{Name: "ci", Role: "Viewer", ExpiresIn: "5m"})
	wantStatus(t, err, 400)
}
