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
	"path/filepath"
	"strconv"
	"text/tabwriter"
	"time"

	"github.com/Abraxas-365/seaport/internal/errx"
	"github.com/Abraxas-365/seaport/internal/hosting"
	"github.com/Abraxas-365/seaport/internal/query"
	"go.yaml.in/yaml/v3"
)

// envFile is an environment as `seaport env apply -f` reads it and
// `seaport env export` writes it: its name and its spec's fields.
type envFile struct {
	Name string `json:"name"`
	hosting.Spec
}

// readEnvFile parses YAML (or JSON) strictly: an unknown key is an error,
// so a typo doesn't silently drop a setting.
func readEnvFile(raw []byte) (envFile, error) {
	var tree any
	if err := yaml.Unmarshal(raw, &tree); err != nil {
		return envFile{}, errx.Validation("environment file: " + err.Error())
	}
	if _, ok := tree.(map[string]any); !ok {
		return envFile{}, errx.Validation("environment file: expected a mapping (name: …, harness: …)")
	}
	js, err := json.Marshal(tree)
	if err != nil {
		return envFile{}, errx.Validation("environment file: " + err.Error())
	}
	dec := json.NewDecoder(bytes.NewReader(js))
	dec.DisallowUnknownFields()
	var f envFile
	if err := dec.Decode(&f); err != nil {
		return envFile{}, errx.Validation("environment file: " + err.Error())
	}
	if f.Name == "" {
		return envFile{}, errx.Validation("environment file: name is required")
	}
	return f, nil
}

// writeEnvFile prints f as block YAML in the spec's field order, leaving
// out empty values.
func writeEnvFile(w io.Writer, f envFile) error {
	if f.Resources.Size != "" { // the preset says it (the port resolves it)
		f.Resources.CPUs, f.Resources.MemoryMB = 0, 0
	}
	js, err := json.Marshal(f)
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
		return (n.Value == "" && n.Tag == "!!str") || ((n.Tag == "!!int" || n.Tag == "!!float") && n.Value == "0")
	}
	return false
}

