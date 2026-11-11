package dockgithub

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/Abraxas-365/seaport/internal/agent"
	"github.com/Abraxas-365/seaport/internal/dock"
	"github.com/Abraxas-365/seaport/internal/errx"
	"github.com/Abraxas-365/seaport/internal/httpx"
	"github.com/Abraxas-365/seaport/internal/identity"
	"github.com/Abraxas-365/seaport/internal/permission"
	"github.com/Abraxas-365/seaport/internal/ship"
	"github.com/Abraxas-365/seaport/internal/text"
)

// Public paths GitHub calls or sends browsers to.
const (
	CreatedPath   = "/github/apps/created"
	InstalledPath = "/github/apps/installed"
	WebhookPath   = "/github/webhook/"
)

// MaxWebhook bounds a webhook body (issue and comment payloads are a few
// tens of KiB; GitHub caps them at 25 MiB).
const MaxWebhook = 5 << 20

// stateTTL bounds a manifest or install flow started from the web.
const stateTTL = time.Hour

// Handler serves the GitHub routes: the admin API of a GitHub dock and
// the public endpoints of the App flows and webhooks.
type Handler struct {
	g     *Gateway
	docks dock.Queries
}

func NewHandler(g *Gateway, docks dock.Queries) *Handler { return &Handler{g: g, docks: docks} }

// Register mounts the admin routes (behind admin auth).
func (h *Handler) Register(mux *http.ServeMux) {
	mux.Handle("GET /api/v1/docks/{id}/github", httpx.RequireFunc(permission.DocksRead, h.info))
	mux.Handle("POST /api/v1/docks/{id}/github/manifest", httpx.RequireFunc(permission.DocksWrite, h.manifest))
}

// RegisterPublic mounts the routes GitHub reaches without a Seaport
// credential: each checks its own proof (a sealed state, a signature).
func (h *Handler) RegisterPublic(mux *http.ServeMux) {
	mux.Handle("GET /github/apps/created", httpx.Handle(h.created))
	mux.Handle("GET /github/apps/installed", httpx.Handle(h.installed))
	mux.Handle("POST /github/webhook/{dock}", httpx.Handle(h.webhook))
}

// githubDock finds the caller's dock and checks it is a GitHub one.
func (h *Handler) githubDock(r *http.Request) (dock.Dock, error) {
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
	if d.Channel != dock.ChannelGitHub {
		return dock.Dock{}, errx.Business("not a GitHub dock")
	}
	return d, nil
}

// AppInfo is a GitHub dock's App as the admin API shows it (no secret).
type AppInfo struct {
	AppID     int64     `json:"app_id"`
	Slug      string    `json:"slug"`
	Name      string    `json:"name"`
	Owner     string    `json:"owner"`
	OwnerType string    `json:"owner_type"`
	HTMLURL   string    `json:"html_url"`
	CreatedAt time.Time `json:"created_at"`
}

// Info is the GitHub side of a dock.
type Info struct {
	App           *AppInfo       `json:"app"`
	Installations []Installation `json:"installations"`
	// WebhookURL is where GitHub must reach this port; Reachable is false
	// when it can't be on the internet (loopback, plain http).
	WebhookURL string `json:"webhook_url"`
	Reachable  bool   `json:"reachable"`
	// InstallURL installs the App on more accounts (with a fresh state).
	InstallURL string `json:"install_url,omitempty"`
}

func (h *Handler) info(w http.ResponseWriter, r *http.Request) error {
	d, err := h.githubDock(r)
	if err != nil {
		return err
	}
	out := Info{Installations: []Installation{}, WebhookURL: h.g.cfg.PublicURL + WebhookPath + d.ID.String(),
		Reachable: reachable(h.g.cfg.PublicURL)}
	a, err := h.g.store.App(r.Context(), d.ID)
	switch {
	case errx.IsType(err, errx.TypeNotFound):
		return httpx.JSON(w, http.StatusOK, out)
	case err != nil:
		return err
	}
	out.App = &AppInfo{AppID: a.AppID, Slug: a.Slug, Name: a.Name, Owner: a.Owner, OwnerType: a.OwnerType, HTMLURL: a.HTMLURL, CreatedAt: a.CreatedAt}
	if out.Installations, err = h.g.store.Installations(r.Context(), d.ID); err != nil {
		return err
	}
	out.InstallURL = h.installURL(a, d)
	return httpx.JSON(w, http.StatusOK, out)
}

