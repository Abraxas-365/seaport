// Package dockwa is the WhatsApp gateway: each dock is a whatsmeow
// "linked device" (multi-device protocol, no browser). Its signal store
// is Seaport's own (store.go): the wa_* tables of the same Postgres, with
// every key sealed by SEAPORT_SECRET_KEY.
package dockwa

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Abraxas-365/seaport/internal/conversation"
	"github.com/Abraxas-365/seaport/internal/dock"
	"github.com/Abraxas-365/seaport/internal/errx"
	"github.com/Abraxas-365/seaport/internal/identity"
	"github.com/Abraxas-365/seaport/internal/seal"
	"github.com/Abraxas-365/seaport/internal/ship"
	"github.com/jmoiron/sqlx"
	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/store"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	"google.golang.org/protobuf/proto"
)

const (
	// minSendGap spaces outbound messages per dock (anti-ban).
	minSendGap = 1200 * time.Millisecond
	// handlerTimeout bounds the work done for one WhatsApp event.
	handlerTimeout = 30 * time.Second
	// downloadTimeout bounds an image download (within handlerTimeout).
	downloadTimeout = 20 * time.Second
	// encryptionOverhead: WhatsApp's encrypted media is the file plus up
	// to one AES block of padding and a 10-byte MAC.
	encryptionOverhead = 16 + 10
	// linkTimeout bounds a QR pairing session.
	linkTimeout = 3 * time.Minute
)

// Receiver is what the gateway needs from the ship domain.
type Receiver interface {
	Receive(ctx context.Context, organization identity.OrganizationID, input ship.Inbound) (ship.Ship, bool, error)
}

// LinkSink is what the gateway needs from the dock domain.
type LinkSink interface {
	SetLinked(ctx context.Context, dock identity.DockID, address string) error
	SetStatus(ctx context.Context, dock identity.DockID, status dock.Status) error
	PairingChanged(ctx context.Context, dock identity.DockID)
}

type Gateway struct {
	devices *Store

	mu       sync.Mutex
	clients  map[identity.DockID]*session
	receiver Receiver
	links    LinkSink
	// replaced: docks whose connection another one replaced, and until
	// when Restore refuses them (growing while it keeps happening).
	replaced map[identity.DockID]replacement
}

type replacement struct {
	until, at time.Time
	delay     time.Duration
}

// replacedPause is the first pause after a replaced connection; it doubles
// while replacements keep coming (within an hour), up to maxReplacedPause.
const (
	replacedPause    = 30 * time.Second
	maxReplacedPause = 10 * time.Minute
)

type session struct {
	dock identity.DockID
	// organization owns the dock; its messages arrive in it.
	organization identity.OrganizationID
	client       *whatsmeow.Client

	// ended: unlinked, stopped (the dock moved to another process) or
	// replaced; its pairing goroutine and events must not report any more.
	ended atomic.Bool

	mu       sync.Mutex
	lastSend time.Time
	titles   map[types.JID]string
	// sentFiles are the ids of file messages sent after a reply's text
	// (no stored ship has them), so their echo in the self chat is dropped.
	sentFiles []types.MessageID
}

// maxSentFiles bounds the remembered file message ids.
const maxSentFiles = 64

func (s *session) sentFile(id types.MessageID) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sentFiles = append(s.sentFiles, id)
	if len(s.sentFiles) > maxSentFiles {
		s.sentFiles = s.sentFiles[len(s.sentFiles)-maxSentFiles:]
	}
}

func (s *session) isSentFile(id types.MessageID) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Contains(s.sentFiles, id)
}

// Open opens the sealed store (secretKey: SEAPORT_SECRET_KEY).
func Open(ctx context.Context, db *sqlx.DB, secretKey string) (*Gateway, error) {
	sealer, err := seal.New(secretKey, SealInfo)
	if err != nil {
		return nil, err
	}
	devices := NewStore(db, sealer)
	if err := devices.CheckKey(ctx); err != nil {
		return nil, err
	}
	return &Gateway{devices: devices, clients: map[identity.DockID]*session{}, replaced: map[identity.DockID]replacement{}}, nil
}

// Bind sets the domain callbacks (they are built after the gateway).
func (g *Gateway) Bind(links LinkSink, receiver Receiver) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.links, g.receiver = links, receiver
}

var _ dock.Gateway = (*Gateway)(nil)

