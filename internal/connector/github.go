package connector

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/Abraxas-365/seaport/internal/crew"
	"github.com/Abraxas-365/seaport/internal/dock"
	"github.com/Abraxas-365/seaport/internal/errx"
	"github.com/Abraxas-365/seaport/internal/hosting"
	"github.com/Abraxas-365/seaport/internal/identity"
	"github.com/Abraxas-365/seaport/internal/ship"
)

// GitHubToken asks the port (SEAPORT_URL, with the crew token in
// SEAPORT_CREW_TOKEN) for an App token for repo (owner/name): the port
// mints it when a GitHub dock of the agent has its App installed there.
// host, when set, is the GitHub host the caller means (another host:
// NotFound). Nothing is written to disk.
func GitHubToken(ctx context.Context, repo, host string) (dock.GitHubToken, error) {
	server := strings.TrimRight(os.Getenv(hosting.EnvURL), "/")
	token := os.Getenv(hosting.EnvToken)
	if server == "" || token == "" {
		return dock.GitHubToken{}, errx.Validation(hosting.EnvURL + " and " + hosting.EnvToken + " are required (set in hosted slots)")
	}
	in := dock.GitHubTokenRequest{Repository: repo, Host: host}
	if err := in.Validate(); err != nil {
		return dock.GitHubToken{}, err
	}
	raw, _ := json.Marshal(in)
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, server+dock.GitHubTokenPath, bytes.NewReader(raw))
	if err != nil {
		return dock.GitHubToken{}, errx.Wrap(err, "github token request", errx.TypeValidation)
	}
	req.Header = authHeader(token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return dock.GitHubToken{}, errx.Wrap(err, "ask the port for a GitHub token", errx.TypeExternal)
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return dock.GitHubToken{}, errx.Wrap(err, "read the port's GitHub token", errx.TypeExternal)
	}
	switch resp.StatusCode {
	case http.StatusOK:
	case http.StatusNotFound:
		return dock.GitHubToken{}, errx.NotFound(portMessage(body, resp.Status))
	case http.StatusUnauthorized, http.StatusForbidden:
		return dock.GitHubToken{}, errx.Unauthorized("the port refused a GitHub token: " + portMessage(body, resp.Status))
	default:
		return dock.GitHubToken{}, errx.External("GitHub token: " + portMessage(body, resp.Status))
	}
	var out dock.GitHubToken
	if err := json.Unmarshal(body, &out); err != nil || out.Token == "" {
		return dock.GitHubToken{}, errx.External("the port's GitHub token does not decode")
	}
	return out, nil
}

// GitHubTokenCommand is `seaport github-token [owner/repo]`: prints a
// token for the repo (default: the origin remote of the current
// directory's git repository), for GH_TOKEN=$(seaport github-token) gh ….
func GitHubTokenCommand(ctx context.Context, args []string, out io.Writer) error {
	repo, host := "", ""
	switch len(args) {
	case 0:
		var ok bool
		if host, repo, ok = remoteRepo(ctx, ""); !ok {
			return errx.Validation("usage: seaport github-token <owner/repo> (no GitHub origin remote here)")
		}
	case 1:
		repo = args[0]
	default:
		return errx.Validation("usage: seaport github-token [owner/repo]")
	}
	t, err := GitHubToken(ctx, repo, host)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintln(out, t.Token)
	return err
}

// parseRemote reads a git remote URL (https://host/o/r(.git),
// git@host:o/r.git, ssh://git@host/o/r) as its host and owner/name.
func parseRemote(raw string) (host, repo string, ok bool) {
	raw = strings.TrimSpace(raw)
	var path string
	if u, err := url.Parse(raw); err == nil && u.Scheme != "" && u.Host != "" {
		host, path = u.Hostname(), u.Path
	} else if at, rest, found := strings.Cut(raw, ":"); found && !strings.Contains(at, "/") {
		_, h, hasUser := strings.Cut(at, "@")
		if !hasUser {
			h = at
		}
		host, path = h, rest
	} else {
		return "", "", false
	}
	repo = strings.TrimSuffix(strings.Trim(path, "/"), ".git")
	if host == "" || !dock.ValidRepository(repo) {
		return "", "", false
	}
	return strings.ToLower(host), repo, true
}

