// Package emailtest is a fake mail server for tests: an in-memory IMAP
// server (go-imap's imapmemserver) and a small SMTP server that records
// what it is given, both on loopback behind a throwaway certificate.
// Tests point a dock at it (Endpoints) and trust it (ClientTLS).
package emailtest

import (
	"bufio"
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/mail"
	"net/textproto"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapserver"
	"github.com/emersion/go-imap/v2/imapserver/imapmemserver"
)

// Sent is one message the SMTP server accepted.
type Sent struct {
	From string
	To   []string
	Data string
}

// Server is a fake mailbox host. Account Address/User/Password are
// whatever the test passes to AddUser.
type Server struct {
	t        *testing.T
	cert     tls.Certificate
	pool     *x509.CertPool
	mem      *imapmemserver.Server
	user     *imapmemserver.User
	username string

	// IMAPPort and SMTPPort are where the servers listen (127.0.0.1).
	IMAPPort, SMTPPort int
	// IMAPTLS and SMTPTLS are the modes the dock should name: "tls" is
	// implicit TLS, "starttls" upgrades.
	IMAPTLS, SMTPTLS string

	mu       sync.Mutex
	imapConn []net.Conn
	sent     []Sent
	password string
	kept     []string // what Deliver put in the INBOX
	smtpAuth string   // PLAIN or LOGIN: what the SMTP server offers
	refuse   map[string]bool
}

// RefuseRecipient makes the SMTP server answer 550 to RCPT TO for it.
func (s *Server) RefuseRecipient(address string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.refuse == nil {
		s.refuse = map[string]bool{}
	}
	s.refuse[strings.ToLower(address)] = true
}

// Options select the modes of the servers.
type Options struct {
	IMAPStartTLS, SMTPStartTLS bool
	// SMTPLogin offers AUTH LOGIN only (Outlook style).
	SMTPLogin bool
}

// New starts the servers; they stop with the test.
func New(t *testing.T, o Options) *Server {
	t.Helper()
	s := &Server{t: t, mem: imapmemserver.New(), IMAPTLS: "tls", SMTPTLS: "tls", smtpAuth: "PLAIN"}
	if o.IMAPStartTLS {
		s.IMAPTLS = "starttls"
	}
	if o.SMTPStartTLS {
		s.SMTPTLS = "starttls"
	}
	if o.SMTPLogin {
		s.smtpAuth = "LOGIN"
	}
	s.makeCert()
	s.startIMAP(o.IMAPStartTLS)
	s.startSMTP(o.SMTPStartTLS)
	return s
}

func (s *Server) makeCert() {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		s.t.Fatal(err)
	}
	tpl := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "mail.test"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(24 * time.Hour),
		KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		DNSNames: []string{"localhost"}, IPAddresses: []net.IP{net.ParseIP("127.0.0.1")},
		BasicConstraintsValid: true, IsCA: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tpl, tpl, &key.PublicKey, key)
	if err != nil {
		s.t.Fatal(err)
	}
	leaf, _ := x509.ParseCertificate(der)
	s.cert = tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key, Leaf: leaf}
	s.pool = x509.NewCertPool()
	s.pool.AddCert(leaf)
}

// ClientTLS trusts the servers' certificate.
func (s *Server) ClientTLS() *tls.Config {
	return &tls.Config{RootCAs: s.pool, MinVersion: tls.VersionTLS12}
}

func (s *Server) serverTLS() *tls.Config {
	return &tls.Config{Certificates: []tls.Certificate{s.cert}, MinVersion: tls.VersionTLS12}
}

// AddUser creates the mailbox account (with an INBOX).
func (s *Server) AddUser(username, password string) {
	u := imapmemserver.NewUser(username, password)
	if err := u.Create("INBOX", nil); err != nil {
		s.t.Fatal(err)
	}
	s.mem.AddUser(u)
	s.mu.Lock()
	s.user, s.username, s.password = u, username, password
	s.mu.Unlock()
}

type literal struct{ *bytes.Reader }

func (l literal) Size() int64 { return int64(l.Reader.Len()) }

// Deliver puts a raw message in the INBOX and returns its UID.
func (s *Server) Deliver(raw string) uint32 {
	s.t.Helper()
	s.mu.Lock()
	s.kept = append(s.kept, raw)
	s.mu.Unlock()
	return s.deliver(raw)
}

