// Package config reads the process configuration from the environment.
package config

import (
	"os"
	"strings"
	"time"

	"github.com/Abraxas-365/puerto/internal/errx"
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

// Server is the configuration of `puerto serve`.
type Server struct {
	Addr        string // PUERTO_ADDR, default 127.0.0.1:8740
	DatabaseURL string // DATABASE_URL (required)
	PublicURL   string // PUERTO_PUBLIC_URL, default http://<Addr>; agents reach /mcp here
	AdminToken  string // PUERTO_ADMIN_TOKEN (required): CLI and /api/v1
	CrewToken   string // PUERTO_CREW_TOKEN (required): agents, /crew/ws and /mcp
	LogLevel    string // PUERTO_LOG_LEVEL, default info
}

func LoadServer() (Server, error) {
	c := Server{
		Addr:        env("PUERTO_ADDR", "127.0.0.1:8740"),
		DatabaseURL: os.Getenv("DATABASE_URL"),
		PublicURL:   strings.TrimRight(os.Getenv("PUERTO_PUBLIC_URL"), "/"),
		AdminToken:  os.Getenv("PUERTO_ADMIN_TOKEN"),
		CrewToken:   os.Getenv("PUERTO_CREW_TOKEN"),
		LogLevel:    env("PUERTO_LOG_LEVEL", "info"),
	}
	if c.PublicURL == "" {
		c.PublicURL = "http://" + c.Addr
	}
	switch {
	case c.DatabaseURL == "":
		return c, errx.Validation("DATABASE_URL is required")
	case len(c.AdminToken) < 16:
		return c, errx.Validation("PUERTO_ADMIN_TOKEN is required (at least 16 characters)")
	case len(c.CrewToken) < 16:
		return c, errx.Validation("PUERTO_CREW_TOKEN is required (at least 16 characters)")
	case c.AdminToken == c.CrewToken:
		return c, errx.Validation("PUERTO_ADMIN_TOKEN and PUERTO_CREW_TOKEN must differ")
	}
	return c, nil
}

func env(key, def string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return def
}
