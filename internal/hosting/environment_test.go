package hosting_test

import (
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/Abraxas-365/seaport/internal/hosting"
)

func TestSpecValidate(t *testing.T) {
	ok := hosting.Spec{Harness: hosting.HarnessClaudeCode, Image: "ghcr.io/acme/agent:1.2",
		Packages: []string{"jq", "ripgrep=13.0.0-4"}, Setup: "npm i -g pnpm",
		Repos: []hosting.Repo{{URL: "https://github.com/acme/app.git", Ref: "main", Credential: "${GITHUB_TOKEN}", OnStart: "pnpm i"},
			{URL: "https://github.com/acme/docs", Path: "ref/docs"}},
		Network:   hosting.Network{Egress: []string{"registry.npmjs.org", "*.atlassian.net"}},
		Resources: hosting.Resources{Size: hosting.SizeM}, IdleStop: "2h"}
	if err := ok.Validate(); err != nil {
		t.Fatalf("valid spec: %v", err)
	}
	bad := map[string]func(*hosting.Spec){
		"harness":        func(s *hosting.Spec) { s.Harness = "codex" },
		"image":          func(s *hosting.Spec) { s.Image = "Not An Image" },
		"package":        func(s *hosting.Spec) { s.Packages = []string{"jq; rm -rf /"} },
		"http repo":      func(s *hosting.Spec) { s.Repos = []hosting.Repo{{URL: "http://github.com/a/b"}} },
		"repo userinfo":  func(s *hosting.Spec) { s.Repos = []hosting.Repo{{URL: "https://x:y@github.com/a/b"}} },
		"repo ref":       func(s *hosting.Spec) { s.Repos = []hosting.Repo{{URL: "https://github.com/a/b", Ref: "../x"}} },
		"repo path dots": func(s *hosting.Spec) { s.Repos = []hosting.Repo{{URL: "https://github.com/a/b", Path: "../etc"}} },
		"repo path wt":   func(s *hosting.Spec) { s.Repos = []hosting.Repo{{URL: "https://github.com/a/b", Path: "wt"}} },
		"same path": func(s *hosting.Spec) {
			s.Repos = []hosting.Repo{{URL: "https://github.com/a/b"}, {URL: "https://gitlab.com/c/b.git"}}
		},
		"credential": func(s *hosting.Spec) {
			s.Repos = []hosting.Repo{{URL: "https://github.com/a/b", Credential: "ghp_literal"}}
		},
		"egress":          func(s *hosting.Spec) { s.Network.Egress = []string{"localhost"} },
		"egress url":      func(s *hosting.Spec) { s.Network.Egress = []string{"https://example.com"} },
		"size":            func(s *hosting.Spec) { s.Resources = hosting.Resources{Size: "XL"} },
		"size and custom": func(s *hosting.Spec) { s.Resources = hosting.Resources{Size: "S", CPUs: 3, MemoryMB: 1024} },
		"cpus":            func(s *hosting.Spec) { s.Resources = hosting.Resources{CPUs: 0.1, MemoryMB: 1024} },
		"workspace":       func(s *hosting.Spec) { s.Workspace = "tmpfs" },
		"repo config":     func(s *hosting.Spec) { s.RepoConfig = "maybe" },
		"idle":            func(s *hosting.Spec) { s.IdleStop = "30s" },
		"setup nul":       func(s *hosting.Spec) { s.Setup = "a\x00b" },
		"model key":       func(s *hosting.Spec) { s.Models.Anthropic = "sk-ant-literal" },
	}
	for name, mutate := range bad {
		s := ok
		s.Repos = append([]hosting.Repo(nil), ok.Repos...)
		mutate(&s)
		if err := s.Validate(); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestNormalize(t *testing.T) {
	in := hosting.Spec{Harness: " rness ", Description: "  PM  ", Setup: "  echo hi \n",
		Packages: []string{"jq", " jq "},
		Repos:    []hosting.Repo{{URL: "https://github.com/acme/app.git"}},
		Network:  hosting.Network{Egress: []string{"Example.com", "example.com"}}}
	got := hosting.Normalize(in)
	want := hosting.Spec{Harness: "rness", Description: "PM", Setup: "echo hi\n", Packages: []string{"jq"},
		Repos:      []hosting.Repo{{URL: "https://github.com/acme/app.git", Path: "app"}},
		RepoConfig: hosting.RepoConfigTrust, Workspace: hosting.WorkspacePersistent,
		Network:   hosting.Network{Egress: []string{"example.com"}},
		Resources: hosting.Resources{Size: "S", CPUs: 1, MemoryMB: 2048}, IdleStop: "30m"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("normalize:\n got %+v\nwant %+v", got, want)
	}
	if again := hosting.Normalize(got); !reflect.DeepEqual(again, got) {
		t.Fatalf("normalize is not idempotent: %+v", again)
	}
	if n := hosting.Normalize(hosting.Spec{Harness: "rness", IdleStop: "90m"}); n.IdleStop != "1h30m" {
		t.Fatalf("idle stop: %q", n.IdleStop)
	}
	if n := hosting.Normalize(hosting.Spec{Harness: "rness", IdleStop: "never"}); n.IdleStop != "0" {
		t.Fatalf("never: %q", n.IdleStop)
	}
}

func TestIdleStopAndLimits(t *testing.T) {
	if d, _ := hosting.ParseIdleStop(""); d != hosting.DefaultIdleStop {
		t.Fatalf("default idle stop: %v", d)
	}
	if d, err := hosting.ParseIdleStop("1h30m"); err != nil || d != 90*time.Minute {
		t.Fatalf("1h30m: %v %v", d, err)
	}
	l := hosting.Limits{MaxCPUs: 2, MaxMemoryMB: 4096}
	if err := l.Check(hosting.Resources{CPUs: 4, MemoryMB: 1024}); err == nil || !strings.Contains(err.Error(), "maximum") {
		t.Fatalf("cpus above the maximum: %v", err)
	}
	if err := l.Check(hosting.Resources{CPUs: 2, MemoryMB: 4096}); err != nil {
		t.Fatalf("at the maximum: %v", err)
	}
	s := hosting.Spec{Repos: []hosting.Repo{{URL: "https://a.com/x", Credential: "${A}"}, {URL: "https://a.com/y", Credential: "${A}"}, {URL: "https://a.com/z"}}}
	if got := s.Secrets(); !reflect.DeepEqual(got, []string{"A"}) {
		t.Fatalf("secrets: %v", got)
	}
	if h := (hosting.Repo{URL: "https://GitHub.com/a/b"}).Host(); h != "github.com" {
		t.Fatalf("host: %q", h)
	}
}

func TestNetworkOfASpec(t *testing.T) {
	s := hosting.Spec{Harness: hosting.HarnessRness, Repos: []hosting.Repo{{URL: "https://github.com/a/b", Credential: "${GH}"}},
		Models:  hosting.Models{Anthropic: "${ANTHROPIC_KEY}"},
		Network: hosting.Network{Egress: []string{"registry.npmjs.org", "github.com"}}}
	if err := s.Validate(); err != nil {
		t.Fatal(err)
	}
	if got := s.Secrets(); !reflect.DeepEqual(got, []string{"GH", "ANTHROPIC_KEY"}) {
		t.Fatalf("secrets: %v", got)
	}
	if got := s.Models.KeyName(hosting.ProviderAnthropic); got != "ANTHROPIC_KEY" {
		t.Fatalf("key name: %q", got)
	}
	if got := s.EgressHosts(); !reflect.DeepEqual(got, []string{"registry.npmjs.org", "github.com"}) {
		t.Fatalf("egress: %v", got)
	}
	allow := []string{"api.example.com", "*.acme.dev"}
	for host, want := range map[string]bool{"api.example.com": true, "API.Example.com.": true, "x.acme.dev": true,
		"a.b.acme.dev": true, "acme.dev": false, "evilacme.dev": false, "example.com": false} {
		if got := hosting.AllowedHost(host, allow); got != want {
			t.Errorf("AllowedHost(%q) = %v", host, got)
		}
	}
}
