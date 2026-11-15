package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"text/tabwriter"

	"github.com/Abraxas-365/seaport/internal/errx"
	"github.com/Abraxas-365/seaport/internal/identity"
	"github.com/Abraxas-365/seaport/internal/query"
	"github.com/Abraxas-365/seaport/internal/vault"
)

const vaultUsage = `usage: seaport vault list | create <name> | show <vault> | update <vault> | delete <vault> -yes
       seaport vault grant <vault> (-agent <agent>|-dock <dock-id>) -mode read|write | revoke <vault> (-agent <agent>|-dock <dock-id>)
       seaport vault entries <vault> [-q words] | get <vault> <name> | put <vault> <name> (-file F|"body") | rm <vault> <name>
       seaport vault versions <vault> <name> | revert <vault> <name> <version>
       seaport vault pull <vault> <dir> | push <vault> <dir> [-prune]`

// vaultCmd is `seaport vault ...`: the organization's shared memory.
func vaultCmd(ctx context.Context, args []string) error {
	sub := "list"
	if len(args) > 0 {
		sub, args = args[0], args[1:]
	}
	a, err := newAdmin()
	if err != nil {
		return err
	}
	if sub == "list" || sub == "ls" {
		return vaultList(ctx, a)
	}
	if sub == "create" || sub == "add" {
		return vaultCreate(ctx, a, args)
	}
	if len(args) == 0 {
		return errx.Validation(vaultUsage)
	}
	// every other subcommand starts with the vault (name or id)
	ref, args := args[0], args[1:]
	v, err := findVault(ctx, a, ref)
	if err != nil {
		return err
	}
	switch sub {
	case "show":
		return vaultShow(ctx, a, v)
	case "update", "edit":
		return vaultUpdate(ctx, a, v, args)
	case "delete":
		fs := flag.NewFlagSet("vault delete", flag.ContinueOnError)
		yes := fs.Bool("yes", false, "confirm")
		if err := fs.Parse(args); err != nil {
			return err
		}
		if !*yes {
			return errx.Validation("deleting a vault deletes its " + strconv.Itoa(v.Entries) + " entries and their versions for good, and the agents and docks lose it; add -yes")
		}
		if err := a.do(ctx, http.MethodDelete, vaultPath(v), nil, nil); err != nil {
			return err
		}
		fmt.Printf("vault %s deleted\n", v.Name)
		return nil
	case "grant", "revoke":
		return vaultGrant(ctx, a, v, sub == "grant", args)
	case "entries":
		return vaultEntries(ctx, a, v, args)
	case "get":
		if len(args) != 1 {
			return errx.Validation("usage: seaport vault get <vault> <name>")
		}
		e, err := entryNamed(ctx, a, v, args[0])
		if err != nil {
			return err
		}
		fmt.Print(e.Body)
		if !strings.HasSuffix(e.Body, "\n") {
			fmt.Println()
		}
		return nil
	case "put":
		return vaultPut(ctx, a, v, args)
	case "rm":
		if len(args) != 1 {
			return errx.Validation("usage: seaport vault rm <vault> <name>")
		}
		e, err := entryNamed(ctx, a, v, args[0])
		if err != nil {
			return err
		}
		if err := a.do(ctx, http.MethodDelete, entryPath(v, e.ID), nil, nil); err != nil {
			return err
		}
		fmt.Printf("%s/%s deleted\n", v.Name, e.Name)
		return nil
	case "versions":
		return vaultVersions(ctx, a, v, args)
	case "revert":
		if len(args) != 2 {
			return errx.Validation("usage: seaport vault revert <vault> <name> <version>")
		}
		n, err := strconv.Atoi(args[1])
		if err != nil || n < 1 {
			return errx.Validation("version must be a number, 1 or more (seaport vault versions)")
		}
		e, err := entryNamed(ctx, a, v, args[0])
		if err != nil {
			return err
		}
		var out vault.Entry
		if err := a.do(ctx, http.MethodPost, entryPath(v, e.ID)+"/revert", map[string]int{"version": n}, &out); err != nil {
			return err
		}
		fmt.Printf("%s/%s is back to the text of version %d, saved as version %d\n", v.Name, out.Name, n, out.Version)
		return nil
	case "pull":
		return vaultPull(ctx, a, v, args)
	case "push":
		return vaultPush(ctx, a, v, args)
	}
	return errx.Validation("unknown vault command " + sub + "\n" + vaultUsage)
}

