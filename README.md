<div align="center">

<img src="docs/assets/seaport-banner.svg" alt="Seaport — Messages arrive. Your agents work them. An illustrated port log: a message passes customs, wakes an agent, and gets a reply." width="1200" />

# Seaport

**A port for your agents.**

Your company's agents, defined once and called from wherever people already work: Slack, GitHub, Jira, WhatsApp or your own systems.<br />
Each message (a ship) wakes the right agent (the crew), which answers in the same thread with the port's tools. Run the agent on a laptop or let the port host it. Either way, you decide who gets through.

<p>
  <a href="go.mod"><img src="https://img.shields.io/badge/Server-Go-1f4f99?style=flat-square&amp;logo=go&amp;logoColor=white" alt="Go server" /></a>
  <a href="https://agentclientprotocol.com"><img src="https://img.shields.io/badge/Agents-ACP-3d6fc4?style=flat-square" alt="Any ACP agent" /></a>
  <a href="#channels"><img src="https://img.shields.io/badge/Channels-Slack_·_GitHub_·_Jira_·_WhatsApp_·_HTTP-25a18e?style=flat-square" alt="Channels: Slack, GitHub, Jira, WhatsApp, HTTP" /></a>
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

**Put your agents where the work is.** Write `@qa review this` on a pull request, `@pm` in a Slack thread or a Jira issue, or message a WhatsApp number: Seaport takes the message, decides whether it may reach your agents, and wakes the right one. The agent answers in the same thread through Seaport's MCP tools, from wherever it runs. You define each agent once in the port (instructions, skills, MCP servers, secrets) and it works on every channel you assign it to.

```text
Slack · GitHub · Jira ──▶ seaport serve ──WebSocket──▶ seaport connect ──ACP──▶ rness --acp
WhatsApp · HTTP api   ──▶      ▲                        (or a slot the port hosts)   │
                               └────────────── MCP: reply / history ◀────────────────┘
```

### Channels

| Channel | What a dock is | Wakes an agent when | Guide |
| :--- | :--- | :--- | :--- |
| Slack | a Slack app in your workspace | someone mentions it or `@agent` in a channel, follows up in its thread, or sends it a DM | [Slack](docs/guides/slack.md) |
| GitHub | a GitHub App on your repositories | a pull request or issue (description, comment or review) mentions the App or `@agent`; a workflow can hand it work too | [GitHub](docs/guides/github.md) |
| Jira | a Jira Cloud service account | an issue or a comment mentions the account or `@agent` | [Jira](docs/guides/jira.md) |
| WhatsApp | a number linked as a device (a QR, like WhatsApp Web) | a chat that customs lets in writes; groups by mention | [WhatsApp](docs/guides/whatsapp.md) |
| `api` | an endpoint you post to from your own systems | you post a message | [api docks](docs/guides/api-docks.md) |

<table>
<tr>
<td width="50%" valign="top">
<h3>01 · Docks</h3>
<p>A channel endpoint: a WhatsApp number linked as a device, a GitHub App on your repositories, a Jira service account, a Slack app in your workspace, or an <code>api</code> dock you feed over HTTP. Link, relink, rename or unlink — history stays.</p>
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
> **Early-stage software.** The WhatsApp channel links a number as a device through an unofficial client ([whatsmeow](https://github.com/tulir/whatsmeow)); numbers that send in bursts get banned, which is why outbound limits are on by default. Review the [configuration](docs/reference/configuration.md) before pointing it at a number you care about. Slack, GitHub and Jira use their official APIs.

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

### 3. Add a dock and let messages in

In the web app (Docks) or from the CLI. The quickest to try needs nothing outside your computer: an `api` dock you post to.

```sh
seaport dock add api -name try                 # prints the dock id
seaport customs -dock <dock-id> default allow  # api chats are yours: let them all in
```

For a real channel, add `slack`, `github` or `jira` (they need the port on a public `https` URL, since those services call it by webhook) or `whatsapp` (scan a QR; nothing public needed):

```sh
seaport dock add slack       # then create or paste the app in the web app
seaport dock add github      # creates a GitHub App from the web app; install it on your repositories
seaport dock add jira        # then enter the site and the service account's API token in the web app
seaport dock add whatsapp    # scan the QR: WhatsApp → Linked devices → Link a device
```

GitHub and Jira forward only messages that mention the App or account or an agent; Slack also forwards DMs and follow-ups in threads an agent was called into. Every new dock asks first (`default ask`): a new conversation waits at customs until you allow it in the web app (Customs), with `seaport allow <conversation-id>`, or with a rule (`seaport customs -dock <dock-id> default allow` lets every conversation of the dock in; on a public GitHub repository add `deny github:outsider`). On WhatsApp your own "message yourself" chat is always allowed.

### 4. Put an agent to work

Create an agent, assign it to the dock, and connect it from the directory it should work in. The connector dials out to the port (no public address needed, like ngrok) and runs any [ACP](https://agentclientprotocol.com) agent — one agent session per conversation, resumed across restarts:

```sh
seaport agents add pm -description "Answers about tickets"
seaport agents assign pm <dock-id> -default
seaport connect -agent pm -- rness --acp -m anthropic/claude-sonnet-4-5
```

Now send it a message: post to the `api` dock (below), or write `@pm` in Slack, GitHub or Jira, or message the WhatsApp number. `pm` wakes, reads the message, and answers in the same thread with the `reply` tool.

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

For a complete one, see [`examples/github-reviewer`](examples/github-reviewer/): a `qa` agent that reviews pull requests when someone writes `@qa review this`, with its instructions, two skills and an optional workflow.

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
| Put an agent in Slack, GitHub or Jira | [Slack](docs/guides/slack.md), [GitHub](docs/guides/github.md), [Jira](docs/guides/jira.md) |
| Hand work to an agent from a GitHub workflow | [GitHub Actions](docs/guides/github.md#trigger-an-agent-from-a-workflow) |
| Put an agent behind a WhatsApp number | [WhatsApp](docs/guides/whatsapp.md) |
| Feed messages from your own systems | [api docks](docs/guides/api-docks.md) |
| Copy a complete agent | [`examples/github-reviewer`](examples/github-reviewer/) |
| Decide who reaches your agents | [Customs](docs/guides/customs.md) |
| Route messages between agents | [Agents and routing](docs/guides/agents-and-routing.md) |
| Write profiles: instructions, skills, MCP, secrets | [Agent profiles](docs/guides/profiles.md) |
| Send and receive images and files | [Messages and images](docs/guides/messages-and-images.md) |
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
| `internal/dock` | Channels: Slack apps, GitHub Apps, Jira service accounts, WhatsApp (whatsmeow) and `api` docks, ownership across processes |
| `internal/conversation` | Chats and customs |
| `internal/ship` | Messages, images and the delivery queue |
| `internal/agent` | Agent library, routing, profiles and secrets |
| `internal/crew` | Crew tokens, the WebSocket hub and the MCP tools |
| `internal/hosting` | Environments, slots, image builds, model gateway, egress proxy, harness homes |
| `internal/connector` | The agent side: WebSocket + ACP client |
| `internal/auth` | iam mode: IAMKit sign-in and sessions |
| `web/` | React web app, embedded into the binary |
| `images/` | Base images of hosted agents (`ghcr.io/abraxas-365/seaport-rness`, `seaport-claude-code`) |
| `action/` | GitHub Action: a workflow hands a message to a GitHub dock (OIDC, no secret) |
| `examples/` | Complete agents to copy (`github-reviewer`) |
| `iam/` | Permissions and roles, the source of the generated code |
| `website/` | The landing page and these docs on GitHub Pages |
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
