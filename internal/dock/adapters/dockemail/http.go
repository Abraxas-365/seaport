package dockemail

import (
	"context"
	"crypto/tls"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/Abraxas-365/seaport/internal/dock"
	"github.com/Abraxas-365/seaport/internal/errx"
	"github.com/Abraxas-365/seaport/internal/httpx"
	"github.com/Abraxas-365/seaport/internal/identity"
	"github.com/Abraxas-365/seaport/internal/permission"
	"github.com/Abraxas-365/seaport/internal/text"
	"github.com/gofiber/fiber/v3"
)

// Limits of the link form.
const (
	maxHost     = 253
	maxUsername = 254
	maxPassword = 1024
	maxFolder   = 200
	checkWithin = 45 * time.Second
)

// Handler serves the admin API of an email dock. There are no public
// routes: mail is read by the owner process over IMAP, not pushed.
type Handler struct {
	g     *Gateway
	docks dock.Queries
	cmds  dock.Commands
}

func NewHandler(g *Gateway, docks dock.Queries, cmds dock.Commands) *Handler {
	return &Handler{g: g, docks: docks, cmds: cmds}
}

// Register mounts the admin routes (behind admin auth).
func (h *Handler) Register(r httpx.Router) {
	r.Get("/api/v1/docks/:id/email", httpx.RequireFunc(permission.DocksRead, h.info))
	r.Put("/api/v1/docks/:id/email", httpx.RequireFunc(permission.DocksWrite, h.link))
}

func (h *Handler) emailDock(c fiber.Ctx) (dock.Dock, error) {
	org, err := httpx.Organization(c)
	if err != nil {
		return dock.Dock{}, err
	}
	id, err := identity.ParseDockID(httpx.Param(c, "id"))
	if err != nil {
		return dock.Dock{}, err
	}
	d, err := h.docks.Find(c.Context(), org, id)
	if err != nil {
		return dock.Dock{}, err
	}
	if d.Channel != dock.ChannelEmail {
		return dock.Dock{}, errx.Business("not an email dock")
	}
	return d, nil
}

// AccountInfo is a dock's mailbox as the admin API shows it (no password).
type AccountInfo struct {
	Address     string     `json:"address"`
	DisplayName string     `json:"display_name"`
	IMAP        Endpoint   `json:"imap"`
	SMTP        Endpoint   `json:"smtp"`
	Username    string     `json:"username"`
	Folder      string     `json:"folder"`
	Connected   bool       `json:"connected"`
	LastError   string     `json:"last_error,omitempty"`
	SeenAt      *time.Time `json:"seen_at,omitempty"`
	CreatedAt   time.Time  `json:"created_at"`
}

// Info is the email side of a dock.
type Info struct {
	Account *AccountInfo `json:"account"`
}

func info(a Account) *AccountInfo {
	return &AccountInfo{Address: a.Address, DisplayName: a.DisplayName, IMAP: a.IMAP, SMTP: a.SMTP, Username: a.Username,
		Folder: a.Folder, Connected: a.Connected, LastError: a.LastError, SeenAt: a.SeenAt, CreatedAt: a.CreatedAt}
}

func (h *Handler) info(c fiber.Ctx) error {
	d, err := h.emailDock(c)
	if err != nil {
		return err
	}
	a, err := h.g.store.Find(c.Context(), d.ID)
	switch {
	case errx.IsType(err, errx.TypeNotFound):
		return httpx.JSON(c, http.StatusOK, Info{})
	case err != nil:
		return err
	}
	return httpx.JSON(c, http.StatusOK, Info{Account: info(a)})
}

// LinkRequest is the link form: both servers, the account and its password
// (an app password where the provider asks for one).
type LinkRequest struct {
	Address     string   `json:"address"`
	DisplayName string   `json:"display_name"`
	IMAP        Endpoint `json:"imap"`
	SMTP        Endpoint `json:"smtp"`
	// Username defaults to the address.
	Username string `json:"username"`
	Password string `json:"password"`
	// Folder defaults to INBOX.
	Folder string `json:"folder"`
}

