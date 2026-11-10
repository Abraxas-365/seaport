package hosting

import (
	"strings"
	"time"

	"github.com/Abraxas-365/seaport/internal/errx"
	"github.com/Abraxas-365/seaport/internal/identity"
)

// Model providers the gateway serves (Spec.Models).
const ProviderAnthropic = "anthropic"

// GatewayPath is where the port serves its model gateway: a slot's
// harness calls GatewayPath/<provider>/... with its slot token as the key.
const GatewayPath = "/gateway"

// Environment of a slot's container for the network (besides Env*):
// EnvGateway is the gateway's base URL (set when the environment names a
// model key; the base image's harness config points the provider there).
const (
	EnvGateway = "SEAPORT_GATEWAY"
)

// Egress proxy settings.
const (
	// EgressCacheFor is how long the proxy trusts a slot token's allowlist
	// before asking again (a new environment version reaches running
	// slots within it).
	EgressCacheFor = 30 * time.Second
	// DenialEvery throttles recording one slot's denials of one host.
	DenialEvery = time.Minute
	// MaxDenials is how many denied hosts a slot's list shows.
	MaxDenials = 50
)

// SlotCaller is a slot's own credential, as the gateway and the egress
// proxy see it (from its crew token; the organization goes apart).
type SlotCaller struct {
	Agent identity.AgentID
	Slot  int
}

func (c SlotCaller) Validate() error {
	if c.Agent.IsZero() || c.Slot < 1 {
		return errx.Forbidden("only a hosted slot's own token can use this")
	}
	return nil
}

// ModelKey is a provider key the gateway forwards with.
type ModelKey struct {
	Provider string
	Key      string
}

// KeyRequest is the input of Commands.ModelKey.
type KeyRequest struct {
	Caller   SlotCaller
	Provider string
}

func (k KeyRequest) Validate() error {
	if err := k.Caller.Validate(); err != nil {
		return err
	}
	if k.Provider != ProviderAnthropic {
		return errx.NotFound("the gateway serves no provider " + k.Provider)
	}
	return nil
}

// Usage is what one model response consumed (the gateway reads it from
// the provider's answer).
type Usage struct {
	Agent    identity.AgentID
	Provider string
	Model    string
	// Tokens: input (uncached), output, cache writes and reads.
	Input, Output, CacheWrite, CacheRead int64
}

func (u Usage) Validate() error {
	if u.Agent.IsZero() {
		return errx.InvalidField("agent_id", "agent_id is required")
	}
	if u.Provider == "" || len(u.Provider) > 32 {
		return errx.InvalidField("provider", "provider is required")
	}
	if len(u.Model) > 200 {
		return errx.InvalidField("model", "model is too long")
	}
	if u.Input < 0 || u.Output < 0 || u.CacheWrite < 0 || u.CacheRead < 0 {
		return errx.InvalidField("usage", "token counts can't be negative")
	}
	return nil
}

// Empty: nothing to record.
func (u Usage) Empty() bool {
	return u.Input == 0 && u.Output == 0 && u.CacheWrite == 0 && u.CacheRead == 0
}

// UsageDay is an agent's model use of one day (UTC) with one model.
type UsageDay struct {
	Day        time.Time `db:"day" json:"day"`
	Provider   string    `db:"provider" json:"provider"`
	Model      string    `db:"model" json:"model"`
	Requests   int64     `db:"requests" json:"requests"`
	Input      int64     `db:"input_tokens" json:"input_tokens"`
	Output     int64     `db:"output_tokens" json:"output_tokens"`
	CacheWrite int64     `db:"cache_write_tokens" json:"cache_write_tokens"`
	CacheRead  int64     `db:"cache_read_tokens" json:"cache_read_tokens"`
}

// UsageQuery picks an agent's usage days.
type UsageQuery struct {
	// Days back from today (UTC), today included: 1..MaxUsageDays.
	Days int
}

// MaxUsageDays bounds UsageQuery.Days.
const MaxUsageDays = 366

func (q UsageQuery) Validate() error {
	if q.Days < 1 || q.Days > MaxUsageDays {
		return errx.InvalidField("days", "days must be between 1 and 366")
	}
	return nil
}

// Denial is a host a slot tried to reach and the egress proxy refused.
type Denial struct {
	Slot     int       `db:"idx" json:"slot"`
	Host     string    `db:"host" json:"host"`
	Count    int64     `db:"count" json:"count"`
	LastSeen time.Time `db:"last_seen" json:"last_seen"`
}

// Denied is the input of Commands.RecordDenial.
type Denied struct {
	Caller SlotCaller
	Host   string
}

func (d Denied) Validate() error {
	if err := d.Caller.Validate(); err != nil {
		return err
	}
	if d.Host == "" || len(d.Host) > 253 {
		return errx.InvalidField("host", "host is required")
	}
	return nil
}

// AllowedHost reports whether host matches the allowlist: equal, or a
// subdomain of a "*.example.com" entry.
func AllowedHost(host string, allow []string) bool {
	host = strings.TrimSuffix(strings.ToLower(host), ".")
	for _, a := range allow {
		if a == host || (strings.HasPrefix(a, "*.") && strings.HasSuffix(host, a[1:])) {
			return true
		}
	}
	return false
}
