package main

import (
	"bufio"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"text/tabwriter"

	"github.com/Abraxas-365/seaport/internal/agent"
	"github.com/Abraxas-365/seaport/internal/errx"
	"golang.org/x/term"
)

// profileFile is the profile as `seaport agents profile` reads and writes
// it (show -json | set -f).
type profileFile struct {
	Instructions string            `json:"instructions"`
	Skills       []agent.Skill     `json:"skills"`
	MCPServers   []agent.MCPServer `json:"mcp_servers"`
	Approve      string            `json:"approve"`
	Model        string            `json:"model"`
	Effort       string            `json:"effort"`
}

// profileCmd is `seaport agents profile [show|set|versions] <agent> ...`.
func profileCmd(ctx context.Context, a *admin, args []string) error {
	sub := "show"
	if len(args) > 0 && (args[0] == "show" || args[0] == "set" || args[0] == "versions") {
		sub, args = args[0], args[1:]
	}
	if len(args) == 0 {
		return errx.Validation("usage: seaport agents profile [show|set|versions] <agent> ...")
	}
	ag, err := findAgent(ctx, a, args[0])
	if err != nil {
		return err
	}
	base := "/api/v1/agents/" + ag.ID.String() + "/profile"
	switch sub {
	case "show":
		fs := flag.NewFlagSet("agents profile show", flag.ContinueOnError)
		version := fs.Int("version", 0, "a past version (default: current)")
		asJSON := fs.Bool("json", false, "print the profile as JSON (the format of set -f)")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		path := base
		if *version > 0 {
			path += "/versions/" + strconv.Itoa(*version)
		}
		var p agent.Profile
		if err := a.do(ctx, http.MethodGet, path, nil, &p); err != nil {
			return err
		}
		if *asJSON {
			enc := json.NewEncoder(os.Stdout)
			enc.SetIndent("", "  ")
			return enc.Encode(fileOf(p))
		}
		printProfile(ag.Name, p)
		return nil
	case "versions":
		var out struct {
			Versions []agent.Version `json:"versions"`
		}
		if err := a.do(ctx, http.MethodGet, base+"/versions", nil, &out); err != nil {
			return err
		}
		if len(out.Versions) == 0 {
			fmt.Printf("agent %s has no profile yet: seaport agents profile set %s ...\n", ag.Name, ag.Name)
			return nil
		}
		w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
		fmt.Fprintln(w, "VERSION\tBY\tAT")
		for _, v := range out.Versions {
			cur := ""
			if v.Version == ag.ProfileVersion {
				cur = " (current)"
			}
			fmt.Fprintf(w, "%d%s\t%s\t%s\n", v.Version, cur, v.CreatedBy, v.CreatedAt.Local().Format("2006-01-02 15:04"))
		}
		return w.Flush()
	case "set":
		return setProfile(ctx, a, ag, base, args[1:])
	}
	return nil
}

