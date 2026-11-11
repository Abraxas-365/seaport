# Configuration

Every environment variable that configures `seaport serve`, with defaults and validation rules, grouped by area.

`seaport serve` reads only the environment (no config file). Numeric limits take a whole number ≥ 0, where `0` turns the limit off; anything else stops the start with a validation error. Blank values count as unset. Fixed values that are not configurable (leases, retention, frame sizes…) are in [Limits](limits.md).

The CLI and the connector read a few variables of their own (`SEAPORT_URL`, `SEAPORT_ADMIN_TOKEN`, `SEAPORT_CREW_TOKEN`, `SEAPORT_AGENT`); they are listed [at the end](#cli-and-connector-variables) and in [CLI](cli.md).

## Core

| Variable | Default | Meaning |
| :--- | :--- | :--- |
| `DATABASE_URL` | none (required) | Postgres connection string. Everything lives there, WhatsApp keys included (sealed). Needs direct or session-pooled connections: Seaport uses `LISTEN`/`NOTIFY` and session advisory locks, so PgBouncer in transaction mode breaks it. |
| `SEAPORT_ADDR` | `127.0.0.1:8740` | Listen address of the HTTP server (web app, API, crew WebSocket, MCP). The image sets `0.0.0.0:8740`. |
| `SEAPORT_PUBLIC_URL` | `http://$SEAPORT_ADDR` | How agents and browsers reach the port (agents use it to find `/mcp`). Trailing `/` is removed. Use `https` off loopback: Seaport logs a warning at start for plain `http` on a non-local host. |
| `SEAPORT_LOG_LEVEL` | `info` | `debug`, `info`, `warn` or `error` (Go `slog` levels; an unknown value falls back to `info`). |
| `SEAPORT_AUTH` | `token` | Auth mode: `none`, `token` or `iam`. See [Auth modes](#auth-modes). |

## Auth modes

| Variable | Default | Meaning |
| :--- | :--- | :--- |
| `SEAPORT_AUTH` | `token` | `none`: no credentials, one organization, **loopback only**. `token`: one admin token, one organization. `iam`: people sign in with IAMKit, many organizations. |
| `SEAPORT_ADMIN_TOKEN` | none | Token mode: required, at least 16 characters; it is the bearer token for the CLI and `/api/v1`, and what you type in the web app's login. In `iam` mode the server **refuses to start** with it set (people sign in with IAMKit, agents use crew tokens); there the CLI's `SEAPORT_ADMIN_TOKEN` is an IAMKit API key (see [CLI variables](#cli-and-connector-variables)). |

Rules checked at start:

- `none` refuses a `SEAPORT_ADDR` that is not loopback (`127.0.0.1`, `::1`, `localhost`). While it runs, requests with a foreign `Host` or with forwarding headers are refused too (DNS rebinding, proxies that would make remote callers look local).
- `none` can't be combined with `SEAPORT_RUNTIME=docker` or `SEAPORT_EGRESS_ADDR` (hosted slots sign in with tokens).
- `token` needs `SEAPORT_ADMIN_TOKEN`.
- `iam` needs the [IAMKit variables](#iamkit), `SEAPORT_SECRET_KEY` and no admin token.

`SEAPORT_CREW_TOKEN` is **ignored by the server** (it logs a warning): every agent connects with a crew token of its own, created per agent. See [Permissions](permissions.md) for what each mode grants and [HTTP API](http-api.md#authentication) for how requests authenticate.

## IAMKit

Only read with `SEAPORT_AUTH=iam`. `scripts/iamkit-provision.sh` writes them to `.iamkit.env` (see [IAM mode](../guides/operations/iam-mode.md)).

| Variable | Default | Meaning |
| :--- | :--- | :--- |
| `IAMKIT_URL` | none (required) | How this process reaches IAMKit. |
| `IAMKIT_PUBLIC_URL` | `IAMKIT_URL` | How browsers reach IAMKit, when that differs (for example IAMKit on the same Docker network). |
| `IAMKIT_ISSUER` | `IAMKIT_PUBLIC_URL` | The issuer of IAMKit's tokens (its `JWT_ISSUER`), when it differs from the public URL. |
| `IAMKIT_ENVIRONMENT_ID` | none (required) | IAMKit environment Seaport lives in. |
| `IAMKIT_APPLICATION_ID` | none (required) | The Seaport application in IAMKit. |
| `IAMKIT_RESOURCE_ID` | none (required) | The Seaport resource (permission catalog). |
| `IAMKIT_AUDIENCE` | none (required) | Token audience; fixed when the resource is created. |
| `IAMKIT_WEB_CLIENT_ID` | none (required) | The web app's public OAuth client. |

`IAMKIT_URL`, `IAMKIT_PUBLIC_URL` and `SEAPORT_PUBLIC_URL` must be `http(s)` URLs, and `https` unless the host is `localhost` or a loopback address. The exception is `IAMKIT_URL`, which may be plain `http` on a private network (a single-label service name such as `http://iamkit:8080`, or a private IP).

## Secret key

`SEAPORT_SECRET_KEY` is the key that seals secrets at rest: the WhatsApp store (private keys, sessions), GitHub Apps' private keys and webhook secrets, Jira API tokens and webhook secrets, agent secrets, message images, harness homes and, in `iam` mode, the IAMKit tokens of browser sessions. A random value of at least 32 characters, for example `openssl rand -base64 32`; shorter keys and placeholder-looking ones (fewer than 12 distinct characters) are refused.

| Variable | Default | Meaning |
| :--- | :--- | :--- |
| `SEAPORT_SECRET_KEY` | none | The key itself. **Required in `iam` mode.** When set it wins over the key file. |
| `SEAPORT_SECRET_KEY_FILE` | `<user config dir>/seaport/secret.key` (`/data/secret.key` in the image) | Where the key is read from when `SEAPORT_SECRET_KEY` is unset. If the file doesn't exist it is generated there on first start (random 32 bytes, base64url, mode `0600`, written and synced before being linked into place so concurrent starts agree). A file readable by other users is refused: `chmod 600` it. |

Things to know:

- **Back it up apart from the database.** A dump without the key is useless (which is also why a stolen dump is). Losing the key means linking every WhatsApp dock again, creating every GitHub dock's App again, linking every Jira dock again, setting every agent secret again and losing every stored image and harness home; in `iam` mode it also signs everyone out.
- **No new key over sealed data.** Seaport never generates a key while `wa_devices`, `agent_secrets`, `ship_attachments`, `hosting_homes`, `github_apps` or `jira_sites` have rows: a new key could not open them, so the start fails and asks you to restore the file or set `SEAPORT_SECRET_KEY`.
- **With several processes, set it explicitly** (same value everywhere) instead of letting each generate its own file. See [Several processes](../guides/operations/several-processes.md).
- **Changing it** makes sealed values unreadable. The start checks one WhatsApp device first, so a changed key stops the start instead of failing every dock.
- In the image, `/data` holds the generated key: mount a volume.

## Storage

Message images and harness homes go to a blob store, sealed with the secret key.

| Variable | Default | Meaning |
| :--- | :--- | :--- |
| `SEAPORT_BLOB_URL` | `blobs/` next to the key file (`/data/blobs` in the image) | `file:///dir` (one server), `s3://bucket?region=…` (AWS credentials from the usual `AWS_*` variables), an S3-compatible store with `s3://bucket?region=…&endpoint=http://host:9000&use_path_style=true`, or `off`. `mem://` also parses but keeps data in memory only (tests). With `off`, or with no key file path and no URL, no images are kept: they arrive as a note in the message text. |
| `AWS_ACCESS_KEY_ID`, `AWS_SECRET_ACCESS_KEY`, … | none | Credentials for `s3://` URLs, read by the S3 client from the standard AWS environment. |

Several Seaport processes need one shared store (S3 or compatible); with local directories each process sees only the images it received. Hosted-agent environments with a harness home (`home:`) need the blob store too.

## WhatsApp

WhatsApp docks have no variables of their own. They use `DATABASE_URL` (the sealed device store) and the [secret key](#secret-key), and are paced by the [outbound limits](#outbound-limits). One thing is fixed in code: each WhatsApp send is spaced at least 1.2 seconds from the previous one of that dock. See [WhatsApp](../guides/whatsapp.md).

## GitHub

[GitHub docks](../guides/github.md) need the [secret key](#secret-key) (it seals each App's private key and webhook secret) and a public `https` `SEAPORT_PUBLIC_URL`, which GitHub calls back. Without a secret key the `github` channel is off.

| Variable | Default | Meaning |
| :--- | :--- | :--- |
| `SEAPORT_GITHUB_URL` | `https://github.com` | GitHub's web address, where the App is created and installed. Set it for GitHub Enterprise Server (`https://github.example.com`). The web app's form policy (CSP `form-action`) allows it. |
| `SEAPORT_GITHUB_API_URL` | `https://api.github.com` | GitHub's REST API (`https://github.example.com/api/v3` on Enterprise Server). |
| `SEAPORT_GITHUB_OIDC_URL` | `https://token.actions.githubusercontent.com` | The issuer of GitHub Actions' OIDC tokens, which [workflows](../guides/github.md#trigger-an-agent-from-a-workflow) send (`https://github.example.com/_services/token` on Enterprise Server). Seaport reads its keys from `/.well-known/openid-configuration`. |

## Jira

[Jira docks](../guides/jira.md) need the [secret key](#secret-key) (it seals each dock's API token and webhook secret) and a public `https` `SEAPORT_PUBLIC_URL`, which Jira calls back. Without a secret key the `jira` channel is off.

| Variable | Default | Meaning |
| :--- | :--- | :--- |
| `SEAPORT_JIRA_HOSTS` | `*.atlassian.net` | Comma-separated allow-list of the Jira sites a dock may name. An entry is an exact host (`jira.example.com`, or `host:port`) or `*.domain` for every subdomain. Seaport sends the account's credentials only to these hosts; a dock naming another one is refused (`400`). |

## Outbound limits

WhatsApp bans numbers that send in bursts, so replies over a limit are refused with `429` (`RATE_LIMITED`, with `Retry-After`) and nothing is sent or stored; the MCP `reply` tool returns the same message to the agent. Windows slide over the last minute or hour and count every reply of the dock (agents, the web app, the API). Failed sends count too, so an agent retrying against a broken dock slows down as well.

| Variable | Default | Meaning |
| :--- | :--- | :--- |
| `SEAPORT_SEND_PER_MINUTE` | `20` | Outbound messages per dock per minute. |
| `SEAPORT_SEND_PER_HOUR` | `300` | Outbound messages per dock per hour. |
| `SEAPORT_SEND_PER_CHAT_MINUTE` | `8` | Outbound messages per conversation per minute. |
| `SEAPORT_SEND_ORG_PER_HOUR` | `1000` | Outbound messages per organization per hour, across its docks. |

## Quotas

Each organization gets the same limits (one organization in `none` and `token` mode, many in `iam` mode). Going over a count is refused with `422` `QUOTA_EXCEEDED` (details `quota` and `limit`). A connector over the agent limit keeps retrying with backoff. `SEAPORT_SEND_ORG_PER_HOUR` above is the third quota.

| Variable | Default | Meaning |
| :--- | :--- | :--- |
| `SEAPORT_MAX_DOCKS` | `10` | Docks per organization. |
| `SEAPORT_MAX_CREW_CONNECTIONS` | `20` | Agents connected at once per organization, counted across every process. |
| `SEAPORT_MAX_SLOTS` | `10` | Hosted-agent slots per organization (see [Hosting](#hosting)). |

## Rate limit

A token bucket per caller on `/api/`, `/crew/ws` and `/mcp` (public routes aren't limited). The caller is organization + role + subject + credential; agents sharing one crew token share its bucket. Over the limit: `429` with `Retry-After`. The bucket is in memory, per process: with N processes a caller may get up to N× the rate.

| Variable | Default | Meaning |
| :--- | :--- | :--- |
| `SEAPORT_API_RATE` | `20` | Requests per second per caller. |
| `SEAPORT_API_BURST` | `60` | Burst size. |

## Hosting

Settings for [hosted agents](../guides/hosting/hosted-agents.md). The environment's own fields are in [Environment spec](environment-spec.md).

| Variable | Default | Meaning |
| :--- | :--- | :--- |
| `SEAPORT_RUNTIME` | `off` | `off` builds and runs nothing (environment builds are refused). `docker` runs hosted agents on the Docker engine; needs `token` or `iam` mode. Any other value stops the start. |
| `DOCKER_HOST` (and `DOCKER_TLS_VERIFY`, `DOCKER_CERT_PATH`, `DOCKER_API_VERSION`) | the local socket | The engine, read by the Docker client. In production use a socket proxy that allows only containers, exec, images/build, volumes and networks (see `deploy/`). The start fails if the engine doesn't answer. |
| `SEAPORT_AGENT_URL` | `SEAPORT_PUBLIC_URL` | How slot containers reach the port (for example `http://seaport:8740` on a shared network, or `http://host.docker.internal:8740` for a port on the host). |
| `SEAPORT_RUNTIME_NETWORK` | `bridge` | The Docker network of slots. Make it `--internal` when you use the egress proxy; otherwise slots can reach the internet around it and the port warns at start. |
| `SEAPORT_RUNTIME_HOST_GATEWAY` | `true` | Maps `host.docker.internal` to the host in slots (Linux engines don't by themselves). Only the exact value `false` turns it off. |
| `SEAPORT_IMAGE_RNESS` | `ghcr.io/abraxas-365/seaport-rness:<tag>` | Base image for `rness` environments without `image:`. `<tag>` is the release's version (for example `0.7.0`), or `latest` for development builds. |
| `SEAPORT_IMAGE_CLAUDE_CODE` | `ghcr.io/abraxas-365/seaport-claude-code:<tag>` | The same for `claude-code`. |
| `SEAPORT_HOST_MAX_CPUS` | `4` | Most CPUs one slot may ask for (`0` = no limit). |
| `SEAPORT_HOST_MAX_MEMORY_MB` | `8192` | Most memory (MB) one slot may ask for (`0` = no limit). |
| `SEAPORT_MAX_SLOTS` | `10` | Slots per organization (`0` = no limit). |
| `SEAPORT_GATEWAY_ANTHROPIC_URL` | `https://api.anthropic.com` | Where the model gateway forwards Anthropic calls. Must be an `http(s)` URL. |
| `SEAPORT_EGRESS_ADDR` | none (off) | Listen address `host:port` (for example `:8741`) of the egress proxy that is the slots' only way out. Off: slots get no proxy. Needs `token` or `iam` mode; a taken address fails the start. |
| `SEAPORT_EGRESS_URL` | host of `SEAPORT_AGENT_URL` + the listen port | How slots reach the proxy. Must be an `http://` URL without credentials. |
| `SEAPORT_EGRESS_PRIVATE` | `false` | Only the exact value `true` lets allowed hosts resolve to private, loopback or link-local addresses (local testing). |

Hosted slots receive their own variables (`SEAPORT_URL`, `SEAPORT_CREW_TOKEN`, `SEAPORT_SLOT`, `SEAPORT_HARNESS`, `SEAPORT_GATEWAY`, proxy variables); the port sets them, you don't. See [Network](../guides/hosting/network.md).

## CLI and connector variables

Read by the `seaport` binary when it is not serving (see [CLI](cli.md)).

| Variable | Default | Used by | Meaning |
| :--- | :--- | :--- | :--- |
| `SEAPORT_URL` | `http://127.0.0.1:8740` | CLI, `connect` | Where the port is (`-server` for `connect`). |
| `SEAPORT_ADMIN_TOKEN` | none | CLI | Bearer token of the admin commands: the admin token in `token` mode, an IAMKit organization API key (`ik_key_…`) in `iam` mode. Not needed against a `none` port. |
| `SEAPORT_CREW_TOKEN` | none | `connect` | The agent's crew token (`-token`); it names the agent. Not needed against a `none` port. |
| `SEAPORT_AGENT` | none | `connect` | The agent to work as (`-agent`). Required against a `none` port; optional with a crew token. |
| `SEAPORT_ADDR` | `127.0.0.1:8740` | `seaport healthcheck` | The local address it asks for `/healthz` (a wildcard host becomes `127.0.0.1`). |
