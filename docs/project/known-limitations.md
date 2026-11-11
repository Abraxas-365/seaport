# Known limitations

Where you may run into limits, what to expect and what you can do about it. It describes the current source version; your installed version may differ. It is not a roadmap.

## Channels and messages

- **WhatsApp, GitHub and `api` are the only channels.** A WhatsApp dock uses an unofficial client library (whatsmeow), not the official Business API, so WhatsApp may ban numbers that behave like bots. Use a number you control and can afford to lose. Seaport limits how fast a dock sends (see [limits](../reference/limits.md)) but can't make an unofficial client official. See [WhatsApp](../guides/whatsapp.md).
- **Inbound media is images only** (agents can send any file; see [files agents send](../guides/messages-and-images.md#files-agents-send)). Seaport keeps JPEG, PNG, WebP and GIF images of at most 3.75 MiB, and only when media storage is on. Videos, audio and documents (and larger images, other formats, failed downloads, senders customs denied) arrive as a note in the message text, such as `[image received; too large to keep]`. The message is never lost, but the file is. See [messages and images](../guides/messages-and-images.md).
- **GitHub answers are plain comments.** An agent's `reply` is an issue comment; formal reviews, line comments and pushes go through `gh` or git with a token of the App (built into hosted slots; `seaport github-token` elsewhere). See [GitHub](../guides/github.md).
- **Agents send text only.** The MCP `reply` tool and the web composer send text; there is no outbound media.
- **Replies on an `api` dock are only stored.** The `api` gateway has no connection to send through: a reply is recorded as a ship (visible in the web app and the history) and nothing is delivered. Your system reads replies from the API or the [events stream](../reference/events.md). See [api docks](../guides/api-docks.md).
- **Media is not shown to every agent.** The connector sends images as ACP image blocks only when the harness advertises image support, at most 8 per prompt. Otherwise the prompt only names them.

## Customs and routing

- **Routing is decided once, at arrival.** A message is routed to an agent (or held) when it arrives. Assigning an agent to a dock later, or mentioning one afterwards, does not release ships already held with `no agent is assigned to this dock` or `no agent was called`. The agent can still read them with the `history` tool, and new messages route normally. See [agents and routing](../guides/agents-and-routing.md).
- **Allowing a chat releases a limited backlog.** When a direct chat becomes allowed, only ships held by customs as pending, from the last 24 hours and at most the newest 50, are delivered. Older ones, those held for another reason, and everything held in a group stay held. Whether a group message addressed your number isn't stored. See [customs](../guides/customs.md).
- **One agent works a conversation at a time, in arrival order.** A conversation waits while the agent it owes a message is offline; another agent doesn't skip ahead.

## Agents and connectors

- **Claude Code has no steering.** Messages that arrive while an agent works are steered into the running turn only when the harness advertises rness's `_rness/steer` ACP extension, which today means rness. A Claude Code harness started through `claude-agent-acp` gets them as the next prompt after the turn. See [connector](../guides/connector.md).
- **Permission requests are rejected by default.** The connector rejects a harness's tool permission requests unless the agent's profile or `-approve allow` says otherwise.
- **The connector speaks ACP only.** Harnesses that don't speak ACP need an adapter.

## Hosted agents

- **Docker only, and not in `none` mode.** Hosting needs `SEAPORT_RUNTIME=docker` and `SEAPORT_AUTH=token` or `iam`, because slots sign in with tokens. With `off` (the default), builds and slots are refused. See [hosted agents](../guides/hosting/hosted-agents.md).
- **One owner's agents per port in v1.** The runtime puts every slot on the same Docker engine, and the hardening (read-only root, dropped capabilities, uid 1000, egress proxy) is meant for one team's agents, not for strangers' code sharing a host. Don't use hosting to run mutually untrusted organizations' agents.
- **The egress proxy is a boundary only on an internal network.** Without `SEAPORT_EGRESS_ADDR` and a `--internal` Docker network for the slots, nothing stops them from reaching the Internet. The port warns at start when the network isn't internal. See [network](../guides/hosting/network.md).
- **rness has no headless subscription sign-in.** Claude subscription sign-in (experimental) works for `claude-code` environments only; give an rness environment a model key. See [Claude subscriptions](../guides/hosting/claude-subscriptions.md).
- **The model gateway forwards Anthropic only.** `models:` accepts `anthropic`, and only `/v1/messages`, `/v1/models` and `/v1/files` are forwarded. Usage is metered for successful Messages calls.
- **Environment edits apply to a running slot once it is idle.** A slot keeps working on its old version until it is idle (or past `idle_stop`), then restarts.
- **Harness homes have size limits.** At most 2000 files, 5 MiB per file and 20 MiB in total; sessions, sign-in, memory and Claude's `plugins/` are never uploaded. See [harness homes](../guides/hosting/harness-homes.md).

## Operating several processes

- **Rate limiting is per process.** The API rate limit is an in-memory bucket per caller in each process; with N replicas a caller may get up to N times the rate. Public routes aren't limited.
- **No PgBouncer in transaction mode.** Session advisory locks and `LISTEN` need session pooling. See [several processes](../guides/operations/several-processes.md).
- **A WhatsApp dock lives in one process, and failover takes time.** After a crash, other processes take the dock when its 30 s lease runs out, and the dead process's agent connections count against the agent quota until they are reaped (about 30 s). Docks are claimed first come, not balanced.
- **Live events are short-lived.** The event log keeps an hour; a client that was away longer gets `reset` and must refetch. Event payloads are stored in clear, so Seaport never publishes secrets in them.
- **Changing `SEAPORT_SECRET_KEY` locks out sealed data.** WhatsApp devices, agent secrets, attachments and harness homes can't be opened with another key, so Seaport refuses to start rather than fail later. See [backups and upgrades](../guides/operations/backups-and-upgrades.md).

## Web app and modes

- **The web app is in Spanish only.** Pages, labels and hold reasons are Spanish; the CLI, API and docs are English.
- **`none` mode is loopback-only.** It refuses other listen addresses, foreign `Host` names and requests with forwarding headers, so a reverse proxy or tunnel needs `token` mode. See [security model](../explanation/security-model.md).
- **A `go install` binary has no web app.** `go install` can't run the web build, so that binary serves the API but not the UI. Release binaries and the Docker image include it.

## Documentation and reporting problems

The docs describe verified behavior; when they and the code disagree, the code (and [AGENTS.md](../../AGENTS.md)) wins, and the doc is a bug.

When reporting a problem, include:

- Your Seaport version (`seaport version`), how you run it (binary or Docker, auth mode, number of processes) and Postgres version.
- The steps that reproduce it, what you expected and what happened instead.
- The relevant log lines, redacted.

Don't post tokens, `SEAPORT_SECRET_KEY`, real phone numbers or message contents.
