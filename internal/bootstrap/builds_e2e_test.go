package bootstrap_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/Abraxas-365/seaport/internal/bootstrap"
	"github.com/Abraxas-365/seaport/internal/config"
	"github.com/Abraxas-365/seaport/internal/errx"
	"github.com/Abraxas-365/seaport/internal/hosting"
)

// fakeRuntime builds by waiting for the test: each build reads its
// outcome from results (an image id, or "fail:<message>"). Slots are
// containers in memory (starts recorded; startErr fails the next start).
type fakeRuntime struct {
	mu       sync.Mutex
	requests []hosting.BuildRequest
	results  chan string
	slots    map[string]hosting.RuntimeSlot
	starts   []hosting.StartSlot
	removed  []hosting.SlotRef
	startErr error
}

func (f *fakeRuntime) Start(_ context.Context, s hosting.StartSlot) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.startErr; err != nil {
		f.startErr = nil
		return err
	}
	if f.slots == nil {
		f.slots = map[string]hosting.RuntimeSlot{}
	}
	f.starts = append(f.starts, s)
	f.slots[s.Slot.Key()] = hosting.RuntimeSlot{Slot: s.Slot, Container: true, Run: s.Run, Running: true}
	return nil
}

func (f *fakeRuntime) Stop(_ context.Context, ref hosting.SlotRef, run string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if r, ok := f.slots[ref.Key()]; ok && (run == "" || r.Run == run) {
		f.slots[ref.Key()] = hosting.RuntimeSlot{Slot: ref} // volumes stay
	}
	return nil
}

func (f *fakeRuntime) Remove(_ context.Context, ref hosting.SlotRef) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.slots, ref.Key())
	f.removed = append(f.removed, ref)
	return nil
}

func (f *fakeRuntime) Slots(context.Context, string) ([]hosting.RuntimeSlot, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]hosting.RuntimeSlot, 0, len(f.slots))
	for _, r := range f.slots {
		out = append(out, r)
	}
	return out, nil
}

// Logs answers a line naming the slot and the lines asked for while it
// has a container.
func (f *fakeRuntime) Logs(_ context.Context, ref hosting.SlotRef, lines int) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.slots[ref.Key()].Container {
		return "", errx.NotFound("the slot has no container (it is stopped): start it to see its log")
	}
	return fmt.Sprintf("slot %d: %d lines\n", ref.Index, lines), nil
}

// exit makes a slot's container exit (the agent crashed).
func (f *fakeRuntime) exit(ref hosting.SlotRef, detail string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	r := f.slots[ref.Key()]
	r.Running, r.Detail = false, detail
	f.slots[ref.Key()] = r
}

func (f *fakeRuntime) running(ref hosting.SlotRef) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.slots[ref.Key()].Running
}

func (f *fakeRuntime) lastStart() hosting.StartSlot {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.starts[len(f.starts)-1]
}

func (f *fakeRuntime) Build(ctx context.Context, req hosting.BuildRequest, log io.Writer) (string, error) {
	f.mu.Lock()
	f.requests = append(f.requests, req)
	f.mu.Unlock()
	fmt.Fprintf(log, "Step 1/1 : FROM %s\n", req.Base)
	select {
	case <-ctx.Done():
		return "", ctx.Err()
	case r := <-f.results:
		if msg, ok := strings.CutPrefix(r, "fail:"); ok {
			fmt.Fprintln(log, msg)
			return "", errx.External("the build failed: " + msg)
		}
		return r, nil
	}
}

func (f *fakeRuntime) last() hosting.BuildRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.requests[len(f.requests)-1]
}

func buildState(state string) func(json.RawMessage) bool {
	return func(d json.RawMessage) bool {
		var b hosting.Build
		return json.Unmarshal(d, &b) == nil && b.State == state
	}
}

