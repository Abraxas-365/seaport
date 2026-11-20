package bootstrap_test

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"slices"
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
	// signedIn: slots whose (fake) harness signed in.
	signedIn map[string]bool
	// noBrowser: the browser relay fails (the engine isn't running).
	noBrowser bool
	// pruned: images the engine no longer has (HasImage).
	pruned map[string]bool
}

func (f *fakeRuntime) browserDown() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.noBrowser
}

// fakeLoginURL is what the fake harness's login prints to sign in at.
const fakeLoginURL = "https://claude.com/cai/oauth/authorize?code=true&client_id=x&state=s"

// Exec plays the harness in a running container: the login prints its
// URL, reads a code ("good-code" signs in, anything else fails) and the
// account command answers whether it signed in.
func (f *fakeRuntime) Exec(ctx context.Context, ref hosting.SlotRef, run string, cmd []string, stdin io.Reader, out io.Writer) (int, error) {
	f.mu.Lock()
	r := f.slots[ref.Key()]
	f.mu.Unlock()
	if !r.Container {
		return 0, errx.NotFound("the slot has no container (it is stopped): start it first")
	}
	if !r.Running || (run != "" && r.Run != run) {
		return 0, errx.Business("the slot's container isn't running")
	}
	switch {
	case slices.Equal(cmd, hosting.LoginCommand):
		fmt.Fprintf(out, "Opening browser to sign in…\nIf the browser didn't open, visit: %s\n", fakeLoginURL)
		line := make(chan string, 1)
		go func() {
			b, _ := bufio.NewReader(stdin).ReadString('\n')
			line <- strings.TrimSpace(b)
		}()
		select {
		case <-ctx.Done():
			return 0, ctx.Err()
		case code := <-line:
			if code != "good-code" {
				fmt.Fprintln(out, "Login failed: Request failed with status code 400")
				return 1, nil
			}
		}
		f.mu.Lock()
		if f.signedIn == nil {
			f.signedIn = map[string]bool{}
		}
		f.signedIn[ref.Key()] = true
		f.mu.Unlock()
		fmt.Fprintln(out, "Login successful.")
		return 0, nil
	case slices.Equal(cmd, hosting.BrowserRelayCommand):
		// The browser relay: a hello, then every CDP request echoed as its
		// result ({"id":N,"result":{"echo":<request>}}), until stdin ends.
		if f.browserDown() {
			fmt.Fprintln(out, "the browser is not running: connection refused")
			return 1, nil
		}
		fmt.Fprintln(out, `{"seaport":"ready"}`)
		sc := bufio.NewScanner(stdin)
		sc.Buffer(make([]byte, 64<<10), hosting.MaxBrowserMessage)
		for sc.Scan() {
			var req struct {
				ID int `json:"id"`
			}
			_ = json.Unmarshal(sc.Bytes(), &req)
			fmt.Fprintf(out, `{"id":%d,"result":{"echo":%s}}`+"\n", req.ID, sc.Bytes())
		}
		return 0, nil
	case slices.Equal(cmd, hosting.AccountCommand):
		f.mu.Lock()
		in := f.signedIn[ref.Key()]
		f.mu.Unlock()
		if in {
			fmt.Fprintln(out, `{"slot":0,"supported":true,"logged_in":true,"method":"claude.ai","subscription":"max"}`)
		} else {
			fmt.Fprintln(out, `{"slot":0,"supported":true,"logged_in":false,"method":"none"}`)
		}
		return 0, nil
	}
	return 127, nil
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

// HasImage: every image a build answered is there, but pruned ones.
func (f *fakeRuntime) HasImage(_ context.Context, image string) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return !f.pruned[image], nil
}

func buildState(state string) func(json.RawMessage) bool {
	return func(d json.RawMessage) bool {
		var b hosting.Build
		return json.Unmarshal(d, &b) == nil && b.State == state
	}
}

