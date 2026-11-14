package dockemail

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/Abraxas-365/seaport/internal/conversation"
	"github.com/Abraxas-365/seaport/internal/dock"
	"github.com/Abraxas-365/seaport/internal/errx"
	"github.com/Abraxas-365/seaport/internal/ship"
	"github.com/Abraxas-365/seaport/internal/text"
	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"
)

const (
	minBackoff = time.Second
	maxBackoff = 5 * time.Minute
	// healthy: a connection that lasted this long was fine, so the next
	// failure starts the backoff again.
	healthy = time.Minute
	// receiveTimeout bounds handing one message to the ship domain.
	receiveTimeout = 2 * time.Minute
	// subjectWindow: how long a reply whose client dropped the threading
	// headers may still join a thread by its subject.
	subjectWindow = 14 * 24 * time.Hour
	// idleRefresh: IDLE is ended and started again this often (servers
	// drop a connection that idles for 30 minutes or so).
	idleRefresh = 20 * time.Minute
	// maxAttempts: a message whose handling fails this many times is
	// skipped, so one that can never be taken in doesn't stop the mailbox.
	maxAttempts = 3
)

// session is one dock's mailbox connection in this process: it connects,
// takes in new mail and reconnects (backoff 1 s → 5 min) until stopped.
type session struct {
	g      *Gateway
	acct   Account
	ctx    context.Context
	cancel context.CancelFunc
	done   chan struct{}
	wake   chan struct{}
	cursor Cursor
	// attempts counts failed tries per UID (this session only).
	attempts map[uint32]int
}

func newSession(g *Gateway, a Account) *session {
	ctx, cancel := context.WithCancel(g.ctx)
	return &session{g: g, acct: a, ctx: ctx, cancel: cancel, done: make(chan struct{}), wake: make(chan struct{}, 1),
		cursor: Cursor{Validity: a.UIDValidity, UID: a.LastUID}, attempts: map[uint32]int{}}
}

// stop ends the session and waits a little for it.
func (s *session) stop() {
	s.cancel()
	select {
	case <-s.done:
	case <-time.After(10 * time.Second):
	}
}

func (s *session) nudge() {
	select {
	case s.wake <- struct{}{}:
	default:
	}
}

func (s *session) run() {
	defer close(s.done)
	delay := minBackoff
	for s.ctx.Err() == nil {
		began := time.Now()
		err := s.connected()
		if s.ctx.Err() != nil {
			return
		}
		var le *loginError
		switch {
		case errors.As(err, &le) && le.permanent:
			s.fail(err)
			s.logout()
			return
		case errors.Is(err, errReconfigured) || errors.Is(err, ErrStaleAccount):
			if a, ferr := s.g.store.Find(s.ctx, s.acct.Dock); ferr == nil {
				s.acct = a
				s.cursor = Cursor{Validity: a.UIDValidity, UID: a.LastUID}
			}
			delay = minBackoff
			continue
		}
		if time.Since(began) > healthy {
			delay = minBackoff
		}
		s.fail(err)
		slog.Warn("email dock disconnected", "dock", s.acct.Dock, "err", err, "retry_in", delay)
		select {
		case <-s.ctx.Done():
			return
		case <-time.After(delay):
		}
		delay = min(2*delay, maxBackoff)
	}
}

// fail records the connection's problem for the admin API.
func (s *session) fail(err error) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(s.ctx), 10*time.Second)
	defer cancel()
	if serr := s.g.store.SetState(ctx, s.acct.Dock, false, err.Error()); serr != nil {
		slog.Warn("store email state", "dock", s.acct.Dock, "err", serr)
	}
}

// logout: the mailbox refused the password for good (until the owner
// links the dock again).
func (s *session) logout() {
	slog.Warn("email dock logged out: the mailbox refused its password", "dock", s.acct.Dock)
	ctx, cancel := context.WithTimeout(context.WithoutCancel(s.ctx), 10*time.Second)
	defer cancel()
	if links, _ := s.g.bound(); links != nil {
		if err := links.SetStatus(ctx, s.acct.Dock, dock.StatusLoggedOut); err != nil {
			slog.Warn("log out email dock", "dock", s.acct.Dock, "err", err)
		}
	}
}

