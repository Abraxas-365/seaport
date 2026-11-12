package dockslack

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/url"
	"regexp"
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
	"github.com/gofiber/fiber/v3"
)

// Public paths Slack calls or sends browsers to.
const (
	EventsPath        = "/slack/events/"
	OAuthCallbackPath = "/slack/oauth/callback"
)

// MaxEvent bounds an Events API body (Slack sends a few KiB; its limit is
// 3 MB per event).
const MaxEvent = 3 << 20

// stateTTL bounds an install flow started from the web.
const stateTTL = time.Hour

// Scopes are the bot scopes the app asks for; Events are the bot events
// it subscribes to.
var (
	Scopes = []string{"app_mentions:read", "chat:write", "channels:history", "groups:history", "im:history", "im:read",
		"im:write", "reactions:write", "users:read", "users:read.email", "files:read", "files:write"}
	Events = []string{"app_mention", "message.channels", "message.groups", "message.im"}
)

// Handler serves the Slack routes: the admin API of a Slack dock and the
// public endpoints of the install flow and the Events API.
type Handler struct {
	g     *Gateway
	docks dock.Queries
}

func NewHandler(g *Gateway, docks dock.Queries) *Handler { return &Handler{g: g, docks: docks} }

// Register mounts the admin routes (behind admin auth).
func (h *Handler) Register(r httpx.Router) {
	r.Get("/api/v1/docks/:id/slack", httpx.RequireFunc(permission.DocksRead, h.info))
	r.Post("/api/v1/docks/:id/slack/create", httpx.RequireFunc(permission.DocksWrite, h.create))
	r.Post("/api/v1/docks/:id/slack/install", httpx.RequireFunc(permission.DocksWrite, h.install))
	r.Post("/api/v1/docks/:id/slack/connect", httpx.RequireFunc(permission.DocksWrite, h.connect))
}

// RegisterPublic mounts the routes Slack reaches without a Seaport
// credential: each checks its own proof (a sealed state, a signature).
func (h *Handler) RegisterPublic(r httpx.Router) {
	r.Get("/slack/oauth/callback", h.callback)
	r.Post("/slack/events/:dock", h.events)
}

// slackDock finds the caller's dock and checks it is a Slack one.
func (h *Handler) slackDock(c fiber.Ctx) (dock.Dock, error) {
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
	if d.Channel != dock.ChannelSlack {
		return dock.Dock{}, errx.Business("not a Slack dock")
	}
	return d, nil
}

// AppInfo is a Slack dock's App as the admin API shows it (no secret).
type AppInfo struct {
	AppID     string `json:"app_id"`
	Name      string `json:"name"`
	TeamID    string `json:"team_id"`
	TeamName  string `json:"team_name"`
	TeamURL   string `json:"team_url"`
	BotUserID string `json:"bot_user_id"`
	Installed bool   `json:"installed"`
	// Managed: Seaport created the app (it can start its install).
	Managed   bool      `json:"managed"`
	CreatedAt time.Time `json:"created_at"`
}

// Info is the Slack side of a dock.
type Info struct {
	App *AppInfo `json:"app"`
	// Manifest is the app manifest for this dock (JSON): paste it at
	// api.slack.com/apps → Create New App → From a manifest.
	Manifest json.RawMessage `json:"manifest"`
	// EventsURL and RedirectURL are where Slack must reach this port;
	// Reachable is false when it can't be on the internet (loopback, plain http).
	EventsURL   string `json:"events_url"`
	RedirectURL string `json:"redirect_url"`
	Reachable   bool   `json:"reachable"`
}

func (h *Handler) info(c fiber.Ctx) error {
	d, err := h.slackDock(c)
	if err != nil {
		return err
	}
	out := Info{Manifest: h.g.manifest(d.ID, "Seaport "+d.Name), EventsURL: h.g.eventsURL(d.ID),
		RedirectURL: h.g.redirectURL(), Reachable: reachable(h.g.cfg.PublicURL)}
	a, err := h.g.store.App(c.Context(), d.ID)
	switch {
	case errx.IsType(err, errx.TypeNotFound):
		return httpx.JSON(c, http.StatusOK, out)
	case err != nil:
		return err
	}
	out.App = &AppInfo{AppID: a.AppID, Name: a.Name, TeamID: a.TeamID, TeamName: a.TeamName, TeamURL: a.TeamURL,
		BotUserID: a.BotUserID, Installed: a.Installed(), Managed: a.ClientID != "" && a.clientSecret != "", CreatedAt: a.CreatedAt}
	return httpx.JSON(c, http.StatusOK, out)
}

