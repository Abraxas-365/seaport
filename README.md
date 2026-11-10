# Seaport

A port for agents. Messages (ships) arrive at the port from channels
(WhatsApp first); your agent (the crew) connects from wherever it runs and is
**woken** by each message, then answers with the port's tools.

```
WhatsApp ──(linked device)──▶ seaport serve ──WebSocket──▶ seaport connect ──ACP──▶ rness --acp
                                   ▲                                                 │
                                   └──────────── MCP: reply / history ◀──────────────┘
```

- **Docks** — a channel endpoint: a WhatsApp number linked as a device (QR, like
  WhatsApp Web, via whatsmeow) or an `api` dock fed over HTTP.
- **Customs** — per dock rules decide which chats and messages wake the agent.
  Messages that don't pass are *held*: stored (visible in history) but not
  delivered. Your own "message yourself" chat is always allowed.
- **Agents** — your library of agents (`@pm`, `@reviewer`, …). An agent works
  only on the docks you assign it to; on a dock with several, a message picks
  one with `@name`, else the chat stays with the agent it was talking to,
  else it goes to the dock's default (with none, it is held).
- **Deliveries** — the allowed messages of one conversation, handed to one
  connector at a time (leased, renewed while the agent works, released if it
  disconnects, so nothing is lost).
