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
	// Browser: the agent uses its machine's managed browser (hosted).
	Browser bool `json:"browser"`
	// BrowserAssist: whom to tell about a takeover ("", conversation or a
	// destination name).
	BrowserAssist string `json:"browser_assist"`
	// BrowserDetect: the connector asks takeovers itself; BrowserIgnoreHosts
	// are the sites it leaves alone.
	BrowserDetect      bool     `json:"browser_detect"`
	BrowserIgnoreHosts []string `json:"browser_ignore_hosts"`
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
	base := configPath(ag)
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
			enc := json.NewEncoder(stdout)
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
			fmt.Fprintf(stdout, "agent %s has no profile yet: seaport agents profile set %s ...\n", ag.Name, ag.Name)
			return nil
		}
		w := tabwriter.NewWriter(stdout, 0, 4, 2, ' ', 0)
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
	browser := fs.String("browser", "", "on|off: use the machine's managed browser (hosted agents)")
	assist := fs.String("browser-assist", "", "whom to tell about a takeover: none|conversation|<destination name>")
	detect := fs.String("browser-detect", "", "on|off: the connector detects sign-in, captcha and 2FA pages and asks the takeover itself")
	ignore := fs.String("browser-ignore", "", "comma-separated sites the detector leaves alone (\"none\" clears)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	set := map[string]bool{}
	fs.Visit(func(f *flag.Flag) { set[f.Name] = true })
	if len(set) == 0 {
		return errx.Validation("nothing to change: -f, -instructions-file, -skills-dir, -skill, -rm-skill, -approve, -model, -effort, -browser, -browser-assist, -browser-detect or -browser-ignore")
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
	if set["browser"] {
		switch *browser {
		case "on":
			p.Browser = true
		case "off":
			p.Browser = false
		default:
			return errx.InvalidField("browser", "-browser must be on or off")
		}
	}
	if set["browser-assist"] {
		p.BrowserAssist = cleared(*assist)
	}
	if set["browser-detect"] {
		switch *detect {
		case "on":
			p.BrowserDetect = true
		case "off":
			p.BrowserDetect = false
		default:
			return errx.InvalidField("browser-detect", "-browser-detect must be on or off")
		}
	}
	if set["browser-ignore"] {
		p.BrowserIgnoreHosts = []string{}
		for _, h := range strings.Split(cleared(*ignore), ",") {
			if h = strings.TrimSpace(h); h != "" {
				p.BrowserIgnoreHosts = append(p.BrowserIgnoreHosts, h)
			}
		}
	}
	if p.BrowserIgnoreHosts == nil {
		p.BrowserIgnoreHosts = []string{}
	}
	// the harness and machine stay as they are (config apply edits them)
	body := configBody(cur.Version, cur.Harness, p, cur.Machine)
	var out agent.Profile
	if err := a.do(ctx, http.MethodPut, base, body, &out); err != nil {
		return err
	}
	if out.Version == cur.Version {
		fmt.Fprintf(stdout, "agent %s profile unchanged (v%d)\n", ag.Name, out.Version)
		return nil
	}
	fmt.Fprintf(stdout, "agent %s profile v%d saved; connected harnesses restart with it once idle\n", ag.Name, out.Version)
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
			fmt.Fprintf(stdout, "agent %s has no secrets: seaport agents secrets set %s NAME [-env]\n", ag.Name, ag.Name)
			return nil
		}
		w := tabwriter.NewWriter(stdout, 0, 4, 2, ' ', 0)
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
		fmt.Fprintf(stdout, "secret %s of agent %s saved; name it as ${%s} in an MCP header or the machine (model keys, repo credentials)\n", args[1], ag.Name, args[1])
		return nil
	case "rm":
		if len(args) != 2 {
			return errx.Validation("usage: seaport agents secrets rm <agent> NAME")
		}
		if err := a.do(ctx, http.MethodDelete, base+"/"+url.PathEscape(args[1]), nil, nil); err != nil {
			return err
		}
		fmt.Fprintf(stdout, "secret %s of agent %s deleted\n", args[1], ag.Name)
		return nil
	}
	return nil
}

// readSecret reads a secret's value: hidden from a terminal, else the
// whole of stdin (one trailing newline dropped). Never from the command
// line, which shells keep in their history.
func readSecret(name string) (string, error) {
	if f, ok := stdin.(*os.File); ok && term.IsTerminal(int(f.Fd())) {
		fmt.Fprintf(stderr, "value of %s: ", name)
		raw, err := term.ReadPassword(int(f.Fd()))
		fmt.Fprintln(stderr)
		if err != nil {
			return "", errx.Wrap(err, "read secret", errx.TypeInternal)
		}
		return string(raw), nil
	}
	raw, err := io.ReadAll(io.LimitReader(bufio.NewReader(stdin), agent.MaxSecretValue+2))
	if err != nil {
		return "", errx.Wrap(err, "read secret", errx.TypeInternal)
	}
	return strings.TrimSuffix(strings.TrimSuffix(string(raw), "\n"), "\r"), nil
}

func fileOf(p agent.Profile) profileFile {
	return profileFile{Instructions: p.Instructions, Skills: p.Skills, MCPServers: p.MCPServers,
		Approve: p.Approve, Model: p.Model, Effort: p.Effort, Browser: p.Browser, BrowserAssist: p.BrowserAssist,
		BrowserDetect: p.BrowserDetect, BrowserIgnoreHosts: p.BrowserIgnoreHosts}
}

func printProfile(name string, p agent.Profile) {
	if p.Version == 0 {
		fmt.Fprintf(stdout, "agent %s has no profile yet: seaport agents profile set %s -instructions-file AGENTS.md ...\n", name, name)
		return
	}
	fmt.Fprintf(stdout, "agent %s, profile v%d (%s, %s)\n", name, p.Version, p.CreatedBy, p.CreatedAt.Local().Format("2006-01-02 15:04"))
	fmt.Fprintf(stdout, "approve: %s  model: %s  effort: %s  browser: %s  browser-assist: %s  browser-detect: %s  browser-ignore: %s\n", orDash(p.Approve, "connector"), orDash(p.Model, "harness default"), orDash(p.Effort, "harness default"), onOff(p.Browser), orDash(p.BrowserAssist, "nobody"), onOff(p.BrowserDetect), orDash(strings.Join(p.BrowserIgnoreHosts, ","), "none"))
	fmt.Fprintf(stdout, "\ninstructions (%d chars):\n", len(p.Instructions))
	if p.Instructions == "" {
		fmt.Fprintln(stdout, "  -")
	} else {
		for _, l := range strings.Split(strings.TrimSpace(p.Instructions), "\n") {
			fmt.Fprintln(stdout, "  "+l)
		}
	}
	fmt.Fprintf(stdout, "\nskills (%d):\n", len(p.Skills))
	for _, s := range p.Skills {
		fmt.Fprintf(stdout, "  %s: %s\n", s.Name, s.Description)
	}
	fmt.Fprintf(stdout, "\nMCP servers (%d):\n", len(p.MCPServers))
	for _, s := range p.MCPServers {
		hs := []string{}
		for _, h := range s.Headers {
			hs = append(hs, h.Name+": "+h.Value)
		}
		fmt.Fprintf(stdout, "  %s  %s  %s\n", s.Name, s.URL, strings.Join(hs, ", "))
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
		return io.ReadAll(stdin)
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

func onOff(b bool) string {
	if b {
		return "on"
	}
	return "off"
}
