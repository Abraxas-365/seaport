package main

import (
	"context"
	"flag"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/Abraxas-365/seaport/internal/errx"
	"github.com/Abraxas-365/seaport/internal/query"
	"github.com/Abraxas-365/seaport/internal/schedule"
	"github.com/Abraxas-365/seaport/internal/ship"
)

// schedules is `seaport schedules [list|add|edit|enable|disable|run|delete|preview]`.
func schedules(ctx context.Context, args []string) error {
	sub := "list"
	if len(args) > 0 {
		sub, args = args[0], args[1:]
	}
	a, err := newAdmin()
	if err != nil {
		return err
	}
	switch sub {
	case "list":
		path := "/api/v1/schedules?limit=100"
		fs := flag.NewFlagSet("schedules list", flag.ContinueOnError)
		all := fs.Bool("done", false, "also the one-shots that already ran")
		rest := args
		if len(rest) > 0 && !strings.HasPrefix(rest[0], "-") { // [conversation-id] [-done]
			path += "&conversation_id=" + url.QueryEscape(rest[0])
			rest = rest[1:]
		}
		if err := fs.Parse(rest); err != nil {
			return err
		}
		if *all {
			path += "&done=true"
		}
		var page query.Paginated[schedule.Schedule]
		if err := a.do(ctx, http.MethodGet, path, nil, &page); err != nil {
			return err
		}
		if len(page.Items) == 0 {
			fmt.Fprintln(stdout, `no schedules yet: seaport schedules add <conversation-id> <name> -cron "0 9 * * 1-5" -text "..."`)
			return nil
		}
		w := tabwriter.NewWriter(stdout, 0, 4, 2, ' ', 0)
		fmt.Fprintln(w, "ID\tNAME\tWHEN\tZONE\tON\tNEXT\tLAST\tCONVERSATION")
		for _, s := range page.Items {
			last := "-"
			if s.LastAt != nil {
				last = s.LastAt.Local().Format("2006-01-02 15:04")
			}
			next := "-"
			if s.Enabled {
				next = s.NextAt.Local().Format("2006-01-02 15:04")
			}
			when := s.Cron
			if s.RunAt != nil { // a one-shot (an alarm when an agent set it)
				when = "once"
				if s.CreatedBy != nil {
					when = "alarm"
				}
				if s.DoneAt != nil {
					when += " (done)"
				}
			}
			fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%v\t%s\t%s\t%s\n", s.ID, s.Name, when, s.Timezone, s.Enabled, next, last, s.Conversation)
		}
		return w.Flush()
	case "add":
		if len(args) < 2 {
			return errx.Validation(`usage: seaport schedules add <conversation-id> <name> (-cron "0 9 * * 1-5" | -at 2026-11-23T09:00:00-05:00) -text "..." [-timezone America/Lima] [-agent <agent>] [-disabled]`)
		}
		fs := flag.NewFlagSet("schedules add", flag.ContinueOnError)
		cron := fs.String("cron", "", "five-field cron expression (or @hourly, @daily, @weekly, @monthly)")
		at := fs.String("at", "", "a one-shot at this time (RFC 3339 with offset), instead of -cron")
		zone := fs.String("timezone", "UTC", "IANA time zone the expression is read in")
		body := fs.String("text", "", "the message to post")
		agentRef := fs.String("agent", "", "agent to call (default: the dock's routing)")
		disabled := fs.Bool("disabled", false, "create it switched off")
		if err := fs.Parse(args[2:]); err != nil {
			return err
		}
		req := map[string]any{"conversation_id": args[0], "name": args[1], "timezone": *zone, "text": *body, "enabled": !*disabled}
		if *at != "" {
			req["at"] = *at
		}
		if *at == "" || *cron != "" { // both are sent when both are given: the port refuses that
			req["cron"] = *cron
		}
		if *agentRef != "" {
			ag, err := findAgent(ctx, a, *agentRef)
			if err != nil {
				return err
			}
			req["agent_id"] = ag.ID.String()
		}
		var s schedule.Schedule
		if err := a.do(ctx, http.MethodPost, "/api/v1/schedules", req, &s); err != nil {
			return err
		}
		fmt.Fprintf(stdout, "schedule %s created (%s)\n", s.Name, s.ID)
		printUpcoming(s)
		return nil
	case "edit":
		if len(args) == 0 {
			return errx.Validation("usage: seaport schedules edit <schedule-id> [-name N] [-cron C | -at T] [-timezone Z] [-text T] [-agent A|none]")
		}
		fs := flag.NewFlagSet("schedules edit", flag.ContinueOnError)
		name := fs.String("name", "", "new name")
		cron := fs.String("cron", "", "new cron expression")
		at := fs.String("at", "", "new time of a one-shot (RFC 3339 with offset; arms it again when it ran)")
		zone := fs.String("timezone", "", "new time zone")
		body := fs.String("text", "", "new message")
		agentRef := fs.String("agent", "", `agent to call ("none" clears it)`)
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		req := map[string]any{}
		fs.Visit(func(f *flag.Flag) {
			switch f.Name {
			case "name":
				req["name"] = *name
			case "cron":
				req["cron"] = *cron
			case "at":
				req["at"] = *at
			case "timezone":
				req["timezone"] = *zone
			case "text":
				req["text"] = *body
			}
		})
		if *agentRef != "" {
			if strings.EqualFold(*agentRef, "none") {
				req["agent_id"] = ""
			} else {
				ag, err := findAgent(ctx, a, *agentRef)
				if err != nil {
					return err
				}
				req["agent_id"] = ag.ID.String()
			}
		}
		return patchSchedule(ctx, a, args[0], req)
	case "enable", "disable":
		if len(args) != 1 {
			return errx.Validation("usage: seaport schedules " + sub + " <schedule-id>")
		}
		return patchSchedule(ctx, a, args[0], map[string]any{"enabled": sub == "enable"})
	case "run":
		if len(args) != 1 {
			return errx.Validation("usage: seaport schedules run <schedule-id>")
		}
		var s ship.Ship
		if err := a.do(ctx, http.MethodPost, "/api/v1/schedules/"+url.PathEscape(args[0])+"/run", nil, &s); err != nil {
			return err
		}
		fmt.Fprintf(stdout, "posted now (ship %s, %s)\n", s.ID, s.Status)
		return nil
	case "delete":
		if len(args) == 0 {
			return errx.Validation("usage: seaport schedules delete <schedule-id> -yes")
		}
		fs := flag.NewFlagSet("schedules delete", flag.ContinueOnError)
		yes := fs.Bool("yes", false, "confirm")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		if !*yes {
			return errx.Validation("deleting a schedule can't be undone: add -yes")
		}
		if err := a.do(ctx, http.MethodDelete, "/api/v1/schedules/"+url.PathEscape(args[0]), nil, nil); err != nil {
			return err
		}
		fmt.Fprintln(stdout, "schedule deleted")
		return nil
	case "preview":
		if len(args) == 0 {
			return errx.Validation(`usage: seaport schedules preview "<cron>" [-timezone Z]`)
		}
		fs := flag.NewFlagSet("schedules preview", flag.ContinueOnError)
		zone := fs.String("timezone", "UTC", "IANA time zone")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		var out struct {
			Preview []time.Time `json:"preview"`
		}
		if err := a.do(ctx, http.MethodPost, "/api/v1/schedules/preview", map[string]string{"cron": args[0], "timezone": *zone}, &out); err != nil {
			return err
		}
		printUpcoming(schedule.Schedule{Enabled: true, Timezone: *zone, Preview: out.Preview})
		return nil
	}
	return errx.Validation("unknown schedules command " + sub + " (seaport help)")
}

func patchSchedule(ctx context.Context, a *admin, id string, req map[string]any) error {
	var s schedule.Schedule
	if err := a.do(ctx, http.MethodPatch, "/api/v1/schedules/"+url.PathEscape(id), req, &s); err != nil {
		return err
	}
	fmt.Fprintf(stdout, "schedule %s saved\n", s.Name)
	printUpcoming(s)
	return nil
}

func printUpcoming(s schedule.Schedule) {
	if !s.Enabled {
		fmt.Fprintln(stdout, "switched off: it won't run")
		return
	}
	loc, err := schedule.Location(s.Timezone)
	if err != nil {
		loc = time.UTC
	}
	for i, t := range s.Preview {
		label := "next:  "
		if i > 0 {
			label = "then:  "
		}
		fmt.Fprintln(stdout, label+t.In(loc).Format("Mon 2006-01-02 15:04 MST"))
	}
}
