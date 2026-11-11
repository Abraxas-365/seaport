package dockslack

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Abraxas-365/seaport/internal/errx"
	"github.com/Abraxas-365/seaport/internal/identity"
	"github.com/Abraxas-365/seaport/internal/ship"
)

// client speaks Slack's Web API (no SDK: a dozen methods). Every method is
// POST <api>/<method>; answers are 200 with {"ok": false, "error": code}
// on failure (429 + Retry-After when rate limited).
type client struct {
	api  string // https://slack.com/api
	http *http.Client

	mu    sync.Mutex
	cache map[string]cached // users and channels, by dock and id
}

type cached struct {
	value   string
	expires time.Time
}

// cacheFor: how long a name read from Slack is kept.
const cacheFor = time.Hour

// maxCache bounds the name cache; a full one starts over.
const maxCache = 5000

func newClient(api string) *client {
	return &client{api: strings.TrimRight(api, "/"), http: &http.Client{Timeout: 30 * time.Second}, cache: map[string]cached{}}
}

// apiError is Slack's error code ("channel_not_found", "invalid_auth"...).
type apiError struct{ Code string }

func (e *apiError) Error() string { return "slack: " + e.Code }

// code is the Slack error code inside err ("" when it isn't one).
func code(err error) string {
	var e *apiError
	if errors.As(err, &e) {
		return e.Code
	}
	return ""
}

// revoked: the token no longer works (app uninstalled, token revoked).
func revoked(err error) bool {
	switch code(err) {
	case "invalid_auth", "token_revoked", "account_inactive", "not_authed", "token_expired", "app_uninstalled":
		return true
	}
	return false
}

// call posts a form to a method with a bearer token (none: ""), and
// decodes the answer into out (nil: ignored).
func (c *client) call(ctx context.Context, method, token string, form url.Values, out any) error {
	return c.send(ctx, method, token, "application/x-www-form-urlencoded", []byte(form.Encode()), "", "", out)
}

// callJSON is call with a JSON body (chat.postMessage's blocks).
func (c *client) callJSON(ctx context.Context, method, token string, body any, out any) error {
	raw, err := json.Marshal(body)
	if err != nil {
		return errx.Wrap(err, "encode slack request", errx.TypeInternal)
	}
	return c.send(ctx, method, token, "application/json; charset=utf-8", raw, "", "", out)
}

// callBasic is call with the app's client id and secret as basic auth
// (oauth.v2.access).
func (c *client) callBasic(ctx context.Context, method, id, secret string, form url.Values, out any) error {
	return c.send(ctx, method, "", "application/x-www-form-urlencoded", []byte(form.Encode()), id, secret, out)
}

func (c *client) send(ctx context.Context, method, token, contentType string, body []byte, user, pass string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.api+"/"+method, bytes.NewReader(body))
	if err != nil {
		return errx.Wrap(err, "slack request", errx.TypeInternal)
	}
	req.Header.Set("Content-Type", contentType)
	req.Header.Set("User-Agent", "seaport")
	switch {
	case token != "":
		req.Header.Set("Authorization", "Bearer "+token)
	case user != "":
		req.SetBasicAuth(user, pass)
	}
	res, err := c.http.Do(req)
	if err != nil {
		return errx.Wrap(err, "call slack", errx.TypeExternal)
	}
	defer func() { _ = res.Body.Close() }()
	raw, err := io.ReadAll(io.LimitReader(res.Body, 4<<20))
	if err != nil {
		return errx.Wrap(err, "read slack answer", errx.TypeExternal)
	}
	if res.StatusCode == http.StatusTooManyRequests {
		wait := res.Header.Get("Retry-After")
		if _, err := strconv.Atoi(wait); err != nil {
			wait = "a few"
		}
		return errx.External("slack is rate limiting this app: retry in " + wait + " seconds")
	}
	if res.StatusCode >= 300 {
		return errx.External("slack answered " + strconv.Itoa(res.StatusCode))
	}
	var status struct {
		OK    bool   `json:"ok"`
		Error string `json:"error"`
	}
	if err := json.Unmarshal(raw, &status); err != nil {
		return errx.Wrap(err, "decode slack answer", errx.TypeExternal)
	}
	if !status.OK {
		return errx.Wrap(&apiError{Code: status.Error}, "slack: "+status.Error, errx.TypeExternal)
	}
	if out != nil {
		if err := json.Unmarshal(raw, out); err != nil {
			return errx.Wrap(err, "decode slack answer", errx.TypeExternal)
		}
	}
	return nil
}

