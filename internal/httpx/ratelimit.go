package httpx

import (
	"math"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/Abraxas-365/seaport/internal/errx"
	"golang.org/x/time/rate"
)

// RateLimit is a token bucket per caller (organization + role + subject +
// credential, so two crew tokens never share one; agents sharing a token
// do): perSecond requests per second with bursts of burst. It runs after
// the Authenticator (it needs the Principal), so unauthenticated requests
// are refused before they cost a bucket.
type RateLimit struct {
	perSecond rate.Limit
	burst     int
	now       func() time.Time

	mu        sync.Mutex
	buckets   map[string]*bucket
	lastSweep time.Time
}

type bucket struct {
	lim  *rate.Limiter
	seen time.Time
}

// NewRateLimit returns nil (no limit) when perSecond ≤ 0; burst below
// perSecond is raised to it.
func NewRateLimit(perSecond, burst int) *RateLimit {
	if perSecond <= 0 {
		return nil
	}
	return &RateLimit{perSecond: rate.Limit(perSecond), burst: max(burst, perSecond), now: time.Now,
		buckets: map[string]*bucket{}}
}

// Wrap refuses a caller over its rate with 429 + Retry-After. A nil
// RateLimit passes everything through.
func (l *RateLimit) Wrap(next http.Handler) http.Handler {
	if l == nil {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p, ok := PrincipalFrom(r.Context())
		if !ok {
			next.ServeHTTP(w, r)
			return
		}
		key := p.Organization.String() + "\x00" + string(p.Role) + "\x00" + p.Subject + "\x00" + p.Credential
		if wait := l.take(key); wait > 0 {
			s := int(math.Ceil(wait.Seconds()))
			WriteError(w, r, errx.TooManyRequests("too many requests; retry in "+strconv.Itoa(s)+"s").
				WithDetail("retry_after_seconds", s))
			return
		}
		next.ServeHTTP(w, r)
	})
}

// take spends one token of key's bucket, or returns how long until one is
// available (nothing spent).
func (l *RateLimit) take(key string) time.Duration {
	now := l.now()
	l.mu.Lock()
	defer l.mu.Unlock()
	l.sweep(now)
	b, ok := l.buckets[key]
	if !ok {
		b = &bucket{lim: rate.NewLimiter(l.perSecond, l.burst)}
		l.buckets[key] = b
	}
	b.seen = now
	res := b.lim.ReserveN(now, 1)
	if wait := res.DelayFrom(now); wait > 0 {
		res.CancelAt(now)
		return wait
	}
	return 0
}

// sweep drops buckets idle long enough to have refilled (a fresh bucket is
// the same), at most once a minute, so the map holds only recent callers.
func (l *RateLimit) sweep(now time.Time) {
	if now.Sub(l.lastSweep) < time.Minute {
		return
	}
	l.lastSweep = now
	full := time.Duration(float64(l.burst)/float64(l.perSecond)*float64(time.Second)) + time.Second
	for k, b := range l.buckets {
		if now.Sub(b.seen) > full {
			delete(l.buckets, k)
		}
	}
}
