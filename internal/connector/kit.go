package connector

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/Abraxas-365/seaport/internal/agent"
	"github.com/Abraxas-365/seaport/internal/errx"
)

// errRestart: the agent's kit changed and the connector is idle; start
// the harness again with the new one.
var errRestart = errors.New("agent profile changed")

// fetchKit gets the agent's kit (GET /crew/profile): its profile and
// secrets. nil, nil: the port predates profiles (no such route). A refused
// credential or agent is an authorization error (the connector stops);
// anything else can be retried.
func fetchKit(ctx context.Context, cfg Config) (*agent.Kit, error) {
	u := strings.TrimRight(cfg.Server, "/") + "/crew/profile"
	if cfg.As != "" {
		u += "?agent=" + url.QueryEscape(cfg.As)
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, errx.Wrap(err, "kit request", errx.TypeValidation)
	}
	req.Header = authHeader(cfg.Token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, errx.Wrap(err, "fetch agent profile", errx.TypeExternal)
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil, errx.Wrap(err, "read agent profile", errx.TypeExternal)
	}
	if mt, _, _ := mime.ParseMediaType(resp.Header.Get("Content-Type")); mt != "application/json" {
		if resp.StatusCode == http.StatusOK || resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusMethodNotAllowed {
			return nil, nil // an older port: its web app answers unknown paths
		}
		return nil, errx.External("fetch agent profile: the port answered " + resp.Status)
	}
	if resp.StatusCode != http.StatusOK {
		msg := portMessage(body, resp.Status)
		switch {
		case resp.StatusCode == http.StatusUnauthorized:
			if cfg.Token == "" {
				return nil, errx.Unauthorized("the port requires a crew token (-token or SEAPORT_CREW_TOKEN)")
			}
			return nil, errx.Unauthorized("the port rejected the crew token (revoked, expired or not of this port); create one with `seaport crew tokens create` or in the web app")
		case resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500:
			return nil, errx.External("fetch agent profile: " + msg)
		default: // forbidden, unknown agent, none named, a secret missing...
			return nil, errx.Unauthorized("the port refused the agent's profile: " + msg)
		}
	}
	var kit agent.Kit
	if err := json.Unmarshal(body, &kit); err != nil {
		return nil, errx.Wrap(err, "decode agent profile", errx.TypeExternal)
	}
	return &kit, nil
}

// portMessage is the message of a port error body ({"error":{"message"}}).
func portMessage(body []byte, status string) string {
	var e struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if json.Unmarshal(body, &e) == nil && e.Error.Message != "" {
		return e.Error.Message
	}
	return status
}

// fetchKitRetrying retries fetchKit while the port is unreachable, with
// the reconnect backoff; it gives up only on a refusal or when ctx ends.
func fetchKitRetrying(ctx context.Context, cfg Config) (*agent.Kit, error) {
	backoff := time.Second
	for {
		kit, err := fetchKit(ctx, cfg)
		if err == nil || errx.IsType(err, errx.TypeAuthorization) || ctx.Err() != nil {
			return kit, err
		}
		slog.Warn("could not fetch the agent profile, retrying", "err", err, "in", backoff)
		select {
		case <-time.After(backoff):
		case <-ctx.Done():
			return nil, ctx.Err()
		}
		backoff = min(backoff*2, 30*time.Second)
	}
}

// revision names the kit a harness runs ("" without one).
func revision(kit *agent.Kit) string {
	if kit == nil {
		return ""
	}
	return kit.Revision
}