// An environment version is built once into an image (in the background,
// environment.build events, the log's tail stored); a build in progress
// or done is answered as it is, a failed one is built again, force
// rebuilds a ready one; with the runtime off building is refused.
func TestEnvironmentBuilds(t *testing.T) {
	rt := &fakeRuntime{results: make(chan string)}
	p := startPortWith(t, func(c *config.Server) {
		c.ImageClaudeCode = "registry.test/seaport-claude-code:1"
	}, bootstrap.Options{Runtime: rt})
	events := p.openEvents("")
	spec := map[string]any{"harness": "claude-code", "packages": []string{"jq"}, "setup": "echo hi"}
	var e hosting.Environment
	if code := p.admin(http.MethodPost, "/api/v1/environments", map[string]any{"name": "web", "spec": spec}, &e); code != http.StatusCreated {
		t.Fatalf("create: %d", code)
	}
	buildPath := "/api/v1/environments/web/build"
	if code := p.admin(http.MethodGet, buildPath, nil, nil); code != http.StatusNotFound {
		t.Fatalf("never built: %d", code)
	}

	var b hosting.Build
	if code := p.admin(http.MethodPost, buildPath, nil, &b); code != http.StatusAccepted || b.State != hosting.BuildBuilding ||
		b.Version != 1 || b.Base != "registry.test/seaport-claude-code:1" {
		t.Fatalf("start: %d %+v", code, b)
	}
	next(t, events, hosting.EventEnvironmentBuild, buildState(hosting.BuildBuilding))
	// Asking again while it builds answers the same build (no second one).
	if code := p.admin(http.MethodPost, buildPath, map[string]any{"version": 1}, &b); code != http.StatusAccepted || b.State != hosting.BuildBuilding {
		t.Fatalf("again while building: %d %+v", code, b)
	}
	rt.results <- "fail:apt: no package jq"
	ev := next(t, events, hosting.EventEnvironmentBuild, buildState(hosting.BuildFailed))
	if strings.Contains(string(ev.Data), `"log"`) {
		t.Fatalf("events carry no log: %s", ev.Data)
	}
	if code := p.admin(http.MethodGet, buildPath, nil, &b); code != http.StatusOK || b.State != hosting.BuildFailed ||
		!strings.Contains(b.Detail, "no package jq") || !strings.Contains(b.Log, "FROM registry.test/seaport-claude-code:1") ||
		b.FinishedAt == nil {
		t.Fatalf("failed: %d %+v", code, b)
	}

	// A failed build is built again; ready, it is answered as it is.
	var retry hosting.Build
	if code := p.admin(http.MethodPost, buildPath, nil, &retry); code != http.StatusAccepted || retry.State != hosting.BuildBuilding || retry.Log != "" {
		t.Fatalf("retry: %d %+v", code, retry)
	}
	rt.results <- "sha256:aaa"
	next(t, events, hosting.EventEnvironmentBuild, buildState(hosting.BuildReady))
	if code := p.admin(http.MethodGet, "/api/v1/environments/web/versions/1/build", nil, &b); code != http.StatusOK ||
		b.State != hosting.BuildReady || b.Image != "sha256:aaa" {
		t.Fatalf("ready: %d %+v", code, b)
	}
	if code := p.admin(http.MethodPost, buildPath, nil, &b); code != http.StatusAccepted || b.State != hosting.BuildReady {
		t.Fatalf("again when ready: %d %+v", code, b)
	}
	if req := rt.last(); strings.TrimSpace(req.Spec.Setup) != "echo hi" || req.Force || req.Version != 1 ||
		!strings.HasPrefix(req.Tag(), "seaport-env-"+e.ID.String()+":v1-") {
		t.Fatalf("request: %+v %s", req, req.Tag())
	}
	if code := p.admin(http.MethodPost, buildPath, map[string]any{"force": true}, &b); code != http.StatusAccepted || b.State != hosting.BuildBuilding {
		t.Fatalf("force: %d %+v", code, b)
	}
	rt.results <- "sha256:bbb"
	next(t, events, hosting.EventEnvironmentBuild, buildState(hosting.BuildReady))
	if req := rt.last(); !req.Force {
		t.Fatalf("forced request: %+v", req)
	}

	// A new version builds on its own; versions that don't exist don't.
	spec["image"] = "registry.test/custom:2"
	if code := p.admin(http.MethodPut, "/api/v1/environments/web", map[string]any{"base": 1, "spec": spec}, nil); code != http.StatusOK {
		t.Fatalf("v2: %d", code)
	}
	if code := p.admin(http.MethodGet, buildPath, nil, nil); code != http.StatusNotFound {
		t.Fatalf("v2 not built yet: %d", code)
	}
	if code := p.admin(http.MethodPost, buildPath, nil, &b); code != http.StatusAccepted || b.Version != 2 || b.Base != "registry.test/custom:2" {
		t.Fatalf("v2 build: %d %+v", code, b)
	}
	rt.results <- "sha256:ccc"
	next(t, events, hosting.EventEnvironmentBuild, buildState(hosting.BuildReady))
	if code := p.admin(http.MethodPost, buildPath, map[string]any{"version": 9}, nil); code != http.StatusNotFound {
		t.Fatalf("missing version: %d", code)
	}
	if code := p.admin(http.MethodGet, "/api/v1/environments/web/versions/1/build", nil, &b); code != http.StatusOK || b.Image != "sha256:bbb" {
		t.Fatalf("v1 keeps its image: %d %+v", code, b)
	}

	// Closing the port while a build runs records it as failed.
	if code := p.admin(http.MethodPost, buildPath, map[string]any{"version": 1, "force": true}, nil); code != http.StatusAccepted {
		t.Fatalf("build before close: %d", code)
	}
	next(t, events, hosting.EventEnvironmentBuild, buildState(hosting.BuildBuilding))
	p.stop()
	off := joinPort(t, func(*config.Server) {}, bootstrap.Options{})
	var interrupted hosting.Build
	if code := off.admin(http.MethodGet, "/api/v1/environments/web/versions/1/build", nil, &interrupted); code != http.StatusOK ||
		interrupted.State != hosting.BuildFailed || !strings.Contains(interrupted.Detail, "stopped") {
		t.Fatalf("interrupted: %d %+v", code, interrupted)
	}
	// SEAPORT_RUNTIME=off: nothing builds.
	if code := off.admin(http.MethodPost, buildPath, nil, nil); code != http.StatusUnprocessableEntity {
		t.Fatalf("runtime off: %d", code)
	}
}