func (h *Handler) installURL(a App, d dock.Dock) string {
	return strings.TrimRight(a.HTMLURL, "/") + "/installations/new?state=" + url.QueryEscape(h.g.state(d.Organization, d.ID, "install"))
}

// reachable: GitHub can call this URL (https, not loopback).
func reachable(public string) bool {
	u, err := url.Parse(public)
	if err != nil || u.Scheme != "https" {
		return false
	}
	host := strings.ToLower(u.Hostname())
	return host != "localhost" && !strings.HasPrefix(host, "127.") && host != "::1"
}

type manifestRequest struct {
	// Organization owns the App (empty: the user who creates it).
	Organization string `json:"organization"`
	// Name prefills the App's name (GitHub requires it to be unique).
	Name string `json:"name"`
}

// Manifest is the form the web posts to GitHub to register the App.
type Manifest struct {
	ActionURL string `json:"action_url"`
	Manifest  string `json:"manifest"`
}

// login is a GitHub user or organization name.
var login = regexp.MustCompile(`^[A-Za-z0-9](?:[A-Za-z0-9]|-[A-Za-z0-9]){0,38}$`)

// maxAppName is GitHub's limit on an App's name.
const maxAppName = 34

func (h *Handler) manifest(w http.ResponseWriter, r *http.Request) error {
	d, err := h.githubDock(r)
	if err != nil {
		return err
	}
	var req manifestRequest
	if err := httpx.DecodeJSON(r, &req); err != nil {
		return err
	}
	req.Organization, req.Name = text.Clean(req.Organization), text.Clean(req.Name)
	if req.Organization != "" && !login.MatchString(req.Organization) {
		return errx.InvalidField("organization", "organization must be a GitHub organization name")
	}
	if err := text.Optional("name", req.Name, maxAppName); err != nil {
		return err
	}
	if req.Name == "" {
		req.Name = clip("Seaport "+d.Name, maxAppName)
	}
	public := h.g.cfg.PublicURL
	m := map[string]any{
		"name":         req.Name,
		"url":          public,
		"description":  "Agents of a Seaport port: mention them in issues and pull requests.",
		"public":       false,
		"redirect_url": public + CreatedPath,
		"setup_url":    public + InstalledPath,
		// setup_on_update: changing the repositories comes back here too.
		"setup_on_update": true,
		"hook_attributes": map[string]any{"url": public + WebhookPath + d.ID.String(), "active": true},
		"default_permissions": map[string]string{
			"metadata": "read", "issues": "write", "pull_requests": "write",
			// contents: hosted agents push branches (docs, screenshots)
			"contents": "write",
		},
		"default_events": []string{"issues", "issue_comment", "pull_request", "pull_request_review", "pull_request_review_comment"},
	}
	raw, _ := json.Marshal(m)
	action := h.g.cfg.WebURL + "/settings/apps/new"
	if req.Organization != "" {
		action = h.g.cfg.WebURL + "/organizations/" + url.PathEscape(req.Organization) + "/settings/apps/new"
	}
	action += "?state=" + url.QueryEscape(h.g.state(d.Organization, d.ID, "manifest"))
	return httpx.JSON(w, http.StatusOK, Manifest{ActionURL: action, Manifest: string(raw)})
}

// flowState is what a manifest or install flow carries through GitHub.
type flowState struct {
	Organization identity.OrganizationID `json:"o"`
	Dock         identity.DockID         `json:"d"`
	Kind         string                  `json:"k"`
	Expires      int64                   `json:"e"`
}

const statePurpose = "github-flow-state"