// env is `seaport env [list|apply|export|versions|build|home|secrets|delete] ...`.
func env(ctx context.Context, args []string) error {
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
		var out query.Paginated[hosting.Environment]
		if err := a.do(ctx, http.MethodGet, "/api/v1/environments?limit=100", nil, &out); err != nil {
			return err
		}
		if len(out.Items) == 0 {
			fmt.Fprintln(stdout, "no environments yet: seaport env apply -f env.yaml")
			return nil
		}
		w := tabwriter.NewWriter(stdout, 0, 4, 2, ' ', 0)
		fmt.Fprintln(w, "NAME\tVERSION\tHARNESS\tSIZE\tREPOS\tAGENTS\tDESCRIPTION")
		for _, e := range out.Items {
			size := e.Spec.Resources.Size
			if size == "" {
				size = fmt.Sprintf("%gcpu/%dMB", e.Spec.Resources.CPUs, e.Spec.Resources.MemoryMB)
			}
			fmt.Fprintf(w, "%s\tv%d\t%s\t%s\t%d\t%d\t%s\n", e.Name, e.Version, e.Spec.Harness, size,
				len(e.Spec.Repos), e.Agents, e.Spec.Description)
		}
		return w.Flush()
	case "apply":
		fs := flag.NewFlagSet("env apply", flag.ContinueOnError)
		file := fs.String("f", "", "environment file (YAML or JSON; - for stdin)")
		if err := fs.Parse(args); err != nil {
			return err
		}
		if *file == "" {
			return errx.Validation("usage: seaport env apply -f env.yaml")
		}
		var raw []byte
		if *file == "-" {
			raw, err = io.ReadAll(stdin)
		} else {
			raw, err = os.ReadFile(*file)
		}
		if err != nil {
			return err
		}
		f, err := readEnvFile(raw)
		if err != nil {
			return err
		}
		// home: a dir (relative to the file) is uploaded first
		if f.Home != "" && !hosting.ValidHomeDigest(f.Home) {
			dir := expandHome(f.Home)
			if !filepath.IsAbs(dir) && *file != "-" {
				dir = filepath.Join(filepath.Dir(*file), dir)
			}
			h, err := pushHome(ctx, a, f.Harness, dir, false, stdout)
			if err != nil {
				return err
			}
			f.Home = h.Digest
		}
		return applyEnv(ctx, a, f)
	case "export", "show":
		fs := flag.NewFlagSet("env export", flag.ContinueOnError)
		version := fs.Int("version", 0, "a past version (default: current)")
		if len(args) == 0 {
			return errx.Validation("usage: seaport env export <env> [-version N]")
		}
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		path := "/api/v1/environments/" + url.PathEscape(args[0])
		if *version > 0 {
			path += "/versions/" + strconv.Itoa(*version)
		}
		var e hosting.Environment
		if err := a.do(ctx, http.MethodGet, path, nil, &e); err != nil {
			return err
		}
		fmt.Fprintf(stdout, "# %s v%d, by %s at %s\n", e.Name, e.Version, e.CreatedBy, e.CreatedAt.Local().Format("2006-01-02 15:04"))
		return writeEnvFile(stdout, envFile{Name: e.Name, Spec: e.Spec})
	case "versions":
		if len(args) != 1 {
			return errx.Validation("usage: seaport env versions <env>")
		}
		var out struct {
			Versions []hosting.Version `json:"versions"`
		}
		if err := a.do(ctx, http.MethodGet, "/api/v1/environments/"+url.PathEscape(args[0])+"/versions", nil, &out); err != nil {
			return err
		}
		w := tabwriter.NewWriter(stdout, 0, 4, 2, ' ', 0)
		fmt.Fprintln(w, "VERSION\tBY\tAT")
		for _, v := range out.Versions {
			fmt.Fprintf(w, "v%d\t%s\t%s\n", v.Version, v.CreatedBy, v.CreatedAt.Local().Format("2006-01-02 15:04"))
		}
		return w.Flush()
	case "build":
		fs := flag.NewFlagSet("env build", flag.ContinueOnError)
		version := fs.Int("version", 0, "a past version (default: current)")
		force := fs.Bool("force", false, "build again even when built (pulls the base anew)")
		wait := fs.Bool("wait", true, "follow the build until it ends")
		if len(args) == 0 {
			return errx.Validation("usage: seaport env build <env> [-version N] [-force] [-wait=false]")
		}
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		return buildEnv(ctx, a, args[0], *version, *force, *wait)
	case "home":
		return envHome(ctx, a, args)
	case "secrets":
		return envSecrets(ctx, a, args)
	case "delete":
		fs := flag.NewFlagSet("env delete", flag.ContinueOnError)
		yes := fs.Bool("yes", false, "confirm")
		if len(args) == 0 {
			return errx.Validation("usage: seaport env delete <env> -yes")
		}
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		if !*yes {
			return errx.Validation("deleting an environment drops its history; add -yes")
		}
		if err := a.do(ctx, http.MethodDelete, "/api/v1/environments/"+url.PathEscape(args[0]), nil, nil); err != nil {
			return err
		}
		fmt.Fprintln(stdout, "deleted environment", args[0])
		return nil
	}
	return errx.Validation("unknown env command " + sub + " (seaport help)")
}

