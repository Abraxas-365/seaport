<div align="center">

<img src="docs/assets/seaport-banner.svg" alt="Seaport — Messages arrive. Your agents work them. An illustrated port log: a WhatsApp message passes customs, wakes an agent, and gets a reply." width="1200" />

# Seaport

**A port for your agents.**

Messages (ships) arrive from channels — WhatsApp first. Your agents (the crew) are woken by each one and answer with the port's tools.<br />
Run the agent on your laptop, or let the port host it. Either way, you decide who gets through.

<p>
  <a href="go.mod"><img src="https://img.shields.io/badge/Server-Go-1f4f99?style=flat-square&amp;logo=go&amp;logoColor=white" alt="Go server" /></a>
  <a href="https://agentclientprotocol.com"><img src="https://img.shields.io/badge/Agents-ACP-3d6fc4?style=flat-square" alt="Any ACP agent" /></a>
  <a href="docs/guides/whatsapp.md"><img src="https://img.shields.io/badge/Channel-WhatsApp-25d366?style=flat-square&amp;logo=whatsapp&amp;logoColor=white" alt="WhatsApp channel" /></a>
  <a href="https://github.com/Abraxas-365/seaport/releases"><img src="https://img.shields.io/github/v/release/Abraxas-365/seaport?style=flat-square&amp;color=6fa3f0" alt="Latest release" /></a>
  <img src="https://img.shields.io/badge/Status-Early_stage-8fa3c4?style=flat-square" alt="Early-stage software" />
</p>