func (g *Gateway) state(org identity.OrganizationID, d identity.DockID, kind string) string {
	raw, _ := json.Marshal(flowState{Organization: org, Dock: d, Kind: kind, Expires: time.Now().Add(stateTTL).Unix()})
	return g.seal.SealString(statePurpose, raw)
}

func (g *Gateway) openState(s, kind string) (flowState, error) {
	raw, err := g.seal.OpenString(statePurpose, s)
	if err != nil {
		return flowState{}, errx.Unauthorized("this GitHub link is not valid; start again from Seaport")
	}
	var st flowState
	if err := json.Unmarshal(raw, &st); err != nil || st.Kind != kind {
		return flowState{}, errx.Unauthorized("this GitHub link is not valid; start again from Seaport")
	}
	if time.Now().Unix() > st.Expires {
		return flowState{}, errx.Unauthorized("this GitHub link expired; start again from Seaport")
	}
	return st, nil
}

// flowDock is the GitHub dock a flow's state names.
func (h *Handler) flowDock(r *http.Request, st flowState) (dock.Dock, error) {
	d, err := h.docks.Find(r.Context(), st.Organization, st.Dock)
	if err != nil {
		return dock.Dock{}, err
	}
	if d.Channel != dock.ChannelGitHub {
		return dock.Dock{}, errx.Business("not a GitHub dock")
	}
	return d, nil
}

// back sends the browser to the dock's page, with an error to show.
func (h *Handler) back(w http.ResponseWriter, r *http.Request, d identity.DockID, problem string) error {
	target := h.g.cfg.PublicURL + "/docks/" + d.String()
	if problem != "" {
		target += "?github_error=" + url.QueryEscape(problem)
	} else {
		target += "?github=installed"
	}
	http.Redirect(w, r, target, http.StatusSeeOther)
	return nil
}

// created: GitHub registered the App from the manifest; trade the code
// for its credentials, then send the browser on to install it.
func (h *Handler) created(w http.ResponseWriter, r *http.Request) error {
	st, err := h.g.openState(r.URL.Query().Get("state"), "manifest")
	if err != nil {
		return err
	}
	code := r.URL.Query().Get("code")
	if code == "" || len(code) > 256 {
		return h.back(w, r, st.Dock, "GitHub did not create the app")
	}
	d, err := h.flowDock(r, st)
	if err != nil {
		return err
	}
	conv, err := h.g.client.convert(r.Context(), code)
	if err != nil {
		slog.Warn("convert github app manifest", "dock", d.ID, "err", err)
		return h.back(w, r, d.ID, "GitHub did not hand over the app's credentials: "+err.Error())
	}
	if err := h.g.store.SaveApp(r.Context(), d.Organization, d.ID, conv); err != nil {
		return err
	}
	h.g.client.forget(d.ID)
	slog.Info("github app created", "dock", d.ID, "app", conv.Slug)
	a, err := h.g.store.App(r.Context(), d.ID)
	if err != nil {
		return err
	}
	http.Redirect(w, r, h.installURL(a, d), http.StatusSeeOther)
	return nil
}

// installed: the App was installed (or its repositories changed); check
// the installation with GitHub and link the dock.
func (h *Handler) installed(w http.ResponseWriter, r *http.Request) error {
	st, err := h.g.openState(r.URL.Query().Get("state"), "install")
	if err != nil {
		return err
	}
	id, err := strconv.ParseInt(r.URL.Query().Get("installation_id"), 10, 64)
	if err != nil || id <= 0 {
		// e.g. an install request an organization owner must approve.
		return h.back(w, r, st.Dock, "GitHub did not finish the installation (an owner may have to approve it)")
	}
	d, err := h.flowDock(r, st)
	if err != nil {
		return err
	}
	a, err := h.g.store.App(r.Context(), d.ID)
	if err != nil {
		return err
	}
	inst, err := h.g.client.installation(r.Context(), a, id)
	if err != nil {
		return h.back(w, r, d.ID, "GitHub does not know this installation of the app: "+err.Error())
	}
	if err := h.g.record(r, a, inst); err != nil {
		return err
	}
	return h.back(w, r, d.ID, "")
}

