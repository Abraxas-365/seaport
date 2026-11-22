// Command seaport is the port (serve), its admin CLI and the agent connector.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"runtime/debug"
	"strings"
	"syscall"
	"time"

	"github.com/Abraxas-365/seaport/internal/bootstrap"
	"github.com/Abraxas-365/seaport/internal/config"
	"github.com/Abraxas-365/seaport/internal/connector"
	"github.com/Abraxas-365/seaport/internal/conversation"
	"github.com/Abraxas-365/seaport/internal/errx"
	"github.com/Abraxas-365/seaport/internal/hosting"
)

const usage = `seaport - a port where messages arrive and agents work them

Server:
  seaport serve                         run the port (env: DATABASE_URL, SEAPORT_AUTH=none|token|iam, SEAPORT_ADMIN_TOKEN, ...)
  seaport healthcheck                   exit 0 if the local port answers /healthz (containers)

Admin (env: SEAPORT_URL, SEAPORT_ADMIN_TOKEN = the admin token, or an IAMKit API key in iam mode):
  seaport dock add whatsapp [-name N]   link a WhatsApp number (shows a QR to scan)
  seaport dock add api [-name N]        a dock fed through the HTTP API
  seaport dock add github [-name N]     a GitHub App (created and installed from the web)
  seaport dock add jira [-name N]       a Jira Cloud service account (site and API token entered in the web)
  seaport dock add slack [-name N]      a Slack App (created or pasted in the web)
  seaport dock add webhook [-name N]    an endpoint any system can POST JSON to (a trigger: nothing is posted back)
  seaport dock webhook <dock-id> [-template F.json [-sample B.json]] [-rotate]
                                        show its URL and template; set the template (a preview of the sample is
                                        shown); -rotate makes a new secret and prints it once
  seaport dock add datadog [-name N]    an endpoint for Datadog's webhook integration (a trigger: alerts wake the crew)
  seaport dock datadog <dock-id> [-mirror <dock-id>:<destination> | -no-mirror] [-rotate]
                                        show its URL and the payload to paste in Datadog; -mirror names the
                                        destination where each new alert thread is opened; -rotate makes a new
                                        secret and prints it once
  seaport dock add email [-name N]      a mailbox read over IMAP and answered over SMTP (connect it with dock email)
  seaport dock email <dock-id> -address A -provider gmail|outlook|icloud | -imap H[:P] -smtp H[:P]  [-username U] [-folder F]
                                        connect an email dock (the password is read from the terminal or stdin)
  seaport dock list
  seaport dock relink <dock-id>
  seaport dock rename <dock-id> <name>
  seaport dock replies <dock-id> on|off  whether the crew may answer on it (off: a trigger; it still wakes the agent)
  seaport dock tools <dock-id> [history|conversations|attachments on|off ...]
                                        what agents may read there besides the messages they get (no switches: show them)
  seaport dock whatsapp <dock-id> [react on|off] [emoji <emoji>] [read on|off]
                                        what a WhatsApp dock does to a message an agent gets (nothing named: show it)
  seaport dock destinations <dock-id>   places where agents may open conversations
  seaport dock destination set <dock-id> <name> <target> [-title T]   a Slack channel id, a WhatsApp +number or group JID, an api chat id
  seaport dock destination rm <dock-id> <name>
  seaport dock open <dock-id> <name> [-thread-of CONV] <text>   open (or continue) the conversation at a destination
  seaport dock unlink <dock-id>         disconnect the channel (keeps history; relink to connect again)
  seaport dock delete <dock-id> -yes    unlink and delete with all its conversations and ships
  seaport crew                          connected agents and what they are working on
  seaport agents [list]                 your organization's agents and the docks they work
  seaport agents add <name> [-description D]
  seaport agents edit <agent> [-name N] [-description D]
  seaport agents assign <agent> <dock-id> [-default]   with several agents on a dock, @name calls one;
                                        the default takes messages that call nobody (else they are held)
  seaport agents unassign <agent> <dock-id>
  seaport agents delete <agent> -yes    revoke its tokens, disconnect it, hold what waits for it
  seaport agents clone <agent> <name> [-secrets] [-description D]   a new agent with the same config
                                        (hosted the same, stopped); -secrets copies its secrets too
  seaport agents config [show] <agent> [-version N]   its whole config as YAML: harness, instructions,
                                        skills, MCP servers, model, browser and the machine it runs on when hosted
  seaport agents config apply <agent> -f agent.yaml   save it (the format of show; - = stdin; an unchanged
                                        file makes no version; machine.home: ./dir uploads that dir first)
  seaport agents config versions <agent>
  seaport agents profile [show] <agent> [-version N] [-json]   what its harness is told and given
  seaport agents profile set <agent> [-f profile.json] [-instructions-file F] [-skills-dir D]
         [-skill SKILL.md]... [-rm-skill NAME]... [-approve allow|reject|connector] [-model M] [-effort E] [-browser on|off]
  seaport agents profile versions <agent>
  seaport agents secrets [list] <agent>
  seaport agents secrets set <agent> NAME [-env]   value from stdin; MCP headers and the machine
                                        (model keys, repo credentials) name it as ${NAME}
  seaport agents secrets rm <agent> NAME
  seaport schedules [list [conversation-id]]   prompts the port posts into conversations at set times
  seaport schedules add <conversation-id> <name> -cron "0 9 * * 1-5" -text "..." [-timezone America/Lima] [-agent A] [-disabled]
  seaport schedules edit <id> [-name N] [-cron C] [-timezone Z] [-text T] [-agent A|none]
  seaport schedules enable|disable <id>
  seaport schedules run <id>            post its message now (its schedule is untouched)
  seaport schedules preview "<cron>" [-timezone Z]   the next times an expression runs
  seaport schedules delete <id> -yes
  seaport vault [list]                  shared memory of the organization: markdown entries agents read and write
  seaport vault create <name> [-title T] [-description D]
  seaport vault show|update|delete <vault> [-title T] [-description D] [-yes]
  seaport vault grant <vault> (-agent A|-dock ID) -mode read|write     revoke <vault> (-agent A|-dock ID)
  seaport vault entries <vault> [-q words]     list or search      get <vault> <name>  print the text
  seaport vault put <vault> <name> (-file F|"text")   rm <vault> <name>   versions <vault> <name>   revert <vault> <name> <version>
  seaport vault pull <vault> <dir>      one <name>.md per entry
  seaport vault push <vault> <dir> [-prune]   create/update entries from <name>.md files
  seaport agents build <agent> [-force] build its machine's image (needs SEAPORT_RUNTIME=docker); follows the log
  seaport agents home push <agent> [dir] [-dry-run]   upload a harness home (default ~/.claude or ~/.rness:
                                        instructions, settings, agents, skills, hooks; never sessions,
                                        sign-in or memory) and save a config version naming it
  seaport agents home show <agent>      the files of its home
  seaport agents home pull <agent> <dir>   download its home
  seaport agents host <agent> [-slots N]   run the agent on the port, on its config's machine
  seaport agents unhost <agent>
  seaport agents autoscale <agent> on|off   start more slots when conversations wait
  seaport agents alerts <agent> [<dock-id>:<destination> [-wait 10m] | off]
                                        tell a chat when a slot or build fails or conversations wait (nothing: show it)
  seaport agents slots [<agent>]        hosted agents and the state of their slots
  seaport agents start|stop <agent>[-<n>]   start or stop a hosted slot (1 by default;
                                        new work also wakes a stopped agent)
  seaport agents logs <agent>[-<n>] [-lines N]   the tail of a running slot's output
  seaport agents usage <agent> [-days N]   model tokens it used through the port's gateway
  seaport agents egress <agent>         hosts the egress proxy refused its slots
  seaport agents login <agent>[-<n>]    sign a slot in to a Claude subscription (experimental)
  seaport agents account <agent>[-<n>]  how a running slot's harness is signed in
  seaport crew tokens [list]            agent credentials of your organization
  seaport crew tokens create <name> -agent <agent> [-expires 720h]   prints the token once
  seaport crew tokens revoke <token-id> revoke and disconnect its agents
  seaport conversations [-allowed|-pending|-denied]
  seaport allow <conversation-id>       always let the agent receive and answer this chat
  seaport deny <conversation-id>        never (both override the dock's rules)
  seaport auto <conversation-id>        follow the dock's rules again
  seaport mode <conversation-id> mentions|all|default   group: wake on mentions only, or on everything
  seaport customs [-dock ID]            show the dock's customs; subcommands:
         default ask|allow|deny | groups mentions|all | allow <pattern> | deny <pattern> | rm <rule-id>
  seaport history <conversation-id> [-limit N] [-thread]   -thread: every conversation of its thread
  seaport thread <conversation-id>      the conversations linked with it (one matter, several channels)
  seaport thread link <conversation-id> <conversation-id>   link two conversations (their threads merge)
  seaport thread unlink <conversation-id>                   take one out of its thread
  seaport send <conversation-id> <text>

Agent (env: SEAPORT_URL, SEAPORT_CREW_TOKEN, SEAPORT_AGENT):
  seaport connect [flags] -- <agent command>   e.g. seaport connect -- rness --acp
                                        works as the token's agent (-agent <name> without a token),
                                        with its profile: instructions, skills, MCP servers, secrets
                                        (-env: its env secrets as the harness's environment too)
  seaport github-token [owner/repo]     a GitHub App token for a repo of the agent's GitHub docks
                                        (default: the origin remote here); hosted slots' gh and git use it
  seaport reply <conversation-id> [-file PATH]... [text]
                                        reply as the agent, with files (reports, screenshots)
  seaport ask <conversation-id> -option TEXT -option TEXT... [-wait SECONDS] <question>
                                        ask people to choose before acting; prints "<id> <status> [answer]"
  seaport approval <approval-id> [-wait SECONDS]
                                        read an approval you asked (-wait blocks while it is pending)
`

