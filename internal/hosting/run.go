package hosting

import (
	"crypto/sha256"
	"encoding/hex"
	"strconv"
	"time"

	"github.com/Abraxas-365/seaport/internal/errx"
	"github.com/Abraxas-365/seaport/internal/identity"
	"github.com/Abraxas-365/seaport/internal/text"
)

// Runtime labels on a slot's container and volumes (besides
// LabelOrganization): the supervisor finds its slots by them. LabelPort
// names the deployment (its database), so two ports sharing a Docker
// engine never touch each other's slots.
const (
	LabelPort  = "io.seaport.port"
	LabelAgent = "io.seaport.agent"
	LabelSlot  = "io.seaport.slot"
	// LabelRun names the run a container belongs to (Repository
	// BeginStart): a starter cleans up only its own run's container.
	LabelRun = "io.seaport.run"
)

// Slot runs (M3).
const (
	// SlotTokenLifetime bounds a slot token: a run longer than this gets
	// a new one at its next start (idle stop restarts most runs sooner).
	SlotTokenLifetime = 30 * 24 * time.Hour
	// StartStale: a slot starting for this long without its starter
	// reporting (its process died) is failed and may be started again.
	StartStale = 2 * time.Minute
	// StopTimeout is how long a slot's agent gets to finish before it is
	// killed.
	StopTimeout = 20 * time.Second
	// SupervisePeriod is how often the supervisor wakes, stops idle slots
	// and reconciles slots with their containers.
	SupervisePeriod = 15 * time.Second
)

// Mount points inside a slot's container (images/Dockerfile).
const (
	WorkspaceDir = "/workspace"
	StateDir     = "/seaport/state"
)

// SlotRef names one slot of one agent of one organization.
type SlotRef struct {
	Organization identity.OrganizationID
	Agent        identity.AgentID
	Index        int
}

// Key is the slot's runtime name: stable, the same in every process, and
// revealing nothing (container seaport-slot-<key>, volumes …-state, …-ws).
func (r SlotRef) Key() string {
	sum := sha256.Sum256([]byte(r.Organization.String() + "/" + r.Agent.String() + "/" + strconv.Itoa(r.Index)))
	return hex.EncodeToString(sum[:])[:12]
}

// StartSlot is what Runtime.Start runs.
type StartSlot struct {
	Slot SlotRef
	// Run is the run being started (LabelRun).
	Run string
	// Name is the slot's display name (<agent>-<n>).
	Name string
	// Port is the deployment's id (LabelPort).
	Port string
	// Image is the environment version's built image.
	Image   string
	Harness string
	// Resources bound the container.
	Resources Resources
	// Persistent keeps the workspace in a named volume (else it goes with
	// the container).
	Persistent bool
	// Env is the container's environment: the port's URL, the slot token,
	// the slot name (SlotEnv).
	Env map[string]string
}

// Environment of a slot's container, read by `seaport agent-run`.
const (
	EnvURL     = "SEAPORT_URL"
	EnvToken   = "SEAPORT_CREW_TOKEN"
	EnvSlot    = "SEAPORT_SLOT"
	EnvHarness = "SEAPORT_HARNESS"
)

// RuntimeSlot is a slot the runtime has something of: a container
// (running or exited) and/or volumes.
type RuntimeSlot struct {
	Slot SlotRef
	// Container: it has a container (of Run); Running: that container is
	// up (else it exited: Detail says how).
	Container bool
	Run       string
	Running   bool
	Detail    string
}

// MaxLogLines bounds a slot log's tail (LogQuery.Lines).
const MaxLogLines = 2000

// LogQuery is the input of Queries.SlotLog.
type LogQuery struct {
	Agent identity.AgentID
	Index int
	// Lines is how many of the last lines (1..MaxLogLines).
	Lines int
}

func (q LogQuery) Validate() error {
	if q.Agent.IsZero() {
		return errx.InvalidField("agent_id", "agent_id is required")
	}
	if q.Index < 1 || q.Index > MaxSlotsPerAgent {
		return errx.InvalidField("slot", "slot must be between 1 and "+strconv.Itoa(MaxSlotsPerAgent))
	}
	if q.Lines < 1 || q.Lines > MaxLogLines {
		return errx.InvalidField("lines", "lines must be between 1 and "+strconv.Itoa(MaxLogLines))
	}
	return nil
}

