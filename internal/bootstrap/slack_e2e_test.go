package bootstrap_test

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Abraxas-365/seaport/internal/bootstrap"
	"github.com/Abraxas-365/seaport/internal/config"
	"github.com/Abraxas-365/seaport/internal/conversation"
	"github.com/Abraxas-365/seaport/internal/crew"
	"github.com/Abraxas-365/seaport/internal/dock"
	"github.com/Abraxas-365/seaport/internal/query"
	"github.com/Abraxas-365/seaport/internal/ship"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// fakeSlack plays Slack's Web API for one workspace (T0TEST, example.slack.com)
// and one app (A0TEST, bot user U0BOT) with a person U0ANA.
type fakeSlack struct {
	t   *testing.T
	srv *httptest.Server

	mu        sync.Mutex
	calls     []string
	posts     []map[string]any
	reactions []string
	uploads   []string
	removed   bool
}

const (
	slBotToken    = "xoxb-test-bot-token"
	slConfigToken = "xoxe.xoxp-test-config-token"
	slSigning     = "test-signing-secret"
	slClientID    = "1234.5678"
	slClientSec   = "test-client-secret"
)

func newFakeSlack(t *testing.T) *fakeSlack {
	f := &fakeSlack{t: t}
	f.srv = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeSlack) serve(w http.ResponseWriter, r *http.Request) {
	method := strings.TrimPrefix(r.URL.Path, "/api/")
	f.mu.Lock()
	f.calls = append(f.calls, method)
	f.mu.Unlock()
	answer := func(v map[string]any) {
		if _, ok := v["ok"]; !ok {
			v["ok"] = true
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(v)
	}
	fail := func(code string) { answer(map[string]any{"ok": false, "error": code}) }
	if r.URL.Path == "/upload/F0FILE" {
		raw, _ := io.ReadAll(r.Body)
		f.mu.Lock()
		f.uploads = append(f.uploads, "bytes:"+string(raw))
		f.mu.Unlock()
		return
	}
	if r.URL.Path == "/files/pic.png" {
		if r.Header.Get("Authorization") != "Bearer "+slBotToken {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		_, _ = w.Write([]byte("\x89PNG\r\n\x1a\n0000"))
		return
	}
	_ = r.ParseForm()
	bearer, _ := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	switch method {
	case "apps.manifest.create":
		if bearer != slConfigToken {
			fail("invalid_auth")
			return
		}
		var m struct {
			Display struct {
				Name string `json:"name"`
			} `json:"display_information"`
			Settings struct {
				Events struct {
					URL string `json:"request_url"`
				} `json:"event_subscriptions"`
			} `json:"settings"`
		}
		if json.Unmarshal([]byte(r.Form.Get("manifest")), &m) != nil || m.Settings.Events.URL == "" {
			fail("invalid_manifest")
			return
		}
		answer(map[string]any{"app_id": "A0TEST", "credentials": map[string]string{
			"client_id": slClientID, "client_secret": slClientSec, "signing_secret": slSigning}})
	case "oauth.v2.access":
		id, secret, _ := r.BasicAuth()
		if id != slClientID || secret != slClientSec || r.Form.Get("code") != "good-code" || r.Form.Get("redirect_uri") == "" {
			fail("invalid_code")
			return
		}
		answer(map[string]any{"access_token": slBotToken, "bot_user_id": "U0BOT", "app_id": "A0TEST",
			"team": map[string]string{"id": "T0TEST", "name": "Test Team"}})
	case "apps.uninstall":
		if bearer != slBotToken || r.Form.Get("client_id") != slClientID || r.Form.Get("client_secret") != slClientSec {
			fail("invalid_auth")
			return
		}
		f.mu.Lock()
		f.removed = true
		f.mu.Unlock()
		answer(map[string]any{})
	default:
		if bearer != slBotToken {
			fail("invalid_auth")
			return
		}
		f.bot(method, r, answer, fail)
	}
}

func (f *fakeSlack) bot(method string, r *http.Request, answer func(map[string]any), fail func(string)) {
	switch method {
	case "auth.test":
		answer(map[string]any{"url": "https://example.slack.com/", "team": "Test Team", "team_id": "T0TEST", "user_id": "U0BOT", "bot_id": "B0BOT"})
	case "users.info":
		answer(map[string]any{"user": map[string]any{"name": "ana", "profile": map[string]string{"display_name": "Ana", "email": "ana@example.com"}}})
	case "conversations.info":
		answer(map[string]any{"channel": map[string]string{"name": "support"}})
	case "chat.postMessage":
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		f.mu.Lock()
		f.posts = append(f.posts, body)
		f.mu.Unlock()
		answer(map[string]any{"ts": "1700000100.000200"})
	case "reactions.add":
		f.mu.Lock()
		f.reactions = append(f.reactions, r.Form.Get("channel")+"/"+r.Form.Get("timestamp")+":"+r.Form.Get("name"))
		f.mu.Unlock()
		answer(map[string]any{})
	case "files.getUploadURLExternal":
		answer(map[string]any{"upload_url": f.srv.URL + "/upload/F0FILE", "file_id": "F0FILE"})
	case "files.completeUploadExternal":
		f.mu.Lock()
		f.uploads = append(f.uploads, "complete:"+r.Form.Get("channel_id")+"/"+r.Form.Get("thread_ts")+":"+r.Form.Get("files"))
		f.mu.Unlock()
		answer(map[string]any{})
	default:
		fail("unknown_method")
	}
}

func (f *fakeSlack) called(method string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, c := range f.calls {
		if c == method {
			return true
		}
	}
	return false
}

func (f *fakeSlack) posted() []map[string]any {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]map[string]any(nil), f.posts...)
}

