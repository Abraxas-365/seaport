package hosting_test

import (
	"reflect"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Abraxas-365/seaport/internal/hosting"
	"github.com/Abraxas-365/seaport/internal/identity"
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
		"egress stars":    func(s *hosting.Spec) { s.Network.Egress = []string{"**"} },
		"egress star tld": func(s *hosting.Spec) { s.Network.Egress = []string{"*.*"} },
		"size":            func(s *hosting.Spec) { s.Resources = hosting.Resources{Size: "XL"} },
		"size and custom": func(s *hosting.Spec) { s.Resources = hosting.Resources{Size: "S", CPUs: 3, MemoryMB: 1024} },
		"cpus":            func(s *hosting.Spec) { s.Resources = hosting.Resources{CPUs: 0.1, MemoryMB: 1024} },
		"workspace":       func(s *hosting.Spec) { s.Workspace = "tmpfs" },
		"repo config":     func(s *hosting.Spec) { s.RepoConfig = "maybe" },
		"idle":            func(s *hosting.Spec) { s.IdleStop = "30s" },
		"setup nul":       func(s *hosting.Spec) { s.Setup = "a\x00b" },
		"model key":       func(s *hosting.Spec) { s.Models = hosting.Models{"anthropic": "sk-ant-literal"} },
		"model provider":  func(s *hosting.Spec) { s.Models = hosting.Models{"mistral": "${KEY}"} },
		"env name":        func(s *hosting.Spec) { s.Env = map[string]string{"lower": "x"} },
		"env seaport":     func(s *hosting.Spec) { s.Env = map[string]string{"SEAPORT_URL": "x"} },
		"env slot":        func(s *hosting.Spec) { s.Env = map[string]string{"OPENAI_API_KEY": "x"} },
		"env proxy":       func(s *hosting.Spec) { s.Env = map[string]string{"HTTPS_PROXY": "x"} },
		"env line":        func(s *hosting.Spec) { s.Env = map[string]string{"A": "x\ny"} },
		"env long":        func(s *hosting.Spec) { s.Env = map[string]string{"A": strings.Repeat("x", hosting.MaxEnvValue+1)} },
		"env secret":      func(s *hosting.Spec) { s.Env = map[string]string{"TOKEN": "ghp_" + strings.Repeat("a", 36)} },
		"env many": func(s *hosting.Spec) {
			s.Env = map[string]string{}
			for i := 0; i <= hosting.MaxEnv; i++ {
				s.Env["V_"+strconv.Itoa(i)] = "x"
			}
		},
		"browser engine": func(s *hosting.Spec) { s.Browser.Engine = "firefox" },
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
		Repos: []hosting.Repo{{URL: "https://github.com/acme/app.git", Path: "app"}}, Models: hosting.Models{}, Env: map[string]string{},
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
	n := hosting.Normalize(hosting.Spec{Models: hosting.Models{"OpenAI": " ${K} ", "groq": ""}, Env: map[string]string{" TZ ": "UTC"}})
	if !reflect.DeepEqual(n.Models, hosting.Models{"openai": "${K}"}) || !reflect.DeepEqual(n.Env, map[string]string{"TZ": "UTC"}) {
		t.Fatalf("models/env: %v %v", n.Models, n.Env)
	}
}

