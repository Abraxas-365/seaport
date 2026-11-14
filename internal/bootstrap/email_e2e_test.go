package bootstrap_test

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/Abraxas-365/seaport/internal/bootstrap"
	"github.com/Abraxas-365/seaport/internal/config"
	"github.com/Abraxas-365/seaport/internal/conversation"
	"github.com/Abraxas-365/seaport/internal/crew"
	"github.com/Abraxas-365/seaport/internal/dock"
	"github.com/Abraxas-365/seaport/internal/dock/adapters/dockemail"
	"github.com/Abraxas-365/seaport/internal/dock/adapters/dockemail/emailtest"
	"github.com/Abraxas-365/seaport/internal/query"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// An email dock: linked with a password checked over IMAP and SMTP (a
// refused one is a 422 and stores nothing), new mail wakes the crew
// (IDLE), a reply goes out by SMTP in the same thread, the customer's
// answer joins the thread (by In-Reply-To, and by subject when the client
// dropped the headers), a dropped connection is re-made, a password the
// mailbox stops accepting logs the dock out and unlinking forgets it.
func TestEmailDock(t *testing.T) {
	mail := emailtest.New(t, emailtest.Options{})
	mail.AddUser("bot@example.com", "app-password")
	mail.SetPassword("app-password")
	p := startPortWith(t, func(c *config.Server) {
		c.SecretKey = "email-test-secret-key-0123456789abcdef"
		c.EmailPrivate = true
	}, bootstrap.Options{Email: dockemail.Config{TLS: mail.ClientTLS(), Recheck: 200 * time.Millisecond}})

	var d dock.Dock
	if code := p.admin(http.MethodPost, "/api/v1/docks", map[string]string{"channel": "email", "name": "Support"}, &d); code != http.StatusCreated {
		t.Fatalf("create email dock: %d", code)
	}
	if d.Status != dock.StatusLinking {
		t.Fatalf("new email dock is %s", d.Status)
	}
	path := "/api/v1/docks/" + d.ID.String() + "/email"
	var info dockemail.Info
	if code := p.admin(http.MethodGet, path, nil, &info); code != http.StatusOK || info.Account != nil {
		t.Fatalf("info before linking: %d %+v", code, info)
	}
	api := p.apiDock("api")
	if code := p.admin(http.MethodGet, "/api/v1/docks/"+api.ID.String()+"/email", nil, nil); code != http.StatusUnprocessableEntity {
		t.Fatalf("email info of an api dock: %d", code)
	}

	form := func(edit func(m map[string]any)) map[string]any {
		m := map[string]any{"address": "bot@example.com", "display_name": "Support Bot",
			"imap":     map[string]any{"host": "127.0.0.1", "port": mail.IMAPPort, "tls": mail.IMAPTLS},
			"smtp":     map[string]any{"host": "127.0.0.1", "port": mail.SMTPPort, "tls": mail.SMTPTLS},
			"password": "app-password"}
		if edit != nil {
			edit(m)
		}
		return m
	}
	for name, c := range map[string]struct {
		edit func(m map[string]any)
		want int
	}{
		"wrong password":  {func(m map[string]any) { m["password"] = "nope" }, http.StatusUnprocessableEntity},
		"not an address":  {func(m map[string]any) { m["address"] = "not-an-address" }, http.StatusBadRequest},
		"plain imap":      {func(m map[string]any) { m["imap"].(map[string]any)["tls"] = "none" }, http.StatusBadRequest},
		"url as host":     {func(m map[string]any) { m["smtp"].(map[string]any)["host"] = "https://x.example/" }, http.StatusBadRequest},
		"no password":     {func(m map[string]any) { delete(m, "password") }, http.StatusBadRequest},
		"nobody listens":  {func(m map[string]any) { m["imap"].(map[string]any)["port"] = 1 }, http.StatusUnprocessableEntity},
		"unknown folder":  {func(m map[string]any) { m["folder"] = "Nope" }, http.StatusUnprocessableEntity},
		"unknown address": {func(m map[string]any) { m["username"] = "ghost@example.com" }, http.StatusUnprocessableEntity},
	} {
		if code := p.admin(http.MethodPut, path, form(c.edit), nil); code != c.want {
			t.Fatalf("%s: %d, want %d", name, code, c.want)
		}
	}
	if code := p.admin(http.MethodGet, path, nil, &info); code != http.StatusOK || info.Account != nil {
		t.Fatalf("a refused link stored something: %+v", info)
	}

	mail.Deliver(emailtest.Raw("old@example.com", "bot@example.com", "Before linking", "old1@example.com", "", "no backlog please"))
	if code := p.admin(http.MethodPut, path, form(nil), &info); code != http.StatusOK || info.Account == nil {
		t.Fatalf("link: %d %+v", code, info)
	}
	if a := info.Account; a.Address != "bot@example.com" || a.Folder != "INBOX" || a.Username != "bot@example.com" || a.DisplayName != "Support Bot" || strings.Contains(fmtJSON(info), "app-password") {
		t.Fatalf("account = %+v", a)
	}
	p.admin(http.MethodGet, "/api/v1/docks/"+d.ID.String(), nil, &d)
	if d.Status != dock.StatusLinked || d.Address != "bot@example.com" {
		t.Fatalf("linked dock: %s %q", d.Status, d.Address)
	}
	eventually(t, "the connection", func() bool {
		p.admin(http.MethodGet, path, nil, &info)
		return info.Account != nil && info.Account.Connected
	})

	p.assignAs(adminToken, d.ID, p.agent(testAgent).ID, true)
	p.letIn(d)

	// New mail wakes the crew; mail from before the link does not.
	mail.Deliver(emailtest.Raw("Ana <ana@example.com>", "bot@example.com", "Pedido 5", "a1@example.com", "", "Hola, mi pedido no llegó.\n\nOn Mon, 1 Jan 2026 at 10:00, Bot wrote:\n> old"))
	c := p.dialCrew(1)
	f := c.read()
	if f.Type != crew.FrameDelivery || len(f.Delivery.Ships) != 1 {
		t.Fatalf("delivery = %+v", f)
	}
	s := f.Delivery.Ships[0]
	if s.Text != "Subject: Pedido 5\n\nHola, mi pedido no llegó." || s.ExternalID != "a1@example.com" || s.SenderID != "ana@example.com" || s.SenderName != "Ana" {
		t.Fatalf("ship = %+v", s)
	}
	if conv := f.Delivery.Conversation; conv.ExternalID != "a1@example.com" || conv.Title != "Pedido 5" || conv.Kind != conversation.KindDirect {
		t.Fatalf("conversation = %+v", conv)
	}
	convID := f.Delivery.Conversation.ID
	ack(c, f)

	// The reply is an email in the thread.
	session := mcpSession(t, p, p.crewToken())
	if res, err := session.CallTool(t.Context(), &mcp.CallToolParams{Name: "reply",
		Arguments: map[string]any{"conversation_id": convID.String(), "text": "Hola Ana, lo miramos ahora."}}); err != nil || res.IsError {
		t.Fatalf("reply: %v %+v", err, res)
	}
	sent := mail.Sent()
	if len(sent) != 1 || sent[0].From != "bot@example.com" || len(sent[0].To) != 1 || sent[0].To[0] != "ana@example.com" {
		t.Fatalf("sent = %+v", sent)
	}
	for _, want := range []string{"Subject: Re: Pedido 5", "In-Reply-To: <a1@example.com>", "References: <a1@example.com>", "Hola Ana, lo miramos ahora.", "Message-Id: <seaport-"} {
		if !strings.Contains(sent[0].Data, want) {
			t.Fatalf("sent mail lacks %q:\n%s", want, sent[0].Data)
		}
	}
	ourID := between(sent[0].Data, "Message-Id: <", ">")

	// The customer answers our message: same conversation (In-Reply-To),
	// then once more with a client that dropped the headers (subject).
	mail.Deliver(emailtest.Raw("Ana <ana@example.com>", "bot@example.com", "Re: Pedido 5", "a2@example.com", ourID, "Gracias, sigo esperando."))
	f = c.read()
	if f.Type != crew.FrameDelivery || f.Delivery.Conversation.ID != convID || f.Delivery.Ships[0].Text != "Gracias, sigo esperando." {
		t.Fatalf("answer = %+v", f)
	}
	ack(c, f)
	mail.Deliver(emailtest.Raw("ana@example.com", "bot@example.com", "RE: Pedido 5", "a3@example.com", "", "¿Alguna novedad?"))
	f = c.read()
	if f.Type != crew.FrameDelivery || f.Delivery.Conversation.ID != convID || f.Delivery.Ships[0].Text != "¿Alguna novedad?" {
		t.Fatalf("headerless answer = %+v", f)
	}
	ack(c, f)

	// Not for the crew: our own mail, automatic mail, delivery reports.
	mail.Deliver(emailtest.Raw("bot@example.com", "ana@example.com", "Re: Pedido 5", "own1@example.com", "", "a copy of what we sent"))
	mail.Deliver("From: ana@example.com\r\nTo: bot@example.com\r\nSubject: Out of office\r\nMessage-ID: <auto1@example.com>\r\nAuto-Submitted: auto-replied\r\n\r\nI am away\r\n")
	mail.Deliver(emailtest.Raw("MAILER-DAEMON@example.com", "bot@example.com", "Undelivered", "dsn1@example.com", "", "bounce"))
	// A new thread from someone else; it is the next thing the crew sees.
	mail.Deliver(emailtest.Raw("Luis <luis@example.com>", "bot@example.com", "Factura", "l1@example.com", "", "Necesito una factura."))
	f = c.read()
	if f.Type != crew.FrameDelivery || f.Delivery.Conversation.ID == convID || f.Delivery.Ships[0].ExternalID != "l1@example.com" {
		t.Fatalf("new thread = %+v", f)
	}
	ack(c, f)

	// A dropped connection is re-made and nothing is missed or repeated.
	mail.KillIMAP()
	mail.Deliver(emailtest.Raw("ana@example.com", "bot@example.com", "Re: Pedido 5", "a4@example.com", "", "Después de la caída"))
	f = c.read()
	if f.Type != crew.FrameDelivery || f.Delivery.Conversation.ID != convID || f.Delivery.Ships[0].ExternalID != "a4@example.com" {
		t.Fatalf("after reconnect = %+v", f)
	}
	ack(c, f)
	var convs query.Paginated[conversation.Conversation]
	p.admin(http.MethodGet, "/api/v1/conversations?dock_id="+d.ID.String(), nil, &convs)
	if len(convs.Items) != 2 {
		t.Fatalf("conversations = %d, want 2", len(convs.Items))
	}

	// A mailbox that stops accepting the password logs the dock out.
	mail.SetPassword("rotated")
	mail.KillIMAP()
	eventually(t, "the dock to log out", func() bool {
		p.admin(http.MethodGet, "/api/v1/docks/"+d.ID.String(), nil, &d)
		return d.Status == dock.StatusLoggedOut
	})
	p.admin(http.MethodGet, path, nil, &info)
	if info.Account == nil || info.Account.Connected || !strings.Contains(info.Account.LastError, "refused") {
		t.Fatalf("after logout: %+v", info.Account)
	}
	// Linking again with the new password starts the session again where
	// it was left.
	if code := p.admin(http.MethodPut, path, form(func(m map[string]any) { m["password"] = "rotated" }), &info); code != http.StatusOK {
		t.Fatalf("relink: %d", code)
	}
	eventually(t, "the dock to connect again", func() bool {
		p.admin(http.MethodGet, path, nil, &info)
		return info.Account != nil && info.Account.Connected
	})
	mail.Deliver(emailtest.Raw("ana@example.com", "bot@example.com", "Re: Pedido 5", "a5@example.com", "", "Ya estoy de vuelta"))
	f = c.read()
	if f.Type != crew.FrameDelivery || f.Delivery.Ships[0].ExternalID != "a5@example.com" {
		t.Fatalf("after relink = %+v", f)
	}
	ack(c, f)

	// Unlinking forgets the mailbox.
	if code := p.admin(http.MethodPost, "/api/v1/docks/"+d.ID.String()+"/unlink", nil, &d); code != http.StatusOK || d.Status != dock.StatusLoggedOut {
		t.Fatalf("unlink: %d %s", code, d.Status)
	}
	if code := p.admin(http.MethodGet, path, nil, &info); code != http.StatusOK || info.Account != nil {
		t.Fatalf("after unlink: %d %+v", code, info)
	}
}

func between(s, a, b string) string {
	_, rest, _ := strings.Cut(s, a)
	v, _, _ := strings.Cut(rest, b)
	return v
}