func setProfile(ctx context.Context, a *admin, ag agent.Agent, base string, args []string) error {
	fs := flag.NewFlagSet("agents profile set", flag.ContinueOnError)
	file := fs.String("f", "", "whole profile from a JSON file (- = stdin; format of show -json)")
	instructions := fs.String("instructions-file", "", "instructions from a file (e.g. AGENTS.md; - = stdin)")
	skillsDir := fs.String("skills-dir", "", "replace the skills with <dir>/*/SKILL.md (front matter name, description)")
	var addSkills, dropSkills multiFlag
	fs.Var(&addSkills, "skill", "add or replace a skill from a SKILL.md file (repeatable)")
	fs.Var(&dropSkills, "rm-skill", "remove a skill by name (repeatable)")
	approve := fs.String("approve", "", "permission requests: allow|reject|connector (the connector's -approve decides)")
	model := fs.String("model", "", "model, among the harness's options (\"none\" clears)")
	effort := fs.String("effort", "", "reasoning effort, among the harness's options (\"none\" clears)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	set := map[string]bool{}
	fs.Visit(func(f *flag.Flag) { set[f.Name] = true })
	if len(set) == 0 {
		return errx.Validation("nothing to change: -f, -instructions-file, -skills-dir, -skill, -rm-skill, -approve, -model or -effort")
	}
	// Start from the current profile (with skill bodies): flags edit it.
	var cur agent.Profile
	if err := a.do(ctx, http.MethodGet, base, nil, &cur); err != nil {
		return err
	}
	p := fileOf(cur)
	if *file != "" {
		raw, err := readInput(*file)
		if err != nil {
			return err
		}
		p = profileFile{}
		dec := json.NewDecoder(strings.NewReader(string(raw)))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&p); err != nil {
			return errx.Validation("profile file: " + err.Error())
		}
	}
	if *instructions != "" {
		raw, err := readInput(*instructions)
		if err != nil {
			return err
		}
		p.Instructions = string(raw)
	}
	if *skillsDir != "" {
		paths, _ := filepath.Glob(filepath.Join(*skillsDir, "*", "SKILL.md"))
		if len(paths) == 0 {
			return errx.Validation("no */SKILL.md under " + *skillsDir)
		}
		sort.Strings(paths)
		p.Skills = nil
		for _, path := range paths {
			s, err := readSkill(path)
			if err != nil {
				return err
			}
			p.Skills = append(p.Skills, s)
		}
	}
	for _, path := range addSkills {
		s, err := readSkill(path)
		if err != nil {
			return err
		}
		p.Skills = putSkill(p.Skills, s)
	}
	for _, name := range dropSkills {
		n := len(p.Skills)
		p.Skills = deleteSkill(p.Skills, name)
		if len(p.Skills) == n {
			return errx.NotFound("agent " + ag.Name + " has no skill " + name)
		}
	}
	if set["approve"] {
		switch *approve {
		case agent.ApproveAllow, agent.ApproveReject:
			p.Approve = *approve
		case "connector":
			p.Approve = ""
		default:
			return errx.InvalidField("approve", "-approve must be allow, reject or connector")
		}
	}
	if set["model"] {
		p.Model = cleared(*model)
	}
	if set["effort"] {
		p.Effort = cleared(*effort)
	}
	body := map[string]any{"base": cur.Version, "instructions": p.Instructions, "skills": p.Skills,
		"mcp_servers": p.MCPServers, "approve": p.Approve, "model": p.Model, "effort": p.Effort}
	var out agent.Profile
	if err := a.do(ctx, http.MethodPut, base, body, &out); err != nil {
		return err
	}
	fmt.Printf("agent %s profile v%d saved; connected harnesses restart with it once idle\n", ag.Name, out.Version)
	return nil
}

// secretsCmd is `seaport agents secrets [list|set|rm] <agent> ...`.
func secretsCmd(ctx context.Context, a *admin, args []string) error {
	sub := "list"
	if len(args) > 0 && (args[0] == "list" || args[0] == "set" || args[0] == "rm") {
		sub, args = args[0], args[1:]
	}
	if len(args) == 0 {
		return errx.Validation("usage: seaport agents secrets [list|set|rm] <agent> ...")
	}
	ag, err := findAgent(ctx, a, args[0])
	if err != nil {
		return err
	}
	base := "/api/v1/agents/" + ag.ID.String() + "/secrets"
	switch sub {
	case "list":
		var out struct {
			Secrets []agent.Secret `json:"secrets"`
		}
		if err := a.do(ctx, http.MethodGet, base, nil, &out); err != nil {
			return err
		}
		if len(out.Secrets) == 0 {
			fmt.Printf("agent %s has no secrets: seaport agents secrets set %s NAME [-env]\n", ag.Name, ag.Name)
			return nil
		}
		w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
		fmt.Fprintln(w, "NAME\tENV\tUPDATED")
		for _, s := range out.Secrets {
			fmt.Fprintf(w, "%s\t%v\t%s\n", s.Name, s.Env, s.UpdatedAt.Local().Format("2006-01-02 15:04"))
		}
		return w.Flush()
	case "set":
		if len(args) < 2 {
			return errx.Validation("usage: seaport agents secrets set <agent> NAME [-env]  (value from stdin)")
		}
		fs := flag.NewFlagSet("agents secrets set", flag.ContinueOnError)
		env := fs.Bool("env", false, "also give it to the harness as an environment variable")
		if err := fs.Parse(args[2:]); err != nil {
			return err
		}
		value, err := readSecret(args[1])
		if err != nil {
			return err
		}
		if err := a.do(ctx, http.MethodPut, base+"/"+url.PathEscape(args[1]), map[string]any{"value": value, "env": *env}, nil); err != nil {
			return err
		}
		fmt.Printf("secret %s of agent %s saved; use it in an MCP header as ${%s}\n", args[1], ag.Name, args[1])
		return nil
	case "rm":
		if len(args) != 2 {
			return errx.Validation("usage: seaport agents secrets rm <agent> NAME")
		}
		if err := a.do(ctx, http.MethodDelete, base+"/"+url.PathEscape(args[1]), nil, nil); err != nil {
			return err
		}
		fmt.Printf("secret %s of agent %s deleted\n", args[1], ag.Name)
		return nil
	}
	return nil
}

