package crewmcp

import (
	"context"
	"strconv"
	"strings"
	"time"

	"github.com/Abraxas-365/seaport/internal/errx"
	"github.com/Abraxas-365/seaport/internal/httpx"
	"github.com/Abraxas-365/seaport/internal/identity"
	"github.com/Abraxas-365/seaport/internal/schedule"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// maxAlarmMinutes is schedule.MaxAlarmAhead in minutes (in_minutes' cap).
const maxAlarmMinutes = int(schedule.MaxAlarmAhead / time.Minute)

// AlarmDeps is what the alarm tools need of the schedule domain.
type AlarmDeps interface {
	SetAlarm(ctx context.Context, organization identity.OrganizationID, input schedule.SetAlarm) (schedule.Schedule, error)
	CancelAlarm(ctx context.Context, organization identity.OrganizationID, agent identity.AgentID, id identity.ScheduleID) error
	Alarms(ctx context.Context, organization identity.OrganizationID, agent identity.AgentID, conversation *identity.ConversationID) ([]schedule.Schedule, error)
}

// addAlarms registers set_alarm, alarms and cancel_alarm when the calling
// agent's profile has alarms on: the owner decides per agent whether it may
// wake itself later.
func (t tools) addAlarms(ctx context.Context, srv *mcp.Server) {
	p, ok := httpx.PrincipalFrom(ctx)
	if !ok || p.Agent.IsZero() || t.d.Alarms == nil {
		return
	}
	profile, err := t.d.Agents.Profile(ctx, p.Organization, p.Agent, 0)
	if err != nil || !profile.Alarms {
		return
	}
	mcp.AddTool(srv, &mcp.Tool{
		Name: "set_alarm",
		Description: "Set an alarm to wake yourself later in a conversation you are working, instead of waiting or polling: " +
			"\"check again in 20 minutes whether the deploy finished\", \"tomorrow at 09:00 look at this again\". " +
			"At the time, Seaport sends you a message in that conversation with your note, so write the note for your future self " +
			"(what to check and why). Give exactly one of in_minutes (1 to 43200), at (RFC 3339 with offset) or cron (5 fields, " +
			"at most once every 5 minutes, with timezone) for a recurring one. Limits: " +
			"at most 3 pending alarms per conversation, 20 in all and 50 created per 24 hours; one-shots 1 minute to 30 days ahead. " +
			"Cancel the ones you no longer need (cancel_alarm) and do not set an alarm every time you wake up.",
	}, t.setAlarm)
	mcp.AddTool(srv, &mcp.Tool{
		Name:        "alarms",
		Description: "List your pending alarms (id, name, when, note), in one conversation or in all.",
	}, t.listAlarms)
	mcp.AddTool(srv, &mcp.Tool{
		Name:        "cancel_alarm",
		Description: "Cancel one of your pending alarms by its id (see alarms). Another agent's alarm is not found.",
	}, t.cancelAlarm)
}

type SetAlarmIn struct {
	ConversationID string `json:"conversation_id" jsonschema:"the conversation you are working (the one in the seaport prompt); the alarm wakes you there"`
	Note           string `json:"note" jsonschema:"what future-you must do or check when it rings (at most 4000 characters)"`
	InMinutes      int    `json:"in_minutes,omitempty" jsonschema:"ring this many minutes from now (1 to 43200)"`
	At             string `json:"at,omitempty" jsonschema:"ring at this time, RFC 3339 with offset (2026-11-23T09:00:00-05:00)"`
	Cron           string `json:"cron,omitempty" jsonschema:"ring on a cron expression (5 fields; at most once every 5 minutes) until you cancel it"`
	Timezone       string `json:"timezone,omitempty" jsonschema:"IANA zone of the cron expression (default UTC)"`
	Name           string `json:"name,omitempty" jsonschema:"a short name (unique); one is made up when empty"`
}

type SetAlarmOut struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	NextAt    time.Time `json:"next_at"`
	Recurring bool      `json:"recurring"`
}

