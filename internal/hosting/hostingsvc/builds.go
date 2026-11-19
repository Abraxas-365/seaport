package hostingsvc

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/Abraxas-365/seaport/internal/errx"
	"github.com/Abraxas-365/seaport/internal/hosting"
	"github.com/Abraxas-365/seaport/internal/identity"
)

// Builder holds what builds need: the runtime (nil = off), the harnesses'
// base images, and the context builds run under (ended by Close).
type Builder struct {
	Runtime hosting.Runtime
	// Bases maps a harness to its base image.
	Bases map[string]string
	// Timeout bounds one build (default 30 minutes).
	Timeout time.Duration
}

// builds runs the background builds of a Service.
type builds struct {
	cfg    Builder
	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup
}

func newBuilds(cfg Builder) *builds {
	if cfg.Timeout <= 0 {
		cfg.Timeout = 30 * time.Minute
	}
	ctx, cancel := context.WithCancel(context.Background())
	return &builds{cfg: cfg, ctx: ctx, cancel: cancel}
}

// Close cancels the builds in progress and waits for them to record their
// end (another process may build them again once they go stale).
func (s *Service) Close() {
	s.builds.cancel()
	s.builds.wg.Wait()
}

// base is the image a spec builds FROM.
func (s *Service) base(spec hosting.Spec) string {
	if spec.Image != "" {
		return spec.Image
	}
	return s.builds.cfg.Bases[spec.Harness]
}

// buildRequest is what building the agent's current config takes (its
// Digest keys the build).
func (s *Service) buildRequest(org identity.OrganizationID, spec hosting.Spec) (hosting.BuildRequest, error) {
	base := s.base(spec)
	if base == "" {
		return hosting.BuildRequest{}, errx.Business("no base image is configured for harness " + spec.Harness)
	}
	return hosting.BuildRequest{Organization: org, Spec: spec, Base: base}, nil
}

func (s *Service) StartBuild(ctx context.Context, org identity.OrganizationID, agentID identity.AgentID, input hosting.BuildAgent) (hosting.Build, error) {
	if agentID.IsZero() {
		return hosting.Build{}, errx.InvalidField("agent_id", "agent_id is required")
	}
	if err := input.Validate(); err != nil {
		return hosting.Build{}, err
	}
	spec, err := s.spec(ctx, org, agentID)
	if err != nil {
		return hosting.Build{}, err
	}
	if s.builds.cfg.Runtime == nil {
		return hosting.Build{}, errx.Business("hosted agents are off on this port (SEAPORT_RUNTIME=off)")
	}
	req, err := s.buildRequest(org, spec)
	if err != nil {
		return hosting.Build{}, err
	}
	req.Force = input.Force
	b, claimed, err := s.repo.ClaimBuild(ctx, org, req.Digest(), agentID, req.Base, input.Force)
	if err != nil {
		return hosting.Build{}, errx.Wrap(err, "claim build", errx.TypeInternal)
	}
	if !claimed {
		return b, nil
	}
	s.events.Publish(org, hosting.EventAgentBuild, withoutLog(b))
	s.builds.wg.Add(1)
	go func() {
		defer s.builds.wg.Done()
		s.runBuild(req)
	}()
	return b, nil
}

func withoutLog(b hosting.Build) hosting.Build {
	b.Log = ""
	return b
}

// buildLog collects a build's output; the builder stores its tail every
// few seconds (which also keeps the build from going stale).
type buildLog struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (l *buildLog) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.buf.Len() > 4*hosting.MaxBuildLog { // keep memory bounded: drop the head
		rest := l.buf.Bytes()[l.buf.Len()-hosting.MaxBuildLog:]
		l.buf = *bytes.NewBuffer(append([]byte(nil), rest...))
	}
	return l.buf.Write(p)
}

func (l *buildLog) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.buf.String()
}

// runBuild builds req and records the outcome; progress is stored every
// few seconds (BuildStale must stay well above that period).
func (s *Service) runBuild(req hosting.BuildRequest) {
	ctx, cancel := context.WithTimeout(s.builds.ctx, s.builds.cfg.Timeout)
	defer cancel()
	log := &buildLog{}
	digest := req.Digest()
	if req.Home == nil {
		home, err := s.buildHome(ctx, req.Organization, req.Spec)
		if err != nil {
			slog.Warn("agent build: home", "digest", digest, "err", err)
			s.finishBuild(req, digest, "", "the machine's home: "+buildFailure(ctx, err), log)
			return
		}
		req.Home = home
	}
	done := make(chan struct{})
	go func() {
		t := time.NewTicker(hosting.BuildStale / 6)
		defer t.Stop()
		for {
			select {
			case <-done:
				return
			case <-t.C:
				if err := s.repo.BuildProgress(ctx, req.Organization, digest, log.String()); err != nil &&
					ctx.Err() == nil {
					slog.Warn("store build progress", "digest", digest, "err", err)
				}
			}
		}
	}()
	image, err := s.builds.cfg.Runtime.Build(ctx, req, log)
	close(done)
	detail := ""
	if err != nil {
		image = ""
		detail = buildFailure(ctx, err)
		slog.Warn("agent build failed", "digest", digest, "err", err)
	}
	s.finishBuild(req, digest, image, detail, log)
}

// finishBuild records a build's end, even when shutting down (its own
// short deadline).
func (s *Service) finishBuild(req hosting.BuildRequest, digest, image, detail string, log *buildLog) {
	end, endCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer endCancel()
	b, ferr := s.repo.FinishBuild(end, req.Organization, digest, image, detail, log.String())
	if ferr != nil {
		slog.Error("record build end", "digest", digest, "err", ferr)
		return
	}
	s.events.Publish(req.Organization, hosting.EventAgentBuild, withoutLog(b))
}

// buildFailure is the detail a failed build shows.
func buildFailure(ctx context.Context, err error) string {
	switch {
	case errors.Is(ctx.Err(), context.DeadlineExceeded):
		return "the build took too long"
	case errors.Is(ctx.Err(), context.Canceled):
		return "the port stopped during the build; build again"
	}
	var e *errx.Error
	if errors.As(err, &e) {
		return e.Message
	}
	return "the build failed (see the log)"
}

func (s *Service) Build(ctx context.Context, org identity.OrganizationID, agentID identity.AgentID) (hosting.Build, error) {
	if agentID.IsZero() {
		return hosting.Build{}, errx.InvalidField("agent_id", "agent_id is required")
	}
	spec, err := s.spec(ctx, org, agentID)
	if err != nil {
		return hosting.Build{}, err
	}
	req, err := s.buildRequest(org, spec)
	if err != nil {
		return hosting.Build{}, err
	}
	return s.repo.Build(ctx, org, req.Digest())
}
