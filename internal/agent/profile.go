package agent

import (
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/Abraxas-365/seaport/internal/errx"
	"github.com/Abraxas-365/seaport/internal/identity"
	"github.com/Abraxas-365/seaport/internal/text"
)

// Profile limits.
const (
	MaxInstructions  = 32768
	MaxSkills        = 50
	MaxSkillBody     = 65536
	MaxMCPServers    = 20
	MaxHeaders       = 20
	MaxHeaderValue   = 4096
	MaxSecrets       = 50
	MaxSecretValue   = 16384
	MaxOptionValue   = 100
	maxSkillDescribe = 1024
)

// Approve policies: how the connector answers the harness's permission
// requests ("" = the connector's own flag, reject by default).
const (
	ApproveAllow  = "allow"
	ApproveReject = "reject"
)

var (
	skillName  = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,63}$`)
	serverName = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,31}$`)
	secretName = regexp.MustCompile(`^[A-Z_][A-Z0-9_]{0,63}$`)
	headerName = regexp.MustCompile("^[!#$%&'*+.^_`|~0-9A-Za-z-]{1,100}$")
	// secretRef is how an MCP header names a secret: ${NAME}.
	secretRef = regexp.MustCompile(`\$\{([A-Z_][A-Z0-9_]{0,63})\}`)
)

// Profile is one version of an agent's configuration: what the harness it
// runs in is told and given. Versions are immutable; an agent runs its
// current one (Agent.ProfileVersion, 0 = none yet).
type Profile struct {
	Agent   identity.AgentID `json:"agent_id"`
	Version int              `json:"version"`
	// Instructions reach the harness as system-level instructions (ACP
	// systemPrompt, append) or, when it can't take them, ahead of a new
	// session's first prompt.
	Instructions string `json:"instructions"`
	// Skills are served by the port's MCP (`skill` tool): the harness sees
	// their names and descriptions and reads a body when it needs it.
	Skills []Skill `json:"skills"`
	// MCPServers are added to every session next to the port's own.
	MCPServers []MCPServer `json:"mcp_servers"`
	Approve    string      `json:"approve"`
	// Model and Effort are picked among the harness's session config
	// options (ACP categories model / thought_level), when it has them.
	Model     string    `json:"model"`
	Effort    string    `json:"effort"`
	CreatedBy string    `json:"created_by"`
	CreatedAt time.Time `json:"created_at"`
}

// Skill is a SKILL.md: a name, when to use it, and its instructions.
type Skill struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Body        string `json:"body,omitempty"`
}

// MCPServer is a streamable-HTTP MCP server the agent uses. Header values
// may name secrets as ${NAME}; the port resolves them for the agent only.
type MCPServer struct {
	Name    string   `json:"name"`
	URL     string   `json:"url"`
	Headers []Header `json:"headers"`
}

type Header struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

// Version is a profile version in the history (no content).
type Version struct {
	Version   int       `db:"version" json:"version"`
	CreatedBy string    `db:"created_by" json:"created_by"`
	CreatedAt time.Time `db:"created_at" json:"created_at"`
}

// Secret is what the admin sees of a secret: never its value.
type Secret struct {
	Name string `db:"name" json:"name"`
	// Env: also given to the harness as an environment variable.
	Env       bool      `db:"env" json:"env"`
	UpdatedAt time.Time `db:"updated_at" json:"updated_at"`
}

// SecretValue is a secret opened for the agent (Kits.Equip).
type SecretValue struct {
	Name  string
	Value string
	Env   bool
}

// SaveProfile stores a new version. Base is the version it was edited
// from (the current one), so two editors don't overwrite each other.
type SaveProfile struct {
	Base         int
	Instructions string
	Skills       []Skill
	MCPServers   []MCPServer
	Approve      string
	Model        string
	Effort       string
	// CreatedBy is filled from the caller (not client input).
	CreatedBy string
}

func (p SaveProfile) Validate() error {
	if p.Base < 0 {
		return errx.InvalidField("base", "base must be the current profile version")
	}
	if err := text.Optional("instructions", p.Instructions, MaxInstructions); err != nil {
		return err
	}
	if len(p.Skills) > MaxSkills {
		return errx.InvalidField("skills", "an agent can have at most "+strconv.Itoa(MaxSkills)+" skills")
	}
	seen := map[string]bool{}
	for _, s := range p.Skills {
		if !skillName.MatchString(s.Name) {
			return errx.InvalidField("skills", "skill name "+strconv.Quote(s.Name)+" must be 1-64 lowercase letters, digits, - or _")
		}
		if seen[s.Name] {
			return errx.InvalidField("skills", "two skills are named "+s.Name)
		}
		seen[s.Name] = true
		if err := text.Required("skills."+s.Name+".description", s.Description, maxSkillDescribe); err != nil {
			return err
		}
		if err := text.Required("skills."+s.Name+".body", s.Body, MaxSkillBody); err != nil {
			return err
		}
	}
	if len(p.MCPServers) > MaxMCPServers {
		return errx.InvalidField("mcp_servers", "an agent can have at most "+strconv.Itoa(MaxMCPServers)+" MCP servers")
	}
	clear(seen)
	for _, s := range p.MCPServers {
		if err := s.validate(); err != nil {
			return err
		}
		if seen[s.Name] {
			return errx.InvalidField("mcp_servers", "two MCP servers are named "+s.Name)
		}
		seen[s.Name] = true
	}
	if p.Approve != "" && p.Approve != ApproveAllow && p.Approve != ApproveReject {
		return errx.InvalidField("approve", "approve must be allow, reject or empty (the connector decides)")
	}
	if err := optionValue("model", p.Model); err != nil {
		return err
	}
	if err := optionValue("effort", p.Effort); err != nil {
		return err
	}
	return text.Optional("created_by", p.CreatedBy, text.MaxName)
}

