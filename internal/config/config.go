// Package config reads the process configuration from the environment.
package config

import (
	"net"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/Abraxas-365/seaport/internal/errx"
	"github.com/Abraxas-365/seaport/internal/identity"
)

const (
	// ClaimLease is how long a delivery stays claimed by a connector before
	// the sweeper hands it to someone else.
	ClaimLease = 10 * time.Minute
	// DockLease is how long a process owns an exclusive dock (WhatsApp)
	// without renewing: a stopped process's docks are taken over within
	// about that long (at once when it stops cleanly).
	DockLease = 30 * time.Second
	// SweepInterval is how often expired claims are released.
	SweepInterval = 30 * time.Second
	// EventRetention is how long the event log keeps events: a live client
	// reconnecting within it resumes, later ones refetch (reset).
	EventRetention = time.Hour
	// SessionTTL is how long a browser session (admin cookie) lasts.
	SessionTTL = 7 * 24 * time.Hour
)

// DefaultOrganization owns everything in the single-organization modes
// (none, token). Data created in them moves to iam mode by re-keying this id.
var DefaultOrganization = identity.MustParseOrganizationID("00000000-0000-4000-8000-000000000001")

// AuthMode is how `seaport serve` authenticates admins and agents.
type AuthMode string

const (
	// AuthNone: no credentials at all, one organization; only on loopback.
	AuthNone AuthMode = "none"
	// AuthToken: static admin and crew tokens, one organization.
	AuthToken AuthMode = "token"
	// AuthIAM: people sign in with IAMKit, many organizations; agents use
	// crew tokens of their organization.
	AuthIAM AuthMode = "iam"
)

func (m AuthMode) Valid() bool { return m == AuthNone || m == AuthToken || m == AuthIAM }

// IAMKit is where IAMKit is and which of its objects Seaport is, written
// by scripts/iamkit-provision.sh (iam mode).
type IAMKit struct {
	URL         string // IAMKIT_URL: how this process reaches IAMKit
	PublicURL   string // IAMKIT_PUBLIC_URL: how browsers do (default IAMKIT_URL)
	Issuer      string // IAMKIT_ISSUER: IAMKit's JWT_ISSUER (default IAMKIT_PUBLIC_URL)
	Environment string // IAMKIT_ENVIRONMENT_ID
	Application string // IAMKIT_APPLICATION_ID
	Resource    string // IAMKIT_RESOURCE_ID
	Audience    string // IAMKIT_AUDIENCE
	WebClientID string // IAMKIT_WEB_CLIENT_ID: the web app's public OAuth client
}

