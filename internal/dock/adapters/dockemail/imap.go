package dockemail

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"net/smtp"
	"sort"
	"time"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"
)

// loginError: the server refused the account's username or password.
type loginError struct{ err error }

func (e *loginError) Error() string { return "the mailbox refused the login: " + e.err.Error() }
func (e *loginError) Unwrap() error { return e.err }

// opWatchdog closes a client whose command hangs (a connection that died
// silently): imapclient waits for answers as long as the socket lives.
const opWatchdog = 2 * time.Minute

// imapConnect opens an authenticated IMAP connection (implicit TLS or
// STARTTLS, never plain). wake is called when the server reports new
// messages.
func (c Config) imapConnect(ctx context.Context, a Account, wake func()) (*imapclient.Client, error) {
	conn, err := c.dialConn(ctx, a.IMAP)
	if err != nil {
		return nil, err
	}
	_ = conn.SetDeadline(time.Now().Add(ioTimeout))
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stop()
	opts := &imapclient.Options{
		TLSConfig: c.tls(a.IMAP.Host),
		UnilateralDataHandler: &imapclient.UnilateralDataHandler{
			Mailbox: func(d *imapclient.UnilateralDataMailbox) {
				if d.NumMessages != nil && wake != nil {
					wake()
				}
			},
		},
	}
	var cl *imapclient.Client
	switch a.IMAP.TLS {
	case TLS:
		t := tls.Client(conn, opts.TLSConfig)
		if err := t.HandshakeContext(ctx); err != nil {
			_ = conn.Close()
			return nil, err
		}
		conn = tlsAsConn(t)
		cl = imapclient.New(conn, opts)
	default:
		cl, err = imapclient.NewStartTLS(conn, opts)
		if err != nil {
			return nil, err
		}
	}
	if err := cl.Login(a.Username, a.password).Wait(); err != nil {
		_ = cl.Close()
		var ie *imap.Error
		if errors.As(err, &ie) {
			return nil, &loginError{err}
		}
		return nil, err
	}
	_ = conn.SetDeadline(time.Time{})
	return cl, nil
}

func tlsAsConn(t *tls.Conn) net.Conn { return t }

// watch closes cl if the returned stop isn't called within opWatchdog.
func watch(cl *imapclient.Client) (stop func()) {
	t := time.AfterFunc(opWatchdog, func() { _ = cl.Close() })
	return func() { t.Stop() }
}

// selectFolder opens the folder read-only (the session never changes a
// flag: mail stays unread for its owner).
func selectFolder(cl *imapclient.Client, folder string) (*imap.SelectData, error) {
	defer watch(cl)()
	sel, err := cl.Select(folder, &imap.SelectOptions{ReadOnly: true}).Wait()
	if err != nil {
		return nil, fmt.Errorf("open folder %q: %w", folder, err)
	}
	return sel, nil
}

// highest is the folder's current end: the highest UID in use.
func highest(cl *imapclient.Client, sel *imap.SelectData) (uint32, error) {
	if sel.UIDNext > 1 {
		return uint32(sel.UIDNext) - 1, nil
	}
	if sel.NumMessages == 0 {
		return 0, nil
	}
	defer watch(cl)()
	data, err := cl.UIDSearch(&imap.SearchCriteria{}, nil).Wait()
	if err != nil {
		return 0, err
	}
	var top uint32
	for _, u := range data.AllUIDs() {
		top = max(top, uint32(u))
	}
	return top, nil
}

// Check connects with the account's IMAP and SMTP settings and says where
// reading would start (the folder's current end): what linking verifies
// before anything is stored.
func (c Config) Check(ctx context.Context, a Account) (Cursor, error) {
	cl, err := c.imapConnect(ctx, a, nil)
	if err != nil {
		return Cursor{}, fmt.Errorf("IMAP: %w", err)
	}
	defer func() { _ = cl.Close() }()
	stop := context.AfterFunc(ctx, func() { _ = cl.Close() })
	defer stop()
	sel, err := selectFolder(cl, a.Folder)
	if err != nil {
		return Cursor{}, fmt.Errorf("IMAP: %w", err)
	}
	top, err := highest(cl, sel)
	if err != nil {
		return Cursor{}, fmt.Errorf("IMAP: %w", err)
	}
	_ = cl.Logout().Wait()
	if err := c.smtpDo(ctx, a, func(*smtp.Client) error { return nil }); err != nil {
		if smtpRefusedLogin(err) {
			return Cursor{}, &loginError{fmt.Errorf("SMTP: %w", err)}
		}
		return Cursor{}, fmt.Errorf("SMTP: %w", err)
	}
	return Cursor{Validity: sel.UIDValidity, UID: top}, nil
}

// fetched is one message of the folder, read.
type fetched struct {
	UID      uint32
	Raw      []byte
	Oversize int64 // the size, when the message was too big to read (Raw: its headers)
	Received time.Time
}

// maxBatch bounds the messages taken in per round.
const maxBatch = 50

// newMail reads the messages after cursor, oldest first (at most
// maxBatch; more is true when the folder has more).
func newMail(ctx context.Context, cl *imapclient.Client, after uint32) (out []fetched, more bool, err error) {
	defer watch(cl)()
	var set imap.UIDSet
	set.AddRange(imap.UID(after+1), 0)
	list, err := cl.Fetch(set, &imap.FetchOptions{UID: true, RFC822Size: true, InternalDate: true}).Collect()
	if err != nil {
		return nil, false, err
	}
	// "n:*" always includes the last message, even below n.
	sort.Slice(list, func(i, j int) bool { return list[i].UID < list[j].UID })
	for _, m := range list {
		if uint32(m.UID) <= after {
			continue
		}
		if len(out) == maxBatch {
			return out, true, nil
		}
		if err := ctx.Err(); err != nil {
			return out, false, err
		}
		f := fetched{UID: uint32(m.UID), Received: m.InternalDate}
		section := &imap.FetchItemBodySection{Peek: true}
		if m.RFC822Size > MaxRaw {
			f.Oversize = m.RFC822Size
			section = &imap.FetchItemBodySection{Specifier: imap.PartSpecifierHeader, Peek: true}
		}
		bufs, err := cl.Fetch(imap.UIDSetNum(m.UID), &imap.FetchOptions{UID: true, BodySection: []*imap.FetchItemBodySection{section}}).Collect()
		if err != nil {
			return out, false, err
		}
		if len(bufs) == 0 {
			continue // expunged meanwhile
		}
		f.Raw = bufs[0].FindBodySection(&imap.FetchItemBodySection{Specifier: section.Specifier})
		out = append(out, f)
	}
	return out, false, nil
}
