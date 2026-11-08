// Package text validates user-supplied text: trimmed, bounded, valid UTF-8
// and free of control characters other than \n and \t (a NUL would reach
// Postgres and come back as a 500).
package text

import (
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/Abraxas-365/puerto/internal/errx"
)

const (
	MaxName    = 200
	MaxMessage = 65536
	MaxSearch  = 200
)

func Clean(s string) string { return strings.TrimSpace(s) }

func Required(field, s string, max int) error {
	if strings.TrimSpace(s) == "" {
		return errx.InvalidField(field, field+" is required")
	}
	return Optional(field, s, max)
}

func Optional(field, s string, max int) error {
	s = strings.TrimSpace(s)
	switch {
	case !utf8.ValidString(s):
		return errx.InvalidField(field, field+" must be valid UTF-8 text")
	case utf8.RuneCountInString(s) > max:
		return errx.InvalidField(field, field+" must be at most "+strconv.Itoa(max)+" characters")
	case HasForbiddenControl(s):
		return errx.InvalidField(field, field+" contains control characters that are not allowed")
	}
	return nil
}

func HasForbiddenControl(s string) bool {
	for _, r := range s {
		if unicode.IsControl(r) && r != '\n' && r != '\t' && r != '\r' {
			return true
		}
	}
	return false
}