func vaultPath(v vault.Vault) string { return "/api/v1/vaults/" + v.ID.String() }

func entryPath(v vault.Vault, id identity.EntryID) string {
	return vaultPath(v) + "/entries/" + id.String()
}

// listAll reads every page of a paginated list.
func listAll[T any](ctx context.Context, a *admin, path string) ([]T, error) {
	var out []T
	sep := "?"
	if strings.Contains(path, "?") {
		sep = "&"
	}
	for offset := 0; ; {
		var page query.Paginated[T]
		if err := a.do(ctx, http.MethodGet, fmt.Sprintf("%s%slimit=%d&offset=%d", path, sep, query.MaxLimit, offset), nil, &page); err != nil {
			return nil, err
		}
		out = append(out, page.Items...)
		offset += len(page.Items)
		if len(page.Items) == 0 || offset >= page.Page.Total || offset > query.MaxOffset {
			return out, nil
		}
	}
}

// findVault takes a vault's id or name.
func findVault(ctx context.Context, a *admin, ref string) (vault.Vault, error) {
	var v vault.Vault
	if id, err := identity.ParseVaultID(ref); err == nil {
		return v, a.do(ctx, http.MethodGet, "/api/v1/vaults/"+id.String(), nil, &v)
	}
	all, err := listAll[vault.Vault](ctx, a, "/api/v1/vaults")
	if err != nil {
		return v, err
	}
	for _, v := range all {
		if v.Name == strings.ToLower(ref) {
			return v, nil
		}
	}
	return v, errx.NotFound("no vault " + ref + " (seaport vault list)")
}

// entryNamed finds an entry by name (the routes name entries by id).
func entryNamed(ctx context.Context, a *admin, v vault.Vault, name string) (vault.Entry, error) {
	if err := vault.ValidName("name", name); err != nil {
		return vault.Entry{}, err
	}
	all, err := listAll[vault.Summary](ctx, a, vaultPath(v)+"/entries")
	if err != nil {
		return vault.Entry{}, err
	}
	for _, s := range all {
		if s.Name == name {
			return fullEntry(ctx, a, v, s.ID)
		}
	}
	return vault.Entry{}, errx.NotFound("no entry " + name + " in " + v.Name + " (seaport vault entries " + v.Name + ")")
}

func fullEntry(ctx context.Context, a *admin, v vault.Vault, id identity.EntryID) (vault.Entry, error) {
	var e vault.Entry
	return e, a.do(ctx, http.MethodGet, entryPath(v, id), nil, &e)
}

func vaultList(ctx context.Context, a *admin) error {
	all, err := listAll[vault.Vault](ctx, a, "/api/v1/vaults")
	if err != nil {
		return err
	}
	if len(all) == 0 {
		fmt.Println(`no vaults yet: seaport vault create <name> [-title T] [-description D]`)
		return nil
	}
	w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(w, "ID\tNAME\tENTRIES\tTITLE")
	for _, v := range all {
		fmt.Fprintf(w, "%s\t%s\t%d\t%s\n", v.ID, v.Name, v.Entries, v.Title)
	}
	return w.Flush()
}