func (s MCPServer) validate() error {
	if !serverName.MatchString(s.Name) || s.Name == "seaport" {
		return errx.InvalidField("mcp_servers", "MCP server name "+strconv.Quote(s.Name)+
			" must be 1-32 lowercase letters, digits, - or _ (and not seaport)")
	}
	u, err := url.Parse(s.URL)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" || u.User != nil || len(s.URL) > 2048 {
		return errx.InvalidField("mcp_servers", "MCP server "+s.Name+" needs an http(s) URL (streamable HTTP)")
	}
	if len(s.Headers) > MaxHeaders {
		return errx.InvalidField("mcp_servers", "MCP server "+s.Name+" has more than "+strconv.Itoa(MaxHeaders)+" headers")
	}
	for _, h := range s.Headers {
		if !headerName.MatchString(h.Name) {
			return errx.InvalidField("mcp_servers", "MCP server "+s.Name+": invalid header name "+strconv.Quote(h.Name))
		}
		if len(h.Value) > MaxHeaderValue || strings.ContainsAny(h.Value, "\r\n\x00") || text.HasForbiddenControl(h.Value) {
			return errx.InvalidField("mcp_servers", "MCP server "+s.Name+": header "+h.Name+" has an invalid value")
		}
	}
	return nil
}

func optionValue(field, v string) error {
	if len(v) > MaxOptionValue || strings.ContainsFunc(v, func(r rune) bool { return r <= ' ' || r == 0x7f }) {
		return errx.InvalidField(field, field+" must be a single word of at most "+strconv.Itoa(MaxOptionValue)+" characters")
	}
	return nil
}

// SecretRefs are the secret names the profile's MCP headers use.
func (p SaveProfile) SecretRefs() []string {
	var out []string
	seen := map[string]bool{}
	for _, s := range p.MCPServers {
		for _, h := range s.Headers {
			for _, m := range secretRef.FindAllStringSubmatch(h.Value, -1) {
				if !seen[m[1]] {
					seen[m[1]] = true
					out = append(out, m[1])
				}
			}
		}
	}
	return out
}

// SecretRefs are the secret names the profile's MCP headers use.
func (p Profile) SecretRefs() []string {
	return SaveProfile{MCPServers: p.MCPServers}.SecretRefs()
}

// SetSecret creates or replaces a secret.
type SetSecret struct {
	Name  string
	Value string
	Env   bool
}

func (s SetSecret) Validate() error {
	if err := ValidSecretName(s.Name); err != nil {
		return err
	}
	if s.Value == "" || len(s.Value) > MaxSecretValue || strings.ContainsRune(s.Value, 0) {
		return errx.InvalidField("value", "value must be 1-"+strconv.Itoa(MaxSecretValue)+" bytes without NUL")
	}
	return nil
}

// ValidSecretName checks a secret name (an environment variable name; the
// SEAPORT_ ones are the connector's).
func ValidSecretName(name string) error {
	if !secretName.MatchString(name) || strings.HasPrefix(name, "SEAPORT_") {
		return errx.InvalidField("name", "name must be an environment variable name (A-Z, 0-9, _; not SEAPORT_…)")
	}
	return nil
}

// Kit is what a connection working as the agent needs: its current
// profile with the secrets resolved. Only the agent's own credential gets
// it (GET /crew/profile).
type Kit struct {
	Agent        identity.AgentID `json:"agent_id"`
	Name         string           `json:"name"`
	Version      int              `json:"version"`
	Instructions string           `json:"instructions"`
	// Skills carry no body: the port's MCP serves it.
	Skills     []Skill     `json:"skills"`
	MCPServers []MCPServer `json:"mcp_servers"`
	Approve    string      `json:"approve"`
	Model      string      `json:"model"`
	Effort     string      `json:"effort"`
	// Env is given to the harness process (secrets marked env).
	Env map[string]string `json:"env"`
	// Revision changes whenever anything above does (profile or secrets):
	// a connector runs one revision and restarts its harness for another.
	Revision string `json:"revision"`
}

// Resolve fills ${NAME} references in v from secrets; an unknown name is
// an error (SaveProfile refuses them, but a secret may have been
// deleted since).
func Resolve(v string, secrets map[string]string) (string, error) {
	var missing string
	out := secretRef.ReplaceAllStringFunc(v, func(m string) string {
		name := m[2 : len(m)-1]
		val, ok := secrets[name]
		if !ok && missing == "" {
			missing = name
		}
		return val
	})
	if missing != "" {
		return "", errx.Business("the profile uses secret " + missing + ", which is not set")
	}
	return out, nil
}
