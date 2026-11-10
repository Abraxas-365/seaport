package hostingdocker

import (
	"archive/tar"
	"bytes"
	"context"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/Abraxas-365/seaport/internal/errx"
	"github.com/Abraxas-365/seaport/internal/hosting"
	"github.com/Abraxas-365/seaport/internal/identity"
	"github.com/moby/moby/api/pkg/stdcopy"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/client"
)

// dockerRuntime connects to the local engine, or skips (fails in CI).
func dockerRuntime(t *testing.T) *Runtime {
	t.Helper()
	r, err := New(context.Background(), Config{})
	if err != nil {
		if os.Getenv("CI") != "" {
			t.Fatalf("docker: %v", err)
		}
		t.Skipf("no Docker engine: %v", err)
	}
	t.Cleanup(func() { _ = r.Close() })
	return r
}

// fakeBase builds a tiny image labeled as Seaport's base for harness.
func fakeBase(t *testing.T, r *Runtime, tag, labels string) {
	t.Helper()
	ctx := context.Background()
	df := "FROM debian:bookworm-slim\nRUN useradd --create-home --uid 1000 agent\nLABEL " + labels + "\nUSER 1000:1000\n"
	res, err := r.api.ImageBuild(ctx, contextOf(t, map[string]string{"Dockerfile": df}), client.ImageBuildOptions{Tags: []string{tag}, Remove: true})
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	var log bytes.Buffer
	if _, err := stream(res.Body, &log); err != nil {
		t.Fatalf("fake base: %v\n%s", err, log.String())
	}
	t.Cleanup(func() { remove(r, tag) })
}

func remove(r *Runtime, ref string) {
	_, _ = r.api.ImageRemove(context.Background(), ref, client.ImageRemoveOptions{Force: true, PruneChildren: true})
}

// Derived images: setup runs as root with the agent's home handed back,
// the result is tagged per version and content and labeled; an existing
// tag isn't built again; a base that isn't Seaport's (or is another
// harness's) is refused; a failing setup fails with its output.
func TestBuild(t *testing.T) {
	r := dockerRuntime(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	suffix := strings.ReplaceAll(identity.NewEnvironmentID().String()[:8], "-", "")
	base := "seaport-test-base-" + suffix + ":rness"
	fakeBase(t, r, base, `io.seaport.base="1" io.seaport.harness="rness"`)

	req := hosting.BuildRequest{Organization: identity.MustParseOrganizationID("00000000-0000-4000-8000-000000000001"), Environment: identity.NewEnvironmentID(),
		Version: 1, Base: base, Spec: hosting.Spec{Harness: hosting.HarnessRness,
			Setup: "id -u > /opt/setup-uid\necho \"$HOME\" > /home/agent/made-by-setup\n"}}
	t.Cleanup(func() { remove(r, req.Tag()) })
	var log bytes.Buffer
	id, err := r.Build(ctx, req, &log)
	if err != nil || !strings.HasPrefix(id, "sha256:") {
		t.Fatalf("build: %q %v\n%s", id, err, log.String())
	}
	img, err := r.api.ImageInspect(ctx, req.Tag())
	if err != nil || img.ID != id {
		t.Fatalf("inspect: %v %s", err, img.ID)
	}
	labels := img.Config.Labels
	if labels[hosting.LabelEnvironment] != req.Environment.String() || labels[hosting.LabelVersion] != "1" ||
		labels[hosting.LabelHarness] != "rness" || img.Config.User != "1000:1000" {
		t.Fatalf("labels/user: %+v %q", labels, img.Config.User)
	}
	out := run(t, r, req.Tag(), "cat /opt/setup-uid; stat -c %u /home/agent/made-by-setup; cat /home/agent/made-by-setup")
	if out != "0\n1000\n/home/agent\n" {
		t.Fatalf("setup ran as %q", out)
	}

	log.Reset()
	again, err := r.Build(ctx, req, &log)
	if err != nil || again != id || !strings.Contains(log.String(), "already built") {
		t.Fatalf("again: %q %v %s", again, err, log.String())
	}

	other := req
	other.Spec.Harness = hosting.HarnessClaudeCode
	if _, err := r.Build(ctx, other, &log); !errx.IsType(err, errx.TypeBusiness) || !strings.Contains(err.Error(), "harness rness") {
		t.Fatalf("other harness: %v", err)
	}
	plain := req
	plain.Base = "debian:bookworm-slim"
	if _, err := r.Build(ctx, plain, &log); !errx.IsType(err, errx.TypeBusiness) || !strings.Contains(err.Error(), "not built FROM") {
		t.Fatalf("not our base: %v", err)
	}

	broken := req
	broken.Version = 2
	broken.Spec.Setup = "echo about to fail\nexit 3\n"
	t.Cleanup(func() { remove(r, broken.Tag()) })
	log.Reset()
	if _, err := r.Build(ctx, broken, &log); !errx.IsType(err, errx.TypeExternal) || !strings.Contains(log.String(), "about to fail") ||
		!strings.Contains(err.Error(), "the setup script exited with code 3") {
		t.Fatalf("broken setup: %v\n%s", err, log.String())
	}
}

func contextOf(t *testing.T, files map[string]string) io.Reader {
	t.Helper()
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	for name, body := range files {
		if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o644, Size: int64(len(body))}); err != nil {
			t.Fatal(err)
		}
		if _, err := io.WriteString(tw, body); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	return &buf
}

// run runs script in a throwaway container of image and answers stdout.
func run(t *testing.T, r *Runtime, image, script string) string {
	t.Helper()
	ctx := context.Background()
	c, err := r.api.ContainerCreate(ctx, client.ContainerCreateOptions{Image: image,
		Config: &container.Config{Entrypoint: []string{"sh", "-c", script}}})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _, _ = r.api.ContainerRemove(ctx, c.ID, client.ContainerRemoveOptions{Force: true}) }()
	if _, err := r.api.ContainerStart(ctx, c.ID, client.ContainerStartOptions{}); err != nil {
		t.Fatal(err)
	}
	wait := r.api.ContainerWait(ctx, c.ID, client.ContainerWaitOptions{})
	select {
	case err := <-wait.Error:
		t.Fatal(err)
	case <-wait.Result:
	}
	logs, err := r.api.ContainerLogs(ctx, c.ID, client.ContainerLogsOptions{ShowStdout: true, ShowStderr: true})
	if err != nil {
		t.Fatal(err)
	}
	defer logs.Close()
	var stdout, stderr bytes.Buffer
	if _, err := stdcopy.StdCopy(&stdout, &stderr, logs); err != nil {
		t.Fatal(err)
	}
	if stderr.Len() > 0 {
		t.Logf("stderr: %s", stderr.String())
	}
	return stdout.String()
}
