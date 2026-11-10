package main

import (
	"context"
	"flag"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"strings"
	"text/tabwriter"

	"github.com/Abraxas-365/seaport/internal/agent"
	"github.com/Abraxas-365/seaport/internal/errx"
	"github.com/Abraxas-365/seaport/internal/hosting"
	"github.com/Abraxas-365/seaport/internal/identity"
	"github.com/Abraxas-365/seaport/internal/query"
)

// hostCmd is `seaport agents host|unhost|slots|start|stop|usage|egress ...`.
func hostCmd(ctx context.Context, a *admin, sub string, args []string) error {
	switch sub {
	case "usage":
		if len(args) < 1 {
			return errx.Validation("usage: seaport agents usage <agent> [-days N]")
		}
		fs := flag.NewFlagSet("agents usage", flag.ContinueOnError)
		days := fs.Int("days", 30, "days back, today included")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		ag, err := findAgent(ctx, a, args[0])
		if err != nil {
			return err
		}
		var out struct{ Days []hosting.UsageDay }
		if err := a.do(ctx, http.MethodGet, fmt.Sprintf("/api/v1/agents/%s/usage?days=%d", ag.ID, *days), nil, &out); err != nil {
			return err
		}
		if len(out.Days) == 0 {
			fmt.Printf("%s used no models through the gateway in %d day(s)\n", ag.Name, *days)
			return nil
		}
		w := tabwriter.NewWriter(os.Stdout, 0, 2, 2, ' ', 0)
		fmt.Fprintln(w, "DAY\tMODEL\tREQUESTS\tINPUT\tOUTPUT\tCACHE WRITE\tCACHE READ")
		for _, d := range out.Days {
			fmt.Fprintf(w, "%s\t%s\t%d\t%d\t%d\t%d\t%d\n", d.Day.Format("2006-01-02"), d.Model, d.Requests, d.Input, d.Output, d.CacheWrite, d.CacheRead)
		}
		return w.Flush()
	case "egress":
		if len(args) != 1 {
			return errx.Validation("usage: seaport agents egress <agent>")
		}
		ag, err := findAgent(ctx, a, args[0])
		if err != nil {
			return err
		}
		var out struct{ Denials []hosting.Denial }
		if err := a.do(ctx, http.MethodGet, "/api/v1/agents/"+ag.ID.String()+"/hosting/egress", nil, &out); err != nil {
			return err
		}
		if len(out.Denials) == 0 {
			fmt.Printf("the egress proxy refused %s nothing\n", ag.Name)
			return nil
		}
		w := tabwriter.NewWriter(os.Stdout, 0, 2, 2, ' ', 0)
		fmt.Fprintln(w, "SLOT\tHOST\tTIMES\tLAST")
		for _, d := range out.Denials {
			fmt.Fprintf(w, "%s\t%s\t%d\t%s\n", hosting.SlotName(ag.Name, d.Slot), d.Host, d.Count, d.LastSeen.Local().Format("2006-01-02 15:04"))
		}
		_ = w.Flush()
		fmt.Println("allow one: add it to the environment's network.egress (seaport env export|apply)")
		return nil
	case "start", "stop":
		if len(args) != 1 {
			return errx.Validation("usage: seaport agents " + sub + " <agent>[-<slot>]   (slot 1 by default)")
		}
		ag, idx, err := findSlot(ctx, a, args[0])
		if err != nil {
			return err
		}
		var h hosting.Hosted
		if err := a.do(ctx, http.MethodPost, fmt.Sprintf("/api/v1/agents/%s/hosting/slots/%d/%s", ag.ID, idx, sub), nil, &h); err != nil {
			return err
		}
		for _, s := range h.Slots {
			if s.Index == idx {
				fmt.Printf("%s: %s %s\n", hosting.SlotName(ag.Name, idx), s.State, s.Detail)
			}
		}
		fmt.Println("follow it with: seaport agents slots " + ag.Name)
		return nil
	case "host":
		if len(args) < 2 {
			return errx.Validation("usage: seaport agents host <agent> <env> [-slots N]")
		}
		fs := flag.NewFlagSet("agents host", flag.ContinueOnError)
		slots := fs.Int("slots", 0, "instances (default 1; each keeps its own sessions and workspace)")
		if err := fs.Parse(args[2:]); err != nil {
			return err
		}
		ag, err := findAgent(ctx, a, args[0])
		if err != nil {
			return err
		}
		var h hosting.Hosted
		if err := a.do(ctx, http.MethodPut, "/api/v1/agents/"+ag.ID.String()+"/hosting",
			map[string]any{"environment": args[1], "slots": *slots}, &h); err != nil {
			return err
		}
		fmt.Printf("agent %s hosted on %s with %d slot(s)\n", ag.Name, args[1], len(h.Slots))
		return nil
	case "unhost":
		if len(args) != 1 {
			return errx.Validation("usage: seaport agents unhost <agent>")
		}
		ag, err := findAgent(ctx, a, args[0])
		if err != nil {
			return err
		}
		if err := a.do(ctx, http.MethodDelete, "/api/v1/agents/"+ag.ID.String()+"/hosting", nil, nil); err != nil {
			return err
		}
		fmt.Printf("agent %s is no longer hosted (connect it yourself: seaport connect)\n", ag.Name)
		return nil
	}
	// slots [<agent>]
	var hosted []hosting.Hosted
	if len(args) > 0 {
		ag, err := findAgent(ctx, a, args[0])
		if err != nil {
			return err
		}
		var h hosting.Hosted
		if err := a.do(ctx, http.MethodGet, "/api/v1/agents/"+ag.ID.String()+"/hosting", nil, &h); err != nil {
			return err
		}
		hosted = append(hosted, h)
	} else {
		var out struct {
			Agents []hosting.Hosted `json:"agents"`
		}
		if err := a.do(ctx, http.MethodGet, "/api/v1/hosting", nil, &out); err != nil {
			return err
		}
		hosted = out.Agents
	}
	if len(hosted) == 0 {
		fmt.Println("no hosted agents: seaport agents host <agent> <env>")
		return nil
	}
	agents, err := listAgents(ctx, a)
	if err != nil {
		return err
	}
	names := map[identity.AgentID]string{}
	for _, ag := range agents {
		names[ag.ID] = ag.Name
	}
	var envs query.Paginated[hosting.Environment]
	if err := a.do(ctx, http.MethodGet, "/api/v1/environments?limit=100", nil, &envs); err != nil {
		return err
	}
	envNames := map[identity.EnvironmentID]string{}
	for _, e := range envs.Items {
		envNames[e.ID] = e.Name
	}
	w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(w, "SLOT\tENV\tSTATE\tDETAIL")
	for _, h := range hosted {
		for _, s := range h.Slots {
			version := ""
			if s.EnvironmentVersion > 0 {
				version = fmt.Sprintf(" v%d", s.EnvironmentVersion)
			}
			fmt.Fprintf(w, "%s\t%s%s\t%s\t%s\n", hosting.SlotName(names[h.Agent], s.Index), envNames[h.Environment], version, s.State, s.Detail)
		}
	}
	return w.Flush()
}

// findSlot resolves "<agent>" (slot 1) or "<agent>-<n>": an agent named
// like a slot wins (names may end in -<digits>).
func findSlot(ctx context.Context, a *admin, name string) (agent.Agent, int, error) {
	ag, err := findAgent(ctx, a, name)
	if err == nil {
		return ag, 1, nil
	}
	i := strings.LastIndex(name, "-")
	if i <= 0 {
		return agent.Agent{}, 0, err
	}
	n, perr := strconv.Atoi(name[i+1:])
	if perr != nil {
		return agent.Agent{}, 0, err
	}
	if ag, aerr := findAgent(ctx, a, name[:i]); aerr == nil {
		return ag, n, nil
	}
	return agent.Agent{}, 0, err
}
