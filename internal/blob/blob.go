// Package blob is the port for binary object storage (message media).
// Adapters live in blob/adapters; the domain depends only on Store, opened
// once in the composition root from SEAPORT_BLOB_URL.
//
// Objects are immutable: Put never overwrites, so a stored file can't be
// silently replaced. Their metadata lives in Postgres (the database is the
// index of what exists, not the bucket).
package blob

import (
	"context"
	"io"
	"strings"

	"github.com/Abraxas-365/seaport/internal/errx"
	"github.com/Abraxas-365/seaport/internal/identity"
)

// Store is implemented by every storage backend (S3 and compatibles, disk,
// memory).
//
// Errors are *errx.Error: NotFound when the key doesn't exist, Conflict when
// Put targets an existing key, Validation for a malformed key.
type Store interface {
	// Put stores r under key. It fails with Conflict if key already exists.
	Put(ctx context.Context, key string, r io.Reader, opts PutOptions) error
	// Get opens the object for reading; the caller must Close it.
	Get(ctx context.Context, key string) (io.ReadCloser, error)
	// Delete removes the object. Deleting a missing key returns NotFound.
	Delete(ctx context.Context, key string) error
}

// PutOptions describe an object being stored.
type PutOptions struct {
	ContentType string            // required
	Metadata    map[string]string // optional, small string pairs
}

// TenantKey builds a key scoped to an organization: "org/<org>/<parts...>".
// Domain code builds keys with it, from typed ids only, so tenants never
// share a prefix and no client input reaches a key.
func TenantKey(organization identity.OrganizationID, parts ...string) string {
	return "org/" + organization.String() + "/" + strings.Join(parts, "/")
}

// ValidateKey rejects keys that could escape their prefix or behave
// differently across backends.
func ValidateKey(key string) error {
	switch {
	case key == "":
		return errx.Validation("blob key is empty")
	case len(key) > 1024:
		return errx.Validation("blob key is too long")
	case strings.HasPrefix(key, "/"), strings.HasSuffix(key, "/"):
		return errx.Validation("blob key must not start or end with '/'")
	}
	for _, segment := range strings.Split(key, "/") {
		if segment == "" || segment == "." || segment == ".." {
			return errx.Validation("blob key has an empty or relative segment")
		}
	}
	if strings.ContainsAny(key, "\\\x00") {
		return errx.Validation("blob key has invalid characters")
	}
	return nil
}