func (g *Gateway) eventsURL(d identity.DockID) string {
	return g.cfg.PublicURL + EventsPath + d.String()
}

func (g *Gateway) redirectURL() string { return g.cfg.PublicURL + OAuthCallbackPath }

func (g *Gateway) installURL(a App, d dock.Dock) string {
	q := url.Values{"client_id": {a.ClientID}, "scope": {strings.Join(Scopes, ",")},
		"redirect_uri": {g.redirectURL()}, "state": {g.state(d.Organization, d.ID, "install")}}
	return strings.TrimRight(g.cfg.WebURL, "/") + "/oauth/v2/authorize?" + q.Encode()
}

// reachable: Slack can call this URL (https, not loopback).
func reachable(public string) bool {
	u, err := url.Parse(public)
	if err != nil || u.Scheme != "https" {
		return false
	}
	host := strings.ToLower(u.Hostname())
	return host != "localhost" && !strings.HasPrefix(host, "127.") && host != "::1"
}

// maxAppName is Slack's limit on an app's name.
const maxAppName = 35

var botName = regexp.MustCompile(`[^a-z0-9._-]+`)

// manifest builds the app manifest of a dock (JSON).
func (g *Gateway) manifest(d identity.DockID, name string) json.RawMessage {
	name = clip(name, maxAppName)
	bot := strings.Trim(botName.ReplaceAllString(strings.ToLower(name), "-"), "-")
	if bot == "" {
		bot = "seaport"
	}
	m := map[string]any{
		"display_information": map[string]any{"name": name, "description": "Agents of a Seaport port: mention them in a channel or message them."},
		"features": map[string]any{
			"bot_user": map[string]any{"display_name": clip(bot, 80), "always_online": true},
			// Without the messages tab people can't DM the app.
			"app_home": map[string]any{"home_tab_enabled": false, "messages_tab_enabled": true, "messages_tab_read_only_enabled": false},
		},
		"oauth_config": map[string]any{
			"redirect_urls": []string{g.redirectURL()},
			"scopes":        map[string]any{"bot": Scopes},
		},
		"settings": map[string]any{
			"event_subscriptions":    map[string]any{"request_url": g.eventsURL(d), "bot_events": Events},
			"org_deploy_enabled":     false,
			"socket_mode_enabled":    false,
			"token_rotation_enabled": false,
		},
	}
	raw, _ := json.Marshal(m)
	return raw
}

// install answers the URL that (re)installs an app Seaport created. Each
// call seals a fresh state, and finishing the install replaces the bot
// token: a write, so it is minted here (docks:write), never by info.
func (h *Handler) install(c fiber.Ctx) error {
	d, err := h.slackDock(c)
	if err != nil {
		return err
	}
	a, err := h.g.store.App(c.Context(), d.ID)
	if errx.IsType(err, errx.TypeNotFound) {
		return errx.Business("this dock has no Slack App yet: create or connect one first")
	}
	if err != nil {
		return err
	}
	if a.ClientID == "" || a.clientSecret == "" {
		return errx.Business("this dock's app was not created by Seaport: paste its tokens instead")
	}
	return httpx.JSON(c, http.StatusOK, Created{InstallURL: h.g.installURL(a, d)})
}

type createRequest struct {
	// ConfigToken is the user's app configuration token (xoxe.xoxp-…): used
	// for this call only, never stored.
	ConfigToken string `json:"config_token"`
	// Name prefills the app's name.
	Name string `json:"name"`
}

// Created is where the web sends the browser to install the app.
type Created struct {
	InstallURL string `json:"install_url"`
}

