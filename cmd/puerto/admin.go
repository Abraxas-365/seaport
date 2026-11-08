package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/Abraxas-365/puerto/internal/conversation"
	"github.com/Abraxas-365/puerto/internal/crew"
	"github.com/Abraxas-365/puerto/internal/dock"
	"github.com/Abraxas-365/puerto/internal/errx"
	"github.com/Abraxas-365/puerto/internal/query"
	"github.com/Abraxas-365/puerto/internal/ship"
	"github.com/mdp/qrterminal/v3"
)

// admin is a tiny client of the port's /api/v1.
type admin struct {
	base  string
	token string
	http  *http.Client
}

func newAdmin() (*admin, error) {
	token := os.Getenv("PUERTO_ADMIN_TOKEN")
	if token == "" {
		return nil, errx.Validation("PUERTO_ADMIN_TOKEN is required")
	}
	return &admin{base: strings.TrimRight(envOr("PUERTO_URL", "http://127.0.0.1:8740"), "/"),
		token: token, http: &http.Client{Timeout: 30 * time.Second}}, nil
}

func (a *admin) do(ctx context.Context, method, path string, body, out any) error {
	var rd io.Reader
	if body != nil {
		raw, _ := json.Marshal(body)
		rd = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, a.base+path, rd)
	if err != nil {
		return errx.Wrap(err, "build request", errx.TypeInternal)
	}
	req.Header.Set("Authorization", "Bearer "+a.token)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := a.http.Do(req)
	if err != nil {
		return errx.External("cannot reach the port at " + a.base + ": " + err.Error())
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if resp.StatusCode >= 300 {
		var e struct {
			Error struct {
				Message string `json:"message"`
			} `json:"error"`
		}
		if json.Unmarshal(raw, &e) == nil && e.Error.Message != "" {
			return errx.FromStatus(resp.StatusCode, e.Error.Message)
		}
		return errx.FromStatus(resp.StatusCode, resp.Status)
	}
	if out != nil {
		if err := json.Unmarshal(raw, out); err != nil {
			return errx.Wrap(err, "decode response", errx.TypeInternal)
		}
	}
	return nil
}

func dockAdd(ctx context.Context, args []string) error {
	if len(args) == 0 {
		return errx.Validation("usage: puerto dock add whatsapp|api [-name N]")
	}
	channel := args[0]
	fs := flag.NewFlagSet("dock add", flag.ContinueOnError)
	name := fs.String("name", channel, "dock name")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	a, err := newAdmin()
	if err != nil {
		return err
	}
	var d dock.Dock
	if err := a.do(ctx, http.MethodPost, "/api/v1/docks", map[string]string{"channel": channel, "name": *name}, &d); err != nil {
		return err
	}
	fmt.Printf("dock %s created (%s)\n", d.ID, d.Channel)
	if d.Channel == dock.ChannelWhatsApp {
		return waitLinked(ctx, a, d)
	}
	return nil
}

func dockRelink(ctx context.Context, args []string) error {
	if len(args) != 1 {
		return errx.Validation("usage: puerto dock relink <dock-id>")
	}
	a, err := newAdmin()
	if err != nil {
		return err
	}
	var d dock.Dock
	if err := a.do(ctx, http.MethodPost, "/api/v1/docks/"+url.PathEscape(args[0])+"/relink", nil, &d); err != nil {
		return err
	}
	return waitLinked(ctx, a, d)
}

func dockRename(ctx context.Context, args []string) error {
	if len(args) < 2 {
		return errx.Validation("usage: puerto dock rename <dock-id> <name>")
	}
	a, err := newAdmin()
	if err != nil {
		return err
	}
	var d dock.Dock
	name := strings.Join(args[1:], " ")
	if err := a.do(ctx, http.MethodPatch, "/api/v1/docks/"+url.PathEscape(args[0]), map[string]string{"name": name}, &d); err != nil {
		return err
	}
	fmt.Printf("dock %s is now %q\n", d.ID, d.Name)
	return nil
}

func dockUnlink(ctx context.Context, args []string) error {
	if len(args) != 1 {
		return errx.Validation("usage: puerto dock unlink <dock-id>")
	}
	a, err := newAdmin()
	if err != nil {
		return err
	}
	var d dock.Dock
	if err := a.do(ctx, http.MethodPost, "/api/v1/docks/"+url.PathEscape(args[0])+"/unlink", nil, &d); err != nil {
		return err
	}
	fmt.Printf("dock %s unlinked (%s); pair again with: puerto dock relink %s\n", d.ID, d.Status, d.ID)
	return nil
}

func dockDelete(ctx context.Context, args []string) error {
	if len(args) != 2 || args[1] != "-yes" {
		return errx.Validation("usage: puerto dock delete <dock-id> -yes (deletes its conversations and ships too)")
	}
	a, err := newAdmin()
	if err != nil {
		return err
	}
	if err := a.do(ctx, http.MethodDelete, "/api/v1/docks/"+url.PathEscape(args[0]), nil, nil); err != nil {
		return err
	}
	fmt.Println("dock deleted")
	return nil
}

func crewStatus(ctx context.Context) error {
	a, err := newAdmin()
	if err != nil {
		return err
	}
	var s crew.Status
	if err := a.do(ctx, http.MethodGet, "/api/v1/crew", nil, &s); err != nil {
		return err
	}
	if len(s.Agents) == 0 {
		fmt.Println("no agents connected (start one with: puerto connect -- <agent>)")
		return nil
	}
	for _, ag := range s.Agents {
		steer := ""
		if ag.Steer {
			steer = ", steer"
		}
		fmt.Printf("%s  %s  concurrency %d%s  since %s  from %s\n", ag.Session, ag.Name, ag.Concurrency, steer,
			ag.ConnectedAt.Local().Format("Jan 2 15:04"), ag.Remote)
		for _, d := range ag.Deliveries {
			fmt.Printf("    working on %s (%d ships, %s)\n", d.Conversation, d.Ships, time.Since(d.Since).Round(time.Second))
		}
	}
	return nil
}

// waitLinked shows each new QR until the phone links the dock.
func waitLinked(ctx context.Context, a *admin, d dock.Dock) error {
	fmt.Println("On your phone: WhatsApp → Settings → Linked devices → Link a device, then scan:")
	shown := ""
	deadline := time.Now().Add(3 * time.Minute)
	for time.Now().Before(deadline) {
		switch d.Status {
		case dock.StatusLinked:
			fmt.Printf("\nlinked as %s ✓\nMessages to this number now arrive at the port. Allow chats with: puerto conversations / puerto allow <id>\n", d.Address)
			return nil
		case dock.StatusFailed, dock.StatusLoggedOut:
			return errx.Business("linking failed (" + string(d.Status) + "); retry with: puerto dock relink " + d.ID.String())
		}
		if d.QRCode != "" && d.QRCode != shown {
			shown = d.QRCode
			qrterminal.GenerateWithConfig(d.QRCode, qrterminal.Config{Level: qrterminal.L, Writer: os.Stdout,
				HalfBlocks: true, BlackChar: qrterminal.BLACK_BLACK, WhiteBlackChar: qrterminal.WHITE_BLACK,
				WhiteChar: qrterminal.WHITE_WHITE, BlackWhiteChar: qrterminal.BLACK_WHITE, QuietZone: 1})
			fmt.Println("(the code refreshes every ~20s; waiting...)")
		}
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(2 * time.Second):
		}
		if err := a.do(ctx, http.MethodGet, "/api/v1/docks/"+d.ID.String(), nil, &d); err != nil {
			return err
		}
	}
	return errx.Business("timed out waiting for the QR scan; retry with: puerto dock relink " + d.ID.String())
}

