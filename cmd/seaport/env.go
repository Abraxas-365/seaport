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

// env is `seaport env [list|apply|export|versions|build|delete] ...`.
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
			fmt.Println("no environments yet: seaport env apply -f env.yaml")
			return nil
		}
		w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
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
			raw, err = io.ReadAll(os.Stdin)
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
			h, err := pushHome(ctx, a, f.Harness, dir, false, os.Stdout)
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
		fmt.Printf("# %s v%d, by %s at %s\n", e.Name, e.Version, e.CreatedBy, e.CreatedAt.Local().Format("2006-01-02 15:04"))
		return writeEnvFile(os.Stdout, envFile{Name: e.Name, Spec: e.Spec})
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
		w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
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
		fmt.Println("deleted environment", args[0])
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
		fmt.Printf("created environment %s v%d\n", e.Name, e.Version)
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
		fmt.Printf("environment %s unchanged (v%d)\n", e.Name, e.Version)
	} else {
		fmt.Printf("environment %s v%d\n", e.Name, e.Version)
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
		fmt.Printf("%s v%d is built: %s (seaport env build %s -force builds it again)\n", name, b.Version, b.Image, name)
		return nil
	}
	if !wait {
		fmt.Printf("building %s v%d FROM %s\n", name, b.Version, b.Base)
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
		fmt.Print(b.Log[printed:])
		printed = len(b.Log)
		switch b.State {
		case hosting.BuildReady:
			fmt.Printf("\n%s v%d built: %s\n", name, b.Version, b.Image)
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
