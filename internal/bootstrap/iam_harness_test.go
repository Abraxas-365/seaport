package bootstrap_test

// Harness of the iam mode end-to-end tests: a real IAMKit (pinned image)
// and its Postgres on a private Docker network, provisioned once through
// the Management API like scripts/iamkit-provision.sh, from the generated
// permission catalog (so the tests can't drift from iam/permissions.json).
// Each test gets its own port (startPortWith), organizations and users.
//
// Run: SEAPORT_TEST_IAMKIT=1 (Docker needed; the port's database comes from
// internal/testdb);
// SEAPORT_TEST_IAMKIT_IMAGE overrides the image. In CI a missing
// SEAPORT_TEST_IAMKIT fails instead of skipping.

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"html"
	"io"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Abraxas-365/seaport/internal/bootstrap"
	"github.com/Abraxas-365/seaport/internal/config"
	"github.com/Abraxas-365/seaport/internal/permission"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/network"
	"github.com/testcontainers/testcontainers-go/wait"
)

const (
	iamkitImage    = "ghcr.io/abraxas-365/iamkit:daacde6" // keep in step with the SDK pin in go.mod
	iamIssuer      = "http://localhost:8080"              // IAMKit's JWT_ISSUER
	iamAudience    = "https://seaport.test"
	iamPassword    = "e2e-Password-1234!"
	iamSecretKey   = "e2e-secret-key-0123456789abcdefghijkl"
	iamOrgKeyLimit = 50
)

// iam is the IAMKit shared by every iam test (started on first use,
// stopped by TestMain).
var iam struct {
	once                               sync.Once
	err                                error
	stop                               []func()
	base, key                          string
	environment, application, resource string
	roles                              map[string]string // manifest role name → IAMKit role id
	// Tenant administration (scripts/iamkit-provision.sh step 7): IAMKit's
	// own IAM resource linked to the application, the "Seaport member
	// admins" companion role on it, and the confidential token-exchange
	// client Seaport's server uses.
	iamResource, membersRole         string
	serverClient, serverClientSecret string
}

// membersRoleName is the companion role's name (iam.roles keeps its id too).
const membersRoleName = "Seaport member admins"

var iamSeq atomic.Int64

func TestMain(m *testing.M) {
	code := m.Run()
	for i := len(iam.stop) - 1; i >= 0; i-- {
		iam.stop[i]()
	}
	os.Exit(code)
}

// needIAMKit skips (fails in CI) without SEAPORT_TEST_IAMKIT=1 and starts
// the shared IAMKit once.
func needIAMKit(t *testing.T) {
	t.Helper()
	testDatabaseURL(t)
	if os.Getenv("SEAPORT_TEST_IAMKIT") != "1" {
		if os.Getenv("CI") != "" {
			t.Fatal("SEAPORT_TEST_IAMKIT=1 not set in CI")
		}
		t.Skip("SEAPORT_TEST_IAMKIT=1 not set (needs Docker)")
	}
	iam.once.Do(func() { iam.err = startIAMKit(context.Background()) })
	if iam.err != nil {
		t.Fatal(iam.err)
	}
}

