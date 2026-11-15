// Package hostinggateway is the port's model gateway: a hosted slot calls
// its model provider here with its own slot token as the key; the gateway
// swaps in the real key (the environment's models: a secret of the
// environment, else the agent's), forwards the call, streams the answer
// back and meters the tokens it used. The key never enters the container.
// It speaks two protocols (hosting.Providers): Anthropic's Messages API and
// the OpenAI one (Chat Completions, Responses, embeddings), which OpenAI,
// OpenRouter, Groq and DeepSeek share.
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
	"github.com/Abraxas-365/seaport/internal/httpx/stdhttp"
	"github.com/Abraxas-365/seaport/internal/identity"
)

// Limits of one call.
const (
	maxRequest = hosting.MaxGatewayBody // images and documents travel inline
	maxMetered = 16 << 20               // a JSON answer read for its usage
)

// Gateway serves GatewayPath/<provider>/... behind crew auth.
type Gateway struct {
	commands hosting.Commands
	// upstreams are the providers' base URLs.
	upstreams map[string]*url.URL
	transport http.RoundTripper
}

// Config: the providers' base URLs by provider (missing: their public
// APIs, hosting.Provider.Upstream).
type Config struct {
	Upstreams map[string]string
	// Transport overrides http.DefaultTransport (tests).
	Transport http.RoundTripper
}

func New(commands hosting.Commands, cfg Config) (*Gateway, error) {
	upstreams := map[string]*url.URL{}
	for name, p := range hosting.Providers {
		raw := cfg.Upstreams[name]
		if raw == "" {
			raw = p.Upstream
		}
		u, err := url.Parse(strings.TrimRight(raw, "/"))
		if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
			return nil, errx.Validation("the " + name + " gateway URL must be an http(s) URL")
		}
		upstreams[name] = u
	}
	for name := range cfg.Upstreams {
		if _, ok := hosting.Providers[name]; !ok {
			return nil, errx.Validation("the gateway serves no provider " + name)
		}
	}
	t := cfg.Transport
	if t == nil {
		// no proxy from the environment: the port reaches providers itself
		dt := http.DefaultTransport.(*http.Transport).Clone()
		dt.Proxy = nil
		t = dt
	}
	return &Gateway{commands: commands, upstreams: upstreams, transport: t}, nil
}

// allowed are the paths a slot may call on a provider of the protocol
// (anything else is 404: the gateway is not an open relay to the
// provider's whole API). Anthropic's are under /v1 (the slot's base URL
// is the provider's root); the OpenAI family's base URL already ends in
// /v1, so theirs come bare.
func allowed(protocol, path string) bool {
	switch protocol {
	case hosting.ProtocolAnthropic:
		switch path {
		case "/v1/messages", "/v1/messages/count_tokens", "/v1/models":
			return true
		}
		return strings.HasPrefix(path, "/v1/models/") || path == "/v1/files" || strings.HasPrefix(path, "/v1/files/")
	case hosting.ProtocolOpenAI:
		switch path {
		case "/chat/completions", "/responses", "/embeddings", "/models":
			return true
		}
		return strings.HasPrefix(path, "/models/") || strings.HasPrefix(path, "/responses/")
	}
	return false
}

// metered: the answers whose usage is recorded.
func metered(protocol, method, path string) bool {
	if method != http.MethodPost {
		return false
	}
	switch protocol {
	case hosting.ProtocolAnthropic:
		return path == "/v1/messages"
	case hosting.ProtocolOpenAI:
		return path == "/chat/completions" || path == "/responses" || path == "/embeddings"
	}
	return false
}

func (g *Gateway) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	rest := strings.TrimPrefix(r.URL.Path, hosting.GatewayPath+"/")
	provider, path, _ := strings.Cut(rest, "/")
	path = "/" + path
	upstream, ok := g.upstreams[provider]
	spec := hosting.Providers[provider]
	if !ok || !allowed(spec.Protocol, path) {
		writeError(w, spec.Protocol, http.StatusNotFound, "not_found_error", "the gateway does not serve "+r.URL.Path)
		return
	}
	p, err := stdhttp.Caller(r)
	if err != nil {
		writeError(w, spec.Protocol, http.StatusUnauthorized, "authentication_error", "missing or invalid slot token")
		return
	}
	caller := hosting.SlotCaller{Agent: p.Agent, Slot: p.Slot}
	key, err := g.commands.ModelKey(r.Context(), p.Organization, hosting.KeyRequest{Caller: caller, Provider: provider})
	if err != nil {
		writeErrx(w, r, spec.Protocol, err)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxRequest)
	meter := metered(spec.Protocol, r.Method, path)
	proxy := &httputil.ReverseProxy{
		Transport:     g.transport,
		FlushInterval: -1, // streamed answers go out as they come
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.Out.URL.Scheme, pr.Out.URL.Host = upstream.Scheme, upstream.Host
			pr.Out.URL.Path, pr.Out.URL.RawPath = upstream.Path+path, ""
			pr.Out.Host = upstream.Host
			h := pr.Out.Header
			for _, k := range []string{"Authorization", "X-Api-Key", "Cookie", "Accept-Encoding", "Proxy-Authorization"} {
				h.Del(k) // the transport decompresses, so usage can be read
			}
			if spec.Protocol == hosting.ProtocolAnthropic {
				h.Set("X-Api-Key", key.Key)
			} else {
				h.Set("Authorization", "Bearer "+key.Key)
			}
		},
		ModifyResponse: func(resp *http.Response) error {
			if meter && resp.StatusCode == http.StatusOK {
				resp.Body = g.meter(r.Context(), resp, spec.Protocol, p.Organization, p.Agent, provider)
			}
			return nil
		},
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			slog.Warn("model gateway: upstream", "provider", provider, "err", err)
			writeError(w, spec.Protocol, http.StatusBadGateway, "api_error", "the port could not reach "+provider)
		},
	}
	proxy.ServeHTTP(w, r)
}