// connected holds one connection: it returns why it ended.
func (s *session) connected() error {
	cl, err := s.g.cfg.imapConnect(s.ctx, s.acct, s.nudge)
	if err != nil {
		return err
	}
	defer func() { _ = cl.Close() }()
	stop := context.AfterFunc(s.ctx, func() { _ = cl.Close() })
	defer stop()
	sel, err := selectFolder(cl, s.acct.Folder)
	if err != nil {
		return err
	}
	if err := s.syncCursor(cl, sel); err != nil {
		return err
	}
	if err := s.g.store.SetState(s.ctx, s.acct.Dock, true, ""); err != nil {
		return err
	}
	_, canIdle := cl.Caps()[imap.CapIdle]
	if !canIdle {
		_, canIdle = cl.Caps()[imap.CapIMAP4rev2]
	}
	recheck := time.NewTicker(s.g.cfg.recheck())
	defer recheck.Stop()
	for {
		more := true
		for more {
			var batch []fetched
			batch, more, err = newMail(s.ctx, cl, s.cursor.UID)
			if err != nil {
				return err
			}
			for _, f := range batch {
				if err := s.take(f); err != nil {
					return err
				}
			}
		}
		if err := s.wait(cl, canIdle, recheck.C); err != nil {
			return err
		}
	}
}

// syncCursor decides where reading starts: where it was, unless the
// folder was renumbered (UIDVALIDITY changed: the old UIDs mean nothing)
// or never read, when it starts at the folder's end, with no backlog.
func (s *session) syncCursor(cl *imapclient.Client, sel *imap.SelectData) error {
	if s.cursor.Validity == sel.UIDValidity && s.cursor.Validity != 0 {
		return nil
	}
	top, err := highest(cl, sel)
	if err != nil {
		return err
	}
	if s.cursor.Validity != 0 {
		slog.Warn("email folder was renumbered; reading from its end", "dock", s.acct.Dock, "uidvalidity", sel.UIDValidity)
	}
	s.cursor = Cursor{Validity: sel.UIDValidity, UID: top}
	return s.g.store.SetCursor(s.ctx, s.acct, s.cursor)
}

// wait blocks until there may be new mail: the server said so (IDLE), the
// poll interval passed, or the settings changed (errReconfigured).
func (s *session) wait(cl *imapclient.Client, idle bool, recheck <-chan time.Time) error {
	var idleCmd *imapclient.IdleCommand
	if idle {
		var err error
		if idleCmd, err = cl.Idle(); err != nil {
			return err
		}
	}
	var poll, refresh <-chan time.Time
	if idle {
		t := time.NewTimer(idleRefresh)
		defer t.Stop()
		refresh = t.C
	}
	if !idle || s.g.cfg.Poll > 0 {
		t := time.NewTimer(s.g.cfg.poll())
		defer t.Stop()
		poll = t.C
	}
	var result error
loop:
	for {
		select {
		case <-s.ctx.Done():
			result = s.ctx.Err()
			break loop
		case <-cl.Closed():
			result = errors.New("the connection closed")
			break loop
		case <-s.wake:
			break loop
		case <-poll:
			break loop
		case <-refresh:
			break loop
		case <-recheck:
			a, err := s.g.store.Find(s.ctx, s.acct.Dock)
			if errors.Is(err, context.Canceled) {
				result = err
				break loop
			}
			if err == nil && !a.Updated.Equal(s.acct.Updated) {
				result = errReconfigured
				break loop
			}
		}
	}
	if idleCmd != nil {
		if err := idleCmd.Close(); err != nil && result == nil {
			result = err
		}
		if err := idleCmd.Wait(); err != nil && result == nil {
			result = err
		}
	}
	if result == nil && !idle {
		defer watch(cl)()
		// A poll asks the server for news (and keeps the connection alive).
		result = cl.Noop().Wait()
	}
	return result
}

// take hands one message to the ship domain and moves the cursor past it.
// Mail that can't become a ship is skipped (logged), never retried for
// ever; a failure of ours (database, ship service) is returned so the
// message is read again after reconnecting, but only maxAttempts times:
// then it is logged and skipped like the others.
func (s *session) take(f fetched) error {
	if err := s.outcome(f.UID, s.handle(f)); err != nil {
		return err
	}
	s.cursor.UID = f.UID
	return s.g.store.SetCursor(s.ctx, s.acct, s.cursor)
}