func (g *Gateway) Channel() dock.Channel { return dock.ChannelWhatsApp }

// Exclusive: a WhatsApp device keeps one connection; a second one replaces
// the first (StreamReplaced).
func (g *Gateway) Exclusive() bool { return true }

// Close disconnects every dock.
func (g *Gateway) Close() {
	g.mu.Lock()
	sessions := g.clients
	g.clients = map[identity.DockID]*session{}
	g.mu.Unlock()
	for _, s := range sessions {
		s.ended.Store(true)
		s.client.Disconnect()
	}
}

// take ends the dock's session in this process, if any.
func (g *Gateway) take(id identity.DockID) *session {
	g.mu.Lock()
	s := g.clients[id]
	delete(g.clients, id)
	g.mu.Unlock()
	if s != nil {
		s.ended.Store(true)
	}
	return s
}

func (g *Gateway) Running(id identity.DockID) bool { return g.session(id) != nil }

func (g *Gateway) RunningDocks() []identity.DockID {
	g.mu.Lock()
	defer g.mu.Unlock()
	out := make([]identity.DockID, 0, len(g.clients))
	for id := range g.clients {
		out = append(out, id)
	}
	return out
}

// Stop disconnects the dock and keeps its keys (another process runs it
// next). A pairing in progress is abandoned.
func (g *Gateway) Stop(id identity.DockID) {
	if s := g.take(id); s != nil {
		s.client.Disconnect()
		ctx, cancel := context.WithTimeout(context.Background(), handlerTimeout)
		defer cancel()
		if err := g.devices.SetPairing(ctx, s.organization, id, ""); err != nil {
			slog.Warn("delete pairing code", "dock", id, "err", err)
		}
		slog.Info("whatsapp dock stopped in this process", "dock", id)
	}
}

func (g *Gateway) Link(ctx context.Context, d dock.Dock) (dock.Status, error) {
	if old := g.take(d.ID); old != nil {
		old.client.Disconnect()
	}

	device := g.devices.NewDevice(d.Organization, d.ID)
	s := g.newSession(d, device)
	// Running from now on (the pairing goroutine may end it at once).
	g.mu.Lock()
	g.clients[d.ID] = s
	g.mu.Unlock()
	qrCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), linkTimeout)
	qrs, err := s.client.GetQRChannel(qrCtx)
	if err != nil {
		cancel()
		g.end(s)
		return "", errx.Wrap(err, "start whatsapp pairing", errx.TypeExternal)
	}
	if err := s.client.Connect(); err != nil {
		cancel()
		g.end(s)
		return "", errx.Wrap(err, "connect to whatsapp", errx.TypeExternal)
	}
	first := make(chan struct{})
	go func() {
		defer cancel()
		var once sync.Once
		for item := range qrs {
			if s.ended.Load() {
				continue
			}
			switch item.Event {
			case whatsmeow.QRChannelEventCode:
				g.setPairing(s, item.Code)
				once.Do(func() { close(first) })
				g.pairingChanged(d.ID)
			case whatsmeow.QRChannelSuccess.Event:
				g.setPairing(s, "")
			default:
				// timeout, error or scanned without multidevice
				g.setPairing(s, "")
				slog.Warn("whatsapp pairing ended", "dock", d.ID, "event", item.Event, "err", item.Error)
				if !s.client.IsLoggedIn() {
					g.setStatus(d.ID, dock.StatusFailed)
					g.end(s)
					s.client.Disconnect()
				} else {
					g.pairingChanged(d.ID)
				}
			}
		}
		once.Do(func() { close(first) })
	}()
	// Wait briefly for the first code so the create response carries it.
	select {
	case <-first:
	case <-time.After(10 * time.Second):
	}
	return dock.StatusLinking, nil
}

func (g *Gateway) Restore(ctx context.Context, d dock.Dock) error {
	device, err := g.devices.Device(ctx, d.Organization, d.ID)
	if err != nil {
		return err
	}
	if device == nil {
		g.setStatus(d.ID, dock.StatusLoggedOut)
		return errx.NotFound("whatsapp device of dock " + d.ID.String() + " is gone; link it again")
	}
	g.mu.Lock()
	r := g.replaced[d.ID]
	g.mu.Unlock()
	if time.Now().Before(r.until) {
		return errx.External("whatsapp dock " + d.ID.String() + " was connected elsewhere moments ago; waiting before reconnecting")
	}
	if old := g.take(d.ID); old != nil {
		old.client.Disconnect()
	}
	s := g.newSession(d, device)
	if err := s.client.Connect(); err != nil {
		return errx.Wrap(err, "connect to whatsapp", errx.TypeExternal)
	}
	g.mu.Lock()
	g.clients[d.ID] = s
	g.mu.Unlock()
	slog.Info("whatsapp dock connected", "dock", d.ID, "jid", d.Address)
	return nil
}

