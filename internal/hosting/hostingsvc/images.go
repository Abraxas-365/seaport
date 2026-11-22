package hostingsvc

import (
	"context"
	"log/slog"
	"maps"
	"slices"
	"time"

	"github.com/Abraxas-365/seaport/internal/errx"
	"github.com/Abraxas-365/seaport/internal/hosting"
)

// collectImages is the image collector's turn of a supervisor round (see
// hosting/images.go): at most every ImageGCEvery per process, in the
// background, one at a time. Off with Builder.KeepImages.
func (s *Service) collectImages(now time.Time) {
	b := s.builds
	if b.cfg.KeepImages {
		return
	}
	b.mu.Lock()
	due := b.collectedAt.IsZero() || now.Sub(b.collectedAt) >= s.imageGCEvery()
	if due {
		b.collectedAt = now
	}
	b.mu.Unlock()
	if !due || !b.collecting.CompareAndSwap(false, true) {
		return
	}
	if b.ctx.Err() != nil {
		b.collecting.Store(false)
		return
	}
	s.background(func(ctx context.Context) {
		defer b.collecting.Store(false)
		if err := s.removeUnusedImages(ctx); err != nil && ctx.Err() == nil {
			slog.Warn("remove unused images", "err", err)
		}
	})
}

// imageGCEvery is how often the collector runs (Builder.GCEvery in tests).
func (s *Service) imageGCEvery() time.Duration {
	if e := s.builds.cfg.GCEvery; e > 0 {
		return e
	}
	return hosting.ImageGCEvery
}

// removeUnusedImages removes the images nothing needs once (hosting.
// ImagesToRemove). A build's record is forgotten before its image goes,
// so no start takes it for built; one the engine refuses (a container or
// child image uses it) is left for the next run.
func (s *Service) removeUnusedImages(ctx context.Context) error {
	rt := s.runtime()
	if rt == nil {
		return nil
	}
	port, err := s.portID(ctx)
	if err != nil {
		return err
	}
	images, err := rt.Images(ctx)
	if err != nil {
		return err
	}
	kept, err := s.repo.KeptImages(ctx)
	if err != nil {
		return err
	}
	bases := slices.Sorted(maps.Values(s.builds.cfg.Bases))
	var freed int64
	removed := 0
	for _, r := range hosting.ImagesToRemove(images, hosting.ImageKeep{Port: port, Kept: kept, Bases: bases}, time.Now()) {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if r.Build != nil {
			ok, err := s.repo.ForgetBuild(ctx, r.Build.Organization, r.Build.Digest, r.Ref)
			if err != nil {
				return err
			}
			if !ok { // needed again since the look
				continue
			}
		}
		deleted, err := rt.RemoveImage(ctx, r.Ref)
		switch {
		case errx.IsType(err, errx.TypeConflict):
			slog.Debug("unused image still in use: kept", "image", r.Ref)
			continue
		case err != nil:
			return err
		}
		removed++
		if deleted {
			freed += r.Size
		}
		slog.Info("removed an unused image", "image", r.Ref, "deleted", deleted)
	}
	if removed > 0 {
		slog.Info("removed unused images", "count", removed, "freed_mb", freed>>20)
	}
	return nil
}
