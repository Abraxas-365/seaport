package dockemail

import (
	"context"
	"crypto/tls"
	"errors"
	"net"
	"net/netip"
	"path"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// Config tunes the gateway.
type Config struct {
	// Private lets a dock name servers on private, loopback or link-local
	// addresses (SEAPORT_EMAIL_PRIVATE). Off, a dock may reach public
	// servers only: the host is checked where it is connected (every
	// address its name resolves to), so a name that points inside is no
	// way around it.
	Private bool
	// TLS is the client TLS configuration (tests: their own roots).
	TLS *tls.Config
	// Poll is how often a server without IDLE is asked for mail (default
	// 60 s); Recheck how often a session looks at its account for a change
	// made on another process (default 30 s).
	Poll, Recheck time.Duration
	// Hosts is the allow-list of server names a dock may name
	// (SEAPORT_EMAIL_HOSTS): globs where * matches any run of characters,
	// case-insensitive. Empty means every name.
	Hosts []string
	// AllowUnverified lets mail through whose sender the mailbox's own
	// server did not vouch for (no Authentication-Results header, or no
	// SPF/DKIM pass in it); it is marked in the text. Off (the default),
	// such mail is skipped (SEAPORT_EMAIL_REQUIRE_AUTH=false turns it on).
	AllowUnverified bool
}

const (
	defaultPoll    = 60 * time.Second
	defaultRecheck = 30 * time.Second
	dialTimeout    = 15 * time.Second
	ioTimeout      = 60 * time.Second
)

// errPrivate is a refused address; the dialer returns it from its Control.
var errPrivate = errors.New("address is private")

// errHost is a server name the operator's allow-list does not include.
var errHost = errors.New("mail server name is not allowed on this port")

// hostAllowed says whether the allow-list names host (no list: all).
func hostAllowed(hosts []string, host string) bool {
	host = strings.ToLower(strings.TrimSpace(host))
	any := false
	for _, h := range hosts {
		h = strings.ToLower(strings.TrimSpace(h))
		if h == "" {
			continue
		}
		any = true
		if ok, err := path.Match(h, host); err == nil && ok {
			return true
		}
	}
	return !any
}

func (c Config) poll() time.Duration {
	if c.Poll > 0 {
		return c.Poll
	}
	return defaultPoll
}

func (c Config) recheck() time.Duration {
	if c.Recheck > 0 {
		return c.Recheck
	}
	return defaultRecheck
}

func (c Config) tls(host string) *tls.Config {
	var t *tls.Config
	if c.TLS != nil {
		t = c.TLS.Clone()
	} else {
		t = &tls.Config{MinVersion: tls.VersionTLS12}
	}
	t.ServerName = host
	return t
}

// dialer connects, refusing private addresses at connect time.
func (c Config) dialer() *net.Dialer {
	return &net.Dialer{Timeout: dialTimeout, KeepAlive: 30 * time.Second,
		Control: func(_, address string, _ syscall.RawConn) error {
			if c.Private {
				return nil
			}
			ap, err := netip.ParseAddrPort(address)
			if err != nil {
				return errPrivate
			}
			if !public(ap.Addr()) {
				return errPrivate
			}
			return nil
		}}
}

// notPublic are ranges that are global-unicast to Go but not public
// servers: carrier-grade NAT, and the IPv6 forms that embed an IPv4
// address (NAT64, 6to4) and so can reach inside.
var notPublic = []netip.Prefix{
	netip.MustParsePrefix("100.64.0.0/10"),
	netip.MustParsePrefix("64:ff9b::/96"),
	netip.MustParsePrefix("64:ff9b:1::/48"),
	netip.MustParsePrefix("2002::/16"),
}

// public: a global unicast address that is not private, carrier-grade
// NAT or an embedded IPv4 form (what the egress proxy of hosted agents
// allows too).
func public(ip netip.Addr) bool {
	ip = ip.Unmap()
	if !ip.IsGlobalUnicast() || ip.IsPrivate() || ip.IsLinkLocalUnicast() {
		return false
	}
	for _, p := range notPublic {
		if p.Contains(ip) {
			return false
		}
	}
	return true
}

// dialTLS opens a TLS connection (implicit TLS) to the endpoint.
func (c Config) dialConn(ctx context.Context, e Endpoint) (net.Conn, error) {
	if !hostAllowed(c.Hosts, e.Host) {
		return nil, errHost
	}
	d := c.dialer()
	return d.DialContext(ctx, "tcp", net.JoinHostPort(e.Host, strconv.Itoa(e.Port)))
}
