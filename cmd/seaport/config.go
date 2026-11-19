package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"strconv"
	"text/tabwriter"
	"time"

	"github.com/Abraxas-365/seaport/internal/agent"
	"github.com/Abraxas-365/seaport/internal/errx"
	"github.com/Abraxas-365/seaport/internal/hosting"
	"go.yaml.in/yaml/v3"
)

// configFile is an agent's config as `seaport agents config apply -f`
// reads it and `config show` writes it: the harness, what the harness is
// told and given (the profile's fields) and the machine it runs on when
// hosted.
type configFile struct {
	Harness string `json:"harness"`
	profileFile
	Machine *hosting.Machine `json:"machine,omitempty"`
}

func configOf(p agent.Profile) (configFile, error) {
	c := configFile{Harness: p.Harness, profileFile: fileOf(p)}
	if len(p.Machine) > 0 && string(p.Machine) != "null" {
		var m hosting.Machine
		if err := json.Unmarshal(p.Machine, &m); err != nil {
			return c, errx.Wrap(err, "decode machine", errx.TypeInternal)
		}
		c.Machine = &m
	}
	return c, nil
}

// readConfigFile parses YAML (or JSON) strictly: an unknown key is an
// error, so a typo doesn't silently drop a setting.
func readConfigFile(raw []byte) (configFile, error) {
	var tree any
	if err := yaml.Unmarshal(raw, &tree); err != nil {
		return configFile{}, errx.Validation("config file: " + err.Error())
	}
	if _, ok := tree.(map[string]any); !ok {
		return configFile{}, errx.Validation("config file: expected a mapping (harness: …, instructions: …, machine: …)")
	}
	js, err := json.Marshal(tree)
	if err != nil {
		return configFile{}, errx.Validation("config file: " + err.Error())
	}
	dec := json.NewDecoder(bytes.NewReader(js))
	dec.DisallowUnknownFields()
	var c configFile
	if err := dec.Decode(&c); err != nil {
		return configFile{}, errx.Validation("config file: " + err.Error())
	}
	return c, nil
}

// writeConfigFile prints c as block YAML in the fields' order, leaving
// out empty values.
func writeConfigFile(w io.Writer, c configFile) error {
	if c.Machine != nil && c.Machine.Resources.Size != "" { // the preset says it (the port resolves it)
		m := *c.Machine
		m.Resources.CPUs, m.Resources.MemoryMB = 0, 0
		c.Machine = &m
	}
	js, err := json.Marshal(c)
	if err != nil {
		return err
	}
	var doc yaml.Node
	if err := yaml.Unmarshal(js, &doc); err != nil {
		return err
	}
	prune(&doc)
	enc := yaml.NewEncoder(w)
	enc.SetIndent(2)
	if err := enc.Encode(&doc); err != nil {
		return err
	}
	return enc.Close()
}

// prune turns JSON's flow style into block style and drops empty strings,
// zero numbers, lists and mappings; it reports whether n is empty.
func prune(n *yaml.Node) bool {
	n.Style = 0
	switch n.Kind {
	case yaml.DocumentNode:
		for _, c := range n.Content {
			prune(c)
		}
		return false
	case yaml.MappingNode:
		kept := n.Content[:0]
		for i := 0; i+1 < len(n.Content); i += 2 {
			k, v := n.Content[i], n.Content[i+1]
			k.Style = 0
			if !prune(v) {
				kept = append(kept, k, v)
			}
		}
		n.Content = kept
		return len(kept) == 0
	case yaml.SequenceNode:
		for _, c := range n.Content {
			prune(c)
		}
		return len(n.Content) == 0
	case yaml.ScalarNode:
		if n.Tag == "!!str" && bytes.ContainsRune([]byte(n.Value), '\n') {
			n.Style = yaml.LiteralStyle
		}
		return (n.Value == "" && n.Tag == "!!str") || ((n.Tag == "!!int" || n.Tag == "!!float") && n.Value == "0") || n.Tag == "!!null"
	}
	return false
}

// configBody is the PUT /agents/{id}/config body: the whole config, from
// base (the version the edit started from).
func configBody(base int, harness string, p profileFile, machine any) map[string]any {
	if p.BrowserIgnoreHosts == nil {
		p.BrowserIgnoreHosts = []string{}
	}
	return map[string]any{"base": base, "instructions": p.Instructions, "skills": p.Skills,
		"mcp_servers": p.MCPServers, "approve": p.Approve, "model": p.Model, "effort": p.Effort, "browser": p.Browser,
		"browser_assist": p.BrowserAssist, "browser_detect": p.BrowserDetect, "browser_ignore_hosts": p.BrowserIgnoreHosts,
		"harness": harness, "machine": machine}
}

func configPath(ag agent.Agent) string { return "/api/v1/agents/" + ag.ID.String() + "/config" }

// saveConfig saves c over version base and says what happened.
func saveConfig(ctx context.Context, a *admin, ag agent.Agent, base int, c configFile) (agent.Profile, error) {
	var machine any
	if c.Machine != nil {
		machine = c.Machine
	}
	var out agent.Profile
	if err := a.do(ctx, http.MethodPut, configPath(ag), configBody(base, c.Harness, c.profileFile, machine), &out); err != nil {
		return out, err
	}
	if out.Version == base {
		fmt.Fprintf(stdout, "agent %s config unchanged (v%d)\n", ag.Name, out.Version)
	} else {
		fmt.Fprintf(stdout, "agent %s config v%d saved; connected harnesses pick it up once idle, hosted slots restart when its machine changed\n", ag.Name, out.Version)
	}
	return out, nil
}