func vaultCreate(ctx context.Context, a *admin, args []string) error {
	if len(args) == 0 {
		return errx.Validation("usage: seaport vault create <name> [-title T] [-description D]")
	}
	fs := flag.NewFlagSet("vault create", flag.ContinueOnError)
	title := fs.String("title", "", "a short title")
	desc := fs.String("description", "", "what the vault is for (agents read it)")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	var v vault.Vault
	if err := a.do(ctx, http.MethodPost, "/api/v1/vaults", map[string]string{"name": args[0], "title": *title, "description": *desc}, &v); err != nil {
		return err
	}
	fmt.Printf("vault %s created (%s)\nGive it to agents or docks: seaport vault grant %s -agent <agent> -mode read\n", v.Name, v.ID, v.Name)
	return nil
}

func vaultShow(ctx context.Context, a *admin, v vault.Vault) error {
	var d struct {
		vault.Vault
		vault.Grants
	}
	if err := a.do(ctx, http.MethodGet, vaultPath(v), nil, &d); err != nil {
		return err
	}
	fmt.Printf("%s (%s)\n", d.Name, d.ID)
	if d.Title != "" {
		fmt.Println("title:      ", d.Title)
	}
	if d.Description != "" {
		fmt.Println("description:", d.Description)
	}
	fmt.Printf("entries:     %d (updated %s)\n", d.Entries, d.UpdatedAt.Local().Format("2006-01-02 15:04"))
	w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(w, "\nGRANTED TO\tNAME\tMODE\tID")
	for _, g := range d.Agents {
		fmt.Fprintf(w, "agent\t%s\t%s\t%s\n", g.Name, g.Mode, g.Agent)
	}
	for _, g := range d.Docks {
		fmt.Fprintf(w, "dock\t%s (%s)\t%s\t%s\n", g.Name, g.Channel, g.Mode, g.Dock)
	}
	if len(d.Agents)+len(d.Docks) == 0 {
		fmt.Fprintln(w, "(nobody)\t\t\t")
	}
	return w.Flush()
}

func vaultUpdate(ctx context.Context, a *admin, v vault.Vault, args []string) error {
	fs := flag.NewFlagSet("vault update", flag.ContinueOnError)
	fs.String("title", "", "a short title")
	fs.String("description", "", "what the vault is for")
	if err := fs.Parse(args); err != nil {
		return err
	}
	// only the flags given are sent: the others keep their value
	body := map[string]string{}
	fs.Visit(func(f *flag.Flag) { body[f.Name] = f.Value.String() })
	if len(body) == 0 {
		return errx.Validation("usage: seaport vault update <vault> [-title T] [-description D]")
	}
	var out vault.Vault
	if err := a.do(ctx, http.MethodPatch, vaultPath(v), body, &out); err != nil {
		return err
	}
	fmt.Printf("vault %s updated\n", out.Name)
	return nil
}

func vaultGrant(ctx context.Context, a *admin, v vault.Vault, grant bool, args []string) error {
	verb := "revoke"
	if grant {
		verb = "grant"
	}
	usage := errx.Validation("usage: seaport vault " + verb + " <vault> (-agent <agent>|-dock <dock-id>)" + map[bool]string{true: " -mode read|write", false: ""}[grant])
	fs := flag.NewFlagSet("vault "+verb, flag.ContinueOnError)
	agentRef := fs.String("agent", "", "an agent (name or id): it sees the vault everywhere")
	dockRef := fs.String("dock", "", "a dock id: its vaults are seen while an agent works a conversation of that dock")
	mode := fs.String("mode", "", "read | write")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() > 0 || (*agentRef == "") == (*dockRef == "") {
		return usage
	}
	method, body := http.MethodDelete, any(nil)
	if grant {
		if *mode == "" {
			return usage
		}
		if err := vault.Mode(*mode).Validate(); err != nil {
			return err
		}
		method, body = http.MethodPut, map[string]string{"mode": *mode}
	}
	to, path := "", ""
	if *agentRef != "" {
		ag, err := findAgent(ctx, a, *agentRef)
		if err != nil {
			return err
		}
		to, path = "agent "+ag.Name, vaultPath(v)+"/agents/"+ag.ID.String()
	} else {
		id, err := identity.ParseDockID(*dockRef)
		if err != nil {
			return errx.Validation("-dock takes a dock id (seaport dock list)")
		}
		to, path = "dock "+id.String(), vaultPath(v)+"/docks/"+id.String()
	}
	if err := a.do(ctx, method, path, body, nil); err != nil {
		return err
	}
	if grant {
		fmt.Printf("%s now has %s on vault %s\n", to, *mode, v.Name)
	} else {
		fmt.Printf("%s no longer has vault %s\n", to, v.Name)
	}
	return nil
}

