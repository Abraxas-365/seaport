// Package authpg stores iam mode's browser sessions in Postgres, with
// their IAMKit tokens sealed.
package authpg

import (
	"context"
	"encoding/json"
	"time"

	"github.com/Abraxas-365/seaport/internal/auth"
	"github.com/Abraxas-365/seaport/internal/errx"
	"github.com/Abraxas-365/seaport/internal/identity"
	"github.com/Abraxas-365/seaport/internal/pgerr"
	"github.com/jmoiron/sqlx"
)

// sealPurpose binds sealed tokens to this table.
const sealPurpose = "web_sessions.tokens"

var messages = pgerr.Messages{NotFound: "session not found"}

type Repository struct {
	db     *sqlx.DB
	sealer auth.Sealer
}

func New(db *sqlx.DB, sealer auth.Sealer) *Repository { return &Repository{db: db, sealer: sealer} }

var _ auth.SessionRepository = (*Repository)(nil)

type row struct {
	IDHash       string                  `db:"id_hash"`
	Organization identity.OrganizationID `db:"organization_id"`
	Subject      string                  `db:"subject"`
	Profile      []byte                  `db:"profile"`
	Tokens       string                  `db:"tokens"`
	ExpiresAt    time.Time               `db:"expires_at"`
	CreatedAt    time.Time               `db:"created_at"`
}

type sealedTokens struct {
	Access    string    `json:"a"`
	Refresh   string    `json:"r"`
	IDToken   string    `json:"i"`
	ExpiresAt time.Time `json:"e"`
}

const columns = `id_hash, organization_id, subject, profile, tokens, expires_at, created_at`

func (r *Repository) seal(t auth.Tokens) string {
	raw, _ := json.Marshal(sealedTokens{Access: t.Access, Refresh: t.Refresh, IDToken: t.IDToken, ExpiresAt: t.ExpiresAt})
	return r.sealer.Seal(sealPurpose, raw)
}

func (r *Repository) toSession(rw row) (auth.Session, error) {
	s := auth.Session{IDHash: rw.IDHash, Organization: rw.Organization, Subject: rw.Subject,
		ExpiresAt: rw.ExpiresAt, CreatedAt: rw.CreatedAt}
	_ = json.Unmarshal(rw.Profile, &s.Profile)
	plain, err := r.sealer.Open(sealPurpose, rw.Tokens)
	if err != nil {
		// Sealed with another SEAPORT_SECRET_KEY: the session is unusable.
		return auth.Session{}, errx.NotFound("session not found")
	}
	var t sealedTokens
	if err := json.Unmarshal(plain, &t); err != nil {
		return auth.Session{}, errx.NotFound("session not found")
	}
	s.Tokens = auth.Tokens{Access: t.Access, Refresh: t.Refresh, IDToken: t.IDToken, ExpiresAt: t.ExpiresAt}
	return s, nil
}

func (r *Repository) Create(ctx context.Context, s auth.Session) error {
	profile, _ := json.Marshal(s.Profile)
	_, err := r.db.ExecContext(ctx, `INSERT INTO web_sessions (id_hash, organization_id, subject, profile, tokens, access_expires, expires_at, created_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`,
		s.IDHash, s.Organization, s.Subject, profile, r.seal(s.Tokens), s.Tokens.ExpiresAt, s.ExpiresAt, s.CreatedAt)
	return pgerr.MapWith(err, "create session", messages)
}

func (r *Repository) Find(ctx context.Context, idHash string) (auth.Session, error) {
	var rw row
	if err := r.db.GetContext(ctx, &rw, `SELECT `+columns+` FROM web_sessions WHERE id_hash = $1`, idHash); err != nil {
		return auth.Session{}, pgerr.MapWith(err, "find session", messages)
	}
	return r.toSession(rw)
}

func (r *Repository) Update(ctx context.Context, idHash string, fn func(auth.Session) (auth.Session, error)) (auth.Session, error) {
	tx, err := r.db.BeginTxx(ctx, nil)
	if err != nil {
		return auth.Session{}, pgerr.Map(err, "begin session update")
	}
	defer func() { _ = tx.Rollback() }()
	var rw row
	if err := tx.GetContext(ctx, &rw, `SELECT `+columns+` FROM web_sessions WHERE id_hash = $1 FOR UPDATE`, idHash); err != nil {
		return auth.Session{}, pgerr.MapWith(err, "lock session", messages)
	}
	cur, err := r.toSession(rw)
	if err != nil {
		return auth.Session{}, err
	}
	next, err := fn(cur)
	if err != nil {
		return auth.Session{}, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE web_sessions SET tokens = $2, access_expires = $3, updated_at = now() WHERE id_hash = $1`,
		idHash, r.seal(next.Tokens), next.Tokens.ExpiresAt); err != nil {
		return auth.Session{}, pgerr.Map(err, "update session")
	}
	if err := tx.Commit(); err != nil {
		return auth.Session{}, pgerr.Map(err, "commit session update")
	}
	return next, nil
}

func (r *Repository) Delete(ctx context.Context, idHash string) error {
	_, err := r.db.ExecContext(ctx, `DELETE FROM web_sessions WHERE id_hash = $1`, idHash)
	return pgerr.Map(err, "delete session")
}

func (r *Repository) DeleteExpired(ctx context.Context, now time.Time) (int, error) {
	res, err := r.db.ExecContext(ctx, `DELETE FROM web_sessions WHERE expires_at <= $1`, now)
	if err != nil {
		return 0, pgerr.Map(err, "delete expired sessions")
	}
	n, _ := res.RowsAffected()
	return int(n), nil
}