// The CLI's streams; tests replace them to capture output and feed input.
var (
	stdin  io.Reader = os.Stdin
	stdout io.Writer = os.Stdout
	stderr io.Writer = os.Stderr
)

func main() {
	if bootstrap.Version == "dev" {
		// `go install …/cmd/seaport@vX` has no -ldflags; the module version
		// it was built from is in the build info.
		if bi, ok := debug.ReadBuildInfo(); ok && bi.Main.Version != "" && bi.Main.Version != "(devel)" {
			bootstrap.Version = bi.Main.Version
		}
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	run := run
	if filepath.Base(os.Args[0]) == "gh" {
		// The gh shim of hosted slots (/opt/seaport/bin/gh → seaport): gh
		// with a GitHub App token for the repo it is about.
		run = func(ctx context.Context, args []string) error { return connector.GH(ctx, args) }
	}
	if err := run(ctx, os.Args[1:]); err != nil {
		fmt.Fprintln(stderr, "seaport:", errorMessage(err))
		os.Exit(1)
	}
}

// errorMessage is what the CLI prints for a failed command: a typed
// error's message, else the error itself.
func errorMessage(err error) string {
	var e *errx.Error
	if errors.As(err, &e) {
		return e.Message
	}
	return err.Error()
}

func run(ctx context.Context, args []string) error {
	if len(args) == 0 || args[0] == "-h" || args[0] == "--help" || args[0] == "help" {
		fmt.Fprint(stdout, usage)
		return nil
	}
	cmd, rest := args[0], args[1:]
	switch cmd {
	case "serve":
		return serve(ctx)
	case "connect":
		return connect(ctx, rest)
	case "agent-run":
		// A hosted slot's entrypoint (Seaport's base images).
		setupLogging("info")
		cfg, err := connector.HostedFromEnv()
		if err != nil {
			return err
		}
		return connector.Hosted(ctx, cfg)
	case "agent-login":
		// a hosted slot's subscription sign-in (the port runs it in the
		// container and relays it).
		return connector.Login(ctx, stdin, stdout)
	case "browser-relay":
		// a hosted slot's browser for the port's viewer: CDP as lines on
		// stdin/stdout (the port runs it in the container). Nothing else
		// may reach stdout or stderr: both come back multiplexed.
		slog.SetDefault(slog.New(slog.NewTextHandler(io.Discard, nil)))
		return connector.BrowserRelay(ctx, stdin, stdout)
	case "agent-account":
		// how a hosted slot's harness is signed in (JSON; the port asks).
		return connector.AccountStatus(ctx, stdout)
	case "home-setup":
		// finishes an agent machine's harness home while its image is built
		// (hosting.Dockerfile): Seaport's lines kept, plugins installed.
		return connector.HomeSetup(ctx, os.Getenv(hosting.EnvHarness), os.Getenv("HOME"), stdout)
	case "git-credential":
		// git's credential helper inside a hosted slot (set up by agent-run).
		op := ""
		if len(rest) > 0 {
			op = rest[0]
		}
		return connector.GitCredential(ctx, op, stdin, stdout)
	case "reply":
		// an agent's reply with files from disk.
		return connector.ReplyCommand(ctx, rest, stdout)
	case "ask":
		// an agent asks people for an approval (optionally waiting for it).
		return connector.AskCommand(ctx, rest, stdout)
	case "approval":
		return connector.ApprovalCommand(ctx, rest, stdout)
	case "github-token":
		// a GitHub App token for a repo of the agent's GitHub docks
		// (GH_TOKEN=$(seaport github-token owner/repo) gh …).
		return connector.GitHubTokenCommand(ctx, rest, stdout)
	case "healthcheck":
		return healthcheck(ctx)
	case "version":
		fmt.Fprintln(stdout, bootstrap.Version)
		return nil
	case "dock":
		if len(rest) == 0 {
			return errx.Validation("usage: seaport dock add|list|relink|rename|replies|webhook|email|datadog|destinations|destination|open|unlink|delete")
		}
		switch rest[0] {
		case "add":
			return dockAdd(ctx, rest[1:])
		case "list":
			return dockList(ctx)
		case "email":
			return dockEmail(ctx, rest[1:])
		case "relink":
			return dockRelink(ctx, rest[1:])
		case "rename":
			return dockRename(ctx, rest[1:])
		case "replies":
			return dockReplies(ctx, rest[1:])
		case "tools":
			return dockTools(ctx, rest[1:])
		case "webhook":
			return dockWebhook(ctx, rest[1:])
		case "datadog":
			return dockDatadog(ctx, rest[1:])
		case "whatsapp":
			return dockWhatsApp(ctx, rest[1:])
		case "destinations":
			return dockDestinations(ctx, rest[1:])
		case "destination":
			return dockDestination(ctx, rest[1:])
		case "open":
			return dockOpen(ctx, rest[1:])
		case "unlink":
			return dockUnlink(ctx, rest[1:])
		case "delete":
			return dockDelete(ctx, rest[1:])
		}
		return errx.Validation("unknown dock command " + rest[0])
	case "schedules", "schedule", "cron":
		return schedules(ctx, rest)
	case "vault", "vaults":
		return vaultCmd(ctx, rest)
	case "agents", "agent":
		return agents(ctx, rest)
	case "crew":
		if len(rest) > 0 && rest[0] == "tokens" {
			return crewTokens(ctx, rest[1:])
		}
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
	case "thread", "threads":
		return thread(ctx, rest)
	case "send":
		return send(ctx, rest)
	}
	return errx.Validation("unknown command " + cmd + " (seaport help)")
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
		slog.Warn("SEAPORT_PUBLIC_URL is plain http on a non-local host: tokens and the session cookie "+
			"travel unencrypted; put Seaport behind TLS (see deploy/)", "public_url", cfg.PublicURL)
	}
	if os.Getenv("SEAPORT_CREW_TOKEN") != "" {
		slog.Warn("SEAPORT_CREW_TOKEN is ignored by the server: every agent now connects with a crew token of its own " +
			"(seaport agents add <name>; seaport crew tokens create <name> -agent <name>)")
	}
	app, err := bootstrap.Build(ctx, cfg, bootstrap.Options{})
	if err != nil {
		return err
	}
	defer app.Close()
	app.Start(ctx)
	return app.Serve(ctx)
}

