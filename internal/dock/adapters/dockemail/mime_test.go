package dockemail

import (
	"net/netip"
	"strings"
	"testing"

	"github.com/Abraxas-365/seaport/internal/dock"
	"github.com/Abraxas-365/seaport/internal/ship"
)

func TestParsePlain(t *testing.T) {
	raw := "From: Ana Pérez <Ana@Example.com>\r\nTo: Bot <bot@example.com>, Luis <luis@example.com>\r\nCc: eva@example.com\r\n" +
		"Subject: =?UTF-8?Q?Pedido_n=C3=BAmero_5?=\r\nMessage-ID: <abc@mail.example.com>\r\nIn-Reply-To: <prev@example.com>\r\n" +
		"References: <first@example.com> <prev@example.com>\r\nDate: Mon, 02 Jan 2026 15:04:05 +0000\r\n" +
		"Content-Type: text/plain; charset=utf-8\r\n\r\nHola, ¿puedes ayudar?\r\n\r\nOn Mon, 1 Jan 2026 at 10:00, Bot <bot@example.com> wrote:\r\n> earlier text\r\n"
	m, err := ParseMessage([]byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	if m.ID != "abc@mail.example.com" || m.InReplyTo != "prev@example.com" || len(m.References) != 2 || m.References[0] != "first@example.com" {
		t.Fatalf("ids: %+v", m)
	}
	if m.From.Address != "ana@example.com" || m.From.Name != "Ana Pérez" {
		t.Fatalf("from: %+v", m.From)
	}
	if m.Subject != "Pedido número 5" {
		t.Fatalf("subject %q", m.Subject)
	}
	if got := strings.Join(m.Others, ","); got != "bot@example.com,luis@example.com,eva@example.com" {
		t.Fatalf("others %q", got)
	}
	if m.Text != "Hola, ¿puedes ayudar?" {
		t.Fatalf("quoted history must go: %q", m.Text)
	}
	if m.Automatic || m.Date.IsZero() {
		t.Fatalf("automatic/date: %+v", m)
	}
}

func TestParseMultipartWithImageAndAttachment(t *testing.T) {
	png := "\x89PNG\r\n\x1a\n" + strings.Repeat("x", 20)
	raw := "From: ana@example.com\r\nSubject: Fotos\r\nMessage-ID: <m1@example.com>\r\nMIME-Version: 1.0\r\nContent-Type: multipart/mixed; boundary=BB\r\n\r\n" +
		"--BB\r\nContent-Type: multipart/alternative; boundary=AA\r\n\r\n" +
		"--AA\r\nContent-Type: text/plain; charset=utf-8\r\n\r\nplain body\r\n" +
		"--AA\r\nContent-Type: text/html; charset=utf-8\r\n\r\n<p>html body</p>\r\n--AA--\r\n" +
		"--BB\r\nContent-Type: image/png; name=\"a.png\"\r\nContent-Disposition: attachment; filename=\"a.png\"\r\nContent-Transfer-Encoding: binary\r\n\r\n" + png + "\r\n" +
		"--BB\r\nContent-Type: application/pdf\r\nContent-Disposition: attachment; filename=\"doc.pdf\"\r\n\r\n%PDF-1.4 data\r\n--BB--\r\n"
	m, err := ParseMessage([]byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	if m.Text != "plain body" {
		t.Fatalf("plain part wins: %q", m.Text)
	}
	if len(m.Media) != 2 || m.Media[0].Name != "a.png" || len(m.Media[0].Data) == 0 || m.Media[0].Kind != ship.AttachmentImage ||
		m.Media[1].Name != "doc.pdf" || m.Media[1].Kind != ship.AttachmentDocument || string(m.Media[1].Data) != "%PDF-1.4 data" {
		t.Fatalf("media: %+v", m.Media)
	}
	if len(m.Notes) != 0 {
		t.Fatalf("notes: %v", m.Notes)
	}
}

func TestParseHTMLOnlyAndCharset(t *testing.T) {
	raw := "From: ana@example.com\r\nSubject: x\r\nContent-Type: text/html; charset=iso-8859-1\r\nContent-Transfer-Encoding: quoted-printable\r\n\r\n" +
		"<html><head><style>p{}</style></head><body><p>Caf=E9 &amp; t=E9</p><div>second</div></body></html>"
	m, err := ParseMessage([]byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	if m.Text != "Café & té\nsecond" {
		t.Fatalf("html text %q", m.Text)
	}
}

func TestAutomaticMail(t *testing.T) {
	for _, h := range []string{"Auto-Submitted: auto-replied", "Precedence: bulk", "X-Autoreply: yes"} {
		m, err := ParseMessage([]byte("From: a@example.com\r\n" + h + "\r\n\r\nout of office\r\n"))
		if err != nil || !m.Automatic {
			t.Errorf("%s: automatic=%v err=%v", h, m.Automatic, err)
		}
	}
	m, _ := ParseMessage([]byte("From: a@example.com\r\nAuto-Submitted: no\r\n\r\nhi\r\n"))
	if m.Automatic {
		t.Error("Auto-Submitted: no is a person")
	}
}

func TestStripQuoted(t *testing.T) {
	for in, want := range map[string]string{
		"thanks\n\n> old\n> older": "thanks",
		"ok\n\nEl lun, 1 ene 2026 a las 10:00, Ana <a@example.com> escribió:\n> hi": "ok",
		"fine\n\n-----Original Message-----\nFrom: x\nSent: y":                      "fine",
		"yes\n\nFrom: Ana <a@example.com>\nSent: Monday\nTo: b\nSubject: s\n\nold":  "yes",
		"> only a quote":                         "> only a quote",
		"From: the thing I said is not a header": "From: the thing I said is not a header",
	} {
		if got := stripQuoted(in); got != want {
			t.Errorf("stripQuoted(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestSubjects(t *testing.T) {
	if SubjectKey("RE: Fwd: re: Hello ") != "hello" {
		t.Fatal(SubjectKey("RE: Fwd: re: Hello "))
	}
	if replySubject("Hello") != "Re: Hello" || replySubject("Re: Hello") != "Re: Hello" || replySubject("") != "Re: (no subject)" {
		t.Fatal("reply subject")
	}
}

func TestChatIDFits(t *testing.T) {
	if chatID("abc@example.com") != "abc@example.com" {
		t.Fatal("short ids stay")
	}
	long := chatID(strings.Repeat("a", 400) + "@x")
	if len(long) > 100 || !strings.HasPrefix(long, "m-") {
		t.Fatalf("long ids hash: %q", long)
	}
}

func TestBuildReply(t *testing.T) {
	a := Account{Address: "bot@example.com", DisplayName: "Bot"}
	th := Thread{Chat: "c1", Subject: "Pedido", Participants: []string{"ana@example.com", "luis@example.com"}, LastMessage: "last@example.com", Refs: []string{"first@example.com", "last@example.com"}}
	raw, err := build(a, th, dock.Outgoing{MessageID: "seaport-1@example.com", Text: "Hola\nmundo ñ"})
	if err != nil {
		t.Fatal(err)
	}
	s := string(raw)
	for _, want := range []string{"Subject: Re: Pedido", "In-Reply-To: <last@example.com>", "Message-Id: <seaport-1@example.com>", "References: <first@example.com> <last@example.com>", "ana@example.com", "luis@example.com", "Auto-Submitted: auto-generated"} {
		if !strings.Contains(s, want) {
			t.Errorf("missing %q in:\n%s", want, s)
		}
	}
	m, err := ParseMessage(raw)
	if err != nil || m.Text != "Hola\nmundo ñ" {
		t.Fatalf("round trip: %q %v", m.Text, err)
	}
	withFile, err := build(a, th, dock.Outgoing{MessageID: "seaport-2@example.com", Text: "see file", Files: []dock.File{{Name: "a.png", MIME: "image/png", Data: []byte("\x89PNG\r\n\x1a\nxx")}, {Name: "n.txt", MIME: "application/octet-stream", Data: []byte("hello")}}})
	if err != nil {
		t.Fatal(err)
	}
	m, err = ParseMessage(withFile)
	if err != nil || m.Text != "see file" || len(m.Media) != 2 || len(m.Notes) != 0 ||
		m.Media[0].Kind != ship.AttachmentImage || m.Media[1].Kind != ship.AttachmentDocument || m.Media[1].Name != "n.txt" ||
		string(m.Media[1].Data) != "hello" {
		t.Fatalf("attachments round trip: %+v %v", m, err)
	}
}

func TestNewMessageIDDomain(t *testing.T) {
	id := newMessageID("bot@mail.example.com")
	if !strings.HasSuffix(id, "@mail.example.com") || !strings.HasPrefix(id, "seaport-") {
		t.Fatal(id)
	}
}

func TestPublicAddresses(t *testing.T) {
	for ip, want := range map[string]bool{"8.8.8.8": true, "127.0.0.1": false, "10.0.0.1": false, "192.168.1.1": false, "169.254.1.1": false, "100.64.0.1": false, "::1": false, "fe80::1": false, "2001:4860:4860::8888": true} {
		a, _ := netipParse(ip)
		if public(a) != want {
			t.Errorf("public(%s) = %v", ip, !want)
		}
	}
}

func netipParse(s string) (netip.Addr, error) { return netip.ParseAddr(s) }