// Unlink logs the device out of WhatsApp (it disappears from the phone's
// linked devices) and deletes its keys, or stops a pairing in progress.
func (g *Gateway) Unlink(ctx context.Context, d dock.Dock) error {
	if err := g.devices.SetPairing(ctx, d.Organization, d.ID, ""); err != nil {
		slog.Warn("delete pairing code", "dock", d.ID, "err", err)
	}
	if s := g.take(d.ID); s != nil {
		if s.client.IsLoggedIn() {
			if err := s.client.Logout(ctx); err != nil {
				s.client.Disconnect()
				return errx.Wrap(err, "log out of whatsapp", errx.TypeExternal)
			}
			slog.Info("whatsapp dock logged out by owner", "dock", d.ID)
			return nil
		}
		s.client.Disconnect()
	} else if g.logoutOffline(ctx, d) {
		return nil
	}
	// Not connected: forget stored keys so a relink starts clean.
	return g.devices.DeleteDock(ctx, d.Organization, d.ID)
}

// logoutOffline logs out a linked device this process isn't running (it
// owns the dock but hasn't reconnected it yet), so the phone drops it.
// Callers delete the keys when this fails.
func (g *Gateway) logoutOffline(ctx context.Context, d dock.Dock) bool {
	device, err := g.devices.Device(ctx, d.Organization, d.ID)
	if err != nil || device == nil {
		return false
	}
	s := g.newSession(d, device)
	s.ended.Store(true) // no callbacks: the caller stores logged_out
	if err := s.client.Connect(); err != nil {
		slog.Warn("connect to log out", "dock", d.ID, "err", err)
		return false
	}
	defer s.client.Disconnect()
	if !s.client.WaitForConnection(15 * time.Second) {
		slog.Warn("whatsapp did not connect to log out", "dock", d.ID)
		return false
	}
	if err := s.client.Logout(ctx); err != nil {
		slog.Warn("log out of whatsapp", "dock", d.ID, "err", err)
		return false
	}
	slog.Info("whatsapp dock logged out by owner", "dock", d.ID)
	return true
}

func (g *Gateway) QR(ctx context.Context, id identity.DockID) (string, error) {
	return g.devices.Pairing(ctx, id)
}

// NewMessageID is a random WhatsApp message id; any process can make one
// (the send may run in the dock's owner).
func (g *Gateway) NewMessageID(identity.DockID) (string, error) {
	return string(whatsmeow.GenerateMessageID()), nil
}

func (g *Gateway) Send(ctx context.Context, msg dock.Outgoing) error {
	s := g.session(msg.Dock)
	if s == nil {
		return errx.Business("whatsapp dock is not connected")
	}
	to, err := types.ParseJID(msg.ChatID)
	if err != nil {
		return errx.Wrap(err, "parse chat id", errx.TypeInternal)
	}
	// The text goes with the message id stored for it (so its echo is a
	// duplicate), each file as its own image or document message after it
	// (the first file takes the id when there is no text).
	id := types.MessageID(msg.MessageID)
	if msg.Text != "" {
		s.pace(ctx)
		_, err = s.client.SendMessage(ctx, to, &waE2E.Message{Conversation: proto.String(msg.Text)}, whatsmeow.SendRequestExtra{ID: id})
		if err != nil {
			return errx.Wrap(err, "send whatsapp message", errx.TypeExternal)
		}
		id = ""
	}
	for _, f := range msg.Files {
		if id == "" {
			id = whatsmeow.GenerateMessageID()
			s.sentFile(id)
		}
		m, err := s.fileMessage(ctx, f)
		if err != nil {
			return err
		}
		s.pace(ctx)
		if _, err := s.client.SendMessage(ctx, to, m, whatsmeow.SendRequestExtra{ID: id}); err != nil {
			return errx.Wrap(err, "send whatsapp file "+f.Name, errx.TypeExternal)
		}
		id = ""
	}
	return nil
}