func startIAMKit(ctx context.Context) error {
	net, err := network.New(ctx)
	if err != nil {
		return fmt.Errorf("docker network: %w", err)
	}
	iam.stop = append(iam.stop, func() { _ = net.Remove(context.Background()) })

	pg, err := testcontainers.Run(ctx, "postgres:16-alpine",
		network.WithNetwork([]string{"db"}, net),
		testcontainers.WithEnv(map[string]string{"POSTGRES_USER": "test", "POSTGRES_PASSWORD": "test", "POSTGRES_DB": "iamkit"}),
		testcontainers.WithWaitStrategyAndDeadline(time.Minute,
			wait.ForLog("database system is ready to accept connections").WithOccurrence(2)),
	)
	iam.stop = append(iam.stop, func() { _ = testcontainers.TerminateContainer(pg) })
	if err != nil {
		return fmt.Errorf("postgres for IAMKit: %w", err)
	}

	image := os.Getenv("SEAPORT_TEST_IAMKIT_IMAGE")
	if image == "" {
		image = iamkitImage
	}
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return err
	}
	pemKey := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})
	c, err := testcontainers.Run(ctx, image,
		network.WithNetwork([]string{"iamkit"}, net),
		testcontainers.WithExposedPorts("8080/tcp"),
		testcontainers.WithEnv(map[string]string{
			"DATABASE_URL":          "postgres://test:test@db:5432/iamkit?sslmode=disable",
			"SERVER_PORT":           "8080",
			"JWT_PRIVATE_KEY_PATH":  "/secrets/jwt.pem",
			"JWT_ISSUER":            iamIssuer,
			"OIDC_HMAC_SECRET":      strings.Repeat("e2e-hmac-", 6),
			"IAMKIT_ENCRYPTION_KEY": "BwcHBwcHBwcHBwcHBwcHBwcHBwcHBwcHBwcHBwcHBwc=",
			"RATE_LIMIT_PER_MINUTE": "100000",
			// Sign-in endpoints have fixed per-IP limits; the tests send a
			// fresh X-Forwarded-For per request, so trust it. Tests only.
			"IAMKIT_TRUSTED_PROXIES": "0.0.0.0/0,::/0",
		}),
		testcontainers.WithFiles(testcontainers.ContainerFile{
			Reader: bytes.NewReader(pemKey), ContainerFilePath: "/secrets/jwt.pem", FileMode: 0o644,
		}),
		testcontainers.WithWaitStrategyAndDeadline(2*time.Minute, wait.ForHTTP("/health").WithPort("8080/tcp")),
	)
	iam.stop = append(iam.stop, func() { _ = testcontainers.TerminateContainer(c) })
	if err != nil {
		if c != nil {
			if logs, lerr := c.Logs(ctx); lerr == nil {
				out, _ := io.ReadAll(logs)
				fmt.Fprintf(os.Stderr, "iamkit logs:\n%s\n", out)
			}
		}
		return fmt.Errorf("start %s: %w", image, err)
	}
	if iam.base, err = c.PortEndpoint(ctx, "8080/tcp", "http"); err != nil {
		return err
	}

	code, out, err := c.Exec(ctx, []string{"iamkit", "bootstrap", "--email", "owner@seaport.test", "--workspace", "Seaport", "--output", "/tmp/owner.json"})
	if err != nil || code != 0 {
		msg, _ := io.ReadAll(out)
		return fmt.Errorf("iamkit bootstrap (exit %d): %v %s", code, err, msg)
	}
	rc, err := c.CopyFileFromContainer(ctx, "/tmp/owner.json")
	if err != nil {
		return err
	}
	defer rc.Close()
	var owner struct {
		Key string `json:"management_key"`
	}
	if err := json.NewDecoder(rc).Decode(&owner); err != nil {
		return err
	}
	iam.key = owner.Key
	return provisionIAMKit()
}

