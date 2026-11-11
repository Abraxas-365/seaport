package dockjira

import (
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/Abraxas-365/seaport/internal/agent"
	"github.com/Abraxas-365/seaport/internal/dock"
	"github.com/Abraxas-365/seaport/internal/errx"
	"github.com/Abraxas-365/seaport/internal/httpx"
	"github.com/Abraxas-365/seaport/internal/identity"
	"github.com/Abraxas-365/seaport/internal/permission"
	"github.com/Abraxas-365/seaport/internal/text"
)

// WebhookPath is where Jira (an admin webhook or an Automation rule)
// calls this port; the dock id follows.
const WebhookPath = "/jira/webhook/"

// MaxWebhook bounds a webhook body (an issue with its fields is a few
// tens of KiB).
const MaxWebhook = 5 << 20

// Limits of the link form (Atlassian's own are well inside them).
const (
	maxEmail = 254
	maxToken = 2048
)

// Handler serves the Jira routes: the admin API of a Jira dock and the
// public webhook.
type Handler struct {
	g     *Gateway
	docks dock.Queries
}

func NewHandler(g *Gateway, docks dock.Queries) *Handler { return &Handler{g: g, docks: docks} }

// Register mounts the admin routes (behind admin auth).
func (h *Handler) Register(mux *http.ServeMux) {
	mux.Handle("GET /api/v1/docks/{id}/jira", httpx.RequireFunc(permission.DocksRead, h.info))
	mux.Handle("PUT /api/v1/docks/{id}/jira", httpx.RequireFunc(permission.DocksWrite, h.link))
	mux.Handle("POST /api/v1/docks/{id}/jira/secret", httpx.RequireFunc(permission.DocksWrite, h.rotate))
}

// RegisterPublic mounts the webhook Jira reaches without a Seaport
// credential: it checks the dock's webhook secret.
func (h *Handler) RegisterPublic(mux *http.ServeMux) {
	mux.Handle("POST /jira/webhook/{dock}", httpx.Handle(h.webhook))
}

// jiraDock finds the caller's dock and checks it is a Jira one.
func (h *Handler) jiraDock(r *http.Request) (dock.Dock, error) {
	org, err := httpx.Organization(r)
	if err != nil {
		return dock.Dock{}, err
	}
	id, err := identity.ParseDockID(r.PathValue("id"))
	if err != nil {
		return dock.Dock{}, err
	}
	d, err := h.docks.Find(r.Context(), org, id)
	if err != nil {
		return dock.Dock{}, err
	}
	if d.Channel != dock.ChannelJira {
		return dock.Dock{}, errx.Business("not a Jira dock")
	}
	return d, nil
}

// SiteInfo is a Jira dock's site as the admin API shows it (no token).
type SiteInfo struct {
	Site        string    `json:"site"`
	AccountID   string    `json:"account_id"`
	DisplayName string    `json:"display_name"`
	Email       string    `json:"email"`
	CreatedAt   time.Time `json:"created_at"`
}

// Info is the Jira side of a dock.
type Info struct {
	Site *SiteInfo `json:"site"`
	// WebhookURL is where Jira must reach this port; Reachable is false
	// when it can't be on the internet (loopback, plain http).
	WebhookURL string `json:"webhook_url"`
	Reachable  bool   `json:"reachable"`
	// Secret is the webhook secret, shown only by the answers that made it
	// (linking, rotating): it is stored sealed and never read back.
	Secret string `json:"secret,omitempty"`
	// Hosts are the sites a dock may name (the form's hint).
	Hosts []string `json:"hosts"`
}

func (h *Handler) info(w http.ResponseWriter, r *http.Request) error {
	d, err := h.jiraDock(r)
	if err != nil {
		return err
	}
	out := h.base(d.ID)
	s, err := h.g.store.Site(r.Context(), d.ID)
	switch {
	case errx.IsType(err, errx.TypeNotFound):
		return httpx.JSON(w, http.StatusOK, out)
	case err != nil:
		return err
	}
	out.Site = siteInfo(s)
	return httpx.JSON(w, http.StatusOK, out)
}