// outcome says whether handling a message ended (nil: move on) or is to
// be tried again after reconnecting (the error).
func (s *session) outcome(uid uint32, err error) error {
	switch {
	case err == nil:
	case errx.IsType(err, errx.TypeValidation):
		slog.Warn("email skipped", "dock", s.acct.Dock, "uid", uid, "err", err)
	case s.ctx.Err() != nil:
		return err
	default:
		s.attempts[uid]++
		if s.attempts[uid] < maxAttempts {
			return err
		}
		slog.Error("email skipped after repeated failures", "dock", s.acct.Dock, "uid", uid, "attempts", maxAttempts, "err", err)
	}
	delete(s.attempts, uid)
	return nil
}

func (s *session) handle(f fetched) error {
	m, err := ParseMessage(f.Raw)
	if err != nil {
		slog.Warn("email unreadable", "dock", s.acct.Dock, "uid", f.UID, "err", err)
		return nil
	}
	if f.Oversize > 0 {
		m.Notes = append(m.Notes, fmt.Sprintf("the message is %d MiB: too large, its body was not read", f.Oversize>>20))
	}
	if skip := s.skip(m); skip != "" {
		slog.Info("email ignored", "dock", s.acct.Dock, "uid", f.UID, "reason", skip)
		return nil
	}
	if m.Auth != AuthPass {
		m.Notes = append(m.Notes, "sender not verified: the mailbox's server added no authentication result for it")
	}
	in, thread, err := s.inbound(f, m)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(s.ctx, receiveTimeout)
	defer cancel()
	// Only the sender joins the thread before customs has spoken.
	if err := s.g.store.Note(ctx, s.acct.Organization, s.acct.Dock, thread, m.ID); err != nil {
		return err
	}
	_, receiver := s.g.bound()
	if receiver == nil {
		return errors.New("the ship service is not bound yet")
	}
	st, _, err := receiver.Receive(ctx, s.acct.Organization, in)
	if err != nil {
		return err
	}
	// The others of the conversation (Reply-To of the sender's domain, To,
	// Cc) are written to only once a message of the thread got through
	// customs: a held or denied message adds nobody.
	if st.Status != ship.StatusHeld {
		if extra := s.extras(m); len(extra) > 0 {
			t := Thread{Chat: thread.Chat, Participants: extra}
			return s.g.store.Note(ctx, s.acct.Organization, s.acct.Dock, t, "")
		}
	}
	return nil
}

// skip names why a message is not for the crew: the mailbox's own mail
// (its replies, copies of what it sent), mail no person wrote and mail
// whose sender the mailbox's server did not vouch for.
func (s *session) skip(m Message) string {
	from := m.From.Address
	local, _, _ := strings.Cut(from, "@")
	switch {
	case !validAddress(from):
		return "no sender address"
	case from == strings.ToLower(s.acct.Address):
		return "sent by the mailbox itself"
	case m.Automatic:
		return "automatic mail"
	case local == "mailer-daemon" || local == "postmaster":
		return "delivery report"
	case m.Auth == AuthFail:
		return "sender failed authentication (SPF/DKIM/DMARC)"
	case m.Auth == AuthNone && !s.g.cfg.AllowUnverified:
		return "no authentication result from the mailbox's server (SEAPORT_EMAIL_REQUIRE_AUTH)"
	}
	return ""
}

// sender is where answers to a message go: its From, or its Reply-To when
// that is on the same domain (a Reply-To elsewhere is the sender's say-so
// and could point anywhere).
func sender(m Message) string {
	if m.ReplyTo != "" && domainOf(m.ReplyTo) == domainOf(m.From.Address) {
		return m.ReplyTo
	}
	return m.From.Address
}

// extras are the addresses a message adds to its thread's recipients: its
// To and Cc, minus the mailbox itself. Called only for a message that got
// through customs.
func (s *session) extras(m Message) []string {
	own := strings.ToLower(s.acct.Address)
	var out []string
	for _, p := range m.Others {
		if p != own {
			out = append(out, p)
		}
	}
	return out
}