// slackEvent posts a body to the dock's events URL signed with secret at ts.
func (p *port) slackEvent(d dock.Dock, secret string, ts time.Time, retry bool, payload any) (int, map[string]any) {
	p.t.Helper()
	raw, _ := json.Marshal(payload)
	stamp := strconv.FormatInt(ts.Unix(), 10)
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte("v0:" + stamp + ":" + string(raw)))
	req, _ := http.NewRequest(http.MethodPost, p.srv.URL+"/slack/events/"+d.ID.String(), strings.NewReader(string(raw)))
	req.Header.Set("X-Slack-Request-Timestamp", stamp)
	req.Header.Set("X-Slack-Signature", "v0="+hex.EncodeToString(mac.Sum(nil)))
	req.Header.Set("Content-Type", "application/json")
	if retry {
		req.Header.Set("X-Slack-Retry-Num", "1")
		req.Header.Set("X-Slack-Retry-Reason", "http_timeout")
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		p.t.Fatal(err)
	}
	defer res.Body.Close()
	var out map[string]any
	_ = json.NewDecoder(res.Body).Decode(&out)
	return res.StatusCode, out
}

func slMessage(typ, channel, ts, thread, text string, extra map[string]any) map[string]any {
	ev := map[string]any{"type": typ, "user": "U0ANA", "text": text, "ts": ts, "channel": channel, "channel_type": "channel"}
	if thread != "" {
		ev["thread_ts"] = thread
	}
	for k, v := range extra {
		ev[k] = v
	}
	return map[string]any{"type": "event_callback", "team_id": "T0TEST", "api_app_id": "A0TEST", "event": ev}
}

type slInfo struct {
	App *struct {
		AppID     string `json:"app_id"`
		TeamName  string `json:"team_name"`
		BotUserID string `json:"bot_user_id"`
		Installed bool   `json:"installed"`
	} `json:"app"`
	Manifest    json.RawMessage `json:"manifest"`
	EventsURL   string          `json:"events_url"`
	RedirectURL string          `json:"redirect_url"`
	InstallURL  string          `json:"install_url"`
}

func newSlackPort(t *testing.T, sl *fakeSlack) *port {
	return startPortWith(t, func(c *config.Server) {
		c.SecretKey = "slack-test-secret-key-0123456789abcdef"
		c.SlackURL, c.SlackAPIURL = sl.srv.URL, sl.srv.URL+"/api"
		c.BlobURL = config.LocalBlobURL(filepath.Join(t.TempDir(), "blobs"))
	}, bootstrap.Options{})
}

