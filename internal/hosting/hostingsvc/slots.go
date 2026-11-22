package hostingsvc

import (
	"context"
	"errors"
	"log/slog"
	"net/url"
	"strings"
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
	// Kits reveal a slot's repo credentials and model keys (the agent's
	// secrets).
	Kits agent.Kits
	// URL is how a slot's container reaches the port (SEAPORT_AGENT_URL).
	URL string
	// Period overrides hosting.SupervisePeriod (tests).
	Period time.Duration
	// Viewers tells a slot's connection that its browser's viewer opened
	// or closed (nil: nobody told).
	Viewers hosting.Viewers
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
	if err := s.checkSecrets(ctx, org, input.Agent); err != nil {
		return hosting.Hosted{}, err
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

// runStart brings a claimed slot up: the agent's image (built now when
// needed), a fresh slot token, the container. Any failure ends the
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
				_ = progress("building the agent's image", identity.CrewTokenID{})
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
	spec := start.Spec
	if err := s.missingSecrets(ctx, org, agentID, spec); err != nil {
		fail(failure(err, "the agent's secrets could not be read"), err, identity.CrewTokenID{})
		return
	}
	image, err := s.imageFor(ctx, org, agentID, spec, start.Previous)
	if err != nil {
		fail(failure(err, "the agent's image could not be built"), err, identity.CrewTokenID{})
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
	vars := s.slotEnv(spec, name, issued.Value)
	stopAlive()
	err = s.runtime().Start(ctx, hosting.StartSlot{Slot: ref, Run: start.Run, Port: port, Name: name, Image: image,
		Harness: spec.Harness, Resources: spec.Resources, Persistent: spec.Workspace == hosting.WorkspacePersistent,
		Env: vars})
	if err != nil {
		fail(failure(err, "the container could not start"), err, issued.ID)
		return
	}
	h, err := s.repo.StartDone(ctx, org, agentID, idx, start.Run, hosting.SlotRun{Image: image, ConfigVersion: start.ConfigVersion, Digest: start.Digest})
	if err != nil {
		// stopped meanwhile: the stop owns the slot now; take ours down
		end := context.WithoutCancel(ctx)
		_ = s.runtime().Stop(end, ref, start.Run)
		s.revoke(end, org, issued.ID)
		return
	}
	s.events.Publish(org, hosting.EventSlotsUpdated, h)
}

// checkSecrets refuses (Business) a start while the agent's config names
// a secret the agent doesn't have: a clone made without secrets, or one
// deleted behind the config's back.
func (s *Service) checkSecrets(ctx context.Context, org identity.OrganizationID, agentID identity.AgentID) error {
	spec, err := s.spec(ctx, org, agentID)
	if err != nil {
		return err
	}
	return s.missingSecrets(ctx, org, agentID, spec)
}

// missingSecrets is nil when every secret the machine names (model keys,
// repo credentials) is one of the agent's; else a Business error naming
// them (names only: never a value).
func (s *Service) missingSecrets(ctx context.Context, org identity.OrganizationID, agentID identity.AgentID, spec hosting.Spec) error {
	names := spec.Secrets()
	if len(names) == 0 {
		return nil
	}
	have, err := s.agents.Secrets(ctx, org, agentID)
	if err != nil {
		return err
	}
	set := make(map[string]bool, len(have))
	for _, h := range have {
		set[h.Name] = true
	}
	var missing []string
	for _, n := range names {
		if !set[n] {
			missing = append(missing, n)
		}
	}
	if len(missing) > 0 {
		return errx.Business("the agent's machine names secrets it doesn't have: " + strings.Join(missing, ", ") +
			" (seaport agents secrets set <agent> NAME; a clone made without -secrets starts with none)")
	}
	return nil
}

// slotEnv is a slot container's environment, lowest precedence first:
// the machine's plain variables, then what the port sets (the agent's env
// secrets come through the kit in the slot and win over the plain ones;
// the connector keeps the port's own from being replaced: Config.Protect):
// the port, its token and name, the model gateway (for every provider the
// machine names a key for: the harness's provider goes there with the
// slot token as its key), the provider it signs in to and the egress
// proxy (the slot signs in to it with its token).
func (s *Service) slotEnv(spec hosting.Spec, name, token string) map[string]string {
	env := map[string]string{}
	for k, v := range spec.Env {
		env[k] = v
	}
	env[hosting.EnvURL] = s.slots.cfg.URL
	env[hosting.EnvToken] = token
	env[hosting.EnvSlot] = name
	env[hosting.EnvHarness] = spec.Harness
	if spec.SignsIn() {
		env[hosting.EnvSignIn] = hosting.ProviderAnthropic
	}
	if providers := spec.Models.Providers(); len(providers) > 0 {
		env[hosting.EnvGateway] = s.slots.cfg.URL + hosting.GatewayPath
		env[hosting.EnvGatewayProviders] = strings.Join(providers, ",")
		for _, name := range providers {
			p := hosting.Providers[name]
			env[p.BaseEnv] = hosting.GatewayURL(s.slots.cfg.URL, name)
			env[p.KeyEnv] = token
		}
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

// imageFor is the ready image of the agent's spec, building it first (and
// waiting) when needed. Builds are keyed by content, so agents whose
// machines build the same share one. While it isn't ready (a new release
// changed the base image), previous (the image a slot of the agent last
// ran on this machine, when the engine still has it) is answered at once
// and the build goes on in the background: the slot restarts on the new
// image once it is ready and the slot idle (Slot.Outdated).
func (s *Service) imageFor(ctx context.Context, org identity.OrganizationID, agentID identity.AgentID, spec hosting.Spec, previous string) (string, error) {
	req, err := s.buildRequest(org, spec)
	if err != nil {
		return "", err
	}
	digest := req.Digest()
	if previous != "" {
		if ok, err := s.runtime().HasImage(ctx, previous); err != nil || !ok {
			previous = ""
		}
	}
	for {
		b, claimed, err := s.repo.ClaimBuild(ctx, org, digest, agentID, req.Base, false)
		if err != nil {
			return "", err
		}
		if claimed {
			s.events.Publish(org, hosting.EventAgentBuild, withoutLog(b))
			if previous != "" {
				slog.Info("starting a slot on its previous image while the new one builds", "agent", agentID, "digest", digest[:12])
				s.background(func(context.Context) {
					s.runBuild(req)
					s.Wake(org, agentID)
				})
				return previous, nil
			}
			s.runBuild(req)
			// Read its outcome (claiming again would rebuild a failed one).
			if b, err = s.repo.Build(ctx, org, digest); err != nil {
				return "", err
			}
		}
		switch b.State {
		case hosting.BuildReady:
			return b.Image, nil
		case hosting.BuildFailed:
			if previous != "" {
				slog.Warn("the agent's new image failed to build: starting on its previous one", "agent", agentID, "detail", b.Detail)
				return previous, nil
			}
			return "", errx.Business("the agent's image failed to build: " + b.Detail + " (seaport agents build)")
		}
		if previous != "" { // building elsewhere (the supervisor, another process)
			slog.Info("starting a slot on its previous image while the new one builds", "agent", agentID, "digest", digest[:12])
			return previous, nil
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
	spec, err := s.spec(ctx, org, agentID)
	if err != nil {
		return hosting.Workspace{}, err
	}
	a, err := s.agents.Find(ctx, org, agentID)
	if err != nil {
		return hosting.Workspace{}, err
	}
	var names []string
	for _, r := range spec.Repos {
		if n := r.CredentialName(); n != "" {
			names = append(names, n)
		}
	}
	secrets, err := s.reveal(ctx, org, agentID, names)
	if err != nil {
		return hosting.Workspace{}, err
	}
	w := hosting.Workspace{Slot: hosting.SlotName(a.Name, slot), Harness: spec.Harness, RepoConfig: spec.RepoConfig,
		Persistent: spec.Workspace == hosting.WorkspacePersistent, SignIn: spec.SignsIn(), Browser: spec.Browser.Engine,
		Concurrency: spec.Concurrency, Repos: make([]hosting.WorkspaceRepo, 0, len(spec.Repos))}
	for _, r := range spec.Repos {
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
