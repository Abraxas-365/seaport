package main

import (
	"context"
	"flag"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"text/tabwriter"
	"time"

	"github.com/Abraxas-365/seaport/internal/crew"
	"github.com/Abraxas-365/seaport/internal/errx"
	"github.com/Abraxas-365/seaport/internal/query"
)

// crewTokens is `seaport crew tokens [list|create|revoke]`.
func crewTokens(ctx context.Context, args []string) error {
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
		var page query.Paginated[crew.Token]
		if err := a.do(ctx, http.MethodGet, "/api/v1/crew/tokens?limit=100", nil, &page); err != nil {
			return err
		}
		w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
		fmt.Fprintln(w, "ID\tAGENT\tNAME\tTOKEN\tCREATED\tEXPIRES\tLAST USED")
		for _, t := range page.Items {
			fmt.Fprintf(w, "%s\t%s\t%s\t%s…\t%s\t%s\t%s\n", t.ID, t.AgentName, t.Name, t.Hint, day(&t.CreatedAt), day(t.ExpiresAt), day(t.LastUsedAt))
		}
		return w.Flush()
	case "create":
		if len(args) == 0 {
			return errx.Validation("usage: seaport crew tokens create <name> -agent <agent> [-expires 720h]")
		}
		name := args[0]
		fs := flag.NewFlagSet("crew tokens create", flag.ContinueOnError)
		as := fs.String("agent", "", "the agent it connects as (name or id; required)")
		expires := fs.Duration("expires", 0, "lifetime (e.g. 720h); default never")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		if *as == "" {
			return errx.Validation("every crew token belongs to an agent: -agent <name> (seaport agents)")
		}
		ag, err := findAgent(ctx, a, *as)
		if err != nil {
			return err
		}
		body := map[string]any{"name": name, "agent_id": ag.ID}
		if *expires > 0 {
			body["expires_at"] = time.Now().Add(*expires).UTC()
		}
		var t crew.Issued
		if err := a.do(ctx, http.MethodPost, "/api/v1/crew/tokens", body, &t); err != nil {
			return err
		}
		fmt.Fprintf(os.Stderr, "crew token %s for agent %s created; it is shown only once:\n", t.ID, ag.Name)
		fmt.Println(t.Value)
		return nil
	case "revoke":
		if len(args) != 1 {
			return errx.Validation("usage: seaport crew tokens revoke <token-id>")
		}
		if err := a.do(ctx, http.MethodDelete, "/api/v1/crew/tokens/"+url.PathEscape(args[0]), nil, nil); err != nil {
			return err
		}
		fmt.Println("crew token revoked")
		return nil
	}
	return errx.Validation("unknown command crew tokens " + sub)
}

func day(t *time.Time) string {
	if t == nil {
		return "-"
	}
	return t.Local().Format("2006-01-02")
}
