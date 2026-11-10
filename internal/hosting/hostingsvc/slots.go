package hostingsvc

import (
	"context"
	"errors"
	"log/slog"
	"net/url"
	"sync"
	"time"

	"github.com/Abraxas-365/seaport/internal/agent"
	"github.com/Abraxas-365/seaport/internal/crew"
	"github.com/Abraxas-365/seaport/internal/errx"
	"github.com/Abraxas-365/seaport/internal/hosting"
	"github.com/Abraxas-365/seaport/internal/identity"
)

// Slots holds what running slots needs besides the builder's runtime.
type Slots struct {
	// Credentials mint and revoke slot tokens (crew).
	Credentials hosting.Credentials
	// Kits reveal a slot's repo credentials (agent secrets).
	Kits agent.Kits
	// URL is how a slot's container reaches the port (SEAPORT_AGENT_URL).
	URL string
	// Period overrides hosting.SupervisePeriod (tests).
	Period time.Duration
}

// slots runs a Service's slot starts and stops in the background.
type slots struct {
	cfg   Slots
	mu    sync.Mutex
	port  string // LabelPort, read once
	poke  chan struct{}
	proxy string // the egress proxy's URL ("": off)
}

// ProxyAt sets the egress proxy slots started from now on go through
// (its URL as they reach it; set once at wiring, before Start).
func (s *Service) ProxyAt(url string) {
	s.slots.mu.Lock()
	s.slots.proxy = url
	s.slots.mu.Unlock()
}

func (s *slots) egressProxy() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.proxy
}

func (s *Service) runtime() hosting.Runtime { return s.builds.cfg.Runtime }

func (s *Service) StartSlot(ctx context.Context, org identity.OrganizationID, input hosting.SlotAction) (hosting.Hosted, error) {
	if err := input.Validate(); err != nil {
		return hosting.Hosted{}, err
	}
	// Another organization's agent, or one not hosted, is NotFound first.
	if _, err := s.repo.Hosted(ctx, org, input.Agent); err != nil {
		return hosting.Hosted{}, err
	}
	if s.runtime() == nil {
		return hosting.Hosted{}, errx.Business("hosted agents are off on this port (SEAPORT_RUNTIME=off)")
	}
	if s.slots.cfg.Credentials == nil || s.slots.cfg.URL == "" {
		return hosting.Hosted{}, errx.Business("hosted agents need crew tokens (SEAPORT_AUTH=token or iam)")
	}
	start, claimed, err := s.repo.BeginStart(ctx, org, input.Agent, input.Index, false)
	if err != nil {
		return hosting.Hosted{}, errx.Wrap(err, "start slot", errx.TypeInternal)
	}
	if claimed {
		s.launch(org, input.Agent, input.Index, start)
	}
	return s.announce(ctx, org, input.Agent)
}

func (s *Service) StopSlot(ctx context.Context, org identity.OrganizationID, input hosting.SlotAction) (hosting.Hosted, error) {
	if err := input.Validate(); err != nil {
		return hosting.Hosted{}, err
	}
	h, claimed, err := s.repo.BeginStop(ctx, org, input.Agent, input.Index, "")
	if err != nil {
		return hosting.Hosted{}, errx.Wrap(err, "stop slot", errx.TypeInternal)
	}
	if claimed {
		s.events.Publish(org, hosting.EventSlotsUpdated, h)
		s.background(func(ctx context.Context) { s.finishStop(ctx, org, input.Agent, input.Index, "") })
	}
	return h, nil
}

// announce publishes and answers the agent's hosting.
func (s *Service) announce(ctx context.Context, org identity.OrganizationID, agentID identity.AgentID) (hosting.Hosted, error) {
	h, err := s.repo.Hosted(ctx, org, agentID)
	if err != nil {
		return h, err
	}
	s.events.Publish(org, hosting.EventSlotsUpdated, h)
	return h, nil
}

