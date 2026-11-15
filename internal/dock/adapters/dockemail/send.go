package dockemail

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"strings"
	"time"

	"github.com/Abraxas-365/seaport/internal/dock"
	"github.com/Abraxas-365/seaport/internal/errx"
	"github.com/emersion/go-message"
	gomail "github.com/emersion/go-message/mail"
)

const sendTimeout = 2 * time.Minute

// Send answers in the thread: to its participants, "Re: <subject>", with
// In-Reply-To and References so the recipients' clients thread it. Files
// go as attachments.
func (g *Gateway) Send(ctx context.Context, msg dock.Outgoing) error {
	a, err := g.store.Find(ctx, msg.Dock)
	if err != nil {
		return err
	}
	t, err := g.store.Thread(ctx, msg.Dock, msg.ChatID)
	if err != nil {
		return err
	}
	if len(t.Participants) == 0 {
		return errx.Business("this email thread has nobody to answer")
	}
	raw, err := build(a, t, msg)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, sendTimeout)
	defer cancel()
	if err := g.cfg.deliver(ctx, a, t.Participants, raw); err != nil {
		if smtpRefusedLogin(err) {
			if links, _ := g.bound(); links != nil {
				if lerr := links.SetStatus(ctx, a.Dock, dock.StatusLoggedOut); lerr != nil {
					slog.Warn("log out email dock", "dock", a.Dock, "err", lerr)
				}
			}
			return errx.External("the mailbox refused the SMTP login (password changed or revoked): link the dock again")
		}
		return errx.External("send the email: " + err.Error())
	}
	// What answers this message finds the thread.
	if err := g.store.Note(context.WithoutCancel(ctx), a.Organization, a.Dock, Thread{Chat: t.Chat}, msg.MessageID); err != nil {
		slog.Warn("remember sent email", "dock", a.Dock, "err", err)
	}
	return nil
}

// Open: an email dock has no destinations yet (a first message to an
// address would start a new thread; not done).
func (*Gateway) Open(context.Context, dock.Opening) (dock.Opened, error) {
	return dock.Opened{}, errx.Business("email docks have no destinations yet")
}

// build writes the reply as RFC 5322: a text part (quoted-printable) and
// one attachment per file.
func build(a Account, t Thread, msg dock.Outgoing) ([]byte, error) {
	var h gomail.Header
	h.SetDate(time.Now())
	from := &gomail.Address{Name: a.DisplayName, Address: a.Address}
	h.SetAddressList("From", []*gomail.Address{from})
	var to []*gomail.Address
	for _, p := range t.Participants {
		to = append(to, &gomail.Address{Address: p})
	}
	h.SetAddressList("To", to)
	h.SetSubject(replySubject(t.Subject))
	h.SetMessageID(strings.Trim(msg.MessageID, "<>"))
	if t.LastMessage != "" {
		h.SetMsgIDList("In-Reply-To", []string{t.LastMessage})
		refs := append([]string{}, t.Refs...)
		if refs = merge(refs, []string{t.LastMessage}, MaxRefs, true); len(refs) > 0 {
			h.SetMsgIDList("References", refs)
		}
	}
	h.Set("Auto-Submitted", "auto-generated")
	h.Set("X-Auto-Response-Suppress", "All")
	h.Set("MIME-Version", "1.0")

	var buf bytes.Buffer
	body := strings.ReplaceAll(strings.ReplaceAll(msg.Text, "\r\n", "\n"), "\n", "\r\n")
	if len(msg.Files) == 0 {
		w, err := gomail.CreateSingleInlineWriter(&buf, gomail.Header{Header: withType(h.Header, "text/plain; charset=utf-8")})
		if err != nil {
			return nil, errx.Wrap(err, "write email", errx.TypeInternal)
		}
		if _, err := io.WriteString(w, body); err != nil {
			return nil, errx.Wrap(err, "write email", errx.TypeInternal)
		}
		if err := w.Close(); err != nil {
			return nil, errx.Wrap(err, "write email", errx.TypeInternal)
		}
		return buf.Bytes(), nil
	}
	mw, err := gomail.CreateWriter(&buf, h)
	if err != nil {
		return nil, errx.Wrap(err, "write email", errx.TypeInternal)
	}
	var ih gomail.InlineHeader
	ih.Set("Content-Type", "text/plain; charset=utf-8")
	pw, err := mw.CreateSingleInline(ih)
	if err != nil {
		return nil, errx.Wrap(err, "write email", errx.TypeInternal)
	}
	_, _ = io.WriteString(pw, body)
	if err := pw.Close(); err != nil {
		return nil, errx.Wrap(err, "write email", errx.TypeInternal)
	}
	for _, f := range msg.Files {
		var ah gomail.AttachmentHeader
		kind := f.MIME
		if kind == "" {
			kind = "application/octet-stream"
		}
		ah.Set("Content-Type", kind)
		ah.SetFilename(f.Name)
		aw, err := mw.CreateAttachment(ah)
		if err != nil {
			return nil, errx.Wrap(err, "write email", errx.TypeInternal)
		}
		if _, err := aw.Write(f.Data); err != nil {
			return nil, errx.Wrap(err, "write email", errx.TypeInternal)
		}
		if err := aw.Close(); err != nil {
			return nil, errx.Wrap(err, "write email", errx.TypeInternal)
		}
	}
	if err := mw.Close(); err != nil {
		return nil, errx.Wrap(err, "write email", errx.TypeInternal)
	}
	return buf.Bytes(), nil
}

func withType(h message.Header, kind string) message.Header {
	h = h.Copy()
	h.Set("Content-Type", kind)
	return h
}
