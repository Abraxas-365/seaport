package hostingsvc

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Abraxas-365/seaport/internal/errx"
	"github.com/Abraxas-365/seaport/internal/hosting"
	"github.com/Abraxas-365/seaport/internal/identity"
	"github.com/Abraxas-365/seaport/internal/text"
)

// Subscription sign-in (experimental): the process that starts a sign-in
// runs the harness's login in the slot's container and relays it through
// slot_logins, so the URL shows and the code may be posted on any
// replica.

// codePoll is how often the relay looks for a submitted code.
const codePoll = time.Second

func (s *Service) StartLogin(ctx context.Context, org identity.OrganizationID, input hosting.SlotAction) (hosting.Login, error) {
	if err := input.Validate(); err != nil {
		return hosting.Login{}, err
	}
	if err := s.signsIn(ctx, org, input); err != nil {
		return hosting.Login{}, err
	}
	l, claimed, err := s.repo.BeginLogin(ctx, org, input.Agent, input.Index, identity.NewCrewTokenID().String(), text.Clean(input.By))
	if err != nil {
		return hosting.Login{}, err
	}
	if claimed {
		ref := hosting.SlotRef{Organization: org, Agent: input.Agent, Index: input.Index}
		s.background(func(ctx context.Context) { s.relayLogin(ctx, ref, l) })
	}
	return l, nil
}

// signsIn: the slot exists, the runtime is on and its environment's
// harness signs in from Seaport.
func (s *Service) signsIn(ctx context.Context, org identity.OrganizationID, input hosting.SlotAction) error {
	h, err := s.repo.Hosted(ctx, org, input.Agent)
	if err != nil {
		return err
	}
	if !slices.ContainsFunc(h.Slots, func(sl hosting.Slot) bool { return sl.Index == input.Index }) {
		return errx.NotFound("the agent has no slot " + strconv.Itoa(input.Index))
	}
	if s.runtime() == nil {
		return errx.Business("hosted agents are off on this port (SEAPORT_RUNTIME=off)")
	}
	env, err := s.repo.Environment(ctx, org, h.Environment, 0)
	if err != nil {
		return err
	}
	switch {
	case env.Spec.Harness != hosting.HarnessClaudeCode:
		return errx.Business("signing in from Seaport works with Claude Code only for now; " + env.Spec.Harness + " uses the environment's model key")
	case !env.Spec.SignsIn():
		return errx.Business("the environment gives its agents an API key (models.anthropic), which wins over a subscription: remove it to sign in")
	}
	return nil
}

// relayLogin runs the harness's login in the slot's container: its
// authorize URL goes to the sign-in, a submitted code to its stdin, its
// end to the sign-in's state. Whatever happens, the sign-in ends.
func (s *Service) relayLogin(ctx context.Context, ref hosting.SlotRef, l hosting.Login) {
	ctx, cancel := context.WithTimeout(ctx, hosting.LoginTimeout)
	defer cancel()
	org, agentID, idx := ref.Organization, ref.Agent, ref.Index
	report := func(u hosting.LoginUpdate) bool {
		if _, err := s.repo.LoginProgress(context.WithoutCancel(ctx), org, agentID, idx, l.ID, u); err != nil {
			if !errx.IsType(err, errx.TypeConflict) {
				slog.Warn("sign-in progress", "agent", agentID, "slot", idx, "err", err)
			}
			return false
		}
		return true
	}
	stdin, feed := io.Pipe()
	defer func() { _ = feed.Close() }()
	out := &loginOutput{onURL: func(u string) { report(hosting.LoginUpdate{State: hosting.LoginWaiting, URL: u}) }}

	// The code, once submitted, goes to the harness; the sign-in stays
	// fresh while it waits; a replaced or ended sign-in stops the relay.
	go func() {
		beat := time.Now()
		for {
			select {
			case <-ctx.Done():
				return
			case <-time.After(codePoll):
			}
			code, err := s.repo.TakeLoginCode(ctx, org, agentID, idx, l.ID)
			if err != nil {
				continue
			}
			if code != "" {
				if _, err := io.WriteString(feed, code+"\n"); err != nil {
					cancel()
					return
				}
				beat = time.Now()
				continue
			}
			if time.Since(beat) >= hosting.LoginBeat {
				beat = time.Now()
				if !report(hosting.LoginUpdate{}) {
					cancel()
					return
				}
			}
		}
	}()

	exit, err := s.runtime().Exec(ctx, ref, l.Run, hosting.LoginCommand, stdin, out)
	switch {
	case err == nil && exit == 0:
		report(hosting.LoginUpdate{State: hosting.LoginDone})
		slog.Info("slot signed in", "agent", agentID, "slot", idx)
	case errors.Is(ctx.Err(), context.DeadlineExceeded):
		report(hosting.LoginUpdate{State: hosting.LoginFailed, Detail: "the sign-in took too long; start it again"})
	case err != nil:
		report(hosting.LoginUpdate{State: hosting.LoginFailed, Detail: failure(err, "the sign-in could not run in the slot")})
	default:
		report(hosting.LoginUpdate{State: hosting.LoginFailed, Detail: hosting.LoginFailure(out.String(), exit)})
	}
}

