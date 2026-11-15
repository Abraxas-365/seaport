package text_test

import (
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/Abraxas-365/seaport/internal/text"
)

// FuzzValidation: Required/Optional accept only trimmed-bounded valid UTF-8
// without forbidden controls, and StripControl output always passes.
func FuzzValidation(f *testing.F) {
	for _, s := range []string{"", " ", "hello", "hola\nmundo\t!", "nul\x00", "bell\a", "\xff", "é", "\u200b", "\u2028", strings.Repeat("x", 300), "\x7f", "\x1b[0m"} {
		f.Add(s, 10)
	}
	f.Fuzz(func(t *testing.T, s string, max int) {
		max = max % 400
		opt := text.Optional("f", s, max)
		req := text.Required("f", s, max)
		if req == nil && (strings.TrimSpace(s) == "" || opt != nil) {
			t.Fatalf("Required accepted %q", s)
		}
		if opt == nil {
			c := text.Clean(s)
			if !utf8.ValidString(c) || text.HasForbiddenControl(c) || utf8.RuneCountInString(c) > max {
				t.Fatalf("Optional accepted %q (max %d)", s, max)
			}
		}
		stripped := text.StripControl(s)
		if !utf8.ValidString(stripped) || text.HasForbiddenControl(stripped) {
			t.Fatalf("StripControl left %q", stripped)
		}
		if text.Optional("f", stripped, utf8.RuneCountInString(stripped)+1) != nil {
			t.Fatalf("stripped text refused: %q", stripped)
		}
	})
}

// FuzzRedact: Redact never panics, only ever shortens credential matches to
// the marker, and leaves nothing a pattern matches.
func FuzzRedact(f *testing.F) {
	for _, s := range []string{
		"sk-" + "ant-api03-" + strings.Repeat("a", 30), "ghp" + "_" + strings.Repeat("A", 36), "github_pat_" + strings.Repeat("a", 50),
		"glpat-" + strings.Repeat("a", 20), "xox" + "b-1234567890-abc", "AKIAABCDEFGHIJKLMNOP", "AIza" + strings.Repeat("a", 35),
		"-----BEGIN RSA PRIVATE KEY" + "-----", "sp_crew_" + strings.Repeat("z", 16), "ik_key_" + strings.Repeat("z", 16),
		"sk-" + strings.Repeat("a", 40), "nothing to see", "", "\xff", "sk-" + "ant-" + strings.Repeat("a", 20) + "sk-" + "ant-" + strings.Repeat("b", 20),
	} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		got := text.Redact(s)
		if len(got) > len(s)+len(text.Redacted) && !strings.Contains(got, text.Redacted) {
			t.Fatalf("grew without redacting: %q", got)
		}
		clean := true
		for _, p := range text.Secrets {
			if p.Re.MatchString(s) {
				clean = false
			}
		}
		if clean && got != s {
			t.Fatalf("changed text with no secret: %q -> %q", s, got)
		}
	})
}
