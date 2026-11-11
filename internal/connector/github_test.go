package connector

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/Abraxas-365/seaport/internal/dock"
	"github.com/Abraxas-365/seaport/internal/hosting"
	"github.com/Abraxas-365/seaport/internal/ship"
)

func TestParseRemote(t *testing.T) {
	cases := map[string][2]string{
		"https://github.com/acme/web.git":         {"github.com", "acme/web"},
		"https://github.com/acme/web":             {"github.com", "acme/web"},
		"https://x-access-token@github.com/a/b/":  {"github.com", "a/b"},
		"git@github.com:acme/web.git":             {"github.com", "acme/web"},
		"ssh://git@ghe.example.test/acme/web.git": {"ghe.example.test", "acme/web"},
	}
	for in, want := range cases {
		host, repo, ok := parseRemote(in)
		if !ok || host != want[0] || repo != want[1] {
			t.Errorf("%s → %s %s %v", in, host, repo, ok)
		}
	}
	for _, in := range []string{"", "/local/path", "https://github.com/acme", "https://github.com/a/b/c", "https://github.com/acme/.."} {
		if _, _, ok := parseRemote(in); ok {
			t.Errorf("%q parsed", in)
		}
	}
}

func TestGHRepo(t *testing.T) {
	t.Setenv("GH_REPO", "")
	ctx := context.Background()
	cases := []struct {
		args       []string
		host, repo string
	}{
		{[]string{"pr", "view", "7", "-R", "acme/web"}, "", "acme/web"},
		{[]string{"pr", "view", "--repo=acme/web"}, "", "acme/web"},
		{[]string{"pr", "review", "-Racme/web", "--approve"}, "", "acme/web"},
		{[]string{"issue", "list", "--repo", "ghe.example.test/acme/web"}, "ghe.example.test", "acme/web"},
		{[]string{"api", "repos/acme/web/pulls/7/comments", "-f", "body=x"}, "", "acme/web"},
		{[]string{"api", "/repos/acme/web?per_page=1"}, "", "acme/web"},
	}
	for _, c := range cases {
		host, repo, ok := ghRepo(ctx, c.args)
		if !ok || host != c.host || repo != c.repo {
			t.Errorf("%v → %q %q %v", c.args, host, repo, ok)
		}
	}
	t.Setenv("GH_REPO", "acme/env")
	if _, repo, ok := ghRepo(ctx, []string{"pr", "list"}); !ok || repo != "acme/env" {
		t.Errorf("GH_REPO → %q %v", repo, ok)
	}
}

// The gh shim runs the real gh with an App token for the repo the
// command names; a caller's own GH_TOKEN wins.
func TestGHShim(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell script fake gh")
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var in dock.GitHubTokenRequest
		_ = json.NewDecoder(r.Body).Decode(&in)
		if r.URL.Path != dock.GitHubTokenPath || r.Header.Get("Authorization") != "Bearer sp_crew_slot" || in.Repository != "acme/web" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(dock.GitHubToken{Token: "ghs_web", Repository: in.Repository, Host: "github.com"})
	}))
	defer srv.Close()
	dir := t.TempDir()
	out := filepath.Join(dir, "out")
	fake := filepath.Join(dir, "gh")
	script := "#!/bin/sh\necho \"$GH_TOKEN|$*\" > " + out + "\n"
	if err := os.WriteFile(fake, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SEAPORT_GH", fake)
	t.Setenv(hosting.EnvURL, srv.URL)
	t.Setenv(hosting.EnvToken, "sp_crew_slot")
	t.Setenv("GH_TOKEN", "")
	t.Setenv("GITHUB_TOKEN", "")
	t.Setenv("GH_REPO", "")
	read := func() string {
		raw, _ := os.ReadFile(out)
		return strings.TrimSpace(string(raw))
	}
	if err := GH(context.Background(), []string{"pr", "comment", "7", "-R", "acme/web", "-b", "hi"}); err != nil {
		t.Fatal(err)
	}
	if got := read(); got != "ghs_web|pr comment 7 -R acme/web -b hi" {
		t.Fatalf("shim ran %q", got)
	}
	t.Setenv("GH_TOKEN", "mine")
	if err := GH(context.Background(), []string{"pr", "list", "-R", "acme/web"}); err != nil {
		t.Fatal(err)
	}
	if got := read(); got != "mine|pr list -R acme/web" {
		t.Fatalf("own token: %q", got)
	}
}

func TestPromptGitHubHint(t *testing.T) {
	var d ship.Delivery
	d.Conversation.Channel = dock.ChannelGitHub
	d.Conversation.ExternalID = "acme/web#7"
	got := Prompt(d, false, nil)
	if !strings.Contains(got, "repository acme/web, issue/PR #7") || !strings.Contains(got, "gh pr view 7 -R acme/web") {
		t.Fatalf("prompt = %s", got)
	}
	d.Conversation.Channel = dock.ChannelAPI
	if strings.Contains(Prompt(d, false, nil), "gh pr") {
		t.Fatal("hint outside GitHub")
	}
}
