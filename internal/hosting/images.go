package hosting

import (
	"slices"
	"strings"
	"time"

	"github.com/Abraxas-365/seaport/internal/identity"
)

// Image garbage collection: every release changes the base tag, so every
// hosted agent gets a new build and the old one (several GB each) stays on
// the engine. The supervisor removes, at most every ImageGCEvery, the
// images nothing needs any more (ImagesToRemove): build images this port
// made that no slot runs and no hosted agent wants, and older tags of the
// port's configured base images. Removal never forces: the engine refuses
// an image a container (or a child image) still uses.

// ImageGCEvery is how often a process looks for unused images.
const ImageGCEvery = time.Hour

// ImageGCGrace: an image younger than this is never removed (a build in
// progress, an image a start is about to run).
const ImageGCGrace = time.Hour

// LabelBuilder names the port (LabelPort's id) that built an image, so
// ports sharing an engine leave each other's builds alone. Images built
// before it carry none: they count as this port's when its database knows
// their build (organization and digest).
const LabelBuilder = "io.seaport.builder"

// Image is an image the engine has that carries LabelBase (Seaport's base
// images and every image built FROM them).
type Image struct {
	ID string
	// Tags are its repo:tag names (none when dangling).
	Tags    []string
	Labels  map[string]string
	Created time.Time
	Size    int64
}

// BuildKey names a build: its organization and digest.
type BuildKey struct {
	Organization identity.OrganizationID `db:"organization_id"`
	Digest       string                  `db:"digest"`
}

// KeptImages is what the database says is still needed (system-wide: the
// engine's images are no organization's).
type KeptImages struct {
	// Images are the ids slots ran last and the ready builds some hosted
	// agent wants.
	Images []string
	// Builds are every build this database knows.
	Builds []BuildKey
	// Pinned are the images agents' current machines name (Machine.Image),
	// never removed even in a base's repository.
	Pinned []string
}

// ImageKeep is everything ImagesToRemove weighs.
type ImageKeep struct {
	// Port is this deployment's id (LabelBuilder).
	Port string
	Kept KeptImages
	// Bases are the port's base images (repo:tag): other tags of their
	// repositories are old releases.
	Bases []string
}

// ImageRemoval is an image (or one tag of it) to remove.
type ImageRemoval struct {
	// Ref is what to remove: a build's id, or an old base's repo:tag.
	Ref  string
	Size int64
	// Build is set for a build image: its record is forgotten first
	// (Repository.ForgetBuild), so a start doesn't take it for ready.
	Build *BuildKey
}

// ImagesToRemove picks the images nothing needs (see the package note):
// a build image this port made (LabelBuilder, or a build its database
// knows) that is not kept; a tag of a base's repository that is not a
// current base nor pinned; and a dangling base (no tag left: an old base
// whose builds went). All older than ImageGCGrace. Builds come first, so
// an old base whose builds went is deleted with its tag, not just
// untagged.
func ImagesToRemove(images []Image, keep ImageKeep, now time.Time) []ImageRemoval {
	kept := map[string]bool{}
	for _, id := range keep.Kept.Images {
		kept[id] = true
	}
	known := map[BuildKey]bool{}
	for _, b := range keep.Kept.Builds {
		known[b] = true
	}
	current := map[string]bool{}
	repos := map[string]bool{}
	for _, b := range keep.Bases {
		current[b] = true
		if repo, ok := repository(b); ok {
			repos[repo] = true
		}
	}
	for _, p := range keep.Kept.Pinned {
		current[p] = true
	}
	var builds, bases []ImageRemoval
	for _, img := range images {
		if now.Sub(img.Created) < ImageGCGrace || kept[img.ID] {
			continue
		}
		if org := img.Labels[LabelOrganization]; org != "" {
			key, ok := buildKey(org, img.Labels[LabelDigest])
			if !ok {
				continue
			}
			mine := img.Labels[LabelBuilder] == keep.Port && keep.Port != ""
			if img.Labels[LabelBuilder] == "" {
				mine = known[key]
			}
			if mine {
				builds = append(builds, ImageRemoval{Ref: img.ID, Size: img.Size, Build: &key})
			}
			continue
		}
		if len(img.Tags) == 0 { // dangling: nobody names it (the engine refuses one a child uses)
			bases = append(bases, ImageRemoval{Ref: img.ID, Size: img.Size})
			continue
		}
		for _, tag := range img.Tags {
			repo, ok := repository(tag)
			if ok && repos[repo] && !current[tag] {
				bases = append(bases, ImageRemoval{Ref: tag, Size: img.Size})
			}
		}
	}
	slices.SortFunc(builds, func(a, b ImageRemoval) int { return strings.Compare(a.Ref, b.Ref) })
	slices.SortFunc(bases, func(a, b ImageRemoval) int { return strings.Compare(a.Ref, b.Ref) })
	return append(builds, bases...)
}

func buildKey(org, digest string) (BuildKey, bool) {
	id, err := identity.ParseOrganizationID(org)
	if err != nil || digest == "" {
		return BuildKey{}, false
	}
	return BuildKey{Organization: id, Digest: digest}, true
}

// repository is an image reference without its tag ("" and false for a
// reference by digest or with no tag).
func repository(ref string) (string, bool) {
	if strings.Contains(ref, "@") {
		return "", false
	}
	i := strings.LastIndex(ref, ":")
	if i < 0 || strings.Contains(ref[i:], "/") { // a registry port, no tag
		return "", false
	}
	return ref[:i], true
}
