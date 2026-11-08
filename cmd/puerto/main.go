// Command puerto is the port (serve), its admin CLI and the agent connector.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/Abraxas-365/puerto/internal/bootstrap"
	"github.com/Abraxas-365/puerto/internal/config"
	"github.com/Abraxas-365/puerto/internal/connector"
	"github.com/Abraxas-365/puerto/internal/conversation"
	"github.com/Abraxas-365/puerto/internal/errx"
)

const usage = `puerto - a port where messages arrive and agents work them

Server:
  puerto serve                         run the port (env: DATABASE_URL, PUERTO_ADMIN_TOKEN, PUERTO_CREW_TOKEN, ...)
  puerto healthcheck                   exit 0 if the local port answers /healthz (containers)

Admin (env: PUERTO_URL, PUERTO_ADMIN_TOKEN):
  puerto dock add whatsapp [-name N]   link a WhatsApp number (shows a QR to scan)
  puerto dock add api [-name N]        a dock fed through the HTTP API
  puerto dock list
  puerto dock relink <dock-id>
  puerto dock rename <dock-id> <name>
  puerto dock unlink <dock-id>         log the number out (keeps history; relink to pair again)
  puerto dock delete <dock-id> -yes    unlink and delete with all its conversations and ships
  puerto crew                          connected agents and what they are working on
  puerto conversations [-allowed|-pending|-denied]
  puerto allow <conversation-id>       always let the agent receive and answer this chat
  puerto deny <conversation-id>        never (both override the dock's rules)
  puerto auto <conversation-id>        follow the dock's rules again
  puerto mode <conversation-id> mentions|all|default   group: wake on mentions only, or on everything
  puerto customs [-dock ID]            show the dock's customs; subcommands:
         default ask|allow|deny | groups mentions|all | allow <pattern> | deny <pattern> | rm <rule-id>
  puerto history <conversation-id> [-limit N]
  puerto send <conversation-id> <text>

Agent (env: PUERTO_URL, PUERTO_CREW_TOKEN):
  puerto connect [flags] -- <agent command>   e.g. puerto connect -- rness --acp
`

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, os.Args[1:]); err != nil {
		var e *errx.Error
		if errors.As(err, &e) {
			fmt.Fprintln(os.Stderr, "puerto:", e.Message)
		} else {
			fmt.Fprintln(os.Stderr, "puerto:", err)
		}
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string) error {
	if len(args) == 0 || args[0] == "-h" || args[0] == "--help" || args[0] == "help" {
		fmt.Print(usage)
		return nil
	}
	cmd, rest := args[0], args[1:]
	switch cmd {
	case "serve":
		return serve(ctx)
	case "connect":
		return connect(ctx, rest)
	case "healthcheck":
		return healthcheck(ctx)
	case "version":
		fmt.Println(bootstrap.Version)
		return nil
	case "dock":
		if len(rest) == 0 {
			return errx.Validation("usage: puerto dock add|list|relink|rename|unlink|delete")
		}
		switch rest[0] {
		case "add":
			return dockAdd(ctx, rest[1:])
		case "list":
			return dockList(ctx)
		case "relink":
			return dockRelink(ctx, rest[1:])
		case "rename":
			return dockRename(ctx, rest[1:])
		case "unlink":
			return dockUnlink(ctx, rest[1:])
		case "delete":
			return dockDelete(ctx, rest[1:])
		}
		return errx.Validation("unknown dock command " + rest[0])
	case "crew":
		return crewStatus(ctx)
	case "conversations", "convs":
		return conversations(ctx, rest)
	case "allow":
		return setAccess(ctx, rest, conversation.AccessAllow)
	case "deny":
		return setAccess(ctx, rest, conversation.AccessDeny)
	case "auto":
		return setAccess(ctx, rest, conversation.AccessAuto)
	case "mode":
		return setGroupMode(ctx, rest)
	case "customs":
		return customs(ctx, rest)
	case "history":
		return history(ctx, rest)
	case "send":
		return send(ctx, rest)
	}
	return errx.Validation("unknown command " + cmd + " (puerto help)")
}

