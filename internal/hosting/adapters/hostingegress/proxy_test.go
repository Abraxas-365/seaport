package hostingegress_test

import (
	"bufio"
	"context"
	"encoding/base64"
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

	"github.com/Abraxas-365/seaport/internal/errx"
	"github.com/Abraxas-365/seaport/internal/hosting"
	"github.com/Abraxas-365/seaport/internal/hosting/adapters/hostingegress"
	"github.com/Abraxas-365/seaport/internal/httpx"
	"github.com/Abraxas-365/seaport/internal/identity"
)

type commands struct {
	hosting.Commands
	mu      sync.Mutex
	hosts   []string
	asked   int
	denials []hosting.Denied
}

func (c *commands) Egress(_ context.Context, _ identity.OrganizationID, caller hosting.SlotCaller) ([]string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.asked++
	if caller.Slot == 9 {
		return nil, errx.NotFound("slot not found")
	}
	return c.hosts, nil
}

func (c *commands) RecordDenial(_ context.Context, _ identity.OrganizationID, d hosting.Denied) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.denials = append(c.denials, d)
	return nil
}

type tokens map[string]httpx.Principal

func (t tokens) ResolveCrew(_ context.Context, v string) (httpx.Principal, error) {
	if p, ok := t[v]; ok {
		return p, nil
	}
	return httpx.Principal{}, errx.Unauthorized("bad token")
}

var org = identity.MustParseOrganizationID("00000000-0000-0000-0000-000000000001")

// world: every name resolves to the test server's loopback address (the
// proxy is told private addresses are fine, except where a test says).
func setup(t *testing.T, private bool, hosts ...string) (*commands, string, string) {
	t.Helper()
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, "hello from %s%s", r.Host, r.URL.Path)
	}))
	t.Cleanup(target.Close)
	_, port, _ := net.SplitHostPort(target.Listener.Addr().String())
	c := &commands{hosts: hosts}
	agentID := identity.NewAgentID()
	tk := tokens{
		"slot":   {Organization: org, Role: httpx.RoleCrew, Agent: agentID, Slot: 1},
		"person": {Organization: org, Role: httpx.RoleCrew, Agent: agentID},
		"gone":   {Organization: org, Role: httpx.RoleCrew, Agent: agentID, Slot: 9},
	}
	p := hostingegress.New(c, tk, hostingegress.Config{Private: private,
		Lookup: func(context.Context, string) ([]netip.Addr, error) {
			return []netip.Addr{netip.MustParseAddr("127.0.0.1")}, nil
		}})
	srv := httptest.NewServer(p)
	t.Cleanup(srv.Close)
	return c, srv.URL, port
}

func client(proxy, user, token string) *http.Client {
	u, _ := url.Parse(proxy)
	u.User = url.UserPassword(user, token)
	return &http.Client{Transport: &http.Transport{Proxy: http.ProxyURL(u)}}
}

