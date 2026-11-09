// Package crewsvc implements the crew token ports.
package crewsvc

import (
	"context"
	"time"

	"github.com/Abraxas-365/seaport/internal/crew"
	"github.com/Abraxas-365/seaport/internal/errx"
	"github.com/Abraxas-365/seaport/internal/identity"
	"github.com/Abraxas-365/seaport/internal/query"
	"github.com/Abraxas-365/seaport/internal/text"
)

// touchEvery limits last_used_at writes (MCP authenticates every call).
const touchEvery = time.Minute

type Service struct {
	repo         crew.TokenRepository
	disconnector crew.Disconnector
	now          func() time.Time
}

func New(repo crew.TokenRepository, d crew.Disconnector) *Service {
	return &Service{repo: repo, disconnector: d, now: time.Now}
}

var (
	_ crew.TokenCommands = (*Service)(nil)
	_ crew.TokenQueries  = (*Service)(nil)
)

func (s *Service) Create(ctx context.Context, org identity.OrganizationID, input crew.CreateToken) (crew.Issued, error) {
	if err := input.Validate(); err != nil {
		return crew.Issued{}, err
	}
	input.Name, input.CreatedBy = text.Clean(input.Name), text.Clean(input.CreatedBy)
	value, hash, hint := crew.NewTokenValue()
	t, err := s.repo.Create(ctx, org, identity.NewCrewTokenID(), input, hash, hint, crew.MaxTokens)
	if err != nil {
		return crew.Issued{}, errx.Wrap(err, "create crew token", errx.TypeInternal)
	}
	return crew.Issued{Token: t, Value: value}, nil
}

func (s *Service) Revoke(ctx context.Context, org identity.OrganizationID, id identity.CrewTokenID) error {
	if err := s.repo.Revoke(ctx, org, id); err != nil {
		return errx.Wrap(err, "revoke crew token", errx.TypeInternal)
	}
	if s.disconnector != nil {
		s.disconnector.Disconnect(org, id)
	}
	return nil
}

func (s *Service) Authenticate(ctx context.Context, value string) (crew.Token, error) {
	invalid := errx.Unauthorized("missing or invalid token")
	if !crew.LooksLikeToken(value) {
		return crew.Token{}, invalid
	}
	t, err := s.repo.FindByHash(ctx, crew.HashToken(value))
	if errx.IsType(err, errx.TypeNotFound) {
		return crew.Token{}, invalid
	}
	if err != nil {
		return crew.Token{}, errx.Wrap(err, "find crew token", errx.TypeInternal)
	}
	now := s.now()
	if t.Expired(now) {
		return crew.Token{}, invalid
	}
	if t.LastUsedAt == nil || now.Sub(*t.LastUsedAt) >= touchEvery {
		// Best effort: a failed write must not refuse a valid token.
		_ = s.repo.Touch(ctx, t.ID, now)
	}
	return t, nil
}

func (s *Service) List(ctx context.Context, org identity.OrganizationID, filter crew.TokenFilter, page query.Pagination) (query.Paginated[crew.Token], error) {
	return s.repo.List(ctx, org, filter, page.Normalize())
}
