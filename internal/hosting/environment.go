// Package hosting runs an organization's agents on the port's own host:
// environments (where an agent runs: harness, image, tools, repos,
// network, resources; versioned like profiles), which agents are hosted
// on which environment and with how many slots (stable instances, each
// with its own state volume).
package hosting

import (
	"net/url"
	"path"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/Abraxas-365/seaport/internal/errx"
	"github.com/Abraxas-365/seaport/internal/identity"
	"github.com/Abraxas-365/seaport/internal/text"
)

// Environment limits.
const (
	MaxEnvironments = 50
	MaxDescription  = 1000
	MaxPackages     = 100
	MaxSetup        = 16384
	MaxRepos        = 10
	MaxOnStart      = 4096
	MaxEgress       = 100
	// MaxIdleStop is the longest idle_stop (0: never stop).
	MaxIdleStop = 7 * 24 * time.Hour
)

// Harnesses an environment can run.
const (
	HarnessRness      = "rness"
	HarnessClaudeCode = "claude-code"
)

// Repo config policies: whether the harness reads the repo's own
// configuration (AGENTS.md, CLAUDE.md, .claude/, .rness/).
const (
	RepoConfigTrust  = "trust"
	RepoConfigIgnore = "ignore"
)

// Workspace kinds.
const (
	WorkspacePersistent = "persistent"
	WorkspaceEphemeral  = "ephemeral"
)

// Resource sizes (Resources.Size): presets of CPUs and memory.
const (
	SizeS = "S"
	SizeM = "M"
	SizeL = "L"
)

// DefaultIdleStop stops a slot after this long without work.
const DefaultIdleStop = 30 * time.Minute

var (
	envName = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,63}$`)
	// a Debian package, optionally pinned (name=version)
	packageName = regexp.MustCompile(`^[a-z0-9][a-z0-9+.-]{0,99}(=[A-Za-z0-9.+:~-]{1,100})?$`)
	// an image reference: [registry[:port]/]path[:tag][@sha256:digest]
	imageRef = regexp.MustCompile(`^[a-z0-9]+([._-][a-z0-9]+)*(\.[a-z0-9-]+)*(:[0-9]{1,5})?(/[a-z0-9]+([._-]+[a-z0-9]+)*)*(:[A-Za-z0-9_][A-Za-z0-9_.-]{0,127})?(@sha256:[a-f0-9]{64})?$`)
	gitRef   = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._/-]{0,199}$`)
	repoPath = regexp.MustCompile(`^[A-Za-z0-9._-]+(/[A-Za-z0-9._-]+)*$`)
	// credential names one of the hosted agent's secrets: ${NAME}
	credentialRef = regexp.MustCompile(`^\$\{([A-Z_][A-Z0-9_]{0,63})\}$`)
	hostLabel     = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`)
)

// sizes are the presets of Resources.Size.
var sizes = map[string]Resources{
	SizeS: {CPUs: 1, MemoryMB: 2048},
	SizeM: {CPUs: 2, MemoryMB: 4096},
	SizeL: {CPUs: 4, MemoryMB: 8192},
}

// Environment is where an organization's hosted agents run. Its spec is
// versioned (Version is the current one); agents hosted on it run the
// current version.
type Environment struct {
	ID           identity.EnvironmentID  `json:"id"`
	Organization identity.OrganizationID `json:"-"`
	// Name is unique in the organization (`seaport env apply` keys on it).
	Name      string    `json:"name"`
	Version   int       `json:"version"`
	Spec      Spec      `json:"spec"`
	CreatedBy string    `json:"created_by"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
	// Agents is how many agents are hosted on it.
	Agents int `json:"agents"`
}

// Version is an environment version in the history (no spec).
type Version struct {
	Version   int       `db:"version" json:"version"`
	CreatedBy string    `db:"created_by" json:"created_by"`
	CreatedAt time.Time `db:"created_at" json:"created_at"`
}

