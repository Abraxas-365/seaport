package dockemail

import (
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/Abraxas-365/seaport/internal/ship"
	"github.com/Abraxas-365/seaport/internal/text"
)

// FuzzParseMessage: mail is untrusted bytes. Parsing never panics and what
// it returns respects the limits one email may bring into a ship.
func FuzzParseMessage(f *testing.F) {
	png := "\x89PNG\r\n\x1a\n" + strings.Repeat("x", 20)
	for _, s := range []string{
		"From: Ana <Ana@Example.com>\r\nTo: Bot <bot@example.com>, Luis <luis@example.com>\r\nCc: eva@example.com\r\nSubject: =?UTF-8?Q?Pedido_n=C3=BAmero_5?=\r\nMessage-ID: <abc@mail.example.com>\r\nIn-Reply-To: <prev@example.com>\r\nReferences: <first@example.com> <prev@example.com>\r\nDate: Mon, 02 Jan 2026 15:04:05 +0000\r\nContent-Type: text/plain; charset=utf-8\r\n\r\nHola\r\n\r\nOn Mon, 1 Jan 2026 at 10:00, Bot <bot@example.com> wrote:\r\n> earlier text\r\n",
		"From: ana@example.com\r\nSubject: Fotos\r\nMessage-ID: <m1@example.com>\r\nMIME-Version: 1.0\r\nContent-Type: multipart/mixed; boundary=BB\r\n\r\n--BB\r\nContent-Type: multipart/alternative; boundary=AA\r\n\r\n--AA\r\nContent-Type: text/plain\r\n\r\nplain\r\n--AA\r\nContent-Type: text/html\r\n\r\n<p>html</p>\r\n--AA--\r\n--BB\r\nContent-Type: image/png; name=\"a.png\"\r\nContent-Disposition: attachment; filename=\"a.png\"\r\nContent-Transfer-Encoding: binary\r\n\r\n" + png + "\r\n--BB\r\nContent-Type: application/pdf\r\nContent-Disposition: attachment; filename=\"doc.pdf\"\r\n\r\n%PDF-1.4\r\n--BB--\r\n",
		"From: a@example.com\r\nContent-Type: text/html; charset=iso-8859-1\r\nContent-Transfer-Encoding: quoted-printable\r\n\r\n<p style=\"display:none\">hidden</p>caf=E9<!-- c --><script>x</script>\r\n",
		"From: a@example.com\r\nAuthentication-Results: mx.example.com; dkim=pass header.d=example.com; spf=pass smtp.mailfrom=a@example.com\r\nAuto-Submitted: auto-replied\r\n\r\nout of office\r\n",
		"From: a@example.com\r\nContent-Type: text/plain; charset=x-unknown\r\nContent-Transfer-Encoding: base64\r\n\r\n!!!notbase64\r\n",
		"From: \"\x00\" <@>\r\nTo: a@b, ,,<<\r\nSubject: =?UTF-8?B?/w==?=\r\n\r\n",
		"Content-Type: multipart/mixed; boundary=\r\n\r\n--\r\n", "Subject:\r\n", "", "\r\n\r\n", "no headers at all",
	} {
		f.Add([]byte(s))
	}
	f.Fuzz(func(t *testing.T, raw []byte) {
		m, err := ParseMessage(raw)
		if err != nil {
			return
		}
		if utf8.RuneCountInString(m.Subject) > maxSubject || len(m.Others) > MaxParticipants || len(m.To) > MaxParticipants ||
			len(m.Media) > ship.MaxAttachments {
			t.Fatalf("out of limits: %+v", m)
		}
		for _, a := range append(append([]string{m.ReplyTo}, m.To...), m.Others...) {
			if a != "" && !validAddress(a) {
				t.Fatalf("invalid address kept: %q", a)
			}
		}
		if !utf8.ValidString(m.Text) {
			t.Fatalf("text is not UTF-8: %q", m.Text)
		}
		_ = chatID(m.ID)
		_ = SubjectKey(m.Subject)
		_ = replySubject(m.Subject)
	})
}

// FuzzMailText covers the text helpers on their own: HTML to text, quote
// stripping, authentication results and subject normalization.
func FuzzMailText(f *testing.F) {
	for _, s := range []string{
		"<p style='display:none'>x</p><b>hi</b>&amp;<br>there", "<!-- unclosed", "<script>", "<p style=\"font-size:0\">", "<<<>>>",
		"hi\n> quoted\nOn Mon, 1 Jan 2026 at 10:00, X <x@example.com> wrote:\n> old", "From: a <a@example.com>\nSent: x\nTo: y\nSubject: z",
		"-----Original Message-----\nfoo", "Re: Fwd: RE: aw: sv: tr: Subject", "re:re:re:", "mx; dmarc=pass (comment (nested)) header.from=example.com",
		"mx; spf=pass smtp.mailfrom=\"a@example.com\"; dkim=pass header.i=@example.com", ";;;", "(((", ")))",
	} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		_ = htmlToText(s)
		_ = stripQuoted(s)
		_ = stripInvisible(s)
		_ = authOf(s, "a@example.com")
		_ = authOf(s, s)
		_, _ = parseAddress(s)
		if k := SubjectKey(s); k != SubjectKey(k) {
			t.Fatalf("SubjectKey is not idempotent: %q -> %q -> %q", s, k, SubjectKey(k))
		}
		if r := replySubject(s); replySubject(r) != r {
			t.Fatalf("replySubject is not idempotent: %q", r)
		}
		if c := clean(s, 20); utf8.RuneCountInString(c) > 20 || text.HasForbiddenControl(c) {
			t.Fatalf("clean: %q", c)
		}
		if id := fit(s, "m-"); id == "" || len(id) > 256 || strings.ContainsAny(id, " \t\r\n") {
			t.Fatalf("fit: %q", id)
		}
	})
}
