package hostingdocker

import (
	"context"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/Abraxas-365/seaport/internal/errx"
	"github.com/Abraxas-365/seaport/internal/hosting"
	"github.com/Abraxas-365/seaport/internal/identity"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/client"
)

// tail keeps the last maxLogBytes, cut at a line start.
func TestTail(t *testing.T) {
	var tl tail
	line := strings.Repeat("x", 99) + "\n"
	for range 3 * maxLogBytes / len(line) {
		_, _ = tl.Write([]byte(line))
	}
	_, _ = tl.Write([]byte("last\n"))
	out := tl.String()
	if len(out) > maxLogBytes || !strings.HasSuffix(out, "\nlast\n") || !strings.HasPrefix(out, "xxx") ||
		strings.Count(out, "\n") != len(out)/len(line)+1 {
		t.Fatalf("tail: %d bytes, starts %q, ends %q", len(out), out[:5], out[len(out)-10:])
	}
	var short tail
	_, _ = short.Write([]byte("a\nb\n"))
	if short.String() != "a\nb\n" {
		t.Fatalf("short tail: %q", short.String())
	}
}

// Logs reads a slot container's stdout and stderr, timestamped, its last
// lines only; a slot with no container is NotFound.
func TestLogs(t *testing.T) {
	r := dockerRuntime(t)
	ctx := context.Background()
	ref := hosting.SlotRef{Organization: identity.OrganizationID(identity.NewAgentID()), Agent: identity.NewAgentID(), Index: 1}
	if _, err := r.Logs(ctx, ref, 10); !errx.IsType(err, errx.TypeNotFound) {
		t.Fatalf("no container: %v", err)
	}
	name := containerName(ref)
	c, err := r.api.ContainerCreate(ctx, client.ContainerCreateOptions{Name: name, Image: "debian:bookworm-slim",
		// stdout and stderr are copied apart: space them so their order is the timestamps'.
		Config: &container.Config{Entrypoint: []string{"sh", "-c", "echo one; sleep 0.2; echo two >&2; sleep 0.2; echo three"}}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = r.api.ContainerRemove(context.Background(), c.ID, client.ContainerRemoveOptions{Force: true})
	})
	if _, err := r.api.ContainerStart(ctx, c.ID, client.ContainerStartOptions{}); err != nil {
		t.Fatal(err)
	}
	wait := r.api.ContainerWait(ctx, c.ID, client.ContainerWaitOptions{})
	select {
	case err := <-wait.Error:
		t.Fatal(err)
	case <-wait.Result:
	}
	all, err := r.Logs(ctx, ref, 10)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(all), "\n")
	if len(lines) != 3 || !strings.HasSuffix(lines[0], " one") || !strings.HasSuffix(lines[1], " two") ||
		!strings.HasPrefix(lines[0], "20") {
		t.Fatalf("logs: %q", all)
	}
	last, err := r.Logs(ctx, ref, 1)
	if err != nil || !strings.HasSuffix(strings.TrimSpace(last), " three") || strings.Count(last, "\n") != 1 {
		t.Fatalf("last line: %q %v", last, err)
	}
}

// Exec runs a command in a slot's running container, as uid 1000, with
// stdin fed and stdout+stderr read, and answers its exit code; another
// run's container, or none, is refused.
func TestExec(t *testing.T) {
	r := dockerRuntime(t)
	ctx := context.Background()
	ref := hosting.SlotRef{Organization: identity.OrganizationID(identity.NewAgentID()), Agent: identity.NewAgentID(), Index: 1}
	if _, err := r.Exec(ctx, ref, "", []string{"true"}, nil, io.Discard); !errx.IsType(err, errx.TypeNotFound) {
		t.Fatalf("no container: %v", err)
	}
	c, err := r.api.ContainerCreate(ctx, client.ContainerCreateOptions{Name: containerName(ref), Image: "debian:bookworm-slim",
		Config: &container.Config{Entrypoint: []string{"sleep", "60"}, Labels: map[string]string{hosting.LabelRun: "run-1"}}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = r.api.ContainerRemove(context.Background(), c.ID, client.ContainerRemoveOptions{Force: true})
	})
	if _, err := r.api.ContainerStart(ctx, c.ID, client.ContainerStartOptions{}); err != nil {
		t.Fatal(err)
	}
	var out strings.Builder
	code, err := r.Exec(ctx, ref, "run-1", []string{"sh", "-c", "read x; echo got $x; id -u; echo oops >&2; exit 3"},
		strings.NewReader("hello\n"), &out)
	// stdout and stderr are two pipes Docker reads concurrently: each keeps
	// its order, but not their order relative to each other.
	var stdout, stderr []string
	for _, l := range strings.Split(strings.TrimSuffix(out.String(), "\n"), "\n") {
		if l == "oops" {
			stderr = append(stderr, l)
		} else {
			stdout = append(stdout, l)
		}
	}
	if err != nil || code != 3 || strings.Join(stdout, ",") != "got hello,1000" || len(stderr) != 1 {
		t.Fatalf("exec: %d %q %v", code, out.String(), err)
	}
	if _, err := r.Exec(ctx, ref, "run-2", []string{"true"}, nil, io.Discard); !errx.IsType(err, errx.TypeBusiness) {
		t.Fatalf("another run: %v", err)
	}
	// ctx ending ends a command waiting on stdin.
	pr, _ := io.Pipe()
	short, cancel := context.WithTimeout(ctx, 500*time.Millisecond)
	defer cancel()
	if _, err := r.Exec(short, ref, "", []string{"sh", "-c", "read x"}, pr, io.Discard); err == nil {
		t.Fatal("exec outlived its context")
	}
}
