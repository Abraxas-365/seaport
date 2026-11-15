package authiam

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"sync"
	"time"

	"github.com/Abraxas-365/iamkit/sdk/apiclient"
	"github.com/Abraxas-365/iamkit/sdk/apierror"
	"github.com/Abraxas-365/iamkit/sdk/authclient"
	"github.com/golang-jwt/jwt/v5"

	"github.com/Abraxas-365/seaport/internal/auth"
	"github.com/Abraxas-365/seaport/internal/errx"
	"github.com/Abraxas-365/seaport/internal/identity"
)

// DirectoryConfig is what the Directory needs: where IAMKit is, which
// resource is Seaport's, and the confidential OAuth client Seaport's server
// exchanges tokens with (scripts/iamkit-provision.sh writes
// IAMKIT_SERVER_CLIENT_ID/_SECRET).
type DirectoryConfig struct {
	// URL is how this process reaches IAMKit; Environment its environment;
	// Resource Seaport's own resource (the roles and keys Seaport manages).
	URL, Environment, Resource string
	// ServerClientID and ServerClientSecret are the token-exchange client.
	ServerClientID, ServerClientSecret string
	// HTTP is the client for IAMKit calls (nil: 10 s timeout).
	HTTP *http.Client
	// Now is the clock (nil: time.Now); tests move it.
	Now func() time.Time
}

const (
	// exchangeMargin: an exchanged token this close to its expiry is
	// exchanged again.
	exchangeMargin = 30 * time.Second
	// cantAdminFor is how long "this person can't administer" is
	// remembered (IAMKit answers invalid_target): the page asks on every
	// load, and a Viewer must not cost an exchange each time.
	cantAdminFor = 60 * time.Second
	// maxCached bounds the exchange cache.
	maxCached = 4096
	// roleFanOut bounds the concurrent member-role lookups of a listing.
	roleFanOut = 8
	// keyPage is the page size when listing keys.
	keyPage = 100
	// defaultExchangeLife is assumed when IAMKit omits expires_in.
	defaultExchangeLife = 5 * time.Minute
)

// Directory is IAMKit's organization administration (auth.Directory): every
// call runs as the signed-in person, with their own access token exchanged
// for one on IAMKit's IAM resource (RFC 8693, a confidential client), so
// IAMKit enforces what their iam:org:* permissions allow. Seaport holds no
// administrator credential.
type Directory struct {
	cfg   DirectoryConfig
	oauth *authclient.OAuthClient
	now   func() time.Time

	mu       sync.Mutex
	cache    map[string]exchanged
	inflight map[string]*exchange
}

// exchanged is a cached exchange: an empty token means "can't administer".
type exchanged struct {
	token   string
	perms   []string
	expires time.Time
}

// exchange is an exchange in progress that concurrent callers wait for.
type exchange struct {
	done chan struct{}
	res  exchanged
	err  error
}

