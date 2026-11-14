package dockemail

import (
	"context"
	"crypto/tls"
	"errors"
	"net"
	"net/netip"
	"strconv"
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
}

const (
	defaultPoll    = 60 * time.Second
	defaultRecheck = 30 * time.Second
	dialTimeout    = 15 * time.Second
	ioTimeout      = 60 * time.Second
)

// errPrivate is a refused address; the dialer returns it from its Control.
var errPrivate = errors.New("address is private")

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

// public: a global unicast address that is not private or carrier-grade
// NAT (what the egress proxy of hosted agents allows too).
func public(ip netip.Addr) bool {
	ip = ip.Unmap()
	if !ip.IsGlobalUnicast() || ip.IsPrivate() || ip.IsLinkLocalUnicast() {
		return false
	}
	return !netip.MustParsePrefix("100.64.0.0/10").Contains(ip)
}

// dialTLS opens a TLS connection (implicit TLS) to the endpoint.
func (c Config) dialConn(ctx context.Context, e Endpoint) (net.Conn, error) {
	d := c.dialer()
	return d.DialContext(ctx, "tcp", net.JoinHostPort(e.Host, strconv.Itoa(e.Port)))
}