// Server is the configuration of `seaport serve`.
type Server struct {
	Addr        string   // SEAPORT_ADDR, default 127.0.0.1:8740
	DatabaseURL string   // DATABASE_URL (required)
	PublicURL   string   // SEAPORT_PUBLIC_URL, default http://<Addr>; agents reach /mcp here
	Auth        AuthMode // SEAPORT_AUTH: none | token (default) | iam
	AdminToken  string   // SEAPORT_ADMIN_TOKEN (token mode): CLI and /api/v1
	// SecretKey (SEAPORT_SECRET_KEY, ≥32 chars) seals the WhatsApp store
	// and, in iam mode (where it is required), IAMKit tokens at rest and
	// the sign-in cookie. Unset outside iam mode: the key is read from
	// SecretKeyFile, generated there (0600) on first start.
	SecretKey string
	// SecretKeyFile (SEAPORT_SECRET_KEY_FILE, default
	// <user config dir>/seaport/secret.key; /data/secret.key in the image).
	SecretKeyFile string
	// BlobURL (SEAPORT_BLOB_URL) is where message media are kept, sealed
	// with the secret key: file:///dir (one node), s3://bucket?region=…
	// (credentials from AWS_*), mem:// (tests), or off. Default: a blobs
	// directory next to SecretKeyFile (/data/blobs in the image); "" with
	// neither keeps no media (a note says one came).
	BlobURL  string
	IAMKit   IAMKit
	LogLevel string // SEAPORT_LOG_LEVEL, default info
	// Outbound limits per dock (0 disables one): SEAPORT_SEND_PER_MINUTE
	// (20), SEAPORT_SEND_PER_HOUR (300), SEAPORT_SEND_PER_CHAT_MINUTE (8).
	SendPerMinute, SendPerHour, SendPerChatMinute int
	// Per-organization quotas (0 disables one; same for every organization
	// until plans exist): SEAPORT_MAX_DOCKS (10),
	// SEAPORT_MAX_CREW_CONNECTIONS (20 live agents),
	// SEAPORT_SEND_ORG_PER_HOUR (1000 outbound messages across its docks).
	MaxDocks, MaxCrewConnections, SendOrgPerHour int
	// SEAPORT_API_RATE (20) and SEAPORT_API_BURST (60): requests per second
	// per caller (organization + subject + credential) on /api, /crew/ws
	// and /mcp; 0 disables.
	APIRate, APIBurst int
	// Hosted agents (0 disables a limit): SEAPORT_HOST_MAX_CPUS (4) and
	// SEAPORT_HOST_MAX_MEMORY_MB (8192) bound one slot's resources,
	// SEAPORT_MAX_SLOTS (10) an organization's slots.
	HostMaxCPUs, HostMaxMemoryMB, MaxSlots int
	// Runtime runs hosted agents: SEAPORT_RUNTIME off (default: nothing is
	// built or run) or docker (the engine at DOCKER_HOST, behind a
	// docker-socket proxy in production).
	Runtime Runtime
	// Base images per harness (environments without `image:` build FROM
	// them): SEAPORT_IMAGE_RNESS, SEAPORT_IMAGE_CLAUDE_CODE; default the
	// published ones at this release's version (ghcr.io/abraxas-365/…).
	ImageRness, ImageClaudeCode string
	// How slot containers reach the port: SEAPORT_AGENT_URL (default
	// SEAPORT_PUBLIC_URL; on one host e.g.
	// http://host.docker.internal:8740), on the Docker network
	// SEAPORT_RUNTIME_NETWORK (default bridge); SEAPORT_RUNTIME_HOST_GATEWAY
	// (default true) maps host.docker.internal to the host on Linux.
	AgentURL, RuntimeNetwork string
	RuntimeHostGateway       bool
	// Model gateway: SEAPORT_GATEWAY_ANTHROPIC_URL is where it forwards
	// Anthropic calls (default https://api.anthropic.com).
	GatewayAnthropicURL string
	// GitHub docks: SEAPORT_GITHUB_URL (default https://github.com) and
	// SEAPORT_GITHUB_API_URL (default https://api.github.com); GitHub
	// Enterprise Server: https://host and https://host/api/v3.
	GitHubURL, GitHubAPIURL string
	// GitHubOIDCURL is the issuer of GitHub Actions' OIDC tokens
	// (SEAPORT_GITHUB_OIDC_URL, default
	// https://token.actions.githubusercontent.com; GHES:
	// https://host/_services/token).
	GitHubOIDCURL string
	// Egress proxy of hosted slots: SEAPORT_EGRESS_ADDR is its listen
	// address ("": off, slots get no proxy and the runtime network alone
	// decides what they reach); slots reach it at SEAPORT_EGRESS_URL
	// (default SEAPORT_AGENT_URL's host with the listen port);
	// SEAPORT_EGRESS_PRIVATE=true lets allowed hosts resolve to private
	// addresses.
	EgressAddr, EgressURL string
	EgressPrivate         bool
}

// Runtime is where hosted agents run (SEAPORT_RUNTIME).
type Runtime string

const (
	RuntimeOff    Runtime = "off"
	RuntimeDocker Runtime = "docker"
)

// BaseImage is the published base image of a harness at a Seaport version
// (images/Dockerfile, pushed by the release workflow for v* tags):
// releases (v1.2.3, goreleaser's 1.2.3, pre-releases too) use their tag,
// anything else (dev, commit SHAs, Go pseudo-versions) :latest.
func BaseImage(harness, version string) string {
	tag := strings.TrimPrefix(version, "v")
	if !releaseVersion.MatchString(tag) || strings.HasPrefix(tag, "0.0.0-") {
		tag = "latest"
	}
	return "ghcr.io/abraxas-365/seaport-" + harness + ":" + tag
}

// releaseVersion is a release's semver (no build metadata).
var releaseVersion = regexp.MustCompile(`^[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.-]+)?$`)

