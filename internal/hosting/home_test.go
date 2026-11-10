package hosting

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"strings"
	"testing"
	"time"
)

type entry struct {
	name, body string
	mode       int64
	typ        byte
}

func archive(t *testing.T, entries ...entry) []byte {
	t.Helper()
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	for _, e := range entries {
		typ := e.typ
		if typ == 0 {
			typ = tar.TypeReg
		}
		mode := e.mode
		if mode == 0 {
			mode = 0o600
		}
		h := &tar.Header{Name: e.name, Typeflag: typ, Mode: mode, Size: int64(len(e.body)), ModTime: time.Now(), Uid: 501}
		if typ != tar.TypeReg {
			h.Size = 0
			h.Linkname = "/etc/passwd"
		}
		if err := tw.WriteHeader(h); err != nil {
			t.Fatal(err)
		}
		if typ == tar.TypeReg {
			_, _ = tw.Write([]byte(e.body))
		}
	}
	_ = tw.Close()
	return buf.Bytes()
}

func TestHomePath(t *testing.T) {
	ok := map[string][]string{
		HarnessClaudeCode: {"CLAUDE.md", "settings.json", "agents/reviewer.md", "skills/go/SKILL.md", "commands/x.md",
			"hooks/pre.sh", "output-styles/terse.md", "statusline.sh", "keybindings.json"},
		HarnessRness: {"init.lua", "hooks.json", "lua/providers.lua", "plugins/memory.lua", "skills/go/SKILL.md"},
	}
	for h, paths := range ok {
		for _, p := range paths {
			if err := HomePath(h, p); err != nil {
				t.Errorf("%s %s: %v", h, p, err)
			}
		}
	}
	bad := map[string][]string{
		HarnessClaudeCode: {"", "/abs", "../x", "agents/../../x", "a//b", "projects/s.jsonl", ".credentials.json",
			"settings.local.json", "plugins/cache/x", "history.jsonl", "agents", "CLAUDE.md/x", "skills/.git/config",
			"skills/x/.DS_Store", "settings.json.bak", "agents/x\\y", "todos/a.json", "statsig/x"},
		HarnessRness: {"sessions/a.jsonl", "credentials.json", "memory/x.md", "lua/seaport.lua", "agents/x.md", "settings.json"},
		"codex":      {"init.lua"},
	}
	for h, paths := range bad {
		for _, p := range paths {
			if err := HomePath(h, p); err == nil {
				t.Errorf("%s %q: accepted", h, p)
			}
		}
	}
	if HomeDir(HarnessClaudeCode, "projects") || !HomeDir(HarnessClaudeCode, "skills/go") || HomeDir(HarnessRness, "skills/.git") {
		t.Fatal("HomeDir")
	}
}

// A home is normalized: the same files in another order, owner, mode and
// time (or gzipped) make the same digest; executables stay executable.
func TestReadHomeNormalizes(t *testing.T) {
	a := archive(t, entry{name: "skills/", typ: tar.TypeDir}, entry{name: "CLAUDE.md", body: "be terse"},
		entry{name: "hooks/pre.sh", body: "#!/bin/sh\n", mode: 0o700}, entry{name: "skills/go/SKILL.md", body: "go"})
	b := archive(t, entry{name: "./skills/go/SKILL.md", body: "go"}, entry{name: "hooks/pre.sh", body: "#!/bin/sh\n", mode: 0o755},
		entry{name: "CLAUDE.md", body: "be terse", mode: 0o644})
	var gz bytes.Buffer
	zw := gzip.NewWriter(&gz)
	_, _ = zw.Write(b)
	_ = zw.Close()
	ha, err := ReadHome(HarnessClaudeCode, a)
	if err != nil {
		t.Fatal(err)
	}
	hb, err := ReadHome(HarnessClaudeCode, gz.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	if ha.Digest != hb.Digest || !ValidHomeDigest(ha.Digest) || !bytes.Equal(ha.Tar, hb.Tar) {
		t.Fatalf("digests differ: %s %s", ha.Digest, hb.Digest)
	}
	if len(ha.Files) != 3 || ha.Files[0].Path != "CLAUDE.md" || !ha.Files[1].Exec || ha.Files[0].Exec || ha.Size != int64(len("be terse#!/bin/sh\ngo")) {
		t.Fatalf("files: %+v", ha.Files)
	}
	tr := tar.NewReader(bytes.NewReader(ha.Tar))
	for {
		h, err := tr.Next()
		if err != nil {
			break
		}
		if h.Uid != 1000 || h.Gid != 1000 || h.ModTime.Unix() != 0 || (h.Mode != 0o644 && h.Mode != 0o755) {
			t.Fatalf("header not normalized: %+v", h)
		}
	}
	if got, err := HomeFileData(ha.Tar, "skills/go/SKILL.md"); err != nil || string(got) != "go" {
		t.Fatalf("file: %q %v", got, err)
	}
	if _, err := HomeFileData(ha.Tar, "nope"); err == nil {
		t.Fatal("missing file found")
	}
}

func TestReadHomeRefuses(t *testing.T) {
	big := strings.Repeat("x", MaxHomeFile+1)
	for name, c := range map[string]struct {
		harness string
		archive []byte
		want    string
	}{
		"symlink":  {HarnessClaudeCode, archive(t, entry{name: "CLAUDE.md", typ: tar.TypeSymlink}), "regular files"},
		"hardlink": {HarnessClaudeCode, archive(t, entry{name: "CLAUDE.md", typ: tar.TypeLink}), "regular files"},
		"session":  {HarnessClaudeCode, archive(t, entry{name: "projects/x.jsonl", body: "{}"}), "not harness config"},
		"escape":   {HarnessClaudeCode, archive(t, entry{name: "../.bashrc", body: "x"}), "relative path"},
		"dup":      {HarnessClaudeCode, archive(t, entry{name: "CLAUDE.md", body: "a"}, entry{name: "./CLAUDE.md", body: "b"}), "twice"},
		"empty":    {HarnessClaudeCode, archive(t), "empty"},
		"garbage":  {HarnessClaudeCode, []byte("not a tar at all, clearly........"), "tar"},
		"big":      {HarnessClaudeCode, archive(t, entry{name: "CLAUDE.md", body: big}), "larger than"},
		"key":      {HarnessClaudeCode, archive(t, entry{name: "settings.json", body: "{\n\"env\": {\"K\": \"sk-ant-api03-" + strings.Repeat("a", 30) + "\"}}"}), "settings.json line 2 looks like an Anthropic API key"},
		"pem":      {HarnessRness, archive(t, entry{name: "lua/k.lua", body: "-----BEGIN OPENSSH PRIVATE KEY-----"}), "private key"},
		"memory":   {HarnessRness, archive(t, entry{name: "memory/a.md", body: "x"}), "not harness config"},
		"plugins":  {HarnessClaudeCode, archive(t, entry{name: "plugins/installed_plugins.json", body: "{}"}), "enabledPlugins"},
		"reserved": {HarnessRness, archive(t, entry{name: "lua/seaport.lua", body: "x"}), "Seaport's"},
	} {
		_, err := ReadHome(c.harness, c.archive)
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: %v, want %q", name, err, c.want)
		}
	}
	// a file referencing a secret through the environment is fine
	if _, err := ReadHome(HarnessClaudeCode, archive(t, entry{name: "settings.json", body: `{"env":{"K":"${GITHUB_TOKEN}"}}`})); err != nil {
		t.Fatal(err)
	}
}

