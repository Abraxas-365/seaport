// Package pgerr maps Postgres errors to typed errx errors, in one place.
package pgerr

import (
	"context"
	"database/sql"
	"errors"

	"github.com/Abraxas-365/seaport/internal/errx"
	"github.com/jackc/pgx/v5/pgconn"
)

// Messages are the client-facing messages for constraint violations.
type Messages struct{ Unique, ForeignKey, Check, NotFound string }

// ErrNoRows is sql.ErrNoRows, re-exported so repositories can report
// "affected nothing" through MapWith.
var ErrNoRows = sql.ErrNoRows

// IsNoRows reports whether err is sql.ErrNoRows.
func IsNoRows(err error) bool { return errors.Is(err, sql.ErrNoRows) }

// Map maps err with default messages. op is for logs only.
func Map(err error, op string) error { return MapWith(err, op, Messages{}) }

func MapWith(err error, op string, m Messages) error {
	if err == nil {
		return nil
	}
	var e *errx.Error
	if errors.As(err, &e) {
		return err
	}
	if errors.Is(err, sql.ErrNoRows) {
		return errx.NotFound(or(m.NotFound, "resource not found"))
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return errx.Wrap(err, op, errx.TypeInternal)
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		switch pgErr.Code {
		case "23505":
			return errx.Conflict(or(m.Unique, "a resource with these values already exists"))
		case "23503":
			return errx.NotFound(or(m.ForeignKey, "a referenced resource does not exist"))
		case "23514":
			return errx.Validation(or(m.Check, "a value is not allowed"))
		case "40001", "40P01":
			return errx.Conflict("concurrent modification, retry")
		case "22021", "22P05":
			return errx.Validation("text contains characters that are not allowed")
		case "22P02", "22007", "22008", "22003", "22001":
			return errx.Validation("a value has an invalid format or is out of range")
		}
	}
	return errx.Wrap(err, op, errx.TypeInternal)
}

func or(s, def string) string {
	if s == "" {
		return def
	}
	return s
}
