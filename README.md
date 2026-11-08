# Puerto

A port for agents. Messages (ships) arrive at the port from channels
(WhatsApp first); your agent (the crew) connects from wherever it runs and is
**woken** by each message, then answers with the port's tools.

```
WhatsApp ──(linked device)──▶ puerto serve ──WebSocket──▶ puerto connect ──ACP──▶ rness --acp
                                   ▲                                                 │
                                   └──────────── MCP: reply / history ◀──────────────┘
```

- **Docks** — a channel endpoint: a WhatsApp number linked as a device (QR, like
  WhatsApp Web, via whatsmeow) or an `api` dock fed over HTTP.
- **Customs** — per dock rules decide which chats and messages wake the agent.
  Messages that don't pass are *held*: stored (visible in history) but not
  delivered. Your own "message yourself" chat is always allowed.
- **Deliveries** — the allowed messages of one conversation, handed to one
  connector at a time (leased, renewed while the agent works, released if it
  disconnects, so nothing is lost).
- **Connector** — `puerto connect -- <agent>` dials out to the port (no public
  address needed, like ngrok) and runs any [ACP](https://agentclientprotocol.com)
  agent, one agent session per conversation, resumed across restarts.

One binary, several ways to run it — only the start-up config differs
(`PUERTO_AUTH`):

| Mode | For | Auth |
|---|---|---|
| `none` | your own computer | no login, no tokens; loopback only — refuses other listen addresses, foreign `Host` names and anything relayed by a proxy or tunnel |
| `token` (default) | one owner on a server | static admin + crew tokens |
| `iam` (planned) | many organizations | IAMKit accounts, per-organization crew tokens |

Inside, Puerto is always multi-tenant: every dock, conversation, ship and
rule belongs to an organization (single-owner modes use one fixed
organization), so moving from one mode to another needs no data migration.

## Run

```bash
docker compose up -d                                    # Postgres on 127.0.0.1:5449
export DATABASE_URL="postgres://puerto:puerto@127.0.0.1:5449/puerto?sslmode=disable"
export PUERTO_ADMIN_TOKEN=$(openssl rand -hex 24) PUERTO_CREW_TOKEN=$(openssl rand -hex 24)
go run ./cmd/puerto serve
```

On your own computer you can skip the tokens: `PUERTO_AUTH=none go run
./cmd/puerto serve` (the web app opens without login; the CLI and `puerto
connect` work without `-token`; browsers on other sites still can't write).
To reach it from anywhere else — a reverse proxy, a tunnel (cloudflared,
ngrok, `tailscale serve`) — use `token` mode: an open port answers 403 to
requests carrying `X-Forwarded-For`/`Forwarded`-style headers.

Open http://127.0.0.1:8740 and sign in with the admin token: inbox, customs,
docks (WhatsApp pairing QR, api docks), crew — all live over SSE. The web app
lives in `web/` (React + Vite + shadcn/ui + Tailwind v4) and is embedded into
the binary, so build it before `go build` (`cd web && pnpm install && pnpm
build`); `pnpm dev` serves it with hot reload on :5173, proxying `/api` to
`$PUERTO_URL` (default :8740). The design system is at `/design`.

Or from the CLI — link WhatsApp and allow chats (admin token):

```bash
puerto dock add whatsapp          # scan the QR: WhatsApp → Linked devices → Link a device
puerto conversations              # chats that wrote to the number
puerto allow <conversation-id>
```

### Customs

Each dock has a **default** for chats no rule covers — `ask` (hold until you
`puerto allow` them; the default), `allow` or `deny` — and a **group mode**:
`mentions` (only messages that @mention the number or reply to it; the
default) or `all`. **Rules** allow or deny chats and senders by pattern:
phone numbers (`51999123436`, `+51*`), WhatsApp ids (`…@lid`, `…@g.us`); `*`
matches anything. Other channels will match their own handles (usernames,
emails).

```bash
puerto customs                        # policy and rules (-dock ID with several docks)
puerto customs allow "+51*"           # Peruvian numbers may wake the agent
puerto customs deny 51999888777       # never this one (also muted inside groups)
puerto customs allow 120363…@g.us     # this group, mentions only
puerto customs default ask|allow|deny
puerto customs groups mentions|all
puerto customs rm <rule-id>
puerto mode <conversation-id> all     # this group: every message (or mentions|default)
puerto allow|deny <conversation-id>   # owner override, beats rules
puerto auto <conversation-id>         # follow the rules again
```

Order: your override → self chat → deny rules → allow rules → default. A deny
rule matching a group member holds that member's messages in allowed groups
(unless you allowed the conversation itself). Changing rules re-decides
existing chats; held history is never delivered retroactively (the agent can
read it with `history`).

Connect your agent (crew token), from the directory it should work in:

```bash
puerto connect -- rness --acp -m anthropic/claude-sonnet-4-5
```

Flags: `-concurrency N`, `-auto-reply` (send the agent's final text, for
agents without HTTP MCP), `-approve allow|reject` (tool permission requests;
default reject), `-deliver steer|queue`, `-server`, `-token`.

Messages that arrive while the agent is working on a conversation are
**steered** into the running turn by default (the model sees them at its next
step, like rness's F9 steer) when the agent advertises it — rness does, via
the ACP extension `_rness/steer` (`agentCapabilities._meta.rness.steer`).
Other agents, or `-deliver queue`, get them as the next prompt after the turn.
A message steered too late (the turn had just ended) is delivered again as a
new prompt, never lost.

| Env | Default | |
|---|---|---|
| `DATABASE_URL` | — | Postgres (puerto + whatsmeow tables) |
| `PUERTO_AUTH` | `token` | `none` (loopback only, no tokens) or `token` |
| `PUERTO_ADMIN_TOKEN` | — | token mode: CLI and `/api/v1` (≥16 chars) |
| `PUERTO_CREW_TOKEN` | — | token mode: agents, `/crew/ws`, `/mcp` (≥16 chars) |
| `PUERTO_ADDR` | `127.0.0.1:8740` | listen address |
| `PUERTO_PUBLIC_URL` | `http://$PUERTO_ADDR` | how agents reach `/mcp` (use https off-loopback) |
| `PUERTO_URL` | `http://127.0.0.1:8740` | CLI/connector: where the port is |

## API

Admin routes accept `Authorization: Bearer $PUERTO_ADMIN_TOKEN` or a
browser session cookie.

| | |
|---|---|
| `GET /api/v1/config` | public: `{"auth", "version"}` — how the web app must sign in |
| `POST /api/v1/session` `{"token"}`, `GET /api/v1/session`, `DELETE /api/v1/session` | browser login: trades the admin token for an HttpOnly, SameSite=Strict cookie (7 days, stateless HMAC; rotating the token ends all sessions). Cookie writes from another origin get 403 |
| `GET /api/v1/events[?last_event_id=]` | live events (SSE, below) |
| `GET /api/v1/crew` | connected agents and their deliveries in flight |
| `GET /api/v1/inbox[?status=&dock_id=]` | conversations by last activity, each with `last_ship`, `held`, `queued`, `in_flight` |
| `GET/POST /api/v1/docks`, `GET/PATCH/DELETE /api/v1/docks/{id}`, `POST …/relink`, `POST …/unlink` | docks (QR in `qr_code` while linking); PATCH `{"name"}`; unlink logs the number out and keeps history; DELETE unlinks and deletes its conversations, ships and customs |
| `POST /api/v1/docks/{id}/ships` | inject an inbound message into an `api` dock |
| `GET /api/v1/conversations[?status=allowed\|pending\|denied&dock_id=]`, `GET/PATCH /api/v1/conversations/{id}` | chats; PATCH `{"access":"auto\|allow\|deny","group_mode":"mentions\|all\|","title":…}` |
| `GET /api/v1/docks/{id}/customs`, `PATCH …/customs/policy` `{"default","group_mode"}`, `POST …/customs/rules` `{"effect","pattern"}`, `DELETE /api/v1/customs/rules/{id}` | customs |
| `GET/POST /api/v1/conversations/{id}/ships` | history; send as the agent |
| `GET /crew/ws` | crew WebSocket (`internal/crew/protocol.go`) |
| `POST /mcp` | MCP tools: `reply`, `history`, `conversations` |
| `GET /healthz` | public liveness: `{"status","version"}`, nothing about any organization |

### Events

`GET /api/v1/events` is a Server-Sent Events stream (`EventSource` with the
session cookie). Each event's `data` is JSON:

| event | data |
|---|---|
| `ready` | `{"boot"}`, first on every stream |
| `reset` | the `Last-Event-ID` is unknown (restart or too old): refetch state |
| `dock.updated` / `dock.deleted` | the dock (status, new QR) / `{"id"}` |
| `conversation.updated` | the conversation (new, title, handles, customs status) |
| `customs.updated` | `{"customs"}` of a dock |
| `ship.created` / `ship.updated` | the ship (inbound, outbound pending → sent/failed) |
| `delivery.changed` | `{"state":"claimed\|joined\|acked\|released\|expired", "delivery_id", "conversation_id", "ship_ids", "count", "owner"}` |
| `crew.changed` | the whole crew status (as `GET /api/v1/crew`) |

Event ids are `<boot>-<seq>`; the browser resends the last one on
reconnect and gets what it missed (last 512 events of this process).
Events are in memory: one process per database for now.
