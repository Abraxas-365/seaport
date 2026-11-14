package dockemail

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"net/smtp"
	"net/textproto"
	"strings"
	"time"
)

// smtpConn opens an authenticated SMTP session (implicit TLS or STARTTLS,
// never plain), runs use, and quits. The credentials only cross a
// connection that is encrypted and whose certificate was checked.
func (c Config) smtpDo(ctx context.Context, a Account, use func(*smtp.Client) error) error {
	conn, err := c.dialConn(ctx, a.SMTP)
	if err != nil {
		return err
	}
	if dl, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(dl)
	} else {
		_ = conn.SetDeadline(time.Now().Add(ioTimeout))
	}
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stop()
	cfg := c.tls(a.SMTP.Host)
	var wire net.Conn = conn
	if a.SMTP.TLS == TLS {
		t := tls.Client(conn, cfg)
		if err := t.HandshakeContext(ctx); err != nil {
			_ = conn.Close()
			return err
		}
		wire = t
	}
	cl, err := smtp.NewClient(wire, a.SMTP.Host)
	if err != nil {
		_ = wire.Close()
		return err
	}
	defer cl.Close()
	if err := cl.Hello("seaport"); err != nil {
		return err
	}
	if a.SMTP.TLS == StartTLS {
		if ok, _ := cl.Extension("STARTTLS"); !ok {
			return errors.New("the SMTP server does not offer STARTTLS")
		}
		if err := cl.StartTLS(cfg); err != nil {
			return err
		}
	}
	auth, err := chooseAuth(cl, a)
	if err != nil {
		return err
	}
	if err := cl.Auth(auth); err != nil {
		return err
	}
	if err := use(cl); err != nil {
		return err
	}
	_ = cl.Quit()
	return nil
}

// chooseAuth picks PLAIN, else LOGIN (Outlook offers little else).
func chooseAuth(cl *smtp.Client, a Account) (smtp.Auth, error) {
	_, mechs := cl.Extension("AUTH")
	fields := strings.Fields(strings.ToUpper(mechs))
	has := func(m string) bool {
		for _, f := range fields {
			if f == m {
				return true
			}
		}
		return false
	}
	switch {
	case has("PLAIN"):
		return smtp.PlainAuth("", a.Username, a.password, a.SMTP.Host), nil
	case has("LOGIN"):
		return &loginAuth{user: a.Username, pass: a.password}, nil
	}
	return nil, errors.New("the SMTP server offers no password sign-in (PLAIN or LOGIN)")
}

// loginAuth is the AUTH LOGIN mechanism.
type loginAuth struct{ user, pass string }

func (l *loginAuth) Start(*smtp.ServerInfo) (string, []byte, error) { return "LOGIN", nil, nil }

func (l *loginAuth) Next(from []byte, more bool) ([]byte, error) {
	if !more {
		return nil, nil
	}
	switch strings.ToLower(strings.TrimSpace(string(from))) {
	case "username:":
		return []byte(l.user), nil
	case "password:":
		return []byte(l.pass), nil
	}
	return nil, fmt.Errorf("unexpected SMTP challenge %q", from)
}

// smtpRefusedLogin: the server said no to the credentials.
func smtpRefusedLogin(err error) bool {
	var te *textproto.Error
	if errors.As(err, &te) {
		switch te.Code {
		case 530, 534, 535, 538:
			return true
		}
	}
	return false
}

// deliver sends one message: MAIL FROM, RCPT TO each, DATA.
func (c Config) deliver(ctx context.Context, a Account, to []string, raw []byte) error {
	return c.smtpDo(ctx, a, func(cl *smtp.Client) error {
		if err := cl.Mail(a.Address); err != nil {
			return err
		}
		for _, r := range to {
			if err := cl.Rcpt(r); err != nil {
				return err
			}
		}
		w, err := cl.Data()
		if err != nil {
			return err
		}
		if _, err := w.Write(raw); err != nil {
			_ = w.Close()
			return err
		}
		return w.Close()
	})
}