// Spec is one version of an environment. No secrets: repo credentials
// name a secret of the hosted agent (${NAME}).
type Spec struct {
	Description string `json:"description"`
	// Harness is the agent program: rness or claude-code (Seaport's base
	// image for it).
	Harness string `json:"harness"`
	// Image replaces the base image; it must be built FROM Seaport's base
	// image of the harness (the user, home and entrypoint are its).
	Image string `json:"image"`
	// Packages are Debian packages installed into a derived image.
	Packages []string `json:"packages"`
	// Setup is a shell script run while building the derived image, as
	// the agent user (uid 1000, HOME=/home/agent): tools, baseline config.
	Setup string `json:"setup"`
	Repos []Repo `json:"repos"`
	// RepoConfig: trust (the harness reads the repos' own AGENTS.md,
	// CLAUDE.md, .claude/, .rness/) or ignore.
	RepoConfig string `json:"repo_config"`
	// Models are the model keys the port's gateway uses for the slots.
	Models    Models    `json:"models"`
	Network   Network   `json:"network"`
	Resources Resources `json:"resources"`
	// Workspace: persistent (kept across restarts) or ephemeral.
	Workspace string `json:"workspace"`
	// IdleStop stops a slot after this long without work ("30m", "2h";
	// "0": never).
	IdleStop string `json:"idle_stop"`
}

// Repo is a git repository cloned into the workspace at start.
type Repo struct {
	// URL is an https git URL.
	URL string `json:"url"`
	// Ref is a branch or tag ("": the default branch).
	Ref string `json:"ref"`
	// Path is where it goes, relative to /workspace ("": its name).
	Path string `json:"path"`
	// Credential names the hosted agent's secret holding a token for the
	// URL's host: ${NAME} ("": public).
	Credential string `json:"credential"`
	// OnStart runs in the repo after each clone or fetch (shell).
	OnStart string `json:"on_start"`
}

// Models name the hosted agent's secrets holding model API keys, as
// ${NAME}. With one set, the slots reach that provider through the port's
// model gateway with their own token: the key never enters the container.
type Models struct {
	Anthropic string `json:"anthropic"`
}

// Network is what a slot may reach besides the port.
type Network struct {
	// Egress are the hosts reachable through the egress proxy
	// ("example.com", "*.example.com"); the repos' hosts are added.
	Egress []string `json:"egress"`
}

// Resources bound a slot: a preset size, or CPUs and memory.
type Resources struct {
	Size     string  `json:"size,omitempty"`
	CPUs     float64 `json:"cpus"`
	MemoryMB int     `json:"memory_mb"`
}

// Limits are the operator's maxima (config SEAPORT_HOST_MAX_*).
type Limits struct {
	MaxCPUs     float64
	MaxMemoryMB int
	// MaxSlots bounds an organization's slots across its hosted agents.
	MaxSlots int
}

// Check refuses resources above the operator's maxima.
func (l Limits) Check(r Resources) error {
	if l.MaxCPUs > 0 && r.CPUs > l.MaxCPUs {
		return errx.InvalidField("resources", "cpus "+strconv.FormatFloat(r.CPUs, 'f', -1, 64)+
			" is above this port's maximum ("+strconv.FormatFloat(l.MaxCPUs, 'f', -1, 64)+")")
	}
	if l.MaxMemoryMB > 0 && r.MemoryMB > l.MaxMemoryMB {
		return errx.InvalidField("resources", "memory_mb "+strconv.Itoa(r.MemoryMB)+
			" is above this port's maximum ("+strconv.Itoa(l.MaxMemoryMB)+")")
	}
	return nil
}

// ValidName checks an environment name.
func ValidName(name string) error {
	if !envName.MatchString(name) {
		return errx.InvalidField("name", "name must be 1-64 lowercase letters, digits, - or _, starting with a letter or digit")
	}
	return nil
}

