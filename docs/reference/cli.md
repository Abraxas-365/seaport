# CLI

Every command and flag of the seaport binary: serving the port, administering it from a terminal, and connecting an agent.

`seaport` is one binary. `seaport help`, `-h` or `--help` prints the command list; commands that take flags print theirs with `-h`. Flags use Go's style: a single dash (`-name N`), and they come **after** the positional arguments of a command (`seaport dock rename <id> <name>`, `seaport crew tokens create ci -agent ci`).

## Environment

| Variable | Used by | Meaning |
| :--- | :--- | :--- |
| `SEAPORT_URL` | admin commands, `connect` | Where the port is. Default `http://127.0.0.1:8740`. |
| `SEAPORT_ADMIN_TOKEN` | admin commands | Bearer token. The admin token in `token` mode, an IAMKit organization API key (`ik_key_…`) in `iam` mode. Not needed against a `none` port; without it a port that requires one answers 401. |
| `SEAPORT_CREW_TOKEN` | `connect` | The agent's crew token (`-token`). |
| `SEAPORT_AGENT` | `connect` | The agent to work as (`-agent`). |

`seaport serve` reads its own variables: see [Configuration](configuration.md). Admin commands are thin clients of the [HTTP API](http-api.md): each call times out after 30 seconds and answers are limited to 32 MiB. Errors print as `seaport: <message>` with exit code 1.

An `<agent>` argument takes the agent's id or its name (a leading `@` is allowed). Dock, conversation, rule and token arguments are ids (as shown by the matching `list`); environments are named.

## Server

| Command | What it does |
| :--- | :--- |
| `seaport serve` | Runs the port. Configured by the environment only. Stops cleanly on `SIGINT`/`SIGTERM`. |
| `seaport healthcheck` | Exits 0 if the local port answers `GET /healthz` with 200 within 3 seconds. Asks `SEAPORT_ADDR` (default `127.0.0.1:8740`; a wildcard host becomes `127.0.0.1`). It is the image's health check, since the image has no `curl`. |
| `seaport version` | Prints the version. |

## Docks

| Command | What it does |
| :--- | :--- |
| `seaport dock add whatsapp [-name N]` | Creates a WhatsApp dock and shows its QR code in the terminal until you scan it (waits up to 3 minutes; the code refreshes about every 20 s). `-name` defaults to the channel name. |
| `seaport dock add api [-name N]` | Creates an `api` dock, fed through the [HTTP API](../guides/api-docks.md). |
| `seaport dock add github [-name N]` | Creates a `github` dock and prints its page in the web app, where you create and install its [GitHub App](../guides/github.md). |
| `seaport dock list` | Table of docks: ID, channel, name, status, address (up to 100). |
| `seaport dock relink <dock-id>` | Pairs the dock again (shows the QR code). |
| `seaport dock rename <dock-id> <name>` | Renames it; the rest of the arguments make the name. |
| `seaport dock unlink <dock-id>` | Logs the number out. History stays; `relink` to pair again. |
| `seaport dock delete <dock-id> -yes` | Unlinks and deletes the dock with all its conversations and ships. `-yes` must be the second argument. |

See [WhatsApp](../guides/whatsapp.md).

## Conversations and customs