// readSecret reads a secret's value: hidden from a terminal, else the
// whole of stdin (one trailing newline dropped). Never from the command
// line, which shells keep in their history.
func readSecret(name string) (string, error) {
	if term.IsTerminal(int(os.Stdin.Fd())) {
		fmt.Fprintf(os.Stderr, "value of %s: ", name)
		raw, err := term.ReadPassword(int(os.Stdin.Fd()))
		fmt.Fprintln(os.Stderr)
		if err != nil {
			return "", errx.Wrap(err, "read secret", errx.TypeInternal)
		}
		return string(raw), nil
	}
	raw, err := io.ReadAll(io.LimitReader(bufio.NewReader(os.Stdin), agent.MaxSecretValue+2))
	if err != nil {
		return "", errx.Wrap(err, "read secret", errx.TypeInternal)
	}
	return strings.TrimSuffix(strings.TrimSuffix(string(raw), "\n"), "\r"), nil
}

func fileOf(p agent.Profile) profileFile {
	return profileFile{Instructions: p.Instructions, Skills: p.Skills, MCPServers: p.MCPServers,
		Approve: p.Approve, Model: p.Model, Effort: p.Effort}
}

func printProfile(name string, p agent.Profile) {
	if p.Version == 0 {
		fmt.Printf("agent %s has no profile yet: seaport agents profile set %s -instructions-file AGENTS.md ...\n", name, name)
		return
	}
	fmt.Printf("agent %s, profile v%d (%s, %s)\n", name, p.Version, p.CreatedBy, p.CreatedAt.Local().Format("2006-01-02 15:04"))
	fmt.Printf("approve: %s  model: %s  effort: %s\n", orDash(p.Approve, "connector"), orDash(p.Model, "harness default"), orDash(p.Effort, "harness default"))
	fmt.Printf("\ninstructions (%d chars):\n", len(p.Instructions))
	if p.Instructions == "" {
		fmt.Println("  -")
	} else {
		for _, l := range strings.Split(strings.TrimSpace(p.Instructions), "\n") {
			fmt.Println("  " + l)
		}
	}
	fmt.Printf("\nskills (%d):\n", len(p.Skills))
	for _, s := range p.Skills {
		fmt.Printf("  %s: %s\n", s.Name, s.Description)
	}
	fmt.Printf("\nMCP servers (%d):\n", len(p.MCPServers))
	for _, s := range p.MCPServers {
		hs := []string{}
		for _, h := range s.Headers {
			hs = append(hs, h.Name+": "+h.Value)
		}
		fmt.Printf("  %s  %s  %s\n", s.Name, s.URL, strings.Join(hs, ", "))
	}
}

// readSkill reads a SKILL.md: front matter (name, description), then the
// instructions. Without a name the directory names it.
func readSkill(path string) (agent.Skill, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return agent.Skill{}, errx.Wrap(err, "read skill "+path, errx.TypeValidation)
	}
	s := agent.Skill{Name: filepath.Base(filepath.Dir(path))}
	body := strings.ReplaceAll(string(raw), "\r\n", "\n")
	if rest, ok := strings.CutPrefix(body, "---\n"); ok {
		if front, after, ok := strings.Cut(rest, "\n---"); ok {
			for _, line := range strings.Split(front, "\n") {
				k, v, ok := strings.Cut(line, ":")
				if !ok {
					continue
				}
				v = strings.Trim(strings.TrimSpace(v), `"'`)
				switch strings.TrimSpace(k) {
				case "name":
					s.Name = v
				case "description":
					s.Description = v
				}
			}
			body = strings.TrimPrefix(after, "\n")
		}
	}
	s.Body = strings.TrimSpace(body)
	if s.Description == "" {
		return s, errx.Validation(path + ": the front matter needs a one-line description (when to use the skill)")
	}
	return s, nil
}

func putSkill(skills []agent.Skill, s agent.Skill) []agent.Skill {
	for i := range skills {
		if skills[i].Name == s.Name {
			skills[i] = s
			return skills
		}
	}
	return append(skills, s)
}

func deleteSkill(skills []agent.Skill, name string) []agent.Skill {
	out := skills[:0]
	for _, s := range skills {
		if s.Name != name {
			out = append(out, s)
		}
	}
	return out
}

func readInput(path string) ([]byte, error) {
	if path == "-" {
		return io.ReadAll(os.Stdin)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, errx.Wrap(err, "read "+path, errx.TypeValidation)
	}
	return raw, nil
}

func cleared(v string) string {
	if v == "none" {
		return ""
	}
	return v
}

func orDash(v, def string) string {
	if v == "" {
		return def
	}
	return v
}

type multiFlag []string

func (m *multiFlag) String() string     { return strings.Join(*m, ",") }
func (m *multiFlag) Set(v string) error { *m = append(*m, v); return nil }