// background runs fn under the service's lifetime (Close cancels and
// waits).
func (s *Service) background(fn func(ctx context.Context)) {
	s.builds.wg.Add(1)
	go func() {
		defer s.builds.wg.Done()
		fn(s.builds.ctx)
	}()
}

func (s *Service) launch(org identity.OrganizationID, agentID identity.AgentID, idx int, start hosting.SlotStart) {
	s.background(func(ctx context.Context) { s.runStart(ctx, org, agentID, idx, start) })
}

// portID is this deployment's id (LabelPort), read once.
func (s *Service) portID(ctx context.Context) (string, error) {
	s.slots.mu.Lock()
	defer s.slots.mu.Unlock()
	if s.slots.port != "" {
		return s.slots.port, nil
	}
	id, err := s.repo.PortID(ctx)
	if err != nil {
		return "", err
	}
	s.slots.port = id
	return id, nil
}

// runStart brings a claimed slot up: the environment's image (built now
// when needed), a fresh slot token, the container. Any failure ends the
// run as failed with a detail; a slot stopped meanwhile (the run lost)
// takes down what this start made.
func (s *Service) runStart(ctx context.Context, org identity.OrganizationID, agentID identity.AgentID, idx int, start hosting.SlotStart) {
	ref := hosting.SlotRef{Organization: org, Agent: agentID, Index: idx}
	name := hosting.SlotName(start.AgentName, idx)
	if !start.OldToken.IsZero() { // a start that died left its token
		s.revoke(ctx, org, start.OldToken)
	}
	progress := func(detail string, token identity.CrewTokenID) error {
		return s.repo.StartProgress(ctx, org, agentID, idx, start.Run, detail, token)
	}
	// Keep the start fresh while the image builds (StartStale).
	alive, stopAlive := context.WithCancel(ctx)
	defer stopAlive()
	go func() {
		t := time.NewTicker(hosting.StartStale / 4)
		defer t.Stop()
		for {
			select {
			case <-alive.Done():
				return
			case <-t.C:
				_ = progress("building the environment's image", identity.CrewTokenID{})
			}
		}
	}()
	fail := func(detail string, err error, token identity.CrewTokenID) {
		stopAlive()
		slog.Warn("slot start failed", "slot", name, "detail", detail, "err", err)
		end := context.WithoutCancel(ctx)
		if ctx.Err() != nil {
			detail = "the port stopped while starting; start it again"
		}
		_ = s.runtime().Stop(end, ref, start.Run)
		if !token.IsZero() {
			s.revoke(end, org, token)
		}
		h, tok, ended, err := s.repo.EndRun(end, org, agentID, idx, hosting.EndRun{From: []string{hosting.SlotStarting},
			Run: start.Run, State: hosting.SlotFailed, Detail: detail})
		if err != nil {
			slog.Error("record slot start failure", "slot", name, "err", err)
			return
		}
		if !tok.IsZero() && tok != token {
			s.revoke(end, org, tok)
		}
		if ended {
			s.events.Publish(org, hosting.EventSlotsUpdated, h)
		}
	}
	env := start.Environment
	image, err := s.imageFor(ctx, org, env)
	if err != nil {
		fail(failure(err, "the environment's image could not be built"), err, identity.CrewTokenID{})
		return
	}
	port, err := s.portID(ctx)
	if err != nil {
		fail("could not read the port's id", err, identity.CrewTokenID{})
		return
	}
	expires := time.Now().Add(hosting.SlotTokenLifetime)
	issued, err := s.slots.cfg.Credentials.Create(ctx, org, crew.CreateToken{Name: name, Agent: agentID, Slot: idx,
		ExpiresAt: &expires, CreatedBy: "hosting"})
	if err != nil {
		fail("could not mint the slot's token", err, identity.CrewTokenID{})
		return
	}
	if err := progress("starting the container", issued.ID); err != nil {
		fail("stopped while starting", err, issued.ID) // stopped meanwhile: the token isn't recorded
		return
	}
	stopAlive()
	err = s.runtime().Start(ctx, hosting.StartSlot{Slot: ref, Run: start.Run, Port: port, Name: name, Image: image,
		Harness: env.Spec.Harness, Resources: env.Spec.Resources, Persistent: env.Spec.Workspace == hosting.WorkspacePersistent,
		Env: s.slotEnv(env.Spec, name, issued.Value)})
	if err != nil {
		fail(failure(err, "the container could not start"), err, issued.ID)
		return
	}
	h, err := s.repo.StartDone(ctx, org, agentID, idx, start.Run, hosting.SlotRun{Image: image, Environment: env.ID, Version: env.Version})
	if err != nil {
		// stopped meanwhile: the stop owns the slot now; take ours down
		end := context.WithoutCancel(ctx)
		_ = s.runtime().Stop(end, ref, start.Run)
		s.revoke(end, org, issued.ID)
		return
	}
	s.events.Publish(org, hosting.EventSlotsUpdated, h)
}

