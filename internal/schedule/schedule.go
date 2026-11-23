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

	// MinOneShotDelay is how far ahead a person's one-shot schedule (the
	// API's "at") must be.
	MinOneShotDelay = 30 * time.Second
	// MaxAlarmsPerConversation bounds the pending alarms one agent has in
	// one conversation; MaxAlarmsPerAgent those it has in all; and
	// MaxAlarmsPerDay the alarms it may create in a rolling 24 hours (done
	// ones count: the guard against an agent that sets an alarm each time
	// it wakes up).
	MaxAlarmsPerConversation = 3
	MaxAlarmsPerAgent        = 20
	MaxAlarmsPerDay          = 50
	// MinAlarmDelay and MaxAlarmAhead bound how far ahead an agent sets a
	// one-shot alarm.
	MinAlarmDelay = time.Minute
	MaxAlarmAhead = 30 * 24 * time.Hour
	// MaxAlarmNote bounds the note an alarm brings back to the agent.
	MaxAlarmNote = 4000
	// AlarmGrace is how late a one-shot may still fire (the port was down):
	// a reminder that arrives late is still useful, one later than this is
	// closed without firing.
	AlarmGrace = time.Hour
	// DoneKeep is how long a one-shot that fired (or was missed) stays
	// visible before the runner purges it.
	DoneKeep = 7 * 24 * time.Hour
)