func (s *Server) deliver(raw string) uint32 {
	s.t.Helper()
	d, err := s.user.Append("INBOX", literal{bytes.NewReader([]byte(raw))}, &imap.AppendOptions{Time: time.Now()})
	if err != nil {
		s.t.Fatal(err)
	}
	return uint32(d.UID)
}

// Sent lists what SMTP accepted so far.
func (s *Server) Sent() []Sent {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Sent(nil), s.sent...)
}

// KillIMAP closes every IMAP connection (the client has to reconnect).
func (s *Server) KillIMAP() {
	s.mu.Lock()
	conns := s.imapConn
	s.imapConn = nil
	s.mu.Unlock()
	for _, c := range conns {
		_ = c.Close()
	}
}

// SetPassword changes the password both servers accept. The IMAP memory
// server can't change one, so the account is made again with the same
// messages (same UIDs and UIDVALIDITY, as when a real password changes).
func (s *Server) SetPassword(p string) {
	s.mu.Lock()
	name, kept := s.username, s.kept
	s.mu.Unlock()
	s.AddUser(name, p)
	for _, raw := range kept {
		s.deliver(raw)
	}
}

type trackListener struct {
	net.Listener
	s *Server
}

func (l trackListener) Accept() (net.Conn, error) {
	c, err := l.Listener.Accept()
	if err != nil {
		return nil, err
	}
	l.s.mu.Lock()
	l.s.imapConn = append(l.s.imapConn, c)
	l.s.mu.Unlock()
	return c, nil
}

func (s *Server) startIMAP(startTLS bool) {
	opts := &imapserver.Options{
		NewSession: func(*imapserver.Conn) (imapserver.Session, *imapserver.GreetingData, error) {
			return s.mem.NewSession(), nil, nil
		},
		Caps:   imap.CapSet{imap.CapIMAP4rev1: {}, imap.CapIMAP4rev2: {}},
		Logger: discard{},
	}
	var ln net.Listener
	raw, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		s.t.Fatal(err)
	}
	ln = trackListener{raw, s}
	if startTLS {
		opts.TLSConfig = s.serverTLS()
	} else {
		ln = tls.NewListener(ln, s.serverTLS())
	}
	srv := imapserver.New(opts)
	s.IMAPPort = raw.Addr().(*net.TCPAddr).Port
	go func() { _ = srv.Serve(ln) }()
	s.t.Cleanup(func() { _ = srv.Close() })
}

type discard struct{}

func (discard) Printf(string, ...any) {}

func (s *Server) startSMTP(startTLS bool) {
	raw, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		s.t.Fatal(err)
	}
	s.SMTPPort = raw.Addr().(*net.TCPAddr).Port
	var l net.Listener = raw
	if !startTLS {
		l = tls.NewListener(raw, s.serverTLS())
	}
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			go s.smtpSession(c, startTLS)
		}
	}()
	s.t.Cleanup(func() { _ = raw.Close() })
}

