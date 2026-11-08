// Package query owns the pagination types that appear in domain ports.
// Filters stay in the domain packages; they are entity-specific.
package query

const (
	DefaultLimit = 20
	MaxLimit     = 100
	MaxOffset    = 10000
)

// Pagination holds limit/offset for paginated queries.
type Pagination struct {
	Limit  int
	Offset int
}

// Normalize applies the default limit and clamps limit/offset into range.
func (p Pagination) Normalize() Pagination {
	if p.Limit <= 0 {
		p.Limit = DefaultLimit
	}
	if p.Limit > MaxLimit {
		p.Limit = MaxLimit
	}
	if p.Offset < 0 {
		p.Offset = 0
	}
	if p.Offset > MaxOffset {
		p.Offset = MaxOffset
	}
	return p
}

// Page is the pagination metadata echoed to clients: the effective
// limit/offset after clamping plus the total match count.
type Page struct {
	Total  int `json:"total"`
	Limit  int `json:"limit"`
	Offset int `json:"offset"`
}

// Paginated is the standard list envelope.
type Paginated[T any] struct {
	Items []T  `json:"items"`
	Page  Page `json:"page"`
}