- **Connector** — `seaport connect -- <agent>` dials out to the port (no public
  address needed, like ngrok) and runs any [ACP](https://agentclientprotocol.com)
  agent, one agent session per conversation, resumed across restarts.
- **Hosting** (optional) — or the port runs the agent itself: rness or Claude
  Code in a hardened container per slot, started when messages arrive and
  stopped when idle, with its repos, a model gateway (the API key never
  enters the container) and an egress allowlist. See
  [Hosted agents](#hosted-agents).

One binary, several ways to run it — only the start-up config differs
(`SEAPORT_AUTH`):

| Mode | For | Auth |
|---|---|---|
| `none` | your own computer | no login, no tokens; loopback only — refuses other listen addresses, foreign `Host` names and anything relayed by a proxy or tunnel |
| `token` (default) | one owner on a server | static admin token; agents use crew tokens |
| `iam` | many organizations | [IAMKit](https://github.com/Abraxas-365/iamkit) accounts, organizations and permissions; per-organization crew tokens |

Inside, Seaport is always multi-tenant: every dock, conversation, ship and
rule belongs to an organization (single-owner modes use one fixed
organization), so moving from one mode to another needs no data migration.

## Run

```bash
docker compose up -d                                    # Postgres on 127.0.0.1:5449
export DATABASE_URL="postgres://seaport:seaport@127.0.0.1:5449/seaport?sslmode=disable"
export SEAPORT_ADMIN_TOKEN=$(openssl rand -hex 24)
go run ./cmd/seaport serve
```

On your own computer you can skip the tokens: `SEAPORT_AUTH=none go run
./cmd/seaport serve` (the web app opens without login; the CLI and `seaport
connect` work without `-token`; browsers on other sites still can't write).
To reach it from anywhere else — a reverse proxy, a tunnel (cloudflared,
ngrok, `tailscale serve`) — use `token` mode: an open port answers 403 to
requests carrying `X-Forwarded-For`/`Forwarded`-style headers.

Open http://127.0.0.1:8740 and sign in with the admin token: inbox, customs,
docks (WhatsApp pairing QR, api docks), crew — all live over SSE. The web app
lives in `web/` (React + Vite + shadcn/ui + Tailwind v4) and is embedded into
the binary, so build it before `go build` (`cd web && pnpm install && pnpm
build`); `pnpm dev` serves it with hot reload on :5173, proxying `/api` to
`$SEAPORT_URL` (default :8740). The design system is at `/design`.

Or from the CLI — link WhatsApp and allow chats (admin token):

```bash
seaport dock add whatsapp          # scan the QR: WhatsApp → Linked devices → Link a device
seaport conversations              # chats that wrote to the number
seaport allow <conversation-id>
```

### Customs

Each dock has a **default** for chats no rule covers — `ask` (hold until you
`seaport allow` them; the default), `allow` or `deny` — and a **group mode**:
`mentions` (only messages that @mention the number or reply to it; the
default) or `all`. **Rules** allow or deny chats and senders by pattern:
phone numbers (`51999123436`, `+51*`), WhatsApp ids (`…@lid`, `…@g.us`); `*`
matches anything. Other channels will match their own handles (usernames,
emails).

```bash
seaport customs                        # policy and rules (-dock ID with several docks)
seaport customs allow "+51*"           # Peruvian numbers may wake the agent
seaport customs deny 51999888777       # never this one (also muted inside groups)
seaport customs allow 120363…@g.us     # this group, mentions only
seaport customs default ask|allow|deny
seaport customs groups mentions|all
seaport customs rm <rule-id>
seaport mode <conversation-id> all     # this group: every message (or mentions|default)
seaport allow|deny <conversation-id>   # owner override, beats rules
seaport auto <conversation-id>         # follow the rules again
```

Order: your override → self chat → deny rules → allow rules → default. A deny
rule matching a group member holds that member's messages in allowed groups
(unless you allowed the conversation itself). Changing rules re-decides
existing chats. When a pending direct chat becomes allowed (by you or a rule),
what it sent while it waited is delivered to the agent, oldest first (the last
24 hours, up to 50 messages); older messages, a denied chat's messages and a
group's held messages stay held (the agent can read them with `history`).

### Images

Images people send (WhatsApp photos; `images` on an `api` dock) arrive
with their message: the caption is its text, the image an attachment.
Seaport downloads each one at arrival (WhatsApp's links expire), checks
from its bytes that it is a JPEG, PNG, WebP or GIF of at most 3.75 MiB,
and keeps it sealed with the secret key in blob storage
(`SEAPORT_BLOB_URL`: a local directory by default, S3 or a compatible
store by URL). Anything else (a larger image, another format, a failed
download, media storage off, a sender customs denied) still arrives,
with a note in its text such as `[image received; too large to keep]`;
videos, audio and documents are notes for now.

The connector shows the images to the agent with the prompt when the
agent takes images (ACP `promptCapabilities.image`: Claude Code; rness
when its model takes images), up to 8 per prompt; otherwise the prompt
only names them. The MCP `history` tool lists each message's
attachments and the `attachment` tool shows one. The web app shows them
in the conversation. Deleting a dock, conversation or message deletes its
images (a sweeper removes their blobs within a minute).

### Agents

Create agents, put them on docks, then connect each one with a crew token of
its own (or in the web app: Agentes, Tokens de agentes), from the directory
it should work in:

```bash
seaport agents add pm -description "Genera tickets"
seaport agents add reviewer
seaport agents assign pm <dock-id> -default     # gets whatever nobody @mentions
seaport agents assign reviewer <dock-id>        # "@reviewer mira el PR 12"
seaport crew tokens create laptop -agent pm     # shown once
SEAPORT_CREW_TOKEN=sp_crew_… seaport connect -- rness --acp -m anthropic/claude-sonnet-4-5
```

Routing on a dock: the first `@name` of an agent on that dock picks it; else
the agent the conversation was talking to (if still on the dock); else the
only agent; else the dock's default. Otherwise the message is held (no agent
on the dock, or several and none called) and shows why in the web app. A
conversation is worked by one agent at a time, in arrival order. An agent on
no dock receives nothing. Unassigning or deleting an agent holds what was
waiting for it; deleting also revokes its tokens and closes its connections.
With `SEAPORT_AUTH=none` there are no tokens: `seaport connect -agent pm --
…` names the agent.

Every crew token belongs to one agent of an organization, can expire
(`-expires 720h`) and is revoked one by one (`seaport crew tokens revoke
<id>` closes its connections). An agent may have several tokens (one per
machine) and several connections; they share its work. The connector reconnects on its own when the connection
drops or the port restarts, but exits with an error when the port refuses
its crew token (revoked, expired or wrong): create a new one.

#### Profiles

An agent's **profile** is what its harness is told and given, kept in the
port and applied by `seaport connect` on whatever machine runs it:
instructions, skills, extra MCP servers, the answer to permission requests
and the model / reasoning effort. Edit it in the web app (Agentes → the
agent) or the CLI:

```bash
seaport agents profile set pm -instructions-file AGENTS.md -skills-dir skills/ -approve allow -effort high
printf %s "$LINEAR_TOKEN" | seaport agents secrets set pm LINEAR_TOKEN   # value from stdin
seaport agents profile show pm -json > pm.json   # edit (e.g. mcp_servers), then:
seaport agents profile set pm -f pm.json
seaport agents profile versions pm               # every save is a version; show -version N
```

- **Instructions** go as the session's system prompt when the agent supports
  ACP's client system prompt (`systemPrompt`, appended to its own), else with
  the first prompt of each session.
- **Skills** (SKILL.md: name, when to use it, instructions) reach any harness
  through the port's MCP server: a `skill` tool lists them and returns one's
  instructions when the agent calls it. Nothing is written on the agent's
  machine.
- **MCP servers** (streamable HTTP) are added to every session next to
  Seaport's; their headers may name the agent's **secrets** as `${NAME}`.
  Secrets are sealed with the port's secret key (`SEAPORT_SECRET_KEY[_FILE]`),
  never shown again after they are written, and handed out (resolved, plus
  those marked `-env` as environment variables of the harness) only to the
  agent's own connections, through `GET /crew/profile`.
- **Permissions** (`allow|reject`) answer the harness's permission requests;
  the connector's `-approve` flag overrides it. **Model** and **effort** are
  set through the agent's ACP session options (`model`, `reasoning_effort`)
  when it offers them.

Saves are optimistic (an edit made on an older version is refused). A saved
profile or secret reaches the agent's connectors at once: each finishes
what it is working on, takes no new work meanwhile, restarts its harness
with the new profile and resumes its sessions. The crew view shows a
connection still running an older version (or a connector from before
profiles) as outdated.

The connector is the same `seaport` binary. On the agent's machine, take it
from [Releases](https://github.com/Abraxas-365/seaport/releases) (linux,
macOS, windows; amd64/arm64; web app included) or build it with Go:

```bash
go install github.com/Abraxas-365/seaport/cmd/seaport@latest
```

(`go install` can't run the web build, so that binary serves the API but not
the web app — fine for `connect` and the CLI. Release binaries and the Docker
image have both.)

Flags: `-concurrency N`, `-auto-reply` (send the agent's final text, for
agents without HTTP MCP), `-approve allow|reject` (tool permission requests;
default: the profile's, else reject), `-deliver steer|queue`, `-server`, `-token`.

Messages that arrive while the agent is working on a conversation are
**steered** into the running turn by default (the model sees them at its next
step, like rness's F9 steer) when the agent advertises it — rness does, via
the ACP extension `_rness/steer` (`agentCapabilities._meta.rness.steer`).
Other agents, or `-deliver queue`, get them as the next prompt after the turn.
A message steered too late (the turn had just ended) is delivered again as a
new prompt, never lost.

| Env | Default | |
|---|---|---|
| `DATABASE_URL` | — | Postgres (everything, WhatsApp keys included — sealed) |
| `SEAPORT_AUTH` | `token` | `none` (loopback only, no tokens), `token` or `iam` |
| `SEAPORT_ADMIN_TOKEN` | — | token mode: CLI and `/api/v1` (≥16 chars). CLI in iam mode: an IAMKit organization API key |
| `SEAPORT_CREW_TOKEN` | — | `seaport connect`: the crew token to use (it names the agent) |
| `SEAPORT_AGENT` | — | `seaport connect` with `SEAPORT_AUTH=none`: the agent to work as (`-agent`) |
| `SEAPORT_SECRET_KEY` | generated | seals the WhatsApp keys, agent secrets and message images at rest, and in iam mode (required there) browser sessions' IAMKit tokens. Random, ≥32 chars (`openssl rand -base64 32`). Unset: read from `SEAPORT_SECRET_KEY_FILE`, generated there (0600) on first start. Losing it means linking every WhatsApp dock again, setting every agent secret again, losing every image (and signs everyone out) |
| `SEAPORT_SECRET_KEY_FILE` | `<config dir>/seaport/secret.key` | where the generated key lives (`/data/secret.key` in the image: mount a volume) |
| `SEAPORT_BLOB_URL` | `blobs/` next to the key file | where message images and harness homes are kept (sealed): `file:///dir` (one server), `s3://bucket?region=…` (credentials from `AWS_*`; S3-compatible stores with `&endpoint=…&use_path_style=true`), `off`. Several processes need a shared store (S3), not a local directory |
| `IAMKIT_*` | — | iam mode, written by `scripts/iamkit-provision.sh` (see [IAM mode](#iam-mode)) |
| `SEAPORT_ADDR` | `127.0.0.1:8740` | listen address |
| `SEAPORT_PUBLIC_URL` | `http://$SEAPORT_ADDR` | how agents reach `/mcp` (use https off-loopback) |
| `SEAPORT_SEND_PER_MINUTE` | `20` | outbound messages per dock per minute (0 = off) |
| `SEAPORT_SEND_PER_HOUR` | `300` | outbound messages per dock per hour (0 = off) |
| `SEAPORT_SEND_PER_CHAT_MINUTE` | `8` | outbound messages per conversation per minute (0 = off) |
| `SEAPORT_SEND_ORG_PER_HOUR` | `1000` | outbound messages per organization per hour, across its docks (0 = off) |
| `SEAPORT_MAX_DOCKS` | `10` | docks per organization (0 = off) |
| `SEAPORT_MAX_CREW_CONNECTIONS` | `20` | agents connected at once per organization (0 = off) |
| `SEAPORT_API_RATE` / `SEAPORT_API_BURST` | `20` / `60` | requests per second (and burst) per caller on `/api`, `/crew/ws`, `/mcp` (0 = off) |
| `SEAPORT_URL` | `http://127.0.0.1:8740` | CLI/connector: where the port is |

**Outbound limits.** WhatsApp bans numbers that send in bursts, so a reply
over a limit is refused with `429` (`RATE_LIMITED`, `Retry-After`; the MCP
`reply` tool returns the same message to the agent) and nothing is sent or
stored. Windows slide over the last minute/hour. They apply to every reply
of a dock (agents, the web app, the API); failed sends count too, so an
agent retrying against a broken dock is slowed down as well.
Each WhatsApp send is also spaced ≥1.2 s from the previous one of that dock.

**Quotas.** Each organization (one in `none`/`token` mode, many in `iam`
mode) gets the same limits: docks, connected agents and outbound messages
per hour. Going over a count is refused with `422` (`QUOTA_EXCEEDED`,
details `quota` and `limit`): delete a dock or disconnect an agent to make
room. A connector over the agent limit keeps retrying with backoff. The API
rate limit is a token bucket per caller (organization + user, API key or
crew token; agents sharing a crew token share it), answering `429` with
`Retry-After`.

## Hosted agents

Instead of running `seaport connect` yourself, the port can run an agent
for you (`SEAPORT_RUNTIME=docker`, Docker on the Seaport host; one owner's
agents in v1). An **environment** says where it runs; **hosting** the agent
on it gives it **slots** (`<agent>-1`, `<agent>-2`, …), each a container
started when the agent has work and stopped when it has none.

```yaml
# env.yaml — seaport env apply -f env.yaml (or the web app: Entornos)
name: backend
harness: rness              # rness | claude-code
packages: [jq, ripgrep]     # apt, into a derived image
setup: |                    # bash as root at build time (tools, baseline config;
  npm i -g pnpm             #   never secrets: it is stored and in the image)
repos:
  - url: https://github.com/acme/api.git
    ref: main
    credential: ${GITHUB_TOKEN}   # a secret of the hosted agent
    on_start: pnpm i
repo_config: trust          # ignore: the harness skips the repos' AGENTS.md,
                            #   CLAUDE.md and .claude/ settings
home: ./rness-home          # your harness config, uploaded by apply (below)
models:
  anthropic: ${ANTHROPIC_API_KEY} # through the port's gateway
network:
  egress: [registry.npmjs.org]    # + the repos' hosts
resources: {size: M}              # S 1 CPU/2 GB, M 2/4, L 4/8, or cpus + memory_mb
idle_stop: 30m                    # 0: never
```

```bash
printf %s "$GITHUB_TOKEN" | seaport agents secrets set pm GITHUB_TOKEN
printf %s "$ANTHROPIC_API_KEY" | seaport agents secrets set pm ANTHROPIC_API_KEY
seaport env apply -f env.yaml && seaport env build backend   # optional: the first start builds too
seaport agents host pm backend -slots 2
seaport agents slots pm          # stopped → starting → running → stopping
seaport agents logs pm-1 ; seaport agents usage pm ; seaport agents egress pm
```

The web app has the same (Entornos; Alojamiento on the agent's page:
slots, logs, model use, refused hosts).

- **Environments** are versioned like profiles (an unchanged file makes no
  version). Each version is built once into an image `FROM` Seaport's base
  image of the harness (`ghcr.io/abraxas-365/seaport-rness` /
  `seaport-claude-code`, the release's tag; `image:` takes your own image
  built `FROM` one of them). Running slots move to a new version once idle.
- **Slots** run hardened: read-only root, all capabilities dropped,
  no-new-privileges, uid 1000, CPU/memory limits; `/workspace` (the repos,
  kept across restarts unless `workspace: ephemeral`) and the harness's
  sessions and sign-in live in per-slot volumes, so a woken slot resumes
  its conversations. Each conversation gets a git worktree
  (`seaport/<conversation>` branch). Git asks the port for repo
  credentials each time; no token is written to disk. A slot signs in with
  a crew token of its own, minted per run and revoked when it stops.
- **Model gateway**: with `models.anthropic`, slots call Anthropic through
  the port (`ANTHROPIC_BASE_URL`) with their slot token; the port forwards
  with the real key and meters tokens per day and model.
- **Egress**: with `SEAPORT_EGRESS_ADDR`, slots go out through the port's
  proxy, which lets through only the environment's `network.egress` and
  repo hosts (never private addresses) and records what it refused. It is
  only a boundary when the slots' network is `internal`
  (`docker network create --internal seaport-slots`,
  `SEAPORT_RUNTIME_NETWORK=seaport-slots`, the Seaport container attached
  to it too); the port warns at start otherwise. `deploy/` sets this up.
- **Harness home**: `home:` bakes your own harness config into the image,
  over the base's: for Claude Code `CLAUDE.md`, `settings.json`,
  `agents/`, `commands/`, `skills/`, `hooks/`, `output-styles/`; for rness
  `init.lua`, `hooks.json`, `lua/`, `plugins/`, `skills/`.
  `seaport env home push backend [~/.claude]` uploads a dir (leaving out
  sessions, sign-in, history, memory, caches) and saves a version naming
  it; `env home show|pull` read it back. Uploads with secret-looking
  strings are refused: give the agent an env secret and read it from the
  environment (`"${GITHUB_TOKEN}"`). Claude Code plugins are installed at
  build from `settings.json`'s `enabledPlugins` (and the marketplaces in
  `extraKnownMarketplaces`), so the build needs those hosts; rness keeps
  Seaport's `require("seaport")` first in `init.lua`. An uploaded file
  replaces the base's whole: an `init.lua` must still declare the
  providers (`require("providers")`), or the slot fails with `unknown
  provider 'anthropic'`. A home is at most
  20 MiB; homes no version names are deleted after a day.
- The profile (instructions, skills, MCP servers, secrets) applies as for
  any connector; a saved change restarts the slot's harness once idle, and
  conversations already under way are told their instructions changed.
- The slot's network settings win over configuration: neither the
  profile's env nor a settings file (Claude Code's `settings.json`, from
  the image or a trusted repo) can point the harness around the gateway
  or the proxy (Claude Code gets them as managed settings).

**Claude subscriptions (experimental).** A `claude-code` environment with
no `models.anthropic` signs each slot in to a Claude account instead: the
slot waits, taking no messages, until someone runs **Conectar cuenta** on
the agent's page (or `seaport agents login pm-1`). Claude Code's own login
runs inside the slot; you open its URL, sign in and paste the code back.
The tokens stay in that slot's state volume (Seaport never stores them);
`seaport agents account pm-1` shows how it is signed in. The slot then uses
that subscription's limits and terms (Anthropic's terms decide whether an
agent like this may use a personal plan: check them), and its model calls
don't go through the gateway (no usage metering). Alternative: run
`claude setup-token` on your computer and save the token as the agent's env
secret `CLAUDE_CODE_OAUTH_TOKEN` (`seaport agents secrets set pm
CLAUDE_CODE_OAUTH_TOKEN -env`): every slot uses it. rness has no headless
subscription sign-in yet; give it a model key.

| Env | Default | |
|---|---|---|
| `SEAPORT_RUNTIME` | `off` | `docker` runs hosted agents on the engine at `DOCKER_HOST` (needs `token` or `iam` mode) |
| `DOCKER_HOST` | local socket | the engine; better a socket proxy limited to containers, exec, images/build, volumes, networks (see `deploy/`) |
| `SEAPORT_AGENT_URL` | `SEAPORT_PUBLIC_URL` | how slots reach the port (e.g. `http://seaport:8740` on a shared network) |
| `SEAPORT_RUNTIME_NETWORK` | `bridge` | the slots' Docker network (make it `--internal` with the egress proxy) |
| `SEAPORT_RUNTIME_HOST_GATEWAY` | `true` | add `host.docker.internal` to slots (a port on the host) |
| `SEAPORT_IMAGE_RNESS` / `SEAPORT_IMAGE_CLAUDE_CODE` | release's `ghcr.io/abraxas-365/seaport-*` | base images |
| `SEAPORT_EGRESS_ADDR` | — | listen address of the slots' egress proxy (e.g. `:8741`); off: no proxy |
| `SEAPORT_EGRESS_URL` | agent URL's host + that port | how slots reach the proxy |
| `SEAPORT_EGRESS_PRIVATE` | `false` | let allowed hosts resolve to private addresses (local testing) |
| `SEAPORT_GATEWAY_ANTHROPIC_URL` | `https://api.anthropic.com` | where the model gateway forwards |
| `SEAPORT_MAX_SLOTS` | `10` | slots per organization (0 = off) |
| `SEAPORT_HOST_MAX_CPUS` / `SEAPORT_HOST_MAX_MEMORY_MB` | `4` / `8192` | most one slot may ask for (0 = off) |

## IAM mode

`SEAPORT_AUTH=iam` hosts many organizations on one port, with people,
organizations, roles and API keys managed by an
[IAMKit](https://github.com/Abraxas-365/iamkit) server (self-hosted).

- **People** sign in to the web app through IAMKit's hosted login (OAuth
  code + PKCE, done by the server: the browser only holds an HttpOnly
  session cookie; IAMKit's tokens are sealed in Postgres and refreshed
  there). Someone in several organizations picks one at sign-in and can
  switch from the account menu. Signing out ends the IAMKit session too.
- **Permissions** come from `iam/permissions.json` (the single source:
  Go and TypeScript constants are generated from it, tests fail on drift).
  Every admin route requires one (`seaport:docks:read`, …); the web app
  hides what your role can't do, and the live stream only carries event
  types you may read. Roles in the manifest: `Admin` (everything),
  `Operator` (works the inbox — allows chats, replies — and sees docks,
  customs and agents, without changing them or tokens), `Viewer`
  (read-only).
- **The CLI** uses an IAMKit organization API key as
  `SEAPORT_ADMIN_TOKEN` (`ik_key_…`, created in IAMKit's admin, holding the
  permissions you choose). An IAMKit access token works too.
- **Agents** use crew tokens of their organization, created by people with
  `seaport:crew-tokens:write` (each for one agent, managed with
  `seaport:agents:write`); deliveries, MCP tools and the stream never
  cross organizations (another organization's object is a 404).
- **When changes take effect.** Seaport asks IAMKit about people's tokens
  and API keys on every request, so removing someone from the organization,
  suspending them or revoking an API key works on their next request (and
  revoking a crew token, Seaport's own, closes its agents at once).
  A **role change** reaches a person's permissions when IAMKit issues their
  next access token: at the session's next refresh (at most the access
  token's lifetime, 15 minutes by IAMKit's default) or when they sign in
  again. An access token sent as a bearer keeps the permissions it was
  issued with until it expires, and an API key keeps the ones it was
  created with (a role change never touches it: revoke it and create
  another).

Provision IAMKit once (idempotent; re-run after editing
`iam/permissions.json`; needs `curl` and `jq` and an IAMKit management key):

```bash
IAMKIT_URL=https://iam.example.com IAMKIT_KEY=ik_mgmt_… \
APP_URL=https://seaport.example.com \
OWNER_EMAIL=you@example.com OWNER_PASSWORD='a long password' \
  scripts/iamkit-provision.sh          # --dry-run shows the changes first
```

It creates the project, environment, a first organization, the
application, the Seaport resource (permission catalog) and its roles, the
browser OAuth client (redirect `APP_URL/auth/callback`), enables
organization API keys, makes `OWNER_EMAIL` an `Admin`, and writes
`.iamkit.env` (`SEAPORT_AUTH`, `SEAPORT_PUBLIC_URL`, `IAMKIT_*`). Then:

```bash
set -a; . ./.iamkit.env; set +a
export SEAPORT_SECRET_KEY=$(openssl rand -base64 32)   # keep it: it opens the stored sessions
seaport serve
```

`IAMKIT_URL` is how Seaport reaches IAMKit; set `IAMKIT_PUBLIC_URL` when
browsers reach it at another address (e.g. IAMKit on the same Docker
network), and `IAMKIT_ISSUER` if its tokens' issuer differs from that.
These URLs and `SEAPORT_PUBLIC_URL` must be https, except on localhost
(and `IAMKIT_URL` on a private network: `http://iamkit:8080`, `10.x`).
Only `.iamkit.env`'s `SEAPORT_AUTH`, `SEAPORT_PUBLIC_URL` and `IAMKIT_*`
settings are read by Seaport (the role and organization ids are for you).
`IAMKIT_AUDIENCE` is fixed when the resource is created. Removing a
permission from the manifest keeps it in IAMKit until `--prune` (IAMKit
would strip it from every role and key at once): add the new one, deploy,
then prune.

## Deploy (one server, HTTPS)

`deploy/` runs Postgres, Seaport, [Caddy](https://caddyserver.com) (automatic
Let's Encrypt certificates, HTTP→HTTPS) and daily backups with Docker
Compose. Point a DNS name at the server, open ports 80 and 443, then:

```bash
cd deploy
cp .env.example .env      # SEAPORT_DOMAIN, POSTGRES_PASSWORD and SEAPORT_ADMIN_TOKEN (`openssl rand -hex 32`)
docker compose up -d --build
docker compose logs -f seaport
```

Open `https://$SEAPORT_DOMAIN`, sign in with `SEAPORT_ADMIN_TOKEN`, link
WhatsApp, create an agent and assign it to the dock (Agentes), then a crew
token for it (Tokens de agentes). On your computer:

```bash
SEAPORT_URL=https://$SEAPORT_DOMAIN SEAPORT_CREW_TOKEN=sp_crew_… seaport connect -- rness --acp -m …
```

- For many organizations set `SEAPORT_AUTH=iam` and the IAMKit settings in
  `.env` instead of the admin token (see `.env.example` and
  [IAM mode](#iam-mode); provision with `APP_URL=https://$SEAPORT_DOMAIN`).
- Only Caddy publishes ports; Seaport and Postgres stay on the compose network.
  The proxy runs `token` or `iam` mode (`none` refuses proxied requests).
- Backups: `pg_dump -Fc` into `deploy/backups` every `BACKUP_EVERY_SECONDS`
  (1 day), kept `BACKUP_KEEP_DAYS` (14), mode 0600 owned by `BACKUP_UID`
  (root by default). Images live in blob storage, not in the dumps:
  back up `/data/blobs` (or the bucket) too. The WhatsApp keys and agent secrets in them (and, in iam mode, the IAMKit
  tokens of browser sessions) are sealed with `SEAPORT_SECRET_KEY`, which is
  not in the dumps: keep a copy of the key apart from them (from `.env`, or
  `docker compose cp seaport:/data/secret.key .` when it was generated) — a
  dump without it is useless, which is also why a stolen dump is. Copy the
  dumps off the server too (the same disk is not a backup). Restore: `docker compose stop seaport backup`, then
  `docker compose exec -T postgres pg_restore -U seaport -d seaport --clean --if-exists < backups/<file>.dump`,
  then `docker compose start seaport backup`.
- Upgrade: `git pull && docker compose up -d --build` (migrations run at start).
  From a version before the sealed WhatsApp store (migration 00008), run
  the release that has it (up to `e807f94`) once first: it moves linked
  WhatsApp devices out of whatsmeow's clear-text tables, which migration
  00012 then drops (it refuses while a linked dock is still only there).
- Hosted agents: `SEAPORT_RUNTIME=docker` in `.env`, then
  `docker compose --profile hosting up -d --build`. Seaport reaches Docker
  through a socket proxy (`docker-proxy`, only the Engine calls it makes;
  the socket itself is never mounted into Seaport), and slots run on the
  internal network `seaport-slots`, where the only thing they reach is
  Seaport (crew, MCP, model gateway, egress proxy on `:8741`). Mind what a
  Docker socket means: whoever controls Seaport controls that engine, so
  give hosting a server (or VM) of its own.
- The image alone: `docker build -t seaport .` (distroless, non-root, ~24 MB,
  `seaport healthcheck` as the container health check).
- Try it locally: `SEAPORT_DOMAIN=localhost` makes Caddy use its own CA
  (`docker compose cp caddy:/data/caddy/pki/authorities/local/root.crt .`
  and trust it, or `curl --cacert root.crt`). Set `HTTP_PORT`/`HTTPS_PORT`
  and `SEAPORT_PUBLIC_URL=https://localhost:<port>` if 80/443 are taken.
- Seaport warns at start when `SEAPORT_PUBLIC_URL` is plain `http` on a
  non-local host.

### Several processes

Several Seaport processes (replicas behind one load balancer, or a rolling
deploy) can serve one Postgres; any process answers any request, no sticky
sessions. Give them all the same configuration, and in particular the same
`SEAPORT_SECRET_KEY` (and admin token in token mode): set the key
explicitly instead of letting each process generate its own file. Point
`SEAPORT_BLOB_URL` at one shared store (S3 or compatible): with local
directories each process sees only the images it received.

Agents may connect to any process: connections are recorded in
Postgres, so the crew status and the agent quota count every process, a
message arriving at one process wakes agents on all of them, and revoking a
token closes its connections everywhere. A process shutting down hands its
agents' deliveries back at once; one that dies is noticed within about half
a minute (it stops renewing its connections) and another process gives its
deliveries to the remaining agents.

A WhatsApp number keeps one connection, so each WhatsApp dock runs in one
process at a time (its owner, recorded in Postgres); the others pass it
their replies, unlinks and relinks, and show its pairing QR code. A process
shutting down hands its docks over at once; one that dies loses them within
about 30 seconds, and another process reconnects them (a pairing in progress
there is lost: link again). Replicas need a direct (or
session-pooled) Postgres connection: they use LISTEN/NOTIFY and session
advisory locks, which PgBouncer's transaction mode breaks.

Per process, not shared: the API rate limit (with N processes a caller may
get up to N× `SEAPORT_API_RATE`).

## API

Admin routes accept `Authorization: Bearer <token>` (the admin token; in
iam mode an IAMKit API key or access token) or a browser session cookie.
In iam mode each route needs its permission (`iam/permissions.json`):
403 without it.

| | |
|---|---|
| `GET /api/v1/config` | public: `{"auth", "version"}` — how the web app must sign in |
| `POST /api/v1/session` `{"token"}`, `GET /api/v1/session`, `DELETE /api/v1/session` | token mode browser login: trades the admin token for an HttpOnly, SameSite=Strict cookie (7 days, stateless HMAC; rotating the token ends all sessions). Cookie writes from another origin get 403. `GET` answers `{"authenticated", "permissions", …}` in every mode (iam: name, email, organization) |
| `GET /api/v1/session/login[?next=&organization=&switch=1]`, `GET /auth/callback` | iam mode: sign in through IAMKit (top-level navigation); `DELETE /api/v1/session` answers `{"logout_url"}` (IAMKit's end session) |
| `GET/POST /api/v1/agents`, `GET/PATCH/DELETE /api/v1/agents/{id}` | the agent library (each with its docks); POST/PATCH `{"name","description"}`; DELETE revokes its tokens and holds what waits for it |
| `GET/PUT /api/v1/agents/{id}/profile`, `GET …/profile/versions[/{version}]` | an agent's profile (version 0: none yet); PUT `{"base","instructions","skills","mcp_servers","approve","model","effort"}` saves version base+1 (`409` if base isn't the current one) |
| `GET /api/v1/agents/{id}/secrets`, `PUT/DELETE …/secrets/{NAME}` | an agent's secrets: names only, never values; PUT `{"value","env"}`; DELETE refuses one the profile's headers use (`409`) |
| `GET /api/v1/docks/{id}/agents`, `PUT/DELETE /api/v1/docks/{id}/agents/{agent}` | a dock's agents (`{"agents":[…]}`); PUT `{"default"}` assigns (or changes the default), both answer the agent |
| `GET/POST /api/v1/crew/tokens`, `DELETE /api/v1/crew/tokens/{id}` | crew tokens of your organization; POST `{"agent_id","name","expires_at"?}` answers the value once |
| `GET /api/v1/events[?last_event_id=]` | live events (SSE, below) |
| `GET /api/v1/crew` | connected agents and their deliveries in flight |
| `GET /api/v1/inbox[?status=&dock_id=]` | conversations by last activity, each with `last_ship` (null without `ships:read`), `held`, `queued`, `in_flight` |
| `GET/POST /api/v1/docks`, `GET/PATCH/DELETE /api/v1/docks/{id}`, `POST …/relink`, `POST …/unlink` | docks (QR in `qr_code` while linking); PATCH `{"name"}`; unlink logs the number out and keeps history; DELETE unlinks and deletes its conversations, ships and customs |
| `POST /api/v1/docks/{id}/ships` | inject an inbound message into an `api` dock; `images: [{"data": base64, "name"?}]` (the body stays within 1 MiB) |
| `GET /api/v1/attachments/{id}` | an image of a message (`ships:read`) |
| `GET /api/v1/conversations[?status=allowed\|pending\|denied&dock_id=]`, `GET/PATCH /api/v1/conversations/{id}` | chats; PATCH `{"access":"auto\|allow\|deny","group_mode":"mentions\|all\|","title":…}` |
| `GET /api/v1/docks/{id}/customs`, `PATCH …/customs/policy` `{"default","group_mode"}`, `POST …/customs/rules` `{"effect","pattern"}`, `DELETE /api/v1/customs/rules/{id}` | customs |
| `GET/POST /api/v1/conversations/{id}/ships` | history; send as the agent |
| `GET /crew/ws` | crew WebSocket (`internal/crew/protocol.go`) |
| `GET /crew/profile[?agent=]` | crew credential: its agent's kit (profile with secrets resolved, `env`, `revision`); `?agent=` only in `none` mode |
| `GET /crew/attachments/{id}` | crew credential: an image of a message of its agent's docks (else 404) |
| `POST /mcp` | MCP tools: `reply`, `history`, `conversations`, `attachment`, and `skill` when the agent's profile has skills |
| `GET /healthz` | public liveness: `{"status","version"}`, nothing about any organization |

### Events

`GET /api/v1/events` is a Server-Sent Events stream (`EventSource` with the
session cookie). Each event's `data` is JSON:

| event | data |
|---|---|
| `ready` | `{"boot"}`, first on every stream |
| `reset` | the `Last-Event-ID` is unknown, older than an hour or too far behind: refetch state |
| `dock.updated` / `dock.deleted` | the dock (status; a new QR without the code: fetch the dock) / `{"id"}` |
| `conversation.updated` | the conversation (new, title, handles, customs status) |
| `customs.updated` | `{"customs"}` of a dock |
| `ship.created` / `ship.updated` | the ship (inbound, outbound pending → sent/failed) |
| `delivery.changed` | `{"state":"claimed\|joined\|acked\|released\|expired", "delivery_id", "conversation_id", "ship_ids", "count", "owner"}` |
| `crew.changed` | the whole crew status (as `GET /api/v1/crew`) |
| `agent.updated` / `agent.deleted` | the agent with its docks (created, edited, assigned, unassigned; a demoted default gets no event: refetch) / `{"id"}` |

Events are stored in Postgres for an hour and announced with
LISTEN/NOTIFY, so every Seaport process streams every process's events.
The browser resends the last event id on reconnect (to any process) and
gets what it missed; after an hour, or with an unknown id, it gets `reset`
and refetches.
