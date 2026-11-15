package dockemail

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"html"
	"io"
	"net/mail"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/Abraxas-365/seaport/internal/conversation"
	"github.com/Abraxas-365/seaport/internal/ship"
	"github.com/Abraxas-365/seaport/internal/text"
	"github.com/emersion/go-message"
	gomail "github.com/emersion/go-message/mail"

	// Charsets of mail in the wild (ISO-8859-x, Windows-125x, GBK…).
	_ "github.com/emersion/go-message/charset"
)

// Message is an email as the ship domain needs it.
type Message struct {
	ID         string // Message-ID without brackets ("" when missing)
	InReplyTo  string
	References []string // oldest first
	From       gomail.Address
	// ReplyTo is where an answer goes when the sender named another place
	// ("" : to From).
	ReplyTo string
	// To are the addresses it was sent to; Others those and the Cc ones
	// (at most MaxParticipants).
	To      []string
	Others  []string
	Subject string
	Date    time.Time
	// Text is the body: the plain part (or the HTML one as text) without
	// the quoted earlier messages.
	Text string
	// Media are the files that came with it (images and documents); Notes
	// name what was left out.
	Media []ship.Media
	Notes []string
	// Automatic marks mail no person wrote (auto-replies, bounces, bulk):
	// answering it could start a loop.
	Automatic bool
	// Auth is what the mailbox's own server says about From.
	Auth Auth
}

// Auth is the verdict of the Authentication-Results header the receiving
// server (the mailbox provider's) put on top of the message: whether the
// sender's domain vouched for From through SPF, DKIM or DMARC, aligned
// with the From domain. Anything below it was written by the sender or
// by relays and is not read.
type Auth int

const (
	AuthNone Auth = iota // no Authentication-Results header
	AuthFail             // present, but nothing passed for From's domain
	AuthPass
)

// Limits of what one email brings into a ship.
const (
	// MaxRaw is the biggest email read whole; a bigger one is announced
	// by its headers only.
	MaxRaw     = 30 << 20
	maxSubject = 200
)

// ParseMessage reads a raw email (RFC 5322 + MIME). It fails only when
// not even the headers are readable.
func ParseMessage(raw []byte) (Message, error) {
	mr, err := gomail.CreateReader(bytes.NewReader(raw))
	if err != nil && !message.IsUnknownCharset(err) {
		return Message{}, err
	}
	defer mr.Close()
	h := mr.Header
	var m Message
	m.ID, _ = h.MessageID()
	m.ID = strings.TrimSpace(m.ID)
	if ids, _ := h.MsgIDList("In-Reply-To"); len(ids) > 0 {
		m.InReplyTo = ids[len(ids)-1]
	}
	m.References, _ = h.MsgIDList("References")
	if from, _ := h.AddressList("From"); len(from) > 0 && from[0] != nil {
		m.From = *from[0]
	}
	m.From.Address = strings.ToLower(strings.TrimSpace(m.From.Address))
	if rt, _ := h.AddressList("Reply-To"); len(rt) > 0 && rt[0] != nil && validAddress(rt[0].Address) {
		m.ReplyTo = strings.ToLower(rt[0].Address)
	}
	for _, key := range []string{"To", "Cc"} {
		list, _ := h.AddressList(key)
		for _, a := range list {
			if a != nil && validAddress(a.Address) && len(m.Others) < MaxParticipants {
				addr := strings.ToLower(a.Address)
				m.Others = append(m.Others, addr)
				if key == "To" {
					m.To = append(m.To, addr)
				}
			}
		}
	}
	m.Auth = authOf(h.Get("Authentication-Results"), m.From.Address)
	m.Subject, _ = h.Subject()
	m.Subject = clean(m.Subject, maxSubject)
	m.Date, _ = h.Date()
	m.Automatic = automatic(h)

	var plains, htmls []string
	for {
		p, err := mr.NextPart()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil && !message.IsUnknownCharset(err) {
			m.Notes = append(m.Notes, "part of the message could not be read")
			break
		}
		switch ph := p.Header.(type) {
		case *gomail.InlineHeader:
			kind, _, _ := ph.ContentType()
			kind = strings.ToLower(kind)
			switch {
			case kind == "text/html":
				htmls = append(htmls, readText(p.Body))
			case strings.HasPrefix(kind, "text/"):
				plains = append(plains, readText(p.Body))
			default:
				// Inline parts that aren't images (signatures, calendar
				// data) are the mail's machinery, not files people sent.
				m.attach(kind, "", p.Body, isImage(kind))
			}
		case *gomail.AttachmentHeader:
			kind, _, _ := ph.ContentType()
			name, _ := ph.Filename()
			m.attach(strings.ToLower(kind), name, p.Body, true)
		}
	}
	body := strings.TrimSpace(strings.Join(plains, "\n\n"))
	if body == "" {
		body = strings.TrimSpace(htmlToText(strings.Join(htmls, "\n")))
	}
	m.Text = stripQuoted(body)
	return m, nil
}

