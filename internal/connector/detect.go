package connector

import (
	"context"
	_ "embed"
	"encoding/json"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"

	"github.com/Abraxas-365/seaport/internal/agent"
	"github.com/Abraxas-365/seaport/internal/errx"
)

// detectScript is the page detector (detect.js): evaluated on a page it
// answers {host, path, kind} with kind login | captcha | 2fa | "".
//
//go:embed detect.js
var detectScript string

// pageHit is a page the detector says needs a person.
type pageHit struct {
	Host string             `json:"host"`
	Path string             `json:"path"`
	Kind agent.TakeoverKind `json:"kind"`
}

// Detector timing: a page is read shortly after it loads and once more
// when its scripts have had time to render a widget; a lost CDP socket is
// dialed again while the browser lives.
var (
	detectSettle  = 1500 * time.Millisecond
	detectRecheck = 5 * time.Second
	detectRetry   = 3 * time.Second
)

// watchPages reads every page of the browser (one passive CDP session,
// nothing is written to the pages) and calls hit for each one that needs
// a person, until ctx ends or the browser exits. The browser MCP of the
// harness has its own CDP connection; Chromium takes several.
func watchPages(ctx context.Context, b *Browser, hit func(pageHit)) {
	for {
		err := watchOnce(ctx, b, hit)
		select {
		case <-ctx.Done():
			return
		case <-b.Done():
			return
		default:
		}
		if err != nil {
			slog.Warn("browser detector lost the browser; retrying", "err", err, "in", detectRetry)
		}
		select {
		case <-time.After(detectRetry):
		case <-ctx.Done():
			return
		case <-b.Done():
			return
		}
	}
}

func watchOnce(ctx context.Context, b *Browser, hit func(pageHit)) error {
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
	w := &pageWatcher{c: c, hit: hit, timers: map[string]*time.Timer{}}
	c.onEvent = w.event
	// Flat mode: every page target is attached and its events come with
	// a sessionId on this one socket; pages opened later attach too.
	if _, err := c.call(ctx, "", "Target.setAutoAttach", map[string]any{"autoAttach": true, "waitForDebuggerOnStart": false, "flatten": true}); err != nil {
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
	w.stop()
	return c.err()
}

// pageWatcher turns CDP events into evaluations of the detector.
type pageWatcher struct {
	c   *cdpClient
	hit func(pageHit)

	mu     sync.Mutex
	timers map[string]*time.Timer // session → pending evaluation
}

func (w *pageWatcher) event(method, session string, params json.RawMessage) {
	switch method {
	case "Target.attachedToTarget":
		var p struct {
			SessionID  string `json:"sessionId"`
			TargetInfo struct {
				Type string `json:"type"`
				URL  string `json:"url"`
			} `json:"targetInfo"`
		}
		if json.Unmarshal(params, &p) != nil || p.TargetInfo.Type != "page" {
			return
		}
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			if _, err := w.c.call(ctx, p.SessionID, "Page.enable", nil); err != nil {
				return
			}
			// The page may be loaded already (the browser was up before us).
			w.schedule(p.SessionID)
		}()
	case "Target.detachedFromTarget":
		var p struct {
			SessionID string `json:"sessionId"`
		}
		if json.Unmarshal(params, &p) == nil {
			w.cancel(p.SessionID)
		}
	case "Page.loadEventFired", "Page.navigatedWithinDocument":
		w.schedule(session)
	case "Page.frameNavigated":
		// A new main document: whatever was scheduled is for the old one.
		var p struct {
			Frame struct {
				ParentID string `json:"parentId"`
			} `json:"frame"`
		}
		if json.Unmarshal(params, &p) == nil && p.Frame.ParentID == "" {
			w.cancel(session)
		}
	}
}