func vaultEntries(ctx context.Context, a *admin, v vault.Vault, args []string) error {
	fs := flag.NewFlagSet("vault entries", flag.ContinueOnError)
	q := fs.String("q", "", "search these words in the entries instead of listing them")
	if err := fs.Parse(args); err != nil {
		return err
	}
	w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
	if *q != "" {
		var out struct {
			Hits []vault.Hit `json:"hits"`
		}
		if err := a.do(ctx, http.MethodGet, vaultPath(v)+"/entries?q="+url.QueryEscape(*q), nil, &out); err != nil {
			return err
		}
		if len(out.Hits) == 0 {
			fmt.Println("no entry matches")
			return nil
		}
		fmt.Fprintln(w, "NAME\tVER\tBY\tSNIPPET")
		for _, h := range out.Hits {
			fmt.Fprintf(w, "%s\tv%d\t%s\t%s\n", h.Name, h.Version, by(h.Provenance), oneLine(h.Snippet))
		}
		return w.Flush()
	}
	all, err := listAll[vault.Summary](ctx, a, vaultPath(v)+"/entries")
	if err != nil {
		return err
	}
	if len(all) == 0 {
		fmt.Printf("%s has no entries: seaport vault put %s <name> \"text\"\n", v.Name, v.Name)
		return nil
	}
	fmt.Fprintln(w, "NAME\tVER\tUPDATED\tBY\tFIRST LINE")
	for _, s := range all {
		fmt.Fprintf(w, "%s\tv%d\t%s\t%s\t%s\n", s.Name, s.Version, s.UpdatedAt.Local().Format("2006-01-02 15:04"), by(s.Provenance), s.FirstLine)
	}
	return w.Flush()
}

// by names who wrote: "agent:ana" or "user:ana".
func by(p vault.Provenance) string { return string(p.Kind) + ":" + p.By }

func oneLine(s string) string { return strings.Join(strings.Fields(s), " ") }

// vaultPut creates an entry, or saves a new version of it (base = the
// version just read).
func vaultPut(ctx context.Context, a *admin, v vault.Vault, args []string) error {
	usage := errx.Validation(`usage: seaport vault put <vault> <name> (-file F.md | "body")   (-file - reads stdin)`)
	// args: <name> then the flags or the body (the vault is already resolved).
	if len(args) < 1 {
		return usage
	}
	name := args[0]
	fs := flag.NewFlagSet("vault put", flag.ContinueOnError)
	file := fs.String("file", "", "read the text from this file (- for stdin)")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	var body string
	switch {
	case *file != "" && fs.NArg() == 0:
		var raw []byte
		var err error
		if *file == "-" {
			raw, err = io.ReadAll(os.Stdin)
		} else {
			raw, err = os.ReadFile(*file)
		}
		if err != nil {
			return err
		}
		body = string(raw)
	case *file == "" && fs.NArg() == 1:
		body = fs.Arg(0)
	default:
		return usage
	}
	if err := vault.ValidName("name", name); err != nil {
		return err
	}
	e, created, err := saveEntry(ctx, a, v, name, body, nil)
	if err != nil {
		return err
	}
	if created {
		fmt.Printf("%s/%s created (version %d)\n", v.Name, e.Name, e.Version)
	} else {
		fmt.Printf("%s/%s saved as version %d\n", v.Name, e.Name, e.Version)
	}
	return nil
}