// healthcheck asks the port on SEAPORT_ADDR for /healthz; distroless images
// have no curl, so the binary checks itself.
func healthcheck(ctx context.Context) error {
	addr := envOr("SEAPORT_ADDR", "127.0.0.1:8740")
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
	server := fs.String("server", envOr("SEAPORT_URL", "http://127.0.0.1:8740"), "port URL")
	token := fs.String("token", os.Getenv("SEAPORT_CREW_TOKEN"), "crew token (default $SEAPORT_CREW_TOKEN; none for a SEAPORT_AUTH=none port)")
	as := fs.String("agent", os.Getenv("SEAPORT_AGENT"), "agent to work as (default $SEAPORT_AGENT; optional with a crew token, which belongs to one; required for a SEAPORT_AUTH=none port)")
	name := fs.String("name", "", "label of this connection shown by the port (default: command name)")
	concurrency := fs.Int("concurrency", 2, "conversations worked at once")
	autoReply := fs.Bool("auto-reply", false, "send the agent's final text as the reply (for agents without MCP)")
	approve := fs.String("approve", "", "answer to agent permission requests: reject|allow (default: the agent profile's, else reject)")
	deliver := fs.String("deliver", "steer", "messages arriving mid-turn: steer (into the running turn, if the agent supports it) | queue (next turn)")
	cwd := fs.String("cwd", "", "agent working directory (default: current)")
	kitEnv := fs.Bool("env", false, "give the harness the agent's env secrets as environment variables (hosted slots always do)")
	verbose := fs.Bool("v", false, "debug logs")
	fs.Usage = func() {
		fmt.Fprintln(stderr, "usage: seaport connect [flags] -- <agent command>")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return err
	}
	command := fs.Args()
	if len(command) == 0 {
		fs.Usage()
		return errx.Validation("an agent command is required, e.g. seaport connect -- rness --acp")
	}
	if *deliver != connector.DeliverSteer && *deliver != connector.DeliverQueue {
		return errx.InvalidField("deliver", "-deliver must be steer or queue")
	}
	if *approve != "" && *approve != "reject" && *approve != "allow" {
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
		Server: strings.TrimRight(*server, "/"), Token: *token, As: *as, AgentName: *name, Concurrency: *concurrency,
		AutoReply: *autoReply, Approve: *approve, Deliver: *deliver, Cwd: dir, Command: command, KitEnv: *kitEnv,
		SessionsFile: filepath.Join(cfgDir, "seaport", "sessions.json"),
	})
}

func envOr(key, def string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return def
}