// fileMessage uploads a file (encrypted, to WhatsApp's media servers) and
// returns the image or document message that shows it.
func (s *session) fileMessage(ctx context.Context, f dock.File) (*waE2E.Message, error) {
	kind := whatsmeow.MediaDocument
	if f.IsImage() {
		kind = whatsmeow.MediaImage
	}
	up, err := s.client.Upload(ctx, f.Data, kind)
	if err != nil {
		return nil, errx.Wrap(err, "upload whatsapp file "+f.Name, errx.TypeExternal)
	}
	if f.IsImage() {
		return &waE2E.Message{ImageMessage: &waE2E.ImageMessage{
			URL: proto.String(up.URL), DirectPath: proto.String(up.DirectPath), MediaKey: up.MediaKey,
			Mimetype: proto.String(f.MIME), FileEncSHA256: up.FileEncSHA256, FileSHA256: up.FileSHA256,
			FileLength: proto.Uint64(up.FileLength),
		}}, nil
	}
	return &waE2E.Message{DocumentMessage: &waE2E.DocumentMessage{
		URL: proto.String(up.URL), DirectPath: proto.String(up.DirectPath), MediaKey: up.MediaKey,
		Mimetype: proto.String(f.MIME), FileEncSHA256: up.FileEncSHA256, FileSHA256: up.FileSHA256,
		FileLength: proto.Uint64(up.FileLength), FileName: proto.String(f.Name), Title: proto.String(f.Name),
	}}, nil
}

func (g *Gateway) session(id identity.DockID) *session {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.clients[id]
}

func (g *Gateway) newSession(d dock.Dock, device *store.Device) *session {
	s := &session{dock: d.ID, organization: d.Organization, client: whatsmeow.NewClient(device, newLogger("client")), titles: map[types.JID]string{}}
	s.client.AddEventHandler(func(evt any) { g.handle(s, evt) })
	return s
}

// setPairing stores the session's current pairing code for every process.
func (g *Gateway) setPairing(s *session, code string) {
	ctx, cancel := context.WithTimeout(context.Background(), handlerTimeout)
	defer cancel()
	if err := g.devices.SetPairing(ctx, s.organization, s.dock, code); err != nil {
		slog.Error("store pairing code", "dock", s.dock, "err", err)
	}
}

// end forgets s if it is still the dock's session.
func (g *Gateway) end(s *session) {
	s.ended.Store(true)
	g.mu.Lock()
	if g.clients[s.dock] == s {
		delete(g.clients, s.dock)
	}
	g.mu.Unlock()
}

// pace keeps at least minSendGap between two sends of one dock.
func (s *session) pace(ctx context.Context) {
	s.mu.Lock()
	wait := time.Until(s.lastSend.Add(minSendGap))
	if wait < 0 {
		wait = 0
	}
	s.lastSend = time.Now().Add(wait)
	s.mu.Unlock()
	select {
	case <-time.After(wait):
	case <-ctx.Done():
	}
}

func (g *Gateway) setStatus(id identity.DockID, status dock.Status) {
	g.mu.Lock()
	links := g.links
	g.mu.Unlock()
	if links == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), handlerTimeout)
	defer cancel()
	if err := links.SetStatus(ctx, id, status); err != nil {
		slog.Error("set dock status", "dock", id, "status", status, "err", err)
	}
}

func (g *Gateway) pairingChanged(id identity.DockID) {
	g.mu.Lock()
	links := g.links
	g.mu.Unlock()
	if links == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), handlerTimeout)
	defer cancel()
	links.PairingChanged(ctx, id)
}

