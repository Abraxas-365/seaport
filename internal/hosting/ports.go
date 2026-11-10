package hosting

import (
	"context"
	"io"

	"github.com/Abraxas-365/seaport/internal/crew"
	"github.com/Abraxas-365/seaport/internal/identity"
	"github.com/Abraxas-365/seaport/internal/query"
)

// Every method takes the organization right after ctx; another
// organization's environment or agent is NotFound (TestTenantFirstPorts).
type Commands interface {
	// CreateEnvironment stores version 1 of a new environment (at most
	// MaxEnvironments per organization).
	CreateEnvironment(ctx context.Context, organization identity.OrganizationID, input CreateEnvironment) (Environment, error)
	// SaveEnvironment stores the next version (Conflict when input.Base is
	// not the current one). A spec equal to the current one (normalized)
	// stores nothing and answers the current version.
	SaveEnvironment(ctx context.Context, organization identity.OrganizationID, environment identity.EnvironmentID, input SaveEnvironment) (Environment, error)
	// DeleteEnvironment removes it with its history (Conflict while an
	// agent is hosted on it).
	DeleteEnvironment(ctx context.Context, organization identity.OrganizationID, environment identity.EnvironmentID) error
	// Host hosts the agent on an environment, or moves it, or changes its
	// slot count (slots beyond it must be stopped: Conflict). The secrets
	// the environment's repo credentials name must be the agent's.
	Host(ctx context.Context, organization identity.OrganizationID, agent identity.AgentID, input Host) (Hosted, error)
	// Unhost stops hosting it (Conflict while a slot is not stopped).
	Unhost(ctx context.Context, organization identity.OrganizationID, agent identity.AgentID) error
	// StartBuild starts building an environment version's image in the
	// background (environment.build events follow) and answers the build.
	// A version already built or building is answered as it is unless
	// input.Force (a failed or stale one is built again). Business error
	// while the runtime is off.
	StartBuild(ctx context.Context, organization identity.OrganizationID, environment identity.EnvironmentID, input BuildEnvironment) (Build, error)
	// StartSlot starts a stopped or failed slot in the background (it
	// builds the environment's current version first when needed;
	// slot.updated events follow) and answers the hosting. A slot already
	// starting or running is answered as it is. Business error while the
	// runtime is off.
	StartSlot(ctx context.Context, organization identity.OrganizationID, input SlotAction) (Hosted, error)
	// StopSlot stops a slot in the background (its agent gets
	// StopTimeout to finish) and answers the hosting.
	StopSlot(ctx context.Context, organization identity.OrganizationID, input SlotAction) (Hosted, error)
	// OpenWorkspace is a running slot's workspace plan, repo credentials
	// resolved (agent secrets): only for the slot's own token
	// (hostinghttp, GET /crew/workspace; TestOnlyTheCrewEquips).
	OpenWorkspace(ctx context.Context, organization identity.OrganizationID, agent identity.AgentID, slot int) (Workspace, error)
}

type Queries interface {
	// Environment is a version of the environment (0: the current one).
	Environment(ctx context.Context, organization identity.OrganizationID, environment identity.EnvironmentID, version int) (Environment, error)
	EnvironmentByName(ctx context.Context, organization identity.OrganizationID, name string) (Environment, error)
	Environments(ctx context.Context, organization identity.OrganizationID, page query.Pagination) (query.Paginated[Environment], error)
	Versions(ctx context.Context, organization identity.OrganizationID, environment identity.EnvironmentID) ([]Version, error)
	// Hosted is the agent's hosting (NotFound when it is not hosted).
	Hosted(ctx context.Context, organization identity.OrganizationID, agent identity.AgentID) (Hosted, error)
	// HostedAgents are the organization's hosted agents.
	HostedAgents(ctx context.Context, organization identity.OrganizationID) ([]Hosted, error)
	// Build is a version's build with its log (version 0: the current
	// one; NotFound when it was never built).
	Build(ctx context.Context, organization identity.OrganizationID, environment identity.EnvironmentID, version int) (Build, error)
}

// Runtime is the container engine hosted agents run on (hostingdocker).
// It knows images and containers, not organizations: requests carry what
// it labels them with.
type Runtime interface {
	// Build builds req's image (named req.Tag(); an image already there is
	// answered at once), writing the build output to log, and answers its
	// id. It pulls a missing base and refuses one that isn't Seaport's base
	// for the spec's harness (LabelBase, LabelHarness).
	Build(ctx context.Context, req BuildRequest, log io.Writer) (string, error)
	// Start runs a slot's container (replacing a leftover one) with
	// Seaport's fixed rules: uid 1000, read-only root, no capabilities,
	// no new privileges, resource limits; its state volume (and workspace
	// volume when persistent) created on first use.
	Start(ctx context.Context, slot StartSlot) error
	// Stop stops a slot's container (StopTimeout, then killed) and
	// removes it; its volumes stay. With run set, only a container of
	// that run. A slot with no container is no error.
	Stop(ctx context.Context, slot SlotRef, run string) error
	// Remove removes whatever the runtime has of a slot: its container
	// and its volumes (state and workspace).
	Remove(ctx context.Context, slot SlotRef) error
	// Slots lists the slots of port (LabelPort) the runtime has a
	// container or volumes of.
	Slots(ctx context.Context, port string) ([]RuntimeSlot, error)
}