// meter wraps an answer's body: it reads the usage as the body streams
// through and records it once the body is done.
func (g *Gateway) meter(ctx context.Context, resp *http.Response, protocol string, org identity.OrganizationID, agentID identity.AgentID, provider string) io.ReadCloser {
	m := &meter{body: resp.Body, openai: protocol == hosting.ProtocolOpenAI,
		stream: strings.HasPrefix(resp.Header.Get("Content-Type"), "text/event-stream")}
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

// meter reads an answer's usage. Anthropic: the JSON body's model and
// usage, or the stream's message_start (model, input tokens) and
// message_delta (cumulative output tokens) events. OpenAI protocol: the
// JSON body's model and usage (prompt/completion or input/output tokens,
// cached prompt tokens), or in a stream the chunk that carries usage (Chat
// Completions' last one with stream_options.include_usage; Responses'
// response.completed); a stream without one records nothing.
type meter struct {
	body   io.ReadCloser
	stream bool
	openai bool
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
			if m.openai {
				m.openAIEvent(m.buf)
			} else {
				var msg message
				if json.Unmarshal(m.buf, &msg) == nil {
					m.usage.Model = msg.Model
					msg.Usage.add(&m.usage, false)
				}
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
	if m.openai {
		m.openAIEvent(data)
		return
	}
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

// openAIEvent reads the usage of an OpenAI-protocol JSON body or stream
// chunk: a Chat Completions / embeddings object (model, usage) or a
// Responses event (response.completed carries response.model and
// response.usage). Counts are totals, so the last chunk with usage wins.
func (m *meter) openAIEvent(data []byte) {
	var ev struct {
		Model    string       `json:"model"`
		Usage    *openAIUsage `json:"usage"`
		Response *struct {
			Model string       `json:"model"`
			Usage *openAIUsage `json:"usage"`
		} `json:"response"`
	}
	if json.Unmarshal(data, &ev) != nil {
		return
	}
	if ev.Response != nil && ev.Usage == nil {
		ev.Model, ev.Usage = ev.Response.Model, ev.Response.Usage
	}
	if ev.Usage == nil {
		return
	}
	if ev.Model != "" {
		m.usage.Model = ev.Model
	}
	ev.Usage.add(&m.usage)
}

type openAIUsage struct {
	Prompt     *int64 `json:"prompt_tokens"`
	Completion *int64 `json:"completion_tokens"`
	Input      *int64 `json:"input_tokens"`
	Output     *int64 `json:"output_tokens"`
	// Cached prompt tokens: Chat Completions' prompt_tokens_details,
	// Responses' input_tokens_details.
	PromptDetails *struct {
		Cached *int64 `json:"cached_tokens"`
	} `json:"prompt_tokens_details"`
	InputDetails *struct {
		Cached *int64 `json:"cached_tokens"`
	} `json:"input_tokens_details"`
}

// add sets the totals: Chat Completions names them prompt/completion,
// Responses input/output; cached tokens are counted in the input, so they
// are recorded as cache reads and taken out of it.
func (u openAIUsage) add(t *hosting.Usage) {
	in, out := u.Prompt, u.Completion
	if in == nil {
		in = u.Input
	}
	if out == nil {
		out = u.Output
	}
	var cached *int64
	if u.PromptDetails != nil {
		cached = u.PromptDetails.Cached
	} else if u.InputDetails != nil {
		cached = u.InputDetails.Cached
	}
	if in != nil {
		t.Input = *in
		if cached != nil && *cached <= *in {
			t.CacheRead = *cached
			t.Input -= *cached
		}
	}
	if out != nil {
		t.Output = *out
	}
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
func writeErrx(w http.ResponseWriter, r *http.Request, protocol string, err error) {
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
	writeError(w, protocol, status, kind, "seaport gateway: "+msg)
}

// writeError answers in the protocol's error shape: Anthropic's
// {"type":"error","error":{type,message}}, OpenAI's
// {"error":{message,type,code}} (an unknown provider gets Anthropic's).
func writeError(w http.ResponseWriter, protocol string, status int, kind, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if protocol == hosting.ProtocolOpenAI {
		_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]any{"message": msg, "type": kind, "code": nil}})
		return
	}
	_ = json.NewEncoder(w).Encode(map[string]any{"type": "error", "error": map[string]string{"type": kind, "message": msg}})
}
