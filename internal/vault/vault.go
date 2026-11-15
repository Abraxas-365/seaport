// Package vault is the organization's shared memory: a vault is a named set
// of markdown entries that agents read and write through Seaport instead of
// through whatever their harness keeps on the slot. A vault is granted to
// agents (seen everywhere) and to docks (seen while an agent works a
// conversation of that dock), each grant with a mode, read or write; when
// both reach an agent the widest mode wins. Every save is a version with
// its provenance, so a bad write can be reverted.
package vault

import (
	"regexp"
	"strings"
	"time"

	"github.com/Abraxas-365/seaport/internal/dock"
	"github.com/Abraxas-365/seaport/internal/errx"
	"github.com/Abraxas-365/seaport/internal/identity"
	"github.com/Abraxas-365/seaport/internal/text"
)

const (
	// MaxVaults is the default bound of an organization's vaults.
	MaxVaults = 64
	// MaxEntries bounds the entries of one vault.
	MaxEntries = 2000
	// MaxEntryBody bounds an entry's text (like agent.MaxSkillBody).
	MaxEntryBody = 65536
	// MaxEntryName bounds an entry's (and a vault's) name.
	MaxEntryName = 64
	// MaxDescription bounds a vault's description.
	MaxDescription = 1000
	// MaxSearchHits bounds one search's answer.
	MaxSearchHits = 20
	// MaxIndex is how many entries the `vaults` tool lists per vault
	// (newest first); the rest is found with a search.
	MaxIndex = 200
	// MaxVersions bounds the versions listed for one entry (newest first).
	MaxVersions = 100
	// FirstLineMax bounds the first line shown in an entry's summary.
	FirstLineMax = 160
)

