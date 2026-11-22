// Package hosting runs an organization's agents on the port's own host:
// what a hosted agent runs is its config's harness and machine (Spec:
// image, tools, repos, model access, network, resources; versioned with
// the agent's config, agent.Profile), which agents are hosted and with
// how many slots (stable instances, each with its own state volume).
package hosting

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/url"
	"path"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/Abraxas-365/seaport/internal/crew"
	"github.com/Abraxas-365/seaport/internal/errx"
)

// Machine limits.
const (
	MaxPackages = 100
	MaxSetup    = 16384
	MaxRepos    = 10
	MaxOnStart  = 4096
	MaxEgress   = 100
	// MaxIdleStop is the longest idle_stop (0: never stop).
	MaxIdleStop = 7 * 24 * time.Hour
)

// Harnesses an agent can run (agent.Profile.Harness).
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

// Browser engines (Browser.Engine). Chromium is the only one so far: the
// light engines tried (2026-11) crashed on the login pages the browser is
// for (plan browser-login.md), and the field keeps the door open.
const BrowserChromium = "chromium"

// BrowserPort is the loopback port of a slot's browser (CDP), and
// BrowserDir the directory of its profile under the state volume.
const (
	BrowserPort = 9222
	BrowserDir  = "browser"
)

// DefaultIdleStop stops a slot after this long without work.
const DefaultIdleStop = 30 * time.Minute

// DefaultConcurrency is how many conversations a slot works at once when
// the machine doesn't say (Machine.Concurrency); at most
// crew.MaxConcurrency.
const DefaultConcurrency = 2