func (h *Handler) base(id identity.DockID) Info {
	return Info{WebhookURL: h.g.cfg.PublicURL + WebhookPath + id.String(), Reachable: reachable(h.g.cfg.PublicURL), Hosts: h.g.cfg.Hosts}
}

func siteInfo(s Site) *SiteInfo {
	return &SiteInfo{Site: s.URL, AccountID: s.AccountID, DisplayName: s.DisplayName, Email: s.Email, CreatedAt: s.CreatedAt}
}

// reachable: Jira can call this URL (https, not loopback).
func reachable(public string) bool {
	u, err := url.Parse(public)
	if err != nil || u.Scheme != "https" {
		return false
	}
	host := strings.ToLower(u.Hostname())
	return host != "localhost" && !strings.HasPrefix(host, "127.") && host != "::1"
}

type linkRequest struct {
	Site     string `json:"site"`
	Email    string `json:"email"`
	APIToken string `json:"api_token"`
}

// link checks the account with the site and stores it: the dock is linked.
func (h *Handler) link(w http.ResponseWriter, r *http.Request) error {
	d, err := h.jiraDock(r)
	if err != nil {
		return err
	}
	var req linkRequest
	if err := httpx.DecodeJSON(r, &req); err != nil {
		return err
	}
	site, err := h.g.checkSite(req.Site)
	if err != nil {
		return err
	}
	req.Email, req.APIToken = text.Clean(req.Email), strings.TrimSpace(req.APIToken)
	if err := text.Required("email", req.Email, maxEmail); err != nil {
		return err
	}
	if !strings.Contains(req.Email, "@") || strings.ContainsAny(req.Email, " :") {
		return errx.InvalidField("email", "email must be the account's email address")
	}
	if err := text.Required("api_token", req.APIToken, maxToken); err != nil {
		return err
	}
	account, name, err := h.g.client.myself(r.Context(), site, req.Email, req.APIToken)
	if err != nil {
		if refusedCredentials(err) {
			return errx.Business("Jira refused these credentials: check the email and the API token, and that the account can use the site")
		}
		slog.Warn("check jira account", "dock", d.ID, "err", err)
		return wrapped(err, "check the jira account")
	}
	secret, err := h.g.store.Save(r.Context(), d.Organization, d.ID, site, account, name, req.Email, req.APIToken)
	if err != nil {
		return err
	}
	if links, _, _ := h.g.bound(); links != nil {
		if err := links.SetLinked(r.Context(), d.ID, hostOf(site)); err != nil {
			return err
		}
	}
	s, err := h.g.store.Site(r.Context(), d.ID)
	if err != nil {
		return err
	}
	slog.Info("jira dock linked", "dock", d.ID, "site", site)
	out := h.base(d.ID)
	out.Site, out.Secret = siteInfo(s), secret
	return httpx.JSON(w, http.StatusOK, out)
}

// rotate replaces the webhook secret and shows the new one once.
func (h *Handler) rotate(w http.ResponseWriter, r *http.Request) error {
	d, err := h.jiraDock(r)
	if err != nil {
		return err
	}
	secret, err := h.g.store.RotateSecret(r.Context(), d.Organization, d.ID)
	if err != nil {
		return err
	}
	out := h.base(d.ID)
	out.Secret = secret
	if s, err := h.g.store.Site(r.Context(), d.ID); err == nil {
		out.Site = siteInfo(s)
	}
	return httpx.JSON(w, http.StatusOK, out)
}

