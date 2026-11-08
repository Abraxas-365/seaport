// Package config reads the process configuration from the environment.
package config

import (
	"net"
	"net/url"
	"os"
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
	// SweepInterval is how often expired claims are released.
	SweepInterval = 30 * time.Second
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
	CrewToken   string   // SEAPORT_CREW_TOKEN (token mode, optional): a static agent token besides the crew tokens created in the admin
	// SecretKey (SEAPORT_SECRET_KEY, iam mode, ≥32 chars) seals IAMKit
	// tokens at rest and the sign-in cookie.
	SecretKey string
	IAMKit    IAMKit
	LogLevel  string // SEAPORT_LOG_LEVEL, default info
	// Outbound limits per dock (0 disables one): SEAPORT_SEND_PER_MINUTE
	// (20), SEAPORT_SEND_PER_HOUR (300), SEAPORT_SEND_PER_CHAT_MINUTE (8).
	SendPerMinute, SendPerHour, SendPerChatMinute int
}

func LoadServer() (Server, error) {
	c := Server{
		Addr:        env("SEAPORT_ADDR", "127.0.0.1:8740"),
		DatabaseURL: os.Getenv("DATABASE_URL"),
		PublicURL:   strings.TrimRight(os.Getenv("SEAPORT_PUBLIC_URL"), "/"),
		Auth:        AuthMode(env("SEAPORT_AUTH", string(AuthToken))),
		AdminToken:  os.Getenv("SEAPORT_ADMIN_TOKEN"),
		CrewToken:   os.Getenv("SEAPORT_CREW_TOKEN"),
		SecretKey:   os.Getenv("SEAPORT_SECRET_KEY"),
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
	} {
		if *l.dst, err = envInt(l.key, l.def); err != nil {
			return c, err
		}
	}
	if c.PublicURL == "" {
		c.PublicURL = "http://" + c.Addr
	}
	return c, c.Validate()
}

// Validate checks the settings the chosen auth mode needs.
func (c Server) Validate() error {
	if c.DatabaseURL == "" {
		return errx.Validation("DATABASE_URL is required")
	}
	switch c.Auth {
	case AuthNone:
		if !Loopback(c.Addr) {
			return errx.Validation("SEAPORT_AUTH=none only listens on loopback (127.0.0.1, ::1, localhost); " +
				"SEAPORT_ADDR " + c.Addr + " would expose an unauthenticated port")
		}
	case AuthToken:
		switch {
		case len(c.AdminToken) < 16:
			return errx.Validation("SEAPORT_ADMIN_TOKEN is required (at least 16 characters)")
		case c.CrewToken != "" && len(c.CrewToken) < 16:
			return errx.Validation("SEAPORT_CREW_TOKEN must be at least 16 characters (or unset: agents use crew tokens created in the admin)")
		case c.CrewToken != "" && c.AdminToken == c.CrewToken:
			return errx.Validation("SEAPORT_ADMIN_TOKEN and SEAPORT_CREW_TOKEN must differ")
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
	case len(c.SecretKey) < 32:
		return errx.Validation("SEAPORT_SECRET_KEY is required with SEAPORT_AUTH=iam (at least 32 characters; e.g. openssl rand -base64 32)")
	case c.AdminToken != "" || c.CrewToken != "":
		return errx.Validation("SEAPORT_ADMIN_TOKEN and SEAPORT_CREW_TOKEN are token mode settings; in iam mode people sign in with IAMKit and agents use crew tokens")
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
