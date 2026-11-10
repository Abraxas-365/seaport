package bootstrap_test

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Abraxas-365/seaport/internal/bootstrap"
	"github.com/Abraxas-365/seaport/internal/config"
	"github.com/Abraxas-365/seaport/internal/hosting"
)

// A hosted slot's network: with models.anthropic its harness calls the
// port's gateway with the slot token, the gateway forwards with the
// agent's key and meters the answer (GET /agents/{id}/usage); the egress
// proxy lets the slot reach only its environment's hosts and records what
// it refused (GET /agents/{id}/hosting/egress).
func TestHostedNetwork(t *testing.T) {
	var mu sync.Mutex
	var seenKey string
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		seenKey = r.Header.Get("X-Api-Key")
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"model":"claude-test","usage":{"input_tokens":100,"output_tokens":20,"cache_read_input_tokens":5}}`)
	}))
	defer provider.Close()
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, "reached %s", r.Host)
	}))
	defer target.Close()
	_, targetPort, _ := net.SplitHostPort(target.Listener.Addr().String())

	rt := &fakeRuntime{results: make(chan string, 4)}
	p := startPortWith(t, func(c *config.Server) {
		c.SecretKey = "e2e-network-secrets-key-0123456789abcdefXYZ"
		c.ImageRness = "registry.test/seaport-rness:1"
		c.AgentURL = "http://port.test:8740"
		c.GatewayAnthropicURL = provider.URL
		c.EgressAddr = "127.0.0.1:0"
		c.EgressPrivate = true // the test's hosts all resolve to loopback
	}, bootstrap.Options{Runtime: rt, SupervisePeriod: time.Hour,
		EgressLookup: func(context.Context, string) ([]netip.Addr, error) {
			return []netip.Addr{netip.MustParseAddr("127.0.0.1")}, nil
		}})
	events := p.openEvents("")
	pm := p.agent("pm")
	agentPath := "/api/v1/agents/" + pm.ID.String()

	spec := map[string]any{"harness": "rness", "models": map[string]string{"anthropic": "${ANTHROPIC_KEY}"},
		"network": map[string]any{"egress": []string{"registry.example.com"}}}
	// The key must be a secret of the agent before it's hosted on the spec.
	if code := p.admin(http.MethodPost, "/api/v1/environments", map[string]any{"name": "net", "spec": spec}, nil); code != http.StatusCreated {
		t.Fatalf("create environment: %d", code)
	}
	if code := p.admin(http.MethodPut, agentPath+"/hosting", map[string]any{"environment": "net", "slots": 1}, nil); code != http.StatusUnprocessableEntity && code != http.StatusBadRequest {
		t.Fatalf("host without the key secret: %d", code)
	}
	if code := p.admin(http.MethodPut, agentPath+"/secrets/ANTHROPIC_KEY", map[string]any{"value": "sk-ant-e2e-real"}, nil); code != http.StatusOK {
		t.Fatalf("set secret: %d", code)
	}
	if code := p.admin(http.MethodPut, agentPath+"/hosting", map[string]any{"environment": "net", "slots": 1}, nil); code != http.StatusOK {
		t.Fatalf("host: %d", code)
	}
	if code := p.admin(http.MethodDelete, agentPath+"/secrets/ANTHROPIC_KEY", nil, nil); code != http.StatusConflict {
		t.Fatalf("delete the model key in use: %d", code)
	}

	rt.results <- "sha256:net1"
	if code := p.admin(http.MethodPost, agentPath+"/hosting/slots/1/start", nil, nil); code != http.StatusAccepted {
		t.Fatalf("start: %d", code)
	}
	next(t, events, hosting.EventSlotsUpdated, slotState(1, hosting.SlotRunning))
	st := rt.lastStart()
	token := st.Env[hosting.EnvToken]
	if st.Env[hosting.EnvGateway] != "http://port.test:8740/gateway" ||
		st.Env["ANTHROPIC_BASE_URL"] != "http://port.test:8740/gateway/anthropic" || st.Env["ANTHROPIC_API_KEY"] != token {
		t.Fatalf("gateway env: %v", st.Env)
	}
	proxyURL, err := url.Parse(st.Env["HTTPS_PROXY"])
	if err != nil || proxyURL.Hostname() != "port.test" || proxyURL.User.Username() != "pm-1" ||
		!strings.Contains(st.Env["NO_PROXY"], "port.test") {
		t.Fatalf("proxy env: %v", st.Env)
	}
	if strings.Contains(fmt.Sprint(st.Env), "sk-ant-e2e-real") {
		t.Fatal("the model key reached the container")
	}

	// The gateway: slot token as x-api-key, real key upstream, usage metered.
	gw := func(token string) (int, string) {
		req, _ := http.NewRequest(http.MethodPost, p.srv.URL+"/gateway/anthropic/v1/messages", strings.NewReader(`{"model":"claude-test"}`))
		req.Header.Set("X-Api-Key", token)
		req.Header.Set("Content-Type", "application/json")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(b)
	}
	for range 2 {
		if code, body := gw(token); code != http.StatusOK || !strings.Contains(body, "claude-test") {
			t.Fatalf("gateway: %d %s", code, body)
		}
	}
	mu.Lock()
	if seenKey != "sk-ant-e2e-real" {
		t.Fatalf("provider saw key %q", seenKey)
	}
	mu.Unlock()
	people := p.issueAs(adminToken, pm.ID, "laptop")
	if code, _ := gw(people.Value); code != http.StatusForbidden {
		t.Fatalf("a person's token on the gateway: %d", code)
	}
	if code, _ := gw(adminToken); code != http.StatusUnauthorized {
		t.Fatalf("the admin token on the gateway: %d", code)
	}
	var usage struct{ Days []hosting.UsageDay }
	eventually(t, "usage metered", func() bool {
		if code := p.admin(http.MethodGet, agentPath+"/usage?days=7", nil, &usage); code != http.StatusOK {
			t.Fatalf("usage: %d", code)
		}
		return len(usage.Days) == 1 && usage.Days[0].Requests == 2
	})
	if u := usage.Days[0]; u.Model != "claude-test" || u.Provider != "anthropic" || u.Input != 200 || u.Output != 40 || u.CacheRead != 10 {
		t.Fatalf("usage: %+v", u)
	}
	if code := p.admin(http.MethodGet, agentPath+"/usage?days=0", nil, nil); code != http.StatusBadRequest {
		t.Fatalf("usage days=0: %d", code)
	}

	// The egress proxy (the test reaches it on loopback, at the URL's port).
	local := *proxyURL
	local.Host = net.JoinHostPort("127.0.0.1", proxyURL.Port())
	client := &http.Client{Transport: &http.Transport{Proxy: http.ProxyURL(&local)}}
	get := func(host string) int {
		resp, err := client.Get("http://" + host + ":" + targetPort + "/")
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp.StatusCode
	}
	if code := get("registry.example.com"); code != http.StatusOK {
		t.Fatalf("allowed host: %d", code)
	}
	if code := get("exfil.example.net"); code != http.StatusForbidden {
		t.Fatalf("other host: %d", code)
	}
	var denials struct{ Denials []hosting.Denial }
	if code := p.admin(http.MethodGet, agentPath+"/hosting/egress", nil, &denials); code != http.StatusOK ||
		len(denials.Denials) != 1 || denials.Denials[0].Host != "exfil.example.net" || denials.Denials[0].Slot != 1 {
		t.Fatalf("denials: %d %+v", code, denials)
	}

	// Stopping the slot revokes its token: the gateway and proxy refuse it.
	if code := p.admin(http.MethodPost, agentPath+"/hosting/slots/1/stop", nil, nil); code != http.StatusAccepted {
		t.Fatalf("stop: %d", code)
	}
	next(t, events, hosting.EventSlotsUpdated, slotState(1, hosting.SlotStopped))
	if code, _ := gw(token); code != http.StatusUnauthorized {
		t.Fatalf("gateway after stop: %d", code)
	}
}
