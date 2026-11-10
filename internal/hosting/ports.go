package hosting

import (
	"context"
	"io"
	"time"

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
	// UploadHome validates a harness home (ReadHome) and stores it sealed
	// (Business when blob storage is off); the same files again answer the
	// home already there. A spec names it by its Digest.
	UploadHome(ctx context.Context, organization identity.OrganizationID, input UploadHome) (Home, error)
	// SweepHomes deletes homes no environment version names, uploaded or
	// named more than HomeGrace before now, in every organization (system:
	// sweeper; their blobs go to the trash), and says how many.
	SweepHomes(ctx context.Context, now time.Time) (int, error)
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
	// ModelKey is the provider key the gateway forwards a slot's model
	// calls with (the environment's models, an agent secret): only for
	// the slot's own token (hostinggateway; TestOnlyTheCrewEquips).
	// NotFound when the environment names none.
	ModelKey(ctx context.Context, organization identity.OrganizationID, input KeyRequest) (ModelKey, error)
	// RecordUsage adds one model response's tokens to the agent's day.
	RecordUsage(ctx context.Context, organization identity.OrganizationID, input Usage) error
	// Egress is the hosts a slot may reach through the egress proxy (its
	// environment's egress and repo hosts; NotFound when the slot is gone).
	Egress(ctx context.Context, organization identity.OrganizationID, caller SlotCaller) ([]string, error)
	// RecordDenial notes a host the egress proxy refused a slot.
	RecordDenial(ctx context.Context, organization identity.OrganizationID, input Denied) error

	// StartLogin starts signing a running slot's harness in to the
	// person's subscription (experimental; Claude Code only, Business
	// otherwise): the harness's login runs in the slot's container,
	// relayed by this process, and the answer is the sign-in (poll Login
	// for its URL). One already going for the slot's run is answered as
	// it is.
	StartLogin(ctx context.Context, organization identity.OrganizationID, input SlotAction) (Login, error)
	// SubmitLoginCode hands the code the sign-in page showed to the
	// waiting sign-in (any process; Conflict when none waits).
	SubmitLoginCode(ctx context.Context, organization identity.OrganizationID, input LoginCode) (Login, error)
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
	// Usage is the agent's model use per day and model, newest first.
	Usage(ctx context.Context, organization identity.OrganizationID, agent identity.AgentID, input UsageQuery) ([]UsageDay, error)
	// Denials are the hosts the egress proxy refused the agent's slots,
	// most recent first (at most MaxDenials).
	Denials(ctx context.Context, organization identity.OrganizationID, agent identity.AgentID) ([]Denial, error)
	// SlotLog is the tail of a slot's container output (NotFound when the
	// agent isn't hosted, has no such slot, or the slot has no container:
	// a stopped slot's went with it).
	SlotLog(ctx context.Context, organization identity.OrganizationID, input LogQuery) (SlotLog, error)
	// Login is the slot's latest sign-in (NotFound when it never had one;
	// a stale live one reads as failed).
	Login(ctx context.Context, organization identity.OrganizationID, input SlotAction) (Login, error)
	// Home is an uploaded home's manifest (NotFound when unknown).
	Home(ctx context.Context, organization identity.OrganizationID, digest string) (Home, error)
	// HomeFile is one file of a home.
	HomeFile(ctx context.Context, organization identity.OrganizationID, digest, path string) ([]byte, error)
	// HomeArchive is a home's normalized tar (seaport env home pull).
	HomeArchive(ctx context.Context, organization identity.OrganizationID, digest string) ([]byte, error)
	// Account is how the slot's harness is signed in, asked of the harness
	// in its container (Business when the slot isn't running).
	Account(ctx context.Context, organization identity.OrganizationID, input SlotAction) (Account, error)
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
	// Logs is the last lines of a slot container's output, stdout and
	// stderr interleaved, each line timestamped (NotFound with no
	// container).
	Logs(ctx context.Context, slot SlotRef, lines int) (string, error)
	// Exec runs cmd in a slot's running container (of run, when set) as
	// uid 1000, stdin fed from stdin (nil: none), stdout and stderr to
	// out, until it exits or ctx ends, and answers its exit code
	// (NotFound with no container; Business when it isn't running run).
	Exec(ctx context.Context, slot SlotRef, run string, cmd []string, stdin io.Reader, out io.Writer) (int, error)
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

	// AddUsage upserts one response's tokens into the agent's day (UTC).
	AddUsage(ctx context.Context, organization identity.OrganizationID, input Usage) error
	Usage(ctx context.Context, organization identity.OrganizationID, agent identity.AgentID, days int) ([]UsageDay, error)
	// AddDenial counts a refused host for a slot (no error when the slot
	// is gone).
	AddDenial(ctx context.Context, organization identity.OrganizationID, input Denied) error
	Denials(ctx context.Context, organization identity.OrganizationID, agent identity.AgentID, limit int) ([]Denial, error)

	// AddHome records an uploaded home (its blob already stored): created
	// = false and the one there when its digest is known (touched). At
	// most limit homes per organization (QuotaExceeded).
	AddHome(ctx context.Context, organization identity.OrganizationID, home Home, limit int) (stored Home, created bool, err error)
	// StageHomeBlob puts a home's blob key in the trash, due at due,
	// before the blob is written (AddHome takes it out).
	StageHomeBlob(ctx context.Context, organization identity.OrganizationID, key string, due time.Time) error
	Home(ctx context.Context, organization identity.OrganizationID, digest string) (Home, error)
	// StaleHomes lists homes of every organization that no environment
	// version names, last touched before before (system: sweeper).
	StaleHomes(ctx context.Context, before time.Time, limit int) ([]Home, error)
	// DeleteHome deletes a home if still unnamed and untouched since
	// before (its blob goes to the trash by trigger).
	DeleteHome(ctx context.Context, organization identity.OrganizationID, digest string, before time.Time) error

	// BeginLogin starts sign-in id of a running slot (its run): claimed =
	// false and that one when one is live for the run and fresh (reported
	// within LoginStale). Business when the slot isn't running.
	BeginLogin(ctx context.Context, organization identity.OrganizationID, agent identity.AgentID, slot int, id, by string) (login Login, claimed bool, err error)
	// LoginProgress updates live sign-in id (empty fields stay; done or
	// failed drop a code not taken) and keeps it fresh; Conflict once it
	// is over or replaced.
	LoginProgress(ctx context.Context, organization identity.OrganizationID, agent identity.AgentID, slot int, id string, update LoginUpdate) (Login, error)
	// TakeLoginCode takes the code submitted to waiting sign-in id (""
	// when none yet), clearing it and making it verifying.
	TakeLoginCode(ctx context.Context, organization identity.OrganizationID, agent identity.AgentID, slot int, id string) (string, error)
	// SubmitLoginCode stores a code for the slot's fresh waiting sign-in
	// (Conflict when none waits or one was submitted).
	SubmitLoginCode(ctx context.Context, organization identity.OrganizationID, agent identity.AgentID, slot int, code string) (Login, error)
	Login(ctx context.Context, organization identity.OrganizationID, agent identity.AgentID, slot int) (Login, error)
}