// create (flow A): register the app from the manifest with the user's
// configuration token, keep its credentials, answer the install URL.
func (h *Handler) create(c fiber.Ctx) error {
	d, err := h.slackDock(c)
	if err != nil {
		return err
	}
	var req createRequest
	if err := httpx.DecodeJSON(c, &req); err != nil {
		return err
	}
	req.ConfigToken, req.Name = strings.TrimSpace(req.ConfigToken), text.Clean(req.Name)
	if err := text.Required("config_token", req.ConfigToken, 512); err != nil {
		return err
	}
	if err := text.Optional("name", req.Name, maxAppName); err != nil {
		return err
	}
	if req.Name == "" {
		req.Name = clip("Seaport "+d.Name, maxAppName)
	}
	made, err := h.g.client.createApp(c.Context(), req.ConfigToken, string(h.g.manifest(d.ID, req.Name)))
	if err != nil {
		slog.Warn("create slack app", "dock", d.ID, "err", err)
		if code(err) == "" {
			return err // Slack unreachable, rate limited...
		}
		return errx.Validation("Slack refused to create the app (" + code(err) + "): check the configuration token and that its workspace allows apps")
	}
	if err := h.g.store.SaveApp(c.Context(), d.Organization, d.ID, NewApp{AppID: made.AppID, Name: req.Name,
		ClientID: made.Credentials.ClientID, ClientSecret: made.Credentials.ClientSecret, SigningSecret: made.Credentials.SigningSecret}); err != nil {
		return err
	}
	h.g.client.forget(d.ID)
	// The new app has no bot token yet: a dock that was linked with the
	// old one is linking again until this one is installed.
	if links, _, _ := h.g.bound(); links != nil {
		if err := links.SetStatus(c.Context(), d.ID, dock.StatusLinking); err != nil {
			return err
		}
	}
	a, err := h.g.store.App(c.Context(), d.ID)
	if err != nil {
		return err
	}
	return httpx.JSON(c, http.StatusOK, Created{InstallURL: h.g.installURL(a, d)})
}

type connectRequest struct {
	SigningSecret string `json:"signing_secret"`
	BotToken      string `json:"bot_token"`
}

// connect (flow B): an app the user made from the manifest; check the
// bot token with auth.test and keep both secrets.
func (h *Handler) connect(c fiber.Ctx) error {
	d, err := h.slackDock(c)
	if err != nil {
		return err
	}
	var req connectRequest
	if err := httpx.DecodeJSON(c, &req); err != nil {
		return err
	}
	req.SigningSecret, req.BotToken = strings.TrimSpace(req.SigningSecret), strings.TrimSpace(req.BotToken)
	if err := text.Required("signing_secret", req.SigningSecret, 128); err != nil {
		return err
	}
	if err := text.Required("bot_token", req.BotToken, 512); err != nil {
		return err
	}
	if !strings.HasPrefix(req.BotToken, "xoxb-") {
		return errx.InvalidField("bot_token", "bot_token is the Bot User OAuth Token and starts with xoxb-")
	}
	who, err := h.g.client.authTest(c.Context(), req.BotToken)
	if err != nil {
		if code(err) == "" {
			return err
		}
		return errx.InvalidField("bot_token", "Slack does not accept this bot token ("+code(err)+")")
	}
	if who.UserID == "" {
		return errx.InvalidField("bot_token", "this token does not belong to a bot")
	}
	if err := h.g.store.SaveApp(c.Context(), d.Organization, d.ID, NewApp{Name: who.Team, SigningSecret: req.SigningSecret,
		BotToken: req.BotToken, TeamID: who.TeamID, TeamName: who.Team, TeamURL: who.URL, BotUserID: who.UserID, BotID: who.BotID}); err != nil {
		return err
	}
	h.g.client.forget(d.ID)
	if err := h.g.linked(c.Context(), d.ID, who.Team, who.URL); err != nil {
		return err
	}
	return h.info(c)
}

// linked marks the dock linked; its address is the workspace.
func (g *Gateway) linked(ctx context.Context, d identity.DockID, team, teamURL string) error {
	links, _, _ := g.bound()
	if links == nil {
		return nil
	}
	address := team
	if u, err := url.Parse(teamURL); err == nil && u.Host != "" {
		if address == "" {
			address = u.Host
		} else {
			address += " (" + u.Host + ")"
		}
	}
	if address == "" {
		address = "slack"
	}
	return links.SetLinked(ctx, d, clip(address, text.MaxName))
}