// attach keeps a file whole when it fits within MaxFile (the service
// tells images from documents by the bytes); else, or unless keep, it is
// only named.
func (m *Message) attach(kind, name string, r io.Reader, keep bool) {
	name = clean(name, ship.MaxAttachmentName)
	label := name
	if label == "" {
		label = "unnamed"
	}
	what, media := "attachment", ship.AttachmentDocument
	if isImage(kind) {
		what, media = "image", ship.AttachmentImage
	}
	if !keep || len(m.Media) == ship.MaxAttachments {
		why := "too many files"
		if !keep {
			why = "not a file"
		}
		n, _ := io.Copy(io.Discard, io.LimitReader(r, MaxRaw))
		m.Notes = append(m.Notes, fmt.Sprintf("%s %s (%s, %d KiB) left out: %s", what, label, kind, (n+1023)/1024, why))
		return
	}
	limit := ship.MaxFile
	data, err := io.ReadAll(io.LimitReader(r, int64(limit)+1))
	switch {
	case err == nil && len(data) > 0 && len(data) <= limit:
		m.Media = append(m.Media, ship.Media{Kind: media, Name: ship.CleanName(name), Data: data})
	case err == nil && len(data) > limit:
		_, _ = io.Copy(io.Discard, io.LimitReader(r, MaxRaw))
		m.Notes = append(m.Notes, fmt.Sprintf("%s %s left out: too large", what, label))
	default:
		m.Notes = append(m.Notes, fmt.Sprintf("%s %s left out: unreadable", what, label))
	}
}

func isImage(kind string) bool {
	for _, t := range ship.ImageTypes {
		if kind == t {
			return true
		}
	}
	return false
}

func automatic(h gomail.Header) bool {
	if v := strings.ToLower(strings.TrimSpace(h.Get("Auto-Submitted"))); v != "" && v != "no" {
		return true
	}
	switch strings.ToLower(strings.TrimSpace(h.Get("Precedence"))) {
	case "bulk", "junk", "auto_reply", "list":
		return true
	}
	return h.Get("X-Autoreply") != "" || h.Get("X-Autorespond") != ""
}

// maxPart bounds the text read from one part: the ship's own limit with
// room for the quoted history that is cut afterwards.
const maxPart = 4 * text.MaxMessage

func readText(r io.Reader) string {
	b, _ := io.ReadAll(io.LimitReader(r, maxPart))
	if !utf8.Valid(b) {
		b = []byte(strings.ToValidUTF8(string(b), "\uFFFD"))
	}
	return string(b)
}

var (
	reComment = regexp.MustCompile(`(?s)<!--.*?-->`)
	reOpenTag = regexp.MustCompile(`(?is)<([a-z][a-z0-9]*)\b[^>]*\bstyle\s*=\s*("[^"]*"|'[^']*')[^>]*>`)
	reInvis   = regexp.MustCompile(`(?i)(display\s*:\s*none|visibility\s*:\s*hidden|mso-hide\s*:\s*all|font-size\s*:\s*0(px|pt|em)?\s*(;|$|"|'))`)
	reHidden  = regexp.MustCompile(`(?is)<(script|style|head)\b[^>]*>.*?</(script|style|head)\s*>`)
	reBlock   = regexp.MustCompile(`(?i)<\s*(br|/p|/div|/li|/tr|/h[1-6]|/blockquote|/table)\b[^>]*>`)
	reTag     = regexp.MustCompile(`(?s)<[^>]*>`)
	reBlanks  = regexp.MustCompile(`\n{3,}`)
	reSpaces  = regexp.MustCompile(`[ \t\x{00a0}]+`)
)