// applyEnv creates the environment, or saves a new version of it from the
// current one (an unchanged spec makes none).
func applyEnv(ctx context.Context, a *admin, f envFile) error {
	var current hosting.Environment
	err := a.do(ctx, http.MethodGet, "/api/v1/environments/"+url.PathEscape(f.Name), nil, &current)
	if errx.IsType(err, errx.TypeNotFound) {
		var e hosting.Environment
		if err := a.do(ctx, http.MethodPost, "/api/v1/environments", map[string]any{"name": f.Name, "spec": f.Spec}, &e); err != nil {
			return err
		}
		fmt.Fprintf(stdout, "created environment %s v%d\n", e.Name, e.Version)
		return nil
	}
	if err != nil {
		return err
	}
	var e hosting.Environment
	if err := a.do(ctx, http.MethodPut, "/api/v1/environments/"+current.ID.String(),
		map[string]any{"base": current.Version, "spec": f.Spec}, &e); err != nil {
		return err
	}
	if e.Version == current.Version {
		fmt.Fprintf(stdout, "environment %s unchanged (v%d)\n", e.Name, e.Version)
	} else {
		fmt.Fprintf(stdout, "environment %s v%d\n", e.Name, e.Version)
	}
	return nil
}

// buildEnv starts a build and, with wait, prints its log as it grows until
// it ends (a failed build is an error).
func buildEnv(ctx context.Context, a *admin, name string, version int, force, wait bool) error {
	path := "/api/v1/environments/" + url.PathEscape(name)
	var b hosting.Build
	if err := a.do(ctx, http.MethodPost, path+"/build", map[string]any{"version": version, "force": force}, &b); err != nil {
		return err
	}
	if b.State == hosting.BuildReady && !force {
		fmt.Fprintf(stdout, "%s v%d is built: %s (seaport env build %s -force builds it again)\n", name, b.Version, b.Image, name)
		return nil
	}
	if !wait {
		fmt.Fprintf(stdout, "building %s v%d FROM %s\n", name, b.Version, b.Base)
		return nil
	}
	path += "/versions/" + strconv.Itoa(b.Version) + "/build"
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
			fmt.Fprintf(stdout, "\n%s v%d built: %s\n", name, b.Version, b.Image)
			return nil
		case hosting.BuildFailed:
			return errx.Business("build of " + name + " v" + strconv.Itoa(b.Version) + " failed: " + b.Detail)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(2 * time.Second):
		}
		b.Log = ""
	}
}

// envSecrets is `seaport env secrets [list|set|rm] <env> ...`: the
// environment's own secrets (model keys, repo credentials, with -env a
// variable of every slot); values come from stdin, never the command line.
func envSecrets(ctx context.Context, a *admin, args []string) error {
	sub := "list"
	if len(args) > 0 && (args[0] == "list" || args[0] == "set" || args[0] == "rm") {
		sub, args = args[0], args[1:]
	}
	if len(args) == 0 {
		return errx.Validation("usage: seaport env secrets [list|set|rm] <env> ...")
	}
	name := args[0]
	base := "/api/v1/environments/" + url.PathEscape(name) + "/secrets"
	switch sub {
	case "list":
		var out struct {
			Secrets []hosting.EnvironmentSecret `json:"secrets"`
		}
		if err := a.do(ctx, http.MethodGet, base, nil, &out); err != nil {
			return err
		}
		if len(out.Secrets) == 0 {
			fmt.Fprintf(stdout, "environment %s has no secrets: seaport env secrets set %s NAME [-env]\n", name, name)
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
			return errx.Validation("usage: seaport env secrets set <env> NAME [-env]  (value from stdin)")
		}
		fs := flag.NewFlagSet("env secrets set", flag.ContinueOnError)
		env := fs.Bool("env", false, "also give it to every slot's harness as an environment variable")
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
		fmt.Fprintf(stdout, "secret %s of environment %s saved; name it in the spec as ${%s}\n", args[1], name, args[1])
		return nil
	case "rm":
		if len(args) != 2 {
			return errx.Validation("usage: seaport env secrets rm <env> NAME")
		}
		if err := a.do(ctx, http.MethodDelete, base+"/"+url.PathEscape(args[1]), nil, nil); err != nil {
			return err
		}
		fmt.Fprintf(stdout, "secret %s of environment %s deleted\n", args[1], name)
		return nil
	}
	return nil
}