// saveEntry creates the entry, or edits it when it exists: a GET gives the
// base. known are the entries already listed (nil: list them).
func saveEntry(ctx context.Context, a *admin, v vault.Vault, name, body string, known map[string]vault.Entry) (vault.Entry, bool, error) {
	var cur vault.Entry
	found := false
	if known != nil {
		cur, found = known[name]
	} else if e, err := entryNamed(ctx, a, v, name); err == nil {
		cur, found = e, true
	} else if !errx.IsType(err, errx.TypeNotFound) {
		return cur, false, err
	}
	var out vault.Entry
	if !found {
		err := a.do(ctx, http.MethodPost, vaultPath(v)+"/entries", map[string]any{"name": name, "body": body}, &out)
		return out, true, err
	}
	err := a.do(ctx, http.MethodPut, entryPath(v, cur.ID), map[string]any{"body": body, "base": cur.Version}, &out)
	return out, false, err
}

func vaultVersions(ctx context.Context, a *admin, v vault.Vault, args []string) error {
	if len(args) != 1 {
		return errx.Validation("usage: seaport vault versions <vault> <name>")
	}
	e, err := entryNamed(ctx, a, v, args[0])
	if err != nil {
		return err
	}
	var out struct {
		Versions []vault.Version `json:"versions"`
	}
	if err := a.do(ctx, http.MethodGet, entryPath(v, e.ID)+"/versions", nil, &out); err != nil {
		return err
	}
	w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(w, "VER\tWHEN\tBY\tSIZE\tFIRST LINE")
	for _, x := range out.Versions {
		cur := ""
		if x.Version == e.Version {
			cur = " (current)"
		}
		fmt.Fprintf(w, "v%d%s\t%s\t%s\t%s\t%s\n", x.Version, cur, x.CreatedAt.Local().Format("2006-01-02 15:04"), by(x.Provenance),
			bytesize(int64(len(x.Body))), vault.FirstLine(x.Body))
	}
	return w.Flush()
}

// vaultPull writes one <name>.md per entry into dir, overwriting.
func vaultPull(ctx context.Context, a *admin, v vault.Vault, args []string) error {
	if len(args) != 1 {
		return errx.Validation("usage: seaport vault pull <vault> <dir>")
	}
	dir := expandHome(args[0])
	all, err := listAll[vault.Summary](ctx, a, vaultPath(v)+"/entries")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return errx.Validation("create " + dir + ": " + err.Error())
	}
	for _, s := range all {
		e, err := fullEntry(ctx, a, v, s.ID)
		if err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(dir, e.Name+".md"), []byte(strings.TrimRight(e.Body, "\n")+"\n"), 0o644); err != nil {
			return errx.Validation("write " + e.Name + ": " + err.Error())
		}
	}
	fmt.Printf("%d entries of %s in %s\n", len(all), v.Name, dir)
	return nil
}

// PushOp is what push does with one entry.
type pushOp string

const (
	pushCreate    pushOp = "create"
	pushUpdate    pushOp = "update"
	pushDelete    pushOp = "delete"
	pushUnchanged pushOp = "unchanged"
)

type pushStep struct {
	Op   pushOp
	Name string
}

// planPush says, per name and sorted by it, what makes the vault hold the
// files: create what is new, update what differs (the server trims the
// text, so surrounding whitespace is no difference), delete what has no
// file when prune, else leave it. local maps entry name → file text,
// remote entry name → current text.
func planPush(local, remote map[string]string, prune bool) []pushStep {
	var steps []pushStep
	for name, body := range local {
		switch cur, ok := remote[name]; {
		case !ok:
			steps = append(steps, pushStep{pushCreate, name})
		case strings.TrimSpace(cur) != strings.TrimSpace(body):
			steps = append(steps, pushStep{pushUpdate, name})
		default:
			steps = append(steps, pushStep{pushUnchanged, name})
		}
	}
	if prune {
		for name := range remote {
			if _, ok := local[name]; !ok {
				steps = append(steps, pushStep{pushDelete, name})
			}
		}
	}
	slices.SortFunc(steps, func(x, y pushStep) int { return strings.Compare(x.Name, y.Name) })
	return steps
}

