package hostinggateway_test

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/Abraxas-365/seaport/internal/errx"
	"github.com/Abraxas-365/seaport/internal/hosting"
	"github.com/Abraxas-365/seaport/internal/hosting/adapters/hostinggateway"
	"github.com/Abraxas-365/seaport/internal/httpx"
	"github.com/Abraxas-365/seaport/internal/identity"
)

// commands is the part of hosting.Commands the gateway calls.
type commands struct {
	hosting.Commands
	mu    sync.Mutex
	key   string
	err   error
	usage []hosting.Usage
}

func (c *commands) ModelKey(_ context.Context, _ identity.OrganizationID, in hosting.KeyRequest) (hosting.ModelKey, error) {
	if err := in.Validate(); err != nil {
		return hosting.ModelKey{}, err
	}
	return hosting.ModelKey{Provider: in.Provider, Key: c.key}, c.err
}

func (c *commands) RecordUsage(_ context.Context, _ identity.OrganizationID, u hosting.Usage) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.usage = append(c.usage, u)
	return nil
}

func (c *commands) recorded() []hosting.Usage {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]hosting.Usage(nil), c.usage...)
}

var agentID = identity.NewAgentID()

func serve(t *testing.T, c *commands, upstream http.HandlerFunc, slot int) *httptest.Server {
	t.Helper()
	up := httptest.NewServer(upstream)
	t.Cleanup(up.Close)
	gw, err := hostinggateway.New(c, hostinggateway.Config{Anthropic: up.URL + "/base"})
	if err != nil {
		t.Fatal(err)
	}
	h := hostinggateway.APIKeyAsBearer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer sp_crew_slot" {
			http.Error(w, "no", http.StatusUnauthorized)
			return
		}
		ctx := httpx.WithPrincipal(r.Context(), httpx.Principal{Organization: identity.MustParseOrganizationID("00000000-0000-0000-0000-000000000001"),
			Role: httpx.RoleCrew, Agent: agentID, Slot: slot})
		gw.ServeHTTP(w, r.WithContext(ctx))
	}))
	s := httptest.NewServer(h)
	t.Cleanup(s.Close)
	return s
}

func call(t *testing.T, s *httptest.Server, path, body string) (*http.Response, string) {
	t.Helper()
	req, _ := http.NewRequest(http.MethodPost, s.URL+path, strings.NewReader(body))
	req.Header.Set("X-Api-Key", "sp_crew_slot")
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp, string(b)
}

func TestGatewaySwapsTheKeyAndMeters(t *testing.T) {
	c := &commands{key: "sk-ant-real"}
	var got http.Header
	var path string
	s := serve(t, c, func(w http.ResponseWriter, r *http.Request) {
		got, path = r.Header.Clone(), r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"m","model":"claude-x","usage":{"input_tokens":12,"output_tokens":5,"cache_read_input_tokens":3}}`)
	}, 1)
	resp, body := call(t, s, "/gateway/anthropic/v1/messages", `{"model":"claude-x"}`)
	if resp.StatusCode != 200 || !strings.Contains(body, `"claude-x"`) {
		t.Fatalf("answer: %d %s", resp.StatusCode, body)
	}
	if path != "/base/v1/messages" || got.Get("X-Api-Key") != "sk-ant-real" || got.Get("Authorization") != "" {
		t.Fatalf("upstream saw path %q key %q auth %q", path, got.Get("X-Api-Key"), got.Get("Authorization"))
	}
	u := c.recorded()
	if len(u) != 1 || u[0].Model != "claude-x" || u[0].Input != 12 || u[0].Output != 5 || u[0].CacheRead != 3 ||
		u[0].Agent != agentID || u[0].Provider != hosting.ProviderAnthropic {
		t.Fatalf("usage: %+v", u)
	}
}

func TestGatewayMetersStreams(t *testing.T) {
	c := &commands{key: "k"}
	s := serve(t, c, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		f := w.(http.Flusher)
		for _, ev := range []string{
			`event: message_start` + "\n" + `data: {"type":"message_start","message":{"model":"claude-s","usage":{"input_tokens":40,"output_tokens":1,"cache_creation_input_tokens":7}}}`,
			`event: content_block_delta` + "\n" + `data: {"type":"content_block_delta","delta":{"text":"hi"}}`,
			`event: message_delta` + "\n" + `data: {"type":"message_delta","usage":{"output_tokens":9}}`,
			`event: message_delta` + "\n" + `data: {"type":"message_delta","usage":{"output_tokens":21}}`,
			`event: message_stop` + "\n" + `data: {"type":"message_stop"}`,
		} {
			_, _ = io.WriteString(w, ev+"\n\n")
			f.Flush()
		}
	}, 2)
	resp, body := call(t, s, "/gateway/anthropic/v1/messages", `{"stream":true}`)
	if resp.StatusCode != 200 || !strings.Contains(body, "message_stop") {
		t.Fatalf("stream: %d %s", resp.StatusCode, body)
	}
	u := c.recorded()
	if len(u) != 1 || u[0].Model != "claude-s" || u[0].Input != 40 || u[0].Output != 21 || u[0].CacheWrite != 7 {
		t.Fatalf("usage: %+v", u)
	}
}

func TestGatewayRefuses(t *testing.T) {
	hit := false
	up := func(w http.ResponseWriter, r *http.Request) { hit = true }
	// a path outside the allowed API
	c := &commands{key: "k"}
	s := serve(t, c, up, 1)
	if resp, body := call(t, s, "/gateway/anthropic/v1/organizations/x", `{}`); resp.StatusCode != 404 || !strings.Contains(body, `"not_found_error"`) {
		t.Fatalf("other path: %d %s", resp.StatusCode, body)
	}
	if resp, _ := call(t, s, "/gateway/openai/v1/chat/completions", `{}`); resp.StatusCode != 404 {
		t.Fatalf("other provider: %d", resp.StatusCode)
	}
	// a person's token (no slot)
	s0 := serve(t, c, up, 0)
	if resp, body := call(t, s0, "/gateway/anthropic/v1/messages", `{}`); resp.StatusCode != 403 || !strings.Contains(body, "permission_error") {
		t.Fatalf("no slot: %d %s", resp.StatusCode, body)
	}
	// no key in the environment
	c.err = errx.NotFound("environment e names no anthropic key")
	if resp, body := call(t, s, "/gateway/anthropic/v1/messages", `{}`); resp.StatusCode != 404 || !strings.Contains(body, "names no anthropic key") {
		t.Fatalf("no key: %d %s", resp.StatusCode, body)
	}
	if hit {
		t.Fatal("a refused call reached the provider")
	}
	if len(c.recorded()) != 0 {
		t.Fatal("refused calls metered")
	}
}

func TestGatewayDoesNotMeterErrors(t *testing.T) {
	c := &commands{key: "k"}
	s := serve(t, c, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(429)
		_, _ = io.WriteString(w, `{"type":"error","error":{"type":"rate_limit_error","message":"slow down"}}`)
	}, 1)
	resp, body := call(t, s, "/gateway/anthropic/v1/messages", `{}`)
	if resp.StatusCode != 429 || !strings.Contains(body, "rate_limit_error") {
		t.Fatalf("provider error passes through: %d %s", resp.StatusCode, body)
	}
	if len(c.recorded()) != 0 {
		t.Fatal("an error was metered")
	}
}