func (s Spec) Validate() error {
	if err := text.Optional("description", s.Description, MaxDescription); err != nil {
		return err
	}
	switch strings.TrimSpace(s.Harness) {
	case HarnessRness, HarnessClaudeCode:
	default:
		return errx.InvalidField("harness", "harness must be rness or claude-code")
	}
	if img := strings.TrimSpace(s.Image); img != "" && (len(img) > 255 || !imageRef.MatchString(img)) {
		return errx.InvalidField("image", "image must be an image reference (registry/name:tag or @sha256:digest)")
	}
	if len(s.Packages) > MaxPackages {
		return errx.InvalidField("packages", "at most "+strconv.Itoa(MaxPackages)+" packages")
	}
	for _, p := range s.Packages {
		if !packageName.MatchString(strings.TrimSpace(p)) {
			return errx.InvalidField("packages", strconv.Quote(p)+" is not a Debian package name (name or name=version)")
		}
	}
	if len(s.Setup) > MaxSetup || strings.ContainsRune(s.Setup, 0) {
		return errx.InvalidField("setup", "setup must be at most "+strconv.Itoa(MaxSetup)+" bytes of text")
	}
	if len(s.Repos) > MaxRepos {
		return errx.InvalidField("repos", "at most "+strconv.Itoa(MaxRepos)+" repos")
	}
	paths := map[string]bool{}
	for _, r := range s.Repos {
		if err := r.validate(); err != nil {
			return err
		}
		p := r.path()
		if paths[p] {
			return errx.InvalidField("repos", "two repos go to "+p+" (set path)")
		}
		paths[p] = true
	}
	switch s.RepoConfig {
	case "", RepoConfigTrust, RepoConfigIgnore:
	default:
		return errx.InvalidField("repo_config", "repo_config must be trust or ignore")
	}
	if s.Models.Anthropic != "" && !credentialRef.MatchString(s.Models.Anthropic) {
		return errx.InvalidField("models.anthropic", "models.anthropic must name a secret of the agent, as ${NAME}")
	}
	if len(s.Network.Egress) > MaxEgress {
		return errx.InvalidField("network.egress", "at most "+strconv.Itoa(MaxEgress)+" hosts")
	}
	for _, h := range s.Network.Egress {
		if !ValidEgressHost(strings.ToLower(strings.TrimSpace(h))) {
			return errx.InvalidField("network.egress", strconv.Quote(h)+" must be a host name (example.com or *.example.com)")
		}
	}
	if err := s.Resources.validate(); err != nil {
		return err
	}
	switch s.Workspace {
	case "", WorkspacePersistent, WorkspaceEphemeral:
	default:
		return errx.InvalidField("workspace", "workspace must be persistent or ephemeral")
	}
	if _, err := ParseIdleStop(s.IdleStop); err != nil {
		return err
	}
	return nil
}

func (r Repo) validate() error {
	u, err := url.Parse(strings.TrimSpace(r.URL))
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" ||
		len(r.URL) > 2048 || strings.Trim(u.Path, "/") == "" {
		return errx.InvalidField("repos", "repo "+strconv.Quote(r.URL)+" needs an https git URL without credentials (use credential)")
	}
	if r.Ref != "" && (!gitRef.MatchString(r.Ref) || strings.Contains(r.Ref, "..")) {
		return errx.InvalidField("repos", "repo "+r.URL+": ref "+strconv.Quote(r.Ref)+" is not a branch or tag name")
	}
	if r.Path != "" {
		clean := path.Clean(r.Path)
		if clean != r.Path || !repoPath.MatchString(r.Path) || strings.HasPrefix(r.Path, ".") || strings.Contains(r.Path, "/.") {
			return errx.InvalidField("repos", "repo "+r.URL+": path must be relative to /workspace, without . or .. parts")
		}
		if r.Path == "wt" || strings.HasPrefix(r.Path, "wt/") {
			return errx.InvalidField("repos", "repo "+r.URL+": path wt is Seaport's (conversation worktrees)")
		}
	}
	if r.Credential != "" && !credentialRef.MatchString(r.Credential) {
		return errx.InvalidField("repos", "repo "+r.URL+": credential must name a secret of the agent, as ${NAME}")
	}
	if len(r.OnStart) > MaxOnStart || strings.ContainsRune(r.OnStart, 0) {
		return errx.InvalidField("repos", "repo "+r.URL+": on_start must be at most "+strconv.Itoa(MaxOnStart)+" bytes of text")
	}
	return nil
}