func dockList(ctx context.Context) error {
	a, err := newAdmin()
	if err != nil {
		return err
	}
	var page query.Paginated[dock.Dock]
	if err := a.do(ctx, http.MethodGet, "/api/v1/docks?limit=100", nil, &page); err != nil {
		return err
	}
	w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(w, "ID\tCHANNEL\tNAME\tSTATUS\tADDRESS")
	for _, d := range page.Items {
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n", d.ID, d.Channel, d.Name, d.Status, d.Address)
	}
	return w.Flush()
}

func conversations(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("conversations", flag.ContinueOnError)
	allowed := fs.Bool("allowed", false, "only allowed")
	pending := fs.Bool("pending", false, "only waiting for a decision")
	denied := fs.Bool("denied", false, "only denied")
	if err := fs.Parse(args); err != nil {
		return err
	}
	a, err := newAdmin()
	if err != nil {
		return err
	}
	path := "/api/v1/conversations?limit=100"
	switch {
	case *allowed:
		path += "&status=allowed"
	case *pending:
		path += "&status=pending"
	case *denied:
		path += "&status=denied"
	}
	var page query.Paginated[conversation.Conversation]
	if err := a.do(ctx, http.MethodGet, path, nil, &page); err != nil {
		return err
	}
	w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(w, "ID\tSTATUS\tACCESS\tKIND\tTITLE\tHANDLES")
	for _, c := range page.Items {
		access := string(c.Access)
		if c.Kind == conversation.KindGroup && c.GroupMode != conversation.GroupInherit {
			access += "/" + string(c.GroupMode)
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\n", c.ID, c.Status, access, c.Kind, c.Title, strings.Join(c.Handles, " "))
	}
	return w.Flush()
}

// setAccess sets the owner's override of a conversation (allow|deny|auto).
func setAccess(ctx context.Context, args []string, access conversation.Access) error {
	if len(args) != 1 {
		return errx.Validation("usage: puerto allow|deny|auto <conversation-id>")
	}
	return patchConversation(ctx, args[0], map[string]any{"access": access})
}

// setGroupMode overrides a group's mode (mentions|all|default).
func setGroupMode(ctx context.Context, args []string) error {
	if len(args) != 2 {
		return errx.Validation("usage: puerto mode <conversation-id> mentions|all|default")
	}
	mode := args[1]
	if mode == "default" {
		mode = ""
	}
	return patchConversation(ctx, args[0], map[string]any{"group_mode": mode})
}

func patchConversation(ctx context.Context, id string, body map[string]any) error {
	a, err := newAdmin()
	if err != nil {
		return err
	}
	var c conversation.Conversation
	if err := a.do(ctx, http.MethodPatch, "/api/v1/conversations/"+url.PathEscape(id), body, &c); err != nil {
		return err
	}
	fmt.Printf("%s %q status=%s access=%s", c.ID, c.Title, c.Status, c.Access)
	if c.Kind == conversation.KindGroup {
		mode := string(c.GroupMode)
		if mode == "" {
			mode = "dock default"
		}
		fmt.Printf(" groups=%s", mode)
	}
	fmt.Println()
	return nil
}

const customsUsage = `usage: puerto customs [-dock ID] [command]
  (no command)               show the dock's policy and rules
  default ask|allow|deny     what to do with chats no rule covers (ask = hold until you allow)
  groups mentions|all        which messages of allowed groups wake the agent
  allow <pattern>            allow chats/senders matching the pattern
  deny <pattern>             deny them (deny wins over allow; denied group members are ignored)
  rm <rule-id>               remove a rule
patterns: phone numbers (51999123436, +51*), WhatsApp ids (…@lid, …@g.us), * matches anything`

func customs(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("customs", flag.ContinueOnError)
	dockID := fs.String("dock", "", "dock id (default: the only dock)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	a, err := newAdmin()
	if err != nil {
		return err
	}
	rest := fs.Args()
	if len(rest) > 0 && rest[0] == "rm" {
		if len(rest) != 2 {
			return errx.Validation(customsUsage)
		}
		if err := a.do(ctx, http.MethodDelete, "/api/v1/customs/rules/"+url.PathEscape(rest[1]), nil, nil); err != nil {
			return err
		}
		fmt.Println("rule removed")
		return nil
	}
	d, err := pickDock(ctx, a, *dockID)
	if err != nil {
		return err
	}
	base := "/api/v1/docks/" + url.PathEscape(d.ID.String()) + "/customs"
	switch {
	case len(rest) == 0:
	case len(rest) == 2 && rest[0] == "default":
		if rest[1] == string(conversation.DefaultAllow) {
			fmt.Fprintln(os.Stderr, "warning: every chat that writes to this number will wake the agent and get answers from it")
		}
		if err := a.do(ctx, http.MethodPatch, base+"/policy", map[string]string{"default": rest[1]}, nil); err != nil {
			return err
		}
	case len(rest) == 2 && rest[0] == "groups":
		if err := a.do(ctx, http.MethodPatch, base+"/policy", map[string]string{"group_mode": rest[1]}, nil); err != nil {
			return err
		}
	case len(rest) == 2 && (rest[0] == "allow" || rest[0] == "deny"):
		if err := a.do(ctx, http.MethodPost, base+"/rules", map[string]string{"effect": rest[0], "pattern": rest[1]}, nil); err != nil {
			return err
		}
	default:
		return errx.Validation(customsUsage)
	}
	var c conversation.Customs
	if err := a.do(ctx, http.MethodGet, base, nil, &c); err != nil {
		return err
	}
	fmt.Printf("dock %s (%s %s)\ndefault: %s   groups: %s\n", d.ID, d.Channel, d.Name, c.Policy.Default, c.Policy.GroupMode)
	if len(c.Rules) == 0 {
		fmt.Println("no rules")
		return nil
	}
	w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(w, "RULE\tEFFECT\tPATTERN")
	for _, r := range c.Rules {
		fmt.Fprintf(w, "%s\t%s\t%s\n", r.ID, r.Effect, r.Pattern)
	}
	return w.Flush()
}

func pickDock(ctx context.Context, a *admin, id string) (dock.Dock, error) {
	var d dock.Dock
	if id != "" {
		return d, a.do(ctx, http.MethodGet, "/api/v1/docks/"+url.PathEscape(id), nil, &d)
	}
	var page query.Paginated[dock.Dock]
	if err := a.do(ctx, http.MethodGet, "/api/v1/docks?limit=100", nil, &page); err != nil {
		return d, err
	}
	switch len(page.Items) {
	case 0:
		return d, errx.Validation("no docks yet: puerto dock add whatsapp")
	case 1:
		return page.Items[0], nil
	}
	return d, errx.Validation("several docks: pick one with -dock ID (puerto dock list)")
}

func history(ctx context.Context, args []string) error {
	if len(args) == 0 {
		return errx.Validation("usage: puerto history <conversation-id> [-limit N]")
	}
	fs := flag.NewFlagSet("history", flag.ContinueOnError)
	limit := fs.Int("limit", 20, "messages")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	a, err := newAdmin()
	if err != nil {
		return err
	}
	var page query.Paginated[ship.Ship]
	if err := a.do(ctx, http.MethodGet, fmt.Sprintf("/api/v1/conversations/%s/ships?limit=%d", url.PathEscape(args[0]), *limit), nil, &page); err != nil {
		return err
	}
	for i := len(page.Items) - 1; i >= 0; i-- {
		s := page.Items[i]
		from := s.SenderName
		if s.Direction == ship.DirectionOut {
			from = "agent"
		} else if from == "" {
			from = s.SenderID
		}
		fmt.Printf("%s  %-8s %s: %s\n", s.SentAt.Local().Format("01-02 15:04"), s.Status, from, s.Text)
	}
	return nil
}

func send(ctx context.Context, args []string) error {
	if len(args) < 2 {
		return errx.Validation("usage: puerto send <conversation-id> <text>")
	}
	a, err := newAdmin()
	if err != nil {
		return err
	}
	var s ship.Ship
	if err := a.do(ctx, http.MethodPost, "/api/v1/conversations/"+url.PathEscape(args[0])+"/ships",
		map[string]string{"text": strings.Join(args[1:], " ")}, &s); err != nil {
		return err
	}
	fmt.Printf("ship %s %s\n", s.ID, s.Status)
	return nil
}