// An agent's machine is built once into an image (in the background,
// agent.build events, the log's tail stored), keyed by what goes into it,
// so agents with the same machine share it; a build in progress or done
// is answered as it is, a failed one is built again, force rebuilds a
// ready one; an instructions edit needs no build; with the runtime off
// building is refused.
func TestAgentBuilds(t *testing.T) {
	rt := &fakeRuntime{results: make(chan string)}
	p := startPortWith(t, func(c *config.Server) {
		c.ImageClaudeCode = "registry.test/seaport-claude-code:1"
	}, bootstrap.Options{Runtime: rt})
	events := p.openEvents("")
	web, twin := p.agent("web"), p.agent("twin")
	buildPath := "/api/v1/agents/" + web.ID.String() + "/build"
	if code := p.admin(http.MethodPost, buildPath, nil, nil); code != http.StatusUnprocessableEntity {
		t.Fatalf("build without a machine: %d", code)
	}
	machine := map[string]any{"packages": []string{"jq"}, "setup": "echo hi"}
	p.setMachine(web.ID, "claude-code", machine)
	if code := p.admin(http.MethodGet, buildPath, nil, nil); code != http.StatusNotFound {
		t.Fatalf("never built: %d", code)
	}

	var b hosting.Build
	if code := p.admin(http.MethodPost, buildPath, nil, &b); code != http.StatusAccepted || b.State != hosting.BuildBuilding ||
		b.Agent != web.ID || b.Base != "registry.test/seaport-claude-code:1" || b.Digest == "" {
		t.Fatalf("start: %d %+v", code, b)
	}
	digest := b.Digest
	next(t, events, hosting.EventAgentBuild, buildState(hosting.BuildBuilding))
	// Asking again while it builds answers the same build (no second one).
	if code := p.admin(http.MethodPost, buildPath, map[string]any{}, &b); code != http.StatusAccepted || b.State != hosting.BuildBuilding {
		t.Fatalf("again while building: %d %+v", code, b)
	}
	rt.results <- "fail:apt: no package jq"
	ev := next(t, events, hosting.EventAgentBuild, buildState(hosting.BuildFailed))
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
	next(t, events, hosting.EventAgentBuild, buildState(hosting.BuildReady))
	if code := p.admin(http.MethodPost, buildPath, nil, &b); code != http.StatusAccepted || b.State != hosting.BuildReady || b.Image != "sha256:aaa" {
		t.Fatalf("again when ready: %d %+v", code, b)
	}
	if req := rt.last(); strings.TrimSpace(req.Spec.Setup) != "echo hi" || req.Force || !strings.HasPrefix(req.Tag(), "seaport-build-") {
		t.Fatalf("request: %+v %s", req, req.Tag())
	}
	// Another agent with the same machine shares the image.
	p.setMachine(twin.ID, "claude-code", machine)
	if code := p.admin(http.MethodGet, "/api/v1/agents/"+twin.ID.String()+"/build", nil, &b); code != http.StatusOK ||
		b.Digest != digest || b.Image != "sha256:aaa" {
		t.Fatalf("twin's build: %d %+v", code, b)
	}
	// An instructions edit keeps the build.
	p.saveConfig(web.ID, func(body map[string]any) { body["instructions"] = "Be brief." })
	if code := p.admin(http.MethodGet, buildPath, nil, &b); code != http.StatusOK || b.Digest != digest {
		t.Fatalf("build after an instructions edit: %d %+v", code, b)
	}
	if code := p.admin(http.MethodPost, buildPath, map[string]any{"force": true}, &b); code != http.StatusAccepted || b.State != hosting.BuildBuilding {
		t.Fatalf("force: %d %+v", code, b)
	}
	rt.results <- "sha256:bbb"
	next(t, events, hosting.EventAgentBuild, buildState(hosting.BuildReady))
	if req := rt.last(); !req.Force {
		t.Fatalf("forced request: %+v", req)
	}

	// A machine edit needs a build of its own.
	machine["image"] = "registry.test/custom:2"
	p.setMachine(web.ID, "claude-code", machine)
	if code := p.admin(http.MethodGet, buildPath, nil, nil); code != http.StatusNotFound {
		t.Fatalf("new machine not built yet: %d", code)
	}
	if code := p.admin(http.MethodPost, buildPath, nil, &b); code != http.StatusAccepted || b.Digest == digest || b.Base != "registry.test/custom:2" {
		t.Fatalf("new machine build: %d %+v", code, b)
	}
	rt.results <- "sha256:ccc"
	next(t, events, hosting.EventAgentBuild, buildState(hosting.BuildReady))
	if code := p.admin(http.MethodGet, "/api/v1/agents/"+twin.ID.String()+"/build", nil, &b); code != http.StatusOK || b.Image != "sha256:bbb" {
		t.Fatalf("the twin keeps its image: %d %+v", code, b)
	}

	// Closing the port while a build runs records it as failed.
	twinBuild := "/api/v1/agents/" + twin.ID.String() + "/build"
	if code := p.admin(http.MethodPost, twinBuild, map[string]any{"force": true}, nil); code != http.StatusAccepted {
		t.Fatalf("build before close: %d", code)
	}
	next(t, events, hosting.EventAgentBuild, buildState(hosting.BuildBuilding))
	p.stop()
	// The same base image as before, so the machine names the same build.
	off := joinPort(t, func(c *config.Server) { c.ImageClaudeCode = "registry.test/seaport-claude-code:1" }, bootstrap.Options{})
	var interrupted hosting.Build
	if code := off.admin(http.MethodGet, twinBuild, nil, &interrupted); code != http.StatusOK ||
		interrupted.State != hosting.BuildFailed || !strings.Contains(interrupted.Detail, "stopped") {
		t.Fatalf("interrupted: %d %+v", code, interrupted)
	}
	// SEAPORT_RUNTIME=off: nothing builds.
	if code := off.admin(http.MethodPost, buildPath, nil, nil); code != http.StatusUnprocessableEntity {
		t.Fatalf("runtime off: %d", code)
	}
}
