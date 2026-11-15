package auth

import (
	"context"
	"time"

	"github.com/Abraxas-365/seaport/internal/identity"
)

// Commands are iam mode's sign-in. Sessions are keyed by a random id, not
// by organization: auth is not a tenant domain (TestTenantFirstPorts).
type Commands interface {
	// Begin prepares a sign-in: the IAMKit authorize URL and the pending
	// state the browser keeps until the callback.
	Begin(ctx context.Context, input Begin) (Authorization, error)
	// Complete finishes a sign-in at the callback and opens a session; it
	// returns the session id (cookie value) and the session.
	Complete(ctx context.Context, pending Pending, callback Callback) (string, Session, error)
	// Authenticate resolves a session id, refreshing its access token when
	// needed, and checks it with IAMKit.
	Authenticate(ctx context.Context, sessionID string) (Identity, Session, error)
	// Bearer checks an IAMKit access token or API key (CLI, scripts).
	Bearer(ctx context.Context, token string) (Identity, error)
	// Logout ends the session (IAMKit tokens revoked) and returns where
	// to send the browser to end its IAMKit session too.
	Logout(ctx context.Context, sessionID string) (string, error)
	// Sweep deletes expired sessions.
	Sweep(ctx context.Context) (int, error)
}

// Provider is IAMKit.
type Provider interface {
	// AuthorizeURL is the authorization request (code + PKCE S256).
	AuthorizeURL(state, nonce, challenge, organization string, selectAccount bool) string
	// Exchange trades a code for tokens and verifies the id_token
	// (signature, issuer, audience, nonce).
	Exchange(ctx context.Context, code, verifier, nonce string) (Tokens, error)
	Refresh(ctx context.Context, refresh string) (Tokens, error)
	// Revoke invalidates a token (best effort at sign-out).
	Revoke(ctx context.Context, token string) error
	// VerifyAccess introspects an access token of a user acting in an
	// organization (Seaport's application and resource).
	VerifyAccess(ctx context.Context, token string) (Identity, error)
	// VerifyAPIKey validates an ik_key_ API key of an organization.
	VerifyAPIKey(ctx context.Context, key string) (Identity, error)
	// IsAPIKey: the value looks like an API key.
	IsAPIKey(value string) bool
	// Profile reads the person's name, email and organization name.
	Profile(ctx context.Context, access, organization string) (Profile, error)
	// EndSessionURL is IAMKit's sign-out page for this session.
	EndSessionURL(idToken string) string
}

// SessionRepository stores browser sessions (system-wide: keyed by the
// hash of a random id).
type SessionRepository interface {
	Create(ctx context.Context, session Session) error
	Find(ctx context.Context, idHash string) (Session, error)
	// Update loads the session locked (SELECT … FOR UPDATE), lets fn
	// change it and stores the result, so concurrent requests refresh a
	// session's tokens once (IAMKit rotates refresh tokens). fn returning
	// an error leaves the row as it was.
	Update(ctx context.Context, idHash string, fn func(Session) (Session, error)) (Session, error)
	Delete(ctx context.Context, idHash string) error
	DeleteExpired(ctx context.Context, now time.Time) (int, error)
}