// Credentials mint and revoke slot tokens (the crew's tokens).
type Credentials interface {
	Create(ctx context.Context, organization identity.OrganizationID, input crew.CreateToken) (crew.Issued, error)
	Revoke(ctx context.Context, organization identity.OrganizationID, token identity.CrewTokenID) error
}

type Repository interface {
	// CreateEnvironment stores it unless the organization already has
	// limit; count and insert are atomic (errx.QuotaExceeded when full).
	CreateEnvironment(ctx context.Context, organization identity.OrganizationID, environment identity.EnvironmentID, input CreateEnvironment, limit int) (Environment, error)
	// SaveEnvironment inserts version input.Base+1 and makes it current,
	// atomically (Conflict when the current version is not input.Base).
	SaveEnvironment(ctx context.Context, organization identity.OrganizationID, environment identity.EnvironmentID, input SaveEnvironment) (Environment, error)
	// DeleteEnvironment deletes it and its versions (Conflict while an
	// agent is hosted on it).
	DeleteEnvironment(ctx context.Context, organization identity.OrganizationID, environment identity.EnvironmentID) error
	Environment(ctx context.Context, organization identity.OrganizationID, environment identity.EnvironmentID, version int) (Environment, error)
	EnvironmentByName(ctx context.Context, organization identity.OrganizationID, name string) (Environment, error)
	Environments(ctx context.Context, organization identity.OrganizationID, page query.Pagination) (query.Paginated[Environment], error)
	Versions(ctx context.Context, organization identity.OrganizationID, environment identity.EnvironmentID) ([]Version, error)

	// Host upserts the agent's hosting and makes its slots 1..input.Slots,
	// atomically, under the organization's hosting lock: at most maxSlots
	// slots across the organization (errx.QuotaExceeded; 0 = no limit),
	// slots it drops must be stopped (Conflict), the agent must be live.
	Host(ctx context.Context, organization identity.OrganizationID, agent identity.AgentID, input Host, maxSlots int) (Hosted, error)
	// Unhost deletes the hosting and its slots (Conflict while one is not
	// stopped; NotFound when it is not hosted).
	Unhost(ctx context.Context, organization identity.OrganizationID, agent identity.AgentID) error
	Hosted(ctx context.Context, organization identity.OrganizationID, agent identity.AgentID) (Hosted, error)
	HostedAgents(ctx context.Context, organization identity.OrganizationID) ([]Hosted, error)

	// ClaimBuild makes this caller the builder of an environment version
	// FROM base: it inserts the build, or restarts one that failed, went
	// stale (no progress for BuildStale: its process died) or is forced,
	// and answers claimed = true; else the build as it is and false.
	// NotFound when the version doesn't exist.
	ClaimBuild(ctx context.Context, organization identity.OrganizationID, environment identity.EnvironmentID, version int, base string, force bool) (build Build, claimed bool, err error)
	// BuildProgress stores the log so far of a build in progress and keeps
	// it fresh (not stale).
	BuildProgress(ctx context.Context, organization identity.OrganizationID, environment identity.EnvironmentID, version int, log string) error
	// FinishBuild ends a build in progress: ready with image, or failed
	// with detail (image empty).
	FinishBuild(ctx context.Context, organization identity.OrganizationID, environment identity.EnvironmentID, version int, image, detail, log string) (Build, error)
	Build(ctx context.Context, organization identity.OrganizationID, environment identity.EnvironmentID, version int) (Build, error)

	// BeginStart makes the caller a slot's starter: stopped or failed (or
	// starting but stale: no progress for StartStale) → starting, in one
	// statement, so only one process starts it. With wake, only when no
	// other slot of the agent is starting or running. Answers what the
	// start needs and claimed = false when another caller has it.
	BeginStart(ctx context.Context, organization identity.OrganizationID, agent identity.AgentID, slot int, wake bool) (start SlotStart, claimed bool, err error)
	// StartProgress records a start's progress (detail; the run's token
	// when not zero) and keeps it fresh; Conflict once the slot is no
	// longer starting run (stopped meanwhile).
	StartProgress(ctx context.Context, organization identity.OrganizationID, agent identity.AgentID, slot int, run, detail string, token identity.CrewTokenID) error
	// StartDone: starting run → running it (Conflict once the slot is no
	// longer starting run).
	StartDone(ctx context.Context, organization identity.OrganizationID, agent identity.AgentID, slot int, run string, started SlotRun) (Hosted, error)
	// BeginStop: starting, running, failed or stopping → stopping, its run
	// forgotten (claimed = false when it is already stopped). With run
	// set, only a slot running that run (the supervisor's idle stop).
	BeginStop(ctx context.Context, organization identity.OrganizationID, agent identity.AgentID, slot int, run string) (hosted Hosted, claimed bool, err error)
	// EndRun ends a slot's run when its state is one of end.From (and,
	// with end.StaleFor, unchanged for that long): state end.State with
	// end.Detail, its token forgotten and answered (to revoke).
	EndRun(ctx context.Context, organization identity.OrganizationID, agent identity.AgentID, slot int, end EndRun) (hosted Hosted, token identity.CrewTokenID, ended bool, err error)
	// Supervise marks the running slots of agents with work active and
	// lists every slot of every organization with its agent's work
	// (system: the supervisor).
	Supervise(ctx context.Context) ([]Supervised, error)
	// PortID is this deployment's id (system: LabelPort).
	PortID(ctx context.Context) (string, error)
}