// path is where the repo goes under /workspace: Path, else the last part
// of the URL without .git.
func (r Repo) path() string {
	if r.Path != "" {
		return r.Path
	}
	u, _ := url.Parse(strings.TrimSpace(r.URL))
	return strings.TrimSuffix(path.Base(strings.TrimRight(u.Path, "/")), ".git")
}

// Host is the repo URL's host name (the egress proxy lets it through).
func (r Repo) Host() string {
	u, err := url.Parse(strings.TrimSpace(r.URL))
	if err != nil {
		return ""
	}
	return strings.ToLower(u.Hostname())
}

// CredentialName is the secret the credential names ("" if none).
func (r Repo) CredentialName() string {
	if m := credentialRef.FindStringSubmatch(r.Credential); m != nil {
		return m[1]
	}
	return ""
}

func (r Resources) validate() error {
	if r.Size != "" {
		if _, ok := sizes[r.Size]; !ok {
			return errx.InvalidField("resources", "size must be S, M or L (or set cpus and memory_mb)")
		}
		if (r.CPUs != 0 || r.MemoryMB != 0) && sizes[r.Size] != (Resources{CPUs: r.CPUs, MemoryMB: r.MemoryMB}) {
			return errx.InvalidField("resources", "set either size or cpus and memory_mb, not both")
		}
		return nil
	}
	if r.CPUs == 0 && r.MemoryMB == 0 {
		return nil // the default size
	}
	if r.CPUs < 0.25 || r.CPUs > 64 {
		return errx.InvalidField("resources", "cpus must be between 0.25 and 64")
	}
	if r.MemoryMB < 256 || r.MemoryMB > 262144 {
		return errx.InvalidField("resources", "memory_mb must be between 256 and 262144")
	}
	return nil
}

// ValidEgressHost checks an egress host: a DNS name of two labels or
// more, optionally starting with "*." (any subdomain).
func ValidEgressHost(h string) bool {
	h = strings.TrimPrefix(h, "*.")
	if len(h) > 253 {
		return false
	}
	labels := strings.Split(h, ".")
	if len(labels) < 2 {
		return false
	}
	for _, l := range labels {
		if !hostLabel.MatchString(l) {
			return false
		}
	}
	return true
}

// ParseIdleStop reads idle_stop: "" is the default, "0" never stops.
func ParseIdleStop(v string) (time.Duration, error) {
	v = strings.TrimSpace(v)
	switch v {
	case "":
		return DefaultIdleStop, nil
	case "0", "never":
		return 0, nil
	}
	d, err := time.ParseDuration(v)
	if err != nil || d < time.Minute || d > MaxIdleStop || d%time.Minute != 0 {
		return 0, errx.InvalidField("idle_stop", "idle_stop must be whole minutes between 1m and 168h (\"30m\", \"2h\"), or 0 for never")
	}
	return d, nil
}

// formatIdleStop writes d as idle_stop ("1h30m", "0").
func formatIdleStop(d time.Duration) string {
	if d == 0 {
		return "0"
	}
	h, m := int(d/time.Hour), int(d%time.Hour/time.Minute)
	switch {
	case h == 0:
		return strconv.Itoa(m) + "m"
	case m == 0:
		return strconv.Itoa(h) + "h"
	}
	return strconv.Itoa(h) + "h" + strconv.Itoa(m) + "m"
}