// MemberCommands change who belongs to the caller's organization and how
// they reach Seaport (iam mode, optional: SEAPORT_AUTH=iam with the
// IAMKIT_SERVER_* settings). Everything runs as the Actor, never as a
// Seaport credential: IAMKit enforces its own iam:org:* permissions.
type MemberCommands interface {
	// SetAccess gives a member one Seaport role and, optionally, the
	// companion role that lets them use the members page. Never the
	// caller's own access, never a role the caller doesn't hold.
	SetAccess(ctx context.Context, organization identity.OrganizationID, actor Actor, user string, input SetAccess) (Member, error)
	// Remove ends a person's membership (immediate: their tokens stop
	// introspecting). Never the caller.
	Remove(ctx context.Context, organization identity.OrganizationID, actor Actor, user string) error
	// Invite invites someone with a Seaport role; the result carries the
	// accept link when IAMKit did not email it.
	Invite(ctx context.Context, organization identity.OrganizationID, actor Actor, input Invite) (Invitation, error)
	// Resend issues a new token (and link) for a pending invitation.
	Resend(ctx context.Context, organization identity.OrganizationID, actor Actor, invitation string) (Invitation, error)
	RevokeInvitation(ctx context.Context, organization identity.OrganizationID, actor Actor, invitation string) error
	// CreateAPIKey issues an organization API key for the CLI holding a
	// Seaport role's permissions; the value is returned once.
	CreateAPIKey(ctx context.Context, organization identity.OrganizationID, actor Actor, input NewAPIKey) (IssuedAPIKey, error)
	RevokeAPIKey(ctx context.Context, organization identity.OrganizationID, actor Actor, key string) error
}

// MemberQueries read the caller's organization (same Actor rules).
type MemberQueries interface {
	// Access says what the caller may do on the members page; all false
	// when they can't administer the organization (not an error).
	Access(ctx context.Context, organization identity.OrganizationID, actor Actor) (Access, error)
	Members(ctx context.Context, organization identity.OrganizationID, actor Actor) ([]Member, error)
	Invitations(ctx context.Context, organization identity.OrganizationID, actor Actor) ([]Invitation, error)
	APIKeys(ctx context.Context, organization identity.OrganizationID, actor Actor) ([]APIKey, error)
}

// Directory is IAMKit's organization administration, as the Actor (the
// adapter exchanges the person's token for one on IAMKit's own resource and
// caches it). IAMKit refuses what the person's iam:org:* permissions don't
// allow (a Forbidden error); an Actor who can't administer at all has no
// permissions here.
type Directory interface {
	// Permissions are the iam:org:* permissions the actor holds in the
	// organization; empty when they can't administer it.
	Permissions(ctx context.Context, organization identity.OrganizationID, actor Actor) ([]string, error)
	// Roles lists the roles the actor may assign: IAMKit's own and
	// Seaport's.
	Roles(ctx context.Context, organization identity.OrganizationID, actor Actor) ([]DirRole, error)
	// Members lists the members with the roles each holds.
	Members(ctx context.Context, organization identity.OrganizationID, actor Actor) ([]DirMember, error)
	// Member reads one (NotFound when not a member of the organization).
	Member(ctx context.Context, organization identity.OrganizationID, actor Actor, user string) (DirMember, error)
	AssignRole(ctx context.Context, organization identity.OrganizationID, actor Actor, user, role string) error
	UnassignRole(ctx context.Context, organization identity.OrganizationID, actor Actor, user, role string) error
	RemoveMember(ctx context.Context, organization identity.OrganizationID, actor Actor, user string) error
	Invitations(ctx context.Context, organization identity.OrganizationID, actor Actor) ([]DirInvitation, error)
	Invite(ctx context.Context, organization identity.OrganizationID, actor Actor, email string, roles []string) (DirInvitation, error)
	Resend(ctx context.Context, organization identity.OrganizationID, actor Actor, invitation string) (DirInvitation, error)
	RevokeInvitation(ctx context.Context, organization identity.OrganizationID, actor Actor, invitation string) error
	// APIKeys lists the organization's keys on Seaport's resource.
	APIKeys(ctx context.Context, organization identity.OrganizationID, actor Actor) ([]DirAPIKey, error)
	// APIKey reads one (NotFound unless it is an organization key on
	// Seaport's resource).
	APIKey(ctx context.Context, organization identity.OrganizationID, actor Actor, key string) (DirAPIKey, error)
	CreateAPIKey(ctx context.Context, organization identity.OrganizationID, actor Actor, input DirKey) (DirIssuedKey, error)
	RevokeAPIKey(ctx context.Context, organization identity.OrganizationID, actor Actor, key string) error
}
