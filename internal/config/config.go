// Package config reads the process configuration from the environment.
package config

import (
	"net"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/Abraxas-365/puerto/internal/errx"
	"github.com/Abraxas-365/puerto/internal/identity"
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

// AuthMode is how `puerto serve` authenticates admins and agents.
type AuthMode string

const (
	// AuthNone: no credentials at all, one organization; only on loopback.
	AuthNone AuthMode = "none"
	// AuthToken: static admin and crew tokens, one organization.
	AuthToken AuthMode = "token"
)

func (m AuthMode) Valid() bool { return m == AuthNone || m == AuthToken }

// Server is the configuration of `puerto serve`.
type Server struct {
	Addr        string   // PUERTO_ADDR, default 127.0.0.1:8740
	DatabaseURL string   // DATABASE_URL (required)
	PublicURL   string   // PUERTO_PUBLIC_URL, default http://<Addr>; agents reach /mcp here
	Auth        AuthMode // PUERTO_AUTH: none | token (default)
	AdminToken  string   // PUERTO_ADMIN_TOKEN (token mode): CLI and /api/v1
	CrewToken   string   // PUERTO_CREW_TOKEN (token mode): agents, /crew/ws and /mcp
	LogLevel    string   // PUERTO_LOG_LEVEL, default info
	// Outbound limits per dock (0 disables one): PUERTO_SEND_PER_MINUTE
	// (20), PUERTO_SEND_PER_HOUR (300), PUERTO_SEND_PER_CHAT_MINUTE (8).
	SendPerMinute, SendPerHour, SendPerChatMinute int
}

func LoadServer() (Server, error) {
	c := Server{
		Addr:        env("PUERTO_ADDR", "127.0.0.1:8740"),
		DatabaseURL: os.Getenv("DATABASE_URL"),
		PublicURL:   strings.TrimRight(os.Getenv("PUERTO_PUBLIC_URL"), "/"),
		Auth:        AuthMode(env("PUERTO_AUTH", string(AuthToken))),
		AdminToken:  os.Getenv("PUERTO_ADMIN_TOKEN"),
		CrewToken:   os.Getenv("PUERTO_CREW_TOKEN"),
		LogLevel:    env("PUERTO_LOG_LEVEL", "info"),
	}
	var err error
	for _, l := range []struct {
		key string
		def int
		dst *int
	}{
		{"PUERTO_SEND_PER_MINUTE", 20, &c.SendPerMinute},
		{"PUERTO_SEND_PER_HOUR", 300, &c.SendPerHour},
		{"PUERTO_SEND_PER_CHAT_MINUTE", 8, &c.SendPerChatMinute},
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
			return errx.Validation("PUERTO_AUTH=none only listens on loopback (127.0.0.1, ::1, localhost); " +
				"PUERTO_ADDR " + c.Addr + " would expose an unauthenticated port")
		}
	case AuthToken:
		switch {
		case len(c.AdminToken) < 16:
			return errx.Validation("PUERTO_ADMIN_TOKEN is required (at least 16 characters)")
		case len(c.CrewToken) < 16:
			return errx.Validation("PUERTO_CREW_TOKEN is required (at least 16 characters)")
		case c.AdminToken == c.CrewToken:
			return errx.Validation("PUERTO_ADMIN_TOKEN and PUERTO_CREW_TOKEN must differ")
		}
	default:
		return errx.Validation("PUERTO_AUTH must be none or token")
	}
	return nil
}

// Loopback reports whether a listen address only accepts local connections.
func Loopback(addr string) bool {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return false
	}
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
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
