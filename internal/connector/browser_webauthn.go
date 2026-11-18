package connector

import (
	"context"
	"encoding/json"
	"log/slog"
	"time"
)

// The slot's browser can't use a security key or a passkey: no USB device
// or platform authenticator reaches a container. Yet a site that asks for
// one (WebAuthn: Google's 2-Step Verification, Okta, GitHub…) makes
// Chromium open its native "use your security key" dialog, which the
// screencast doesn't show and which takes the page's input while it is up:
// the person in the viewer sees the page but can't click "Try another
// way". So every page of the browser runs in Chromium's virtual WebAuthn
// environment with no authenticators and no dialog (WebAuthn.enable
// {enableUI: false}): a request finds no authenticator, the page stays
// usable, and the site offers its other methods (a code, a phone prompt).
//
// The guard is one CDP connection of its own for the browser's life. It
// auto-attaches with waitForDebuggerOnStart, so a new tab or popup (one
// that asks for a key as it loads) is paused until it is guarded and then
// resumed; a page's out-of-process iframes and workers are attached and
// resumed the same way. Chromium resumes a paused target when the
// connection that paused it goes away, so a lost guard never leaves a page
// stuck; it is dialed again while the browser lives.

// webauthnRetry is the wait before dialing a lost guard connection again.
var webauthnRetry = 3 * time.Second

// guardTimeout bounds the calls that guard one target.
const guardTimeout = 10 * time.Second

// autoAttach is Target.setAutoAttach as the guard sends it, to the browser
// and to each page.
var autoAttach = map[string]any{"autoAttach": true, "waitForDebuggerOnStart": true, "flatten": true}

// guardWebAuthn keeps every page of b in the virtual WebAuthn environment
// until ctx ends or the browser exits.
func guardWebAuthn(ctx context.Context, b *Browser) {
	for {
		err := guardOnce(ctx, b)
		select {
		case <-ctx.Done():
			return
		case <-b.Done():
			return
		default:
		}
		if err != nil {
			slog.Warn("browser WebAuthn guard lost the browser; retrying", "err", err, "in", webauthnRetry)
		}
		select {
		case <-time.After(webauthnRetry):
		case <-ctx.Done():
			return
		case <-b.Done():
			return
		}
	}
}

func guardOnce(ctx context.Context, b *Browser) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	wsURL, err := browserSocket(ctx, b.URL)
	if err != nil {
		return err
	}
	c, err := dialCDP(ctx, wsURL)
	if err != nil {
		return err
	}
	defer c.close()
	c.onEvent = func(method, _ string, params json.RawMessage) {
		if method != "Target.attachedToTarget" {
			return
		}
		var p struct {
			SessionID  string `json:"sessionId"`
			TargetInfo struct {
				Type string `json:"type"`
			} `json:"targetInfo"`
			Waiting bool `json:"waitingForDebugger"`
		}
		if json.Unmarshal(params, &p) != nil || p.SessionID == "" {
			return
		}
		// Not on the reading goroutine: the calls wait for answers it reads.
		go guardTarget(ctx, c, p.SessionID, p.TargetInfo.Type, p.Waiting)
	}
	if _, err := c.call(ctx, "", "Target.setAutoAttach", autoAttach); err != nil {
		return err
	}
	select {
	case <-c.done:
	case <-b.Done():
		c.close()
		<-c.done
	case <-ctx.Done():
		c.close()
		<-c.done
	}
	return c.err()
}

// guardTarget puts a page or iframe in the virtual WebAuthn environment,
// attaches its own child targets, and resumes it when it waits for us.
func guardTarget(ctx context.Context, c *cdpClient, session, kind string, waiting bool) {
	ctx, cancel := context.WithTimeout(ctx, guardTimeout)
	defer cancel()
	if kind == "page" || kind == "iframe" {
		if _, err := c.call(ctx, session, "WebAuthn.enable", map[string]any{"enableUI": false}); err != nil {
			slog.Debug("browser WebAuthn guard: enable", "type", kind, "err", err)
		}
		if _, err := c.call(ctx, session, "Target.setAutoAttach", autoAttach); err != nil {
			slog.Debug("browser WebAuthn guard: attach children", "type", kind, "err", err)
		}
	}
	if waiting {
		if _, err := c.call(ctx, session, "Runtime.runIfWaitingForDebugger", nil); err != nil {
			slog.Debug("browser WebAuthn guard: resume", "type", kind, "err", err)
		}
	}
}
