package hosting

import (
	"slices"
	"testing"
	"time"

	"github.com/Abraxas-365/seaport/internal/identity"
)

// ImagesToRemove: only this port's builds that nothing keeps, and older
// tags of the configured bases, past the grace; custom and pinned bases,
// other ports' builds and young images stay.
func TestImagesToRemove(t *testing.T) {
	now := time.Date(2026, 11, 22, 12, 0, 0, 0, time.UTC)
	old := now.Add(-2 * ImageGCGrace)
	org := identity.MustParseOrganizationID("00000000-0000-4000-8000-000000000001")
	build := func(id, digest, builder string, created time.Time) Image {
		labels := map[string]string{LabelBase: "1", LabelOrganization: org.String(), LabelDigest: digest}
		if builder != "" {
			labels[LabelBuilder] = builder
		}
		return Image{ID: id, Tags: []string{"seaport-build-x:" + digest[:4]}, Labels: labels, Created: created, Size: 10}
	}
	base := func(id string, created time.Time, tags ...string) Image {
		return Image{ID: id, Tags: tags, Labels: map[string]string{LabelBase: "1"}, Created: created, Size: 5}
	}
	images := []Image{
		build("sha256:running", "aaaa1", "port-a", old),    // a slot runs it
		build("sha256:unused", "bbbb1", "port-a", old),     // removed
		build("sha256:young", "cccc1", "port-a", now),      // in its grace
		build("sha256:other-port", "dddd1", "port-b", old), // another port's
		build("sha256:legacy-known", "eeee1", "", old),     // no builder label, our database's
		build("sha256:legacy-unknown", "ffff1", "", old),   // no builder label, not ours
		base("sha256:base-old", old, "ghcr.io/x/seaport-rness:0.12.0"),
		base("sha256:base-now", old, "ghcr.io/x/seaport-rness:0.13.0"),
		base("sha256:pinned", old, "ghcr.io/x/seaport-rness:0.11.0"),
		base("sha256:custom", old, "registry.test/mine:1"),
		base("sha256:two-tags", old, "ghcr.io/x/seaport-claude-code:0.12.0", "ghcr.io/x/seaport-claude-code:0.13.0"),
		base("sha256:dangling", old),
	}
	keep := ImageKeep{
		Port: "port-a",
		Kept: KeptImages{Images: []string{"sha256:running"}, Builds: []BuildKey{{Organization: org, Digest: "eeee1"}},
			Pinned: []string{"ghcr.io/x/seaport-rness:0.11.0"}},
		Bases: []string{"ghcr.io/x/seaport-rness:0.13.0", "ghcr.io/x/seaport-claude-code:0.13.0"},
	}
	got := ImagesToRemove(images, keep, now)
	var refs []string
	for _, r := range got {
		refs = append(refs, r.Ref)
	}
	want := []string{"sha256:legacy-known", "sha256:unused", "ghcr.io/x/seaport-claude-code:0.12.0", "ghcr.io/x/seaport-rness:0.12.0", "sha256:dangling"}
	if !slices.Equal(refs, want) {
		t.Fatalf("removals: %v, want %v", refs, want)
	}
	if got[1].Build == nil || got[1].Build.Digest != "bbbb1" || got[1].Build.Organization != org || got[1].Size != 10 {
		t.Fatalf("build removal: %+v", got[1])
	}
	if got[2].Build != nil {
		t.Fatalf("a base removal names a build: %+v", got[2])
	}
	// No port id: no labeled build is taken for ours.
	keep.Port = ""
	for _, r := range ImagesToRemove(images, keep, now) {
		if r.Ref == "sha256:unused" {
			t.Fatal("removed a build with no port id")
		}
	}
}

func TestRepository(t *testing.T) {
	for ref, want := range map[string]string{
		"ghcr.io/x/seaport-rness:0.13.0": "ghcr.io/x/seaport-rness",
		"localhost:5000/img:1":           "localhost:5000/img",
		"localhost:5000/img":             "",
		"img@sha256:abc":                 "",
		"img":                            "",
	} {
		got, ok := repository(ref)
		if got != want || ok != (want != "") {
			t.Errorf("repository(%q) = %q %v, want %q", ref, got, ok, want)
		}
	}
}