**[Get started](#get-started)** &nbsp; · &nbsp;
**[Host your agents](#let-the-port-host-them)** &nbsp; · &nbsp;
**[Read the docs](https://abraxas-365.github.io/seaport/docs/)** &nbsp; · &nbsp;
**[Deploy](docs/guides/operations/deploy.md)** &nbsp; · &nbsp;
**[Contribute](#contributing)**

</div>

---

## Your agents, open for business

**Put an agent behind a phone number.** Seaport links a WhatsApp number as a device (a QR, like WhatsApp Web), decides which chats may reach your agents, and wakes the right one for each message. The agent answers through Seaport's MCP tools — from wherever it runs.

```text
WhatsApp ──(linked device)──▶ seaport serve ──WebSocket──▶ seaport connect ──ACP──▶ rness --acp
                                   ▲                                                 │
                                   └──────────── MCP: reply / history ◀──────────────┘
```

<table>
<tr>
<td width="50%" valign="top">
<h3>01 · Docks</h3>
<p>A channel endpoint: a WhatsApp number linked as a device, a GitHub App on your repositories, a Slack app in your workspace, or an <code>api</code> dock you feed over HTTP. Link, relink, rename or unlink — history stays.</p>
</td>
<td width="50%" valign="top">
<h3>02 · Customs</h3>
<p>Per-dock rules decide which chats and senders wake an agent. What doesn't pass is <em>held</em>: stored and visible, never delivered. Groups wake on mentions by default.</p>
</td>
</tr>
<tr>
<td width="50%" valign="top">
<h3>03 · Crew</h3>
<p>Your library of agents (<code>@pm</code>, <code>@reviewer</code>, …), each on the docks you assign. <code>@name</code> picks one; otherwise the chat stays with the agent it was talking to, or goes to the dock's default.</p>
</td>
<td width="50%" valign="top">
<h3>04 · Nothing lost</h3>
<p>Each conversation's messages go to one connection at a time, leased while the agent works and released if it disconnects. Messages that arrive mid-turn are steered into the running turn.</p>
</td>
</tr>
</table>

**Channels bring the ships. Customs decides. Your agents do the work.**

> [!IMPORTANT]
> **Early-stage software.** Linking a WhatsApp number as a device uses an unofficial client ([whatsmeow](https://github.com/tulir/whatsmeow)); numbers that send in bursts get banned, which is why outbound limits are on by default. Review the [configuration](docs/reference/configuration.md) before pointing it at a number you care about.

## From one laptop to many organizations

One binary. Only the start-up config (`SEAPORT_AUTH`) differs:

| Mode | For | Auth |
| :--- | :--- | :--- |
| `none` | your own computer | no login, no tokens; loopback only — refuses other listen addresses, foreign `Host` names and anything relayed by a proxy or tunnel |
| `token` (default) | one owner on a server | a static admin token; agents use crew tokens |
| `iam` | many organizations | [IAMKit](https://github.com/Abraxas-365/iamkit) accounts, organizations, roles and permissions; per-organization crew tokens |

Inside, Seaport is always multi-tenant: every dock, conversation, ship and rule belongs to an organization (single-owner modes use one fixed organization), so moving between modes needs no data migration.

**[Read the IAM mode guide →](docs/guides/operations/iam-mode.md)** &nbsp; **[Deploy on one server →](docs/guides/operations/deploy.md)**

## Get started

### 1. Install

**From a release:** download the `seaport` binary for your platform (linux, macOS, windows; amd64/arm64; web app included) from [GitHub Releases](https://github.com/Abraxas-365/seaport/releases).

**With Go:** the connector and the CLI only need

```sh
go install github.com/Abraxas-365/seaport/cmd/seaport@latest
```

`go install` can't run the web build, so that binary serves the API but not the web app — fine for `connect` and the CLI. Release binaries and the Docker image have both.

**From a checkout:** build the web app first, since it is embedded into the binary:

```sh
(cd web && pnpm install && pnpm build) && go build ./cmd/seaport
```

### 2. Run the port

Seaport keeps everything in Postgres. The repository's compose file starts one on `127.0.0.1:5449`:

```sh
docker compose up -d
export DATABASE_URL="postgres://seaport:seaport@127.0.0.1:5449/seaport?sslmode=disable"
SEAPORT_AUTH=none seaport serve
```

Open **http://127.0.0.1:8740**: inbox, customs, docks, agents and crew — all live. In `none` mode there is no login; the CLI and `seaport connect` need no token.

> [!TIP]
> **Going beyond your computer?** Use `token` mode (`SEAPORT_ADMIN_TOKEN=$(openssl rand -hex 24) seaport serve`) and sign in with that token. A `none` port refuses anything relayed by a reverse proxy or tunnel (cloudflared, ngrok, `tailscale serve`). For HTTPS on a server, see [Deploy](docs/guides/operations/deploy.md).

### 3. Link a number and let chats in

In the web app (Docks) or from the CLI:

```sh
seaport dock add whatsapp          # scan the QR: WhatsApp → Linked devices → Link a device
seaport conversations              # chats that wrote to the number
seaport allow <conversation-id>    # or a rule: seaport customs allow "+34*"
```

Your own "message yourself" chat is always allowed — the easiest way to try it.

### 4. Put an agent to work

Create an agent, assign it to the dock, and connect it from the directory it should work in. The connector dials out to the port (no public address needed, like ngrok) and runs any [ACP](https://agentclientprotocol.com) agent — one agent session per conversation, resumed across restarts:

```sh
seaport agents add pm -description "Answers about tickets"
seaport agents assign pm <dock-id> -default
seaport connect -agent pm -- rness --acp -m anthropic/claude-sonnet-4-5
```

Now write to the number. `pm` wakes, reads the message, and answers with the `reply` tool.

<details>
<summary><strong>Ways to connect · token mode, flags, other agents</strong></summary>

### Connecting an agent

| Situation | Command |
| :--- | :--- |
| `none` mode (your computer) | `seaport connect -agent pm -- rness --acp` |
| `token` / `iam` mode | `seaport crew tokens create laptop -agent pm`, then `SEAPORT_URL=https://… SEAPORT_CREW_TOKEN=sp_crew_… seaport connect -- rness --acp` |
| Claude Code ([claude-agent-acp](https://www.npmjs.com/package/@agentclientprotocol/claude-agent-acp)) | `seaport connect -agent pm -approve allow -- claude-agent-acp` |
| Several conversations at once | `-concurrency N` (default 2) |
| Agents without HTTP MCP | `-auto-reply` sends the agent's final text |
| Tool permission requests | `-approve allow\|reject` (default: the profile's, else reject) |
| Queue mid-turn messages instead of steering | `-deliver queue` |

A crew token names its agent, can expire (`-expires 720h`) and is revoked one by one; an agent may have several (one per machine), and they share its work. The connector reconnects on its own when the connection drops or the port restarts.

Messages that arrive while the agent is working are **steered** into the running turn when the agent supports it (rness does, via the ACP extension `_rness/steer`); other agents get them as the next prompt.

See [Agents and routing](docs/guides/agents-and-routing.md) and [the connector](docs/guides/connector.md) in full.

</details>

<details>
<summary><strong>An <code>api</code> dock · feed messages over HTTP</strong></summary>

### Messages from your own systems

An `api` dock has no phone behind it: you post messages to it and read the agent's replies from the history or the live event stream.

```sh
seaport dock add api -name support
seaport customs -dock <dock-id> default allow      # api chats are yours: let them all in
curl -X POST "$SEAPORT_URL/api/v1/docks/<dock-id>/ships" \
  -H "Authorization: Bearer $SEAPORT_ADMIN_TOKEN" \
  -d '{"chat_id":"ticket-42","message_id":"m-1","sender_id":"ana","text":"@pm what is the status?"}'
```

`message_id` makes a retried post a duplicate, not a second message. Customs and routing apply as for any dock. Images go as `images: [{"data": base64}]` (the body stays within 1 MiB). See [api docks](docs/guides/api-docks.md).

</details>

## Shape what your agents know

An agent's **profile** lives in the port and is applied by whatever runs it — your laptop's connector or a hosted slot:

| Give it | How |
| :--- | :--- |
| **Instructions** | Sent as the session's system prompt (or with its first prompt); a changed brief reaches sessions already under way |
| **Skills** | `SKILL.md` files served through the port's MCP `skill` tool; nothing is written on the agent's machine |
| **MCP servers** | Added to every session next to Seaport's; headers may name the agent's secrets as `${NAME}` |
| **Secrets** | Sealed at rest, never shown again, handed only to the agent's own connections |
| **Model and effort** | Set through the agent's ACP session options when it offers them |

```sh
seaport agents profile set pm -instructions-file AGENTS.md -skills-dir skills/ -approve allow -effort high
printf %s "$LINEAR_TOKEN" | seaport agents secrets set pm LINEAR_TOKEN   # value from stdin
seaport agents profile versions pm                                       # every save is a version
```

A saved change reaches the agent at once: each connector finishes what it is doing, restarts its harness with the new profile and resumes its sessions.

## Let the port host them

Don't want to keep a laptop open? The port can run the agent itself — **rness** or **Claude Code** in a hardened container per slot, started when messages arrive and stopped when idle.

```yaml
# env.yaml — seaport env apply -f env.yaml (or the web app: Entornos)
name: backend
harness: rness                    # rness | claude-code
repos:
  - url: https://github.com/acme/api.git
    credential: ${GITHUB_TOKEN}   # a secret of the hosted agent
home: ./rness-home                # your own harness config
models:
  anthropic: ${ANTHROPIC_API_KEY} # through the port's gateway
network:
  egress: [registry.npmjs.org]    # + the repos' hosts
idle_stop: 30m
```

```sh
seaport env apply -f env.yaml
seaport agents host pm backend -slots 2
seaport agents slots pm            # stopped → starting → running → stopping
```

| What you get | Why it matters |
| :--- | :--- |
| **Hardened slots** | Read-only root, capabilities dropped, uid 1000, CPU/memory limits; each conversation in its own git worktree |
| **A model gateway** | Slots call Anthropic through the port with a slot token — the API key never enters the container — and usage is metered per day and model |
| **An egress allowlist** | Slots reach only the hosts you list (plus their repos), never private addresses; refused hosts are recorded |
| **Your harness, your way** | Upload your `~/.claude` or `~/.rness` as a versioned home — from the CLI or by dropping a folder in the web app — and it is baked into the image |
| **Credentials on demand** | Git asks the port for repo credentials each time; no token is written to disk |

> [!NOTE]
> **Hosting needs Docker and `token` or `iam` mode.** Set `SEAPORT_RUNTIME=docker`; the egress proxy is a boundary only when the slots' network is `internal`. The [deploy stack](docs/guides/operations/deploy.md) sets all of this up behind a Docker socket proxy.

**[Read the hosting guide →](docs/guides/hosting/hosted-agents.md)**

## Go deeper

| If you're here to… | Start here |
| :--- | :--- |
| Run your first port and see a message arrive | [Your first port](docs/tutorials/first-port.md) |
| Find your way around the web app | [Web app tour](docs/guides/web-app.md) |
| Decide who reaches your agents | [Customs](docs/guides/customs.md) |
| Route messages between agents | [Agents and routing](docs/guides/agents-and-routing.md) |
| Write profiles: instructions, skills, MCP, secrets | [Agent profiles](docs/guides/profiles.md) |
| Send and read images | [Messages and images](docs/guides/messages-and-images.md) |
| Host agents on the port | [Hosted agents](docs/guides/hosting/hosted-agents.md) |
| Tune limits, quotas and storage | [Configuration](docs/reference/configuration.md) |
| Serve many organizations | [IAM mode](docs/guides/operations/iam-mode.md) |
| Put it on a server with HTTPS | [Deploy](docs/guides/operations/deploy.md) |
| Integrate over HTTP, MCP or SSE | [HTTP API](docs/reference/http-api.md), [MCP tools](docs/reference/mcp-tools.md), [Events](docs/reference/events.md) |
| Understand the architecture | [Architecture](docs/architecture.md), [AGENTS.md](AGENTS.md) |

<details>
<summary><strong>Inside the repository</strong></summary>

| Path | Responsibility |
| :--- | :--- |
| `cmd/seaport` | One binary: `serve`, the admin CLI, `connect` |
| `internal/dock` | Channels: WhatsApp (whatsmeow), GitHub Apps, Slack apps and `api` docks, ownership across processes |
| `internal/conversation` | Chats and customs |
| `internal/ship` | Messages, images and the delivery queue |
| `internal/agent` | Agent library, routing, profiles and secrets |
| `internal/crew` | Crew tokens, the WebSocket hub and the MCP tools |
| `internal/hosting` | Environments, slots, image builds, model gateway, egress proxy, harness homes |
| `internal/connector` | The agent side: WebSocket + ACP client |
| `internal/auth` | iam mode: IAMKit sign-in and sessions |
| `web/` | React web app, embedded into the binary |
| `images/` | Base images of hosted agents (`ghcr.io/abraxas-365/seaport-rness`, `seaport-claude-code`) |
| `deploy/` | Production compose: Postgres, Seaport, Caddy, backups |
| `migrations/` | SQL migrations, applied at start |

Seaport is hexagonal (ports and adapters), and architecture tests enforce the layering; see [AGENTS.md](AGENTS.md).

</details>

## Contributing

Bug reports and focused fixes are welcome. Read [Contributing](docs/contributing/README.md) and [AGENTS.md](AGENTS.md) first: they describe the architecture, the rules the architecture tests enforce, and the invariants worth knowing.

Remove phone numbers, chat ids, tokens and message content before sharing logs.

Useful checks from the repository root:

```sh
go build ./... && go vet ./... && go test ./...
docker compose up -d && PGPASSWORD=seaport createdb -h 127.0.0.1 -p 5449 -U seaport seaport_test
SEAPORT_TEST_DATABASE_URL="postgres://seaport:seaport@127.0.0.1:5449/seaport_test?sslmode=disable" \
  go test -count=1 -p 1 ./...      # integration tests (they wipe that database)
(cd web && pnpm lint && pnpm build)
```

---

<div align="center">

**Messages arrive. Your agents work them. You decide who gets through.**

[Get started](#get-started) · [Host your agents](#let-the-port-host-them) · [Read the docs](https://abraxas-365.github.io/seaport/docs/) · [Website](https://abraxas-365.github.io/seaport/)

</div>