// flowState is what the install flow carries through Slack.
type flowState struct {
	Organization identity.OrganizationID `json:"o"`
	Dock         identity.DockID         `json:"d"`
	Kind         string                  `json:"k"`
	Expires      int64                   `json:"e"`
}

const statePurpose = "slack-flow-state"

func (g *Gateway) state(org identity.OrganizationID, d identity.DockID, kind string) string {
	raw, _ := json.Marshal(flowState{Organization: org, Dock: d, Kind: kind, Expires: time.Now().Add(stateTTL).Unix()})
	return g.seal.SealString(statePurpose, raw)
}

func (g *Gateway) openState(s, kind string) (flowState, error) {
	raw, err := g.seal.OpenString(statePurpose, s)
	if err != nil {
		return flowState{}, errx.Unauthorized("this Slack link is not valid; start again from Seaport")
	}
	var st flowState
	if err := json.Unmarshal(raw, &st); err != nil || st.Kind != kind {
		return flowState{}, errx.Unauthorized("this Slack link is not valid; start again from Seaport")
	}
	if time.Now().Unix() > st.Expires {
		return flowState{}, errx.Unauthorized("this Slack link expired; start again from Seaport")
	}
	return st, nil
}

// back sends the browser to the dock's page, with an error to show.
func (h *Handler) back(c fiber.Ctx, d identity.DockID, problem string) error {
	target := h.g.cfg.PublicURL + "/docks/" + d.String()
	if problem != "" {
		target += "?slack_error=" + url.QueryEscape(problem)
	} else {
		target += "?slack=installed"
	}
	return c.Redirect().Status(http.StatusSeeOther).To(target)
}

// callback: the user allowed the app in Slack; trade the code for the bot
// token and link the dock.
func (h *Handler) callback(c fiber.Ctx) error {
	st, err := h.g.openState(c.Query("state"), "install")
	if err != nil {
		return err
	}
	if denied := c.Query("error"); denied != "" {
		return h.back(c, st.Dock, "Slack did not finish the installation ("+clip(denied, 64)+")")
	}
	oauthCode := c.Query("code")
	if oauthCode == "" || len(oauthCode) > 512 {
		return h.back(c, st.Dock, "Slack did not hand over an installation code")
	}
	d, err := h.docks.Find(c.Context(), st.Organization, st.Dock)
	if err != nil {
		return err
	}
	if d.Channel != dock.ChannelSlack {
		return errx.Business("not a Slack dock")
	}
	a, err := h.g.store.App(c.Context(), d.ID)
	if err != nil {
		return err
	}
	if a.ClientID == "" || a.clientSecret == "" {
		return h.back(c, d.ID, "this dock's app was not created by Seaport: paste its tokens instead")
	}
	acc, err := h.g.client.exchange(c.Context(), a.ClientID, a.clientSecret, oauthCode, h.g.redirectURL())
	if err != nil {
		slog.Warn("exchange slack oauth code", "dock", d.ID, "err", err)
		return h.back(c, d.ID, "Slack did not hand over the bot token: "+code(err))
	}
	if acc.AccessToken == "" || acc.BotUserID == "" {
		return h.back(c, d.ID, "Slack installed the app without a bot user")
	}
	// auth.test adds the bot id and the workspace's address.
	who, err := h.g.client.authTest(c.Context(), acc.AccessToken)
	if err != nil {
		slog.Warn("slack auth.test after install", "dock", d.ID, "err", err)
	}
	in := Install{AppID: acc.AppID, BotToken: acc.AccessToken, TeamID: firstOf(acc.Team.ID, who.TeamID),
		TeamName: firstOf(acc.Team.Name, who.Team), TeamURL: who.URL, BotUserID: acc.BotUserID, BotID: who.BotID}
	if err := h.g.store.SaveInstall(c.Context(), d.ID, in); err != nil {
		return err
	}
	h.g.client.forget(d.ID)
	if err := h.g.linked(c.Context(), d.ID, in.TeamName, in.TeamURL); err != nil {
		return err
	}
	return h.back(c, d.ID, "")
}

