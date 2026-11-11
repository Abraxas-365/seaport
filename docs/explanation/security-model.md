# Security model

What Seaport protects, who it trusts, and where each guarantee comes from, so you can judge whether it fits your deployment.

Seaport sits between strangers who send messages and agents that can act on them. The design assumes two things: anyone can message a connected number, and the text of a message is untrusted input to an agent. Most of the model follows from that.

## Threats it is built for

| Threat | What stops it |
| :--- | :--- |
| A stranger messages a number and wakes your agent. | [Customs](#customs-are-the-gate): nothing reaches an agent until you allow the chat. |
| A prompt-injected agent changes who can reach it. | Agents have no tool that edits customs, docks or tokens. |
| A secret leaks through the admin API, an event or an agent tool. | Secrets are sealed and never returned. |
| One customer reads another's data. | Every row belongs to an organization, and other organizations' objects are `NotFound`. |
| A hosted agent steals your model key or reaches your network. | The key never enters the container, and egress is an allowlist. |
| A web page attacks the port from a browser. | Origin checks, strict cookies and a locked-down content policy. |
| Someone floods the port. | Rate limits and quotas. |

It does not defend against a malicious operator: whoever holds the admin credential, the database and the secret key controls everything.

## Modes

`SEAPORT_AUTH` picks who may call the port.

| Mode | Who calls | Rules |
| :--- | :--- | :--- |
| `none` | Anyone on this machine. One organization. | Loopback only. The port refuses to start on another address. |
| `token` | Whoever holds the admin token. One organization. | The token is at least 16 characters. |
| `iam` | People and API keys from IAMKit. Many organizations. | Permissions per role. See [IAM mode](../guides/operations/iam-mode.md). |

In `none` mode, the port goes further than binding to loopback. It answers only to loopback `Host` names, which defeats a web page that re-points its own domain at `127.0.0.1` (DNS rebinding). It also refuses any request with a forwarding header (`X-Forwarded-For`, `Forwarded` and similar). A reverse proxy or tunnel on the same machine would make remote callers look local, and these headers are how it shows. If you put a proxy in front, use `token` or `iam`.

Agents don't use these credentials. Each connects with a **crew token** of its own, tied to one agent and one organization. Only a hash of the token is stored, and it is shown once. Revoking it closes its connections in every process. In `none` and `token` mode, every admin credential holds every permission. In `iam` mode, [permissions](../reference/permissions.md) come from roles.

### Browsers

The web app signs in with an HttpOnly cookie set to `SameSite=Strict`, and `Secure` when the public URL is `https`. In `iam` mode the cookie holds only a random id: IAMKit's tokens stay on the server, sealed. Writes that a browser sends from another origin are rejected. The web app is served with a content security policy that allows only its own scripts, forbids framing (`frame-ancestors 'none'`), and has no inline scripts.

## Customs are the gate

[Customs](../guides/customs.md) decide whether a chat may reach an agent: an owner's override, deny rules, allow rules, then the dock's default. A new dock asks first. A message from a chat that isn't allowed is held, and the agent never sees it.

Agents can't move this gate. The tools they get (`reply`, `history`, `attachment`, `conversations`, and `skill`) read and answer; none of them allows a chat, edits a rule or touches a token. An agent that is tricked by a message still can't let more strangers in. The `reply` tool answers only allowed chats, so it can't start a conversation with someone you haven't allowed.

## Secrets

Secrets are the WhatsApp session keys, agent secrets (such as repo credentials or model keys), message images, harness homes, and, in `iam` mode, IAMKit tokens of browser sessions. They are sealed in the database with `SEAPORT_SECRET_KEY` (AES-GCM), and each value is sealed for its own place, so a value copied to another row doesn't open.

- **Never returned.** The admin API answers secret names and flags, never values. The code that opens an agent's secrets is limited by tests to one caller: the agent's own crew connection when it fetches its profile (`Equip`). No admin handler, event or agent tool can hand a secret back.
- **Never in events.** Events are stored in clear for an hour, so services never publish secrets. The WhatsApp pairing QR code is dropped from dock events too; the web app fetches it separately.
- **Never in specs.** An environment names secrets as `${NAME}`. Setup scripts and harness homes are stored and baked into images, so the port refuses homes that contain strings that look like credentials.
- **A stolen dump is not enough.** A database dump holds sealed values only. The key is not in it. See [Backups and upgrades](../guides/operations/backups-and-upgrades.md).

## Hosted agents

[Hosted agents](../guides/hosting/hosted-agents.md) run code the port didn't write, so the container is the boundary:

- **Hardened container.** Read-only root file system, all Linux capabilities dropped, `no-new-privileges`, user `1000:1000`, a process limit, and CPU and memory limits. Only `/tmp` (a small tmpfs), the home, the state volume and the workspace are writable.
- **No Docker socket.** The port talks to Docker through a socket proxy that allows only the calls it makes. The socket is never mounted into Seaport or any slot. Even so, whoever controls the port controls that engine, so give hosting its own server.
- **The key stays out.** With `models.anthropic`, a slot holds only its own crew token. The [gateway](../guides/hosting/network.md#model-gateway) adds the real key on the port's side. Neither the agent's profile nor a settings file in a repo can point the harness around the gateway.
- **An allowlist for the network.** On an internal Docker network, the [egress proxy](../guides/hosting/network.md#egress-proxy) is the only way out. It allows the environment's hosts and its repos' hosts, never private addresses, and records what it refused.
- **Short-lived identity.** Each run gets a crew token of its own, revoked when the slot stops. Only a slot's token can read the slot's workspace or use the model gateway.
- **No token on disk.** Git asks the port for repo credentials each time.
- **Limits.** A cap on slots per organization and on what one slot may ask for.

## Limits and quotas

Per-organization quotas (docks, crew tokens, live agent connections, hosted slots) bound what one tenant can use. Outbound sends are limited per minute and hour for a dock, a chat, and an organization, so a compromised agent can't spam through a linked number. The API itself has a per-caller token bucket. It is kept per process, so with several processes a caller may get up to that many times the rate. Message images are checked by their bytes, not the sender's declared type, and served with `nosniff` and a sandbox policy. See [Limits](../reference/limits.md).

## Tenancy

Every row belongs to an organization, and rows that reference each other must share it, enforced by the database. Another organization's object is always `NotFound`, never `Forbidden`, so you can't learn that it exists. Events carry their organization, and the stream shows a caller only their own, and only the types their permissions may read. A crew connection and each tool call act for the token's organization.

## Public routes

Two routes need no credentials: `/healthz`, and `/api/v1/config`, which tells the web app the auth mode and the version before sign-in. Neither reveals anything about an organization, a dock or an agent.

The routes GitHub calls are public too, each with its own proof instead of a credential: the App's redirects (`/github/apps/created`, `/github/apps/installed`) need the state Seaport signed for that dock an hour earlier at most, and a webhook (`/github/webhook/{dock}`) needs the HMAC signature of that dock's App secret. A test in the build keeps the list of public routes explicit.

## Where to read more

- [Customs](../guides/customs.md)
- [IAM mode](../guides/operations/iam-mode.md)
- [Network](../guides/hosting/network.md)
- [Known limitations](../project/known-limitations.md)