// provisionIAMKit mirrors scripts/iamkit-provision.sh: project →
// environment → application + "seaport" resource (catalog, roles) → link;
// organization API keys allowed for the Seaport permissions.
func provisionIAMKit() error {
	var id struct {
		ID string `json:"id"`
	}
	if err := management("POST", "/projects", map[string]any{"name": "Seaport e2e"}, &id); err != nil {
		return err
	}
	if err := management("POST", "/projects/"+id.ID+"/environments", map[string]any{"name": "e2e"}, &id); err != nil {
		return err
	}
	iam.environment = id.ID
	if err := management("POST", envPath("/applications"), map[string]any{"name": "Seaport Web"}, &id); err != nil {
		return err
	}
	iam.application = id.ID
	if err := management("POST", envPath("/resources"), map[string]any{"name": permission.ResourceName,
		"prefix": permission.Prefix, "audience": iamAudience, "permissions": permission.All}, &id); err != nil {
		return err
	}
	iam.resource = id.ID
	if err := management("POST", envPath("/application-resources"),
		map[string]any{"application_id": iam.application, "resource_id": iam.resource}, nil); err != nil {
		return err
	}
	iam.roles = map[string]string{}
	for _, r := range permission.Roles {
		if err := management("POST", envPath("/roles"), map[string]any{"name": r.Name, "resource_id": iam.resource, "permissions": r.Permissions}, &id); err != nil {
			return err
		}
		iam.roles[r.Name] = id.ID
	}
	if err := provisionMembers(); err != nil {
		return err
	}
	// The CLI authenticates with organization API keys (ik_key_…).
	if err := management("PUT", envPath("/api-key-policy"), map[string]any{"user_keys": false, "organization_keys": true,
		"max_per_user": 10, "max_per_organization": iamOrgKeyLimit}, nil); err != nil {
		return err
	}
	return management("PUT", envPath("/resources/"+iam.resource+"/api-key-permissions"),
		map[string]any{"user": []string{}, "organization": permission.All}, nil)
}

// provisionMembers is step 7 of scripts/iamkit-provision.sh: link IAMKit's
// IAM resource, create the companion role (organization administration
// without settings, domains, SSO or audit) and the exchange client.
func provisionMembers() error {
	var resources struct {
		Items []struct {
			ID     string `json:"id"`
			Prefix string `json:"prefix"`
		} `json:"items"`
	}
	if err := management("GET", envPath("/resources"), nil, &resources); err != nil {
		return err
	}
	for _, r := range resources.Items {
		if r.Prefix == "iam" {
			iam.iamResource = r.ID
		}
	}
	if iam.iamResource == "" {
		return fmt.Errorf("IAMKit has no IAM resource (prefix iam)")
	}
	if err := management("POST", envPath("/application-resources"),
		map[string]any{"application_id": iam.application, "resource_id": iam.iamResource}, nil); err != nil {
		return err
	}
	var id struct {
		ID string `json:"id"`
	}
	if err := management("POST", envPath("/roles"), map[string]any{"name": membersRoleName, "resource_id": iam.iamResource,
		"permissions": []string{"iam:org:read", "iam:org:members:read", "iam:org:members:write", "iam:org:roles:read", "iam:org:roles:assign",
			"iam:org:invitations:write", "iam:org:api-keys:read", "iam:org:api-keys:write"}}, &id); err != nil {
		return err
	}
	iam.membersRole = id.ID
	var client struct {
		ID     string `json:"client_id"`
		Secret string `json:"client_secret"`
	}
	if err := management("POST", envPath("/oauth-clients"), map[string]any{"application_id": iam.application, "resource_id": iam.iamResource,
		"public": false, "redirect_uris": []string{}, "grant_types": []string{"urn:ietf:params:oauth:grant-type:token-exchange"}}, &client); err != nil {
		return err
	}
	iam.serverClient, iam.serverClientSecret = client.ID, client.Secret
	return nil
}

func envPath(p string) string { return "/environments/" + iam.environment + p }

// management calls IAMKit's Management API; out may be nil.
func management(method, path string, body, out any) error {
	var rd io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rd = bytes.NewReader(raw)
	}
	req, err := http.NewRequest(method, iam.base+"/management/v1"+path, rd)
	if err != nil {
		return err
	}
	req.Header.Set("X-API-Key", iam.key)
	req.Header.Set("Content-Type", "application/json")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	data, _ := io.ReadAll(res.Body)
	if res.StatusCode >= 300 {
		return fmt.Errorf("%s %s: %d %s", method, path, res.StatusCode, data)
	}
	if out != nil {
		return json.Unmarshal(data, out)
	}
	return nil
}