var (
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

// Spec is what a hosted agent runs: its config's harness (rness or
// claude-code: Seaport's base image for it; agent.Profile.Harness) and
// machine (agent.Profile.Machine).
type Spec struct {
	Harness string
	Machine
}

// SpecOf reads an agent config's harness and machine (agent.Profile;
// NotFound-free: a config with no machine is a Business error, the agent
// can't be hosted yet).
func SpecOf(harness string, machine []byte) (Spec, error) {
	if len(machine) == 0 || string(machine) == "null" {
		return Spec{}, errx.Business("the agent's config has no machine: set where it runs (seaport agents config apply, or the web's Dónde corre) before hosting it")
	}
	var m Machine
	if err := json.Unmarshal(machine, &m); err != nil {
		return Spec{}, errx.Wrap(err, "decode machine", errx.TypeInternal)
	}
	return Spec{Harness: harness, Machine: m}, nil
}

// Machine is where and how a hosted agent runs, part of its versioned
// config (agent.Profile.Machine; hosting checks and normalizes it:
// agent.MachineCheck). No secret values: repo credentials and model keys
// name one of the agent's secrets as ${NAME} (Secrets), and Env holds
// plain variables only.
type Machine struct {
	// Image replaces the base image; it must be built FROM Seaport's base
	// image of the harness (the user, home and entrypoint are its).
	Image string `json:"image"`
	// Packages are Debian packages installed into a derived image.
	Packages []string `json:"packages"`
	// Setup is a shell script run while building the derived image, as
	// root (HOME=/home/agent, handed back to uid 1000 afterwards): tools,
	// baseline config. It is stored and baked into the image: no secrets.
	Setup string `json:"setup"`
	Repos []Repo `json:"repos"`
	// RepoConfig: trust (the harness reads the repos' own AGENTS.md,
	// CLAUDE.md, .claude/ settings) or ignore (connector.harnessFor: rness
	// without instruction files, Claude Code with user settings only).
	RepoConfig string `json:"repo_config"`
	// Home names the harness config baked into the image (its ~/.claude or
	// ~/.rness: instructions, settings, agents, skills, plugins): a home
	// uploaded to the organization, as sha256:<digest> ("": none).
	Home string `json:"home"`
	// Models say how the slots reach each provider (Providers): ${NAME} of
	// one of the agent's secrets holding the API key (the port's gateway
	// uses it; the key never enters the container), or Subscription
	// (Anthropic only: the slot signs in to the person's Claude account).
	Models Models `json:"models"`
	// Env are plain variables every slot's harness gets (TZ, NODE_ENV, a
	// flag): name → value, never a credential (ValidEnv refuses what looks
	// like one: those are secrets). The agent's env secrets win over them.
	Env     map[string]string `json:"env"`
	Network Network           `json:"network"`
	// Browser gives the slots a managed browser (Browser.Enabled): the
	// image gets the engine and the agent's tooling; the config's browser
	// switch (agent.Profile.Browser) needs it.
	Browser   Browser   `json:"browser"`
	Resources Resources `json:"resources"`
	// Workspace: persistent (kept across restarts) or ephemeral.
	Workspace string `json:"workspace"`
	// IdleStop stops a slot after this long without work ("30m", "2h";
	// "0": never).
	IdleStop string `json:"idle_stop"`
	// Concurrency is how many conversations each slot works at once (its
	// connector's hello.concurrency): the next ones wait queued until one
	// of those finishes its turn. One harness process serves them all, so
	// they share the slot's CPU, memory and browser. 0: DefaultConcurrency.
	Concurrency int `json:"concurrency"`
}

// Repo is a git repository cloned into the workspace at start.
type Repo struct {
	// URL is an https git URL.
	URL string `json:"url"`
	// Ref is a branch or tag ("": the default branch).
	Ref string `json:"ref"`
	// Path is where it goes, relative to /workspace ("": its name).
	Path string `json:"path"`
	// Credential names the agent's secret holding a token for the URL's
	// host: ${NAME} ("": public).
	Credential string `json:"credential"`
	// OnStart runs in the repo after each clone or fetch (shell).
	OnStart string `json:"on_start"`
}

// Models say how the slots reach each model provider (Providers):
// provider → ${NAME} of the agent's secret holding its API key (the slots
// reach it through the port's model gateway with their own token: the key
// never enters the container) or Subscription (the slot signs in).
type Models map[string]string

// Network is what a slot may reach besides the port.
type Network struct {
	// Egress are the hosts reachable through the egress proxy
	// ("example.com", "*.example.com"); the repos' hosts are added.
	Egress []string `json:"egress"`
}

// Browser is the managed browser of an agent's slots: a CDP engine
// (BrowserChromium) run by the slot with the agent's profile
// (cookies, storage) in its state volume. The sites it visits go through
// the egress proxy like everything else (Network.Egress).
type Browser struct {
	// Engine is the browser program: "" for none, else BrowserChromium.
	Engine string `json:"engine"`
}

// Enabled says whether the slots get a browser.
func (b Browser) Enabled() bool { return b.Engine != "" }

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

// Validate checks a spec: the harness and the machine.
func (s Spec) Validate() error {
	switch strings.TrimSpace(s.Harness) {
	case HarnessRness, HarnessClaudeCode:
	default:
		return errx.InvalidField("harness", "harness must be rness or claude-code")
	}
	// A subscription and keys mix: Anthropic signs in inside the slot
	// (EnvSignIn) and the providers with a key go through the gateway
	// (rness: images/rness/seaport.lua; Claude Code uses Anthropic only).
	return s.Machine.Validate()
}

// ValidHarness checks a config's harness: "" (not set) or one of the
// harnesses.
func ValidHarness(h string) error {
	switch h {
	case "", HarnessRness, HarnessClaudeCode:
		return nil
	}
	return errx.InvalidField("harness", "harness must be rness or claude-code")
}

// Validate checks a machine (Spec.Validate adds the harness).
func (s Machine) Validate() error {
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
	if s.Home != "" && !homeDigest.MatchString(s.Home) {
		return errx.InvalidField("home", "home must name an uploaded home as sha256:<digest> (seaport agents home push)")
	}
	if err := s.Models.validate(); err != nil {
		return err
	}
	if err := ValidEnv(s.Env); err != nil {
		return err
	}
	if len(s.Network.Egress) > MaxEgress {
		return errx.InvalidField("network.egress", "at most "+strconv.Itoa(MaxEgress)+" hosts")
	}
	for _, h := range s.Network.Egress {
		if !ValidEgressHost(strings.ToLower(strings.TrimSpace(h))) {
			return errx.InvalidField("network.egress", strconv.Quote(h)+" must be a host name (example.com or *.example.com), or * for any public host")
		}
	}
	switch strings.TrimSpace(s.Browser.Engine) {
	case "", BrowserChromium:
	default:
		return errx.InvalidField("browser.engine", "browser.engine must be "+BrowserChromium+" (or empty for no browser)")
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
	if s.Concurrency < 0 || s.Concurrency > crew.MaxConcurrency {
		return errx.InvalidField("concurrency", "concurrency must be between 1 and "+strconv.Itoa(crew.MaxConcurrency)+
			" conversations at once (0 or empty: "+strconv.Itoa(DefaultConcurrency)+")")
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
		return errx.InvalidField("repos", "repo "+r.URL+": credential must name a secret, as ${NAME}")
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
// more, optionally starting with "*." (any subdomain), or AnyHost.
func ValidEgressHost(h string) bool {
	if h == AnyHost {
		return true
	}
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
	out := Spec{Harness: strings.TrimSpace(s.Harness), Machine: normalizeMachine(s.Machine)}
	// Model access defaults to the subscription where the harness has no
	// key for its sessions: Claude Code with no Anthropic key, rness with
	// no key at all.
	switch out.Harness {
	case HarnessClaudeCode:
		if out.Models[ProviderAnthropic] == "" {
			out.Models[ProviderAnthropic] = Subscription
		}
	case HarnessRness:
		if len(out.Models.Providers()) == 0 {
			out.Models[ProviderAnthropic] = Subscription
		}
	}
	return out
}

// Digest names what a slot runs: sha256 (hex) of the normalized spec, so
// two configs whose harness and machine run the same share it. A slot
// started on another digest than its agent's current config is outdated
// (restarted when idle); an edit of the instructions or skills keeps it.
func (s Spec) Digest() string {
	raw, _ := json.Marshal(Normalize(s))
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

// NormalizeModels trims providers (lowercase) and values, dropping empty
// ones.
func NormalizeModels(in Models) Models {
	out := Models{}
	for p, ref := range in {
		if ref = strings.TrimSpace(ref); ref != "" {
			out[strings.ToLower(strings.TrimSpace(p))] = ref
		}
	}
	return out
}

func normalizeMachine(s Machine) Machine {
	out := Machine{
		Image: strings.TrimSpace(s.Image), Setup: strings.TrimSpace(s.Setup), RepoConfig: s.RepoConfig, Home: s.Home,
		Models: NormalizeModels(s.Models), Env: map[string]string{},
		Browser:   Browser{Engine: strings.TrimSpace(s.Browser.Engine)},
		Workspace: s.Workspace, Packages: []string{}, Repos: []Repo{}, Network: Network{Egress: []string{}}}
	for k, v := range s.Env {
		out.Env[strings.TrimSpace(k)] = v
	}
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
	out.Concurrency = s.Concurrency
	if out.Concurrency == 0 {
		out.Concurrency = DefaultConcurrency
	}
	return out
}

// Secrets are the secret names the machine names: repo credentials and
// model keys, in a stable order (repos first, then providers by name).
func (s Machine) Secrets() []string {
	out := []string{}
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
	for _, p := range s.Models.Providers() {
		add(s.Models.KeyName(p))
	}
	return out
}

// Providers are the providers reached with a key (the gateway serves
// them), sorted; a subscription is not one (SignsIn).
func (m Models) Providers() []string {
	out := make([]string, 0, len(m))
	for p := range m {
		if m.KeyName(p) != "" {
			out = append(out, p)
		}
	}
	slices.Sort(out)
	return out
}

// KeyName is the secret holding provider's key ("" if none).
func (m Models) KeyName(provider string) string {
	if v := credentialRef.FindStringSubmatch(m[provider]); v != nil {
		return v[1]
	}
	return ""
}

func (m Models) validate() error {
	for p, ref := range m {
		p = strings.ToLower(strings.TrimSpace(p))
		if _, ok := Providers[p]; !ok {
			return errx.InvalidField("models", "models."+p+": the gateway serves no provider "+strconv.Quote(p)+" (one of "+strings.Join(ProviderNames(), ", ")+")")
		}
		ref = strings.TrimSpace(ref)
		if ref == Subscription {
			if p != ProviderAnthropic {
				return errx.InvalidField("models."+p, "models."+p+": only anthropic has a subscription to sign in to; name a secret with the key, as ${NAME}")
			}
			continue
		}
		if !credentialRef.MatchString(ref) {
			return errx.InvalidField("models."+p, "models."+p+" must name a secret with the key, as ${NAME}, or be "+Subscription)
		}
	}
	return nil
}

// EgressHosts are the hosts a slot may reach through the egress proxy:
// the machine's egress plus its repos' hosts, and the provider's hosts
// when its slots sign in to a subscription (SignsIn).
func (s Machine) EgressHosts() []string {
	out := slices.Clone(s.Network.Egress)
	for _, r := range s.Repos {
		if h := r.Host(); h != "" && !slices.Contains(out, h) {
			out = append(out, h)
		}
	}
	if s.SignsIn() {
		for _, h := range subscriptionHosts {
			if !slices.Contains(out, h) {
				out = append(out, h)
			}
		}
	}
	return out
}