// slotEnv is a slot container's environment: the port, its token and
// name, the model gateway (when the environment names a model key: the
// harness's provider goes there with the slot token as its key) and the
// egress proxy (the slot signs in to it with its token).
func (s *Service) slotEnv(spec hosting.Spec, name, token string) map[string]string {
	env := map[string]string{
		hosting.EnvURL:     s.slots.cfg.URL,
		hosting.EnvToken:   token,
		hosting.EnvSlot:    name,
		hosting.EnvHarness: spec.Harness,
	}
	if spec.Models.KeyName(hosting.ProviderAnthropic) != "" {
		gw := s.slots.cfg.URL + hosting.GatewayPath
		env[hosting.EnvGateway] = gw
		env["ANTHROPIC_BASE_URL"] = gw + "/" + hosting.ProviderAnthropic
		env["ANTHROPIC_API_KEY"] = token
	}
	if p := s.slots.egressProxy(); p != "" {
		if u, err := url.Parse(p); err == nil {
			u.User = url.UserPassword(name, token)
			proxy := u.String()
			for _, k := range []string{"HTTPS_PROXY", "HTTP_PROXY", "https_proxy", "http_proxy"} {
				env[k] = proxy
			}
			noProxy := "localhost,127.0.0.1,::1"
			if pu, err := url.Parse(s.slots.cfg.URL); err == nil && pu.Hostname() != "" {
				noProxy += "," + pu.Hostname()
			}
			env["NO_PROXY"], env["no_proxy"] = noProxy, noProxy
		}
	}
	return env
}

// imageFor is the ready image of the environment's version, building it
// first (and waiting) when needed.
func (s *Service) imageFor(ctx context.Context, org identity.OrganizationID, env hosting.Environment) (string, error) {
	base := s.base(env.Spec)
	if base == "" {
		return "", errx.Business("no base image is configured for harness " + env.Spec.Harness)
	}
	for {
		b, claimed, err := s.repo.ClaimBuild(ctx, org, env.ID, env.Version, base, false)
		if err != nil {
			return "", err
		}
		if claimed {
			s.events.Publish(org, hosting.EventEnvironmentBuild, withoutLog(b))
			s.runBuild(hosting.BuildRequest{Organization: org, Environment: env.ID, Version: env.Version, Spec: env.Spec, Base: base})
			continue // read its outcome
		}
		switch b.State {
		case hosting.BuildReady:
			return b.Image, nil
		case hosting.BuildFailed:
			return "", errx.Business("the environment's image failed to build: " + b.Detail + " (seaport env build)")
		}
		select { // another process builds it: wait
		case <-ctx.Done():
			return "", ctx.Err()
		case <-time.After(2 * time.Second):
		}
	}
}

// failure is the detail of a failed start (no secrets: typed messages).
func failure(err error, fallback string) string {
	var e *errx.Error
	if errors.As(err, &e) && e.Type != errx.TypeInternal {
		return e.Message
	}
	return fallback
}