func (s *Server) smtpSession(c net.Conn, startTLS bool) {
	defer func() { _ = c.Close() }()
	_ = c.SetDeadline(time.Now().Add(30 * time.Second))
	var rw io.ReadWriter = c
	br := bufio.NewReader(rw)
	say := func(line string) { _, _ = io.WriteString(rw, line+"\r\n") }
	say("220 mail.test ESMTP")
	authed := false
	var from string
	var to []string
	for {
		line, err := br.ReadString('\n')
		if err != nil {
			return
		}
		line = strings.TrimRight(line, "\r\n")
		verb := strings.ToUpper(line)
		switch {
		case strings.HasPrefix(verb, "EHLO"), strings.HasPrefix(verb, "HELO"):
			say("250-mail.test")
			if startTLS {
				if _, ok := rw.(*tls.Conn); !ok {
					say("250-STARTTLS")
				}
			}
			say("250 AUTH " + s.smtpAuth)
		case verb == "STARTTLS":
			say("220 go ahead")
			t := tls.Server(c, s.serverTLS())
			if err := t.Handshake(); err != nil {
				return
			}
			rw = t
			br = bufio.NewReader(t)
		case strings.HasPrefix(verb, "AUTH PLAIN"):
			f := strings.Fields(line)
			raw, _ := base64.StdEncoding.DecodeString(f[len(f)-1])
			parts := strings.Split(string(raw), "\x00")
			if len(parts) == 3 && s.checkLogin(parts[1], parts[2]) {
				authed = true
				say("235 2.7.0 ok")
			} else {
				say("535 5.7.8 authentication failed")
			}
		case verb == "AUTH LOGIN":
			say("334 " + base64.StdEncoding.EncodeToString([]byte("Username:")))
			u, _ := br.ReadString('\n')
			say("334 " + base64.StdEncoding.EncodeToString([]byte("Password:")))
			p, _ := br.ReadString('\n')
			ub, _ := base64.StdEncoding.DecodeString(strings.TrimSpace(u))
			pb, _ := base64.StdEncoding.DecodeString(strings.TrimSpace(p))
			if s.checkLogin(string(ub), string(pb)) {
				authed = true
				say("235 2.7.0 ok")
			} else {
				say("535 5.7.8 authentication failed")
			}
		case strings.HasPrefix(verb, "MAIL FROM:"):
			if !authed {
				say("530 5.7.0 authentication required")
				continue
			}
			from = strings.Trim(strings.TrimSpace(line[len("MAIL FROM:"):]), "<>")
			say("250 ok")
		case strings.HasPrefix(verb, "RCPT TO:"):
			rcpt := strings.Trim(strings.TrimSpace(line[len("RCPT TO:"):]), "<>")
			s.mu.Lock()
			no := s.refuse[strings.ToLower(rcpt)]
			s.mu.Unlock()
			if no {
				say("550 5.1.1 no such user")
				continue
			}
			to = append(to, rcpt)
			say("250 ok")
		case verb == "DATA":
			say("354 end with <CRLF>.<CRLF>")
			data, err := textproto.NewReader(br).ReadDotBytes()
			if err != nil {
				return
			}
			s.mu.Lock()
			s.sent = append(s.sent, Sent{From: from, To: to, Data: string(data)})
			s.mu.Unlock()
			from, to = "", nil
			say("250 queued")
		case verb == "RSET", verb == "NOOP":
			say("250 ok")
		case verb == "QUIT":
			say("221 bye")
			return
		default:
			say("502 not implemented " + strconv.Itoa(len(line)))
		}
	}
}

func (s *Server) checkLogin(user, pass string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.username != "" && user == s.username && pass == s.password
}

// Raw builds a simple plain-text email that the mailbox's server
// authenticated (an Authentication-Results header with DKIM and SPF
// passing for the sender's domain, as a real provider adds).
func Raw(from, to, subject, id, inReplyTo, body string) string {
	domain := from
	if a, err := mail.ParseAddress(from); err == nil {
		domain = a.Address
	}
	if i := strings.LastIndex(domain, "@"); i >= 0 {
		domain = domain[i+1:]
	}
	return WithHeaders(Raw0(from, to, subject, id, inReplyTo, body),
		"Authentication-Results: mx.example.com; dkim=pass header.d="+domain+"; spf=pass smtp.mailfrom="+domain)
}

// WithHeaders puts header lines first in a raw email (like a receiving
// server does).
func WithHeaders(raw string, lines ...string) string {
	return strings.Join(lines, "\r\n") + "\r\n" + raw
}

// Raw0 is Raw without any Authentication-Results header.
func Raw0(from, to, subject, id, inReplyTo, body string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "From: %s\r\nTo: %s\r\nSubject: %s\r\nDate: %s\r\n", from, to, subject, time.Now().Format(time.RFC1123Z))
	if id != "" {
		fmt.Fprintf(&b, "Message-ID: <%s>\r\n", id)
	}
	if inReplyTo != "" {
		fmt.Fprintf(&b, "In-Reply-To: <%s>\r\nReferences: <%s>\r\n", inReplyTo, inReplyTo)
	}
	b.WriteString("MIME-Version: 1.0\r\nContent-Type: text/plain; charset=utf-8\r\n\r\n")
	b.WriteString(strings.ReplaceAll(body, "\n", "\r\n"))
	b.WriteString("\r\n")
	return b.String()
}