func (p *port) slackDock(name string) dock.Dock {
	p.t.Helper()
	var d dock.Dock
	if code := p.admin(http.MethodPost, "/api/v1/docks", map[string]string{"channel": "slack", "name": name}, &d); code != http.StatusCreated {
		p.t.Fatalf("create slack dock: %d", code)
	}
	if d.Status != dock.StatusLinking {
		p.t.Fatalf("new slack dock is %s", d.Status)
	}
	return d
}

// Flow B: the owner makes the app from the manifest and pastes its signing
// secret and bot token; Seaport checks the token with auth.test.
func TestSlackDockLinkedByPastedTokens(t *testing.T) {
	sl := newFakeSlack(t)
	p := newSlackPort(t, sl)
	d := p.slackDock("Support")
	path := "/api/v1/docks/" + d.ID.String() + "/slack"

	var info slInfo
	if code := p.admin(http.MethodGet, path, nil, &info); code != http.StatusOK || info.App != nil ||
		info.EventsURL != p.srv.URL+"/slack/events/"+d.ID.String() || info.RedirectURL != p.srv.URL+"/slack/oauth/callback" {
		t.Fatalf("slack info: %d %+v", code, info)
	}
	var manifest struct {
		Settings struct {
			Events struct {
				URL string `json:"request_url"`
			} `json:"event_subscriptions"`
		} `json:"settings"`
	}
	if err := json.Unmarshal(info.Manifest, &manifest); err != nil || manifest.Settings.Events.URL != info.EventsURL {
		t.Fatalf("manifest = %s (%v)", info.Manifest, err)
	}
	api := p.apiDock("api")
	if code := p.admin(http.MethodGet, "/api/v1/docks/"+api.ID.String()+"/slack", nil, nil); code != http.StatusUnprocessableEntity {
		t.Fatalf("slack info of an api dock: %d", code)
	}

	// The request URL is verified before any secret is known.
	if code, out := p.slackEvent(d, "anything", time.Now(), false, map[string]string{"type": "url_verification", "challenge": "c-1"}); code != http.StatusOK || out["challenge"] != "c-1" {
		t.Fatalf("url_verification before connect: %d %v", code, out)
	}
	// Other events before it are unknown.
	if code, _ := p.slackEvent(d, "anything", time.Now(), false, slMessage("message", "D0DM", "1700000000.000100", "", "hi", map[string]any{"channel_type": "im"})); code != http.StatusNotFound {
		t.Fatalf("event before connect: %d", code)
	}

	for name, body := range map[string]map[string]string{
		"not a bot token": {"signing_secret": slSigning, "bot_token": "xoxp-test-user-token"},
		"wrong token":     {"signing_secret": slSigning, "bot_token": "xoxb-test-wrong"},
		"no secret":       {"bot_token": slBotToken},
	} {
		if code := p.admin(http.MethodPost, path+"/connect", body, nil); code != http.StatusBadRequest && code != http.StatusUnprocessableEntity {
			t.Fatalf("connect with %s: %d", name, code)
		}
	}
	if code := p.admin(http.MethodGet, "/api/v1/docks/"+d.ID.String(), nil, &d); code != http.StatusOK || d.Status != dock.StatusLinking {
		t.Fatalf("dock after refused connects: %d %s", code, d.Status)
	}
	if code := p.admin(http.MethodPost, path+"/connect", map[string]string{"signing_secret": slSigning, "bot_token": slBotToken}, &info); code != http.StatusOK ||
		info.App == nil || !info.App.Installed || info.App.BotUserID != "U0BOT" || info.App.TeamName != "Test Team" {
		t.Fatalf("connect: %d %+v", code, info)
	}
	if info.InstallURL != "" {
		t.Fatalf("a pasted app has no install url: %s", info.InstallURL)
	}
	p.admin(http.MethodGet, "/api/v1/docks/"+d.ID.String(), nil, &d)
	if d.Status != dock.StatusLinked || !strings.Contains(d.Address, "Test Team") || !strings.Contains(d.Address, "example.slack.com") {
		t.Fatalf("linked dock: %s %q", d.Status, d.Address)
	}
	if raw, _ := json.Marshal(info); strings.Contains(string(raw), slBotToken) || strings.Contains(string(raw), slSigning) {
		t.Fatal("slack info leaks a secret")
	}
	// An app made by hand has no client secret to uninstall it with: unlink
	// forgets it without calling Slack.
	if code := p.admin(http.MethodPost, "/api/v1/docks/"+d.ID.String()+"/unlink", nil, &d); code != http.StatusOK || d.Status != dock.StatusLoggedOut || sl.called("apps.uninstall") {
		t.Fatalf("unlink: %d %s uninstall=%v", code, d.Status, sl.called("apps.uninstall"))
	}
}