func (t tools) setAlarm(ctx context.Context, _ *mcp.CallToolRequest, in SetAlarmIn) (*mcp.CallToolResult, SetAlarmOut, error) {
	org, agentID, err := caller(ctx)
	if err != nil {
		return failure(err), SetAlarmOut{}, nil
	}
	if agentID == nil {
		return failure(errx.Validation("no agent: an alarm belongs to an agent")), SetAlarmOut{}, nil
	}
	conv, err := identity.ParseConversationID(strings.TrimSpace(in.ConversationID))
	if err != nil {
		return failure(err), SetAlarmOut{}, nil
	}
	input := schedule.SetAlarm{Agent: *agentID, Conversation: conv, Name: in.Name, Note: in.Note, Cron: in.Cron, Timezone: in.Timezone}
	if in.InMinutes != 0 {
		if in.InMinutes < 1 || in.InMinutes > maxAlarmMinutes {
			return failure(errx.InvalidField("in_minutes", "in_minutes must be between 1 and "+strconv.Itoa(maxAlarmMinutes))), SetAlarmOut{}, nil
		}
		d := time.Duration(in.InMinutes) * time.Minute
		input.In = &d
	}
	if raw := strings.TrimSpace(in.At); raw != "" {
		at, err := time.Parse(time.RFC3339, raw)
		if err != nil {
			return failure(errx.InvalidField("at", "at must be an RFC 3339 time with offset, such as 2026-11-23T09:00:00-05:00")), SetAlarmOut{}, nil
		}
		input.At = &at
	}
	out, err := t.d.Alarms.SetAlarm(ctx, org, input)
	if err != nil {
		return failure(err), SetAlarmOut{}, nil
	}
	return nil, SetAlarmOut{ID: out.ID.String(), Name: out.Name, NextAt: out.NextAt.UTC(), Recurring: out.RunAt == nil}, nil
}

type AlarmsIn struct {
	ConversationID string `json:"conversation_id,omitempty" jsonschema:"only the alarms of this conversation"`
}

// AlarmInfo is one of the agent's pending alarms.
type AlarmInfo struct {
	ID             string     `json:"id"`
	Name           string     `json:"name"`
	ConversationID string     `json:"conversation_id"`
	NextAt         time.Time  `json:"next_at"`
	Cron           string     `json:"cron,omitempty"`
	Timezone       string     `json:"timezone,omitempty"`
	RunAt          *time.Time `json:"run_at,omitempty"`
	Note           string     `json:"note"`
}

type AlarmsOut struct {
	Alarms []AlarmInfo `json:"alarms"`
}

func (t tools) listAlarms(ctx context.Context, _ *mcp.CallToolRequest, in AlarmsIn) (*mcp.CallToolResult, AlarmsOut, error) {
	org, agentID, err := caller(ctx)
	if err != nil {
		return failure(err), AlarmsOut{}, nil
	}
	if agentID == nil {
		return failure(errx.Validation("no agent: an alarm belongs to an agent")), AlarmsOut{}, nil
	}
	var conv *identity.ConversationID
	if raw := strings.TrimSpace(in.ConversationID); raw != "" {
		id, err := identity.ParseConversationID(raw)
		if err != nil {
			return failure(err), AlarmsOut{}, nil
		}
		conv = &id
	}
	list, err := t.d.Alarms.Alarms(ctx, org, *agentID, conv)
	if err != nil {
		return failure(err), AlarmsOut{}, nil
	}
	out := AlarmsOut{Alarms: make([]AlarmInfo, 0, len(list))}
	for _, a := range list {
		info := AlarmInfo{ID: a.ID.String(), Name: a.Name, ConversationID: a.Conversation.String(), NextAt: a.NextAt.UTC(), Note: a.Text}
		if a.RunAt != nil {
			r := a.RunAt.UTC()
			info.RunAt = &r
		} else {
			info.Cron, info.Timezone = a.Cron, a.Timezone
		}
		out.Alarms = append(out.Alarms, info)
	}
	return nil, out, nil
}

type CancelAlarmIn struct {
	ID string `json:"id" jsonschema:"the alarm's id (set_alarm and alarms give it)"`
}

type CancelAlarmOut struct {
	Cancelled bool `json:"cancelled"`
}

func (t tools) cancelAlarm(ctx context.Context, _ *mcp.CallToolRequest, in CancelAlarmIn) (*mcp.CallToolResult, CancelAlarmOut, error) {
	org, agentID, err := caller(ctx)
	if err != nil {
		return failure(err), CancelAlarmOut{}, nil
	}
	if agentID == nil {
		return failure(errx.Validation("no agent: an alarm belongs to an agent")), CancelAlarmOut{}, nil
	}
	id, err := identity.ParseScheduleID(strings.TrimSpace(in.ID))
	if err != nil {
		return failure(err), CancelAlarmOut{}, nil
	}
	if err := t.d.Alarms.CancelAlarm(ctx, org, *agentID, id); err != nil {
		return failure(err), CancelAlarmOut{}, nil
	}
	return nil, CancelAlarmOut{Cancelled: true}, nil
}