// record stores an installation and updates the dock's link state.
func (g *Gateway) record(r *http.Request, a App, inst installation) error {
	in := Installation{ID: inst.ID, Account: inst.Account.Login, AccountType: inst.Account.Type, Suspended: inst.SuspendedAt != nil}
	if err := g.store.SaveInstallation(r.Context(), a.Organization, a.Dock, in); err != nil {
		return err
	}
	return g.relink(r, a)
}

// relink sets the dock linked while one installation is active, logged
// out when none is.
func (g *Gateway) relink(r *http.Request, a App) error {
	links, _, _ := g.bound()
	if links == nil {
		return nil
	}
	installs, err := g.store.Installations(r.Context(), a.Dock)
	if err != nil {
		return err
	}
	if active(installs) > 0 {
		return links.SetLinked(r.Context(), a.Dock, a.Slug)
	}
	return links.SetStatus(r.Context(), a.Dock, dock.StatusLoggedOut)
}

// webhook takes GitHub's events for one dock, signed with its App's
// webhook secret.
func (h *Handler) webhook(w http.ResponseWriter, r *http.Request) error {
	id, err := identity.ParseDockID(r.PathValue("dock"))
	if err != nil {
		return errx.NotFound("unknown webhook")
	}
	a, err := h.g.store.App(r.Context(), id)
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
	if !validSignature(a.webhookSecret, body, r.Header.Get("X-Hub-Signature-256")) {
		return errx.Unauthorized("bad webhook signature")
	}
	kind := r.Header.Get("X-GitHub-Event")
	switch kind {
	case "ping":
		return httpx.JSON(w, http.StatusOK, map[string]string{"status": "pong"})
	case "installation":
		return h.installationEvent(w, r, a, body)
	}
	_, receiver, teams := h.g.bound()
	if receiver == nil {
		return errx.External("this port is starting; try again")
	}
	var team []agent.Member
	if teams != nil {
		if team, err = teams.Team(r.Context(), a.Organization, a.Dock); err != nil {
			return err
		}
	}
	at, ok := parse(kind, body, a.Dock, a.Slug, team)
	if !ok {
		return httpx.JSON(w, http.StatusAccepted, map[string]string{"status": "ignored"})
	}
	st, created, err := receiver.Receive(r.Context(), a.Organization, at.in)
	if err != nil {
		slog.Error("receive github message", "dock", a.Dock, "chat", at.in.ChatID, "err", err)
		return err
	}
	if created && st.Status == ship.StatusQueued {
		h.g.seen(a, at)
	}
	return httpx.JSON(w, http.StatusAccepted, map[string]any{"status": "received", "ship_id": st.ID, "created": created})
}

// installationEvent keeps the installations in step with GitHub (also
// installs started on GitHub rather than from Seaport).
func (h *Handler) installationEvent(w http.ResponseWriter, r *http.Request, a App, body []byte) error {
	var e struct {
		Action       string       `json:"action"`
		Installation installation `json:"installation"`
	}
	if err := json.Unmarshal(body, &e); err != nil || e.Installation.ID <= 0 {
		return errx.Validation("not an installation event")
	}
	switch e.Action {
	case "deleted":
		if err := h.g.store.DeleteInstallation(r.Context(), a.Dock, e.Installation.ID); err != nil {
			return err
		}
		h.g.client.forget(a.Dock)
		if err := h.g.relink(r, a); err != nil {
			return err
		}
	case "created", "suspend", "unsuspend", "new_permissions_accepted":
		if e.Action == "suspend" && e.Installation.SuspendedAt == nil {
			now := time.Now()
			e.Installation.SuspendedAt = &now
		}
		if e.Action == "unsuspend" {
			e.Installation.SuspendedAt = nil
		}
		if err := h.g.record(r, a, e.Installation); err != nil {
			return err
		}
	}
	return httpx.JSON(w, http.StatusOK, map[string]string{"status": "ok"})
}
