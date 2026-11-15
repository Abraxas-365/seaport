package hosting

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"net/url"
	"path"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

// fuzzTar builds a tar the way an upload would arrive.
func fuzzTar(entries ...entry) []byte {
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
		h := &tar.Header{Name: e.name, Typeflag: typ, Mode: mode, Size: int64(len(e.body)), ModTime: time.Unix(1, 0)}
		if typ != tar.TypeReg {
			h.Size = 0
			h.Linkname = "/etc/passwd"
		}
		_ = tw.WriteHeader(h)
		if typ == tar.TypeReg {
			_, _ = tw.Write([]byte(e.body))
		}
	}
	_ = tw.Close()
	return buf.Bytes()
}

func fuzzGzip(b []byte) []byte {
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	_, _ = zw.Write(b)
	_ = zw.Close()
	return buf.Bytes()
}

// FuzzReadHome feeds arbitrary bytes as an uploaded home. Whatever is
// accepted must obey the rules the build relies on: allowed relative
// paths only, limits kept, no secret-looking strings, and a normalized
// tar that reads back as the very same home (stable digest).
func FuzzReadHome(f *testing.F) {
	good := fuzzTar(entry{name: "CLAUDE.md", body: "be terse"}, entry{name: "hooks/pre.sh", body: "#!/bin/sh\n", mode: 0o755},
		entry{name: "skills/", typ: tar.TypeDir}, entry{name: "skills/go/SKILL.md", body: "go"})
	rness := fuzzTar(entry{name: "init.lua", body: "require('seaport')"}, entry{name: "lua/a.lua", body: "return 1"})
	seeds := [][]byte{
		good, fuzzGzip(good), rness,
		fuzzTar(entry{name: "../.bashrc", body: "x"}),
		fuzzTar(entry{name: "CLAUDE.md", typ: tar.TypeSymlink}),
		fuzzTar(entry{name: "CLAUDE.md", body: "a"}, entry{name: "./CLAUDE.md", body: "b"}),
		fuzzTar(entry{name: "settings.json", body: `{"env":{"K":"sk-` + "ant-api03-" + strings.Repeat("a", 30) + `"}}`}),
		fuzzTar(entry{name: "projects/x.jsonl", body: "{}"}),
		fuzzTar(entry{name: "lua/seaport.lua", body: "x"}),
		fuzzTar(), {0x1f, 0x8b}, []byte("not a tar at all, clearly........"), nil,
	}
	for _, s := range seeds {
		f.Add(false, s)
		f.Add(true, s)
	}
	f.Fuzz(func(t *testing.T, claude bool, data []byte) {
		harness := HarnessRness
		if claude {
			harness = HarnessClaudeCode
		}
		b, err := ReadHome(harness, data)
		if err != nil {
			return
		}
		if !ValidHomeDigest(b.Digest) {
			t.Fatalf("bad digest %q", b.Digest)
		}
		if len(b.Files) > MaxHomeFiles || b.Size > MaxHomeBytes {
			t.Fatalf("limits broken: %d files, %d bytes", len(b.Files), b.Size)
		}
		var size int64
		for i, hf := range b.Files {
			if err := HomePath(harness, hf.Path); err != nil {
				t.Fatalf("accepted path %q: %v", hf.Path, err)
			}
			if hf.Size > MaxHomeFile {
				t.Fatalf("%s: %d bytes", hf.Path, hf.Size)
			}
			if i > 0 && b.Files[i-1].Path >= hf.Path {
				t.Fatalf("files not sorted/unique at %q", hf.Path)
			}
			body, err := HomeFileData(b.Tar, hf.Path)
			if err != nil {
				t.Fatalf("%s listed but not in the tar: %v", hf.Path, err)
			}
			if err := ScanSecrets(hf.Path, body); err != nil {
				t.Fatalf("accepted a secret: %v", err)
			}
			size += hf.Size
		}
		if size != b.Size {
			t.Fatalf("size %d != sum of files %d", b.Size, size)
		}
		again, err := ReadHome(harness, b.Tar)
		if err != nil || again.Digest != b.Digest || !bytes.Equal(again.Tar, b.Tar) {
			t.Fatalf("the normalized home does not read back the same: %v", err)
		}
		// the tar is reproducible and carries only normalized headers
		tr := tar.NewReader(bytes.NewReader(b.Tar))
		for {
			h, err := tr.Next()
			if err != nil {
				break
			}
			if h.Typeflag != tar.TypeReg || h.Uid != 1000 || h.Gid != 1000 || h.ModTime.Unix() != 0 || (h.Mode != 0o644 && h.Mode != 0o755) {
				t.Fatalf("header not normalized: %+v", h)
			}
		}
	})
}

// FuzzHomePath: an accepted path is clean, relative, inside the home and
// never git metadata or a credentials file.
func FuzzHomePath(f *testing.F) {
	for _, s := range []string{"CLAUDE.md", "skills/go/SKILL.md", "../x", "/etc/passwd", "a//b", ".git/config", "a\\b",
		"settings.local.json", "plugins/x", "lua/seaport.lua", "", ".", "skills/./x", "agents/\x00", "credentials.json.bak"} {
		f.Add(false, s)
		f.Add(true, s)
	}
	f.Fuzz(func(t *testing.T, claude bool, rel string) {
		harness := HarnessRness
		if claude {
			harness = HarnessClaudeCode
		}
		if HomePath(harness, rel) != nil {
			return
		}
		if rel == "" || rel == "." || path.IsAbs(rel) || path.Clean(rel) != rel || !utf8.ValidString(rel) ||
			strings.ContainsAny(rel, "\x00\\") || len(rel) > MaxHomePath {
			t.Fatalf("accepted %q", rel)
		}
		for _, p := range strings.Split(rel, "/") {
			if p == ".." || p == ".git" {
				t.Fatalf("accepted %q", rel)
			}
		}
		if strings.HasPrefix(path.Base(rel), "credentials.json") || path.Base(rel) == ".credentials.json" {
			t.Fatalf("accepted credentials file %q", rel)
		}
	})
}

// FuzzLoginURL: the line comes from inside a container, so a URL is shown
// only when it is the harness's authorize page on a known host.
func FuzzLoginURL(f *testing.F) {
	for _, s := range []string{
		"Open https://claude.ai/oauth/authorize?client_id=x to sign in",
		"https://evil.example.com/oauth/authorize", "https://user@claude.ai/oauth/authorize",
		"https://claude.ai:444/oauth/authorize", "https://claude.com/other", "http://claude.ai/oauth/authorize",
		"https://CLAUDE.AI/oauth/authorize", "",
	} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, line string) {
		got := LoginURL(line)
		_ = LoginFailure(line, 1)
		if got == "" {
			return
		}
		u, err := url.Parse(got)
		if err != nil || u.Scheme != "https" || u.User != nil || u.Port() != "" || !strings.Contains(u.Path, "/oauth/authorize") {
			t.Fatalf("bad login url %q", got)
		}
		ok := false
		for _, h := range loginHosts {
			ok = ok || strings.EqualFold(u.Hostname(), h)
		}
		if !ok {
			t.Fatalf("host %q not allowed", u.Hostname())
		}
	})
}