// remoteRepo is the origin remote of the git repository at dir ("": the
// current directory).
func remoteRepo(ctx context.Context, dir string) (host, repo string, ok bool) {
	cmd := exec.CommandContext(ctx, "git", "remote", "get-url", "origin")
	cmd.Dir = dir
	raw, err := cmd.Output()
	if err != nil {
		return "", "", false
	}
	return parseRemote(string(raw))
}

// ghRepo finds the repository a gh command names: -R/--repo, GH_REPO, a
// `gh api repos/<owner>/<name>/…` path, else the current directory's
// origin remote.
func ghRepo(ctx context.Context, args []string) (host, repo string, ok bool) {
	for i, a := range args {
		if a == "--" {
			break
		}
		var v string
		switch {
		case a == "-R" || a == "--repo":
			if i+1 < len(args) {
				v = args[i+1]
			}
		case strings.HasPrefix(a, "--repo="):
			v = strings.TrimPrefix(a, "--repo=")
		case strings.HasPrefix(a, "-R") && len(a) > 2:
			v = strings.TrimPrefix(a[2:], "=")
		}
		if v != "" {
			return repoArg(v)
		}
	}
	if v := os.Getenv("GH_REPO"); v != "" {
		return repoArg(v)
	}
	if len(args) > 1 && args[0] == "api" {
		for _, a := range args[1:] {
			p := strings.TrimPrefix(a, "/")
			if rest, found := strings.CutPrefix(p, "repos/"); found {
				parts := strings.SplitN(rest, "/", 3)
				if len(parts) >= 2 {
					name, _, _ := strings.Cut(parts[1], "?")
					if r := parts[0] + "/" + name; dock.ValidRepository(r) {
						return "", r, true
					}
				}
			}
		}
	}
	return remoteRepo(ctx, "")
}

// repoArg reads gh's repository syntax: owner/name, host/owner/name or a URL.
func repoArg(v string) (host, repo string, ok bool) {
	if strings.Contains(v, "://") || strings.HasPrefix(v, "git@") {
		return parseRemote(v)
	}
	parts := strings.Split(strings.TrimSuffix(v, ".git"), "/")
	switch len(parts) {
	case 2:
		r := parts[0] + "/" + parts[1]
		return "", r, dock.ValidRepository(r)
	case 3:
		r := parts[1] + "/" + parts[2]
		return strings.ToLower(parts[0]), r, dock.ValidRepository(r)
	}
	return "", "", false
}

// GH is the `gh` shim of hosted slots (the seaport binary called as gh):
// it finds the repository the command is about, puts an App token for it
// in GH_TOKEN (GH_ENTERPRISE_TOKEN and GH_HOST on GitHub Enterprise) and
// runs the real gh. A caller's own GH_TOKEN wins; without a token gh runs
// as is (and says it is signed out).
func GH(ctx context.Context, args []string) error {
	real, err := realGH()
	if err != nil {
		return err
	}
	cmd := exec.CommandContext(ctx, real, args...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	cmd.Env = os.Environ()
	if os.Getenv("GH_TOKEN") == "" && os.Getenv("GITHUB_TOKEN") == "" && os.Getenv(hosting.EnvToken) != "" {
		if host, repo, ok := ghRepo(ctx, args); ok {
			t, err := GitHubToken(ctx, repo, host)
			switch {
			case err == nil:
				cmd.Env = append(cmd.Env, "GH_TOKEN="+t.Token, "GH_PROMPT_DISABLED=1")
				if t.Host != "" && t.Host != "github.com" {
					cmd.Env = append(cmd.Env, "GH_HOST="+t.Host, "GH_ENTERPRISE_TOKEN="+t.Token)
				}
			default:
				slog.Debug("no GitHub App token for gh", "repo", repo, "err", err)
				fmt.Fprintf(os.Stderr, "no GitHub App token from Seaport for %s: %s\n", repo, errMessage(err))
			}
		}
	}
	err = cmd.Run()
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		os.Exit(exit.ExitCode())
	}
	if err != nil {
		return errx.Wrap(err, "run gh", errx.TypeExternal)
	}
	return nil
}

// GHReal is where the base images put the real gh (the shim sits earlier
// in PATH, at /opt/seaport/bin/gh).
const GHReal = "/usr/local/bin/gh"

