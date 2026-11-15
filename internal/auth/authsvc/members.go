package authsvc

import (
	"context"
	"slices"
	"sort"
	"strings"

	"github.com/Abraxas-365/seaport/internal/auth"
	"github.com/Abraxas-365/seaport/internal/errx"
	"github.com/Abraxas-365/seaport/internal/identity"
	"github.com/Abraxas-365/seaport/internal/permission"
)

// Members is the tenant administration of iam mode (auth.MemberCommands and
// auth.MemberQueries). It only translates between Seaport's vocabulary
// (Admin, Operator, Viewer, "manages members") and IAMKit's roles, and adds
// two guards IAMKit does not give a client: nobody changes their own access
// and nobody hands out more than they hold. Whether the caller may
// administer at all is IAMKit's decision (its iam:org:* permissions).
//
// Only Seaport's roles and the companion role are ever touched; any other
// role a member holds is shown and left alone.
type Members struct {
	dir auth.Directory
	// companion is the IAMKit role id of "Seaport member admins" (the
	// iam:org:* permissions that open the members page).
	companion string
}

func NewMembers(dir auth.Directory, companionRoleID string) *Members {
	return &Members{dir: dir, companion: companionRoleID}
}

var (
	_ auth.MemberCommands = (*Members)(nil)
	_ auth.MemberQueries  = (*Members)(nil)
)

// catalog is the organization's roles seen from Seaport.
type catalog struct {
	// seaport maps a Seaport role name to its IAMKit role.
	seaport   map[string]auth.DirRole
	byID      map[string]auth.DirRole
	companion auth.DirRole
}

func (m *Members) catalog(ctx context.Context, org identity.OrganizationID, actor auth.Actor) (catalog, error) {
	roles, err := m.dir.Roles(ctx, org, actor)
	if err != nil {
		return catalog{}, err
	}
	c := catalog{seaport: map[string]auth.DirRole{}, byID: map[string]auth.DirRole{}}
	for _, r := range roles {
		c.byID[r.ID] = r
		if r.ID == m.companion {
			c.companion = r
		}
		if r.Seaport && isSeaportRole(r.Name) {
			c.seaport[r.Name] = r
		}
	}
	return c, nil
}

func isSeaportRole(name string) bool {
	return slices.ContainsFunc(permission.Roles, func(r permission.Role) bool { return r.Name == name })
}

// holds reports whether the caller may hand out perms: their own Seaport
// permissions must include every one.
func holds(actor auth.Actor, perms []string) bool {
	for _, p := range perms {
		if !permission.Has(actor.Permissions, p) {
			return false
		}
	}
	return true
}

func seaportPermissions(role string) []string {
	for _, r := range permission.Roles {
		if r.Name == role {
			return r.Permissions
		}
	}
	return nil
}

// grant resolves the roles an invitation or access change hands out and
// refuses what the caller does not hold.
func (m *Members) grant(c catalog, actor auth.Actor, role string, manages bool) (seaport string, companion string, err error) {
	r, ok := c.seaport[role]
	if !ok {
		return "", "", errx.Business("the " + role + " role is not set up in IAMKit; run scripts/iamkit-provision.sh")
	}
	if !holds(actor, seaportPermissions(role)) {
		return "", "", errx.Forbidden("you can't give the " + role + " role: it holds permissions you don't have")
	}
	if !manages {
		return r.ID, "", nil
	}
	if m.companion == "" || c.companion.ID == "" {
		return "", "", errx.Business("the members role is not set up in IAMKit; run scripts/iamkit-provision.sh")
	}
	return r.ID, c.companion.ID, nil
}

func (m *Members) need(ctx context.Context, org identity.OrganizationID, actor auth.Actor, allowed func(auth.Access) bool, what string) error {
	perms, err := m.dir.Permissions(ctx, org, actor)
	if err != nil {
		return err
	}
	if !allowed(auth.AccessOf(perms)) {
		return errx.Forbidden("you can't " + what + " in this organization")
	}
	return nil
}

func (m *Members) Access(ctx context.Context, org identity.OrganizationID, actor auth.Actor) (auth.Access, error) {
	perms, err := m.dir.Permissions(ctx, org, actor)
	if err != nil {
		return auth.Access{}, err
	}
	return auth.AccessOf(perms), nil
}

func (m *Members) member(c catalog, actor auth.Actor, d auth.DirMember) auth.Member {
	out := auth.Member{ID: d.ID, Name: d.Name, Email: d.Email, Active: d.Active, You: d.ID == actor.Subject,
		OtherRoles: []string{}}
	var held []string
	for _, r := range d.Roles {
		switch {
		case m.companion != "" && r.ID == m.companion:
			out.ManagesMembers = true
		case c.seaport[r.Name].ID == r.ID && r.ID != "":
			held = append(held, r.Name)
		default:
			out.OtherRoles = append(out.OtherRoles, r.Name)
		}
	}
	// Several Seaport roles: the widest (manifest order) is the one shown.
	for _, r := range permission.Roles {
		if slices.Contains(held, r.Name) {
			out.Role = r.Name
			break
		}
	}
	for _, name := range held {
		if name != out.Role {
			out.OtherRoles = append(out.OtherRoles, name)
		}
	}
	return out
}