// schedule reads the page after detectSettle and once more after
// detectRecheck (coalescing events in between).
func (w *pageWatcher) schedule(session string) {
	if session == "" {
		return
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if t := w.timers[session]; t != nil {
		t.Stop()
	}
	w.timers[session] = time.AfterFunc(detectSettle, func() {
		w.evaluate(session)
		w.mu.Lock()
		w.timers[session] = time.AfterFunc(detectRecheck-detectSettle, func() { w.evaluate(session) })
		w.mu.Unlock()
	})
}

func (w *pageWatcher) cancel(session string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if t := w.timers[session]; t != nil {
		t.Stop()
		delete(w.timers, session)
	}
}

func (w *pageWatcher) stop() {
	w.mu.Lock()
	defer w.mu.Unlock()
	for s, t := range w.timers {
		t.Stop()
		delete(w.timers, s)
	}
}

// evaluate runs the detector on the page and reports a hit.
func (w *pageWatcher) evaluate(session string) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	raw, err := w.c.call(ctx, session, "Runtime.evaluate", map[string]any{"expression": detectScript, "returnByValue": true})
	if err != nil {
		return // the page went away, or the socket did
	}
	var res struct {
		Result struct {
			Value pageHit `json:"value"`
		} `json:"result"`
		ExceptionDetails *struct {
			Text string `json:"text"`
		} `json:"exceptionDetails"`
	}
	if json.Unmarshal(raw, &res) != nil || res.ExceptionDetails != nil {
		return
	}
	h := res.Result.Value
	if h.Kind == "" || h.Host == "" {
		return
	}
	h.Host = strings.ToLower(h.Host)
	if h.Path == "" {
		h.Path = "/"
	}
	if len(h.Path) > agent.MaxTakeoverPath {
		h.Path = h.Path[:agent.MaxTakeoverPath]
	}
	w.hit(h)
}

// cdpClient is a minimal CDP client over one WebSocket: calls get their
// answer by id, events go to onEvent (with the page session they belong
// to, "" for the browser's own).
type cdpClient struct {
	ws      *websocket.Conn
	onEvent func(method, session string, params json.RawMessage)

	mu      sync.Mutex
	next    int64
	pending map[int64]chan cdpReply
	done    chan struct{}
	failure error
}

type cdpReply struct {
	Result json.RawMessage
	Err    error
}

type cdpMessage struct {
	ID        int64           `json:"id,omitempty"`
	SessionID string          `json:"sessionId,omitempty"`
	Method    string          `json:"method,omitempty"`
	Params    any             `json:"params,omitempty"`
	Result    json.RawMessage `json:"result,omitempty"`
	Error     *struct {
		Message string `json:"message"`
	} `json:"error,omitempty"`
}

func dialCDP(ctx context.Context, wsURL string) (*cdpClient, error) {
	ws, _, err := websocket.Dial(ctx, wsURL, nil)
	if err != nil {
		return nil, errx.Wrap(err, "connect to the browser", errx.TypeBusiness)
	}
	ws.SetReadLimit(maxCDPMessage)
	c := &cdpClient{ws: ws, pending: map[int64]chan cdpReply{}, done: make(chan struct{})}
	go c.read(ctx)
	return c, nil
}

func (c *cdpClient) read(ctx context.Context) {
	defer close(c.done)
	for {
		_, raw, err := c.ws.Read(ctx)
		if err != nil {
			c.fail(err)
			return
		}
		var m cdpMessage
		if json.Unmarshal(raw, &m) != nil {
			continue
		}
		if m.ID != 0 {
			c.mu.Lock()
			ch := c.pending[m.ID]
			delete(c.pending, m.ID)
			c.mu.Unlock()
			if ch != nil {
				r := cdpReply{Result: m.Result}
				if m.Error != nil {
					r.Err = errx.External("cdp: " + m.Error.Message)
				}
				ch <- r
			}
			continue
		}
		if m.Method != "" && c.onEvent != nil {
			params, _ := json.Marshal(m.Params)
			c.onEvent(m.Method, m.SessionID, params)
		}
	}
}

func (c *cdpClient) fail(err error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.failure == nil {
		c.failure = err
	}
	for id, ch := range c.pending {
		ch <- cdpReply{Err: errx.External("cdp: connection closed")}
		delete(c.pending, id)
	}
}

func (c *cdpClient) err() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.failure
}

// call sends method (to session, or the browser when "") and waits for its
// answer.
func (c *cdpClient) call(ctx context.Context, session, method string, params any) (json.RawMessage, error) {
	ch := make(chan cdpReply, 1)
	c.mu.Lock()
	if c.failure != nil {
		c.mu.Unlock()
		return nil, errx.External("cdp: connection closed")
	}
	c.next++
	id := c.next
	c.pending[id] = ch
	c.mu.Unlock()
	raw, _ := json.Marshal(cdpMessage{ID: id, SessionID: session, Method: method, Params: params})
	if err := c.ws.Write(ctx, websocket.MessageText, raw); err != nil {
		c.mu.Lock()
		delete(c.pending, id)
		c.mu.Unlock()
		return nil, errx.Wrap(err, "cdp "+method, errx.TypeExternal)
	}
	select {
	case r := <-ch:
		return r.Result, r.Err
	case <-ctx.Done():
		c.mu.Lock()
		delete(c.pending, id)
		c.mu.Unlock()
		return nil, ctx.Err()
	}
}

func (c *cdpClient) close() {
	_ = c.ws.Close(websocket.StatusNormalClosure, "")
}
