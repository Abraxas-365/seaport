// Package schedulesvc implements the schedule ports and the runner that
// fires them.
package schedulesvc

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"log/slog"
	"slices"
	"strconv"
	"time"

	"github.com/Abraxas-365/seaport/internal/agent"
	"github.com/Abraxas-365/seaport/internal/conversation"
	"github.com/Abraxas-365/seaport/internal/errx"
	"github.com/Abraxas-365/seaport/internal/event"
	"github.com/Abraxas-365/seaport/internal/identity"
	"github.com/Abraxas-365/seaport/internal/query"
	"github.com/Abraxas-365/seaport/internal/schedule"
	"github.com/Abraxas-365/seaport/internal/ship"
	"github.com/Abraxas-365/seaport/internal/text"
)

// batch is how many due schedules one claim takes.
const batch = 20

// maxBatches bounds one FireDue round (a safety net: claimed rows are
// advanced, so the loop ends by itself).
const maxBatches = 50

// fireTimeout bounds posting one claimed batch, which runs to its end even
// while the process shuts down.
const fireTimeout = 10 * time.Second

// errAlarmsOff: the agent that set an alarm has the switch off now.
var errAlarmsOff = errors.New("alarms are off for the agent")

type Service struct {
	repo   schedule.Repository
	convs  conversation.Queries
	agents agent.Queries
	events event.Publisher
	// max bounds an organization's schedules (≤ 0: unlimited).
	max int
	// deliverer is the ship service (Bind; nil: nothing fires).
	deliverer schedule.Deliverer
	now       func() time.Time
}

func New(repo schedule.Repository, convs conversation.Queries, agents agent.Queries, events event.Publisher, max int) *Service {
	if events == nil {
		events = event.Nop{}
	}
	return &Service{repo: repo, convs: convs, agents: agents, events: events, max: max, now: time.Now}
}

// Bind wires the ship service in (built after this one).
func (s *Service) Bind(d schedule.Deliverer) { s.deliverer = d }

var (
	_ schedule.Commands = (*Service)(nil)
	_ schedule.Queries  = (*Service)(nil)
)

func (s *Service) Create(ctx context.Context, org identity.OrganizationID, input schedule.Create) (schedule.Schedule, error) {
	if err := input.Validate(); err != nil {
		return schedule.Schedule{}, err
	}
	tz := zone(input.Timezone)
	now := s.now()
	var spec string
	var runAt *time.Time
	var next time.Time
	if input.At != nil { // a one-shot
		at := input.At.UTC()
		if at.Before(now.Add(schedule.MinOneShotDelay)) {
			return schedule.Schedule{}, errx.InvalidField("at", "at must be at least "+schedule.MinOneShotDelay.String()+" from now")
		}
		runAt, next = &at, at
	} else {
		cron, _ := schedule.ParseCron(input.Cron) // validated above
		loc, _ := schedule.Location(tz)
		spec, next = cron.String(), cron.Next(now, loc)
	}
	if _, err := s.convs.Find(ctx, org, input.Conversation); err != nil {
		return schedule.Schedule{}, errx.Wrap(err, "find conversation", errx.TypeInternal)
	}
	if input.Agent != nil {
		if _, err := s.agents.Find(ctx, org, *input.Agent); err != nil {
			return schedule.Schedule{}, errx.Wrap(err, "find agent", errx.TypeInternal)
		}
	}
	row := schedule.Schedule{ID: identity.NewScheduleID(), Organization: org, Conversation: input.Conversation, Agent: input.Agent,
		Name: text.Clean(input.Name), Cron: spec, RunAt: runAt, Timezone: tz, Text: text.Clean(input.Text), Enabled: input.Enabled,
		NextAt: next}
	out, err := s.repo.Create(ctx, org, row, s.max)
	if err != nil {
		return schedule.Schedule{}, errx.Wrap(err, "create schedule", errx.TypeInternal)
	}
	out = s.withPreview(out)
	s.events.Publish(org, schedule.EventCreated, out)
	return out, nil
}

