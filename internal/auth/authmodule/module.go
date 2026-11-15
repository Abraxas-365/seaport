// Package authmodule wires iam mode's sign-in: IAMKit (authiam), the
// session store (authpg), the service and the HTTP Authenticator.
package authmodule

import (
	"context"
	"log/slog"
	"strings"
	"time"

	"github.com/Abraxas-365/seaport/internal/auth"
	"github.com/Abraxas-365/seaport/internal/auth/adapters/authhttp"
	"github.com/Abraxas-365/seaport/internal/auth/adapters/authiam"
	"github.com/Abraxas-365/seaport/internal/auth/adapters/authpg"
	"github.com/Abraxas-365/seaport/internal/auth/authsvc"
	"github.com/Abraxas-365/seaport/internal/config"
	"github.com/Abraxas-365/seaport/internal/httpx"
	"github.com/jmoiron/sqlx"
)

// SweepInterval is how often expired sessions are deleted.
const SweepInterval = time.Hour

type Module struct {
	Authenticator httpx.Authenticator
	Service       auth.Commands
	// Members is the tenant administration API (nil unless
	// IAMKIT_SERVER_CLIENT_ID is set).
	Members *authhttp.Members
}

// Register mounts the members routes on r; without the feature there are
// none (404).
func (m *Module) Register(r httpx.Router) {
	if m.Members != nil {
		m.Members.Register(r)
	}
}

func New(db *sqlx.DB, cfg config.Server, crew httpx.CrewResolver) (*Module, error) {
	sealer, err := auth.NewSealer(cfg.SecretKey)
	if err != nil {
		return nil, err
	}
	k := cfg.IAMKit
	provider := authiam.New(authiam.Config{URL: k.URL, PublicURL: k.PublicURL, Issuer: k.Issuer,
		Environment: k.Environment, Application: k.Application, Resource: k.Resource, Audience: k.Audience,
		ClientID: k.WebClientID, RedirectURI: cfg.PublicURL + authhttp.CallbackPath, AfterLogout: cfg.PublicURL + "/"})
	svc := authsvc.New(provider, authpg.New(db, sealer), config.SessionTTL)
	secure := strings.HasPrefix(cfg.PublicURL, "https://")
	mod := &Module{Service: svc, Authenticator: authhttp.New(svc, sealer, crew, secure, config.SessionTTL)}
	if cfg.MembersEnabled() {
		dir := authiam.NewDirectory(authiam.DirectoryConfig{URL: k.URL, Environment: k.Environment, Resource: k.Resource,
			ServerClientID: k.ServerClientID, ServerClientSecret: k.ServerClientSecret})
		members := authsvc.NewMembers(dir, k.MembersRoleID)
		mod.Members = authhttp.NewMembers(svc, members, members)
	}
	return mod, nil
}

// Sweep deletes expired sessions every SweepInterval until ctx ends.
func (m *Module) Sweep(ctx context.Context) {
	t := time.NewTicker(SweepInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if _, err := m.Service.Sweep(ctx); err != nil {
				slog.Error("sweep expired sessions", "err", err)
			}
		}
	}
}
