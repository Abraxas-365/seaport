package dockemail

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Abraxas-365/seaport/internal/dock/adapters/dockemail/emailtest"
)

func account(m *emailtest.Server, password string) Account {
	return Account{Address: "bot@example.com", Username: "bot@example.com", password: password, Folder: "INBOX",
		IMAP: Endpoint{"127.0.0.1", m.IMAPPort, m.IMAPTLS}, SMTP: Endpoint{"127.0.0.1", m.SMTPPort, m.SMTPTLS}}
}

// Check logs in over both protocols in each TLS mode and each SMTP
// mechanism, says where reading starts, and tells a refused password from
// a connection problem.
func TestCheckModes(t *testing.T) {
	for name, o := range map[string]emailtest.Options{
		"implicit tls":      {},
		"starttls":          {IMAPStartTLS: true, SMTPStartTLS: true},
		"auth login (smtp)": {SMTPLogin: true},
	} {
		t.Run(name, func(t *testing.T) {
			m := emailtest.New(t, o)
			m.AddUser("bot@example.com", "pw")
			m.Deliver(emailtest.Raw("a@example.com", "bot@example.com", "x", "x1@example.com", "", "hi"))
			m.Deliver(emailtest.Raw("a@example.com", "bot@example.com", "y", "y1@example.com", "", "hi"))
			cfg := Config{Private: true, TLS: m.ClientTLS()}
			cur, err := cfg.Check(context.Background(), account(m, "pw"))
			if err != nil {
				t.Fatal(err)
			}
			if cur.UID != 2 || cur.Validity == 0 {
				t.Fatalf("cursor = %+v: reading starts at the folder's end", cur)
			}
			_, err = cfg.Check(context.Background(), account(m, "wrong"))
			var le *loginError
			if !errors.As(err, &le) {
				t.Fatalf("wrong password: %v", err)
			}
		})
	}
}

// The server's certificate must be trusted, and private addresses are
// refused unless the operator allowed them.
func TestCheckRefusals(t *testing.T) {
	m := emailtest.New(t, emailtest.Options{})
	m.AddUser("bot@example.com", "pw")
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if _, err := (Config{Private: true}).Check(ctx, account(m, "pw")); err == nil {
		t.Fatal("a certificate no root vouches for was accepted")
	}
	if _, err := (Config{TLS: m.ClientTLS()}).Check(ctx, account(m, "pw")); !errors.Is(err, errPrivate) {
		t.Fatalf("loopback without SEAPORT_EMAIL_PRIVATE: %v", err)
	}
}