func TestSpecHome(t *testing.T) {
	s := Spec{Harness: HarnessRness, Home: "sha256:" + strings.Repeat("a", 64)}
	if err := s.Validate(); err != nil {
		t.Fatal(err)
	}
	if Normalize(s).Home != s.Home {
		t.Fatal("normalize drops home")
	}
	s.Home = "latest"
	if err := s.Validate(); err == nil {
		t.Fatal("bad home accepted")
	}
	// The home changes the image: Dockerfile and tag.
	r := BuildRequest{Spec: Spec{Harness: HarnessRness}, Base: "b"}
	plain, tag := Dockerfile(r.Spec, r.Base), r.Tag()
	r.Spec.Home = "sha256:" + strings.Repeat("b", 64)
	df := Dockerfile(r.Spec, r.Base)
	if df == plain || r.Tag() == tag || !strings.Contains(df, "COPY --chown=1000:1000 home/ /home/agent/") ||
		!strings.Contains(df, "seaport home-setup") {
		t.Fatalf("dockerfile:\n%s", df)
	}
	// after setup, as the agent user
	r.Spec.Setup = "echo hi"
	df = Dockerfile(r.Spec, r.Base)
	if strings.Index(df, "USER 1000:1000") > strings.Index(df, "COPY --chown") {
		t.Fatalf("home before setup:\n%s", df)
	}
}

func TestHomeDigestNamesTheHarness(t *testing.T) {
	a := archive(t, entry{name: "skills/go/SKILL.md", body: "go"})
	r, err1 := ReadHome(HarnessRness, a)
	c, err2 := ReadHome(HarnessClaudeCode, a)
	if err1 != nil || err2 != nil || r.Digest == c.Digest || !bytes.Equal(r.Tar, c.Tar) {
		t.Fatalf("%v %v %s %s", err1, err2, r.Digest, c.Digest)
	}
}

// HomeRulesOf says what HomePath accepts: every name it lists is allowed.
func TestHomeRules(t *testing.T) {
	rules := HomeRulesOf()
	if len(rules) != 2 {
		t.Fatalf("%+v", rules)
	}
	for harness, r := range rules {
		if r.Root != HomeRoot(harness) || len(r.Dirs) == 0 || len(r.Files) == 0 || r.MaxBytes != MaxHomeBytes {
			t.Fatalf("%s: %+v", harness, r)
		}
		for _, d := range r.Dirs {
			if err := HomePath(harness, d+"/x.md"); err != nil {
				t.Errorf("%s dir %s: %v", harness, d, err)
			}
		}
		for _, f := range r.Files {
			if err := HomePath(harness, f); err != nil {
				t.Errorf("%s file %s: %v", harness, f, err)
			}
		}
		for _, p := range r.FilePrefixes {
			if err := HomePath(harness, p+".sh"); err != nil {
				t.Errorf("%s prefix %s: %v", harness, p, err)
			}
		}
		for _, p := range r.Reserved {
			if err := HomePath(harness, p); err == nil {
				t.Errorf("%s reserved %s accepted", harness, p)
			}
		}
		if len(r.Secrets) != len(secretPatterns) {
			t.Fatalf("%s secrets: %+v", harness, r.Secrets)
		}
		// JavaScript-compatible: no RE2-only syntax (flags, \z, \A, \Q…\E, [[:class:]], \p).
		for _, s := range r.Secrets {
			for _, bad := range []string{"(?", `\z`, `\A`, `\Q`, "[[:", `\p`} {
				if strings.Contains(s.Pattern, bad) {
					t.Errorf("%s: %q uses %q", s.What, s.Pattern, bad)
				}
			}
		}
	}
}