// checkSite validates the site a dock names against the allow-list and
// returns it normalized (https://host, no trailing slash): the port calls
// this address with the account's credentials, so it must be one the
// operator allowed.
func (g *Gateway) checkSite(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", errx.InvalidField("site", "site is required")
	}
	if !strings.Contains(raw, "://") {
		raw = "https://" + raw
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || u.User != nil || (u.Path != "" && u.Path != "/") || u.RawQuery != "" || u.Fragment != "" {
		return "", errx.InvalidField("site", "site must be the address of your Jira site, such as https://example.atlassian.net")
	}
	host := strings.ToLower(u.Hostname())
	switch {
	case u.Scheme == "https":
	case u.Scheme == "http" && loopback(host):
		// A fake or local Jira, only when the operator allowed that host.
	default:
		return "", errx.InvalidField("site", "site must use https")
	}
	if !allowed(g.cfg.Hosts, strings.ToLower(u.Host), host) {
		return "", errx.InvalidField("site", "this Jira site is not allowed on this port (allowed: "+strings.Join(g.cfg.Hosts, ", ")+"; the operator sets SEAPORT_JIRA_HOSTS)")
	}
	return u.Scheme + "://" + strings.ToLower(u.Host), nil
}

func loopback(host string) bool {
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// allowed: host (or host:port) is listed, exactly or as a subdomain of a
// "*.domain" entry.
func allowed(hosts []string, hostport, host string) bool {
	for _, h := range hosts {
		h = strings.ToLower(strings.TrimSpace(h))
		if suffix, ok := strings.CutPrefix(h, "*."); ok {
			if strings.HasSuffix(host, "."+suffix) && len(host) > len(suffix)+1 && !strings.Contains(strings.TrimSuffix(host, "."+suffix), "..") {
				return true
			}
			continue
		}
		if h != "" && (h == hostport || h == host) {
			return true
		}
	}
	return false
}

// webhook takes Jira's events for one dock, proven by its webhook secret
// (the HMAC of an admin webhook, or the bearer of an Automation rule).
func (h *Handler) webhook(w http.ResponseWriter, r *http.Request) error {
	id, err := identity.ParseDockID(r.PathValue("dock"))
	if err != nil {
		return errx.NotFound("unknown webhook")
	}
	s, err := h.g.store.Site(r.Context(), id)
	if errx.IsType(err, errx.TypeNotFound) {
		return errx.NotFound("unknown webhook")
	}
	if err != nil {
		return err
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, MaxWebhook+1))
	if err != nil {
		return errx.Validation("could not read request body")
	}
	if len(body) > MaxWebhook {
		return errx.PayloadTooLarge("webhook body is too large")
	}
	sig := r.Header.Get("X-Hub-Signature")
	if sig == "" {
		sig = r.Header.Get("X-Hub-Signature-256")
	}
	if !validSignature(s.webhookSecret, body, sig) && !validBearer(s.webhookSecret, r.Header.Get("Authorization")) {
		return errx.Unauthorized("bad webhook signature")
	}
	_, receiver, teams := h.g.bound()
	if receiver == nil {
		return errx.External("this port is starting; try again")
	}
	var team []agent.Member
	if teams != nil {
		if team, err = teams.Team(r.Context(), s.Organization, s.Dock); err != nil {
			return err
		}
	}
	at, ok := parse(body, s.Dock, s, team)
	if !ok {
		return httpx.JSON(w, http.StatusAccepted, map[string]string{"status": "ignored"})
	}
	if at.in.Own {
		// A comment Seaport sent carries its message id: the stored reply.
		stamp, err := h.g.client.stamp(r.Context(), s, at.commentID)
		switch {
		case err == nil && strings.HasPrefix(stamp, "jira-") && len(stamp) <= 64 && !strings.ContainsAny(stamp, " \n"):
			at.in.ExternalID = stamp
		case err != nil:
			if refusedCredentials(err) {
				return h.g.failed(r.Context(), s, err, "read the jira comment")
			}
			// Without its stamp Seaport's own reply would be recorded
			// twice: let Jira deliver the webhook again.
			slog.Warn("read jira comment stamp", "dock", s.Dock, "comment", at.commentID, "err", err)
			return wrapped(err, "read the jira comment")
		}
	}
	st, created, err := receiver.Receive(r.Context(), s.Organization, at.in)
	if err != nil {
		slog.Error("receive jira message", "dock", s.Dock, "chat", at.in.ChatID, "err", err)
		return err
	}
	status := "received"
	if at.in.Own {
		status = "recorded"
	}
	return httpx.JSON(w, http.StatusAccepted, map[string]any{"status": status, "ship_id": st.ID, "created": created})
}
