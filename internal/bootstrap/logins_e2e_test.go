package bootstrap_test

import (
	"net/http"
	"testing"
	"time"

	"github.com/Abraxas-365/seaport/internal/bootstrap"
	"github.com/Abraxas-365/seaport/internal/config"
	"github.com/Abraxas-365/seaport/internal/hosting"
)

// A Claude Code slot with no model key signs in to a subscription from
// Seaport: the harness's login runs in its container, its authorize URL
// shows, the pasted code goes to it, and the account then reads signed
// in. A bad code fails the sign-in with the harness's reason; a slot that
// isn't running, an environment with a model key or an rness one can't
// sign in; its workspace tells agent-run to wait for a sign-in.
func TestSlotSignIn(t *testing.T) {
	rt := &fakeRuntime{results: make(chan string, 4)}
	p := startPortWith(t, func(c *config.Server) {
		c.SecretKey = "e2e-login-secrets-key-0123456789abcdefXYZ"
		c.ImageClaudeCode = "registry.test/seaport-claude-code:1"
		c.ImageRness = "registry.test/seaport-rness:1"
		c.AgentURL = "http://port.test:8740"
	}, bootstrap.Options{Runtime: rt, SupervisePeriod: time.Hour})
	events := p.openEvents("")
	cc := p.agent("cc")
	var env hosting.Environment
	if code := p.admin(http.MethodPost, "/api/v1/environments", map[string]any{"name": "sub",
		"spec": map[string]any{"harness": "claude-code"}}, &env); code != http.StatusCreated {
		t.Fatalf("create environment: %d", code)
	}
	hostPath := "/api/v1/agents/" + cc.ID.String() + "/hosting"
	if code := p.admin(http.MethodPut, hostPath, map[string]any{"environment": "sub", "slots": 2}, nil); code != http.StatusOK {
		t.Fatalf("host: %d", code)
	}
	login := hostPath + "/slots/1/login"

	// Stopped: no sign-in, no account.
	if code := p.admin(http.MethodPost, login, nil, nil); code != http.StatusUnprocessableEntity {
		t.Fatalf("sign in a stopped slot: %d", code)
	}
	if code := p.admin(http.MethodGet, hostPath+"/slots/1/account", nil, nil); code != http.StatusUnprocessableEntity {
		t.Fatalf("account of a stopped slot: %d", code)
	}
	if code := p.admin(http.MethodGet, login, nil, nil); code != http.StatusNotFound {
		t.Fatalf("no sign-in yet: %d", code)
	}

	rt.results <- "sha256:cc1"
	if code := p.admin(http.MethodPost, hostPath+"/slots/1/start", nil, nil); code != http.StatusAccepted {
		t.Fatalf("start: %d", code)
	}
	next(t, events, hosting.EventSlotsUpdated, slotState(1, hosting.SlotRunning))
	st := rt.lastStart()
	var ws hosting.Workspace
	if code := p.call(http.MethodGet, "/crew/workspace", st.Env[hosting.EnvToken], nil, &ws); code != http.StatusOK || !ws.SignIn {
		t.Fatalf("workspace of a subscription slot: %d %+v", code, ws)
	}
	var acc hosting.Account
	if code := p.admin(http.MethodGet, hostPath+"/slots/1/account", nil, &acc); code != http.StatusOK ||
		acc.Slot != 1 || !acc.Supported || acc.LoggedIn || acc.Method != "none" {
		t.Fatalf("account before: %d %+v", code, acc)
	}

	// No sign-in waits for a code yet.
	if code := p.admin(http.MethodPost, login+"/code", map[string]string{"code": "good-code"}, nil); code != http.StatusConflict {
		t.Fatalf("code with no sign-in: %d", code)
	}

	// A bad code: the harness refuses, the sign-in fails with its reason.
	var l hosting.Login
	if code := p.admin(http.MethodPost, login, nil, &l); code != http.StatusAccepted || l.State != hosting.LoginStarting || l.Slot != 1 {
		t.Fatalf("start sign-in: %d %+v", code, l)
	}
	first := l.ID
	waitLogin(t, p, login, hosting.LoginWaiting)
	// Starting again while one is going answers that one.
	if code := p.admin(http.MethodPost, login, nil, &l); code != http.StatusAccepted || l.ID != first {
		t.Fatalf("start sign-in again: %d %+v", code, l)
	}
	if code := p.admin(http.MethodPost, login+"/code", map[string]string{"code": "has space"}, nil); code != http.StatusBadRequest {
		t.Fatalf("code with a space: %d", code)
	}
	if code := p.admin(http.MethodPost, login+"/code", map[string]string{"code": "bad-code"}, nil); code != http.StatusAccepted {
		t.Fatalf("submit bad code: %d", code)
	}
	l = waitLogin(t, p, login, hosting.LoginFailed)
	if l.Detail != "Login failed: Request failed with status code 400" {
		t.Fatalf("failed sign-in detail: %+v", l)
	}

	// Again, with the good code: signed in.
	if code := p.admin(http.MethodPost, login, nil, &l); code != http.StatusAccepted || l.ID == first {
		t.Fatalf("start a new sign-in: %d %+v", code, l)
	}
	l = waitLogin(t, p, login, hosting.LoginWaiting)
	if l.URL != fakeLoginURL || l.CreatedBy == "" {
		t.Fatalf("waiting sign-in: %+v", l)
	}
	if code := p.admin(http.MethodPost, login+"/code", map[string]string{"code": "good-code"}, nil); code != http.StatusAccepted {
		t.Fatalf("submit code: %d", code)
	}
	waitLogin(t, p, login, hosting.LoginDone)
	if code := p.admin(http.MethodGet, hostPath+"/slots/1/account", nil, &acc); code != http.StatusOK ||
		!acc.LoggedIn || acc.Method != "claude.ai" || acc.Subscription != "max" {
		t.Fatalf("account after: %d %+v", code, acc)
	}

	// An environment with a model key (it wins) or rness: no sign-in.
	if code := p.admin(http.MethodPut, "/api/v1/agents/"+cc.ID.String()+"/secrets/ANTHROPIC_KEY", map[string]any{"value": "sk-ant-x"}, nil); code != http.StatusOK {
		t.Fatalf("set secret: %d", code)
	}
	envPath := "/api/v1/environments/" + env.ID.String()
	if code := p.admin(http.MethodPut, envPath, map[string]any{"base": env.Version,
		"spec": map[string]any{"harness": "claude-code", "models": map[string]string{"anthropic": "${ANTHROPIC_KEY}"}}}, nil); code != http.StatusOK {
		t.Fatalf("save environment: %d", code)
	}
	if code := p.admin(http.MethodPost, login, nil, nil); code != http.StatusUnprocessableEntity {
		t.Fatalf("sign in with a model key: %d", code)
	}
	if code := p.admin(http.MethodPut, envPath, map[string]any{"base": env.Version + 1,
		"spec": map[string]any{"harness": "rness"}}, nil); code != http.StatusOK {
		t.Fatalf("save rness environment: %d", code)
	}
	if code := p.admin(http.MethodPost, login, nil, nil); code != http.StatusUnprocessableEntity {
		t.Fatalf("sign in rness: %d", code)
	}
}

func waitLogin(t *testing.T, p *port, path, state string) hosting.Login {
	t.Helper()
	var l hosting.Login
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if code := p.admin(http.MethodGet, path, nil, &l); code == http.StatusOK && l.State == state {
			return l
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("sign-in never %s: %+v", state, l)
	return l
}