func get(t *testing.T, c *http.Client, u string) (int, string) {
	t.Helper()
	resp, err := c.Get(u)
	if err != nil {
		return 0, err.Error()
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

func TestProxyAllowsOnlyTheEnvironmentsHosts(t *testing.T) {
	c, proxy, port := setup(t, true, "api.example.com", "*.acme.dev")
	cl := client(proxy, "a-1", "slot")
	if st, body := get(t, cl, "http://api.example.com:"+port+"/x"); st != 200 || !strings.Contains(body, "hello from api.example.com") {
		t.Fatalf("allowed host: %d %s", st, body)
	}
	if st, _ := get(t, cl, "http://git.acme.dev:"+port+"/"); st != 200 {
		t.Fatalf("wildcard host: %d", st)
	}
	if st, body := get(t, cl, "http://evil.com:"+port+"/"); st != 403 || !strings.Contains(body, "network.egress") {
		t.Fatalf("other host: %d %s", st, body)
	}
	if st, _ := get(t, cl, "http://acme.dev:"+port+"/"); st != 403 {
		t.Fatalf("a wildcard doesn't cover its apex: %d", st)
	}
	// a second refusal of the same host within a minute isn't recorded again
	get(t, cl, "http://evil.com:"+port+"/")
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.denials) != 2 || c.denials[0].Host != "evil.com" || c.denials[1].Host != "acme.dev" || c.denials[0].Caller.Slot != 1 {
		t.Fatalf("denials: %+v", c.denials)
	}
	if c.asked != 1 {
		t.Fatalf("the allowlist is cached per token: asked %d times", c.asked)
	}
}

func TestProxyTunnelsAllowedHosts(t *testing.T) {
	c, proxy, port := setup(t, true, "api.example.com")
	connect := func(token, target string) string {
		conn, err := net.Dial("tcp", strings.TrimPrefix(proxy, "http://"))
		if err != nil {
			t.Fatal(err)
		}
		defer conn.Close()
		auth := base64.StdEncoding.EncodeToString([]byte("a-1:" + token))
		fmt.Fprintf(conn, "CONNECT %s HTTP/1.1\r\nHost: %s\r\nProxy-Authorization: Basic %s\r\n\r\n", target, target, auth)
		br := bufio.NewReader(conn)
		resp, err := http.ReadResponse(br, nil)
		if err != nil {
			t.Fatal(err)
		}
		if resp.StatusCode != 200 {
			return resp.Status
		}
		// speak plain HTTP through the tunnel
		fmt.Fprintf(conn, "GET /t HTTP/1.1\r\nHost: api.example.com\r\nConnection: close\r\n\r\n")
		r2, err := http.ReadResponse(br, nil)
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(r2.Body)
		return string(b)
	}
	if got := connect("slot", "api.example.com:"+port); !strings.Contains(got, "hello from api.example.com/t") {
		t.Fatalf("tunnel: %s", got)
	}
	if got := connect("slot", "evil.com:443"); !strings.HasPrefix(got, "403") {
		t.Fatalf("tunnel to other host: %s", got)
	}
	if got := connect("nope", "api.example.com:"+port); !strings.HasPrefix(got, "407") {
		t.Fatalf("bad token: %s", got)
	}
	if got := connect("person", "api.example.com:"+port); !strings.HasPrefix(got, "407") {
		t.Fatalf("a person's token: %s", got)
	}
	if got := connect("gone", "api.example.com:"+port); !strings.HasPrefix(got, "407") {
		t.Fatalf("a gone slot: %s", got)
	}
	_ = c
}

func TestProxyRefusesPrivateAddresses(t *testing.T) {
	c, proxy, port := setup(t, false, "api.example.com")
	if st, body := get(t, client(proxy, "a-1", "slot"), "http://api.example.com:"+port+"/"); st != 403 || !strings.Contains(body, "private") {
		t.Fatalf("an allowed name resolving to loopback: %d %s", st, body)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.denials) != 1 {
		t.Fatalf("denials: %+v", c.denials)
	}
}

// "*" opens the public web (search, any page) but never private addresses.
func TestProxyAnyHost(t *testing.T) {
	_, proxy, port := setup(t, true, hosting.AnyHost)
	cl := client(proxy, "a-1", "slot")
	for _, h := range []string{"html.duckduckgo.com", "example.org", "a.b.example.net"} {
		if st, body := get(t, cl, "http://"+h+":"+port+"/q"); st != 200 || !strings.Contains(body, "hello from "+h) {
			t.Fatalf("%s through *: %d %s", h, st, body)
		}
	}
	c, proxy, port := setup(t, false, hosting.AnyHost)
	if st, body := get(t, client(proxy, "a-1", "slot"), "http://internal.example.com:"+port+"/"); st != 403 || !strings.Contains(body, "private") {
		t.Fatalf("* reached a private address: %d %s", st, body)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.denials) != 1 {
		t.Fatalf("denials: %+v", c.denials)
	}
}

// With egress "*" any name may resolve anywhere, so the address check is
// the boundary: special-purpose ranges and IPv6 forms that embed a private
// IPv4 address are refused.
func TestPublicAddresses(t *testing.T) {
	for addr, want := range map[string]bool{
		"93.184.215.14": true, "2606:2800:21f:cb07:6820:80da:af6b:8b2c": true,
		"10.1.2.3": false, "127.0.0.1": false, "169.254.169.254": false, "100.64.0.1": false,
		"0.1.2.3": false, "192.0.0.8": false, "198.18.0.1": false, "240.0.0.1": false, "255.255.255.255": false,
		"192.0.2.1": false, "::1": false, "fe80::1": false, "fd00::1": false,
		"::ffff:10.0.0.1": false, "::ffff:93.184.215.14": true,
		"64:ff9b::a9fe:a9fe": false, "64:ff9b::5db8:d70e": true, "64:ff9b:1::1": false,
		"2002:a00:1::1": false, "2002:5db8:d70e::1": true, "2001::1": false, "2001:db8::1": false,
	} {
		if got := hostingegress.Public(netip.MustParseAddr(addr)); got != want {
			t.Errorf("public(%s) = %v, want %v", addr, got, want)
		}
	}
}

func TestProxyNeedsAToken(t *testing.T) {
	_, proxy, port := setup(t, true, "api.example.com")
	cl := &http.Client{Transport: &http.Transport{Proxy: func(*http.Request) (*url.URL, error) { return url.Parse(proxy) }}}
	if st, _ := get(t, cl, "http://api.example.com:"+port+"/"); st != 407 {
		t.Fatalf("no token: %d", st)
	}
}