// Flow A: a configuration token creates the app from the manifest, the
// browser installs it through OAuth, the callback links the dock.
func TestSlackDockCreatedFromManifest(t *testing.T) {
	sl := newFakeSlack(t)
	p := newSlackPort(t, sl)
	d := p.slackDock("Support")
	path := "/api/v1/docks/" + d.ID.String() + "/slack"

	if code := p.admin(http.MethodPost, path+"/create", map[string]string{"config_token": "xoxe.xoxp-test-wrong"}, nil); code != http.StatusUnprocessableEntity && code != http.StatusBadRequest {
		t.Fatalf("create with a wrong token: %d", code)
	}
	var created struct {
		InstallURL string `json:"install_url"`
	}
	if code := p.admin(http.MethodPost, path+"/create", map[string]string{"config_token": slConfigToken, "name": "Seaport Support"}, &created); code != http.StatusOK {
		t.Fatalf("create: %d", code)
	}
	install, err := url.Parse(created.InstallURL)
	if err != nil || !strings.HasPrefix(created.InstallURL, sl.srv.URL+"/oauth/v2/authorize?") || install.Query().Get("client_id") != slClientID ||
		install.Query().Get("redirect_uri") != p.srv.URL+"/slack/oauth/callback" || !strings.Contains(install.Query().Get("scope"), "app_mentions:read") {
		t.Fatalf("install url = %s (%v)", created.InstallURL, err)
	}
	state := install.Query().Get("state")
	// Created, not yet installed: still linking, and the token was never stored.
	var info slInfo
	p.admin(http.MethodGet, path, nil, &info)
	p.admin(http.MethodGet, "/api/v1/docks/"+d.ID.String(), nil, &d)
	if info.App == nil || info.App.Installed || info.App.AppID != "A0TEST" || d.Status != dock.StatusLinking || info.InstallURL == "" {
		t.Fatalf("created app: %s %+v", d.Status, info)
	}
	if raw, _ := json.Marshal(info); strings.Contains(string(raw), slClientSec) || strings.Contains(string(raw), slConfigToken) {
		t.Fatal("slack info leaks a secret")
	}

	// A forged state, a code Slack refuses and a denied install.
	for _, bad := range []string{"forged", ""} {
		if res, err := noRedirect.Get(p.srv.URL + "/slack/oauth/callback?code=good-code&state=" + url.QueryEscape(bad)); err != nil || res.StatusCode != http.StatusUnauthorized {
			t.Fatalf("forged state: %v %v", err, res.StatusCode)
		}
	}
	if back := redirected(t, p.srv.URL+"/slack/oauth/callback?code=bad&state="+url.QueryEscape(state)); back.Query().Get("slack_error") == "" {
		t.Fatalf("refused code: %s", back)
	}
	if back := redirected(t, p.srv.URL+"/slack/oauth/callback?error=access_denied&state="+url.QueryEscape(state)); back.Query().Get("slack_error") == "" {
		t.Fatalf("denied install: %s", back)
	}
	p.admin(http.MethodGet, "/api/v1/docks/"+d.ID.String(), nil, &d)
	if d.Status != dock.StatusLinking {
		t.Fatalf("dock after refused installs: %s", d.Status)
	}

	back := redirected(t, p.srv.URL+"/slack/oauth/callback?code=good-code&state="+url.QueryEscape(state))
	if back.Path != "/docks/"+d.ID.String() || back.Query().Get("slack") != "installed" {
		t.Fatalf("back = %s", back)
	}
	p.admin(http.MethodGet, "/api/v1/docks/"+d.ID.String(), nil, &d)
	p.admin(http.MethodGet, path, nil, &info)
	if d.Status != dock.StatusLinked || !strings.Contains(d.Address, "Test Team") || info.App == nil || !info.App.Installed ||
		info.App.BotUserID != "U0BOT" || info.InstallURL == "" {
		t.Fatalf("installed: %s %q %+v", d.Status, d.Address, info)
	}
	// The same events work with the created app's signing secret.
	if code, out := p.slackEvent(d, slSigning, time.Now(), false, map[string]string{"type": "url_verification", "challenge": "c-2"}); code != http.StatusOK || out["challenge"] != "c-2" {
		t.Fatalf("url_verification: %d %v", code, out)
	}
}