func (s *Service) Update(ctx context.Context, org identity.OrganizationID, id identity.ScheduleID, input schedule.Update) (schedule.Schedule, error) {
	if err := input.Validate(); err != nil {
		return schedule.Schedule{}, err
	}
	cur, err := s.repo.Find(ctx, org, id)
	if err != nil {
		return schedule.Schedule{}, errx.Wrap(err, "find schedule", errx.TypeInternal)
	}
	oneShot := cur.RunAt != nil
	if oneShot && input.Cron != nil {
		return schedule.Schedule{}, errx.Business("a one-shot schedule has no cron: delete it and create a recurring one")
	}
	if !oneShot && input.At != nil {
		return schedule.Schedule{}, errx.Business("a recurring schedule has no at: delete it and create a one-shot")
	}
	// An alarm wakes the agent that set it ("Alarm you set…"): people may
	// switch, move, reword or delete it, not hand it to someone else.
	if cur.CreatedBy != nil && input.Agent != nil && *input.Agent != *cur.CreatedBy {
		return schedule.Schedule{}, errx.Business("an alarm wakes the agent that set it: delete it and create a schedule for another agent")
	}
	now := s.now()
	// The next slot comes from the expression and zone it will have (a
	// one-shot's is its time).
	spec, tz := cur.Cron, cur.Timezone
	if input.Cron != nil {
		spec = *input.Cron
	}
	if input.Timezone != nil {
		tz = zone(*input.Timezone)
	}
	var cron schedule.Cron
	var loc *time.Location
	if !oneShot {
		if cron, err = schedule.ParseCron(spec); err != nil {
			return schedule.Schedule{}, err
		}
		if loc, err = schedule.Location(tz); err != nil {
			return schedule.Schedule{}, err
		}
	}
	next := cur.NextAt
	if oneShot {
		if _, err := schedule.Location(tz); err != nil {
			return schedule.Schedule{}, err
		}
		switch {
		case input.At != nil:
			at := input.At.UTC()
			if at.Before(now.Add(schedule.MinOneShotDelay)) {
				return schedule.Schedule{}, errx.InvalidField("at", "at must be at least "+schedule.MinOneShotDelay.String()+" from now")
			}
			next = at
			input.At = &at
			if cur.DoneAt != nil && input.Enabled == nil { // moving a finished one-shot arms it again
				on := true
				input.Enabled = &on
			}
		case cur.DoneAt != nil && input.Enabled != nil && *input.Enabled:
			return schedule.Schedule{}, errx.Business("this one-shot already ran: give a new time (at) to arm it again")
		}
	}
	if input.Agent != nil && !input.Agent.IsZero() {
		if _, err := s.agents.Find(ctx, org, *input.Agent); err != nil {
			return schedule.Schedule{}, errx.Wrap(err, "find agent", errx.TypeInternal)
		}
	}
	if input.Name != nil {
		n := text.Clean(*input.Name)
		input.Name = &n
	}
	if input.Text != nil {
		t := text.Clean(*input.Text)
		input.Text = &t
	}
	if input.Cron != nil {
		c := cron.String()
		input.Cron = &c
	}
	if input.Timezone != nil {
		input.Timezone = &tz
	}
	// The slot moves only when what decides it changed: the expression, the
	// zone, or switching on (an old next_at would fire a stale slot).
	if !oneShot && (cron.String() != cur.Cron || tz != cur.Timezone || (input.Enabled != nil && *input.Enabled != cur.Enabled)) {
		next = cron.Next(now, loc)
	}
	out, err := s.repo.Update(ctx, org, id, input, next)
	if err != nil {
		return schedule.Schedule{}, errx.Wrap(err, "update schedule", errx.TypeInternal)
	}
	out = s.withPreview(out)
	s.events.Publish(org, schedule.EventUpdated, out)
	return out, nil
}

func (s *Service) Delete(ctx context.Context, org identity.OrganizationID, id identity.ScheduleID) error {
	if id.IsZero() {
		return errx.InvalidField("schedule_id", "schedule_id is required")
	}
	gone, err := s.repo.Delete(ctx, org, id)
	if err != nil {
		return errx.Wrap(err, "delete schedule", errx.TypeInternal)
	}
	gone.Preview = []time.Time{}
	s.events.Publish(org, schedule.EventDeleted, gone)
	return nil
}

func (s *Service) Find(ctx context.Context, org identity.OrganizationID, id identity.ScheduleID) (schedule.Schedule, error) {
	if id.IsZero() {
		return schedule.Schedule{}, errx.InvalidField("schedule_id", "schedule_id is required")
	}
	out, err := s.repo.Find(ctx, org, id)
	if err != nil {
		return schedule.Schedule{}, errx.Wrap(err, "find schedule", errx.TypeInternal)
	}
	return s.withPreview(out), nil
}

func (s *Service) List(ctx context.Context, org identity.OrganizationID, filter schedule.Filter, page query.Pagination) (query.Paginated[schedule.Schedule], error) {
	if filter.Conversation != nil { // another organization's conversation is NotFound, not empty
		if _, err := s.convs.Find(ctx, org, *filter.Conversation); err != nil {
			return query.Paginated[schedule.Schedule]{}, errx.Wrap(err, "find conversation", errx.TypeInternal)
		}
	}
	out, err := s.repo.List(ctx, org, filter, page.Normalize())
	if err != nil {
		return out, errx.Wrap(err, "list schedules", errx.TypeInternal)
	}
	for i := range out.Items {
		out.Items[i] = s.withPreview(out.Items[i])
	}
	return out, nil
}

