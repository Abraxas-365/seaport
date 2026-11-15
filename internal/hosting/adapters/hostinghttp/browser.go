package hostinghttp

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/url"
	"strings"
	"time"

	"github.com/Abraxas-365/seaport/internal/errx"
	"github.com/Abraxas-365/seaport/internal/hosting"
	"github.com/gofiber/contrib/v3/websocket"
	"github.com/gofiber/fiber/v3"
)

// The browser viewer: GET /api/v1/agents/{id}/hosting/slots/{n}/browser is
// a WebSocket (behind admin auth, permission.BrowserUse) that carries CDP
// as it is between the web and the slot's managed browser
// (Commands.OpenBrowser). Like the crew hub, everything that can refuse
// happens before the upgrade; after it the hijacked goroutine never
// touches the Fiber context (TestNoFiberCtxInGoroutines).

// browserWrite bounds one frame write to the web.
const browserWrite = 30 * time.Second

// BrowserRoute says whether path is a viewer's
// (/api/v1/agents/{id}/hosting/slots/{n}/browser), a stream bootstrap
// exempts from Deadline (isStream).
func BrowserRoute(path string) bool {
	return strings.HasPrefix(path, "/api/v1/agents/") && strings.HasSuffix(path, "/browser") && strings.Contains(path, "/hosting/slots/")
}

func (h *Handler) browser(c fiber.Ctx) error {
	p, in, err := h.slotAction(c)
	if err != nil {
		return err
	}
	// Another organization's agent is NotFound before anything about the
	// request itself (its shape, the origin) is judged.
	if _, err := h.queries.Hosted(c.Context(), p.Organization, in.Agent); err != nil {
		return err
	}
	// A page from another site must not drive the browser (cookies would
	// authenticate it).
	if !sameOrigin(c.Get(fiber.HeaderOrigin), string(c.RequestCtx().Host())) {
		return errx.Forbidden("the WebSocket origin is not allowed")
	}
	if !websocket.IsWebSocketUpgrade(c) {
		return errx.Validation("the browser viewer is a WebSocket")
	}
	conn, err := h.commands.OpenBrowser(c.Context(), p.Organization, in)
	if err != nil {
		return err
	}
	// The upgrade is refused only for a bad handshake; the viewer is
	// closed either way by serve or here.
	upgrade := websocket.New(func(ws *websocket.Conn) { serveBrowser(ws, conn) }, websocket.Config{AllowEmptyOrigin: true})
	if err := upgrade(c); err != nil {
		conn.Close()
		return err
	}
	return nil
}

// serveBrowser copies frames both ways until either side ends. It runs on
// the hijacked connection's goroutine.
func serveBrowser(ws *websocket.Conn, conn hosting.BrowserConn) {
	defer ws.Close()
	defer conn.Close()
	ws.SetReadLimit(hosting.MaxBrowserMessage)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{}, 2)
	// browser → web: the one writer of ws.
	go func() {
		defer func() { done <- struct{}{} }()
		for {
			msg, err := conn.Read(ctx)
			if err != nil {
				reason := "the browser session ended"
				if !errors.Is(err, io.EOF) && ctx.Err() == nil {
					reason = err.Error()
					slog.Debug("browser viewer", "err", err)
				}
				_ = ws.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseNormalClosure, truncate(reason)), time.Now().Add(time.Second))
				return
			}
			_ = ws.SetWriteDeadline(time.Now().Add(browserWrite))
			if err := ws.WriteMessage(websocket.TextMessage, msg); err != nil {
				return
			}
		}
	}()
	// web → browser
	go func() {
		defer func() { done <- struct{}{} }()
		for {
			_, msg, err := ws.ReadMessage()
			if err != nil {
				return
			}
			if err := conn.Write(ctx, msg); err != nil {
				return
			}
		}
	}()
	<-done
	cancel()
	conn.Close()
	_ = ws.SetReadDeadline(time.Now()) // unblocks the reader
	<-done
}

// truncate keeps a close reason within the protocol's 123 bytes.
func truncate(s string) string {
	if len(s) > 123 {
		return s[:123]
	}
	return s
}

// sameOrigin: no Origin is fine (not a browser; the CLI); otherwise its
// host must equal the request's Host, case-insensitively (as the crew
// hub checks /crew/ws).
func sameOrigin(origin, host string) bool {
	if origin == "" {
		return true
	}
	u, err := url.Parse(origin)
	if err != nil {
		return false
	}
	return strings.EqualFold(host, u.Host)
}