// A Slack dock at work: mentions, thread follow-ups, DMs, replies, retries,
// bad signatures, the app's own messages, 👀, files and unlinking.
func TestSlackDock(t *testing.T) {
	sl := newFakeSlack(t)
	p := newSlackPort(t, sl)
	d := p.slackDock("Support")
	path := "/api/v1/docks/" + d.ID.String() + "/slack"
	var created struct {
		InstallURL string `json:"install_url"`
	}
	if code := p.admin(http.MethodPost, path+"/create", map[string]string{"config_token": slConfigToken}, &created); code != http.StatusOK {
		t.Fatalf("create: %d", code)
	}
	install, _ := url.Parse(created.InstallURL)
	redirected(t, p.srv.URL+"/slack/oauth/callback?code=good-code&state="+url.QueryEscape(install.Query().Get("state")))
	p.assignAs(adminToken, d.ID, p.agent(testAgent).ID, true)
	p.letIn(d)
	now := time.Now()

	// Signatures: wrong secret and replays are refused.
	mention := slMessage("app_mention", "C0SUP", "1700000000.000100", "", "<@U0BOT> please review <https://example.com/pr/7|PR 7>", nil)
	if code, _ := p.slackEvent(d, "wrong", now, false, mention); code != http.StatusUnauthorized {
		t.Fatalf("bad signature: %d", code)
	}
	if code, _ := p.slackEvent(d, slSigning, now.Add(-10*time.Minute), false, mention); code != http.StatusUnauthorized {
		t.Fatalf("old timestamp: %d", code)
	}
	if code, out := p.slackEvent(d, slSigning, now, false, map[string]string{"type": "url_verification", "challenge": "c-3"}); code != http.StatusOK || out["challenge"] != "c-3" {
		t.Fatalf("url_verification: %d %v", code, out)
	}

	// Ignored: messages that call no one, bots (ours included), edits.
	for _, ev := range []map[string]any{
		slMessage("message", "C0SUP", "1700000001.000100", "", "lunch?", nil),
		slMessage("message", "C0SUP", "1700000002.000100", "", "<@U0BOT> loop", map[string]any{"bot_id": "B0BOT", "user": "U0BOT"}),
		slMessage("message", "C0SUP", "1700000003.000100", "", "@test from another bot", map[string]any{"bot_id": "B0OTHER"}),
		slMessage("message", "C0SUP", "1700000004.000100", "", "@test edited", map[string]any{"subtype": "message_changed"}),
		slMessage("message", "C0SUP", "1700000005.000100", "1699999999.000001", "reply in a thread nobody answers in", nil),
	} {
		if code, out := p.slackEvent(d, slSigning, now, false, ev); code != http.StatusOK || out["status"] != "ignored" {
			t.Fatalf("ignored event: %d %v", code, out)
		}
	}

	// A mention: Slack sends app_mention and message.channels for one message.
	code, out := p.slackEvent(d, slSigning, now, false, mention)
	if code != http.StatusOK || out["status"] != "received" || out["created"] != true {
		t.Fatalf("mention: %d %v", code, out)
	}
	if code, again := p.slackEvent(d, slSigning, now, false, slMessage("message", "C0SUP", "1700000000.000100", "", "<@U0BOT> please review <https://example.com/pr/7|PR 7>", nil)); code != http.StatusOK ||
		again["created"] != false || again["ship_id"] != out["ship_id"] {
		t.Fatalf("the same message as a message event: %d %v", code, again)
	}
	// A retry is the same ship too.
	if code, again := p.slackEvent(d, slSigning, now, true, mention); code != http.StatusOK || again["created"] != false || again["ship_id"] != out["ship_id"] {
		t.Fatalf("retry: %d %v", code, again)
	}

	c := p.dialCrew(1)
	f := c.read()
	want := "please review [PR 7](https://example.com/pr/7)"
	if f.Type != crew.FrameDelivery || len(f.Delivery.Ships) != 1 || f.Delivery.Ships[0].Text != want || f.Delivery.Ships[0].SenderName != "Ana" {
		t.Fatalf("delivery = %+v", f)
	}
	var convs query.Paginated[conversation.Conversation]
	p.admin(http.MethodGet, "/api/v1/conversations?dock_id="+d.ID.String(), nil, &convs)
	if len(convs.Items) != 1 || convs.Items[0].ExternalID != "C0SUP/1700000000.000100" || convs.Items[0].Title != "#support" ||
		convs.Items[0].Kind != conversation.KindGroup {
		t.Fatalf("conversations = %+v", convs.Items)
	}
	waitFor(t, func() bool {
		sl.mu.Lock()
		defer sl.mu.Unlock()
		return len(sl.reactions) == 1 && sl.reactions[0] == "C0SUP/1700000000.000100:eyes"
	})

	// The agent replies: a markdown block in the thread.
	session := mcpSession(t, p, p.crewToken())
	reply := func(text string) {
		t.Helper()
		res, err := session.CallTool(t.Context(), &mcp.CallToolParams{Name: "reply",
			Arguments: map[string]any{"conversation_id": convs.Items[0].ID.String(), "text": text}})
		if err != nil || res.IsError {
			t.Fatalf("reply: %v %+v", err, res)
		}
	}
	reply("Looks good: **ship it**.")
	posts := sl.posted()
	if len(posts) != 1 || posts[0]["channel"] != "C0SUP" || posts[0]["thread_ts"] != "1700000000.000100" || posts[0]["text"] != "Looks good: **ship it**." {
		t.Fatalf("posts = %v", posts)
	}
	blocks, _ := posts[0]["blocks"].([]any)
	if len(blocks) != 1 || fmtJSON(blocks[0]) != `{"text":"Looks good: **ship it**.","type":"markdown"}` {
		t.Fatalf("blocks = %v", posts[0]["blocks"])
	}
	// A long reply is split below Slack's 12 000 characters.
	reply(strings.Repeat("lorem ipsum ", 1500)) // 18 000 characters
	posts = sl.posted()
	if len(posts) != 3 {
		t.Fatalf("a long reply made %d posts", len(posts)-1)
	}
	for _, post := range posts[1:] {
		if n := len(post["blocks"].([]any)[0].(map[string]any)["text"].(string)); n > 12000 || post["thread_ts"] != "1700000000.000100" {
			t.Fatalf("part of %d characters: %v", n, post["thread_ts"])
		}
	}
	// Slack tells us about our own message: dropped, never a second ship.
	if code, out := p.slackEvent(d, slSigning, now, false, slMessage("message", "C0SUP", "1700000100.000200", "1700000000.000100", "Looks good", map[string]any{"bot_id": "B0BOT", "user": "U0BOT"})); code != http.StatusOK || out["status"] != "ignored" {
		t.Fatalf("own message: %d %v", code, out)
	}
	ack(c, f)

	// A follow-up in the thread needs no mention; the same channel without
	// the thread stays ignored.
	if code, out := p.slackEvent(d, slSigning, now, false, slMessage("message", "C0SUP", "1700000200.000100", "1700000000.000100", "and the tests?", nil)); code != http.StatusOK || out["status"] != "received" || out["created"] != true {
		t.Fatalf("follow-up: %d %v", code, out)
	}
	if code, out := p.slackEvent(d, slSigning, now, false, slMessage("message", "C0SUP", "1700000300.000100", "", "unrelated chatter", nil)); code != http.StatusOK || out["status"] != "ignored" {
		t.Fatalf("unrelated message: %d %v", code, out)
	}
	f = c.read()
	if f.Type != crew.FrameDelivery || len(f.Delivery.Ships) != 1 || f.Delivery.Ships[0].Text != "and the tests?" || f.Delivery.Conversation.ID != convs.Items[0].ID {
		t.Fatalf("follow-up delivery = %+v", f)
	}
	ack(c, f)

	// A DM is its own direct conversation, addressed without a mention, and
	// replies go to the DM (no thread).
	if code, out := p.slackEvent(d, slSigning, now, false, slMessage("message", "D0ANA", "1700000400.000100", "", "hello there", map[string]any{"channel_type": "im"})); code != http.StatusOK || out["status"] != "received" {
		t.Fatalf("dm: %d %v", code, out)
	}
	f = c.read()
	if f.Type != crew.FrameDelivery || f.Delivery.Ships[0].Text != "hello there" {
		t.Fatalf("dm delivery = %+v", f)
	}
	p.admin(http.MethodGet, "/api/v1/conversations?dock_id="+d.ID.String(), nil, &convs)
	var dm conversation.Conversation
	for _, cv := range convs.Items {
		if cv.ExternalID == "D0ANA" {
			dm = cv
		}
	}
	if dm.Kind != conversation.KindDirect || dm.Title != "Ana" {
		t.Fatalf("dm conversation = %+v", dm)
	}
	res, err := session.CallTool(t.Context(), &mcp.CallToolParams{Name: "reply",
		Arguments: map[string]any{"conversation_id": dm.ID.String(), "text": "hi Ana"}})
	if err != nil || res.IsError {
		t.Fatalf("dm reply: %v %+v", err, res)
	}
	if last := sl.posted()[len(sl.posted())-1]; last["channel"] != "D0ANA" || last["thread_ts"] != nil {
		t.Fatalf("dm post = %v", last)
	}
	ack(c, f)

	// Image files are fetched with the bot token; others are named.
	files := map[string]any{"subtype": "file_share", "files": []map[string]string{
		{"name": "pic.png", "mimetype": "image/png", "url_private": sl.srv.URL + "/files/pic.png"},
		{"name": "notes.pdf", "mimetype": "application/pdf", "url_private": sl.srv.URL + "/files/notes.pdf"}}}
	if code, out := p.slackEvent(d, slSigning, now, false, slMessage("app_mention", "C0SUP", "1700000500.000100", "1700000000.000100", "<@U0BOT> see attached", files)); code != http.StatusOK || out["status"] != "received" {
		t.Fatalf("files: %d %v", code, out)
	}
	f = c.read()
	if f.Type != crew.FrameDelivery || len(f.Delivery.Ships) != 1 || len(f.Delivery.Ships[0].Attachments) != 1 ||
		!strings.Contains(f.Delivery.Ships[0].Text, "[file: notes.pdf]") {
		t.Fatalf("files delivery = %+v", f)
	}
	ack(c, f)

	// Files in a reply go through Slack's external upload flow.
	thread := convs.Items[0].ID
	for _, cv := range convs.Items {
		if cv.ExternalID == "C0SUP/1700000000.000100" {
			thread = cv.ID
		}
	}
	if code := p.call(http.MethodPost, crew.ReplyPath, p.crewToken(), crew.ReplyRequest{ConversationID: thread, Text: "report attached",
		Files: []ship.File{{Name: "report.txt", Data: []byte("all green")}}}, nil); code != http.StatusOK && code != http.StatusCreated {
		t.Fatalf("reply with a file: %d", code)
	}
	sl.mu.Lock()
	uploads := strings.Join(sl.uploads, "|")
	sl.mu.Unlock()
	if !strings.Contains(uploads, "bytes:all green") || !strings.Contains(uploads, "complete:C0SUP/1700000000.000100:") {
		t.Fatalf("uploads = %s", uploads)
	}

	// Unlink uninstalls the app and forgets it: events are unknown.
	if code := p.admin(http.MethodPost, "/api/v1/docks/"+d.ID.String()+"/unlink", nil, &d); code != http.StatusOK || d.Status != dock.StatusLoggedOut {
		t.Fatalf("unlink: %d %s", code, d.Status)
	}
	sl.mu.Lock()
	removed := sl.removed
	sl.mu.Unlock()
	if !removed {
		t.Fatal("unlink did not uninstall the app")
	}
	if code, _ := p.slackEvent(d, slSigning, now, false, mention); code != http.StatusNotFound {
		t.Fatalf("event after unlink: %d", code)
	}
	var info slInfo
	if p.admin(http.MethodGet, path, nil, &info); info.App != nil {
		t.Fatalf("app kept after unlink: %+v", info.App)
	}
}
