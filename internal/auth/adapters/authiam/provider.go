// Package authiam is IAMKit for iam mode (auth.Provider): the only package
// that imports the IAMKit SDK (vendorSDKs rule).
package authiam

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/Abraxas-365/iamkit/sdk/apierror"
	"github.com/Abraxas-365/iamkit/sdk/authclient"
	"github.com/golang-jwt/jwt/v5"

	"github.com/Abraxas-365/seaport/internal/auth"
	"github.com/Abraxas-365/seaport/internal/errx"
	"github.com/Abraxas-365/seaport/internal/identity"
)

// Config is where IAMKit is and which of its objects Seaport is
// (scripts/iamkit-provision.sh writes them).
type Config struct {
	// URL is how this process reaches IAMKit; PublicURL how browsers do
	// (authorize, end_session). Issuer is IAMKit's JWT_ISSUER.
	URL, PublicURL, Issuer                       string
	Environment, Application, Resource, Audience string
	// ClientID is the public (PKCE) OAuth client of the web app.
	ClientID string
	// RedirectURI is SEAPORT_PUBLIC_URL/auth/callback; AfterLogout where
	// IAMKit returns after signing out (SEAPORT_PUBLIC_URL/).
	RedirectURI, AfterLogout string
	// HTTP is the client for IAMKit calls (nil: 10 s timeout).
	HTTP *http.Client
}

// Scopes asked at sign-in: an id_token, a refresh token, name and email.
const Scopes = "openid profile email offline_access"

type Provider struct {
	cfg   Config
	iam   *authclient.Client
	oauth *authclient.OAuthClient
	keys  *authclient.KeySet
}

func New(cfg Config) *Provider {
	if cfg.HTTP == nil {
		cfg.HTTP = &http.Client{Timeout: 10 * time.Second}
	}
	return &Provider{
		cfg:   cfg,
		iam:   authclient.New(cfg.URL, authclient.WithHTTPClient(cfg.HTTP), authclient.WithAudience(cfg.Environment, cfg.Audience)),
		oauth: authclient.NewOAuth(cfg.URL, cfg.ClientID, "", authclient.WithOAuthHTTPClient(cfg.HTTP)),
		keys:  authclient.NewKeySet(cfg.URL+"/.well-known/jwks.json", cfg.HTTP),
	}
}

var _ auth.Provider = (*Provider)(nil)

func (p *Provider) AuthorizeURL(state, nonce, challenge, organization string, selectAccount bool) string {
	q := url.Values{
		"client_id": {p.cfg.ClientID}, "redirect_uri": {p.cfg.RedirectURI}, "response_type": {"code"},
		"scope": {Scopes}, "state": {state}, "nonce": {nonce},
		"code_challenge": {challenge}, "code_challenge_method": {"S256"},
	}
	if organization != "" {
		q.Set("organization_id", organization)
	}
	if selectAccount {
		q.Set("prompt", "select_account")
	}
	return p.cfg.PublicURL + "/oauth/authorize?" + q.Encode()
}

func (p *Provider) Exchange(ctx context.Context, code, verifier, nonce string) (auth.Tokens, error) {
	t, err := p.oauth.Exchange(ctx, code, p.cfg.RedirectURI, verifier)
	if err != nil {
		return auth.Tokens{}, failure(err, "exchange code")
	}
	if err := p.verifyIDToken(ctx, t.IDToken, nonce); err != nil {
		return auth.Tokens{}, err
	}
	return tokens(t), nil
}

// verifyIDToken: signed by IAMKit, for this client, of this sign-in.
func (p *Provider) verifyIDToken(ctx context.Context, raw, nonce string) error {
	if raw == "" {
		return errx.Unauthorized("IAMKit returned no id_token")
	}
	var claims struct {
		Nonce string `json:"nonce"`
		jwt.RegisteredClaims
	}
	_, err := jwt.ParseWithClaims(raw, &claims, func(t *jwt.Token) (any, error) {
		kid, _ := t.Header["kid"].(string)
		return p.keys.Key(ctx, kid)
	}, jwt.WithValidMethods([]string{"RS256"}), jwt.WithIssuer(p.cfg.Issuer), jwt.WithAudience(p.cfg.ClientID),
		jwt.WithExpirationRequired(), jwt.WithLeeway(30*time.Second))
	if err != nil {
		var api *apierror.Error
		if errors.As(err, &api) && api.HTTPStatus >= 500 {
			return errx.Wrap(err, "IAMKit signing keys unavailable", errx.TypeExternal)
		}
		return errx.Unauthorized("invalid id_token")
	}
	if claims.Nonce == "" || claims.Nonce != nonce {
		return errx.Unauthorized("id_token does not belong to this sign-in")
	}
	return nil
}