func mustManage(t *testing.T, method, path string, body, out any) {
	t.Helper()
	if err := management(method, path, body, out); err != nil {
		t.Fatal(err)
	}
}

// ---- per-test fixtures ----------------------------------------------------

type iamOrg struct{ ID, Name string }

func newIAMOrg(t *testing.T) iamOrg {
	t.Helper()
	name := fmt.Sprintf("Naviera %d", iamSeq.Add(1))
	var id struct {
		ID string `json:"id"`
	}
	mustManage(t, "POST", envPath("/organizations"), map[string]any{"name": name}, &id)
	return iamOrg{ID: id.ID, Name: name}
}

type iamUser struct{ ID, Name, Email string }

// newIAMUser creates a user, member of each org with exactly perms on the
// Seaport resource there.
func newIAMUser(t *testing.T, perms []string, orgs ...iamOrg) iamUser {
	t.Helper()
	n := iamSeq.Add(1)
	u := iamUser{Name: fmt.Sprintf("Capitana %d", n), Email: fmt.Sprintf("user%d@seaport.test", n)}
	var id struct {
		ID string `json:"id"`
	}
	mustManage(t, "POST", envPath("/users"), map[string]any{"name": u.Name, "email": u.Email, "password": iamPassword}, &id)
	u.ID = id.ID
	for _, o := range orgs {
		mustManage(t, "POST", envPath("/memberships"), map[string]any{"organization_id": o.ID, "user_id": u.ID}, nil)
		mustManage(t, "PUT", envPath("/grants"), map[string]any{"organization_id": o.ID, "user_id": u.ID,
			"resource_id": iam.resource, "permissions": perms}, nil)
	}
	return u
}

// newIAMRoleUser creates a member of org holding a manifest role (how
// scripts/iamkit-provision.sh and IAMKit's admin console give access).
func newIAMRoleUser(t *testing.T, org iamOrg, role string) iamUser {
	t.Helper()
	u := newIAMUser(t, nil)
	mustManage(t, "POST", envPath("/memberships"), map[string]any{"organization_id": org.ID, "user_id": u.ID}, nil)
	mustManage(t, "POST", envPath("/role-assignments"), map[string]any{"organization_id": org.ID, "user_id": u.ID, "role_id": iam.roles[role]}, nil)
	return u
}

