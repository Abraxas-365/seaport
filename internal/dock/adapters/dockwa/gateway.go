// Package dockwa is the WhatsApp gateway: each dock is a whatsmeow
// "linked device" (multi-device protocol, no browser). Its signal store
// lives in the same Postgres (whatsmeow_* tables).
package dockwa

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Abraxas-365/seaport/internal/conversation"
	"github.com/Abraxas-365/seaport/internal/dock"
	"github.com/Abraxas-365/seaport/internal/errx"
	"github.com/Abraxas-365/seaport/internal/identity"
	"github.com/Abraxas-365/seaport/internal/ship"
	_ "github.com/jackc/pgx/v5/stdlib" // database/sql driver "pgx" for the whatsmeow store
	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/store"
	"go.mau.fi/whatsmeow/store/sqlstore"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	"google.golang.org/protobuf/proto"
)

const (
	// minSendGap spaces outbound messages per dock (anti-ban).
	minSendGap = 1200 * time.Millisecond
	// handlerTimeout bounds the work done for one WhatsApp event.
	handlerTimeout = 30 * time.Second
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
	devices *sqlstore.Container

	mu       sync.Mutex
	clients  map[identity.DockID]*session
	receiver Receiver
	links    LinkSink
}

type session struct {
	dock identity.DockID
	// organization owns the dock; its messages arrive in it.
	organization identity.OrganizationID
	client       *whatsmeow.Client

	// unlinked: the owner ended this session; its pairing goroutine and
	// events must not report a status any more.
	unlinked atomic.Bool

	mu       sync.Mutex
	qr       string
	lastSend time.Time
	titles   map[types.JID]string
}

// Open connects the whatsmeow store to Postgres and upgrades its tables.
func Open(ctx context.Context, databaseURL string) (*Gateway, error) {
	container, err := sqlstore.New(ctx, "pgx", databaseURL, newLogger("store"))
	if err != nil {
		return nil, errx.Wrap(err, "open whatsapp store", errx.TypeInternal)
	}
	return &Gateway{devices: container, clients: map[identity.DockID]*session{}}, nil
}

// Bind sets the domain callbacks (they are built after the gateway).
func (g *Gateway) Bind(links LinkSink, receiver Receiver) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.links, g.receiver = links, receiver
}

var _ dock.Gateway = (*Gateway)(nil)

func (g *Gateway) Channel() dock.Channel { return dock.ChannelWhatsApp }

// Close disconnects every dock.
func (g *Gateway) Close() {
	g.mu.Lock()
	defer g.mu.Unlock()
	for _, s := range g.clients {
		s.client.Disconnect()
	}
	_ = g.devices.Close()
}

func (g *Gateway) Link(ctx context.Context, d dock.Dock) (dock.Status, error) {
	g.mu.Lock()
	if old, ok := g.clients[d.ID]; ok {
		old.client.Disconnect()
		delete(g.clients, d.ID)
	}
	g.mu.Unlock()

	device := g.devices.NewDevice()
	s := g.newSession(d, device)
	qrCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), linkTimeout)
	qrs, err := s.client.GetQRChannel(qrCtx)
	if err != nil {
		cancel()
		return "", errx.Wrap(err, "start whatsapp pairing", errx.TypeExternal)
	}
	if err := s.client.Connect(); err != nil {
		cancel()
		return "", errx.Wrap(err, "connect to whatsapp", errx.TypeExternal)
	}
	first := make(chan struct{})
	go func() {
		defer cancel()
		var once sync.Once
		for item := range qrs {
			if s.unlinked.Load() {
				continue
			}
			switch item.Event {
			case whatsmeow.QRChannelEventCode:
				s.setQR(item.Code)
				once.Do(func() { close(first) })
				g.pairingChanged(d.ID)
			case whatsmeow.QRChannelSuccess.Event:
				s.setQR("")
			default:
				// timeout, error or scanned without multidevice
				s.setQR("")
				slog.Warn("whatsapp pairing ended", "dock", d.ID, "event", item.Event, "err", item.Error)
				if !s.client.IsLoggedIn() {
					g.setStatus(d.ID, dock.StatusFailed)
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
	g.mu.Lock()
	g.clients[d.ID] = s
	g.mu.Unlock()
	return dock.StatusLinking, nil
}

func (g *Gateway) Restore(ctx context.Context, d dock.Dock) error {
	jid, err := types.ParseJID(d.Address)
	if err != nil {
		return errx.Wrap(err, "parse dock address", errx.TypeInternal)
	}
	device, err := g.devices.GetDevice(ctx, jid)
	if err != nil {
		return errx.Wrap(err, "load whatsapp device", errx.TypeInternal)
	}
	if device == nil {
		g.setStatus(d.ID, dock.StatusLoggedOut)
		return errx.NotFound("whatsapp device of dock " + d.ID.String() + " is gone; link it again")
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
	g.mu.Lock()
	s := g.clients[d.ID]
	delete(g.clients, d.ID)
	g.mu.Unlock()
	if s != nil {
		s.unlinked.Store(true)
		s.setQR("")
		if s.client.IsLoggedIn() {
			if err := s.client.Logout(ctx); err != nil {
				s.client.Disconnect()
				return errx.Wrap(err, "log out of whatsapp", errx.TypeExternal)
			}
			slog.Info("whatsapp dock logged out by owner", "dock", d.ID)
			return nil
		}
		s.client.Disconnect()
	}
	// Not connected: forget stored keys so a relink starts clean.
	if d.Address == "" {
		return nil
	}
	jid, err := types.ParseJID(d.Address)
	if err != nil {
		return nil
	}
	device, err := g.devices.GetDevice(ctx, jid)
	if err != nil {
		return errx.Wrap(err, "load whatsapp device", errx.TypeInternal)
	}
	if device != nil {
		if err := device.Delete(ctx); err != nil {
			return errx.Wrap(err, "delete whatsapp device", errx.TypeInternal)
		}
	}
	return nil
}

func (g *Gateway) QR(id identity.DockID) string {
	s := g.session(id)
	if s == nil {
		return ""
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.qr
}

func (g *Gateway) NewMessageID(id identity.DockID) (string, error) {
	s := g.session(id)
	if s == nil {
		return "", errx.Business("whatsapp dock is not connected")
	}
	return string(s.client.GenerateMessageID()), nil
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
	s.pace(ctx)
	_, err = s.client.SendMessage(ctx, to, &waE2E.Message{Conversation: proto.String(msg.Text)},
		whatsmeow.SendRequestExtra{ID: types.MessageID(msg.MessageID)})
	if err != nil {
		return errx.Wrap(err, "send whatsapp message", errx.TypeExternal)
	}
	return nil
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

func (s *session) setQR(code string) {
	s.mu.Lock()
	s.qr = code
	s.mu.Unlock()
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
	if s.unlinked.Load() {
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
		g.setStatus(s.dock, dock.StatusLoggedOut)
		g.mu.Lock()
		delete(g.clients, s.dock)
		g.mu.Unlock()
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
	if info.IsFromMe && !self {
		return ship.Inbound{}, false
	}
	body := messageText(e.Message)
	if body == "" {
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