func (m *Members) Members(ctx context.Context, org identity.OrganizationID, actor auth.Actor) ([]auth.Member, error) {
	if err := m.need(ctx, org, actor, func(a auth.Access) bool { return a.Members }, "view members"); err != nil {
		return nil, err
	}
	c, err := m.catalog(ctx, org, actor)
	if err != nil {
		return nil, err
	}
	list, err := m.dir.Members(ctx, org, actor)
	if err != nil {
		return nil, err
	}
	out := make([]auth.Member, 0, len(list))
	for _, d := range list {
		out = append(out, m.member(c, actor, d))
	}
	sort.SliceStable(out, func(i, j int) bool {
		return strings.ToLower(out[i].Name+" "+out[i].Email) < strings.ToLower(out[j].Name+" "+out[j].Email)
	})
	return out, nil
}

func (m *Members) SetAccess(ctx context.Context, org identity.OrganizationID, actor auth.Actor, user string, in auth.SetAccess) (auth.Member, error) {
	if err := in.Validate(); err != nil {
		return auth.Member{}, err
	}
	if user == actor.Subject {
		return auth.Member{}, errx.Business("you can't change your own access; ask another administrator")
	}
	if err := m.need(ctx, org, actor, func(a auth.Access) bool { return a.AssignRoles }, "change roles"); err != nil {
		return auth.Member{}, err
	}
	c, err := m.catalog(ctx, org, actor)
	if err != nil {
		return auth.Member{}, err
	}
	role, companion, err := m.grant(c, actor, in.Role, in.ManagesMembers)
	if err != nil {
		return auth.Member{}, err
	}
	cur, err := m.dir.Member(ctx, org, actor, user)
	if err != nil {
		return auth.Member{}, err
	}
	held := map[string]bool{}
	for _, r := range cur.Roles {
		held[r.ID] = true
	}
	// Assign before unassigning, so a failure never leaves the person with
	// less than they had.
	want := []string{role}
	if companion != "" {
		want = append(want, companion)
	}
	for _, id := range want {
		if !held[id] {
			if err := m.dir.AssignRole(ctx, org, actor, user, id); err != nil {
				return auth.Member{}, err
			}
		}
	}
	for _, r := range cur.Roles {
		ours := (c.seaport[r.Name].ID == r.ID && r.ID != "") || (m.companion != "" && r.ID == m.companion)
		if ours && !slices.Contains(want, r.ID) {
			if err := m.dir.UnassignRole(ctx, org, actor, user, r.ID); err != nil {
				return auth.Member{}, err
			}
		}
	}
	after, err := m.dir.Member(ctx, org, actor, user)
	if err != nil {
		return auth.Member{}, err
	}
	return m.member(c, actor, after), nil
}

func (m *Members) Remove(ctx context.Context, org identity.OrganizationID, actor auth.Actor, user string) error {
	if user == actor.Subject {
		return errx.Business("you can't remove yourself from the organization")
	}
	if err := m.need(ctx, org, actor, func(a auth.Access) bool { return a.Remove }, "remove members"); err != nil {
		return err
	}
	return m.dir.RemoveMember(ctx, org, actor, user)
}

func (m *Members) invitation(c catalog, d auth.DirInvitation) auth.Invitation {
	out := auth.Invitation{ID: d.ID, Email: d.Email, Status: d.Status, ExpiresAt: d.ExpiresAt, Link: d.Link}
	for _, id := range d.RoleIDs {
		switch {
		case m.companion != "" && id == m.companion:
			out.ManagesMembers = true
		case c.byID[id].Seaport && isSeaportRole(c.byID[id].Name) && out.Role == "":
			out.Role = c.byID[id].Name
		}
	}
	return out
}

func (m *Members) Invitations(ctx context.Context, org identity.OrganizationID, actor auth.Actor) ([]auth.Invitation, error) {
	if err := m.need(ctx, org, actor, func(a auth.Access) bool { return a.Invite }, "view invitations"); err != nil {
		return nil, err
	}
	c, err := m.catalog(ctx, org, actor)
	if err != nil {
		return nil, err
	}
	list, err := m.dir.Invitations(ctx, org, actor)
	if err != nil {
		return nil, err
	}
	out := make([]auth.Invitation, 0, len(list))
	for _, d := range list {
		out = append(out, m.invitation(c, d))
	}
	return out, nil
}

