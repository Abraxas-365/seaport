package main

import (
	"context"
	"flag"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"text/tabwriter"

	"github.com/Abraxas-365/seaport/internal/agent"
	"github.com/Abraxas-365/seaport/internal/errx"
	"github.com/Abraxas-365/seaport/internal/identity"
	"github.com/Abraxas-365/seaport/internal/query"
)

// agents is `seaport agents [list|add|edit|delete|assign|unassign|config|profile|home|secrets|build|clone|host...]`.
func agents(ctx context.Context, args []string) error {
	sub := "list"
	if len(args) > 0 {
		sub, args = args[0], args[1:]
	}
	a, err := newAdmin()
	if err != nil {
		return err
	}
	switch sub {
	case "profile":
		return profileCmd(ctx, a, args)
	case "config":
		return configCmd(ctx, a, args)
	case "home":
		return agentHome(ctx, a, args)
	case "secrets":
		return secretsCmd(ctx, a, args)
	case "build":
		fs := flag.NewFlagSet("agents build", flag.ContinueOnError)
		force := fs.Bool("force", false, "build again even when built (pulls the base anew)")
		wait := fs.Bool("wait", true, "follow the build until it ends")
		if len(args) == 0 {
			return errx.Validation("usage: seaport agents build <agent> [-force] [-wait=false]")
		}
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		ag, err := findAgent(ctx, a, args[0])
		if err != nil {
			return err
		}
		return buildAgent(ctx, a, ag, *force, *wait)
	case "list":
		all, err := listAgents(ctx, a)
		if err != nil {
			return err
		}
		if len(all) == 0 {
			fmt.Fprintln(stdout, "no agents yet: seaport agents add <name>")
			return nil
		}
		w := tabwriter.NewWriter(stdout, 0, 4, 2, ' ', 0)
		fmt.Fprintln(w, "ID\tNAME\tPROFILE\tDOCKS\tDESCRIPTION")
		for _, ag := range all {
			docks := []string{}
			for _, d := range ag.Docks {
				s := d.Dock.String()
				if d.Default {
					s += " (default)"
				}
				docks = append(docks, s)
			}
			if len(docks) == 0 {
				docks = append(docks, "- (inactive)")
			}
			profile := "-"
			if ag.ProfileVersion > 0 {
				profile = fmt.Sprintf("v%d", ag.ProfileVersion)
			}
			fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n", ag.ID, ag.Name, profile, strings.Join(docks, ", "), ag.Description)
		}
		return w.Flush()
	case "host", "unhost", "slots", "start", "stop", "logs", "usage", "egress", "login", "account", "autoscale", "alerts":
		return hostCmd(ctx, a, sub, args)
	case "add":
		if len(args) == 0 {
			return errx.Validation("usage: seaport agents add <name> [-description D]")
		}
		fs := flag.NewFlagSet("agents add", flag.ContinueOnError)
		desc := fs.String("description", "", "what it does (shown to people choosing whom to call)")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		var ag agent.Agent
		if err := a.do(ctx, http.MethodPost, "/api/v1/agents", map[string]string{"name": args[0], "description": *desc}, &ag); err != nil {
			return err
		}
		fmt.Fprintf(stdout, "agent %s created (%s); put it on a dock: seaport agents assign %s <dock-id>\n", ag.Name, ag.ID, ag.Name)
		return nil
	case "clone":
		if len(args) < 2 {
			return errx.Validation("usage: seaport agents clone <agent> <name> [-secrets] [-description D]")
		}
		fs := flag.NewFlagSet("agents clone", flag.ContinueOnError)
		secrets := fs.Bool("secrets", false, "also copy the agent's secrets (resealed for the clone; values are never shown)")
		fs.String("description", "", "description of the clone (default: the source's)")
		if err := fs.Parse(args[2:]); err != nil {
			return err
		}
		body := map[string]any{"name": args[1], "secrets": *secrets}
		fs.Visit(func(f *flag.Flag) {
			if f.Name == "description" {
				body["description"] = f.Value.String()
			}
		})
		src, err := findAgent(ctx, a, args[0])
		if err != nil {
			return err
		}
		var ag agent.Agent
		if err := a.do(ctx, http.MethodPost, "/api/v1/agents/"+src.ID.String()+"/clone", body, &ag); err != nil {
			return err
		}
		fmt.Fprintf(stdout, "agent %s cloned from %s (%s)\n", ag.Name, src.Name, ag.ID)
		if !*secrets {
			fmt.Fprintf(stdout, "its secrets were not copied: set what its config names with seaport agents secrets set %s NAME, or clone again with -secrets\n", ag.Name)
		}
		return nil
	case "edit":
		if len(args) == 0 {
			return errx.Validation("usage: seaport agents edit <agent> [-name N] [-description D]")
		}
		fs := flag.NewFlagSet("agents edit", flag.ContinueOnError)
		fs.String("name", "", "new name (also its @mention)")
		fs.String("description", "", "new description")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		body := map[string]string{} // only the flags given
		fs.Visit(func(f *flag.Flag) { body[f.Name] = f.Value.String() })
		if len(body) == 0 {
			return errx.Validation("nothing to change: -name or -description")
		}
		ag, err := findAgent(ctx, a, args[0])
		if err != nil {
			return err
		}
		if err := a.do(ctx, http.MethodPatch, "/api/v1/agents/"+ag.ID.String(), body, &ag); err != nil {
			return err
		}
		fmt.Fprintf(stdout, "agent %s updated\n", ag.Name)
		return nil
	case "delete":
		fs := flag.NewFlagSet("agents delete", flag.ContinueOnError)
		yes := fs.Bool("yes", false, "confirm")
		if len(args) == 0 {
			return errx.Validation("usage: seaport agents delete <agent> -yes")
		}
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		if !*yes {
			return errx.Validation("deleting an agent revokes its crew tokens, disconnects it and holds what waits for it; add -yes")
		}
		ag, err := findAgent(ctx, a, args[0])
		if err != nil {
			return err
		}
		if err := a.do(ctx, http.MethodDelete, "/api/v1/agents/"+ag.ID.String(), nil, nil); err != nil {
			return err
		}
		fmt.Fprintf(stdout, "agent %s deleted\n", ag.Name)
		return nil
	case "assign":
		if len(args) < 2 {
			return errx.Validation("usage: seaport agents assign <agent> <dock-id> [-default]")
		}
		fs := flag.NewFlagSet("agents assign", flag.ContinueOnError)
		def := fs.Bool("default", false, "it takes the dock's messages that call nobody")
		if err := fs.Parse(args[2:]); err != nil {
			return err
		}
		ag, err := findAgent(ctx, a, args[0])
		if err != nil {
			return err
		}
		if err := a.do(ctx, http.MethodPut, "/api/v1/docks/"+url.PathEscape(args[1])+"/agents/"+ag.ID.String(),
			map[string]bool{"default": *def}, nil); err != nil {
			return err
		}
		fmt.Fprintf(stdout, "agent %s works dock %s\n", ag.Name, args[1])
		return nil
	case "unassign":
		if len(args) != 2 {
			return errx.Validation("usage: seaport agents unassign <agent> <dock-id>")
		}
		ag, err := findAgent(ctx, a, args[0])
		if err != nil {
			return err
		}
		if err := a.do(ctx, http.MethodDelete, "/api/v1/docks/"+url.PathEscape(args[1])+"/agents/"+ag.ID.String(), nil, nil); err != nil {
			return err
		}
		fmt.Fprintf(stdout, "agent %s no longer works dock %s (what waited for it there is held)\n", ag.Name, args[1])
		return nil
	}
	return errx.Validation("unknown command agents " + sub)
}

func listAgents(ctx context.Context, a *admin) ([]agent.Agent, error) {
	var page query.Paginated[agent.Agent]
	err := a.do(ctx, http.MethodGet, fmt.Sprintf("/api/v1/agents?limit=%d", agent.MaxAgents), nil, &page)
	return page.Items, err
}

// findAgent takes an agent's id or name.
func findAgent(ctx context.Context, a *admin, ref string) (agent.Agent, error) {
	var ag agent.Agent
	if id, err := identity.ParseAgentID(ref); err == nil {
		return ag, a.do(ctx, http.MethodGet, "/api/v1/agents/"+id.String(), nil, &ag)
	}
	all, err := listAgents(ctx, a)
	if err != nil {
		return ag, err
	}
	for _, ag := range all {
		if ag.Name == strings.ToLower(strings.TrimPrefix(ref, "@")) {
			return ag, nil
		}
	}
	return ag, errx.NotFound("no agent " + ref + " (seaport agents)")
}