// events takes Slack's Events API requests for one dock, signed with its
// app's signing secret. url_verification is answered even before the
// signing secret is known (a dock whose app is not connected yet).
func (h *Handler) events(c fiber.Ctx) error {
	id, err := identity.ParseDockID(httpx.Param(c, "dock"))
	if err != nil {
		return errx.NotFound("unknown events endpoint")
	}
	body := c.Body()
	if len(body) > MaxEvent {
		return errx.PayloadTooLarge("event body is too large")
	}
	var env envelope
	if err := json.Unmarshal(body, &env); err != nil {
		return errx.Validation("not a Slack event")
	}
	a, err := h.g.store.App(c.Context(), id)
	if errx.IsType(err, errx.TypeNotFound) {
		if env.Type == "url_verification" && env.Challenge != "" {
			return httpx.JSON(c, http.StatusOK, map[string]string{"challenge": env.Challenge})
		}
		return errx.NotFound("unknown events endpoint")
	}
	if err != nil {
		return err
	}
	if !validSignature(a.signingSecret, body, c.Get("X-Slack-Request-Timestamp"), c.Get("X-Slack-Signature"), time.Now()) {
		return errx.Unauthorized("bad event signature")
	}
	if env.Type == "url_verification" {
		return httpx.JSON(c, http.StatusOK, map[string]string{"challenge": env.Challenge})
	}
	if !a.Installed() {
		return httpx.JSON(c, http.StatusOK, map[string]string{"status": "ignored"})
	}
	_, receiver, teams := h.g.bound()
	if receiver == nil {
		return errx.External("this port is starting; try again")
	}
	var team []agent.Member
	if teams != nil {
		if team, err = teams.Team(c.Context(), a.Organization, a.Dock); err != nil {
			return err
		}
	}
	known := func(channel, ts string) (bool, error) {
		return h.g.store.KnownThread(c.Context(), a.Dock, channel, ts)
	}
	rt, ok, err := route(env, a, team, known)
	if err != nil {
		return err
	}
	if !ok {
		return httpx.JSON(c, http.StatusOK, map[string]string{"status": "ignored"})
	}
	// Slack wants an answer within 3 s and retries otherwise: names, images
	// and the ship are taken in the background (a retry is a duplicate the
	// ship's external id drops).
	go h.forward(a, env, rt, receiver)
	return httpx.JSON(c, http.StatusOK, map[string]string{"status": "accepted"})
}

// forwardTimeout bounds the background work of one event.
const forwardTimeout = 30 * time.Second

// forward reads a routed event (asking Slack for names and images) and
// hands it to the ship domain, on a context of its own.
func (h *Handler) forward(a App, env envelope, rt routing, receiver Receiver) {
	ctx, cancel := context.WithTimeout(context.Background(), forwardTimeout)
	defer cancel()
	at, ok, err := build(env, a.Dock, rt, lookups{
		dir: directoryOf{g: h.g, dock: a.Dock, token: a.botToken, ctx: ctx},
		fetch: func(u string) func(ctx context.Context) ([]byte, error) {
			return func(ctx context.Context) ([]byte, error) { return h.g.client.download(ctx, a.botToken, u) }
		},
	})
	if err != nil || !ok {
		if err != nil {
			slog.Error("read slack message", "dock", a.Dock, "err", err)
		}
		return
	}
	st, created, err := receiver.Receive(ctx, a.Organization, at.in)
	if err != nil {
		slog.Error("receive slack message", "dock", a.Dock, "chat", at.in.ChatID, "err", err)
		return
	}
	// A message customs or routing holds is not a conversation Seaport
	// answers in: its thread is remembered once something is queued.
	if at.thread != "" && st.Status != ship.StatusHeld {
		if err := h.g.store.RememberThread(ctx, a.Organization, a.Dock, at.channel, at.thread); err != nil {
			slog.Error("remember slack thread", "dock", a.Dock, "err", err)
		}
	}
	if created && st.Status == ship.StatusQueued {
		h.g.seen(a, at)
	}
}