// htmlToText is a small HTML → plain text for mail that has no plain part:
// block ends become line breaks, tags go, entities are decoded.
func htmlToText(s string) string {
	s = reComment.ReplaceAllString(s, "")
	s = reHidden.ReplaceAllString(s, "")
	s = stripInvisible(s)
	s = reBlock.ReplaceAllString(s, "\n")
	s = reTag.ReplaceAllString(s, "")
	s = html.UnescapeString(s)
	s = strings.ReplaceAll(s, "\r", "")
	lines := strings.Split(s, "\n")
	for i, l := range lines {
		lines[i] = strings.TrimSpace(reSpaces.ReplaceAllString(l, " "))
	}
	return strings.TrimSpace(reBlanks.ReplaceAllString(strings.Join(lines, "\n"), "\n\n"))
}

var (
	reWrote   = regexp.MustCompile(`(?i)^(on .{5,200} wrote:|el .{5,200} escribi[óo]:|le .{5,200} a [ée]crit\s?:|am .{5,200} schrieb .{0,100}:)\s*$`)
	reOrig    = regexp.MustCompile(`(?i)^(-{2,}\s*(original message|mensaje original|forwarded message|mensaje reenviado)\s*-{2,}|_{20,}|\*{0,2}(from|de):\*{0,2}\s.+<.+@.+>.*)$`)
	reOutlook = regexp.MustCompile(`(?i)^(from|de):\s.+`)
)

// stripQuoted drops the quoted earlier messages of a reply: lines quoted
// with ">", and everything after an attribution line ("On … wrote:") or a
// forwarding/original-message separator. A message made only of quotes is
// kept as it is (nothing else to say).
func stripQuoted(s string) string {
	s = strings.ReplaceAll(strings.ReplaceAll(s, "\r\n", "\n"), "\r", "\n")
	lines := strings.Split(s, "\n")
	var out []string
	for i, l := range lines {
		t := strings.TrimSpace(l)
		if reWrote.MatchString(t) && i+1 <= len(lines) {
			break
		}
		if reOrig.MatchString(t) || (reOutlook.MatchString(t) && nextIsHeader(lines[i+1:])) {
			break
		}
		if strings.HasPrefix(t, ">") {
			continue
		}
		out = append(out, l)
	}
	kept := strings.TrimSpace(strings.Join(out, "\n"))
	if kept == "" {
		return strings.TrimSpace(s)
	}
	return kept
}

// nextIsHeader: an Outlook-style quoted header block ("From: …" followed by
// "Sent:/To:/Subject:") rather than a line that starts with "From:".
func nextIsHeader(rest []string) bool {
	for _, l := range rest {
		t := strings.ToLower(strings.TrimSpace(l))
		if t == "" {
			continue
		}
		for _, p := range []string{"sent:", "date:", "to:", "subject:", "enviado:", "para:", "asunto:", "fecha:"} {
			if strings.HasPrefix(t, p) {
				return true
			}
		}
		return false
	}
	return false
}

func clean(s string, max int) string {
	s = strings.TrimSpace(text.StripControl(s))
	s = strings.Join(strings.Fields(s), " ")
	if utf8.RuneCountInString(s) > max {
		r := []rune(s)
		s = string(r[:max])
	}
	return s
}

func validAddress(a string) bool {
	if a == "" || len(a) > 254 || strings.ContainsAny(a, " \t\r\n<>,;\"") {
		return false
	}
	at := strings.LastIndex(a, "@")
	return at > 0 && at < len(a)-1
}

// chatID names a thread by its first message's Message-ID, which fits a
// conversation's external id (hashed when it doesn't).
func chatID(first string) string { return fit(first, "m-") }

func fit(id, prefix string) string {
	if id != "" && len(id) <= conversation.MaxExternalID-8 && !text.HasForbiddenControl(id) && utf8.ValidString(id) && !strings.ContainsAny(id, " \t\r\n") {
		return id
	}
	sum := sha256.Sum256([]byte(id))
	return prefix + hex.EncodeToString(sum[:16])
}

// SubjectKey is a subject without reply/forward prefixes, lowercased: what
// replies of one thread share.
func SubjectKey(subject string) string {
	s := strings.ToLower(strings.TrimSpace(subject))
	for {
		before := s
		for _, p := range []string{"re:", "fwd:", "fw:", "rv:", "aw:", "sv:", "tr:"} {
			s = strings.TrimSpace(strings.TrimPrefix(s, p))
		}
		if s == before {
			return s
		}
	}
}