func NewDirectory(cfg DirectoryConfig) *Directory {
	if cfg.HTTP == nil {
		cfg.HTTP = &http.Client{Timeout: 10 * time.Second}
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	return &Directory{
		cfg:      cfg,
		oauth:    authclient.NewOAuth(cfg.URL, cfg.ServerClientID, cfg.ServerClientSecret, authclient.WithOAuthHTTPClient(cfg.HTTP)),
		now:      cfg.Now,
		cache:    map[string]exchanged{},
		inflight: map[string]*exchange{},
	}
}

var _ auth.Directory = (*Directory)(nil)

// cacheKey is the hash of the person's token: the cache never holds it.
func cacheKey(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// session returns the person's exchanged token (cached until 30 s before it
// expires; "can't administer" for 60 s). Concurrent callers share one
// exchange.
func (d *Directory) session(ctx context.Context, actor auth.Actor, fresh bool) (exchanged, error) {
	if actor.Token == "" {
		return exchanged{}, errx.Forbidden("sign in as a person to manage the organization")
	}
	key := cacheKey(actor.Token)
	d.mu.Lock()
	if fresh {
		delete(d.cache, key)
	}
	if e, ok := d.cache[key]; ok && d.now().Before(e.expires) {
		d.mu.Unlock()
		return e, nil
	}
	if call, ok := d.inflight[key]; ok {
		d.mu.Unlock()
		select {
		case <-call.done:
			return call.res, call.err
		case <-ctx.Done():
			return exchanged{}, ctx.Err()
		}
	}
	call := &exchange{done: make(chan struct{})}
	d.inflight[key] = call
	d.mu.Unlock()

	// The exchange outlives the first caller's request (others wait on it).
	xctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 15*time.Second)
	defer cancel()
	call.res, call.err = d.exchange(xctx, actor.Token)

	d.mu.Lock()
	delete(d.inflight, key)
	if call.err == nil {
		d.store(key, call.res)
	}
	d.mu.Unlock()
	close(call.done)
	return call.res, call.err
}

// store keeps e (mu held), dropping what expired first and everything when
// the cache is still too big.
func (d *Directory) store(key string, e exchanged) {
	if len(d.cache) >= maxCached {
		now := d.now()
		for k, v := range d.cache {
			if !now.Before(v.expires) {
				delete(d.cache, k)
			}
		}
		if len(d.cache) >= maxCached {
			clear(d.cache)
		}
	}
	d.cache[key] = e
}

func (d *Directory) exchange(ctx context.Context, subject string) (exchanged, error) {
	now := d.now()
	got, err := d.oauth.ExchangeToken(ctx, subject, "urn:iamkit:environment:"+d.cfg.Environment)
	if err != nil {
		var oauth *authclient.OAuthError
		if errors.As(err, &oauth) {
			switch oauth.Code {
			case "invalid_target":
				// IAMKit: this person holds no organization-administration
				// permission here.
				return exchanged{expires: now.Add(cantAdminFor)}, nil
			case "invalid_grant", "invalid_token":
				return exchanged{}, errx.Unauthorized("sign in again")
			}
		}
		return exchanged{}, errx.Wrap(err, "IAMKit token exchange", errx.TypeExternal)
	}
	life := time.Duration(got.ExpiresIn) * time.Second
	if life <= 0 {
		life = defaultExchangeLife
	}
	perms, exp := claimsOf(got.AccessToken)
	if !exp.IsZero() && exp.Sub(now) < life {
		life = exp.Sub(now)
	}
	return exchanged{token: got.AccessToken, perms: perms, expires: now.Add(life - exchangeMargin)}, nil
}

// claimsOf reads the permissions and expiry of the exchanged token. It is
// not verified: it came straight from IAMKit's token endpoint and only
// decides what the page shows; IAMKit enforces every call itself.
func claimsOf(token string) ([]string, time.Time) {
	claims := jwt.MapClaims{}
	if _, _, err := jwt.NewParser().ParseUnverified(token, claims); err != nil {
		return nil, time.Time{}
	}
	var perms []string
	if raw, ok := claims["permissions"].([]any); ok {
		for _, p := range raw {
			if s, ok := p.(string); ok {
				perms = append(perms, s)
			}
		}
	}
	exp, _ := claims.GetExpirationTime()
	if exp == nil {
		return perms, time.Time{}
	}
	return perms, exp.Time
}

// call runs fn as the actor on the organization. A 401 from IAMKit means
// the exchanged token ended early (its session was revoked): exchange again
// once.
func (d *Directory) call(ctx context.Context, org identity.OrganizationID, actor auth.Actor, fn func(apiclient.OrgAdmin) error) error {
	for attempt := 0; ; attempt++ {
		s, err := d.session(ctx, actor, attempt > 0)
		if err != nil {
			return err
		}
		if s.token == "" {
			return errx.Forbidden("you can't administer this organization")
		}
		admin := apiclient.New(d.cfg.URL, s.token, apiclient.WithHTTPClient(d.cfg.HTTP)).
			Environment(d.cfg.Environment).OrgAdmin(org.String())
		err = fn(admin)
		var api *apierror.Error
		if attempt == 0 && errors.As(err, &api) && api.HTTPStatus == http.StatusUnauthorized {
			continue
		}
		return failureOf(err)
	}
}

// failureOf maps IAMKit's API answer to errx. A 403 passes through with
// IAMKit's words (it names the missing iam:org permission); a 401 after a
// fresh exchange is IAMKit misbehaving, never a sign-out of the person.
func failureOf(err error) error {
	if err == nil {
		return nil
	}
	var typed *errx.Error
	if errors.As(err, &typed) {
		return err
	}
	var api *apierror.Error
	if !errors.As(err, &api) {
		return errx.Wrap(err, "IAMKit organization administration", errx.TypeExternal)
	}
	switch {
	case api.HTTPStatus == http.StatusForbidden:
		return errx.Forbidden("IAMKit refused: " + api.Message)
	case api.HTTPStatus == http.StatusUnauthorized, api.HTTPStatus >= 500:
		return errx.Wrap(err, "IAMKit organization administration", errx.TypeExternal)
	}
	return errx.FromStatus(api.HTTPStatus, api.Message)
}

func (d *Directory) Permissions(ctx context.Context, _ identity.OrganizationID, actor auth.Actor) ([]string, error) {
	s, err := d.session(ctx, actor, false)
	if err != nil {
		return nil, err
	}
	return s.perms, nil
}

func (d *Directory) Roles(ctx context.Context, org identity.OrganizationID, actor auth.Actor) ([]auth.DirRole, error) {
	var out []auth.DirRole
	err := d.call(ctx, org, actor, func(a apiclient.OrgAdmin) error {
		roles, err := a.Roles(ctx)
		for _, r := range roles {
			out = append(out, auth.DirRole{ID: r.ID, Name: r.Name, Permissions: r.Permissions, Seaport: r.ResourceID == d.cfg.Resource})
		}
		return err
	})
	return out, err
}

func (d *Directory) Members(ctx context.Context, org identity.OrganizationID, actor auth.Actor) ([]auth.DirMember, error) {
	var out []auth.DirMember
	err := d.call(ctx, org, actor, func(a apiclient.OrgAdmin) error {
		members, err := a.Members(ctx)
		if err != nil {
			return err
		}
		out = make([]auth.DirMember, len(members))
		errs := make([]error, len(members))
		sem := make(chan struct{}, roleFanOut)
		var wg sync.WaitGroup
		for i, m := range members {
			out[i] = auth.DirMember{ID: m.UserID, Name: m.UserName, Email: m.UserEmail, Active: m.Active}
			wg.Add(1)
			sem <- struct{}{}
			go func() {
				defer wg.Done()
				defer func() { <-sem }()
				out[i].Roles, errs[i] = memberRoles(ctx, a, m.UserID)
			}()
		}
		wg.Wait()
		return errors.Join(errs...)
	})
	return out, err
}

func memberRoles(ctx context.Context, a apiclient.OrgAdmin, user string) ([]auth.DirRoleRef, error) {
	held, err := a.MemberRoles(ctx, user)
	if err != nil {
		return nil, err
	}
	out := make([]auth.DirRoleRef, 0, len(held))
	for _, r := range held {
		out = append(out, auth.DirRoleRef{ID: r.RoleID, Name: r.RoleName})
	}
	return out, nil
}

func (d *Directory) Member(ctx context.Context, org identity.OrganizationID, actor auth.Actor, user string) (auth.DirMember, error) {
	var out auth.DirMember
	err := d.call(ctx, org, actor, func(a apiclient.OrgAdmin) error {
		members, err := a.Members(ctx)
		if err != nil {
			return err
		}
		for _, m := range members {
			if m.UserID != user {
				continue
			}
			out = auth.DirMember{ID: m.UserID, Name: m.UserName, Email: m.UserEmail, Active: m.Active}
			out.Roles, err = memberRoles(ctx, a, user)
			return err
		}
		return errx.NotFound("member not found")
	})
	return out, err
}

func (d *Directory) AssignRole(ctx context.Context, org identity.OrganizationID, actor auth.Actor, user, role string) error {
	return d.call(ctx, org, actor, func(a apiclient.OrgAdmin) error { return a.AssignRole(ctx, user, role) })
}

func (d *Directory) UnassignRole(ctx context.Context, org identity.OrganizationID, actor auth.Actor, user, role string) error {
	return d.call(ctx, org, actor, func(a apiclient.OrgAdmin) error { return a.UnassignRole(ctx, user, role) })
}

func (d *Directory) RemoveMember(ctx context.Context, org identity.OrganizationID, actor auth.Actor, user string) error {
	return d.call(ctx, org, actor, func(a apiclient.OrgAdmin) error { return a.RemoveMember(ctx, user) })
}

func (d *Directory) Invitations(ctx context.Context, org identity.OrganizationID, actor auth.Actor) ([]auth.DirInvitation, error) {
	var out []auth.DirInvitation
	err := d.call(ctx, org, actor, func(a apiclient.OrgAdmin) error {
		list, err := a.Invitations(ctx, "pending")
		for _, i := range list {
			out = append(out, invitation(i))
		}
		return err
	})
	return out, err
}

func (d *Directory) Invite(ctx context.Context, org identity.OrganizationID, actor auth.Actor, email string, roles []string) (auth.DirInvitation, error) {
	var out auth.DirInvitation
	err := d.call(ctx, org, actor, func(a apiclient.OrgAdmin) error {
		i, err := a.Invite(ctx, apiclient.OrgInvite{Email: email, RoleIDs: roles})
		out = invitation(i)
		return err
	})
	return out, err
}

func (d *Directory) Resend(ctx context.Context, org identity.OrganizationID, actor auth.Actor, id string) (auth.DirInvitation, error) {
	var out auth.DirInvitation
	err := d.call(ctx, org, actor, func(a apiclient.OrgAdmin) error {
		i, err := a.ResendInvitation(ctx, id)
		out = invitation(i)
		return err
	})
	return out, err
}

func (d *Directory) RevokeInvitation(ctx context.Context, org identity.OrganizationID, actor auth.Actor, id string) error {
	return d.call(ctx, org, actor, func(a apiclient.OrgAdmin) error { return a.RevokeInvitation(ctx, id) })
}

func invitation(i apiclient.OrgInvitation) auth.DirInvitation {
	out := auth.DirInvitation{ID: i.ID, Email: i.Email, Status: i.Status, RoleIDs: i.RoleIDs, Link: i.Link}
	if t, err := time.Parse(time.RFC3339, i.ExpiresAt); err == nil {
		out.ExpiresAt = &t
	}
	return out
}

func (d *Directory) APIKeys(ctx context.Context, org identity.OrganizationID, actor auth.Actor) ([]auth.DirAPIKey, error) {
	var out []auth.DirAPIKey
	err := d.call(ctx, org, actor, func(a apiclient.OrgAdmin) error {
		filter := apiclient.APIKeyFilter{OwnerType: "organization", ResourceID: d.cfg.Resource}
		for offset := 0; ; {
			page, err := a.APIKeys(ctx, filter, apiclient.Page{Limit: keyPage, Offset: offset})
			if err != nil {
				return err
			}
			for _, k := range page.Items {
				if k.Owner.Type == "organization" && k.ResourceID == d.cfg.Resource {
					out = append(out, key(k))
				}
			}
			offset += len(page.Items)
			if len(page.Items) == 0 || offset >= page.Page.Total {
				return nil
			}
		}
	})
	return out, err
}

func (d *Directory) APIKey(ctx context.Context, org identity.OrganizationID, actor auth.Actor, id string) (auth.DirAPIKey, error) {
	var out auth.DirAPIKey
	err := d.call(ctx, org, actor, func(a apiclient.OrgAdmin) error {
		k, err := a.APIKey(ctx, id)
		if err != nil {
			return err
		}
		// A member's own key or one on another resource is not ours to show.
		if k.Owner.Type != "organization" || k.ResourceID != d.cfg.Resource {
			return errx.NotFound("API key not found")
		}
		out = key(k)
		return nil
	})
	return out, err
}

func (d *Directory) CreateAPIKey(ctx context.Context, org identity.OrganizationID, actor auth.Actor, in auth.DirKey) (auth.DirIssuedKey, error) {
	var out auth.DirIssuedKey
	err := d.call(ctx, org, actor, func(a apiclient.OrgAdmin) error {
		k, err := a.CreateAPIKey(ctx, apiclient.NewAPIKey{OrganizationID: org.String(), ResourceID: d.cfg.Resource,
			Name: in.Name, Permissions: in.Permissions, ExpiresIn: in.ExpiresIn})
		out = auth.DirIssuedKey{DirAPIKey: key(k.APIKey), Value: k.Value}
		return err
	})
	return out, err
}

func (d *Directory) RevokeAPIKey(ctx context.Context, org identity.OrganizationID, actor auth.Actor, id string) error {
	return d.call(ctx, org, actor, func(a apiclient.OrgAdmin) error { return a.RevokeAPIKey(ctx, id) })
}

func key(k apiclient.APIKey) auth.DirAPIKey {
	out := auth.DirAPIKey{ID: k.ID, Name: k.Name, Permissions: k.Permissions, Hint: k.ObfuscatedValue, State: k.State,
		LastUsedAt: k.LastUsedAt, CreatedAt: k.CreatedAt}
	if k.Expires {
		t := k.ExpiresAt
		out.ExpiresAt = &t
	}
	if out.Permissions == nil {
		out.Permissions = []string{}
	}
	return out
}
