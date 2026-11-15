package auth

import (
	"net/mail"
	"strings"
	"time"

	"github.com/Abraxas-365/seaport/internal/errx"
	"github.com/Abraxas-365/seaport/internal/permission"
	"github.com/Abraxas-365/seaport/internal/text"
)

// Tenant administration from Seaport (iam mode, optional): people who hold
// IAMKit's organization-administration permissions (iam:org:*, not part of
// Seaport's catalog) manage their own organization's members, invitations
// and CLI API keys. Seaport never holds an administrator credential: every
// call acts with the signed-in person's own token, exchanged for IAMKit's
// IAM resource, so IAMKit is the one that enforces who may do what.

const (
	// APIKeySubjectPrefix starts the Subject of an Identity that is an API
	// key, not a person.
	APIKeySubjectPrefix = "api-key:"

	// MaxKeyName bounds an API key's name.
	MaxKeyName = 80
	// MaxEmail bounds an invited email (RFC 5321's path limit).
	MaxEmail = 254
	// MinKeyLife and MaxKeyLife bound a key's lifetime (IAMKit's 1h–8760h).
	MinKeyLife = time.Hour
	MaxKeyLife = 8760 * time.Hour
)

// OrgPermission names IAMKit's organization-administration permissions,
// which Access reads from the exchanged token.
const (
	OrgMembersRead      = "iam:org:members:read"
	OrgMembersWrite     = "iam:org:members:write"
	OrgRolesRead        = "iam:org:roles:read"
	OrgRolesAssign      = "iam:org:roles:assign"
	OrgInvitationsWrite = "iam:org:invitations:write"
	OrgAPIKeysRead      = "iam:org:api-keys:read"
	OrgAPIKeysWrite     = "iam:org:api-keys:write"
)

// Actor is the person on whose behalf an administration call runs: their
// own Seaport access token (what the exchange starts from), who they are
// and what they may do in Seaport (to refuse handing out more than that).
type Actor struct {
	// Token is the person's IAMKit access token for Seaport's resource.
	Token string
	// Subject is the IAMKit user id.
	Subject string
	// Permissions are the caller's Seaport permissions.
	Permissions []string
}

// Access is what the signed-in person may do in the members page, read
// from the iam:org:* permissions of the exchanged token. All false means
// "can't administer" (no page), never an error.
type Access struct {
	// Members: list members and their roles.
	Members bool `json:"members"`
	// Invite: invite people and manage pending invitations.
	Invite bool `json:"invite"`
	// AssignRoles: change a member's role.
	AssignRoles bool `json:"assign_roles"`
	// Remove: remove members.
	Remove bool `json:"remove"`
	// KeysRead / KeysWrite: list / create and revoke organization API keys.
	KeysRead  bool `json:"keys_read"`
	KeysWrite bool `json:"keys_write"`
}

// AccessOf reads Access from iam:org:* permissions.
func AccessOf(perms []string) Access {
	has := func(p string) bool { return permission.Has(perms, p) }
	return Access{
		Members:     has(OrgMembersRead) && has(OrgRolesRead),
		Invite:      has(OrgInvitationsWrite) && has(OrgRolesAssign),
		AssignRoles: has(OrgRolesAssign),
		Remove:      has(OrgMembersWrite),
		KeysRead:    has(OrgAPIKeysRead),
		KeysWrite:   has(OrgAPIKeysWrite),
	}
}

// Member is a person of the organization with their access to Seaport.
type Member struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Email  string `json:"email"`
	Active bool   `json:"active"`
	// You marks the signed-in person (who can't change their own access).
	You bool `json:"you"`
	// Role is the Seaport role they hold (Admin, Operator, Viewer), or ""
	// when none.
	Role string `json:"role"`
	// ManagesMembers: they hold the companion role that lets them use this
	// page.
	ManagesMembers bool `json:"manages_members"`
	// OtherRoles are the IAMKit roles Seaport shows but never changes
	// (organization owner, custom roles, further Seaport roles).
	OtherRoles []string `json:"other_roles"`
}

// Invitation is a pending invitation to join the organization.
type Invitation struct {
	ID             string     `json:"id"`
	Email          string     `json:"email"`
	Status         string     `json:"status"`
	Role           string     `json:"role"`
	ManagesMembers bool       `json:"manages_members"`
	ExpiresAt      *time.Time `json:"expires_at"`
	// Link is the invitation's accept page; IAMKit answers it only when it
	// created or resent the invitation without emailing it, so the person
	// who invites can hand it over.
	Link string `json:"link,omitempty"`
}

