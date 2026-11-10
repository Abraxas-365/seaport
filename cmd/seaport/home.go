package main

import (
	"archive/tar"
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"text/tabwriter"

	"github.com/Abraxas-365/seaport/internal/errx"
	"github.com/Abraxas-365/seaport/internal/hosting"
)

// envHome is `seaport env home push|show|pull ...`: an environment's
// harness home (its ~/.claude or ~/.rness config, baked into the image).
func envHome(ctx context.Context, a *admin, args []string) error {
	if len(args) < 2 {
		return errx.Validation("usage: seaport env home push|show|pull <env> ...")
	}
	sub, name, rest := args[0], args[1], args[2:]
	e, err := getEnv(ctx, a, name)
	if err != nil {
		return err
	}
	switch sub {
	case "push":
		fs := flag.NewFlagSet("env home push", flag.ContinueOnError)
		dry := fs.Bool("dry-run", false, "list what would be uploaded, upload nothing")
		dir, err := oneDir(fs, rest, "~/"+hosting.HomeRoot(e.Spec.Harness))
		if err != nil {
			return err
		}
		h, err := pushHome(ctx, a, e.Spec.Harness, dir, *dry, os.Stdout)
		if err != nil || *dry {
			return err
		}
		f := envFile{Name: e.Name, Spec: e.Spec}
		f.Home = h.Digest
		return applyEnv(ctx, a, f)
	case "show":
		if e.Spec.Home == "" {
			fmt.Printf("%s v%d has no home (seaport env home push %s [dir])\n", e.Name, e.Version, e.Name)
			return nil
		}
		var h hosting.Home
		if err := a.do(ctx, http.MethodGet, "/api/v1/homes/"+url.PathEscape(e.Spec.Home), nil, &h); err != nil {
			return err
		}
		fmt.Printf("# %s v%d: %s home %s, %d files, %s, by %s\n", e.Name, e.Version, h.Harness, h.Digest, len(h.Files),
			bytesize(h.Size), h.CreatedBy)
		w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
		for _, f := range h.Files {
			x := ""
			if f.Exec {
				x = "x"
			}
			fmt.Fprintf(w, "%s\t%s\t%s\n", f.Path, bytesize(f.Size), x)
		}
		return w.Flush()
	case "pull":
		fs := flag.NewFlagSet("env home pull", flag.ContinueOnError)
		dir, err := oneDir(fs, rest, "")
		if err != nil {
			return err
		}
		if dir == "" {
			return errx.Validation("usage: seaport env home pull <env> <dir> (a new or empty dir)")
		}
		if e.Spec.Home == "" {
			return errx.Validation(e.Name + " has no home")
		}
		raw, err := a.send(ctx, http.MethodGet, "/api/v1/homes/"+url.PathEscape(e.Spec.Home)+"/archive", "", nil)
		if err != nil {
			return err
		}
		n, err := extractHome(raw, e.Spec.Harness, dir)
		if err != nil {
			return err
		}
		fmt.Printf("%d files of %s's home in %s\n", n, e.Name, dir)
		return nil
	}
	return errx.Validation("unknown env home command " + sub + " (push, show, pull)")
}

func getEnv(ctx context.Context, a *admin, name string) (hosting.Environment, error) {
	var e hosting.Environment
	err := a.do(ctx, http.MethodGet, "/api/v1/environments/"+url.PathEscape(name), nil, &e)
	return e, err
}

// oneDir parses flags and an optional dir argument (def when missing),
// with ~ expanded.
func oneDir(fs *flag.FlagSet, args []string, def string) (string, error) {
	dir := def
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		dir, args = args[0], args[1:]
	}
	if err := fs.Parse(args); err != nil {
		return "", err
	}
	if fs.NArg() > 0 {
		return "", errx.Validation("unexpected argument " + fs.Arg(0))
	}
	return expandHome(dir), nil
}

func expandHome(p string) string {
	if rest, ok := strings.CutPrefix(p, "~/"); ok {
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, rest)
		}
	}
	return p
}