// created is what apps.manifest.create answers.
type created struct {
	AppID       string `json:"app_id"`
	Credentials struct {
		ClientID      string `json:"client_id"`
		ClientSecret  string `json:"client_secret"`
		SigningSecret string `json:"signing_secret"`
	} `json:"credentials"`
}

// createApp registers an app from a manifest with a configuration token
// (used for this call only).
func (c *client) createApp(ctx context.Context, configToken, manifest string) (created, error) {
	var out created
	err := c.call(ctx, "apps.manifest.create", configToken, url.Values{"manifest": {manifest}}, &out)
	return out, err
}

// access is what oauth.v2.access answers for an app's install.
type access struct {
	AccessToken string `json:"access_token"`
	BotUserID   string `json:"bot_user_id"`
	AppID       string `json:"app_id"`
	Team        struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	} `json:"team"`
}

// exchange trades an install's code for the bot token.
func (c *client) exchange(ctx context.Context, clientID, clientSecret, code, redirect string) (access, error) {
	var out access
	err := c.callBasic(ctx, "oauth.v2.access", clientID, clientSecret, url.Values{"code": {code}, "redirect_uri": {redirect}}, &out)
	return out, err
}

// identity is what auth.test says about a bot token.
type identityOf struct {
	URL    string `json:"url"`
	Team   string `json:"team"`
	TeamID string `json:"team_id"`
	UserID string `json:"user_id"`
	BotID  string `json:"bot_id"`
}

func (c *client) authTest(ctx context.Context, token string) (identityOf, error) {
	var out identityOf
	err := c.call(ctx, "auth.test", token, url.Values{}, &out)
	return out, err
}

// uninstall removes the app from its workspace (best effort at unlink).
func (c *client) uninstall(ctx context.Context, token, clientID, clientSecret string) error {
	return c.call(ctx, "apps.uninstall", token, url.Values{"client_id": {clientID}, "client_secret": {clientSecret}}, nil)
}

// posted is chat.postMessage's answer.
type posted struct {
	TS string `json:"ts"`
}

// post sends one message: markdown block plus plain text fallback, in a
// thread when threadTS is set.
func (c *client) post(ctx context.Context, token, channel, threadTS, markdown string) (posted, error) {
	body := map[string]any{
		"channel": channel,
		"text":    fallback(markdown),
		"blocks":  []map[string]string{{"type": "markdown", "text": markdown}},
	}
	if threadTS != "" {
		body["thread_ts"] = threadTS
	}
	var out posted
	err := c.callJSON(ctx, "chat.postMessage", token, body, &out)
	return out, err
}

// react adds a reaction to a message ("already_reacted" is fine).
func (c *client) react(ctx context.Context, token, channel, ts, name string) error {
	err := c.call(ctx, "reactions.add", token, url.Values{"channel": {channel}, "timestamp": {ts}, "name": {name}}, nil)
	if code(err) == "already_reacted" {
		return nil
	}
	return err
}

// upload puts files in a channel (in a thread when threadTS is set) with
// Slack's external upload flow: an upload URL per file, the bytes posted to
// it, one completion naming them all.
func (c *client) upload(ctx context.Context, token, channel, threadTS string, files []fileUpload) error {
	type ref struct {
		ID    string `json:"id"`
		Title string `json:"title"`
	}
	refs := make([]ref, 0, len(files))
	for _, f := range files {
		var slot struct {
			UploadURL string `json:"upload_url"`
			FileID    string `json:"file_id"`
		}
		err := c.call(ctx, "files.getUploadURLExternal", token,
			url.Values{"filename": {f.Name}, "length": {strconv.Itoa(len(f.Data))}}, &slot)
		if err != nil {
			return err
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, slot.UploadURL, bytes.NewReader(f.Data))
		if err != nil {
			return errx.Wrap(err, "slack upload request", errx.TypeExternal)
		}
		req.Header.Set("Content-Type", "application/octet-stream")
		res, err := c.http.Do(req)
		if err != nil {
			return errx.Wrap(err, "upload to slack", errx.TypeExternal)
		}
		_, _ = io.Copy(io.Discard, io.LimitReader(res.Body, 1<<20))
		_ = res.Body.Close()
		if res.StatusCode >= 300 {
			return errx.External("slack refused the upload of " + f.Name + " (" + strconv.Itoa(res.StatusCode) + ")")
		}
		refs = append(refs, ref{ID: slot.FileID, Title: f.Name})
	}
	raw, _ := json.Marshal(refs)
	form := url.Values{"files": {string(raw)}, "channel_id": {channel}}
	if threadTS != "" {
		form.Set("thread_ts", threadTS)
	}
	return c.call(ctx, "files.completeUploadExternal", token, form, nil)
}