// accessToken signs u in headlessly to org (an IAMKit access token for
// the Seaport resource, used as an admin bearer).
func accessToken(t *testing.T, u iamUser, org iamOrg) string {
	t.Helper()
	raw, _ := json.Marshal(map[string]string{"environment_id": iam.environment, "organization_id": org.ID,
		"application_id": iam.application, "resource_id": iam.resource, "email": u.Email, "password": iamPassword})
	req, _ := http.NewRequest(http.MethodPost, iam.base+"/identity/v1/login", bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Forwarded-For", freshIP())
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	var out struct {
		Access string `json:"access_token"`
	}
	body, _ := io.ReadAll(res.Body)
	if res.StatusCode != http.StatusOK || json.Unmarshal(body, &out) != nil || out.Access == "" {
		t.Fatalf("login %s: %d %s", u.Email, res.StatusCode, body)
	}
	return out.Access
}

type iamAPIKey struct{ ID, Name, Value string }

// newOrgAPIKey creates an organization API key on the Seaport resource.
func newOrgAPIKey(t *testing.T, org iamOrg, perms ...string) iamAPIKey {
	t.Helper()
	var k struct {
		ID    string `json:"id"`
		Value string `json:"value"`
	}
	name := fmt.Sprintf("cli %d", iamSeq.Add(1))
	mustManage(t, "POST", envPath("/api-keys"), map[string]any{"organization_id": org.ID, "resource_id": iam.resource,
		"name": name, "permissions": perms}, &k)
	if !strings.HasPrefix(k.Value, "ik_key_") {
		t.Fatalf("API key: %+v", k)
	}
	return iamAPIKey{ID: k.ID, Name: name, Value: k.Value}
}

// startIAMPort starts a port in iam mode with its own public OAuth client
// (hosted login, redirect to this port's /auth/callback). Tenant
// administration is on (the exchange client and the companion role).
func startIAMPort(t *testing.T) (*port, config.Server) {
	t.Helper()
	return startIAMPortWith(t, true)
}

// startIAMPortWith is startIAMPort with tenant administration on or off.
func startIAMPortWith(t *testing.T, members bool) (*port, config.Server) {
	t.Helper()
	needIAMKit(t)
	var cfg config.Server
	p := startPortWith(t, func(c *config.Server) {
		var client struct {
			ID string `json:"client_id"`
		}
		mustManage(t, "POST", envPath("/oauth-clients"), map[string]any{
			"application_id": iam.application, "resource_id": iam.resource, "public": true,
			"grant_types":   []string{"authorization_code", "refresh_token"},
			"redirect_uris": []string{c.PublicURL + "/auth/callback"}, "post_logout_redirect_uris": []string{c.PublicURL + "/"},
			"allowed_origins": []string{c.PublicURL}, "hosted_login": true,
		}, &client)
		c.Auth, c.AdminToken, c.SecretKey = config.AuthIAM, "", iamSecretKey
		c.IAMKit = config.IAMKit{URL: iam.base, PublicURL: iam.base, Issuer: iamIssuer, Environment: iam.environment,
			Application: iam.application, Resource: iam.resource, Audience: iamAudience, WebClientID: client.ID}
		if members {
			c.IAMKit.ServerClientID, c.IAMKit.ServerClientSecret, c.IAMKit.MembersRoleID = iam.serverClient, iam.serverClientSecret, iam.membersRole
		}
		if err := c.Validate(); err != nil {
			t.Fatal(err)
		}
		cfg = *c
	}, bootstrap.Options{})
	return p, cfg
}

func freshIP() string {
	n := iamSeq.Add(1)
	return fmt.Sprintf("10.%d.%d.%d", n>>16&255, n>>8&255, n&255)
}

// ---- a browser without JavaScript -------------------------------------------

// browser follows no redirects and keeps cookies per origin, ignoring
// Secure (IAMKit's __Host- binding cookie travels over plain http here).
type browser struct {
	t       *testing.T
	cookies map[string]map[string]*http.Cookie
}

type page struct {
	Status   int
	Body     string
	Location string // absolute
	Cookies  []*http.Cookie
}

func newBrowser(t *testing.T) *browser {
	return &browser{t: t, cookies: map[string]map[string]*http.Cookie{}}
}

var noRedirects = &http.Client{Timeout: 30 * time.Second,
	CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}

func (b *browser) do(method, raw string, body io.Reader, contentType string) page {
	b.t.Helper()
	req, err := http.NewRequest(method, raw, body)
	if err != nil {
		b.t.Fatal(err)
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	req.Header.Set("X-Forwarded-For", freshIP())
	origin := req.URL.Scheme + "://" + req.URL.Host
	for _, c := range b.cookies[origin] {
		req.AddCookie(&http.Cookie{Name: c.Name, Value: c.Value})
	}
	res, err := noRedirects.Do(req)
	if err != nil {
		b.t.Fatal(err)
	}
	data, _ := io.ReadAll(res.Body)
	_ = res.Body.Close()
	if b.cookies[origin] == nil {
		b.cookies[origin] = map[string]*http.Cookie{}
	}
	for _, c := range res.Cookies() {
		if c.MaxAge < 0 {
			delete(b.cookies[origin], c.Name)
		} else {
			b.cookies[origin][c.Name] = c
		}
	}
	p := page{Status: res.StatusCode, Body: string(data), Cookies: res.Cookies()}
	if loc := res.Header.Get("Location"); loc != "" {
		u, err := req.URL.Parse(loc)
		if err != nil {
			b.t.Fatal(err)
		}
		p.Location = u.String()
	}
	return p
}

func (b *browser) get(raw string) page { return b.do(http.MethodGet, raw, nil, "") }

// post submits a form.
func (b *browser) post(raw string, form url.Values) page {
	return b.do(http.MethodPost, raw, strings.NewReader(form.Encode()), "application/x-www-form-urlencoded")
}

// api calls a JSON endpoint with the browser's cookies; out may be nil.
func (b *browser) api(method, raw string, body, out any) int {
	b.t.Helper()
	var rd io.Reader
	ct := ""
	if body != nil {
		data, _ := json.Marshal(body)
		rd, ct = bytes.NewReader(data), "application/json"
	}
	p := b.do(method, raw, rd, ct)
	if out != nil && p.Status < 300 {
		if err := json.Unmarshal([]byte(p.Body), out); err != nil {
			b.t.Fatalf("%s %s: %v %s", method, raw, err, p.Body)
		}
	}
	return p.Status
}

// cookie is the browser's cookie name for the origin of raw.
func (b *browser) cookie(raw, name string) string {
	u, _ := url.Parse(raw)
	if c := b.cookies[u.Scheme+"://"+u.Host][name]; c != nil {
		return c.Value
	}
	return ""
}

var hiddenField = regexp.MustCompile(`name="([a-z_]+)" value="([^"]*)"`)

// fields returns the form values named name, in page order.
func (p page) fields(name string) []string {
	var out []string
	for _, m := range hiddenField.FindAllStringSubmatch(p.Body, -1) {
		if m[1] == name {
			out = append(out, html.UnescapeString(m[2]))
		}
	}
	return out
}

func (p page) field(name string) string {
	if f := p.fields(name); len(f) > 0 {
		return f[0]
	}
	return ""
}

// grantSeaport grants the Seaport resource to org (what the provisioning
// script does for a new organization): without the grant its administrators
// can't assign Seaport's roles or issue its API keys.
func grantSeaport(t *testing.T, org iamOrg) {
	t.Helper()
	mustManage(t, "PUT", envPath("/resource-grants"), map[string]any{"resource_id": iam.resource, "organization_id": org.ID, "role_ids": nil}, nil)
}

// newIAMMemberAdmin is a member of org with a Seaport role and, when
// manages is set, the companion role that opens the members page.
func newIAMMemberAdmin(t *testing.T, org iamOrg, role string, manages bool) iamUser {
	t.Helper()
	u := newIAMRoleUser(t, org, role)
	if manages {
		mustManage(t, "POST", envPath("/role-assignments"), map[string]any{"organization_id": org.ID, "user_id": u.ID, "role_id": iam.membersRole}, nil)
	}
	return u
}

// acceptInvitation accepts the invitation behind link as a new person
// (IAMKit's identity API, as the invitee's browser would) and returns them.
func acceptInvitation(t *testing.T, link, email string) iamUser {
	t.Helper()
	u, err := url.Parse(link)
	if err != nil || u.Query().Get("token") == "" {
		t.Fatalf("invitation link %q has no token", link)
	}
	name := "Invitada " + email
	raw, _ := json.Marshal(map[string]string{"token": u.Query().Get("token"), "name": name, "password": iamPassword})
	req, _ := http.NewRequest(http.MethodPost, iam.base+"/identity/v1/invitations/accept", bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Forwarded-For", freshIP())
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	body, _ := io.ReadAll(res.Body)
	if res.StatusCode != http.StatusOK && res.StatusCode != http.StatusCreated {
		t.Fatalf("accept invitation: %d %s", res.StatusCode, body)
	}
	var out struct {
		UserID string `json:"user_id"`
	}
	if err := json.Unmarshal(body, &out); err != nil || out.UserID == "" {
		t.Fatalf("accept invitation: %v %s", err, body)
	}
	return iamUser{ID: out.UserID, Name: name, Email: email}
}