// replySubject is the subject of an answer: "Re: …" once.
func replySubject(subject string) string {
	subject = strings.TrimSpace(subject)
	if subject == "" {
		return "Re: (no subject)"
	}
	if strings.HasPrefix(strings.ToLower(subject), "re:") {
		return subject
	}
	return "Re: " + subject
}

// parseAddress reads one "Name <a@b>" (or "a@b") address.
func parseAddress(s string) (gomail.Address, error) {
	a, err := mail.ParseAddress(strings.TrimSpace(s))
	if err != nil {
		return gomail.Address{}, err
	}
	if !validAddress(a.Address) {
		return gomail.Address{}, errors.New("not an email address")
	}
	return *a, nil
}

// stripInvisible drops elements styled to be invisible (display:none and
// the like): text a reader never sees but an agent would read.
func stripInvisible(s string) string {
	for guard := 0; guard < 200; guard++ {
		var cut []int
		for _, loc := range reOpenTag.FindAllStringSubmatchIndex(s, -1) {
			if reInvis.MatchString(s[loc[4]:loc[5]]) {
				cut = loc
				break
			}
		}
		if cut == nil {
			return s
		}
		name := strings.ToLower(s[cut[2]:cut[3]])
		end := cut[1]
		if !voidTag[name] {
			end = closeOf(s, name, cut[1])
		}
		s = s[:cut[0]] + s[end:]
	}
	return s
}

var voidTag = map[string]bool{"br": true, "img": true, "hr": true, "input": true, "meta": true, "link": true}

// closeOf returns where the element opened before from ends (matching
// nested tags of the same name); the end of s when it never closes.
func closeOf(s, name string, from int) int {
	re := regexp.MustCompile(`(?is)<(/?)` + regexp.QuoteMeta(name) + `\b[^>]*>`)
	depth := 1
	for _, loc := range re.FindAllStringSubmatchIndex(s[from:], -1) {
		if loc[2] == loc[3] { // opening tag
			if !strings.HasSuffix(s[from+loc[0]:from+loc[1]], "/>") {
				depth++
			}
			continue
		}
		if depth--; depth == 0 {
			return from + loc[1]
		}
	}
	return len(s)
}

// authOf reads the topmost Authentication-Results value (the receiving
// server's): AuthPass when DMARC passed, or DKIM or SPF passed for a
// domain aligned with the sender's.
func authOf(value, from string) Auth {
	if strings.TrimSpace(value) == "" {
		return AuthNone
	}
	fromDomain := domainOf(from)
	parts := strings.Split(stripComments(value), ";")
	for _, part := range parts[1:] { // the first is the server's own id
		fields := strings.Fields(part)
		if len(fields) == 0 {
			continue
		}
		method, result, ok := strings.Cut(strings.ToLower(fields[0]), "=")
		if !ok || result != "pass" {
			continue
		}
		props := map[string]string{}
		for _, f := range fields[1:] {
			if k, v, ok := strings.Cut(strings.ToLower(f), "="); ok {
				props[k] = strings.Trim(v, `"`)
			}
		}
		switch method {
		case "dmarc":
			return AuthPass
		case "dkim":
			d := props["header.d"]
			if d == "" {
				d = domainOf(props["header.i"])
			}
			if aligned(d, fromDomain) {
				return AuthPass
			}
		case "spf":
			if aligned(domainOf(props["smtp.mailfrom"]), fromDomain) {
				return AuthPass
			}
		}
	}
	return AuthFail
}

func stripComments(s string) string {
	var b strings.Builder
	depth := 0
	for _, r := range s {
		switch {
		case r == '(':
			depth++
		case r == ')' && depth > 0:
			depth--
		case depth == 0:
			b.WriteRune(r)
		}
	}
	return b.String()
}

func domainOf(a string) string {
	if i := strings.LastIndex(a, "@"); i >= 0 {
		a = a[i+1:]
	}
	return strings.ToLower(strings.TrimSpace(a))
}

// aligned: the two names are one domain or one is a subdomain of the
// other (relaxed alignment; no public suffix list, so a bare suffix such
// as "com" never counts).
func aligned(a, b string) bool {
	if a == "" || b == "" || !strings.Contains(a, ".") || !strings.Contains(b, ".") {
		return false
	}
	return a == b || strings.HasSuffix(a, "."+b) || strings.HasSuffix(b, "."+a)
}
