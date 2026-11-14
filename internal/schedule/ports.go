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
	// FireDue posts the message of every schedule due at now (what the
	// runner of each process does every tick) and says how many it fired.
	FireDue(ctx context.Context, now time.Time) (int, error)
}

type Queries interface {
	Find(ctx context.Context, organization identity.OrganizationID, schedule identity.ScheduleID) (Schedule, error)
	List(ctx context.Context, organization identity.OrganizationID, filter Filter, page query.Pagination) (query.Paginated[Schedule], error)
	// Preview lists the next fire times of an expression.
	Preview(ctx context.Context, organization identity.OrganizationID, input PreviewInput) ([]time.Time, error)
}

type Repository interface {
	// Create inserts the schedule, counting the organization's under a
	// lock first (limit ≤ 0: unlimited).
	Create(ctx context.Context, organization identity.OrganizationID, schedule Schedule, limit int) (Schedule, error)
	Find(ctx context.Context, organization identity.OrganizationID, schedule identity.ScheduleID) (Schedule, error)
	List(ctx context.Context, organization identity.OrganizationID, filter Filter, page query.Pagination) (query.Paginated[Schedule], error)
	// Update applies the set fields and the recomputed next slot.
	Update(ctx context.Context, organization identity.OrganizationID, schedule identity.ScheduleID, input Update, next time.Time) (Schedule, error)
	// Delete removes the schedule and returns it.
	Delete(ctx context.Context, organization identity.OrganizationID, schedule identity.ScheduleID) (Schedule, error)
	// Record notes a firing that made a ship: last_at and last_ship_id.
	Record(ctx context.Context, organization identity.OrganizationID, schedule identity.ScheduleID, at time.Time, ship identity.ShipID) error
	// Due claims up to limit enabled schedules with next_at ≤ now (rows
	// locked FOR UPDATE SKIP LOCKED, so two processes never take one),
	// asks plan what to do with each, stores the new next_at in the same
	// transaction and returns the ones to fire plus how many it claimed
	// (limit claimed: there may be more).
	Due(ctx context.Context, now time.Time, limit int, plan func(Schedule) Advance) (fire []Due, claimed int, err error)
}

// Deliverer hands the message of a schedule to the ship domain (bound
// late: the ship service is built after this one), so it goes through
// customs, routing and delivery as any message does.
type Deliverer interface {
	Receive(ctx context.Context, organization identity.OrganizationID, input ship.Inbound) (ship.Ship, bool, error)
}