// Normalize fills a valid spec's defaults and canonical forms: trimmed
// text, lowercase egress hosts (deduplicated), repo paths, sizes resolved
// to CPUs and memory, idle_stop canonical. Two specs that run the same
// normalize equal.
func Normalize(s Spec) Spec {
	out := Spec{Description: text.Clean(s.Description), Harness: strings.TrimSpace(s.Harness),
		Image: strings.TrimSpace(s.Image), Setup: strings.TrimSpace(s.Setup), RepoConfig: s.RepoConfig, Models: s.Models,
		Workspace: s.Workspace, Packages: []string{}, Repos: []Repo{}, Network: Network{Egress: []string{}}}
	if out.Setup != "" {
		out.Setup += "\n"
	}
	seen := map[string]bool{}
	for _, p := range s.Packages {
		if p = strings.TrimSpace(p); !seen[p] {
			seen[p] = true
			out.Packages = append(out.Packages, p)
		}
	}
	for _, r := range s.Repos {
		r.URL = strings.TrimSpace(r.URL)
		r.Path = r.path()
		r.OnStart = strings.TrimSpace(r.OnStart)
		out.Repos = append(out.Repos, r)
	}
	clear(seen)
	for _, h := range s.Network.Egress {
		if h = strings.ToLower(strings.TrimSpace(h)); !seen[h] {
			seen[h] = true
			out.Network.Egress = append(out.Network.Egress, h)
		}
	}
	if out.RepoConfig == "" {
		out.RepoConfig = RepoConfigTrust
	}
	if out.Workspace == "" {
		out.Workspace = WorkspacePersistent
	}
	out.Resources = s.Resources
	switch {
	case s.Resources.Size != "":
		out.Resources = sizes[s.Resources.Size]
		out.Resources.Size = s.Resources.Size
	case s.Resources.CPUs == 0 && s.Resources.MemoryMB == 0:
		out.Resources = sizes[SizeS]
		out.Resources.Size = SizeS
	}
	d, _ := ParseIdleStop(s.IdleStop)
	out.IdleStop = formatIdleStop(d)
	return out
}

// Secrets are the agent secrets the spec names: repo credentials and
// model keys.
func (s Spec) Secrets() []string {
	var out []string
	seen := map[string]bool{}
	add := func(n string) {
		if n != "" && !seen[n] {
			seen[n] = true
			out = append(out, n)
		}
	}
	for _, r := range s.Repos {
		add(r.CredentialName())
	}
	add(s.Models.KeyName(ProviderAnthropic))
	return out
}

// KeyName is the secret holding provider's key ("" if none).
func (m Models) KeyName(provider string) string {
	ref := ""
	if provider == ProviderAnthropic {
		ref = m.Anthropic
	}
	if v := credentialRef.FindStringSubmatch(ref); v != nil {
		return v[1]
	}
	return ""
}

// EgressHosts are the hosts a slot may reach through the egress proxy:
// the spec's egress plus its repos' hosts.
func (s Spec) EgressHosts() []string {
	out := slices.Clone(s.Network.Egress)
	for _, r := range s.Repos {
		if h := r.Host(); h != "" && !slices.Contains(out, h) {
			out = append(out, h)
		}
	}
	return out
}

// CreateEnvironment is the input of Commands.CreateEnvironment.
type CreateEnvironment struct {
	Name string
	Spec Spec
	// CreatedBy is filled from the caller (not client input).
	CreatedBy string
}

func (c CreateEnvironment) Validate() error {
	if err := ValidName(c.Name); err != nil {
		return err
	}
	if err := c.Spec.Validate(); err != nil {
		return err
	}
	return text.Optional("created_by", c.CreatedBy, text.MaxName)
}

// SaveEnvironment stores the next version of an environment. Base is the
// version it was edited from (the current one).
type SaveEnvironment struct {
	Base      int
	Spec      Spec
	CreatedBy string
}

func (s SaveEnvironment) Validate() error {
	if s.Base <= 0 {
		return errx.InvalidField("base", "base must be the current environment version")
	}
	if err := s.Spec.Validate(); err != nil {
		return err
	}
	return text.Optional("created_by", s.CreatedBy, text.MaxName)
}
