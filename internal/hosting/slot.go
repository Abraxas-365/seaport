package hosting

import (
	"strconv"
	"time"

	"github.com/Abraxas-365/seaport/internal/errx"
	"github.com/Abraxas-365/seaport/internal/identity"
	"github.com/Abraxas-365/seaport/internal/text"
)

// MaxSlotsPerAgent bounds one hosted agent's slots.
const MaxSlotsPerAgent = 10

// Slot states.
const (
	SlotStopped  = "stopped"
	SlotStarting = "starting"
	SlotRunning  = "running"
	SlotStopping = "stopping"
	SlotFailed   = "failed"
)

// Hosted is an agent the port runs, in how many slots. What they run is
// the agent's config (its harness and machine).
type Hosted struct {
	Agent     identity.AgentID `json:"agent_id"`
	Slots     []Slot           `json:"slots"`
	UpdatedBy string           `json:"updated_by"`
	UpdatedAt time.Time        `json:"updated_at"`
}

// Slot is one stable instance of a hosted agent (<agent>-<index>): its
// own container, state volume (harness sign-in, sessions) and workspace.
type Slot struct {
	Index int    `db:"idx" json:"index"`
	State string `db:"state" json:"state"`
	// Detail says why it failed or what it is doing (no secrets).
	Detail string `db:"detail" json:"detail"`
	// Image is the image digest it runs (or last ran).
	Image string `db:"image" json:"image"`
	// ConfigVersion is the agent config version it started on (0: never
	// started).
	ConfigVersion int `db:"config_version" json:"config_version"`
	// Outdated: it runs and its machine is not the agent's current one
	// (the config's machine changed), or its image is not the agent's
	// current build, which is ready (a new Seaport release changed the
	// base image): the supervisor restarts it once idle (after a machine
	// change, once the new image is built). An instructions edit doesn't
	// make it outdated (the harness picks the new kit up in place).
	Outdated  bool       `db:"outdated" json:"outdated"`
	StartedAt *time.Time `db:"started_at" json:"started_at"`
	ActiveAt  *time.Time `db:"active_at" json:"active_at"`
	UpdatedAt time.Time  `db:"updated_at" json:"updated_at"`
}

// SlotName is a slot's stable name: <agent>-<index>.
func SlotName(agentName string, index int) string { return agentName + "-" + strconv.Itoa(index) }

// Host hosts an agent (or changes its slots). Its config must have a
// harness and a machine.
type Host struct {
	// Slots is how many instances (default 1).
	Slots int
	// By is filled from the caller (not client input).
	By string
}

func (h Host) Validate() error {
	if h.Slots < 0 || h.Slots > MaxSlotsPerAgent {
		return errx.InvalidField("slots", "slots must be between 1 and "+strconv.Itoa(MaxSlotsPerAgent))
	}
	return text.Optional("by", h.By, text.MaxName)
}

// SlotsOrDefault is Slots, 1 when unset.
func (h Host) SlotsOrDefault() int {
	if h.Slots == 0 {
		return 1
	}
	return h.Slots
}
