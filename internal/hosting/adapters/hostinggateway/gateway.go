// Package hostinggateway is the port's model gateway: a hosted slot calls
// its model provider here with its own slot token as the key; the gateway
// swaps in the agent's real key (the environment's models, an agent
// secret), forwards the call, streams the answer back and meters the
// tokens it used. The key never enters the container.
package hostinggateway

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/Abraxas-365/seaport/internal/errx"
	"github.com/Abraxas-365/seaport/internal/hosting"
	"github.com/Abraxas-365/seaport/internal/httpx"
	"github.com/Abraxas-365/seaport/internal/identity"
)

// Limits of one call.
const (
	maxRequest = 64 << 20 // images and documents travel inline
	maxMetered = 16 << 20 // a JSON answer read for its usage
)

// Gateway serves GatewayPath/<provider>/... behind crew auth.
type Gateway struct {
	commands hosting.Commands
	// upstreams are the providers' base URLs.
	upstreams map[string]*url.URL
	transport http.RoundTripper
}

// Config: the providers' base URLs (default their public APIs).
type Config struct {
	Anthropic string
	// Transport overrides http.DefaultTransport (tests).
	Transport http.RoundTripper
}

// DefaultAnthropic is Anthropic's API.
const DefaultAnthropic = "https://api.anthropic.com"

func New(commands hosting.Commands, cfg Config) (*Gateway, error) {
	if cfg.Anthropic == "" {
		cfg.Anthropic = DefaultAnthropic
	}
	u, err := url.Parse(strings.TrimRight(cfg.Anthropic, "/"))
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
		return nil, errx.Validation("SEAPORT_GATEWAY_ANTHROPIC_URL must be an http(s) URL")
	}
	t := cfg.Transport
	if t == nil {
		// no proxy from the environment: the port reaches providers itself
		dt := http.DefaultTransport.(*http.Transport).Clone()
		dt.Proxy = nil
		t = dt
	}
	return &Gateway{commands: commands, upstreams: map[string]*url.URL{hosting.ProviderAnthropic: u}, transport: t}, nil
}

// APIKeyAsBearer lets harnesses that send their key as x-api-key (the
// Anthropic SDKs) sign in to crew auth: it becomes the bearer.
func APIKeyAsBearer(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if k := r.Header.Get("X-Api-Key"); k != "" && r.Header.Get("Authorization") == "" {
			r.Header.Set("Authorization", "Bearer "+k)
		}
		next.ServeHTTP(w, r)
	})
}

// allowed are the provider paths a slot may call (anything else is 404:
// the gateway is not an open relay to the provider's whole API).
func allowed(path string) bool {
	switch path {
	case "/v1/messages", "/v1/messages/count_tokens", "/v1/models":
		return true
	}
	return strings.HasPrefix(path, "/v1/models/") || path == "/v1/files" || strings.HasPrefix(path, "/v1/files/")
}

func (g *Gateway) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	rest := strings.TrimPrefix(r.URL.Path, hosting.GatewayPath+"/")
	provider, path, _ := strings.Cut(rest, "/")
	path = "/" + path
	upstream, ok := g.upstreams[provider]
	if !ok || !allowed(path) {
		writeError(w, http.StatusNotFound, "not_found_error", "the gateway does not serve "+r.URL.Path)
		return
	}
	p, err := httpx.Caller(r)
	if err != nil {
		writeError(w, http.StatusUnauthorized, "authentication_error", "missing or invalid slot token")
		return
	}
	caller := hosting.SlotCaller{Agent: p.Agent, Slot: p.Slot}
	key, err := g.commands.ModelKey(r.Context(), p.Organization, hosting.KeyRequest{Caller: caller, Provider: provider})
	if err != nil {
		writeErrx(w, r, err)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxRequest)
	metered := r.Method == http.MethodPost && path == "/v1/messages"
	proxy := &httputil.ReverseProxy{
		Transport:     g.transport,
		FlushInterval: -1, // streamed answers go out as they come
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.Out.URL.Scheme, pr.Out.URL.Host = upstream.Scheme, upstream.Host
			pr.Out.URL.Path, pr.Out.URL.RawPath = upstream.Path+path, ""
			pr.Out.Host = upstream.Host
			h := pr.Out.Header
			for _, k := range []string{"Authorization", "Cookie", "Accept-Encoding", "Proxy-Authorization"} {
				h.Del(k) // the transport decompresses, so usage can be read
			}
			h.Set("X-Api-Key", key.Key)
		},
		ModifyResponse: func(resp *http.Response) error {
			if metered && resp.StatusCode == http.StatusOK {
				resp.Body = g.meter(r.Context(), resp, p.Organization, p.Agent, provider)
			}
			return nil
		},
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			slog.Warn("model gateway: upstream", "provider", provider, "err", err)
			writeError(w, http.StatusBadGateway, "api_error", "the port could not reach "+provider)
		},
	}
	proxy.ServeHTTP(w, r)
}