func (p *Provider) Refresh(ctx context.Context, refresh string) (auth.Tokens, error) {
	t, err := p.oauth.Refresh(ctx, refresh)
	if err != nil {
		return auth.Tokens{}, failure(err, "refresh token")
	}
	return tokens(t), nil
}

func (p *Provider) Revoke(ctx context.Context, token string) error {
	return failure(p.oauth.Revoke(ctx, token), "revoke token")
}

func (p *Provider) VerifyAccess(ctx context.Context, token string) (auth.Identity, error) {
	c, err := p.iam.Introspect(ctx, token, p.cfg.Issuer, p.cfg.Audience, p.cfg.Environment, p.cfg.Application, p.cfg.Resource)
	if err != nil {
		return auth.Identity{}, failure(err, "introspect token")
	}
	// Machine tokens (service accounts) belong to no organization's
	// people: tenant routes take user tokens and organization API keys.
	if c.Purpose != "application" || c.OrganizationID == "" {
		return auth.Identity{}, errx.Forbidden("the token is not bound to an organization")
	}
	org, err := identity.ParseOrganizationID(c.OrganizationID)
	if err != nil {
		return auth.Identity{}, errx.Forbidden("the token's organization is invalid")
	}
	who := auth.Identity{Organization: org, Subject: c.Subject, Permissions: c.Permissions}
	if c.ExpiresAt != nil {
		who.Expires = c.ExpiresAt.Time
	}
	return who, nil
}

func (p *Provider) IsAPIKey(value string) bool { return authclient.IsAPIKey(value) }

func (p *Provider) VerifyAPIKey(ctx context.Context, key string) (auth.Identity, error) {
	k, err := p.iam.ValidateAPIKey(ctx, key)
	if err != nil {
		return auth.Identity{}, failure(err, "validate API key")
	}
	if k.ResourceID != "" && k.ResourceID != p.cfg.Resource {
		return auth.Identity{}, errx.Unauthorized("the API key is for another resource")
	}
	org, err := identity.ParseOrganizationID(k.OrganizationID)
	if err != nil {
		return auth.Identity{}, errx.Forbidden("the API key is not bound to an organization")
	}
	return auth.Identity{Organization: org, Subject: "api-key:" + k.ID, Name: strings.TrimSpace("API key " + k.Name),
		Permissions: k.Permissions, Expires: k.ExpiresAt}, nil
}

func (p *Provider) Profile(ctx context.Context, access, organization string) (auth.Profile, error) {
	info, err := p.oauth.UserInfo(ctx, access)
	if err != nil {
		return auth.Profile{}, failure(err, "read userinfo")
	}
	out := auth.Profile{Name: info.Name, Email: info.Email}
	orgs, err := p.iam.Organizations(ctx, access, p.cfg.Environment, p.cfg.Audience)
	if err != nil {
		return out, failure(err, "list organizations")
	}
	for _, o := range orgs {
		if o.ID == organization {
			out.OrganizationName = o.Name
		}
	}
	return out, nil
}

func (p *Provider) EndSessionURL(idToken string) string {
	end := authclient.NewOAuth(p.cfg.PublicURL, p.cfg.ClientID, "")
	if idToken == "" {
		return end.EndSessionURL("", "", "")
	}
	return end.EndSessionURL(idToken, p.cfg.AfterLogout, "")
}

// defaultTokenLife is assumed when IAMKit omits expires_in (else every
// request would refresh); introspection still rejects an expired token.
const defaultTokenLife = 5 * time.Minute

func tokens(t authclient.OAuthTokens) auth.Tokens {
	life := time.Duration(t.ExpiresIn) * time.Second
	if life <= 0 {
		life = defaultTokenLife
	}
	return auth.Tokens{Access: t.AccessToken, Refresh: t.RefreshToken, IDToken: t.IDToken, ExpiresAt: time.Now().Add(life)}
}

// failure maps an IAMKit answer to errx: refused credentials are
// Unauthorized (the caller must sign in again), IAMKit being down or
// misconfigured is External (never a sign-out).
func failure(err error, op string) error {
	if err == nil {
		return nil
	}
	var api *apierror.Error
	if errors.As(err, &api) {
		switch {
		case api.HTTPStatus == http.StatusUnauthorized:
			return errx.Unauthorized("missing or invalid token")
		case api.HTTPStatus == http.StatusForbidden:
			return errx.Forbidden("IAMKit refused: " + api.Message)
		}
		return errx.Wrap(err, "IAMKit "+op, errx.TypeExternal)
	}
	var oauth *authclient.OAuthError
	if errors.As(err, &oauth) {
		switch oauth.Code {
		case "invalid_grant", "invalid_token", "access_denied", "login_required", "unauthorized_client":
			return errx.Unauthorized("IAMKit refused the sign-in (" + oauth.Code + ")")
		}
		return errx.Wrap(err, "IAMKit "+op, errx.TypeExternal)
	}
	return errx.Wrap(err, "IAMKit "+op, errx.TypeExternal)
}
