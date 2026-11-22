package bootstrap_test

import (
	"net/http"
	"testing"
	"time"

	"github.com/Abraxas-365/seaport/internal/bootstrap"
	"github.com/Abraxas-365/seaport/internal/config"
	"github.com/Abraxas-365/seaport/internal/hosting"
	"github.com/Abraxas-365/seaport/internal/identity"
)

// The supervisor removes the images nothing needs: an agent's previous
// build once its new one is ready and no slot runs the old one (its
// record forgotten too), and older tags of the port's base images; a
// current base, a custom base, another port's build and an image in its
// grace stay, and SEAPORT_IMAGE_GC=off removes nothing.
func TestImageGC(t *testing.T) {
	old := time.Now().Add(-2 * hosting.ImageGCGrace)
	otherOrg := identity.MustParseOrganizationID("00000000-0000-4000-8000-0000000000ff")
	base := func(id, tag string) hosting.Image {
		return hosting.Image{ID: id, Tags: []string{tag}, Labels: map[string]string{hosting.LabelBase: "1"}, Created: old, Size: 1 << 30}
	}
	rt := &fakeRuntime{results: make(chan string, 1), images: map[string]hosting.Image{
		"sha256:base0":  base("sha256:base0", "registry.test/seaport-rness:0"),
		"sha256:base1":  base("sha256:base1", "registry.test/seaport-rness:1"),
		"sha256:custom": base("sha256:custom", "registry.test/mine:1"),
		"sha256:foreign": {ID: "sha256:foreign", Tags: []string{"seaport-build-x:1"}, Created: old,
			Labels: map[string]string{hosting.LabelBase: "1", hosting.LabelOrganization: otherOrg.String(),
				hosting.LabelDigest: "abc", hosting.LabelBuilder: "another-port"}},
	}}
	tag := func(v string, off bool) func(*config.Server) {
		return func(c *config.Server) { c.ImageRness, c.KeepImages = "registry.test/seaport-rness:"+v, off }
	}
	opts := bootstrap.Options{Runtime: rt, SupervisePeriod: 100 * time.Millisecond, ImageGCEvery: 100 * time.Millisecond}

	// Off: nothing goes, not even an old base tag.
	p := startPortWith(t, tag("1", true), opts)
	time.Sleep(500 * time.Millisecond)
	if !rt.hasImage("sha256:base0") {
		t.Fatal("removed an image with SEAPORT_IMAGE_GC=off")
	}
	p.stop()

	// On: the old base tag goes at once; the current, custom and foreign
	// ones stay.
	p = joinPort(t, tag("1", false), opts)
	eventually(t, "the old base removed", func() bool { return !rt.hasImage("sha256:base0") })
	pm := p.agent("pm")
	p.setMachine(pm.ID, "rness", map[string]any{})
	hostPath := "/api/v1/agents/" + pm.ID.String() + "/hosting"
	if code := p.admin(http.MethodPut, hostPath, map[string]any{}, nil); code != http.StatusOK {
		t.Fatalf("host: %d", code)
	}
	rt.results <- "sha256:v1"
	if code := p.admin(http.MethodPost, hostPath+"/slots/1/start", nil, nil); code != http.StatusAccepted {
		t.Fatalf("start: %d", code)
	}
	eventually(t, "running on v1", func() bool {
		s := p.slot(pm.ID, 1)
		return s.State == hosting.SlotRunning && s.Image == "sha256:v1"
	})
	if img := rt.image("sha256:v1"); img.Labels[hosting.LabelBuilder] == "" {
		t.Fatalf("the build isn't labeled with the port: %+v", img)
	}
	rt.ageImages()
	time.Sleep(400 * time.Millisecond)
	if !rt.hasImage("sha256:v1") {
		t.Fatal("removed the image of a running slot")
	}
	if code := p.admin(http.MethodPost, hostPath+"/slots/1/stop", nil, nil); code != http.StatusAccepted {
		t.Fatalf("stop: %d", code)
	}
	eventually(t, "stopped", func() bool { return p.slot(pm.ID, 1).State == hosting.SlotStopped })
	time.Sleep(400 * time.Millisecond)
	if !rt.hasImage("sha256:v1") {
		t.Fatal("removed the wanted build of a stopped slot")
	}
	p.stop()

	// The upgrade: a new base. While the new build runs, the stopped
	// slot's previous image stays (a start runs on it meanwhile).
	rt.mu.Lock()
	rt.images["sha256:base2"] = base("sha256:base2", "registry.test/seaport-rness:2")
	rt.mu.Unlock()
	p = joinPort(t, tag("2", false), opts)
	eventually(t, "the old base tag removed", func() bool { return !rt.hasImage("sha256:base1") })
	time.Sleep(400 * time.Millisecond)
	if !rt.hasImage("sha256:v1") {
		t.Fatal("removed the previous image while the new one builds")
	}
	// Built: the previous one goes, with its record.
	rt.results <- "sha256:v2"
	eventually(t, "the previous build removed", func() bool { return !rt.hasImage("sha256:v1") })
	db := testDB(t)
	var builds []string
	if err := db.Select(&builds, `SELECT image FROM hosting_builds ORDER BY image`); err != nil {
		t.Fatal(err)
	}
	if len(builds) != 1 || builds[0] != "sha256:v2" {
		t.Fatalf("build records: %v", builds)
	}
	for _, id := range []string{"sha256:v2", "sha256:base2", "sha256:custom", "sha256:foreign"} {
		if !rt.hasImage(id) {
			t.Fatalf("removed %s", id)
		}
	}
	// A start finds the new build (results is empty: a build would hang).
	if code := p.admin(http.MethodPost, hostPath+"/slots/1/start", nil, nil); code != http.StatusAccepted {
		t.Fatalf("start on v2: %d", code)
	}
	eventually(t, "running on v2", func() bool {
		s := p.slot(pm.ID, 1)
		return s.State == hosting.SlotRunning && s.Image == "sha256:v2"
	})

	// An image the engine refuses (a container still uses it) is kept and
	// tried again on the next run.
	rt.mu.Lock()
	rt.images["sha256:base3"] = base("sha256:base3", "registry.test/seaport-rness:3")
	rt.inUse = map[string]bool{"sha256:base3": true}
	rt.mu.Unlock()
	eventually(t, "refused twice", func() bool { return rt.triesOf("registry.test/seaport-rness:3") >= 2 })
	if !rt.hasImage("sha256:base3") {
		t.Fatal("removed an image in use")
	}
	rt.mu.Lock()
	rt.inUse = nil
	rt.mu.Unlock()
	eventually(t, "removed once free", func() bool { return !rt.hasImage("sha256:base3") })
	if !rt.removedImage("registry.test/seaport-rness:3") {
		t.Fatal("not removed by its tag")
	}
}