// configCmd is `seaport agents config [show|apply|versions] <agent> ...`.
func configCmd(ctx context.Context, a *admin, args []string) error {
	sub := "show"
	if len(args) > 0 && (args[0] == "show" || args[0] == "apply" || args[0] == "versions") {
		sub, args = args[0], args[1:]
	}
	if len(args) == 0 {
		return errx.Validation("usage: seaport agents config [show|apply|versions] <agent> ...")
	}
	ag, err := findAgent(ctx, a, args[0])
	if err != nil {
		return err
	}
	switch sub {
	case "show":
		fs := flag.NewFlagSet("agents config show", flag.ContinueOnError)
		version := fs.Int("version", 0, "a past version (default: current)")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		path := configPath(ag)
		if *version > 0 {
			path += "/versions/" + strconv.Itoa(*version)
		}
		var p agent.Profile
		if err := a.do(ctx, http.MethodGet, path, nil, &p); err != nil {
			return err
		}
		if p.Version == 0 {
			fmt.Fprintf(stdout, "# agent %s has no config yet: seaport agents config apply %s -f agent.yaml\n", ag.Name, ag.Name)
			return nil
		}
		c, err := configOf(p)
		if err != nil {
			return err
		}
		fmt.Fprintf(stdout, "# %s v%d, by %s at %s\n", ag.Name, p.Version, p.CreatedBy, p.CreatedAt.Local().Format("2006-01-02 15:04"))
		return writeConfigFile(stdout, c)
	case "versions":
		var out struct {
			Versions []agent.Version `json:"versions"`
		}
		if err := a.do(ctx, http.MethodGet, configPath(ag)+"/versions", nil, &out); err != nil {
			return err
		}
		if len(out.Versions) == 0 {
			fmt.Fprintf(stdout, "agent %s has no config yet: seaport agents config apply %s -f agent.yaml\n", ag.Name, ag.Name)
			return nil
		}
		w := tabwriter.NewWriter(stdout, 0, 4, 2, ' ', 0)
		fmt.Fprintln(w, "VERSION\tBY\tAT")
		for _, v := range out.Versions {
			cur := ""
			if v.Version == ag.ProfileVersion {
				cur = " (current)"
			}
			fmt.Fprintf(w, "v%d%s\t%s\t%s\n", v.Version, cur, v.CreatedBy, v.CreatedAt.Local().Format("2006-01-02 15:04"))
		}
		return w.Flush()
	case "apply":
		fs := flag.NewFlagSet("agents config apply", flag.ContinueOnError)
		file := fs.String("f", "", "config file (YAML or JSON; - for stdin; the format of config show)")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		if *file == "" {
			return errx.Validation("usage: seaport agents config apply <agent> -f agent.yaml")
		}
		raw, err := readInput(*file)
		if err != nil {
			return err
		}
		c, err := readConfigFile(raw)
		if err != nil {
			return err
		}
		// machine.home: a dir (relative to the file) is uploaded first
		if c.Machine != nil && c.Machine.Home != "" && !hosting.ValidHomeDigest(c.Machine.Home) {
			if c.Harness == "" {
				return errx.Validation("config file: a machine home needs the harness")
			}
			dir := expandHome(c.Machine.Home)
			if !filepath.IsAbs(dir) && *file != "-" {
				dir = filepath.Join(filepath.Dir(*file), dir)
			}
			h, err := pushHome(ctx, a, c.Harness, dir, false, stdout)
			if err != nil {
				return err
			}
			c.Machine.Home = h.Digest
		}
		_, err = saveConfig(ctx, a, ag, ag.ProfileVersion, c)
		return err
	}
	return nil
}

// buildAgent starts a build of the agent's machine and, with wait, prints
// its log as it grows until it ends (a failed build is an error).
func buildAgent(ctx context.Context, a *admin, ag agent.Agent, force, wait bool) error {
	path := "/api/v1/agents/" + ag.ID.String() + "/build"
	var b hosting.Build
	if err := a.do(ctx, http.MethodPost, path, map[string]any{"force": force}, &b); err != nil {
		return err
	}
	if b.State == hosting.BuildReady && !force {
		fmt.Fprintf(stdout, "%s's machine is built: %s (seaport agents build %s -force builds it again)\n", ag.Name, b.Image, ag.Name)
		return nil
	}
	if !wait {
		fmt.Fprintf(stdout, "building %s's machine FROM %s\n", ag.Name, b.Base)
		return nil
	}
	printed := 0
	for {
		if err := a.do(ctx, http.MethodGet, path, nil, &b); err != nil {
			return err
		}
		// the stored log is a tail: print what is new (all of it if it rolled)
		if len(b.Log) < printed {
			printed = 0
		}
		fmt.Fprint(stdout, b.Log[printed:])
		printed = len(b.Log)
		switch b.State {
		case hosting.BuildReady:
			fmt.Fprintf(stdout, "\n%s's machine built: %s\n", ag.Name, b.Image)
			return nil
		case hosting.BuildFailed:
			return errx.Business("build of " + ag.Name + "'s machine failed: " + b.Detail)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(2 * time.Second):
		}
		b.Log = ""
	}
}