func (g *Gateway) handle(s *session, evt any) {
	if s.ended.Load() {
		return
	}
	g.mu.Lock()
	links, receiver := g.links, g.receiver
	g.mu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), handlerTimeout)
	defer cancel()
	switch e := evt.(type) {
	case *events.PairSuccess:
		slog.Info("whatsapp dock paired", "dock", s.dock, "jid", e.ID)
		if links != nil {
			if err := links.SetLinked(ctx, s.dock, e.ID.String()); err != nil {
				slog.Error("set dock linked", "dock", s.dock, "err", err)
			}
		}
	case *events.LoggedOut:
		slog.Warn("whatsapp dock logged out", "dock", s.dock, "reason", e.Reason)
		g.end(s)
		g.setStatus(s.dock, dock.StatusLoggedOut)
	case *events.StreamReplaced:
		// Another connection of this device took over: another process
		// that believes it owns the dock, or a copy of the keys elsewhere.
		// The owner reconnects it later (backoff).
		g.mu.Lock()
		r := g.replaced[s.dock]
		r.delay = replacedPause
		if time.Since(r.at) < time.Hour {
			r.delay = min(2*r.delay, maxReplacedPause)
		}
		r.at, r.until = time.Now(), time.Now().Add(r.delay)
		g.replaced[s.dock] = r
		g.mu.Unlock()
		slog.Error("whatsapp dock connection replaced by another one", "dock", s.dock, "reconnect_in", r.delay)
		g.end(s)
		s.client.Disconnect()
	case *events.Message:
		if receiver == nil {
			return
		}
		in, ok := s.inbound(ctx, e)
		if !ok {
			return
		}
		if _, _, err := receiver.Receive(ctx, s.organization, in); err != nil {
			slog.Error("receive whatsapp message", "dock", s.dock, "chat", in.ChatID, "err", err)
		}
	}
}

// inbound turns a WhatsApp message into a ship, or ok=false to ignore it:
// status broadcasts, newsletters, our own messages to other chats (and the
// echo of our sends), protocol messages and anything without content.
func (s *session) inbound(ctx context.Context, e *events.Message) (ship.Inbound, bool) {
	info := e.Info
	chat := info.Chat.ToNonAD()
	if chat.Server == types.BroadcastServer || chat.Server == types.NewsletterServer {
		return ship.Inbound{}, false
	}
	own, ownLID := s.client.Store.GetJID().ToNonAD(), s.client.Store.GetLID().ToNonAD()
	self := !info.IsGroup && (chat.User == own.User || (!ownLID.IsEmpty() && chat.User == ownLID.User))
	if info.IsFromMe && (!self || s.isSentFile(info.ID)) {
		return ship.Inbound{}, false
	}
	body := messageText(e.Message)
	var media []ship.Media
	if img := e.Message.GetImageMessage(); img != nil {
		// WhatsApp's media URLs expire: the image is downloaded now.
		var m *ship.Media
		m, body = imageMedia(img, s.client.DownloadToFile)
		if m != nil {
			media = append(media, *m)
		}
	}
	if body == "" && len(media) == 0 {
		return ship.Inbound{}, false
	}
	in := ship.Inbound{
		Dock:       s.dock,
		Channel:    dock.ChannelWhatsApp,
		ChatID:     chat.String(),
		ChatKind:   conversation.KindDirect,
		ExternalID: string(info.ID),
		SenderID:   info.Sender.ToNonAD().String(),
		SenderName: info.PushName,
		Text:       body,
		SentAt:     info.Timestamp,
		Media:      media,
	}
	sender := []types.JID{info.Sender.ToNonAD(), info.SenderAlt.ToNonAD()}
	in.SenderHandles = s.handles(ctx, sender...)
	switch {
	case self:
		in.ChatID, in.ChatKind, in.ChatTitle = own.String(), conversation.KindSelf, "Me (self chat)"
	case info.IsGroup:
		in.ChatKind, in.ChatTitle = conversation.KindGroup, s.groupTitle(ctx, chat)
		in.ChatHandles = []string{chat.String()}
		in.Addressed = addressed(e.Message, own, ownLID)
	default:
		in.ChatTitle = info.PushName
		// The peer's phone and LID both identify a direct chat.
		in.ChatHandles = s.handles(ctx, append([]types.JID{chat}, sender...)...)
	}
	if in.SentAt.IsZero() {
		in.SentAt = time.Now()
	}
	return in, true
}

// handles are the customs identities of users: the JID ("…@lid",
// "…@s.whatsapp.net") and the phone as "+<digits>", looked up for LIDs
// when whatsmeow knows the mapping.
func (s *session) handles(ctx context.Context, jids ...types.JID) []string {
	var out []string
	for _, j := range jids {
		if j.IsEmpty() {
			continue
		}
		out = append(out, j.String())
		switch j.Server {
		case types.DefaultUserServer:
			out = append(out, "+"+j.User)
		case types.HiddenUserServer:
			if pn, err := s.client.Store.LIDs.GetPNForLID(ctx, j); err == nil && !pn.IsEmpty() {
				out = append(out, pn.ToNonAD().String(), "+"+pn.User)
			}
		}
	}
	return out
}