func LoadServer() (Server, error) {
	c := Server{
		Addr:          env("SEAPORT_ADDR", "127.0.0.1:8740"),
		DatabaseURL:   os.Getenv("DATABASE_URL"),
		PublicURL:     strings.TrimRight(os.Getenv("SEAPORT_PUBLIC_URL"), "/"),
		Auth:          AuthMode(env("SEAPORT_AUTH", string(AuthToken))),
		AdminToken:    os.Getenv("SEAPORT_ADMIN_TOKEN"),
		SecretKey:     os.Getenv("SEAPORT_SECRET_KEY"),
		SecretKeyFile: strings.TrimSpace(os.Getenv("SEAPORT_SECRET_KEY_FILE")),
		BlobURL:       strings.TrimSpace(os.Getenv("SEAPORT_BLOB_URL")),
		IAMKit: IAMKit{
			URL:         strings.TrimRight(os.Getenv("IAMKIT_URL"), "/"),
			PublicURL:   strings.TrimRight(os.Getenv("IAMKIT_PUBLIC_URL"), "/"),
			Issuer:      os.Getenv("IAMKIT_ISSUER"),
			Environment: os.Getenv("IAMKIT_ENVIRONMENT_ID"),
			Application: os.Getenv("IAMKIT_APPLICATION_ID"),
			Resource:    os.Getenv("IAMKIT_RESOURCE_ID"),
			Audience:    os.Getenv("IAMKIT_AUDIENCE"),
			WebClientID: os.Getenv("IAMKIT_WEB_CLIENT_ID"),
		},
		LogLevel: env("SEAPORT_LOG_LEVEL", "info"),
		Runtime:  Runtime(env("SEAPORT_RUNTIME", string(RuntimeOff))),
		// empty: bootstrap fills BaseImage(harness, Version)
		ImageRness:          strings.TrimSpace(os.Getenv("SEAPORT_IMAGE_RNESS")),
		ImageClaudeCode:     strings.TrimSpace(os.Getenv("SEAPORT_IMAGE_CLAUDE_CODE")),
		AgentURL:            strings.TrimRight(os.Getenv("SEAPORT_AGENT_URL"), "/"),
		RuntimeNetwork:      env("SEAPORT_RUNTIME_NETWORK", "bridge"),
		RuntimeHostGateway:  os.Getenv("SEAPORT_RUNTIME_HOST_GATEWAY") != "false",
		GatewayAnthropicURL: strings.TrimRight(strings.TrimSpace(os.Getenv("SEAPORT_GATEWAY_ANTHROPIC_URL")), "/"),
		GitHubURL:           strings.TrimRight(env("SEAPORT_GITHUB_URL", "https://github.com"), "/"),
		GitHubAPIURL:        strings.TrimRight(env("SEAPORT_GITHUB_API_URL", "https://api.github.com"), "/"),
		GitHubOIDCURL:       strings.TrimRight(env("SEAPORT_GITHUB_OIDC_URL", "https://token.actions.githubusercontent.com"), "/"),
		EgressAddr:          strings.TrimSpace(os.Getenv("SEAPORT_EGRESS_ADDR")),
		EgressURL:           strings.TrimRight(strings.TrimSpace(os.Getenv("SEAPORT_EGRESS_URL")), "/"),
		EgressPrivate:       os.Getenv("SEAPORT_EGRESS_PRIVATE") == "true",
	}
	if c.IAMKit.PublicURL == "" {
		c.IAMKit.PublicURL = c.IAMKit.URL
	}
	if c.IAMKit.Issuer == "" {
		c.IAMKit.Issuer = c.IAMKit.PublicURL
	}
	var err error
	for _, l := range []struct {
		key string
		def int
		dst *int
	}{
		{"SEAPORT_SEND_PER_MINUTE", 20, &c.SendPerMinute},
		{"SEAPORT_SEND_PER_HOUR", 300, &c.SendPerHour},
		{"SEAPORT_SEND_PER_CHAT_MINUTE", 8, &c.SendPerChatMinute},
		{"SEAPORT_MAX_DOCKS", 10, &c.MaxDocks},
		{"SEAPORT_MAX_CREW_CONNECTIONS", 20, &c.MaxCrewConnections},
		{"SEAPORT_SEND_ORG_PER_HOUR", 1000, &c.SendOrgPerHour},
		{"SEAPORT_API_RATE", 20, &c.APIRate},
		{"SEAPORT_API_BURST", 60, &c.APIBurst},
		{"SEAPORT_HOST_MAX_CPUS", 4, &c.HostMaxCPUs},
		{"SEAPORT_HOST_MAX_MEMORY_MB", 8192, &c.HostMaxMemoryMB},
		{"SEAPORT_MAX_SLOTS", 10, &c.MaxSlots},
	} {
		if *l.dst, err = envInt(l.key, l.def); err != nil {
			return c, err
		}
	}
	if c.PublicURL == "" {
		c.PublicURL = "http://" + c.Addr
	}
	if c.AgentURL == "" {
		c.AgentURL = c.PublicURL
	}
	if c.SecretKeyFile == "" {
		if dir, err := os.UserConfigDir(); err == nil {
			c.SecretKeyFile = filepath.Join(dir, "seaport", "secret.key")
		}
	}
	switch {
	case c.BlobURL == BlobOff:
		c.BlobURL = ""
	case c.BlobURL == "" && c.SecretKeyFile != "":
		c.BlobURL = LocalBlobURL(filepath.Join(filepath.Dir(c.SecretKeyFile), "blobs"))
	}
	return c, c.Validate()
}

