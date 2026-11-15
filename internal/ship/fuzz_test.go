package ship_test

import (
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/Abraxas-365/seaport/internal/ship"
	"github.com/Abraxas-365/seaport/internal/text"
)

// FuzzMatchOption: a reply from a chat (untrusted) resolves to one of the
// options verbatim, or to nothing; the card never fails to render.
func FuzzMatchOption(f *testing.F) {
	for _, s := range []string{
		"2", "2.", "opción 2", "Option 1", "NO.3", "yes", "  Sí! ", "@bot 2", "@a @b yes", "@", "@ ", "2 pero espera", "0", "7", "02", "٢", "1e1",
		"nº 1", "n.:  2", "(1)", "\"deploy\"", "", "  ", strings.Repeat("a", 500), "opción", "opción 99999999999999999999",
	} {
		f.Add(s, "Deploy", "Cancel", "Yes", "No")
	}
	f.Fuzz(func(t *testing.T, reply, a, b, c, d string) {
		// shipsvc stores the options cleaned (text.Clean)
		options := []string{text.Clean(a), text.Clean(b), text.Clean(c), text.Clean(d)}
		got, ok := ship.MatchOption(options, reply)
		if !ok {
			if got != "" {
				t.Fatalf("no match but %q", got)
			}
			return
		}
		found := false
		for _, o := range options {
			found = found || o == got
		}
		if !found {
			t.Fatalf("%q is not one of %q", got, options)
		}
		_ = ship.ApprovalCard(reply, options)
		if ship.ValidateOptions(options) == nil && (got == "" || strings.ContainsAny(got, "\n\r")) {
			t.Fatalf("a valid option matched as %q", got)
		}
	})
}

// FuzzValidateFiles: validation never panics, and what it accepts obeys the
// limits and has plain, unique names.
func FuzzValidateFiles(f *testing.F) {
	f.Add("report.txt", []byte("hello"), "Report.TXT", []byte("x"))
	f.Add("../etc/passwd", []byte("x"), "b", []byte("y"))
	f.Add("a b (1)+2.png", []byte("\x89PNG"), "c", []byte{})
	f.Add("", []byte("x"), "\x00", []byte("x"))
	f.Add("..", []byte("x"), "a..b", []byte("x"))
	f.Add(strings.Repeat("n", 300), []byte("x"), "ok", []byte("x"))
	f.Fuzz(func(t *testing.T, n1 string, d1 []byte, n2 string, d2 []byte) {
		files := []ship.File{{Name: n1, Data: d1}, {Name: n2, Data: d2}}
		if ship.ValidateFiles(files) != nil {
			return
		}
		if len(files) > ship.MaxAttachments || len(d1)+len(d2) > ship.MaxFiles {
			t.Fatal("limits broken")
		}
		for _, fl := range files {
			if len(fl.Data) == 0 || len(fl.Data) > ship.MaxFile || strings.ContainsAny(fl.Name, "/\\\x00") || strings.Contains(fl.Name, "..") ||
				utf8.RuneCountInString(fl.Name) > ship.MaxAttachmentName || text.HasForbiddenControl(fl.Name) {
				t.Fatalf("accepted file %q (%d bytes)", fl.Name, len(fl.Data))
			}
		}
		if strings.EqualFold(n1, n2) {
			t.Fatalf("accepted duplicate names %q %q", n1, n2)
		}
	})
}

// FuzzToolTitle: what an agent calls a tool ends up stored and shown: no
// control characters, bounded, valid UTF-8 and no credential-looking
// string.
func FuzzToolTitle(f *testing.F) {
	for _, s := range []string{
		"Read file main.go", "curl -H 'Authorization: Bearer sk-" + "ant-api03-" + strings.Repeat("a", 30) + "'", "ghp" + "_" + strings.Repeat("A", 36),
		"\x00\x1b[31mred\x1b[0m", strings.Repeat("é", 400), "-----BEGIN RSA PRIVATE KEY" + "-----", "sk-" + "ant-sk-" + "ant-" + strings.Repeat("b", 25),
		"xox" + "b-1234567890-abc", "AKIAABCDEFGHIJKLMNOP", "sp_crew_" + strings.Repeat("z", 20), "\xff\xfe", "",
	} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		got := ship.ToolTitle(s)
		if utf8.RuneCountInString(got) > ship.MaxToolTitle || !utf8.ValidString(got) || text.HasForbiddenControl(got) {
			t.Fatalf("bad title %q", got)
		}
		for _, p := range text.Secrets {
			if p.Re.MatchString(got) {
				t.Fatalf("title still holds %s: %q (from %q)", p.What, got, s)
			}
		}
	})
}