// finishStop takes a stopping slot down (run: only that run's container)
// and records it stopped.
func (s *Service) finishStop(ctx context.Context, org identity.OrganizationID, agentID identity.AgentID, idx int, detail string) {
	ref := hosting.SlotRef{Organization: org, Agent: agentID, Index: idx}
	end := context.WithoutCancel(ctx)
	if rt := s.runtime(); rt != nil {
		if err := rt.Stop(end, ref, ""); err != nil {
			slog.Warn("stop slot container", "agent", agentID, "slot", idx, "err", err)
			h, tok, ended, _ := s.repo.EndRun(end, org, agentID, idx, hosting.EndRun{From: []string{hosting.SlotStopping},
				State: hosting.SlotFailed, Detail: failure(err, "the container could not be stopped")})
			s.revoke(end, org, tok)
			if ended {
				s.events.Publish(org, hosting.EventSlotsUpdated, h)
			}
			return
		}
	}
	h, tok, ended, err := s.repo.EndRun(end, org, agentID, idx, hosting.EndRun{From: []string{hosting.SlotStopping},
		State: hosting.SlotStopped, Detail: detail})
	if err != nil {
		slog.Error("record slot stop", "agent", agentID, "slot", idx, "err", err)
		return
	}
	s.revoke(end, org, tok)
	if ended {
		s.events.Publish(org, hosting.EventSlotsUpdated, h)
	}
}

// revoke revokes a slot token (closing its connections); zero is none.
func (s *Service) revoke(ctx context.Context, org identity.OrganizationID, token identity.CrewTokenID) {
	if token.IsZero() || s.slots.cfg.Credentials == nil {
		return
	}
	if err := s.slots.cfg.Credentials.Revoke(ctx, org, token); err != nil && !errx.IsType(err, errx.TypeNotFound) {
		slog.Warn("revoke slot token", "token", token, "err", err)
	}
}

func (s *Service) OpenWorkspace(ctx context.Context, org identity.OrganizationID, agentID identity.AgentID, slot int) (hosting.Workspace, error) {
	if err := (hosting.SlotAction{Agent: agentID, Index: slot}).Validate(); err != nil {
		return hosting.Workspace{}, err
	}
	h, err := s.repo.Hosted(ctx, org, agentID)
	if err != nil {
		return hosting.Workspace{}, err
	}
	if !hasSlot(h, slot) {
		return hosting.Workspace{}, errx.NotFound("slot not found")
	}
	env, err := s.repo.Environment(ctx, org, h.Environment, 0)
	if err != nil {
		return hosting.Workspace{}, err
	}
	a, err := s.agents.Find(ctx, org, agentID)
	if err != nil {
		return hosting.Workspace{}, err
	}
	secrets := map[string]string{}
	if names := env.Spec.Secrets(); len(names) > 0 {
		if s.slots.cfg.Kits == nil {
			return hosting.Workspace{}, errx.Internal("no secrets to open repo credentials")
		}
		if secrets, err = s.slots.cfg.Kits.Reveal(ctx, org, agentID, names); err != nil {
			return hosting.Workspace{}, err
		}
	}
	w := hosting.Workspace{Slot: hosting.SlotName(a.Name, slot), Harness: env.Spec.Harness, RepoConfig: env.Spec.RepoConfig,
		Persistent: env.Spec.Workspace == hosting.WorkspacePersistent, SignIn: env.Spec.SignsIn(), Repos: make([]hosting.WorkspaceRepo, 0, len(env.Spec.Repos))}
	for _, r := range env.Spec.Repos {
		repo := hosting.WorkspaceRepo{URL: r.URL, Ref: r.Ref, Path: r.Path, OnStart: r.OnStart}
		if n := r.CredentialName(); n != "" {
			v, ok := secrets[n]
			if !ok {
				return hosting.Workspace{}, errx.Business("agent " + a.Name + " has no secret " + n + " (repo " + r.URL + ")")
			}
			repo.Token = v
		}
		w.Repos = append(w.Repos, repo)
	}
	return w, nil
}

func hasSlot(h hosting.Hosted, slot int) bool {
	for _, s := range h.Slots {
		if s.Index == slot {
			return true
		}
	}
	return false
}