// BlobOff (SEAPORT_BLOB_URL=off) keeps no message media.
const BlobOff = "off"

// LocalBlobURL is a file:// bucket in dir (created; temporary files next
// to the objects, so the final rename stays on one filesystem).
func LocalBlobURL(dir string) string {
	return (&url.URL{Scheme: "file", Path: filepath.ToSlash(dir), RawQuery: "create_dir=1&no_tmp_dir=1"}).String()
}

// Validate checks the settings the chosen auth mode needs.
func (c Server) Validate() error {
	if c.DatabaseURL == "" {
		return errx.Validation("DATABASE_URL is required")
	}
	switch c.Runtime {
	case "", RuntimeOff, RuntimeDocker:
	default:
		return errx.Validation("SEAPORT_RUNTIME must be off or docker")
	}
	if c.Runtime == RuntimeDocker && c.Auth == AuthNone {
		// hosted agents sign in with slot tokens: there are none in open mode
		return errx.Validation("SEAPORT_RUNTIME=docker needs SEAPORT_AUTH=token or iam (hosted agents sign in with tokens)")
	}
	if c.EgressAddr != "" {
		if _, _, err := net.SplitHostPort(c.EgressAddr); err != nil {
			return errx.Validation("SEAPORT_EGRESS_ADDR must be host:port (e.g. :8741)")
		}
		if c.Auth == AuthNone {
			return errx.Validation("SEAPORT_EGRESS_ADDR needs SEAPORT_AUTH=token or iam (slots sign in to the proxy with their tokens)")
		}
	}
	if c.EgressURL != "" {
		if u, err := url.Parse(c.EgressURL); err != nil || u.Scheme != "http" || u.Host == "" || u.User != nil {
			return errx.Validation("SEAPORT_EGRESS_URL must be an http:// URL without credentials")
		}
	}
	if c.GatewayAnthropicURL != "" {
		if u, err := url.Parse(c.GatewayAnthropicURL); err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
			return errx.Validation("SEAPORT_GATEWAY_ANTHROPIC_URL must be an http(s) URL")
		}
	}
	for _, u := range []struct{ key, value string }{{"SEAPORT_GITHUB_URL", c.GitHubURL}, {"SEAPORT_GITHUB_API_URL", c.GitHubAPIURL}, {"SEAPORT_GITHUB_OIDC_URL", c.GitHubOIDCURL}} {
		if u.value == "" {
			continue
		}
		if p, err := url.Parse(u.value); err != nil || (p.Scheme != "https" && p.Scheme != "http") || p.Host == "" || p.User != nil {
			return errx.Validation(u.key + " must be an http(s) URL without credentials")
		}
	}
	switch c.Auth {
	case AuthNone:
		if !Loopback(c.Addr) {
			return errx.Validation("SEAPORT_AUTH=none only listens on loopback (127.0.0.1, ::1, localhost); " +
				"SEAPORT_ADDR " + c.Addr + " would expose an unauthenticated port")
		}
	case AuthToken:
		if len(c.AdminToken) < 16 {
			return errx.Validation("SEAPORT_ADMIN_TOKEN is required (at least 16 characters)")
		}
	case AuthIAM:
		return c.validateIAM()
	default:
		return errx.Validation("SEAPORT_AUTH must be none, token or iam")
	}
	return nil
}