// name is the shape of a vault's and an entry's name: short, lowercase,
// usable in a prompt, a file name and a shell (like dock destinations).
var name = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,63}$`)

// ValidName checks a vault or entry name (the field names the error).
func ValidName(field, value string) error {
	if !name.MatchString(value) {
		return errx.InvalidField(field, field+" must be 1-64 lowercase letters, digits, - or _, starting with a letter or digit")
	}
	return nil
}

// Mode is what a grant allows.
type Mode string

const (
	ModeRead  Mode = "read"
	ModeWrite Mode = "write"
)

func (m Mode) Validate() error {
	if m != ModeRead && m != ModeWrite {
		return errx.InvalidField("mode", "mode must be read or write")
	}
	return nil
}

// Allows reports whether the mode permits reading (write false) or
// writing (write true); write includes read.
func (m Mode) Allows(write bool) bool {
	if write {
		return m == ModeWrite
	}
	return m == ModeRead || m == ModeWrite
}

// Vault is a named set of entries of an organization.
type Vault struct {
	ID           identity.VaultID        `db:"id" json:"id"`
	Organization identity.OrganizationID `db:"organization_id" json:"-"`
	// Name is what agents call it (unique per organization).
	Name        string `db:"name" json:"name"`
	Title       string `db:"title" json:"title"`
	Description string `db:"description" json:"description"`
	// Entries counts its entries (filled when listed or found).
	Entries   int       `db:"entries" json:"entries"`
	CreatedAt time.Time `db:"created_at" json:"created_at"`
	UpdatedAt time.Time `db:"updated_at" json:"updated_at"`
}

// Create is the input of Commands.Create.
type Create struct {
	Name        string
	Title       string
	Description string
}

func (c Create) Validate() error {
	if err := ValidName("name", c.Name); err != nil {
		return err
	}
	if err := text.Optional("title", c.Title, text.MaxName); err != nil {
		return err
	}
	return text.Optional("description", c.Description, MaxDescription)
}

// Update is the input of Commands.Update: nil keeps a field.
type Update struct {
	Title       *string
	Description *string
}

func (u Update) Validate() error {
	if u.Title == nil && u.Description == nil {
		return errx.Validation("nothing to update: give title or description")
	}
	if u.Title != nil {
		if err := text.Optional("title", *u.Title, text.MaxName); err != nil {
			return err
		}
	}
	if u.Description != nil {
		return text.Optional("description", *u.Description, MaxDescription)
	}
	return nil
}

// Grant is a mode on a vault, to an agent or a dock (the table says which).
type Grant struct {
	Vault identity.VaultID `db:"vault_id" json:"vault_id"`
	Mode  Mode             `db:"mode" json:"mode"`
}

// AgentGrant is a vault granted to an agent, as the vault's detail lists it.
type AgentGrant struct {
	Agent     identity.AgentID `db:"agent_id" json:"agent_id"`
	Name      string           `db:"name" json:"name"`
	Mode      Mode             `db:"mode" json:"mode"`
	CreatedAt time.Time        `db:"created_at" json:"created_at"`
}

// DockGrant is a vault granted to a dock.
type DockGrant struct {
	Dock      identity.DockID `db:"dock_id" json:"dock_id"`
	Name      string          `db:"name" json:"name"`
	Channel   dock.Channel    `db:"channel" json:"channel"`
	Mode      Mode            `db:"mode" json:"mode"`
	CreatedAt time.Time       `db:"created_at" json:"created_at"`
}

// Grants is who a vault is granted to.
type Grants struct {
	Agents []AgentGrant `json:"agents"`
	Docks  []DockGrant  `json:"docks"`
}

// Access is a vault with the mode one caller (an agent, a dock) has on it.
type Access struct {
	Vault Vault `json:"vault"`
	Mode  Mode  `json:"mode"`
}

// Resolve merges the grants an agent holds itself with those of the dock it
// is working (nil for none): one grant per vault, the widest mode, in the
// order the vaults first appear (the agent's first).
func Resolve(agentGrants, dockGrants []Grant) []Grant {
	out := make([]Grant, 0, len(agentGrants)+len(dockGrants))
	at := map[identity.VaultID]int{}
	for _, g := range append(append([]Grant{}, agentGrants...), dockGrants...) {
		if g.Mode.Validate() != nil {
			continue
		}
		i, seen := at[g.Vault]
		switch {
		case !seen:
			at[g.Vault] = len(out)
			out = append(out, g)
		case g.Mode.Allows(true):
			out[i].Mode = ModeWrite
		}
	}
	return out
}

// ProvenanceKind says who wrote an entry.
type ProvenanceKind string

const (
	WrittenByAgent ProvenanceKind = "agent"
	WrittenByUser  ProvenanceKind = "user"
)

// Provenance is where a write came from: always who, and for an agent the
// conversation and dock it was working.
type Provenance struct {
	Kind ProvenanceKind `db:"-" json:"kind"`
	// By is the agent's name or the user's name.
	By           string                   `db:"-" json:"by"`
	Conversation *identity.ConversationID `db:"-" json:"conversation_id,omitempty"`
	Dock         *identity.DockID         `db:"-" json:"dock_id,omitempty"`
}

func (p Provenance) Validate() error {
	if p.Kind != WrittenByAgent && p.Kind != WrittenByUser {
		return errx.InvalidField("provenance", "provenance kind must be agent or user")
	}
	return text.Optional("provenance", p.By, text.MaxName)
}

// Entry is one markdown note of a vault.
type Entry struct {
	ID           identity.EntryID        `json:"id"`
	Organization identity.OrganizationID `json:"-"`
	Vault        identity.VaultID        `json:"vault_id"`
	// Name is unique within the vault.
	Name string `json:"name"`
	Body string `json:"body"`
	// Version is the current one (1 at creation, +1 per save).
	Version    int        `json:"version"`
	Provenance Provenance `json:"provenance"`
	CreatedAt  time.Time  `json:"created_at"`
	UpdatedAt  time.Time  `json:"updated_at"`
}

// Summary is an entry without its body: its first line instead.
type Summary struct {
	ID        identity.EntryID `json:"id"`
	Name      string           `json:"name"`
	FirstLine string           `json:"first_line"`
	Version   int              `json:"version"`
	// Provenance is the last write's.
	Provenance Provenance `json:"provenance"`
	UpdatedAt  time.Time  `json:"updated_at"`
}

// Version is one saved state of an entry.
type Version struct {
	Entry      identity.EntryID `json:"entry_id"`
	Version    int              `json:"version"`
	Name       string           `json:"name"`
	Body       string           `json:"body"`
	Provenance Provenance       `json:"provenance"`
	CreatedAt  time.Time        `json:"created_at"`
}

// Hit is one search result: where it is and a snippet around the match.
type Hit struct {
	Vault      identity.VaultID `json:"vault_id"`
	VaultName  string           `json:"vault"`
	Entry      identity.EntryID `json:"entry_id"`
	Name       string           `json:"name"`
	Snippet    string           `json:"snippet"`
	Version    int              `json:"version"`
	Provenance Provenance       `json:"provenance"`
	UpdatedAt  time.Time        `json:"updated_at"`
	Rank       float64          `json:"-"`
}

// SaveEntry is the input of Commands.SaveEntry (and Writer.WriteAs).
type SaveEntry struct {
	Vault identity.VaultID
	Name  string
	Body  string
	// Base is the version being edited: 0 creates (Conflict when the name
	// exists), else it must be the entry's current version (Conflict when
	// someone saved meanwhile, as profiles do).
	Base int
	// Provenance is filled by the caller (the service for an agent), not
	// client input.
	Provenance Provenance
}

func (s SaveEntry) Validate() error {
	if s.Vault.IsZero() {
		return errx.InvalidField("vault_id", "vault_id is required")
	}
	if err := ValidName("name", s.Name); err != nil {
		return err
	}
	if s.Base < 0 {
		return errx.InvalidField("base", "base must be 0 (a new entry) or the entry's current version")
	}
	if err := ValidBody(s.Body); err != nil {
		return err
	}
	return s.Provenance.Validate()
}

// ValidBody checks an entry's text: bounded, no control characters and no
// credential-looking string (the refusal names the kind, never the match).
func ValidBody(body string) error {
	if err := text.Required("body", body, MaxEntryBody); err != nil {
		return err
	}
	for _, p := range text.Secrets {
		if p.Re.MatchString(body) {
			return errx.InvalidField("body", "body looks like "+p.What+": keep secrets out of vaults (give the agent an env secret instead)")
		}
	}
	return nil
}

// FirstLine is an entry's summary line: the first non-blank line of its
// body, at most FirstLineMax runes.
func FirstLine(body string) string {
	line, _, _ := strings.Cut(strings.TrimSpace(body), "\n")
	line = strings.TrimSpace(line)
	if rs := []rune(line); len(rs) > FirstLineMax {
		line = string(rs[:FirstLineMax])
	}
	return line
}

// Revert is the input of Commands.Revert: the entry gets the body of an
// older version as a new version.
type Revert struct {
	Vault      identity.VaultID
	Entry      identity.EntryID
	Version    int
	Provenance Provenance
}

func (r Revert) Validate() error {
	if r.Vault.IsZero() {
		return errx.InvalidField("vault_id", "vault_id is required")
	}
	if r.Entry.IsZero() {
		return errx.InvalidField("entry_id", "entry_id is required")
	}
	if r.Version < 1 {
		return errx.InvalidField("version", "version must be at least 1")
	}
	return r.Provenance.Validate()
}
