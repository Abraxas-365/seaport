package connector

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"time"

	"github.com/Abraxas-365/seaport/internal/errx"
	"github.com/Abraxas-365/seaport/internal/hosting"
)

// Subscription sign-in inside a hosted slot (experimental): the port runs
// `seaport agent-login` in the slot's container (Runtime.Exec) and relays
// its output and stdin; `seaport agent-account` answers how the harness
// is signed in. Tokens stay with the harness, in the state volume.

// signedInMark is touched in the state volume by a successful sign-in;
// agent-run watches it to restart the harness signed in.
const signedInMark = "connector/signed-in"

// Login runs the harness's own sign-in with this process's stdin and
// stdout (the port relays them) and marks a success for agent-run.
func Login(ctx context.Context, stdin io.Reader, stdout io.Writer) error {
	cfg, err := HostedFromEnv()
	if err != nil {
		return err
	}
	if cfg.Harness != hosting.HarnessClaudeCode {
		return errx.Business("signing in from Seaport works with Claude Code only for now")
	}
	// The relay closing stdin (it gave up, or its process ended) ends the
	// login: claude would wait on a closed stdin forever.
	ctx, cancel := context.WithTimeout(ctx, hosting.LoginTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "claude", "auth", "login", "--claudeai")
	// A file, not a reader: exec would wait on its copy goroutine (the
	// relay's stdin stays open) after claude exited.
	pr, pw, err := os.Pipe()
	if err != nil {
		return errx.Wrap(err, "sign in", errx.TypeInternal)
	}
	defer func() { _ = pr.Close() }()
	go func() {
		_, _ = io.Copy(pw, stdin)
		_ = pw.Close()
		// time to verify a code sent just before
		select {
		case <-ctx.Done():
		case <-time.After(30 * time.Second):
			cancel()
		}
	}()
	cmd.Stdin, cmd.Stdout, cmd.Stderr = pr, stdout, stdout
	if err := cmd.Run(); err != nil {
		return errx.Wrap(err, "sign in", errx.TypeExternal)
	}
	return markSignedIn(cfg)
}

func markSignedIn(cfg HostedConfig) error {
	p := filepath.Join(cfg.State, signedInMark)
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return errx.Wrap(err, "mark the sign-in", errx.TypeInternal)
	}
	if err := os.WriteFile(p, []byte(time.Now().UTC().Format(time.RFC3339Nano)), 0o600); err != nil {
		return errx.Wrap(err, "mark the sign-in", errx.TypeInternal)
	}
	return nil
}

// AccountStatus writes the harness's account (hosting.Account, JSON) to
// out.
func AccountStatus(ctx context.Context, out io.Writer) error {
	cfg, err := HostedFromEnv()
	if err != nil {
		return err
	}
	acc, err := harnessAccount(ctx, cfg, kitEnv(ctx, cfg))
	if err != nil {
		return err
	}
	return json.NewEncoder(out).Encode(acc)
}

// kitEnv is the environment the harness runs with: this process's plus
// the kit's env secrets (the slot's protected ones stay). A kit that
// can't be read adds nothing.
func kitEnv(ctx context.Context, cfg HostedConfig) []string {
	env := os.Environ()
	kit, err := fetchKit(ctx, Config{Server: cfg.Server, Token: cfg.Token})
	if err != nil || kit == nil {
		return env
	}
	protect := protected()
	for k, v := range kit.Env {
		if !slices.Contains(protect, k) {
			env = append(env, k+"="+v)
		}
	}
	return env
}

// harnessAccount asks the harness how it is signed in (with env, the
// harness's environment). A harness Seaport can't sign in answers
// Supported false.
func harnessAccount(ctx context.Context, cfg HostedConfig, env []string) (hosting.Account, error) {
	if cfg.Harness != hosting.HarnessClaudeCode {
		return hosting.Account{Method: "none"}, nil
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "claude", "auth", "status", "--json")
	cmd.Env = env
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = io.Discard
	_ = cmd.Run() // exits 1 when signed out, with the same JSON
	var st struct {
		LoggedIn         bool   `json:"loggedIn"`
		AuthMethod       string `json:"authMethod"`
		SubscriptionType string `json:"subscriptionType"`
	}
	if err := json.Unmarshal(out.Bytes(), &st); err != nil {
		return hosting.Account{}, errx.Wrap(err, "read Claude Code's account", errx.TypeExternal)
	}
	if st.AuthMethod == "" {
		st.AuthMethod = "none"
	}
	return hosting.Account{Supported: true, LoggedIn: st.LoggedIn, Method: st.AuthMethod, Subscription: st.SubscriptionType}, nil
}

// waitSignedIn holds a subscription slot until its harness is signed in
// (a sign-in from Seaport, or a token in the kit), so it never takes work
// it can only answer with "please log in". It checks at each sign-in mark
// and every signInPoll (a secret added to the kit).
func waitSignedIn(ctx context.Context, cfg HostedConfig) error {
	logged := false
	for {
		acc, err := harnessAccount(ctx, cfg, kitEnv(ctx, cfg))
		if err == nil && acc.LoggedIn {
			if logged {
				slog.Info("the agent is signed in; going to work", "method", acc.Method)
			}
			return nil
		}
		if !logged {
			slog.Warn("the agent isn't signed in to its subscription: sign it in from Seaport (Conectar cuenta, or `seaport agents login`); waiting", "err", err)
			logged = true
		}
		mark := markTime(cfg)
		deadline := time.Now().Add(signInPoll)
		for time.Now().Before(deadline) && markTime(cfg).Equal(mark) {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(markPoll):
			}
		}
	}
}

const (
	signInPoll = 30 * time.Second
	markPoll   = 2 * time.Second
)

func markTime(cfg HostedConfig) time.Time {
	fi, err := os.Stat(filepath.Join(cfg.State, signedInMark))
	if err != nil {
		return time.Time{}
	}
	return fi.ModTime()
}

// watchSignIns fires reload at each new sign-in mark until ctx ends.
func watchSignIns(ctx context.Context, cfg HostedConfig) <-chan struct{} {
	reload := make(chan struct{}, 1)
	go func() {
		last := markTime(cfg)
		for {
			select {
			case <-ctx.Done():
				return
			case <-time.After(markPoll):
			}
			if t := markTime(cfg); !t.Equal(last) {
				last = t
				select {
				case reload <- struct{}{}:
				default:
				}
			}
		}
	}()
	return reload
}