// meter wraps an answer's body: it reads the usage as the body streams
// through and records it once the body is done.
func (g *Gateway) meter(ctx context.Context, resp *http.Response, org identity.OrganizationID, agentID identity.AgentID, provider string) io.ReadCloser {
	m := &meter{body: resp.Body, stream: strings.HasPrefix(resp.Header.Get("Content-Type"), "text/event-stream")}
	m.record = func(u hosting.Usage) {
		u.Agent, u.Provider = agentID, provider
		if u.Empty() {
			return
		}
		rctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
		defer cancel()
		if err := g.commands.RecordUsage(rctx, org, u); err != nil {
			slog.Warn("model gateway: record usage", "agent", agentID, "err", err)
		}
	}
	return m
}

// meter reads Anthropic's usage from a Messages answer: the JSON body's
// model and usage, or the stream's message_start (model, input tokens)
// and message_delta (cumulative output tokens) events.
type meter struct {
	body   io.ReadCloser
	stream bool
	buf    []byte // JSON so far, or the stream's partial line
	usage  hosting.Usage
	once   sync.Once
	record func(hosting.Usage)
}

func (m *meter) Read(p []byte) (int, error) {
	n, err := m.body.Read(p)
	if n > 0 {
		m.feed(p[:n])
	}
	if err == io.EOF {
		m.finish()
	}
	return n, err
}

func (m *meter) Close() error {
	m.finish()
	return m.body.Close()
}

func (m *meter) finish() {
	m.once.Do(func() {
		if !m.stream {
			var msg message
			if json.Unmarshal(m.buf, &msg) == nil {
				m.usage.Model = msg.Model
				msg.Usage.add(&m.usage, false)
			}
		}
		m.buf = nil
		m.record(m.usage)
	})
}

func (m *meter) feed(b []byte) {
	if !m.stream {
		if len(m.buf)+len(b) <= maxMetered {
			m.buf = append(m.buf, b...)
		}
		return
	}
	m.buf = append(m.buf, b...)
	for {
		i := bytes.IndexByte(m.buf, '\n')
		if i < 0 {
			if len(m.buf) > maxMetered {
				m.buf = m.buf[:0] // a line this long carries no usage we read
			}
			return
		}
		line := bytes.TrimRight(m.buf[:i], "\r")
		m.buf = m.buf[i+1:]
		if data, ok := bytes.CutPrefix(line, []byte("data:")); ok {
			m.event(bytes.TrimSpace(data))
		}
	}
}

func (m *meter) event(data []byte) {
	var ev struct {
		Type    string  `json:"type"`
		Message message `json:"message"`
		Usage   usage   `json:"usage"`
	}
	if json.Unmarshal(data, &ev) != nil {
		return
	}
	switch ev.Type {
	case "message_start":
		m.usage.Model = ev.Message.Model
		ev.Message.Usage.add(&m.usage, false)
	case "message_delta":
		ev.Usage.add(&m.usage, true)
	}
}

type message struct {
	Model string `json:"model"`
	Usage usage  `json:"usage"`
}

type usage struct {
	Input      *int64 `json:"input_tokens"`
	Output     *int64 `json:"output_tokens"`
	CacheWrite *int64 `json:"cache_creation_input_tokens"`
	CacheRead  *int64 `json:"cache_read_input_tokens"`
}

// add sets u's counts on the total: message_delta's counts are
// cumulative, so a later event replaces what an earlier one said.
func (u usage) add(t *hosting.Usage, delta bool) {
	set := func(dst *int64, v *int64) {
		if v != nil && (!delta || *v >= *dst) {
			*dst = *v
		}
	}
	set(&t.Input, u.Input)
	set(&t.Output, u.Output)
	set(&t.CacheWrite, u.CacheWrite)
	set(&t.CacheRead, u.CacheRead)
}

// writeErrx answers a typed error in the provider's error shape, so the
// harness shows it.
func writeErrx(w http.ResponseWriter, r *http.Request, err error) {
	status, kind := http.StatusInternalServerError, "api_error"
	msg := errx.GenericMessage
	var e *errx.Error
	if errx.As(err, &e) && e.Type != errx.TypeInternal {
		status, msg = e.HTTPStatus, e.Message
		switch e.Type {
		case errx.TypeNotFound:
			kind = "not_found_error"
		case errx.TypeAuthorization:
			kind = "permission_error"
		default:
			kind = "invalid_request_error"
		}
		if status == 0 {
			status = http.StatusBadRequest
		}
	}
	if status >= 500 {
		slog.Error("model gateway", "path", r.URL.Path, "err", err)
	}
	writeError(w, status, kind, "seaport gateway: "+msg)
}

func writeError(w http.ResponseWriter, status int, kind, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{"type": "error", "error": map[string]string{"type": kind, "message": msg}})
}