// realGH finds the gh binary that isn't this one: SEAPORT_GH, GHReal, or
// the next gh on PATH.
func realGH() (string, error) {
	self, _ := os.Executable()
	selfReal, _ := filepath.EvalSymlinks(self)
	isSelf := func(p string) bool {
		r, err := filepath.EvalSymlinks(p)
		return err == nil && (r == selfReal || r == self)
	}
	candidates := []string{os.Getenv("SEAPORT_GH"), GHReal}
	for _, dir := range filepath.SplitList(os.Getenv("PATH")) {
		candidates = append(candidates, filepath.Join(dir, "gh"))
	}
	for _, c := range candidates {
		if c == "" || isSelf(c) {
			continue
		}
		if fi, err := os.Stat(c); err == nil && !fi.IsDir() && fi.Mode()&0o111 != 0 {
			return c, nil
		}
	}
	return "", errx.NotFound("gh is not installed (no gh besides Seaport's shim on PATH)")
}

func errMessage(err error) string {
	var e *errx.Error
	if errors.As(err, &e) {
		return e.Message
	}
	return err.Error()
}

// githubHint tells the agent how to act on a GitHub conversation
// (owner/repo#N): its reply is a comment, and gh/git work as the App.
func githubHint(chat string) string {
	repo, num, ok := strings.Cut(chat, "#")
	if !ok || !dock.ValidRepository(repo) {
		return ""
	}
	return "GitHub: repository " + repo + ", issue/PR #" + num + ". Your reply is posted there as a comment. " +
		"For anything else on GitHub (reviews, line comments, labels, pushing a branch) use `gh` (e.g. `gh pr view " + num +
		" -R " + repo + " --comments` reads the whole thread, `gh pr review " + num + " -R " + repo + " --comment -b …`) and git:" +
		" both act as the port's GitHub App for this repository (`seaport github-token " + repo + "` prints its token). " +
		"A review you post already answers in the thread: don't repeat it in a reply (reply only when nothing else was posted, " +
		"or to add something new). In anything you post, link files with full URLs " +
		"(https://github.com/" + repo + "/blob/<ref>/<path>#L<n>): relative links break in comments. " +
		"To attach files (a QA report, screenshots), reply with `seaport reply <conversation_id> -file PATH... \"text\"`" +
		" instead of the reply tool: images show in the comment, other files are linked.\n"
}

// ReplyCommand is `seaport reply <conversation-id> [-file PATH]... [text]`:
// an agent's reply with files from disk (the MCP reply tool takes them as
// base64, which a model would have to write out), sent as the agent of
// SEAPORT_CREW_TOKEN to SEAPORT_URL.
func ReplyCommand(ctx context.Context, args []string, out io.Writer) error {
	const usage = "usage: seaport reply <conversation-id> [-file PATH]... [text]"
	if len(args) == 0 {
		return errx.Validation(usage)
	}
	in := crew.ReplyRequest{}
	conv, err := identity.ParseConversationID(args[0])
	if err != nil {
		return errx.Validation(usage + " (" + err.Error() + ")")
	}
	in.ConversationID = conv
	var words []string
	for i := 1; i < len(args); i++ {
		a := args[i]
		if a != "-file" && a != "--file" {
			words = append(words, a)
			continue
		}
		if i+1 == len(args) {
			return errx.Validation(usage)
		}
		i++
		data, err := os.ReadFile(args[i])
		if err != nil {
			return errx.Wrap(err, "read "+args[i], errx.TypeValidation)
		}
		in.Files = append(in.Files, ship.File{Name: filepath.Base(args[i]), Data: data})
	}
	in.Text = strings.Join(words, " ")
	if err := in.Validate(); err != nil {
		return err
	}
	server := strings.TrimRight(os.Getenv(hosting.EnvURL), "/")
	token := os.Getenv(hosting.EnvToken)
	if server == "" || token == "" {
		return errx.Validation(hosting.EnvURL + " and " + hosting.EnvToken + " are required (the connector sets them for its agent)")
	}
	raw, _ := json.Marshal(in)
	ctx, cancel := context.WithTimeout(ctx, 3*time.Minute)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, server+crew.ReplyPath, bytes.NewReader(raw))
	if err != nil {
		return errx.Wrap(err, "reply request", errx.TypeValidation)
	}
	req.Header = authHeader(token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return errx.Wrap(err, "send the reply to the port", errx.TypeExternal)
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusCreated {
		return errx.External("reply: " + portMessage(body, resp.Status))
	}
	var a crew.ReplyAnswer
	if err := json.Unmarshal(body, &a); err != nil {
		return errx.External("the port's answer does not decode")
	}
	_, err = fmt.Fprintf(out, "%s %s\n", a.ShipID, a.Status)
	return err
}