func (c Server) validateIAM() error {
	k := c.IAMKit
	for _, req := range []struct{ key, value string }{
		{"IAMKIT_URL", k.URL}, {"IAMKIT_ENVIRONMENT_ID", k.Environment}, {"IAMKIT_APPLICATION_ID", k.Application},
		{"IAMKIT_RESOURCE_ID", k.Resource}, {"IAMKIT_AUDIENCE", k.Audience}, {"IAMKIT_WEB_CLIENT_ID", k.WebClientID},
	} {
		if strings.TrimSpace(req.value) == "" {
			return errx.Validation(req.key + " is required with SEAPORT_AUTH=iam (scripts/iamkit-provision.sh writes it)")
		}
	}
	for _, u := range []struct{ key, value string }{{"IAMKIT_URL", k.URL}, {"IAMKIT_PUBLIC_URL", k.PublicURL}, {"SEAPORT_PUBLIC_URL", c.PublicURL}} {
		parsed, err := url.Parse(u.value)
		if err != nil || (parsed.Scheme != "https" && parsed.Scheme != "http") || parsed.Host == "" {
			return errx.Validation(u.key + " must be an http(s) URL")
		}
		// Sessions, OAuth codes and tokens cross these: plain http only on
		// this machine (or a private service name for the server-side URL).
		if parsed.Scheme == "http" && !localHost(parsed.Hostname()) && !(u.key == "IAMKIT_URL" && privateHost(parsed.Hostname())) {
			return errx.Validation(u.key + " must be https (http only for localhost)")
		}
	}
	switch {
	case c.SecretKey == "":
		return errx.Validation("SEAPORT_SECRET_KEY is required with SEAPORT_AUTH=iam (e.g. openssl rand -base64 32)")
	case CheckSecretKey(c.SecretKey) != nil:
		return CheckSecretKey(c.SecretKey)
	case c.AdminToken != "":
		return errx.Validation("SEAPORT_ADMIN_TOKEN is a token mode setting; in iam mode people sign in with IAMKit and agents use crew tokens")
	}
	return nil
}

const (
	// MinSecretKey is the shortest SEAPORT_SECRET_KEY accepted.
	MinSecretKey = 32
	// minDistinct rejects placeholder keys ("xxxx…", "changemechangeme…"):
	// a random key (openssl rand -base64 32 / -hex 32) has far more.
	minDistinct = 12
)

// CheckSecretKey refuses short or placeholder SEAPORT_SECRET_KEYs (a
// heuristic against obvious placeholders, not an entropy measure).
func CheckSecretKey(secret string) error {
	if len(secret) < MinSecretKey {
		return errx.Validation("SEAPORT_SECRET_KEY must be at least 32 characters (openssl rand -base64 32)")
	}
	seen := map[byte]bool{}
	for i := 0; i < len(secret); i++ {
		seen[secret[i]] = true
	}
	if len(seen) < minDistinct {
		return errx.Validation("SEAPORT_SECRET_KEY looks like a placeholder; use a random key (openssl rand -base64 32)")
	}
	return nil
}

// Loopback reports whether a listen address only accepts local connections.
func Loopback(addr string) bool {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return false
	}
	return localHost(host)
}

// localHost: localhost or a loopback IP.
func localHost(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// privateHost: a name or address only reachable inside a private network
// (a single-label service name like "iamkit", or a private IP).
func privateHost(host string) bool {
	if ip := net.ParseIP(host); ip != nil {
		return ip.IsPrivate() || ip.IsLoopback()
	}
	return host != "" && !strings.Contains(host, ".")
}

func envInt(key string, def int) (int, error) {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		return def, nil
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < 0 {
		return 0, errx.InvalidField(key, key+" must be a whole number ≥ 0 (0 disables the limit)")
	}
	return n, nil
}

func env(key, def string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return def
}