// SlotLog is the tail of a slot container's output (agent-run, the
// connector and the harness's stderr), timestamped by the runtime.
type SlotLog struct {
	Slot int    `json:"slot"`
	Log  string `json:"log"`
}

// SlotAction is the input of Commands.StartSlot and StopSlot.
type SlotAction struct {
	Agent identity.AgentID
	Index int
	// By is filled from the caller (not client input).
	By string
}

func (a SlotAction) Validate() error {
	if a.Agent.IsZero() {
		return errx.InvalidField("agent_id", "agent_id is required")
	}
	if a.Index < 1 || a.Index > MaxSlotsPerAgent {
		return errx.InvalidField("slot", "slot must be between 1 and "+strconv.Itoa(MaxSlotsPerAgent))
	}
	return text.Optional("by", a.By, text.MaxName)
}

// Supervised is a slot as the supervisor sees it (every organization):
// its state, its agent's work, and its environment's idle stop.
type Supervised struct {
	Organization identity.OrganizationID `db:"organization_id"`
	Agent        identity.AgentID        `db:"agent_id"`
	Index        int                     `db:"idx"`
	State        string                  `db:"state"`
	// Run is the slot's current run ("" when stopped or stopping).
	Run       string     `db:"run"`
	UpdatedAt time.Time  `db:"updated_at"`
	ActiveAt  *time.Time `db:"active_at"`
	StartedAt *time.Time `db:"started_at"`
	// IdleStop is the environment's idle_stop ("0": never).
	IdleStop string `db:"idle_stop"`
	// Outdated: the slot runs another environment or version than its
	// agent's current one (restarted when idle).
	Outdated bool `db:"outdated"`
	// Queued and Claimed count the agent's ships waiting and in work.
	Queued  int `db:"queued"`
	Claimed int `db:"claimed"`
}

// Ref is the slot's reference.
func (s Supervised) Ref() SlotRef {
	return SlotRef{Organization: s.Organization, Agent: s.Agent, Index: s.Index}
}

// SlotStart is what Repository.BeginStart hands the starter: the slot's
// agent and environment (current version).
type SlotStart struct {
	// Run identifies this start: the starter's later writes match it, so
	// a starter that lost the slot (stopped meanwhile) changes nothing.
	Run         string
	AgentName   string
	Environment Environment
	// OldToken is the token of a run that died while starting (revoke it).
	OldToken identity.CrewTokenID
}

// Workspace is what a slot's container asks the port for at start
// (GET /crew/workspace, its slot token): how to prepare /workspace. Repo
// credentials come resolved (the agent's secrets): only the slot's own
// token gets them.
type Workspace struct {
	Slot       string          `json:"slot"`
	Harness    string          `json:"harness"`
	RepoConfig string          `json:"repo_config"`
	Persistent bool            `json:"persistent"`
	Repos      []WorkspaceRepo `json:"repos"`
	// SignIn: the harness signs in to a subscription (Spec.SignsIn), so
	// the slot waits for a sign-in before it takes work.
	SignIn bool `json:"sign_in"`
}

// WorkspaceRepo is a repo to clone or fetch, its token resolved.
type WorkspaceRepo struct {
	URL     string `json:"url"`
	Ref     string `json:"ref"`
	Path    string `json:"path"`
	OnStart string `json:"on_start"`
	// Token is the credential's value ("" for a public repo).
	Token string `json:"token,omitempty"`
}

// EndRun is how Repository.EndRun ends a run.
type EndRun struct {
	From []string
	// Run, when set, ends it only if it is still that run.
	Run    string
	State  string // SlotStopped or SlotFailed
	Detail string
	// StaleFor, when set, ends it only if unchanged for that long.
	StaleFor time.Duration
}

// SlotRun is what a slot runs once started.
type SlotRun struct {
	Image       string
	Environment identity.EnvironmentID
	Version     int
}