func (s *Service) Preview(_ context.Context, _ identity.OrganizationID, input schedule.PreviewInput) ([]time.Time, error) {
	if err := input.Validate(); err != nil {
		return nil, err
	}
	cron, _ := schedule.ParseCron(input.Cron)
	loc, _ := schedule.Location(input.Timezone)
	return schedule.Upcoming(cron, loc, s.now(), schedule.Preview), nil
}

// withPreview fills the next fire times (none while disabled or when the
// stored expression no longer parses).
func (s *Service) withPreview(sc schedule.Schedule) schedule.Schedule {
	sc.Preview = []time.Time{}
	if !sc.Enabled {
		return sc
	}
	if sc.RunAt != nil {
		sc.Preview = []time.Time{sc.RunAt.UTC()}
		return sc
	}
	cron, err := schedule.ParseCron(sc.Cron)
	if err != nil {
		return sc
	}
	loc, err := schedule.Location(sc.Timezone)
	if err != nil {
		return sc
	}
	sc.Preview = schedule.Upcoming(cron, loc, s.now(), schedule.Preview)
	return sc
}

func zone(name string) string {
	if name = text.Clean(name); name == "" {
		return schedule.DefaultTimezone
	}
	return name
}

// Run posts the schedule's message now; it is its own slot, so running it
// twice posts twice.
func (s *Service) Run(ctx context.Context, org identity.OrganizationID, id identity.ScheduleID) (ship.Ship, error) {
	if id.IsZero() {
		return ship.Ship{}, errx.InvalidField("schedule_id", "schedule_id is required")
	}
	sc, err := s.repo.Find(ctx, org, id)
	if err != nil {
		return ship.Ship{}, errx.Wrap(err, "find schedule", errx.TypeInternal)
	}
	now := s.now()
	st, _, err := s.fire(ctx, sc, now, "run-"+strconv.FormatInt(now.UnixNano(), 10))
	if errors.Is(err, errAlarmsOff) {
		return ship.Ship{}, errx.Business("alarms are off for the agent that set this alarm")
	}
	return st, err
}

// FireDue claims the schedules due at now, advances each to its next slot
// and posts the message of those still within schedule.MissedGrace; the
// slots missed by longer are skipped (logged). Several processes may call
// it at once: a slot is claimed by exactly one.
func (s *Service) FireDue(ctx context.Context, now time.Time) (int, error) {
	fired := 0
	for range maxBatches {
		claim, err := s.repo.Due(ctx, now, batch, func(sc schedule.Schedule) schedule.Advance {
			return s.plan(sc, now)
		})
		if err != nil {
			return fired, errx.Wrap(err, "claim due schedules", errx.TypeInternal)
		}
		for _, sc := range claim.Disabled {
			s.events.Publish(sc.Organization, schedule.EventUpdated, sc)
		}
		// The slots are claimed (committed): post them even if the process
		// is shutting down, or they would be lost (a claimed slot never
		// comes back).
		fctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), fireTimeout)
		for _, d := range claim.Fire {
			slot := strconv.FormatInt(d.Slot.Unix(), 10)
			if _, _, err := s.fire(fctx, d.Schedule, d.Slot, slot); err != nil {
				if errors.Is(err, errAlarmsOff) {
					continue
				}
				slog.Error("fire schedule", "schedule", d.ID, "name", d.Name, "err", err)
				continue
			}
			fired++
		}
		cancel()
		if claim.Claimed < batch {
			break
		}
	}
	return fired, nil
}

// plan decides what happens to a claimed schedule at now: the next slot is
// counted from the present (never from the old one: a port that was down
// doesn't fire a backlog) and the claimed slot fires only if it is not too
// late.
func (s *Service) plan(sc schedule.Schedule, now time.Time) schedule.Advance {
	if sc.RunAt != nil { // a one-shot runs once, late by up to AlarmGrace, and is done either way
		adv := schedule.Advance{Fire: now.Sub(sc.NextAt) <= schedule.AlarmGrace, Done: true}
		if !adv.Fire {
			slog.Warn("one-shot schedule missed, closed without firing", "schedule", sc.ID, "name", sc.Name,
				"at", sc.NextAt, "late", now.Sub(sc.NextAt).Round(time.Second))
		}
		return adv
	}
	cron, err := schedule.ParseCron(sc.Cron)
	if err == nil {
		var loc *time.Location
		if loc, err = schedule.Location(sc.Timezone); err == nil {
			adv := schedule.Advance{Next: cron.Next(now, loc), Fire: now.Sub(sc.NextAt) <= schedule.MissedGrace}
			if !adv.Fire {
				slog.Warn("schedule slot missed, skipped", "schedule", sc.ID, "name", sc.Name,
					"slot", sc.NextAt, "late", now.Sub(sc.NextAt).Round(time.Second))
			}
			return adv
		}
	}
	slog.Error("schedule can't be evaluated, disabled", "schedule", sc.ID, "name", sc.Name, "err", err)
	return schedule.Advance{}
}

