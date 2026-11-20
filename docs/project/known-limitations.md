# Known limitations

Where you may run into limits, what to expect and what you can do about it. It describes the current source version; your installed version may differ. It is not a roadmap.

## Channels and messages

- **WhatsApp, GitHub, Jira, Slack, email, `api`, webhook and Datadog are the only channels.** Webhook and Datadog docks only trigger: they never take replies. There is no Microsoft Teams dock; bridge it through an `api` dock. A WhatsApp dock uses an unofficial client library (whatsmeow), not the official Business API, so WhatsApp may ban numbers that behave like bots. Use a number you control and can afford to lose. Seaport limits how fast a dock sends (see [limits](../reference/limits.md)) but can't make an unofficial client official. See [WhatsApp](../guides/whatsapp.md).
- **Inbound media is images and documents, and only some channels bring them** (WhatsApp, Slack, email and `api`; GitHub and Jira messages arrive as text). Agents can send any file; see [files agents send](../guides/messages-and-images.md#files-agents-send). Seaport keeps JPEG, PNG, WebP and GIF images of at most 3.75 MiB and any other file of at most 10 MiB as a document, and only when media storage is on. WhatsApp videos, audio and stickers (and larger files, failed downloads, senders customs denied) arrive as a note in the message text, such as `[document received; too large to keep]`. The message is never lost, but the file is. See [messages and images](../guides/messages-and-images.md).
- **GitHub answers are plain comments.** An agent's `reply` is an issue comment; formal reviews, line comments and pushes go through `gh` or git with a token of the App (built into hosted slots; `seaport github-token` elsewhere). See [GitHub](../guides/github.md).
- **Outbound files depend on the channel.** An agent's `reply` can carry files (up to 10, 10 MiB each, 25 MiB together). GitHub gets them as a commit to the `seaport-files` branch, linked from the comment; WhatsApp sends each as its own message; Slack uploads them to the thread (the app needs the `files:write` scope); an `api` dock only keeps them. An email dock sends them as attachments. A Jira dock refuses a reply with files: the reply must be text. The web composer sends text only. See [files agents send](../guides/messages-and-images.md#files-agents-send).
- **Replies on an `api` dock are only stored.** The `api` gateway has no connection to send through: a reply is recorded as a ship (visible in the web app and the history) and nothing is delivered. Your system reads replies from the API or the [events stream](../reference/events.md). See [api docks](../guides/api-docks.md).
- **Media is not shown to every agent.** The connector sends images as ACP image blocks only when the harness advertises image support, at most 8 per prompt. Otherwise the prompt only names them.

## Customs and routing

- **Routing is decided at arrival; assigning an agent releases a limited backlog.** A message is routed to an agent (or held) when it arrives. Assigning an agent to a dock routes again only what was held for routing in the last 24 hours, in allowed chats, and only messages newer than the chat's last routed message or reply. Mentioning an agent later releases nothing. The agent can still read held messages with the `history` tool. See [agents and routing](../guides/agents-and-routing.md).
- **Allowing a chat releases a limited backlog.** When a chat becomes allowed, only ships held by customs as pending, from the last 24 hours and at most the newest 50, are delivered. Older ones and those held for another reason stay held. See [customs](../guides/customs.md).
- **One agent works a conversation at a time, in arrival order.** A conversation waits while the agent it owes a message is offline; another agent doesn't skip ahead.

## Agents and connectors

- **Claude Code has no steering.** Messages that arrive while an agent works are steered into the running turn only when the harness advertises rness's `_rness/steer` ACP extension, which today means rness. A Claude Code harness started through `claude-agent-acp` gets them as the next prompt after the turn. See [connector](../guides/connector.md).
- **Permission requests are rejected by default.** The connector rejects a harness's tool permission requests unless the agent's profile or `-approve allow` says otherwise.
- **The connector speaks ACP only.** Harnesses that don't speak ACP need an adapter.

## Vaults

- **Full-text search only.** [Vault](../guides/vaults.md) search matches words in entry names and bodies. There are no embeddings, so a search for "refund" doesn't find an entry that says "money back".
- **Markdown text only.** An entry is text up to 64 KiB, with no files or images. A vault holds at most 2000 entries.
- **Agents' own harness memory is separate.** rness's `~/.rness/memory` stays in each slot's state volume and isn't shared; a vault is the memory that doesn't depend on the harness.
- **No approval of agent writes.** A `write` grant lets the agent save without a person's review, including on a dock open to strangers. The trail is provenance, versions and a secret check, not a `propose` mode.
- **The admin API reads versions only of existing entries.** A deleted entry's versions stay stored until its vault is deleted, but to restore one you save the text again (`vault pull` snapshots are the easy way).

## Hosted agents

- **Docker only, and not in `none` mode.** Hosting needs `SEAPORT_RUNTIME=docker` and `SEAPORT_AUTH=token` or `iam`, because slots sign in with tokens. With `off` (the default), builds and slots are refused. See [hosted agents](../guides/hosting/hosted-agents.md).
- **One owner's agents per port in v1.** The runtime puts every slot on the same Docker engine, and the hardening (read-only root, dropped capabilities, uid 1000, egress proxy) is meant for one team's agents, not for strangers' code sharing a host. Don't use hosting to run mutually untrusted organizations' agents.
- **The egress proxy is a boundary only on an internal network.** Without `SEAPORT_EGRESS_ADDR` and a `--internal` Docker network for the slots, nothing stops them from reaching the Internet. The port warns at start when the network isn't internal. See [network](../guides/hosting/network.md).
- **Subscription calls aren't metered.** A slot signed in to a Claude account (experimental) calls Anthropic directly, so **Uso de modelos** counts only the providers it reaches with a key through the gateway (an rness slot can have both). See [Claude subscriptions](../guides/hosting/claude-subscriptions.md).
- **The model gateway knows five providers.** `models:` accepts `anthropic` (Anthropic protocol) and `openai`, `openrouter`, `groq`, `deepseek` (OpenAI protocol); a provider that isn't one of them, or another protocol (Gemini's, Bedrock, Vertex), can't go through the gateway. Only the model-call, model-list, files and embeddings paths are forwarded. A streamed Chat Completions call is metered only when it asks for usage (`stream_options.include_usage`).
- **Machine edits apply to a running slot once it is idle.** A slot keeps working on its old config until its new image is built and it is idle (or past `idle_stop`), then restarts; it shows as outdated meanwhile. The same goes for a Seaport upgrade, which needs new images. Edits of the instructions or skills don't restart the container.
- **A hosted agent isn't connected from people's machines.** Hosting an agent that has live crew tokens is refused, and so is minting a token for a hosted agent: revoke the tokens, or stop hosting, first.
- **There are no shared environments any more.** Each agent has its own machine and secrets. Agents with the same machine share the built image, not the secrets; to reuse a setup, apply the same config file to each agent.
- **The managed browser is for hosted agents only.** `seaport connect` on a machine of your own starts no browser; a profile with `browser: true` there just logs a warning. Chromium is the only engine, headed on a virtual display, one viewer per slot. See [browser](../guides/hosting/browser.md).
- **Harness homes have size limits.** At most 2000 files, 5 MiB per file and 20 MiB in total; sessions, sign-in, memory and Claude's `plugins/` are never uploaded. See [harness homes](../guides/hosting/harness-homes.md).

## Operating several processes

- **Rate limiting is per process.** The API rate limit is an in-memory bucket per caller in each process; with N replicas a caller may get up to N times the rate. Public routes aren't limited.
- **No PgBouncer in transaction mode.** Session advisory locks and `LISTEN` need session pooling. See [several processes](../guides/operations/several-processes.md).
- **A WhatsApp or email dock lives in one process, and failover takes time.** Only WhatsApp and email are process-bound (one device or IMAP connection); GitHub, Jira, Slack and `api` docks need no owner, and any process serves their webhooks and sends. After a crash, other processes take the dock when its 30 s lease runs out, and the dead process's agent connections count against the agent quota until they are reaped (about 30 s). Docks are claimed first come, not balanced.
- **Live events are short-lived.** The event log keeps an hour; a client that was away longer gets `reset` and must refetch. Event payloads are stored in clear, so Seaport never publishes secrets in them.
- **Changing `SEAPORT_SECRET_KEY` locks out sealed data.** WhatsApp devices, agent secrets, attachments, harness homes and the GitHub, Jira, Slack and email credentials can't be opened with another key, so Seaport refuses to start rather than fail later. See [backups and upgrades](../guides/operations/backups-and-upgrades.md).

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