// addressed: the message mentions the account or replies to one of its
// messages.
func addressed(m *waE2E.Message, own, ownLID types.JID) bool {
	ci := contextInfo(m)
	if ci == nil {
		return false
	}
	isOwn := func(raw string) bool {
		j, err := types.ParseJID(raw)
		if err != nil {
			return false
		}
		return j.User == own.User || (!ownLID.IsEmpty() && j.User == ownLID.User)
	}
	for _, raw := range ci.GetMentionedJID() {
		if isOwn(raw) {
			return true
		}
	}
	return ci.GetStanzaID() != "" && isOwn(ci.GetParticipant())
}

func contextInfo(m *waE2E.Message) *waE2E.ContextInfo {
	for _, ci := range []*waE2E.ContextInfo{
		m.GetExtendedTextMessage().GetContextInfo(),
		m.GetImageMessage().GetContextInfo(),
		m.GetVideoMessage().GetContextInfo(),
		m.GetDocumentMessage().GetContextInfo(),
		m.GetAudioMessage().GetContextInfo(),
		m.GetStickerMessage().GetContextInfo(),
	} {
		if ci != nil {
			return ci
		}
	}
	return nil
}

func (s *session) groupTitle(ctx context.Context, chat types.JID) string {
	s.mu.Lock()
	title, ok := s.titles[chat]
	s.mu.Unlock()
	if ok {
		return title
	}
	if info, err := s.client.GetGroupInfo(ctx, chat); err == nil {
		title = info.Name
	}
	s.mu.Lock()
	s.titles[chat] = title
	s.mu.Unlock()
	return title
}

// imageMedia turns an image message into its caption and the image,
// fetched only when the port keeps it (WhatsApp's URLs expire, so within
// the arrival; never for a denied sender or a redelivery), or no image
// and the caption with a note when it is declared too large. The download
// is bounded by ship.MaxImage whatever size the sender declared.
func imageMedia(img *waE2E.ImageMessage,
	download func(context.Context, whatsmeow.DownloadableMessage, whatsmeow.File) error) (*ship.Media, string) {
	caption := img.GetCaption()
	if img.GetFileLength() > ship.MaxImage {
		note := ship.MediaNote(ship.AttachmentImage, "too large to keep")
		if caption == "" {
			return nil, note
		}
		return nil, caption + "\n" + note
	}
	fetch := func(ctx context.Context) ([]byte, error) {
		ctx, cancel := context.WithTimeout(ctx, downloadTimeout)
		defer cancel()
		// The encrypted file is the image plus its padding and MAC.
		f := newMemFile(ship.MaxImage + encryptionOverhead)
		if err := download(ctx, img, f); errors.Is(err, errFileFull) {
			return nil, ship.ErrMediaTooLarge
		} else if err != nil {
			return nil, err
		}
		if len(f.Bytes()) > ship.MaxImage {
			return nil, ship.ErrMediaTooLarge
		}
		return f.Bytes(), nil
	}
	return &ship.Media{Kind: ship.AttachmentImage, Fetch: fetch}, caption
}

// messageText extracts the text of a message (captions included); media
// without a caption becomes a short marker so the agent knows it arrived.
func messageText(m *waE2E.Message) string {
	if m == nil {
		return ""
	}
	if t := m.GetConversation(); t != "" {
		return t
	}
	if t := m.GetExtendedTextMessage().GetText(); t != "" {
		return t
	}
	media := []struct {
		kind, caption string
		present       bool
	}{
		{"image", m.GetImageMessage().GetCaption(), m.GetImageMessage() != nil},
		{"video", m.GetVideoMessage().GetCaption(), m.GetVideoMessage() != nil},
		{"document", m.GetDocumentMessage().GetCaption(), m.GetDocumentMessage() != nil},
		{"audio", "", m.GetAudioMessage() != nil},
		{"sticker", "", m.GetStickerMessage() != nil},
		{"location", m.GetLocationMessage().GetName(), m.GetLocationMessage() != nil},
		{"contact", m.GetContactMessage().GetDisplayName(), m.GetContactMessage() != nil},
	}
	for _, md := range media {
		if !md.present {
			continue
		}
		if md.caption != "" {
			return fmt.Sprintf("[%s] %s", md.kind, md.caption)
		}
		return "[" + md.kind + " received; media is not supported yet]"
	}
	return ""
}
