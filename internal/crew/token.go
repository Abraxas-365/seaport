package crew

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"strings"
	"time"

	"github.com/Abraxas-365/seaport/internal/errx"
	"github.com/Abraxas-365/seaport/internal/identity"
	"github.com/Abraxas-365/seaport/internal/text"
)

// Crew tokens are the credentials agents connect with (`seaport connect`,
// /crew/ws, /mcp). Each belongs to one agent of one organization: its
// connections work as that agent (only the ships routed to it) and its MCP
// calls act only there. The value is shown once at creation; Seaport
// stores its SHA-256.
const (
	// TokenPrefix starts every crew token value (recognizable in logs and
	// secret scanners).
	TokenPrefix = "sp_crew_"
	// MaxTokens bounds an organization's active tokens.
	MaxTokens = 100
	// MaxTokenLifetime bounds an expiring token.
	MaxTokenLifetime = 5 * 365 * 24 * time.Hour
	// hintLength is how much of the value is kept to recognize it.
	hintLength = len(TokenPrefix) + 4
)

// Token is a crew token (never its value).
type Token struct {
	ID           identity.CrewTokenID    `db:"id" json:"id"`
	Organization identity.OrganizationID `db:"organization_id" json:"-"`
	Name         string                  `db:"name" json:"name"`
	// Agent is who connects with it; AgentName its name (display).
	Agent     identity.AgentID `db:"agent_id" json:"agent_id"`
	AgentName string           `db:"agent_name" json:"agent_name"`
	// Hint is the start of the value ("sp_crew_AbCd"), to tell tokens apart.
	Hint string `db:"hint" json:"hint"`
	// CreatedBy is who created it (the admin's name, else its subject).
	CreatedBy  string     `db:"created_by" json:"created_by"`
	CreatedAt  time.Time  `db:"created_at" json:"created_at"`
	ExpiresAt  *time.Time `db:"expires_at" json:"expires_at"`
	LastUsedAt *time.Time `db:"last_used_at" json:"last_used_at"`
}

// Expired reports whether the token is past its expiry at now.
func (t Token) Expired(now time.Time) bool { return t.ExpiresAt != nil && !now.Before(*t.ExpiresAt) }

// TokenFilter narrows TokenQueries.List.
type TokenFilter struct {
	Agent *identity.AgentID
}

// Issued is a newly created token with its value (returned once).
type Issued struct {
	Token
	Value string `json:"value"`
}

// CreateToken is the input of TokenCommands.Create.
type CreateToken struct {
	Name string
	// Agent is the agent it connects as (required, must exist).
	Agent identity.AgentID
	// ExpiresAt, when set, must be in the future and within
	// MaxTokenLifetime.
	ExpiresAt *time.Time
	// CreatedBy is filled from the caller (not client input).
	CreatedBy string
}

func (c CreateToken) Validate() error {
	if err := text.Required("name", c.Name, text.MaxName); err != nil {
		return err
	}
	if c.Agent.IsZero() {
		return errx.InvalidField("agent_id", "agent_id is required: a crew token connects as an agent")
	}
	if c.ExpiresAt != nil {
		now := time.Now()
		if !c.ExpiresAt.After(now) {
			return errx.InvalidField("expires_at", "expires_at must be in the future")
		}
		if c.ExpiresAt.After(now.Add(MaxTokenLifetime)) {
			return errx.InvalidField("expires_at", "expires_at must be within 5 years")
		}
	}
	return text.Optional("created_by", c.CreatedBy, text.MaxName)
}

// NewTokenValue returns a fresh token value, its stored hash and its hint.
func NewTokenValue() (value, hash, hint string) {
	var b [32]byte
	_, _ = rand.Read(b[:])
	value = TokenPrefix + base64.RawURLEncoding.EncodeToString(b[:])
	return value, HashToken(value), value[:hintLength]
}

// HashToken is how a value is stored and looked up.
func HashToken(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}

// LooksLikeToken: a value that could be a crew token (cheap pre-check
// before a database lookup).
func LooksLikeToken(value string) bool {
	return strings.HasPrefix(value, TokenPrefix) && len(value) == len(TokenPrefix)+43
}