// disable switches off a schedule that can't run as it is (the agent it
// names is gone) instead of failing at every slot, and tells the web.
func (s *Service) disable(ctx context.Context, sc schedule.Schedule, reason string) {
	off := false
	out, err := s.repo.Update(ctx, sc.Organization, sc.ID, schedule.Update{Enabled: &off}, sc.NextAt)
	if err != nil {
		slog.Error("disable schedule", "schedule", sc.ID, "name", sc.Name, "err", err)
		return
	}
	slog.Error("schedule disabled: "+reason, "schedule", sc.ID, "name", sc.Name)
	s.events.Publish(sc.Organization, schedule.EventUpdated, out)
}

// fire hands the schedule's message to the ship domain. slot names it
// ("schedule-<id>-<slot>"), so firing the same slot twice is one ship.
func (s *Service) fire(ctx context.Context, sc schedule.Schedule, at time.Time, slot string) (ship.Ship, bool, error) {
	if s.deliverer == nil {
		return ship.Ship{}, false, errx.Internal("schedules are not wired to the ship service")
	}
	conv, err := s.convs.Find(ctx, sc.Organization, sc.Conversation)
	if err != nil {
		return ship.Ship{}, false, errx.Wrap(err, "find schedule conversation", errx.TypeInternal)
	}
	// An alarm fires only while its agent still has alarms on: the owner's
	// switch outranks what the agent set earlier.
	if sc.CreatedBy != nil {
		p, err := s.agents.Profile(ctx, sc.Organization, *sc.CreatedBy, 0)
		if err != nil {
			if errx.IsType(err, errx.TypeNotFound) {
				s.disable(ctx, sc, "its agent is gone")
			}
			return ship.Ship{}, false, errx.Wrap(err, "find alarm agent profile", errx.TypeInternal)
		}
		if !p.Alarms {
			slog.Warn("alarm not fired, alarms are off for its agent", "schedule", sc.ID, "name", sc.Name, "agent", *sc.CreatedBy)
			if sc.RunAt == nil { // a one-shot is done already; a recurring one stops
				s.disable(ctx, sc, "alarms are off for its agent")
			}
			return ship.Ship{}, false, errAlarmsOff
		}
	}
	body, senderName, handles := sc.Text, "Seaport · "+sc.Name, []string{schedule.SenderID, "schedule:" + sc.Name}
	if sc.Agent != nil {
		a, err := s.agents.Find(ctx, sc.Organization, *sc.Agent)
		if err != nil {
			if errx.IsType(err, errx.TypeNotFound) {
				s.disable(ctx, sc, "its agent is gone")
			}
			return ship.Ship{}, false, errx.Wrap(err, "find schedule agent", errx.TypeInternal)
		}
		if sc.CreatedBy != nil {
			body = "@" + a.Name + " Alarm you set (" + sc.Name + ", set " + sc.CreatedAt.UTC().Format(time.RFC3339) + "): " + sc.Text
			senderName = "Seaport · alarma " + sc.Name
			handles = append(handles, "alarm:"+a.Name)
		} else {
			body = "@" + a.Name + " " + body
		}
	}
	st, created, err := s.deliverer.Receive(ctx, sc.Organization, ship.Inbound{
		Dock: conv.Dock, Channel: conv.Channel, ChatID: conv.ExternalID, ChatKind: conv.Kind, ChatTitle: conv.Title,
		ExternalID: "schedule-" + sc.ID.String() + "-" + slot,
		SenderID:   schedule.SenderID, SenderName: text.Cut(senderName, text.MaxName),
		Text: body, SentAt: at,
		ChatHandles:   []string{conv.ExternalID},
		SenderHandles: handles,
		Addressed:     true,
	})
	if err != nil {
		return ship.Ship{}, false, errx.Wrap(err, "post scheduled message", errx.TypeInternal)
	}
	// Best effort: the message is posted; the record only informs.
	if err := s.repo.Record(ctx, sc.Organization, sc.ID, at, st.ID); err != nil {
		slog.Warn("record schedule run", "schedule", sc.ID, "err", err)
	} else {
		sc.LastAt, sc.LastShip = &at, &st.ID
	}
	if created {
		s.events.Publish(sc.Organization, schedule.EventFired, s.withPreview(sc))
	}
	return st, created, nil
}