func (s *Service) SubmitLoginCode(ctx context.Context, org identity.OrganizationID, input hosting.LoginCode) (hosting.Login, error) {
	if err := input.Validate(); err != nil {
		return hosting.Login{}, err
	}
	if _, err := s.repo.Hosted(ctx, org, input.Agent); err != nil {
		return hosting.Login{}, err
	}
	return s.repo.SubmitLoginCode(ctx, org, input.Agent, input.Index, strings.TrimSpace(input.Code))
}

func (s *Service) Login(ctx context.Context, org identity.OrganizationID, input hosting.SlotAction) (hosting.Login, error) {
	if err := input.Validate(); err != nil {
		return hosting.Login{}, err
	}
	l, err := s.repo.Login(ctx, org, input.Agent, input.Index)
	if err != nil {
		return l, err
	}
	if l.Live() && time.Since(l.UpdatedAt) > hosting.LoginStale {
		l.State, l.Detail = hosting.LoginFailed, "the sign-in stopped (the process relaying it ended); start it again"
	}
	return l, nil
}

func (s *Service) Account(ctx context.Context, org identity.OrganizationID, input hosting.SlotAction) (hosting.Account, error) {
	if err := input.Validate(); err != nil {
		return hosting.Account{}, err
	}
	h, err := s.repo.Hosted(ctx, org, input.Agent)
	if err != nil {
		return hosting.Account{}, err
	}
	i := slices.IndexFunc(h.Slots, func(sl hosting.Slot) bool { return sl.Index == input.Index })
	if i < 0 {
		return hosting.Account{}, errx.NotFound("the agent has no slot " + strconv.Itoa(input.Index))
	}
	if h.Slots[i].State != hosting.SlotRunning {
		return hosting.Account{}, errx.Business("the slot isn't running: start it to see its account")
	}
	rt := s.runtime()
	if rt == nil {
		return hosting.Account{}, errx.Business("hosted agents are off on this port (SEAPORT_RUNTIME=off)")
	}
	ctx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	var out bytes.Buffer
	ref := hosting.SlotRef{Organization: org, Agent: input.Agent, Index: input.Index}
	exit, err := rt.Exec(ctx, ref, "", hosting.AccountCommand, nil, &limited{w: &out, n: 64 << 10})
	if err != nil {
		return hosting.Account{}, err
	}
	var acc hosting.Account
	if exit != 0 || json.Unmarshal(lastLine(out.Bytes()), &acc) != nil {
		return hosting.Account{}, errx.External("the slot couldn't say how its agent is signed in: " + hosting.LoginFailure(out.String(), exit))
	}
	acc.Slot = input.Index
	return acc, nil
}

// lastLine is the last non-empty line of b (agent-account's JSON comes
// last, after any log lines).
func lastLine(b []byte) []byte {
	lines := bytes.Split(bytes.TrimSpace(b), []byte("\n"))
	return lines[len(lines)-1]
}

// loginOutput keeps the login's output (bounded) and reports the first
// authorize URL in it (whole lines only: a chunk may cut one).
type loginOutput struct {
	mu    sync.Mutex
	buf   []byte
	line  []byte
	seen  bool
	onURL func(string)
}

const maxLoginOutput = 16 << 10

func (o *loginOutput) Write(p []byte) (int, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.buf = append(o.buf, p...)
	if len(o.buf) > 2*maxLoginOutput {
		o.buf = append(o.buf[:0], o.buf[len(o.buf)-maxLoginOutput:]...)
	}
	for _, c := range p {
		if c != '\n' {
			if len(o.line) < maxLoginOutput {
				o.line = append(o.line, c)
			}
			continue
		}
		o.scan()
		o.line = o.line[:0]
	}
	return len(p), nil
}

func (o *loginOutput) scan() {
	if o.seen {
		return
	}
	if u := hosting.LoginURL(string(o.line)); u != "" {
		o.seen = true
		o.onURL(u)
	}
}

func (o *loginOutput) String() string {
	o.mu.Lock()
	defer o.mu.Unlock()
	return string(o.buf)
}

// limited writes at most n bytes to w, dropping the rest.
type limited struct {
	w io.Writer
	n int
}

func (l *limited) Write(p []byte) (int, error) {
	if l.n <= 0 {
		return len(p), nil
	}
	q := p
	if len(q) > l.n {
		q = q[:l.n]
	}
	l.n -= len(q)
	if _, err := l.w.Write(q); err != nil {
		return 0, err
	}
	return len(p), nil
}