| Command | What it does |
| :--- | :--- |
| `seaport conversations [-allowed\|-pending\|-denied]` | Table of conversations (alias `convs`; up to 100): ID, status, access, kind, title, handles. One status flag filters (if you give several, the first of allowed, pending, denied wins). |
| `seaport allow <conversation-id>` | Owner override: always let the agent receive and answer this chat. |
| `seaport deny <conversation-id>` | Owner override: never. |
| `seaport auto <conversation-id>` | Remove the override: the dock's rules decide again. |
| `seaport mode <conversation-id> mentions\|all\|default` | A group's mode: wake the agent on mentions only, on everything, or follow the dock's `groups` setting. |
| `seaport customs [-dock ID]` | Shows the dock's policy and rules. `-dock` is optional when you have one dock. |
| `seaport customs [-dock ID] default ask\|allow\|deny` | What to do with chats no rule covers (`ask` holds them until you allow). `allow` prints a warning: every chat that writes to the number wakes the agent. |
| `seaport customs [-dock ID] groups mentions\|all` | Which messages of allowed groups wake the agent. |
| `seaport customs [-dock ID] allow <pattern>` | Adds an allow rule. |
| `seaport customs [-dock ID] deny <pattern>` | Adds a deny rule (deny wins over allow). |
| `seaport customs rm <rule-id>` | Removes a rule. |
| `seaport history <conversation-id> [-limit N]` | The last `N` messages (default 20), oldest first: time, status, sender, text. |
| `seaport send <conversation-id> <text>` | Sends a reply as the port (the rest of the arguments make the text). Subject to the [outbound limits](configuration.md#outbound-limits). |

Patterns are phone numbers (`51999123436`, `+51*`), WhatsApp ids (`…@lid`, `…@g.us`) and `*`. See [Customs](../guides/customs.md).

## Agents

| Command | What it does |
| :--- | :--- |
| `seaport agents [list]` | Table of agents: ID, name, profile version, docks (the default one marked), description. Alias: `seaport agent`. |
| `seaport agents add <name> [-description D]` | Creates an agent. Its name is its `@mention` handle. |
| `seaport agents edit <agent> [-name N] [-description D]` | Changes the flags you give; at least one is required. |
| `seaport agents assign <agent> <dock-id> [-default]` | Puts the agent on a dock's team. `-default` makes it the one that takes messages that call nobody. |
| `seaport agents unassign <agent> <dock-id>` | Takes it off the team; what waited for it there is held. |
| `seaport agents delete <agent> -yes` | Deletes the agent: revokes its tokens, disconnects it, holds what waits for it. |

See [Agents and routing](../guides/agents-and-routing.md).

### Profiles

The profile is what the agent's harness is told and given: see [Agent profile](agent-profile.md).

| Command | What it does |
| :--- | :--- |
| `seaport agents profile [show] <agent> [-version N] [-json]` | Shows the current profile, or a past `-version`. `-json` prints it in the format `set -f` reads. |
| `seaport agents profile versions <agent>` | Table of versions: number (current marked), author, time. |
| `seaport agents profile set <agent> [flags]` | Saves a new version. Starts from the current profile, applies the flags in the order below, and refuses a stale base. At least one flag is required. |

Flags of `profile set`:

| Flag | Meaning |
| :--- | :--- |
| `-f FILE` | Replace the whole profile from a JSON file (`-` = stdin), in the format of `show -json`. Unknown keys are an error. |
| `-instructions-file FILE` | Instructions from a file (for example `AGENTS.md`; `-` = stdin). |
| `-skills-dir DIR` | Replace the skills with `DIR/*/SKILL.md`. |
| `-skill SKILL.md` | Add or replace one skill from a file; repeatable. |
| `-rm-skill NAME` | Remove a skill by name; repeatable. Errors if the agent has no such skill. |
| `-approve allow\|reject\|connector` | Answer to the harness's permission requests; `connector` leaves it to the connector's `-approve`. |
| `-model M` | Model, among the harness's options; `none` clears it. |
| `-effort E` | Reasoning effort, among the harness's options; `none` clears it. |

A `SKILL.md` has front matter with `name` (default: its directory's name) and a required one-line `description` (when to use the skill), then the instructions. MCP servers are edited with `-f` only.

### Secrets

| Command | What it does |
| :--- | :--- |
| `seaport agents secrets [list] <agent>` | Table of secret names, whether each is given as an environment variable, and update time. Values are never shown. |
| `seaport agents secrets set <agent> NAME [-env]` | Saves a secret. The value comes from a hidden terminal prompt or from stdin (one trailing newline dropped), never from the command line. MCP headers use it as `${NAME}`. `-env` also gives it to the harness as an environment variable. |
| `seaport agents secrets rm <agent> NAME` | Deletes it. A secret an MCP header, a repo credential or a model key still uses can't be deleted. |

### Hosting

These commands need `SEAPORT_RUNTIME=docker` on the port for anything that builds or runs. See [Hosted agents](../guides/hosting/hosted-agents.md).

| Command | What it does |
| :--- | :--- |
| `seaport agents host <agent> <env> [-slots N]` | Runs the agent on the port in that environment. `-slots` is the number of instances (default 1; each keeps its own sessions and workspace). |
| `seaport agents unhost <agent>` | Stops hosting it (running slots must be stopped first). |
| `seaport agents slots [<agent>]` | Table of hosted slots: slot, environment and version, state, detail. |
| `seaport agents start <agent>[-<n>]` | Starts a slot (1 by default). New work also wakes a stopped agent. |
| `seaport agents stop <agent>[-<n>]` | Stops a slot. |
| `seaport agents logs <agent>[-<n>] [-lines N]` | The tail of a running slot's output; `-lines` defaults to 300, at most 2000. |
| `seaport agents usage <agent> [-days N]` | Model tokens the agent used through the port's gateway, by day and model; `-days` defaults to 30. |
| `seaport agents egress <agent>` | Hosts the egress proxy refused its slots. |
| `seaport agents login <agent>[-<n>]` | Signs a running Claude Code slot in to a subscription: prints a URL, reads the code you paste, waits for the result. Experimental; see [Claude subscriptions](../guides/hosting/claude-subscriptions.md). |
| `seaport agents account <agent>[-<n>]` | How a running slot's harness is signed in. |

`<agent>-<n>` names slot `n` of an agent; an agent whose own name looks like that wins.

## Environments

An environment says where hosted agents run: see [Environment spec](environment-spec.md). Aliases: `seaport envs`, `seaport environments`.

| Command | What it does |
| :--- | :--- |
| `seaport env [list]` | Table: name, version, harness, size, repos, agents, description. |
| `seaport env apply -f FILE` | Creates the environment, or saves a new version (an unchanged file makes none). `FILE` is YAML or JSON (`-` = stdin), read strictly: an unknown key is an error. The file holds `name` plus the spec's fields. A `home:` that is a directory (relative to the file) is uploaded first. |
| `seaport env export <env> [-version N]` | Prints the environment as YAML, the format `apply` reads (alias `show`). |
| `seaport env versions <env>` | Table of versions: number, author, time. |
| `seaport env build <env> [-version N] [-force] [-wait=false]` | Builds the image of a version (default: current) and follows the log until it ends; a failed build is an error. `-force` builds again even if built (pulls the base anew). `-wait=false` returns once the build starts. |
| `seaport env delete <env> -yes` | Deletes it and its history. Refused while agents run on it. |

### Harness homes

A harness home is an uploaded `~/.claude` or `~/.rness` baked into the environment's image. See [Harness homes](../guides/hosting/harness-homes.md).

| Command | What it does |
| :--- | :--- |
| `seaport env home push <env> [dir] [-dry-run]` | Packs the harness's config files from `dir` (default `~/.claude` or `~/.rness`, by the environment's harness), checks them with the server's own rules, uploads them and saves a version naming the home. What isn't config (sessions, sign-in, memory…) is left out and listed. `-dry-run` lists what would be uploaded and uploads nothing. |
| `seaport env home show <env>` | The files of its home: path, size, executable mark. |
| `seaport env home pull <env> <dir>` | Downloads the home into a new or empty directory. |

## Crew tokens

| Command | What it does |
| :--- | :--- |
| `seaport crew` | Connected agents and what they are working on: session, agent, connection name, concurrency, steer, since, remote address, and each delivery in flight. |
| `seaport crew tokens [list]` | Table of your organization's agent tokens: ID, agent, name, first characters of the token, created, expires, last used. |
| `seaport crew tokens create <name> -agent <agent> [-expires D]` | Creates a token for one agent (`-agent` is required). `-expires` is a Go duration such as `720h` (default: never). The token goes to stdout, **once**; the note goes to stderr. |
| `seaport crew tokens revoke <token-id>` | Revokes it and disconnects its agents. |

## connect

`seaport connect [flags] -- <agent command>` runs an agent harness on your machine, attached to a port. It dials out (no public address needed), speaks the [crew protocol](crew-protocol.md), starts `<agent command>` as an ACP agent in `-cwd`, and gives it the port's [MCP tools](mcp-tools.md). Example: `seaport connect -- rness --acp`. See [Connector](../guides/connector.md).

| Flag | Default | Meaning |
| :--- | :--- | :--- |
| `-server URL` | `$SEAPORT_URL`, else `http://127.0.0.1:8740` | The port. |
| `-token TOKEN` | `$SEAPORT_CREW_TOKEN` | Crew token. It belongs to one agent and names it. Not needed against a `SEAPORT_AUTH=none` port. |
| `-agent NAME` | `$SEAPORT_AGENT` | The agent to work as. Optional with a crew token (a given name must match the token's agent); required against a `none` port. |
| `-name LABEL` | the command's base name | Label of this connection shown by the port. |
| `-concurrency N` | `2` | Conversations worked at once. |
| `-auto-reply` | off | Send the agent's final text as the reply, for agents that can't use MCP. |
| `-approve reject\|allow` | the agent profile's, else `reject` | Answer to the agent's permission requests. |
| `-deliver steer\|queue` | `steer` | Messages that arrive mid-turn: `steer` puts them into the running turn when the agent supports it, `queue` waits for the next turn. |
| `-cwd DIR` | the current directory | The agent's working directory. |
| `-v` | off | Debug logs. |

The connector keeps its sessions in `sessions.json` under `<user config dir>/seaport/`, so conversations resume after a restart. A command is required after `--`; an invalid `-deliver` or `-approve` is refused before connecting.

## github-token

`seaport github-token [owner/repo]` prints a token of the [GitHub App](../guides/github.md#act-on-github-as-the-app) of one of the agent's GitHub docks, limited to that repository and valid for up to an hour. Without an argument it uses the `origin` remote of the current directory. It needs `SEAPORT_URL` and `SEAPORT_CREW_TOKEN` (the agent's own token); a repository the App isn't installed on is an error. Example: `GH_TOKEN=$(seaport github-token acme/web) gh pr view 12 -R acme/web`.


## reply

`seaport reply <conversation-id> [-file PATH]... [text]` sends a reply as the agent, with files from disk: a QA report, screenshots, a log. It is the MCP `reply` tool for files a model shouldn't write out as base64. It needs `SEAPORT_URL` and `SEAPORT_CREW_TOKEN` (the agent's own token; the connector passes both to the agent it runs). It prints the message id and its status. Limits: 10 files, 10 MiB each, 25 MiB together. See [Files agents send](../guides/messages-and-images.md#files-agents-send).

```bash
seaport reply 0193… -file qa-report.md -file checkout.png "QA done: 2 issues"
```

## Commands for hosted slots

These run inside a hosted slot's container and aren't meant to be called by hand; they appear here so you can recognize them in a slot's log.

| Command | What it does |
| :--- | :--- |
| `seaport agent-run` | A slot's entrypoint: reads its environment (`SEAPORT_URL`, `SEAPORT_CREW_TOKEN`, `SEAPORT_SLOT`, `SEAPORT_HARNESS`), prepares the workspace and runs the connector. |
| `seaport agent-login` | The subscription sign-in the port runs in a slot and relays. |
| `seaport agent-account` | Prints how the slot's harness is signed in, as JSON; the port asks. |
| `seaport home-setup` | Finishes an environment's harness home while its image is built. |
| `seaport git-credential` | Git's credential helper in a slot: asks the port for repo credentials each time (the environment's, else a [GitHub App](../guides/github.md#act-on-github-as-the-app) token), so no token stays on disk. |
| `gh` | Seaport's shim of GitHub's CLI in a slot (the seaport binary called as `gh`): runs the real `gh` with a GitHub App token for the repository the command is about. |
