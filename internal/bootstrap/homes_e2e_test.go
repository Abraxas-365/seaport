package bootstrap_test

import (
	"archive/tar"
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Abraxas-365/seaport/internal/bootstrap"
	"github.com/Abraxas-365/seaport/internal/config"
	"github.com/Abraxas-365/seaport/internal/hosting"
)

// homeTar packs files (path → body) as an upload.
func homeTar(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	for name, body := range files {
		if err := tw.WriteHeader(&tar.Header{Typeflag: tar.TypeReg, Name: name, Mode: 0o600, Size: int64(len(body))}); err != nil {
			t.Fatal(err)
		}
		_, _ = tw.Write([]byte(body))
	}
	_ = tw.Close()
	return buf.Bytes()
}

// upload POSTs a home archive and decodes the answer.
func (p *port) upload(harness, contentType string, body []byte, out any) int {
	p.t.Helper()
	req, _ := http.NewRequest(http.MethodPost, p.srv.URL+"/api/v1/homes?harness="+harness, bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+adminToken)
	req.Header.Set("Content-Type", contentType)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		p.t.Fatal(err)
	}
	defer resp.Body.Close()
	if out != nil {
		_ = json.NewDecoder(resp.Body).Decode(out)
	}
	return resp.StatusCode
}

// A harness home is uploaded (checked, normalized, sealed at rest),
// named by an environment's spec, baked into its build, read back file by
// file; a home no version names is swept with its blob.
func TestHarnessHomes(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "blobs")
	rt := &fakeRuntime{results: make(chan string, 4)}
	p := startPortWith(t, func(c *config.Server) {
		c.SecretKey = "e2e-homes-secrets-key-0123456789abcdefXYZ"
		c.BlobURL = config.LocalBlobURL(dir)
		c.ImageClaudeCode = "registry.test/seaport-claude-code:1"
	}, bootstrap.Options{Runtime: rt, SweepInterval: 200 * time.Millisecond})

	files := map[string]string{"CLAUDE.md": "be terse", "skills/go/SKILL.md": "---\nname: go\n---\nuse gofmt",
		"settings.json": `{"model":"sonnet"}`}
	var h hosting.Home
	if code := p.upload("claude-code", "application/x-tar", homeTar(t, files), &h); code != http.StatusCreated ||
		!hosting.ValidHomeDigest(h.Digest) || len(h.Files) != 3 || h.Harness != "claude-code" || h.CreatedBy == "" {
		t.Fatalf("upload: %d %+v", code, h)
	}
	// Sealed at rest: one blob, none of the files in clear.
	blobs := blobFiles(t, dir)
	if len(blobs) != 1 {
		t.Fatalf("blobs: %v", blobs)
	}
	if raw, _ := os.ReadFile(blobs[0]); bytes.Contains(raw, []byte("be terse")) {
		t.Fatal("home stored in clear")
	}
	// The same files again: the same home, no second blob.
	var again hosting.Home
	if code := p.upload("claude-code", "application/x-tar", homeTar(t, files), &again); code != http.StatusCreated || again.Digest != h.Digest {
		t.Fatalf("again: %d %+v", code, again)
	}
	if n := len(blobFiles(t, dir)); n != 1 {
		t.Fatalf("second blob: %d", n)
	}
	// Refusals: what isn't config, secrets, other types, other harnesses.
	for name, c := range map[string]struct {
		harness, ct string
		body        []byte
		want        int
	}{
		"session": {"claude-code", "application/x-tar", homeTar(t, map[string]string{"projects/a/s.jsonl": "{}"}), http.StatusBadRequest},
		"key": {"claude-code", "application/x-tar", homeTar(t, map[string]string{"CLAUDE.md": "token sk-ant-api03-" + strings.Repeat("x", 40)}),
			http.StatusBadRequest},
		"json":    {"claude-code", "application/json", []byte(`{}`), http.StatusUnsupportedMediaType},
		"harness": {"codex", "application/x-tar", homeTar(t, files), http.StatusBadRequest},
		"empty":   {"claude-code", "application/x-tar", nil, http.StatusBadRequest},
	} {
		if code := p.upload(c.harness, c.ct, c.body, nil); code != c.want {
			t.Errorf("%s: %d, want %d", name, code, c.want)
		}
	}

	// Reads: the manifest, a file, the archive.
	var got hosting.Home
	if code := p.admin(http.MethodGet, "/api/v1/homes/"+h.Digest, nil, &got); code != http.StatusOK || got.Digest != h.Digest || len(got.Files) != 3 {
		t.Fatalf("manifest: %d %+v", code, got)
	}
	if code, ct, body := p.fetch("/api/v1/homes/"+h.Digest+"/files/skills/go/SKILL.md", adminToken); code != http.StatusOK ||
		!strings.HasPrefix(ct, "text/plain") || string(body) != files["skills/go/SKILL.md"] {
		t.Fatalf("file: %d %s %q", code, ct, body)
	}
	if code, _, _ := p.fetch("/api/v1/homes/"+h.Digest+"/files/nope.md", adminToken); code != http.StatusNotFound {
		t.Fatalf("missing file: %d", code)
	}
	if code, ct, body := p.fetch("/api/v1/homes/"+h.Digest+"/archive", adminToken); code != http.StatusOK || ct != "application/x-tar" ||
		!bytes.Contains(body, []byte("be terse")) {
		t.Fatalf("archive: %d %s", code, ct)
	}
	if code := p.admin(http.MethodGet, "/api/v1/homes/sha256:"+strings.Repeat("0", 64), nil, nil); code != http.StatusNotFound {
		t.Fatalf("unknown home: %d", code)
	}

	// A spec names it; an unknown or other harness's home is refused.
	spec := map[string]any{"harness": "claude-code", "home": "sha256:" + strings.Repeat("1", 64)}
	if code := p.admin(http.MethodPost, "/api/v1/environments", map[string]any{"name": "web", "spec": spec}, nil); code != http.StatusBadRequest {
		t.Fatalf("unknown home in spec: %d", code)
	}
	var rh hosting.Home
	if code := p.upload("rness", "application/x-tar", homeTar(t, map[string]string{"skills/go/SKILL.md": "go"}), &rh); code != http.StatusCreated {
		t.Fatalf("rness home: %d", code)
	}
	spec["home"] = rh.Digest
	if code := p.admin(http.MethodPost, "/api/v1/environments", map[string]any{"name": "web", "spec": spec}, nil); code != http.StatusBadRequest {
		t.Fatalf("rness home on claude-code: %d", code)
	}
	spec["home"] = h.Digest
	var e hosting.Environment
	if code := p.admin(http.MethodPost, "/api/v1/environments", map[string]any{"name": "web", "spec": spec}, &e); code != http.StatusCreated || e.Spec.Home != h.Digest {
		t.Fatalf("create: %d %+v", code, e)
	}

	// The build gets the home's tar and the Dockerfile copies it in.
	var b hosting.Build
	rt.results <- "sha256:aaa"
	if code := p.admin(http.MethodPost, "/api/v1/environments/web/build", nil, &b); code != http.StatusAccepted {
		t.Fatalf("build: %d %+v", code, b)
	}
	waitFor(t, func() bool {
		p.admin(http.MethodGet, "/api/v1/environments/web/build", nil, &b)
		return b.State == hosting.BuildReady
	})
	req := rt.last()
	if !bytes.Contains(req.Home, []byte("be terse")) || !strings.Contains(hosting.Dockerfile(req.Spec, req.Base), "COPY --chown=1000:1000 home/ /home/agent/") {
		t.Fatalf("build request: home %d bytes", len(req.Home))
	}

	// Sweep: the rness home (named by nothing) goes once past the grace;
	// the named one stays. Make both old.
	db := testDB(t)
	if _, err := db.ExecContext(context.Background(), `UPDATE hosting_homes SET touched_at = now() - interval '2 days'`); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { return len(blobFiles(t, dir)) == 1 })
	if code := p.admin(http.MethodGet, "/api/v1/homes/"+rh.Digest, nil, nil); code != http.StatusNotFound {
		t.Fatalf("unused home kept: %d", code)
	}
	if code := p.admin(http.MethodGet, "/api/v1/homes/"+h.Digest, nil, nil); code != http.StatusOK {
		t.Fatalf("named home swept: %d", code)
	}
	// Dropping the environment frees its home for the next sweep.
	if code := p.admin(http.MethodDelete, "/api/v1/environments/web", nil, nil); code != http.StatusNoContent {
		t.Fatalf("delete env: %d", code)
	}
	waitFor(t, func() bool { return len(blobFiles(t, dir)) == 0 })
}

func waitFor(t *testing.T, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !ok() {
		if time.Now().After(deadline) {
			t.Fatal("timed out")
		}
		time.Sleep(100 * time.Millisecond)
	}
}