// Schedule posts Text into Conversation whenever Cron matches (on the wall
// clock of Timezone), or once at RunAt (a one-shot: Cron is empty). A
// one-shot set by an agent is an alarm (CreatedBy).
type Schedule struct {
	ID           identity.ScheduleID     `db:"id" json:"id"`
	Organization identity.OrganizationID `db:"organization_id" json:"-"`
	Conversation identity.ConversationID `db:"conversation_id" json:"conversation_id"`
	// Agent is called by name in the text ("@name ..."), so routing picks
	// it; nil leaves the choice to the dock's team (default agent).
	Agent *identity.AgentID `db:"agent_id" json:"agent_id,omitempty"`
	Name  string            `db:"name" json:"name"`
	// Cron is the expression; empty for a one-shot (RunAt is set then).
	Cron string `db:"cron" json:"cron"`
	// RunAt is the time of a one-shot schedule (nil: it is recurring).
	RunAt *time.Time `db:"run_at" json:"run_at,omitempty"`
	// CreatedBy is the agent that set this schedule as an alarm (nil: a
	// person created it).
	CreatedBy *identity.AgentID `db:"created_by" json:"created_by,omitempty"`
	// DoneAt is when a one-shot fired or was missed (it is then disabled
	// and stays visible for DoneKeep).
	DoneAt   *time.Time `db:"done_at" json:"done_at,omitempty"`
	Timezone string     `db:"timezone" json:"timezone"`
	Text     string     `db:"text" json:"text"`
	Enabled  bool       `db:"enabled" json:"enabled"`
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
// ones plan could not evaluate (or closed unfired: a missed one-shot) and the
// repository switched off (Enabled already false), and how many rows it claimed in all.
type Claim struct {
	Fire     []Due
	Disabled []Schedule
	Claimed  int
}

// Advance is the runner's decision about a claimed schedule: when it runs
// next and whether the claimed slot is fired (one missed by more than
// MissedGrace is not). Done closes a one-shot (fired or missed): it never
// runs again, and Next is ignored. A zero Next without Done means the
// schedule can't be evaluated: it is switched off.
type Advance struct {
	Next time.Time
	Fire bool
	Done bool
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

// Create is the input of Commands.Create: exactly one of Cron (recurring)
// and At (a one-shot).
type Create struct {
	Name         string
	Conversation identity.ConversationID
	Agent        *identity.AgentID
	Cron         string
	At           *time.Time
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
	switch {
	case c.At != nil && strings.TrimSpace(c.Cron) != "":
		return errx.Validation("give cron or at, not both")
	case c.At != nil:
		if c.At.IsZero() {
			return errx.InvalidField("at", "at is not a valid time")
		}
	default:
		if _, err := ParseCron(c.Cron); err != nil {
			return err
		}
	}
	if _, err := Location(c.Timezone); err != nil {
		return err
	}
	return text.Required("text", c.Text, MaxText)
}

// Update changes what is set (the conversation never changes). Agent set
// to the zero id clears the agent.
type Update struct {
	Name  *string
	Agent *identity.AgentID
	Cron  *string
	// At moves a one-shot (and, being the time it runs at, makes it
	// pending again: done_at is cleared); a recurring schedule has none.
	At       *time.Time
	Timezone *string
	Text     *string
	Enabled  *bool
}

func (u Update) Validate() error {
	if u.Name == nil && u.Agent == nil && u.Cron == nil && u.At == nil && u.Timezone == nil && u.Text == nil && u.Enabled == nil {
		return errx.Validation("nothing to update")
	}
	if u.Cron != nil && u.At != nil {
		return errx.Validation("give cron or at, not both")
	}
	if u.At != nil && u.At.IsZero() {
		return errx.InvalidField("at", "at is not a valid time")
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

// Filter narrows List. Lists show the pending schedules; IncludeDone adds
// the one-shots that already ran (the last DoneKeep).
type Filter struct {
	Conversation *identity.ConversationID
	// Agent keeps the alarms this agent set (CreatedBy).
	Agent       *identity.AgentID
	IncludeDone bool
}

// AlarmLimits are the per-agent bounds CreateAlarm enforces under the
// organization's lock (see the MaxAlarms… constants).
type AlarmLimits struct {
	PerConversation int
	PerAgent        int
	PerDay          int
}

// DefaultAlarmLimits are the bounds of the MaxAlarms… constants.
var DefaultAlarmLimits = AlarmLimits{PerConversation: MaxAlarmsPerConversation, PerAgent: MaxAlarmsPerAgent, PerDay: MaxAlarmsPerDay}

// SetAlarm is what an agent asks for: wake itself in Conversation, with
// Note, once (At, or In from now) or on a Cron. Exactly one of the three.
type SetAlarm struct {
	Agent        identity.AgentID
	Conversation identity.ConversationID
	// Name is optional: "<agent>-<8 hex>" when empty.
	Name     string
	Note     string
	At       *time.Time
	In       *time.Duration
	Cron     string
	Timezone string
}

func (a SetAlarm) Validate() error {
	if a.Agent.IsZero() {
		return errx.InvalidField("agent_id", "an alarm belongs to an agent")
	}
	if a.Conversation.IsZero() {
		return errx.InvalidField("conversation_id", "conversation_id is required")
	}
	if err := text.Required("note", a.Note, MaxAlarmNote); err != nil {
		return err
	}
	if err := text.Optional("name", a.Name, MaxName); err != nil {
		return err
	}
	n := 0
	if a.At != nil {
		n++
	}
	if a.In != nil {
		n++
	}
	if strings.TrimSpace(a.Cron) != "" {
		n++
	}
	if n != 1 {
		return errx.Validation("give exactly one of at, in and cron")
	}
	if a.At != nil && a.At.IsZero() {
		return errx.InvalidField("at", "at is not a valid time")
	}
	if a.In != nil && *a.In <= 0 {
		return errx.InvalidField("in", "in must be a positive duration")
	}
	if a.Cron != "" {
		if _, err := ParseCron(a.Cron); err != nil {
			return err
		}
	}
	_, err := Location(a.Timezone)
	return err
}

// When works out the alarm's first run from now: the cron expression
// (normalized, "" for a one-shot) and the next time. A one-shot must be
// MinAlarmDelay to MaxAlarmAhead ahead. The input must be valid.
func (a SetAlarm) When(now time.Time) (cron string, next time.Time, err error) {
	if strings.TrimSpace(a.Cron) != "" {
		c, err := ParseCron(a.Cron)
		if err != nil {
			return "", time.Time{}, err
		}
		loc, err := Location(a.Timezone)
		if err != nil {
			return "", time.Time{}, err
		}
		return c.String(), c.Next(now, loc), nil
	}
	at := now
	switch {
	case a.At != nil:
		at = *a.At
	case a.In != nil:
		at = now.Add(*a.In)
	}
	if at.Before(now.Add(MinAlarmDelay)) {
		return "", time.Time{}, errx.InvalidField("at", "an alarm must be at least "+MinAlarmDelay.String()+" ahead")
	}
	if at.After(now.Add(MaxAlarmAhead)) {
		return "", time.Time{}, errx.InvalidField("at", "an alarm can be at most 30 days ahead")
	}
	return "", at.UTC(), nil
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