func TestProvidersAndEnvironmentSecrets(t *testing.T) {
	s := hosting.Spec{Harness: hosting.HarnessRness, Env: map[string]string{"TZ": "America/Lima", "NODE_ENV": "production", "EMPTY": ""},
		Models: hosting.Models{"openai": "${OPENAI}", "anthropic": "${ANTHROPIC}", "groq": "${OPENAI}"}}
	if err := s.Validate(); err != nil {
		t.Fatal(err)
	}
	if got := s.Models.Providers(); !reflect.DeepEqual(got, []string{"anthropic", "groq", "openai"}) {
		t.Fatalf("providers: %v", got)
	}
	if got := s.Secrets(); !reflect.DeepEqual(got, []string{"ANTHROPIC", "OPENAI"}) {
		t.Fatalf("secrets: %v", got)
	}
	if s.SignsIn() {
		t.Fatal("rness with model keys signs in")
	}
	for _, name := range hosting.ProviderNames() {
		p := hosting.Providers[name]
		if p.Name != name || p.KeyEnv == "" || p.BaseEnv == "" || !strings.HasPrefix(p.Upstream, "https://") ||
			(p.Protocol != hosting.ProtocolAnthropic && p.Protocol != hosting.ProtocolOpenAI) {
			t.Errorf("provider %s: %+v", name, p)
		}
		if err := (hosting.KeyRequest{Provider: name, Caller: hosting.SlotCaller{Agent: identity.NewAgentID(), Slot: 1}}).Validate(); err != nil {
			t.Errorf("key request %s: %v", name, err)
		}
	}
	if err := (hosting.KeyRequest{Provider: "mistral", Caller: hosting.SlotCaller{Agent: identity.NewAgentID(), Slot: 1}}).Validate(); err == nil {
		t.Fatal("unknown provider accepted")
	}
	if got := hosting.GatewayURL("https://port.example.com/", "openai"); got != "https://port.example.com/gateway/openai" {
		t.Fatalf("gateway url: %q", got)
	}
	slot := hosting.SlotSet()
	for _, v := range []string{"SEAPORT_GATEWAY", "ANTHROPIC_API_KEY", "OPENAI_BASE_URL", "HTTPS_PROXY", "no_proxy"} {
		if !slices.Contains(slot, v) {
			t.Errorf("slot set lacks %s", v)
		}
	}
	for name, in := range map[string]hosting.SetEnvironmentSecret{
		"ok":      {Name: "OPENAI", Value: "sk-" + strings.Repeat("x", 40)},
		"env":     {Name: "DATABASE_URL", Value: "postgres://x", Env: true},
		"lower":   {Name: "openai", Value: "x"},
		"seaport": {Name: "SEAPORT_TOKEN", Value: "x"},
		"empty":   {Name: "OPENAI", Value: ""},
		"nul":     {Name: "OPENAI", Value: "a\x00b"},
		// A slot's network variable can't be an environment variable…
		"slot env": {Name: "ANTHROPIC_BASE_URL", Value: "https://x.example.com", Env: true},
		// …but a secret named like one, read only through ${NAME}, can.
		"slot name": {Name: "ANTHROPIC_API_KEY", Value: "x"},
	} {
		err := in.Validate()
		if want := name == "ok" || name == "env" || name == "slot name"; (err == nil) != want {
			t.Errorf("%s: %v", name, err)
		}
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
		Models:  hosting.Models{"anthropic": "${ANTHROPIC_KEY}"},
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
	for _, host := range []string{"html.duckduckgo.com", "example.org", "a.b.c.example.net"} {
		if !hosting.AllowedHost(host, []string{hosting.AnyHost}) {
			t.Errorf("AnyHost refused %q", host)
		}
	}
	open := hosting.Spec{Harness: hosting.HarnessRness, Network: hosting.Network{Egress: []string{" * "}}}
	if err := open.Validate(); err != nil {
		t.Fatalf("egress *: %v", err)
	}
	if got := hosting.Normalize(open).Network.Egress; !reflect.DeepEqual(got, []string{"*"}) {
		t.Fatalf("normalized egress *: %v", got)
	}
}

func TestBrowserInTheSpecAndTheImage(t *testing.T) {
	s := hosting.Spec{Harness: hosting.HarnessRness, Browser: hosting.Browser{Engine: " chromium "}}
	if err := s.Validate(); err != nil {
		t.Fatalf("chromium: %v", err)
	}
	n := hosting.Normalize(s)
	if n.Browser.Engine != hosting.BrowserChromium || !n.Browser.Enabled() {
		t.Fatalf("normalized browser %+v", n.Browser)
	}
	if (hosting.Browser{}).Enabled() {
		t.Fatal("no engine is enabled")
	}
	with, without := hosting.Dockerfile(n, "base:1"), hosting.Dockerfile(hosting.Spec{Harness: hosting.HarnessRness}, "base:1")
	if !strings.Contains(with, "chromium") || !strings.Contains(with, " xvfb ") || !strings.Contains(with, hosting.BrowserNode) || !strings.Contains(with, hosting.BrowserMCPPkg) {
		t.Fatalf("browser stage missing:\n%s", with)
	}
	if strings.Contains(without, "chromium") {
		t.Fatalf("browser stage without a browser:\n%s", without)
	}
	if !strings.HasSuffix(strings.TrimSpace(with), "USER 1000:1000") {
		t.Fatalf("the image must end as the agent user:\n%s", with)
	}
	a := hosting.BuildRequest{Spec: n, Base: "base:1"}
	b := hosting.BuildRequest{Spec: hosting.Spec{Harness: hosting.HarnessRness}, Base: "base:1"}
	if a.Tag() == b.Tag() {
		t.Fatal("a browser must change the image tag")
	}
}