// APIKey is an organization API key for the CLI (its value is never kept).
type APIKey struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	// Role is the Seaport role whose permissions the key holds, or "" when
	// they match none.
	Role        string     `json:"role"`
	Permissions []string   `json:"permissions"`
	Hint        string     `json:"hint"`
	State       string     `json:"state"`
	ExpiresAt   *time.Time `json:"expires_at"`
	LastUsedAt  *time.Time `json:"last_used_at"`
	CreatedAt   time.Time  `json:"created_at"`
}

// IssuedAPIKey is a new key with its value, shown once.
type IssuedAPIKey struct {
	APIKey
	Value string `json:"value"`
}

// Invite asks IAMKit to invite someone with a Seaport role.
type Invite struct {
	Email          string `json:"email"`
	Role           string `json:"role"`
	ManagesMembers bool   `json:"manages_members"`
}

func (i Invite) Validate() error {
	if err := text.Required("email", i.Email, MaxEmail); err != nil {
		return err
	}
	addr, err := mail.ParseAddress(strings.TrimSpace(i.Email))
	if err != nil || addr.Name != "" || !strings.Contains(addr.Address, "@") {
		return errx.InvalidField("email", "email must be a valid address")
	}
	return ValidRole(i.Role)
}

// SetAccess changes a member's Seaport role and whether they manage members.
type SetAccess struct {
	Role           string `json:"role"`
	ManagesMembers bool   `json:"manages_members"`
}

func (s SetAccess) Validate() error { return ValidRole(s.Role) }

// NewAPIKey asks for an organization API key holding a Seaport role's
// permissions.
type NewAPIKey struct {
	Name string `json:"name"`
	Role string `json:"role"`
	// ExpiresIn is a Go duration between 1h and 8760h, or "" / "never".
	ExpiresIn string `json:"expires_in"`
}

func (k NewAPIKey) Validate() error {
	if err := text.Required("name", k.Name, MaxKeyName); err != nil {
		return err
	}
	if err := ValidRole(k.Role); err != nil {
		return err
	}
	if k.ExpiresIn == "" || k.ExpiresIn == "never" {
		return nil
	}
	d, err := time.ParseDuration(k.ExpiresIn)
	if err != nil || d < MinKeyLife || d > MaxKeyLife {
		return errx.InvalidField("expires_in", "expires_in must be a duration between 1h and 8760h, or never")
	}
	return nil
}

// ValidRole: role names one of the manifest's Seaport roles.
func ValidRole(role string) error {
	for _, r := range permission.Roles {
		if r.Name == role {
			return nil
		}
	}
	return errx.InvalidField("role", "role must be one of: "+strings.Join(roleNames(), ", "))
}

func roleNames() []string {
	out := make([]string, 0, len(permission.Roles))
	for _, r := range permission.Roles {
		out = append(out, r.Name)
	}
	return out
}

// DirRoleRef is a role a member holds.
type DirRoleRef struct{ ID, Name string }

// DirMember is a member as IAMKit lists it, with the roles they hold
// (IAMKit's and Seaport's). A removed member stays listed, inactive.
type DirMember struct {
	ID, Name, Email string
	Active          bool
	Roles           []DirRoleRef
}

// DirRole is a role an organization administrator may assign.
type DirRole struct {
	ID, Name    string
	Permissions []string
	// Seaport: the role belongs to Seaport's resource (not IAMKit's own).
	Seaport bool
}

// DirInvitation is an invitation as IAMKit answers it.
type DirInvitation struct {
	ID, Email, Status string
	RoleIDs           []string
	ExpiresAt         *time.Time
	Link              string
}

// DirKey asks IAMKit for an organization key on Seaport's resource.
type DirKey struct {
	Name        string
	Permissions []string
	// ExpiresIn: a Go duration (1h–8760h) or "" for no expiry.
	ExpiresIn string
}

// DirAPIKey is an organization key as IAMKit lists it.
type DirAPIKey struct {
	ID, Name    string
	Permissions []string
	Hint, State string
	ExpiresAt   *time.Time
	LastUsedAt  *time.Time
	CreatedAt   time.Time
}

// DirIssuedKey is a new key with its value.
type DirIssuedKey struct {
	DirAPIKey
	Value string
}