// inbound builds the ship and the thread note of a message. The thread is
// found by the message ids it answers, else (a reply whose client dropped
// them) by subject, else a new one starts; but only a thread the sender
// already takes part in counts, so naming someone else's Message-ID can't
// pull a message (and the sender as a recipient) into their conversation:
// it starts a thread of its own and customs decides about it.
func (s *session) inbound(f fetched, m Message) (ship.Inbound, Thread, error) {
	ctx, cancel := context.WithTimeout(s.ctx, receiveTimeout)
	defer cancel()
	var ids []string
	if m.ID != "" {
		ids = append(ids, m.ID) // a message read again keeps its thread
	}
	if m.InReplyTo != "" {
		ids = append(ids, m.InReplyTo)
	}
	for i := len(m.References) - 1; i >= 0; i-- {
		ids = append(ids, m.References[i])
	}
	who := sender(m)
	members := []string{m.From.Address, who}
	chat, found, err := s.g.store.ThreadOf(ctx, s.acct.Dock, ids, members)
	if err != nil {
		return ship.Inbound{}, Thread{}, err
	}
	key := SubjectKey(m.Subject)
	if !found && key != strings.ToLower(strings.TrimSpace(m.Subject)) {
		chat, found, err = s.g.store.ThreadBySubject(ctx, s.acct.Dock, key, members, subjectWindow)
		if err != nil {
			return ship.Inbound{}, Thread{}, err
		}
	}
	first := !found
	var earlier []string // who the thread already writes to
	if first {
		chat = chatID(m.ID)
		if m.ID == "" {
			chat = fit(fmt.Sprintf("%s/%d/%d", s.acct.Dock, s.cursor.Validity, f.UID), "u-")
		}
	} else if t, err := s.g.store.Thread(ctx, s.acct.Dock, chat); err == nil {
		earlier = t.Participants
	}
	refs := append(append([]string{}, m.References...), m.InReplyTo)
	thread := Thread{Chat: chat, Subject: m.Subject, Key: key, Participants: []string{who}, Refs: refs}

	// A conversation with more than one other person is a group (whose
	// messages need to address the mailbox: it is in To, not just Cc/Bcc).
	own := strings.ToLower(s.acct.Address)
	kind, addressed := conversation.KindDirect, true
	if first {
		others := map[string]bool{m.From.Address: true, who: true}
		for _, p := range m.Others {
			if p != own {
				others[p] = true
			}
		}
		if len(others) > 1 {
			kind = conversation.KindGroup
		}
	}
	// Addressed: the mailbox is a To recipient, not only in Cc or Bcc.
	addressed = false
	for _, p := range m.To {
		if p == own {
			addressed = true
		}
	}

	body := m.Text
	if first && m.Subject != "" {
		body = "Subject: " + m.Subject + "\n\n" + body
	}
	if len(m.Notes) > 0 {
		body = strings.TrimSpace(body) + "\n\n[" + strings.Join(m.Notes, "; ") + "]"
	}
	body = text.StripControl(strings.TrimSpace(body))
	if utf8.RuneCountInString(body) > text.MaxMessage {
		body = string([]rune(body)[:text.MaxMessage-40]) + "\n[…message cut: too long]"
	}
	if body == "" && len(m.Media) == 0 {
		body = "(empty message)"
	}
	external := m.ID
	if external == "" || len(external) > conversation.MaxExternalID || text.HasForbiddenControl(external) {
		external = fit(m.ID+"/"+s.acct.Dock.String()+fmt.Sprint(f.UID), "x-")
	}
	sent := m.Date
	if sent.IsZero() {
		sent = f.Received
	}
	if sent.IsZero() {
		sent = time.Now()
	}
	// Handles customs matches: the sender first (never cut), then their
	// domain, then who the thread already writes to. Never the To/Cc of
	// this message: a rule that allows one of them must not let a stranger
	// in by copying them.
	domain := "@" + domainOf(m.From.Address)
	handles := handleList(append([]string{m.From.Address, who, domain}, earlier...))
	in := ship.Inbound{
		Dock: s.acct.Dock, Channel: dock.ChannelEmail,
		ChatID: chat, ChatKind: kind, ChatTitle: clean(m.Subject, text.MaxName), ChatHandles: handles,
		ExternalID: external, SenderID: m.From.Address, SenderName: clean(m.From.Name, text.MaxName),
		SenderHandles: handleList([]string{m.From.Address, domain}), Text: body, SentAt: sent, Addressed: addressed, Media: m.Media,
	}
	return in, thread, nil
}

// handleList dedupes handles and keeps the first conversation.MaxHandles
// (an arrival reporting more is refused whole).
func handleList(in []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, h := range in {
		if h != "" && h != "@" && len(h) <= conversation.MaxExternalID && !seen[h] {
			seen[h] = true
			out = append(out, h)
		}
	}
	if len(out) > conversation.MaxHandles {
		out = out[:conversation.MaxHandles]
	}
	return out
}