func (r LinkRequest) validate(hosts []string) (Account, error) {
	var a Account
	addr, err := parseAddress(r.Address)
	if err != nil || addr.Name != "" {
		return a, errx.InvalidField("address", "address must be an email address, such as ana@example.com")
	}
	a.Address = strings.ToLower(addr.Address)
	if err := text.Required("address", a.Address, dock.MaxAddress); err != nil {
		return a, err
	}
	if err := text.Optional("display_name", r.DisplayName, text.MaxName); err != nil {
		return a, err
	}
	a.DisplayName = text.Clean(r.DisplayName)
	if strings.ContainsAny(a.DisplayName, "\r\n") {
		return a, errx.InvalidField("display_name", "display_name must be one line")
	}
	if a.IMAP, err = r.IMAP.validate("imap", hosts); err != nil {
		return a, err
	}
	if a.SMTP, err = r.SMTP.validate("smtp", hosts); err != nil {
		return a, err
	}
	a.Username = text.Clean(r.Username)
	if a.Username == "" {
		a.Username = a.Address
	}
	if err := text.Required("username", a.Username, maxUsername); err != nil {
		return a, err
	}
	if r.Password == "" {
		return a, errx.InvalidField("password", "password is required")
	}
	if len(r.Password) > maxPassword || text.HasForbiddenControl(r.Password) {
		return a, errx.InvalidField("password", "password is not valid")
	}
	a.password = r.Password
	a.Folder = text.Clean(r.Folder)
	if a.Folder == "" {
		a.Folder = "INBOX"
	}
	if err := text.Required("folder", a.Folder, maxFolder); err != nil {
		return a, err
	}
	return a, nil
}

func (e Endpoint) validate(field string, hosts []string) (Endpoint, error) {
	e.Host = strings.ToLower(strings.TrimSpace(e.Host))
	switch {
	case e.Host == "" || len(e.Host) > maxHost || !validHost(e.Host):
		return e, errx.InvalidField(field+".host", field+".host must be a server name such as imap.example.com (no scheme, port or path)")
	case !hostAllowed(hosts, e.Host):
		return e, errx.InvalidField(field+".host", field+".host is not allowed on this port (allowed: "+strings.Join(hosts, ", ")+"; the operator sets SEAPORT_EMAIL_HOSTS)")
	case e.Port < 1 || e.Port > 65535:
		return e, errx.InvalidField(field+".port", field+".port must be between 1 and 65535")
	case e.TLS != TLS && e.TLS != StartTLS:
		return e, errx.InvalidField(field+".tls", field+".tls must be tls or starttls: plain connections are not allowed")
	}
	return e, nil
}

// validHost: a DNS name or an IP literal, nothing else.
func validHost(h string) bool {
	if net.ParseIP(h) != nil {
		return true
	}
	for _, r := range h {
		if !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '-' || r == '.') {
			return false
		}
	}
	return !strings.HasPrefix(h, ".") && !strings.HasPrefix(h, "-") && !strings.Contains(h, "..")
}

// link checks the mailbox (IMAP login and folder, SMTP login), stores it
// and starts the dock's session on its owner. A password the server
// refuses is a 422 saying so; nothing is stored then.
func (h *Handler) link(c fiber.Ctx) error {
	d, err := h.emailDock(c)
	if err != nil {
		return err
	}
	var req LinkRequest
	if err := httpx.DecodeJSON(c, &req); err != nil {
		return err
	}
	a, err := req.validate(h.g.cfg.Hosts)
	if err != nil {
		return err
	}
	a.Dock, a.Organization = d.ID, d.Organization
	ctx, cancel := context.WithTimeout(c.Context(), checkWithin)
	defer cancel()
	start, err := h.g.cfg.Check(ctx, a)
	if err != nil {
		return refused(err)
	}
	if err := h.g.store.Save(c.Context(), a, start); err != nil {
		return err
	}
	if d.Status != dock.StatusLinked {
		// Starts the session where the dock's owner runs (and marks it linked).
		if _, err := h.cmds.Relink(c.Context(), d.Organization, d.ID); err != nil {
			return err
		}
	}
	saved, err := h.g.store.Find(c.Context(), d.ID)
	if err != nil {
		return err
	}
	return httpx.JSON(c, http.StatusOK, Info{Account: info(saved)})
}

// refused turns what a check found into the answer the form shows. What
// the remote server said (its banner, its refusal text) is logged, not
// handed back: a tenant could use it to read what an internal host says.
func refused(err error) error {
	slog.Info("email check failed", "err", short(err.Error()))
	var le *loginError
	var cert *tls.CertificateVerificationError
	var nerr net.Error
	switch {
	case errors.As(err, &le):
		return errx.Business("the mailbox refused the password or username; with Gmail, Outlook or iCloud use an app password")
	case errors.Is(err, errPrivate):
		return errx.Business("that server address is private or local: this port only connects to public mail servers (the operator can allow others with SEAPORT_EMAIL_PRIVATE)")
	case errors.Is(err, errHost):
		return errx.Business("that mail server is not allowed on this port (the operator sets SEAPORT_EMAIL_HOSTS)")
	case errors.As(err, &cert):
		return errx.Business("the mail server's certificate could not be verified")
	case errors.As(err, &nerr) && nerr.Timeout() || errors.Is(err, context.DeadlineExceeded):
		return errx.Business("the mail server did not answer in time")
	}
	return errx.Business("could not connect to the mailbox: check the servers, ports and folder")
}

func short(s string) string {
	s = strings.Join(strings.Fields(strings.ToValidUTF8(s, "")), " ")
	if len(s) > 240 {
		s = s[:240]
	}
	return s
}