func (m *Members) Invite(ctx context.Context, org identity.OrganizationID, actor auth.Actor, in auth.Invite) (auth.Invitation, error) {
	if err := in.Validate(); err != nil {
		return auth.Invitation{}, err
	}
	if err := m.need(ctx, org, actor, func(a auth.Access) bool { return a.Invite }, "invite people"); err != nil {
		return auth.Invitation{}, err
	}
	c, err := m.catalog(ctx, org, actor)
	if err != nil {
		return auth.Invitation{}, err
	}
	role, companion, err := m.grant(c, actor, in.Role, in.ManagesMembers)
	if err != nil {
		return auth.Invitation{}, err
	}
	roles := []string{role}
	if companion != "" {
		roles = append(roles, companion)
	}
	d, err := m.dir.Invite(ctx, org, actor, strings.TrimSpace(in.Email), roles)
	if err != nil {
		return auth.Invitation{}, err
	}
	out := m.invitation(c, d)
	if out.Role == "" {
		// IAMKit answered without the roles: they are what we asked for.
		out.Role, out.ManagesMembers = in.Role, in.ManagesMembers
	}
	return out, nil
}

func (m *Members) Resend(ctx context.Context, org identity.OrganizationID, actor auth.Actor, id string) (auth.Invitation, error) {
	if err := m.need(ctx, org, actor, func(a auth.Access) bool { return a.Invite }, "resend invitations"); err != nil {
		return auth.Invitation{}, err
	}
	c, err := m.catalog(ctx, org, actor)
	if err != nil {
		return auth.Invitation{}, err
	}
	d, err := m.dir.Resend(ctx, org, actor, id)
	if err != nil {
		return auth.Invitation{}, err
	}
	return m.invitation(c, d), nil
}

func (m *Members) RevokeInvitation(ctx context.Context, org identity.OrganizationID, actor auth.Actor, id string) error {
	if err := m.need(ctx, org, actor, func(a auth.Access) bool { return a.Invite }, "revoke invitations"); err != nil {
		return err
	}
	return m.dir.RevokeInvitation(ctx, org, actor, id)
}

// apiKey adds the Seaport role whose permissions the key holds.
func apiKey(d auth.DirAPIKey) auth.APIKey {
	out := auth.APIKey{ID: d.ID, Name: d.Name, Permissions: d.Permissions, Hint: d.Hint, State: d.State,
		ExpiresAt: d.ExpiresAt, LastUsedAt: d.LastUsedAt, CreatedAt: d.CreatedAt}
	if out.Permissions == nil {
		out.Permissions = []string{}
	}
	for _, r := range permission.Roles {
		if sameSet(r.Permissions, d.Permissions) {
			out.Role = r.Name
			break
		}
	}
	return out
}

func sameSet(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for _, p := range a {
		if !slices.Contains(b, p) {
			return false
		}
	}
	return true
}

func (m *Members) APIKeys(ctx context.Context, org identity.OrganizationID, actor auth.Actor) ([]auth.APIKey, error) {
	if err := m.need(ctx, org, actor, func(a auth.Access) bool { return a.KeysRead }, "view API keys"); err != nil {
		return nil, err
	}
	list, err := m.dir.APIKeys(ctx, org, actor)
	if err != nil {
		return nil, err
	}
	out := make([]auth.APIKey, 0, len(list))
	for _, d := range list {
		out = append(out, apiKey(d))
	}
	// Newest first.
	sort.SliceStable(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })
	return out, nil
}

func (m *Members) CreateAPIKey(ctx context.Context, org identity.OrganizationID, actor auth.Actor, in auth.NewAPIKey) (auth.IssuedAPIKey, error) {
	if err := in.Validate(); err != nil {
		return auth.IssuedAPIKey{}, err
	}
	if err := m.need(ctx, org, actor, func(a auth.Access) bool { return a.KeysWrite }, "create API keys"); err != nil {
		return auth.IssuedAPIKey{}, err
	}
	perms := seaportPermissions(in.Role)
	if !holds(actor, perms) {
		return auth.IssuedAPIKey{}, errx.Forbidden("you can't create a key with the " + in.Role + " role: it holds permissions you don't have")
	}
	expires := in.ExpiresIn
	if expires == "never" {
		expires = ""
	}
	d, err := m.dir.CreateAPIKey(ctx, org, actor, auth.DirKey{Name: strings.TrimSpace(in.Name), Permissions: slices.Clone(perms), ExpiresIn: expires})
	if err != nil {
		return auth.IssuedAPIKey{}, err
	}
	return auth.IssuedAPIKey{APIKey: apiKey(d.DirAPIKey), Value: d.Value}, nil
}

func (m *Members) RevokeAPIKey(ctx context.Context, org identity.OrganizationID, actor auth.Actor, id string) error {
	if err := m.need(ctx, org, actor, func(a auth.Access) bool { return a.KeysWrite }, "revoke API keys"); err != nil {
		return err
	}
	// Only organization keys on Seaport's resource are ours to revoke.
	if _, err := m.dir.APIKey(ctx, org, actor, id); err != nil {
		return err
	}
	return m.dir.RevokeAPIKey(ctx, org, actor, id)
}
