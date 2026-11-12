package crewws

import (
	"context"
	"sync/atomic"
	"time"

	"github.com/gofiber/contrib/v3/websocket"
)

// wsConn is the hub's side of one agent's WebSocket (Fiber's contrib
// package over fasthttp/websocket). Two things differ from the library the
// hub used before, and this type carries them:
//
//   - Data writes are not safe for concurrent callers. The hub has exactly
//     one writer per connection (conn.writer: everything else queues frames
//     on conn.send), so that holds without a lock. Control frames and Close
//     may be called from any goroutine.
//   - Reads take no context: cancelling ctx unblocks a pending read by
//     moving its deadline to now (read), the way the old reads were cut.
type wsConn struct {
	c *websocket.Conn
	// pings sent and pongs received: a ping still unanswered when the next
	// one is due means a dead link (ping).
	pings int64
	pongs atomic.Int64
}

func newWSConn(c *websocket.Conn, readLimit int64, ctx context.Context) *wsConn {
	w := &wsConn{c: c}
	c.SetReadLimit(readLimit)
	c.SetPongHandler(func(string) error { w.pongs.Add(1); return nil })
	go func() {
		<-ctx.Done()
		_ = c.SetReadDeadline(time.Now())
	}()
	return w
}

// read waits for the next message at most timeout (0: no limit) or until
// ctx ends.
func (w *wsConn) read(ctx context.Context, timeout time.Duration) ([]byte, error) {
	var deadline time.Time
	if timeout > 0 {
		deadline = time.Now().Add(timeout)
	}
	_ = w.c.SetReadDeadline(deadline)
	// After setting it: a cancel that fired before is seen here, one that
	// fires later moves the deadline itself.
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	_, raw, err := w.c.ReadMessage()
	return raw, err
}

// write sends one text frame within timeout (the writer goroutine only).
func (w *wsConn) write(raw []byte, timeout time.Duration) error {
	_ = w.c.SetWriteDeadline(time.Now().Add(timeout))
	return w.c.WriteMessage(websocket.TextMessage, raw)
}

// ping sends a ping; false when the previous one was never answered.
func (w *wsConn) ping(timeout time.Duration) bool {
	if w.pongs.Load() < w.pings {
		return false
	}
	w.pings++
	return w.c.WriteControl(websocket.PingMessage, nil, time.Now().Add(timeout)) == nil
}

// maxReason is the longest close reason the protocol carries.
const maxReason = 123

// closeWith sends a close frame with code and reason, then drops the
// socket. The peer sees a CloseError with both.
func (w *wsConn) closeWith(code int, reason string) {
	if len(reason) > maxReason {
		reason = reason[:maxReason]
	}
	_ = w.c.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(code, reason), time.Now().Add(time.Second))
	_ = w.c.Close()
}