// pushHome packs dir's harness config (hosting.HomePath: what isn't is
// skipped and listed), checks it like the port does (hosting.ReadHome),
// and uploads it (unless dry).
func pushHome(ctx context.Context, a *admin, harness, dir string, dry bool, out io.Writer) (hosting.Home, error) {
	tarball, skipped, err := packHome(harness, dir)
	if err != nil {
		return hosting.Home{}, err
	}
	b, err := hosting.ReadHome(harness, tarball)
	if err != nil {
		return hosting.Home{}, err
	}
	if len(skipped) > 0 {
		fmt.Fprintf(out, "left out (not %s config): %s\n", harness, strings.Join(skipped, ", "))
	}
	if dry {
		for _, f := range b.Files {
			fmt.Fprintf(out, "  %s (%s)\n", f.Path, bytesize(f.Size))
		}
		fmt.Fprintf(out, "%d files, %s: %s (dry run, nothing uploaded)\n", len(b.Files), bytesize(b.Size), b.Digest)
		return hosting.Home{Digest: b.Digest, Files: b.Files, Size: b.Size}, nil
	}
	raw, err := a.send(ctx, http.MethodPost, "/api/v1/homes?harness="+url.QueryEscape(harness), "application/x-tar", tarball)
	if err != nil {
		return hosting.Home{}, err
	}
	var h hosting.Home
	if err := json.Unmarshal(raw, &h); err != nil {
		return hosting.Home{}, errx.Wrap(err, "decode response", errx.TypeInternal)
	}
	fmt.Fprintf(out, "uploaded %s home from %s: %d files, %s (%s)\n", harness, dir, len(h.Files), bytesize(h.Size), h.Digest)
	return h, nil
}

// packHome tars the files under dir that a harness home may hold. Links
// are followed (dotfiles often link skills in), to MaxHomeDepth; what is
// skipped is listed by its top-level name.
func packHome(harness, dir string) ([]byte, []string, error) {
	if fi, err := os.Stat(dir); err != nil || !fi.IsDir() {
		return nil, nil, errx.Validation(dir + " is not a directory")
	}
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	skipped := map[string]bool{}
	var walk func(abs, rel string, depth int) error
	walk = func(abs, rel string, depth int) error {
		entries, err := os.ReadDir(abs)
		if err != nil {
			return errx.Validation("read " + abs + ": " + err.Error())
		}
		for _, d := range entries {
			r := path.Join(rel, d.Name())
			p := filepath.Join(abs, d.Name())
			fi, err := os.Stat(p) // follows links
			if err != nil {
				continue // a dangling link
			}
			if fi.IsDir() {
				if !hosting.HomeDir(harness, r) || depth >= hosting.MaxHomeDepth {
					skipped[strings.SplitN(r, "/", 2)[0]+"/"] = true
					continue
				}
				if err := walk(p, r, depth+1); err != nil {
					return err
				}
				continue
			}
			if !fi.Mode().IsRegular() || hosting.HomePath(harness, r) != nil {
				top := strings.SplitN(r, "/", 2)[0]
				if top != r {
					top += "/"
				}
				skipped[top] = true
				continue
			}
			if fi.Size() > hosting.MaxHomeFile {
				return errx.Validation(r + " is larger than " + bytesize(hosting.MaxHomeFile))
			}
			body, err := os.ReadFile(p)
			if err != nil {
				return errx.Validation("read " + p + ": " + err.Error())
			}
			if err := tw.WriteHeader(&tar.Header{Typeflag: tar.TypeReg, Name: r, Mode: int64(fi.Mode().Perm()), Size: int64(len(body))}); err != nil {
				return errx.Wrap(err, "pack the home", errx.TypeInternal)
			}
			if _, err := tw.Write(body); err != nil {
				return errx.Wrap(err, "pack the home", errx.TypeInternal)
			}
		}
		return nil
	}
	if err := walk(dir, "", 1); err != nil {
		return nil, nil, err
	}
	if err := tw.Close(); err != nil {
		return nil, nil, errx.Wrap(err, "pack the home", errx.TypeInternal)
	}
	list := make([]string, 0, len(skipped))
	for s := range skipped {
		list = append(list, s)
	}
	slices.Sort(list)
	return buf.Bytes(), list, nil
}

// extractHome writes a home's tar into dir (new or empty), checking each
// path again.
func extractHome(tarball []byte, harness, dir string) (int, error) {
	if entries, err := os.ReadDir(dir); err == nil && len(entries) > 0 {
		return 0, errx.Validation(dir + " is not empty")
	}
	tr := tar.NewReader(bytes.NewReader(tarball))
	n := 0
	for {
		h, err := tr.Next()
		if err == io.EOF {
			return n, nil
		}
		if err != nil {
			return n, errx.Wrap(err, "read the home", errx.TypeInternal)
		}
		if h.Typeflag != tar.TypeReg || hosting.HomePath(harness, h.Name) != nil {
			return n, errx.Validation("the home holds " + h.Name)
		}
		p := filepath.Join(dir, filepath.FromSlash(h.Name))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			return n, err
		}
		body, err := io.ReadAll(io.LimitReader(tr, hosting.MaxHomeFile+1))
		if err != nil {
			return n, err
		}
		if err := os.WriteFile(p, body, os.FileMode(h.Mode).Perm()); err != nil {
			return n, err
		}
		n++
	}
}

func bytesize(n int64) string {
	switch {
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MiB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.1f KiB", float64(n)/(1<<10))
	}
	return fmt.Sprintf("%d B", n)
}
