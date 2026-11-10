// Package hostingegress is the egress proxy of hosted slots: the only way
// out of the runtime network besides the port itself. A slot signs in with
// its slot token (Proxy-Authorization, from HTTPS_PROXY's userinfo) and
// reaches only its environment's egress hosts and repo hosts; tunnels
// (CONNECT) and plain http are both checked, every refusal is recorded.
package hostingegress

import (
	"context"
	"encoding/base64"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Abraxas-365/seaport/internal/errx"
	"github.com/Abraxas-365/seaport/internal/hosting"
	"github.com/Abraxas-365/seaport/internal/httpx"
	"github.com/Abraxas-365/seaport/internal/identity"
)

// Proxy serves HTTP proxy requests (CONNECT and absolute-URI http).
type Proxy struct {
	commands hosting.Commands
	tokens   httpx.CrewResolver
	cfg      Config
	dialer   net.Dialer

	mu      sync.Mutex
	allow   map[string]allowance // by slot token
	denials map[string]time.Time // by org/agent/slot/host: last recorded
}

// Config tunes the proxy.
type Config struct {
	// Private lets slots reach private, loopback and link-local addresses
	// (a host on the allowlist resolving into the port's own network).
	Private bool
	// Lookup resolves a host (default net.DefaultResolver; tests).
	Lookup func(ctx context.Context, host string) ([]netip.Addr, error)
}

type allowance struct {
	org     identity.OrganizationID
	caller  hosting.SlotCaller
	hosts   []string
	expires time.Time
}

func New(commands hosting.Commands, tokens httpx.CrewResolver, cfg Config) *Proxy {
	p := &Proxy{commands: commands, tokens: tokens, cfg: cfg, allow: map[string]allowance{}, denials: map[string]time.Time{}}
	p.dialer = net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}
	return p
}

// Server is the proxy's HTTP server on addr.
func (p *Proxy) Server(addr string) *http.Server {
	return &http.Server{Addr: addr, Handler: p, ReadHeaderTimeout: 10 * time.Second, IdleTimeout: 120 * time.Second}
}

func (p *Proxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	a, err := p.authorize(r)
	if err != nil {
		w.Header().Set("Proxy-Authenticate", `Basic realm="seaport"`)
		http.Error(w, "seaport egress: "+err.Error(), http.StatusProxyAuthRequired)
		return
	}
	host, port := r.URL.Hostname(), r.URL.Port()
	if r.Method == http.MethodConnect {
		host, port, err = net.SplitHostPort(r.Host)
		if err != nil {
			http.Error(w, "seaport egress: bad CONNECT target", http.StatusBadRequest)
			return
		}
	} else if r.URL.Scheme != "http" || host == "" {
		http.Error(w, "seaport egress: only CONNECT and http:// requests", http.StatusBadRequest)
		return
	}
	if port == "" {
		port = "80"
	}
	host = strings.TrimSuffix(strings.ToLower(host), ".")
	if !hosting.AllowedHost(host, a.hosts) {
		p.deny(r.Context(), a, host)
		http.Error(w, "seaport egress: "+host+" is not in the environment's network.egress", http.StatusForbidden)
		return
	}
	conn, err := p.dial(r.Context(), host, port)
	if err != nil {
		slog.Info("egress: dial", "host", host, "err", err)
		status := http.StatusBadGateway
		if errors.Is(err, errPrivate) {
			status = http.StatusForbidden
			p.deny(r.Context(), a, host)
		}
		http.Error(w, "seaport egress: "+err.Error(), status)
		return
	}
	if r.Method == http.MethodConnect {
		p.tunnel(w, conn)
		return
	}
	p.forward(w, r, conn)
}

// authorize resolves the slot token of Proxy-Authorization (Basic, any
// user name: the slot's) to its allowlist.
func (p *Proxy) authorize(r *http.Request) (allowance, error) {
	token := proxyToken(r.Header.Get("Proxy-Authorization"))
	if token == "" {
		return allowance{}, errors.New("sign in with the slot's token")
	}
	p.mu.Lock()
	a, ok := p.allow[token]
	p.mu.Unlock()
	if ok && time.Now().Before(a.expires) {
		return a, nil
	}
	pr, err := p.tokens.ResolveCrew(r.Context(), token)
	if err != nil {
		return allowance{}, errors.New("the token was refused")
	}
	if pr.Slot == 0 || pr.Agent.IsZero() {
		return allowance{}, errors.New("only a hosted slot's token can use the egress proxy")
	}
	a = allowance{org: pr.Organization, caller: hosting.SlotCaller{Agent: pr.Agent, Slot: pr.Slot}}
	if a.hosts, err = p.commands.Egress(r.Context(), pr.Organization, a.caller); err != nil {
		if errx.IsType(err, errx.TypeNotFound) {
			return allowance{}, errors.New("the slot is gone")
		}
		return allowance{}, errors.New("could not read the slot's network")
	}
	a.expires = time.Now().Add(hosting.EgressCacheFor)
	p.mu.Lock()
	if len(p.allow) > 10000 { // tokens of slots long stopped
		clear(p.allow)
	}
	p.allow[token] = a
	p.mu.Unlock()
	return a, nil
}