// fileUpload is one file of a reply.
type fileUpload struct {
	Name string
	Data []byte
}

// download reads a private file with the bot token, at most ship.MaxImage
// bytes (ship.ErrMediaTooLarge past it, without reading on).
func (c *client) download(ctx context.Context, token, fileURL string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, fileURL, nil)
	if err != nil {
		return nil, errx.Wrap(err, "slack file request", errx.TypeExternal)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("User-Agent", "seaport")
	res, err := c.http.Do(req)
	if err != nil {
		return nil, errx.Wrap(err, "download slack file", errx.TypeExternal)
	}
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != http.StatusOK {
		return nil, errx.External("slack answered " + strconv.Itoa(res.StatusCode) + " for a file")
	}
	data, err := io.ReadAll(io.LimitReader(res.Body, ship.MaxImage+1))
	if err != nil {
		return nil, errx.Wrap(err, "read slack file", errx.TypeExternal)
	}
	if len(data) > ship.MaxImage {
		return nil, ship.ErrMediaTooLarge
	}
	return data, nil
}

// userInfo is what users.info tells about a person.
type userInfo struct {
	Name  string
	Email string
}

func (c *client) cached(key string) (string, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	v, ok := c.cache[key]
	if !ok || time.Now().After(v.expires) {
		return "", false
	}
	return v.value, true
}

func (c *client) remember(key, value string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.cache) >= maxCache {
		c.cache = map[string]cached{}
	}
	c.cache[key] = cached{value: value, expires: time.Now().Add(cacheFor)}
}

// forget drops what the dock's cache holds (unlink).
func (c *client) forget(dock identity.DockID) {
	prefix := dock.String() + "\x00"
	c.mu.Lock()
	defer c.mu.Unlock()
	for k := range c.cache {
		if strings.HasPrefix(k, prefix) {
			delete(c.cache, k)
		}
	}
}

// user reads a person's name and email (cached). It never fails: Slack
// refusing (a missing scope, a deleted user) leaves the id as the name.
func (c *client) user(ctx context.Context, dock identity.DockID, token, id string) userInfo {
	key := dock.String() + "\x00u\x00" + id
	if v, ok := c.cached(key); ok {
		name, email, _ := strings.Cut(v, "\x00")
		return userInfo{Name: name, Email: email}
	}
	var out struct {
		User struct {
			Name     string `json:"name"`
			RealName string `json:"real_name"`
			Profile  struct {
				DisplayName string `json:"display_name"`
				RealName    string `json:"real_name"`
				Email       string `json:"email"`
			} `json:"profile"`
		} `json:"user"`
	}
	if err := c.call(ctx, "users.info", token, url.Values{"user": {id}}, &out); err != nil {
		return userInfo{Name: id}
	}
	u := out.User
	name := firstOf(u.Profile.DisplayName, u.Profile.RealName, u.RealName, u.Name, id)
	c.remember(key, name+"\x00"+u.Profile.Email)
	return userInfo{Name: name, Email: u.Profile.Email}
}

// channel reads a channel's name as "#name" (cached); "#<id>" when Slack
// won't say (a private channel without groups:read).
func (c *client) channel(ctx context.Context, dock identity.DockID, token, id string) string {
	key := dock.String() + "\x00c\x00" + id
	if v, ok := c.cached(key); ok {
		return v
	}
	var out struct {
		Channel struct {
			Name string `json:"name"`
		} `json:"channel"`
	}
	if err := c.call(ctx, "conversations.info", token, url.Values{"channel": {id}}, &out); err != nil || out.Channel.Name == "" {
		return "#" + id
	}
	name := "#" + out.Channel.Name
	c.remember(key, name)
	return name
}

func firstOf(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}
