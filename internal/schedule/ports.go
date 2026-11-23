package schedule

import (
	"context"
	"time"

	"github.com/Abraxas-365/seaport/internal/identity"
	"github.com/Abraxas-365/seaport/internal/query"
	"github.com/Abraxas-365/seaport/internal/ship"
)

// Every method takes the organization right after ctx; another
// organization's schedule, conversation or agent is NotFound
// (TestTenantFirstPorts). Only the runner's FireDue and the repository's
// Due span organizations (systemWide).
type Commands interface {
	Create(ctx context.Context, organization identity.OrganizationID, input Create) (Schedule, error)
	// Update changes the schedule and recomputes its next slot.
	Update(ctx context.Context, organization identity.OrganizationID, schedule identity.ScheduleID, input Update) (Schedule, error)
	Delete(ctx context.Context, organization identity.OrganizationID, schedule identity.ScheduleID) error
	// Run posts the schedule's message now (enabled or not), through the
	// same path as the runner, and returns the ship; the next slot is
	// untouched.
	Run(ctx context.Context, organization identity.OrganizationID, schedule identity.ScheduleID) (ship.Ship, error)
	// SetAlarm is an agent waking itself later in a conversation it
	// works: a schedule it created (CreatedBy), within the per-agent
	// limits (QuotaExceeded when over one). NotFound unless the agent
	// works the conversation's dock; Business when its profile has
	// alarms off.
	SetAlarm(ctx context.Context, organization identity.OrganizationID, input SetAlarm) (Schedule, error)
	// CancelAlarm deletes an alarm the agent set (NotFound for any other
	// schedule, whoever created it).
	CancelAlarm(ctx context.Context, organization identity.OrganizationID, agent identity.AgentID, schedule identity.ScheduleID) error
	// FireDue posts the message of every schedule due at now (what the
	// runner of each process does every tick) and says how many it fired.
	FireDue(ctx context.Context, now time.Time) (int, error)
}

type Queries interface {
	Find(ctx context.Context, organization identity.OrganizationID, schedule identity.ScheduleID) (Schedule, error)
	List(ctx context.Context, organization identity.OrganizationID, filter Filter, page query.Pagination) (query.Paginated[Schedule], error)
	// Preview lists the next fire times of an expression.
	Preview(ctx context.Context, organization identity.OrganizationID, input PreviewInput) ([]time.Time, error)
	// Alarms lists the pending alarms the agent set, oldest first, in one
	// conversation or (nil) in all.
	Alarms(ctx context.Context, organization identity.OrganizationID, agent identity.AgentID, conversation *identity.ConversationID) ([]Schedule, error)
}

type Repository interface {
	// Create inserts the schedule, counting the organization's under a
	// lock first (limit ≤ 0: unlimited).
	Create(ctx context.Context, organization identity.OrganizationID, schedule Schedule, limit int) (Schedule, error)
	// CreateAlarm inserts an alarm (schedule.CreatedBy set) under the
	// same organization lock: the agent's pending alarms in the
	// conversation and in all, and those it created in the last 24 hours
	// (done ones included), are counted against limits first, then the
	// organization's pending schedules against orgLimit (≤ 0: unlimited).
	CreateAlarm(ctx context.Context, organization identity.OrganizationID, schedule Schedule, limits AlarmLimits, orgLimit int) (Schedule, error)
	Find(ctx context.Context, organization identity.OrganizationID, schedule identity.ScheduleID) (Schedule, error)
	List(ctx context.Context, organization identity.OrganizationID, filter Filter, page query.Pagination) (query.Paginated[Schedule], error)
	// Update applies the set fields and the recomputed next slot (for a
	// one-shot, next is its run_at; moving it clears done_at).
	Update(ctx context.Context, organization identity.OrganizationID, schedule identity.ScheduleID, input Update, next time.Time) (Schedule, error)
	// Delete removes the schedule and returns it.
	Delete(ctx context.Context, organization identity.OrganizationID, schedule identity.ScheduleID) (Schedule, error)
	// Record notes a firing that made a ship: last_at and last_ship_id.
	Record(ctx context.Context, organization identity.OrganizationID, schedule identity.ScheduleID, at time.Time, ship identity.ShipID) error
	// Due claims up to limit enabled schedules with next_at ≤ now (rows
	// locked FOR UPDATE SKIP LOCKED, so two processes never take one),
	// asks plan what to do with each, stores the new next_at in the same
	// transaction (Advance.Done closes a one-shot: disabled, done_at = now)
	// and returns the ones to fire plus how many it claimed (limit
	// claimed: there may be more) and the ones it switched off. It also
	// purges one-shots done for longer than DoneKeep (a bounded delete).
	Due(ctx context.Context, now time.Time, limit int, plan func(Schedule) Advance) (Claim, error)
}

// Deliverer hands the message of a schedule to the ship domain (bound
// late: the ship service is built after this one), so it goes through
// customs, routing and delivery as any message does.
type Deliverer interface {
	Receive(ctx context.Context, organization identity.OrganizationID, input ship.Inbound) (ship.Ship, bool, error)
}
