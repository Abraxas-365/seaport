package hostingsvc

import (
	"context"
	"log/slog"
	"time"

	"github.com/Abraxas-365/seaport/internal/errx"
	"github.com/Abraxas-365/seaport/internal/hosting"
	"github.com/Abraxas-365/seaport/internal/identity"
)

// Warm builds: a hosted agent's image is built ahead of its work. A build
// is keyed by the machine and the port's base image (BuildRequest.Digest),
// so a new Seaport release (a new base tag) or a machine edit needs a new
// one; without this the first message after an upgrade waited for the
// whole build (a big machine takes many minutes). The supervisor records
// the build each hosted agent's config needs (Repository.WantBuild) and
// starts the missing ones, one at a time per process. Meanwhile a start
// runs on the slot's previous image of the same machine (imageFor), and a
// running slot whose image isn't the new build's is outdated once that is
// ready: the supervisor restarts it once idle.

const (
	// warmRetry: a build the supervisor started that failed is started
	// again by it only this long after (a start or `seaport agents build`
	// retries it at once).
	warmRetry = 30 * time.Minute
)

// warm is the supervisor's look at the hosted agents' images (at most once
// per supervise period: wakes don't add any): it records the build each
// one's current config needs and starts one missing build in the
// background (none while one it started still runs).
func (s *Service) warm(ctx context.Context, now time.Time) {
	b := s.builds
	b.mu.Lock()
	due := b.warmedAt.IsZero() || now.Sub(b.warmedAt) >= s.supervisePeriod()
	if due {
		b.warmedAt = now
	}
	b.mu.Unlock()
	if !due {
		return
	}
	agents, err := s.repo.HostedMachines(ctx)
	if err != nil {
		if ctx.Err() == nil {
			slog.Warn("warm builds: list hosted agents", "err", err)
		}
		return
	}
	for _, a := range agents {
		if ctx.Err() != nil {
			return
		}
		req, ok := s.warmRequest(a)
		if !ok {
			continue
		}
		digest := req.Digest()
		if a.Build != digest || a.BuildMachine != a.MachineDigest {
			if err := s.repo.WantBuild(ctx, a.Organization, a.Agent, digest, a.MachineDigest); err != nil {
				slog.Warn("warm builds: record the agent's build", "agent", a.Agent, "err", err)
				continue
			}
		}
		s.warmBuild(ctx, a.Agent, req, now)
	}
}

// warmRequest is the build an agent's current config needs (false when
// it can't be worked out: the start reports why).
func (s *Service) warmRequest(a hosting.HostedMachine) (hosting.BuildRequest, bool) {
	spec, err := hosting.SpecOf(a.Harness, a.Machine)
	if err != nil {
		return hosting.BuildRequest{}, false
	}
	req, err := s.buildRequest(a.Organization, hosting.Normalize(spec))
	if err != nil {
		return hosting.BuildRequest{}, false
	}
	return req, true
}

// warmBuild starts req's build in the background unless it is built,
// building (here or in another process), one this process started is
// still running, or this process started it and it failed less than
// warmRetry ago. The record is per process on purpose: a build a deploy
// interrupted reads as failed, and the new process retries it at once.
func (s *Service) warmBuild(ctx context.Context, agentID identity.AgentID, req hosting.BuildRequest, now time.Time) {
	b := s.builds
	digest := req.Digest()
	key := req.Organization.String() + "/" + digest
	existing, err := s.repo.Build(ctx, req.Organization, digest)
	switch {
	case err != nil && !errx.IsType(err, errx.TypeNotFound):
		if ctx.Err() == nil {
			slog.Warn("warm builds: read the build", "agent", agentID, "err", err)
		}
		return
	case err == nil && existing.State == hosting.BuildReady:
		return
	case err == nil && existing.State == hosting.BuildBuilding && now.Sub(existing.UpdatedAt) < hosting.BuildStale:
		return
	case err == nil && existing.State == hosting.BuildFailed:
		b.mu.Lock()
		at, tried := b.tried[key]
		b.mu.Unlock()
		if tried && now.Sub(at) < warmRetry {
			return
		}
	}
	if !b.warming.CompareAndSwap(false, true) {
		return
	}
	if b.ctx.Err() != nil { // closing: start nothing new
		b.warming.Store(false)
		return
	}
	claimed, err := s.claimWarm(ctx, agentID, req, key, now)
	if err != nil || !claimed {
		b.warming.Store(false)
		if err != nil && ctx.Err() == nil {
			slog.Warn("warm builds: claim", "agent", agentID, "err", err)
		}
		return
	}
	slog.Info("building an agent's image ahead of its work", "agent", agentID, "digest", digest[:12], "base", req.Base)
	s.background(func(context.Context) {
		defer b.warming.Store(false)
		s.runBuild(req)
		s.Wake(req.Organization, agentID) // restart its outdated idle slots now
	})
}

func (s *Service) claimWarm(ctx context.Context, agentID identity.AgentID, req hosting.BuildRequest, key string, now time.Time) (bool, error) {
	build, claimed, err := s.repo.ClaimBuild(ctx, req.Organization, req.Digest(), agentID, req.Base, false)
	if err != nil || !claimed {
		return false, err
	}
	s.builds.mu.Lock()
	for k, at := range s.builds.tried { // keep it small: older ones may retry anyway
		if now.Sub(at) >= warmRetry {
			delete(s.builds.tried, k)
		}
	}
	s.builds.tried[key] = now
	s.builds.mu.Unlock()
	s.events.Publish(req.Organization, hosting.EventAgentBuild, withoutLog(build))
	return true, nil
}

// supervisePeriod is how often the supervisor runs a round.
func (s *Service) supervisePeriod() time.Duration {
	if p := s.slots.cfg.Period; p > 0 {
		return p
	}
	return hosting.SupervisePeriod
}