// readEntryFiles reads the *.md files of dir whose name (without .md) is a
// valid entry name; the others are answered by name as skipped.
func readEntryFiles(dir string) (files map[string]string, skipped []string, err error) {
	des, err := os.ReadDir(dir)
	if err != nil {
		return nil, nil, errx.Validation("read " + dir + ": " + err.Error())
	}
	files = map[string]string{}
	for _, d := range des {
		stem, ok := strings.CutSuffix(d.Name(), ".md")
		if !ok || d.IsDir() || vault.ValidName("name", stem) != nil {
			if !d.IsDir() {
				skipped = append(skipped, d.Name())
			}
			continue
		}
		raw, err := os.ReadFile(filepath.Join(dir, d.Name()))
		if err != nil {
			return nil, nil, errx.Validation("read " + d.Name() + ": " + err.Error())
		}
		files[stem] = string(raw)
	}
	return files, skipped, nil
}

// vaultPush makes the vault hold the dir's entry files (see planPush).
func vaultPush(ctx context.Context, a *admin, v vault.Vault, args []string) error {
	usage := errx.Validation("usage: seaport vault push <vault> <dir> [-prune]")
	if len(args) < 1 {
		return usage
	}
	fs := flag.NewFlagSet("vault push", flag.ContinueOnError)
	prune := fs.Bool("prune", false, "delete the entries that have no file in the dir")
	if err := fs.Parse(args[1:]); err != nil || fs.NArg() > 0 {
		if err == nil {
			err = usage
		}
		return err
	}
	dir := expandHome(args[0])
	files, skipped, err := readEntryFiles(dir)
	if err != nil {
		return err
	}
	if len(skipped) > 0 {
		fmt.Printf("left out (not <name>.md with a valid entry name): %s\n", strings.Join(skipped, ", "))
	}
	if len(files) == 0 && !*prune {
		return errx.Validation("no entry files in " + dir)
	}
	sums, err := listAll[vault.Summary](ctx, a, vaultPath(v)+"/entries")
	if err != nil {
		return err
	}
	known := map[string]vault.Entry{}
	remote := map[string]string{}
	for _, s := range sums {
		e := vault.Entry{ID: s.ID, Name: s.Name, Version: s.Version}
		if _, here := files[s.Name]; here {
			if e, err = fullEntry(ctx, a, v, s.ID); err != nil {
				return err
			}
		}
		known[s.Name], remote[s.Name] = e, e.Body
	}
	counts := map[pushOp]int{}
	var failed []string
	for _, st := range planPush(files, remote, *prune) {
		var err error
		switch st.Op {
		case pushCreate, pushUpdate:
			_, _, err = saveEntry(ctx, a, v, st.Name, files[st.Name], known)
		case pushDelete:
			err = a.do(ctx, http.MethodDelete, entryPath(v, known[st.Name].ID), nil, nil)
		}
		if err != nil {
			fmt.Fprintf(os.Stderr, "%s: %s: %v\n", st.Name, st.Op, errMessage(err))
			failed = append(failed, st.Name)
			continue
		}
		counts[st.Op]++
		if st.Op != pushUnchanged {
			fmt.Printf("  %s %s\n", st.Op, st.Name)
		}
	}
	fmt.Printf("%s: %d created, %d updated, %d deleted, %d unchanged\n", v.Name,
		counts[pushCreate], counts[pushUpdate], counts[pushDelete], counts[pushUnchanged])
	if len(failed) > 0 {
		return errx.Validation(strconv.Itoa(len(failed)) + " entries failed: " + strings.Join(failed, ", "))
	}
	return nil
}

func errMessage(err error) string {
	var e *errx.Error
	if errors.As(err, &e) {
		return e.Message
	}
	return err.Error()
}