// proxyToken is the password of a Basic Proxy-Authorization (or a Bearer
// token).
func proxyToken(h string) string {
	if t, ok := strings.CutPrefix(h, "Bearer "); ok {
		return strings.TrimSpace(t)
	}
	raw, ok := strings.CutPrefix(h, "Basic ")
	if !ok {
		return ""
	}
	dec, err := base64.StdEncoding.DecodeString(strings.TrimSpace(raw))
	if err != nil {
		return ""
	}
	_, pass, _ := strings.Cut(string(dec), ":")
	return pass
}

// deny records a refusal (at most once per DenialEvery per slot and host).
func (p *Proxy) deny(ctx context.Context, a allowance, host string) {
	key := a.org.String() + "/" + a.caller.Agent.String() + "/" + strconv.Itoa(a.caller.Slot) + "/" + host
	now := time.Now()
	p.mu.Lock()
	last, seen := p.denials[key]
	if seen && now.Sub(last) < hosting.DenialEvery {
		p.mu.Unlock()
		return
	}
	if len(p.denials) > 10000 {
		clear(p.denials)
	}
	p.denials[key] = now
	p.mu.Unlock()
	slog.Info("egress: denied", "agent", a.caller.Agent, "slot", a.caller.Slot, "host", host)
	rctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	if err := p.commands.RecordDenial(rctx, a.org, hosting.Denied{Caller: a.caller, Host: host}); err != nil {
		slog.Warn("egress: record denial", "err", err)
	}
}

var errPrivate = errors.New("the host resolves to a private address")

// dial connects to host:port, refusing private, loopback and link-local
// addresses unless Config.Private (the allowlist names hosts, and a name
// must not lead into the port's own network).
func (p *Proxy) dial(ctx context.Context, host, port string) (net.Conn, error) {
	lookup := p.cfg.Lookup
	if lookup == nil {
		lookup = func(ctx context.Context, host string) ([]netip.Addr, error) {
			return net.DefaultResolver.LookupNetIP(ctx, "ip", host)
		}
	}
	addrs, err := lookup(ctx, host)
	if err != nil {
		return nil, errors.New("cannot resolve " + host)
	}
	var last error = errors.New("no address for " + host)
	for _, ip := range addrs {
		ip = ip.Unmap()
		if !p.cfg.Private && !public(ip) {
			last = errPrivate
			continue
		}
		c, err := p.dialer.DialContext(ctx, "tcp", net.JoinHostPort(ip.String(), port))
		if err == nil {
			return c, nil
		}
		last = err
	}
	return nil, last
}

func public(ip netip.Addr) bool {
	return ip.IsGlobalUnicast() && !ip.IsPrivate() && !cgnat.Contains(ip)
}

// cgnat is 100.64.0.0/10 (shared address space, not routed publicly).
var cgnat = netip.MustParsePrefix("100.64.0.0/10")

func (p *Proxy) tunnel(w http.ResponseWriter, up net.Conn) {
	rc := http.NewResponseController(w)
	w.WriteHeader(http.StatusOK)
	if err := rc.Flush(); err != nil {
		up.Close()
		return
	}
	down, buf, err := rc.Hijack()
	if err != nil {
		up.Close()
		return
	}
	if n := buf.Reader.Buffered(); n > 0 {
		b, _ := buf.Peek(n)
		if _, err := up.Write(b); err != nil {
			up.Close()
			down.Close()
			return
		}
	}
	done := make(chan struct{})
	go func() {
		_, _ = io.Copy(up, down)
		if tc, ok := up.(*net.TCPConn); ok {
			_ = tc.CloseWrite()
		}
		close(done)
	}()
	_, _ = io.Copy(down, up)
	_ = down.Close()
	<-done
	_ = up.Close()
}

// forward sends a plain http request over conn (the checked connection)
// and copies the answer back.
func (p *Proxy) forward(w http.ResponseWriter, r *http.Request, conn net.Conn) {
	t := &http.Transport{
		DialContext:       func(context.Context, string, string) (net.Conn, error) { return conn, nil },
		DisableKeepAlives: true,
		Proxy:             nil,
	}
	defer t.CloseIdleConnections()
	out := r.Clone(r.Context())
	out.RequestURI = ""
	out.Header.Del("Proxy-Authorization")
	out.Header.Del("Proxy-Connection")
	resp, err := t.RoundTrip(out)
	if err != nil {
		http.Error(w, "seaport egress: "+err.Error(), http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()
	for k, vs := range resp.Header {
		for _, v := range vs {
			w.Header().Add(k, v)
		}
	}
	w.WriteHeader(resp.StatusCode)
	_, _ = io.Copy(w, resp.Body)
}