// SetAlarm stores an alarm an agent set for itself in a conversation it
// works. The profile's switch, the dock, the time and the limits are
// checked here; the limits again under the organization's lock by the
// repository.
func (s *Service) SetAlarm(ctx context.Context, org identity.OrganizationID, input schedule.SetAlarm) (schedule.Schedule, error) {
	if err := input.Validate(); err != nil {
		return schedule.Schedule{}, err
	}
	conv, err := s.convs.Find(ctx, org, input.Conversation)
	if err != nil {
		return schedule.Schedule{}, errx.Wrap(err, "find conversation", errx.TypeInternal)
	}
	team, err := s.agents.Team(ctx, org, conv.Dock)
	if err != nil {
		return schedule.Schedule{}, errx.Wrap(err, "dock team", errx.TypeInternal)
	}
	if !slices.ContainsFunc(team, func(m agent.Member) bool { return m.Agent == input.Agent }) {
		return schedule.Schedule{}, errx.NotFound("conversation not found")
	}
	a, err := s.agents.Find(ctx, org, input.Agent)
	if err != nil {
		return schedule.Schedule{}, errx.Wrap(err, "find agent", errx.TypeInternal)
	}
	profile, err := s.agents.Profile(ctx, org, input.Agent, 0)
	if err != nil {
		return schedule.Schedule{}, errx.Wrap(err, "find agent profile", errx.TypeInternal)
	}
	if !profile.Alarms {
		return schedule.Schedule{}, errx.Business("alarms are off for this agent: ask the owner to turn them on in its profile")
	}
	now := s.now()
	spec, next, err := input.When(now)
	if err != nil {
		return schedule.Schedule{}, err
	}
	name := text.Clean(input.Name)
	if name == "" {
		var b [4]byte
		_, _ = rand.Read(b[:])
		name = text.Cut(a.Name, 40) + "-" + hex.EncodeToString(b[:])
	}
	row := schedule.Schedule{ID: identity.NewScheduleID(), Organization: org, Conversation: input.Conversation, Agent: &input.Agent,
		CreatedBy: &input.Agent, Name: name, Cron: spec, Timezone: zone(input.Timezone), Text: text.Clean(input.Note), Enabled: true, NextAt: next}
	if spec == "" {
		row.RunAt = &next
	}
	out, err := s.repo.CreateAlarm(ctx, org, row, schedule.DefaultAlarmLimits, s.max)
	if err != nil {
		return schedule.Schedule{}, errx.Wrap(err, "create alarm", errx.TypeInternal)
	}
	out = s.withPreview(out)
	s.events.Publish(org, schedule.EventCreated, out)
	return out, nil
}

// CancelAlarm deletes an alarm the agent set; any other schedule is
// NotFound to it.
func (s *Service) CancelAlarm(ctx context.Context, org identity.OrganizationID, agentID identity.AgentID, id identity.ScheduleID) error {
	if id.IsZero() {
		return errx.InvalidField("schedule_id", "schedule_id is required")
	}
	if agentID.IsZero() {
		return errx.InvalidField("agent_id", "an alarm belongs to an agent")
	}
	cur, err := s.repo.Find(ctx, org, id)
	if err != nil {
		return errx.Wrap(err, "find alarm", errx.TypeInternal)
	}
	if cur.CreatedBy == nil || *cur.CreatedBy != agentID {
		return errx.NotFound("alarm not found")
	}
	return s.Delete(ctx, org, id)
}

// Alarms lists the agent's pending alarms, oldest first.
func (s *Service) Alarms(ctx context.Context, org identity.OrganizationID, agentID identity.AgentID, conversation *identity.ConversationID) ([]schedule.Schedule, error) {
	if agentID.IsZero() {
		return nil, errx.InvalidField("agent_id", "an alarm belongs to an agent")
	}
	// Bounded by schedule.MaxAlarmsPerAgent: one page holds them all.
	page, err := s.repo.List(ctx, org, schedule.Filter{Agent: &agentID, Conversation: conversation}, query.Pagination{Limit: query.MaxLimit})
	if err != nil {
		return nil, errx.Wrap(err, "list alarms", errx.TypeInternal)
	}
	for i := range page.Items {
		page.Items[i] = s.withPreview(page.Items[i])
	}
	return page.Items, nil
}
