// Package schedule is Seaport's cron: a schedule makes the port itself post
// a prompt into a conversation at given times, as a system sender, so the
// usual pipeline (customs, routing, delivery, threads, replies) does the
// rest. Cron is the pure expression type; the runner lives in schedulesvc.
package schedule

import (
	"strings"
	"time"

	"github.com/Abraxas-365/seaport/internal/errx"
	"github.com/Abraxas-365/seaport/internal/identity"
	"github.com/Abraxas-365/seaport/internal/text"
)

const (
	// MaxSchedules is the default bound of an organization's schedules.
	MaxSchedules = 100
	// MaxName bounds a schedule's name: it is part of the sender's name
	// ("Seaport · <name>"), which a ship bounds by text.MaxName.
	MaxName = 100
	// MissedGrace is how late a slot may still fire: the port was down
	// (or every process busy) for longer, the slot is skipped and the next
	// one is computed from the present.
	MissedGrace = 2 * time.Minute
	// MaxText bounds a prompt, leaving room for the "@agent " prefix.
	MaxText = text.MaxMessage - 256
	// Preview is how many next fire times an answer lists.
	Preview = 3
	// SenderID is the sender of every scheduled message; customs rules and
	// the web recognize system senders by the "system:" prefix.
	SenderID = "system:schedule"
	// DefaultTimezone applies when none is given.
	DefaultTimezone = "UTC"
)

// Schedule posts Text into Conversation whenever Cron matches (on the wall
// clock of Timezone).
type Schedule struct {
	ID           identity.ScheduleID     `db:"id" json:"id"`
	Organization identity.OrganizationID `db:"organization_id" json:"-"`
	Conversation identity.ConversationID `db:"conversation_id" json:"conversation_id"`
	// Agent is called by name in the text ("@name ..."), so routing picks
	// it; nil leaves the choice to the dock's team (default agent).
	Agent    *identity.AgentID `db:"agent_id" json:"agent_id,omitempty"`
	Name     string            `db:"name" json:"name"`
	Cron     string            `db:"cron" json:"cron"`
	Timezone string            `db:"timezone" json:"timezone"`
	Text     string            `db:"text" json:"text"`
	Enabled  bool              `db:"enabled" json:"enabled"`
	// NextAt is the next slot (always computed, also while disabled: it
	// is recomputed when the schedule is saved).
	NextAt    time.Time        `db:"next_at" json:"next_at"`
	LastAt    *time.Time       `db:"last_at" json:"last_at,omitempty"`
	LastShip  *identity.ShipID `db:"last_ship_id" json:"last_ship_id,omitempty"`
	CreatedAt time.Time        `db:"created_at" json:"created_at"`
	UpdatedAt time.Time        `db:"updated_at" json:"updated_at"`
	// Preview lists the next fire times (filled by the service when the
	// schedule is enabled).
	Preview []time.Time `db:"-" json:"preview"`
}

// Due is a schedule the runner claimed: Slot is the time it was due for
// (its NextAt before the claim advanced it).
type Due struct {
	Schedule
	Slot time.Time
}

// Claim is what one Repository.Due call took: the schedules to fire, the
// ones plan could not evaluate and the repository switched off (Enabled
// already false), and how many rows it claimed in all.
type Claim struct {
	Fire     []Due
	Disabled []Schedule
	Claimed  int
}

// Advance is the runner's decision about a claimed schedule: when it runs
// next and whether the claimed slot is fired (one missed by more than
// MissedGrace is not).
type Advance struct {
	Next time.Time
	Fire bool
}

// Location resolves an IANA zone name ("" is UTC); "Local" is refused so a
// schedule means the same on every host.
func Location(name string) (*time.Location, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		name = DefaultTimezone
	}
	if strings.EqualFold(name, "local") {
		return nil, errx.InvalidField("timezone", "timezone must be an IANA name such as America/Lima or UTC")
	}
	loc, err := time.LoadLocation(name)
	if err != nil {
		return nil, errx.InvalidField("timezone", "timezone must be an IANA name such as America/Lima or UTC")
	}
	return loc, nil
}

// Upcoming lists the next n fire times after the given instant.
func Upcoming(c Cron, loc *time.Location, after time.Time, n int) []time.Time {
	out := make([]time.Time, 0, n)
	for len(out) < n {
		after = c.Next(after, loc)
		if after.IsZero() {
			break
		}
		out = append(out, after.UTC())
	}
	return out
}

// Create is the input of Commands.Create.
type Create struct {
	Name         string
	Conversation identity.ConversationID
	Agent        *identity.AgentID
	Cron         string
	Timezone     string
	Text         string
	Enabled      bool
}

func (c Create) Validate() error {
	if err := text.Required("name", c.Name, MaxName); err != nil {
		return err
	}
	if c.Conversation.IsZero() {
		return errx.InvalidField("conversation_id", "conversation_id is required")
	}
	if c.Agent != nil && c.Agent.IsZero() {
		return errx.InvalidField("agent_id", "agent_id is not a valid id")
	}
	if _, err := ParseCron(c.Cron); err != nil {
		return err
	}
	if _, err := Location(c.Timezone); err != nil {
		return err
	}
	return text.Required("text", c.Text, MaxText)
}

// Update changes what is set (the conversation never changes). Agent set
// to the zero id clears the agent.
type Update struct {
	Name     *string
	Agent    *identity.AgentID
	Cron     *string
	Timezone *string
	Text     *string
	Enabled  *bool
}

func (u Update) Validate() error {
	if u.Name == nil && u.Agent == nil && u.Cron == nil && u.Timezone == nil && u.Text == nil && u.Enabled == nil {
		return errx.Validation("nothing to update")
	}
	if u.Name != nil {
		if err := text.Required("name", *u.Name, MaxName); err != nil {
			return err
		}
	}
	if u.Cron != nil {
		if _, err := ParseCron(*u.Cron); err != nil {
			return err
		}
	}
	if u.Timezone != nil {
		if _, err := Location(*u.Timezone); err != nil {
			return err
		}
	}
	if u.Text != nil {
		return text.Required("text", *u.Text, MaxText)
	}
	return nil
}

// Filter narrows List.
type Filter struct {
	Conversation *identity.ConversationID
}

// PreviewInput asks for the next fire times of an expression without
// saving anything (the web's "new schedule" dialog).
type PreviewInput struct {
	Cron     string
	Timezone string
}

func (p PreviewInput) Validate() error {
	if _, err := ParseCron(p.Cron); err != nil {
		return err
	}
	_, err := Location(p.Timezone)
	return err
}