func setupLogging(level string) {
	var l slog.Level
	if err := l.UnmarshalText([]byte(level)); err != nil {
		l = slog.LevelInfo
	}
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: l})))
}

func serve(ctx context.Context) error {
	cfg, err := config.LoadServer()
	if err != nil {
		return err
	}
	setupLogging(cfg.LogLevel)
	if u, err := url.Parse(cfg.PublicURL); err == nil && u.Scheme == "http" && !config.Loopback(net.JoinHostPort(u.Hostname(), "0")) {
		slog.Warn("PUERTO_PUBLIC_URL is plain http on a non-local host: tokens and the session cookie "+
			"travel unencrypted; put Puerto behind TLS (see deploy/)", "public_url", cfg.PublicURL)
	}
	app, err := bootstrap.Build(ctx, cfg, bootstrap.Options{})
	if err != nil {
		return err
	}
	defer app.Close()
	app.Start(ctx)
	return app.Serve(ctx)
}

// healthcheck asks the port on PUERTO_ADDR for /healthz; distroless images
// have no curl, so the binary checks itself.
func healthcheck(ctx context.Context) error {
	addr := envOr("PUERTO_ADDR", "127.0.0.1:8740")
	if host, port, err := net.SplitHostPort(addr); err == nil && (host == "" || host == "0.0.0.0" || host == "::") {
		addr = net.JoinHostPort("127.0.0.1", port)
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+addr+"/healthz", nil)
	if err != nil {
		return err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("healthz answered %d", resp.StatusCode)
	}
	return nil
}

func connect(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("connect", flag.ContinueOnError)
	server := fs.String("server", envOr("PUERTO_URL", "http://127.0.0.1:8740"), "port URL")
	token := fs.String("token", os.Getenv("PUERTO_CREW_TOKEN"), "crew token (default $PUERTO_CREW_TOKEN; none for a PUERTO_AUTH=none port)")
	name := fs.String("name", "", "agent name shown by the port (default: command name)")
	concurrency := fs.Int("concurrency", 2, "conversations worked at once")
	autoReply := fs.Bool("auto-reply", false, "send the agent's final text as the reply (for agents without MCP)")
	approve := fs.String("approve", "reject", "answer to agent permission requests: reject|allow")
	deliver := fs.String("deliver", "steer", "messages arriving mid-turn: steer (into the running turn, if the agent supports it) | queue (next turn)")
	cwd := fs.String("cwd", "", "agent working directory (default: current)")
	verbose := fs.Bool("v", false, "debug logs")
	fs.Usage = func() {
		fmt.Fprintln(os.Stderr, "usage: puerto connect [flags] -- <agent command>")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return err
	}
	command := fs.Args()
	if len(command) == 0 {
		fs.Usage()
		return errx.Validation("an agent command is required, e.g. puerto connect -- rness --acp")
	}
	if *deliver != connector.DeliverSteer && *deliver != connector.DeliverQueue {
		return errx.InvalidField("deliver", "-deliver must be steer or queue")
	}
	if *approve != "reject" && *approve != "allow" {
		return errx.InvalidField("approve", "-approve must be reject or allow")
	}
	if *name == "" {
		*name = filepath.Base(command[0])
	}
	dir := *cwd
	if dir == "" {
		var err error
		if dir, err = os.Getwd(); err != nil {
			return errx.Wrap(err, "working directory", errx.TypeInternal)
		}
	}
	dir, _ = filepath.Abs(dir)
	level := "info"
	if *verbose {
		level = "debug"
	}
	setupLogging(level)
	cfgDir, _ := os.UserConfigDir()
	return connector.Run(ctx, connector.Config{
		Server: strings.TrimRight(*server, "/"), Token: *token, AgentName: *name, Concurrency: *concurrency,
		AutoReply: *autoReply, Approve: *approve, Deliver: *deliver, Cwd: dir, Command: command,
		SessionsFile: filepath.Join(cfgDir, "puerto", "sessions.json"),
	})
}

func envOr(key, def string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return def
}
