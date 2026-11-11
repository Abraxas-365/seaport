# Deploy on one server with HTTPS

Run Seaport on a single server with a public name, automatic HTTPS, Postgres and daily backups, using the `deploy/` Docker Compose file.

WhatsApp keeps a long connection, agents connect over WebSockets and browsers need HTTPS, so the port belongs on a server with a stable address, not on a laptop. The compose file in `deploy/` gives you the whole stack with one command.

## What you get

| Service | What it does |
| :--- | :--- |
| `postgres` | Postgres 16. Everything Seaport stores lives here. |
| `seaport` | The port, built from this repository. It is not published; only Caddy reaches it. |
| `caddy` | Terminates HTTPS with automatic Let's Encrypt certificates, redirects HTTP to HTTPS, and proxies WebSockets (`/crew/ws`) and event streams as they are. |
| `backup` | A `pg_dump` into `deploy/backups` every day. See [Backups and upgrades](backups-and-upgrades.md). |

Only Caddy publishes ports (80 and 443). Seaport and Postgres stay on the compose network. Behind a proxy the port runs in `token` or `iam` mode: `none` refuses proxied requests.

## Before you start

- A server with Docker and Docker Compose.
- A DNS name (for example `port.example.com`) pointing at it, and ports 80 and 443 open.
- A checkout of the Seaport repository on the server. The compose file builds the image from it.

## First start

```sh
cd deploy
cp .env.example .env
```

Edit `.env` and set:

| Variable | Value |
| :--- | :--- |
| `SEAPORT_DOMAIN` | Your DNS name, such as `port.example.com`. |
| `POSTGRES_PASSWORD` | A random value. Use hex only (`openssl rand -hex 32`): it goes into a URL unescaped. |
| `SEAPORT_ADMIN_TOKEN` | A random value of at least 16 characters (`openssl rand -hex 32`). It is the web sign-in and the CLI's credential. |

Then start the stack and watch the port come up:

```sh
docker compose up -d --build
docker compose logs -f seaport
```

Seaport applies its database migrations at start, and tells you which setting a mode is missing if the configuration is incomplete.

Open `https://port.example.com` and sign in with the admin token. From there, link a WhatsApp number, create an agent and assign it to the dock, then create a crew token for it. The [first port tutorial](../../tutorials/first-port.md) walks through it, and [first agent](../../tutorials/first-agent.md) connects one. On the machine that runs the agent:

```sh
SEAPORT_URL=https://port.example.com SEAPORT_CREW_TOKEN=sp_crew_… seaport connect -- rness --acp
```

## The secret key

`SEAPORT_SECRET_KEY` seals WhatsApp keys, agent secrets, message images and harness homes in the database. In token mode it is optional: if you leave it unset, Seaport generates one on first start into the `seaport_data` volume (`/data/secret.key`).

Either way, keep a copy apart from your backups. A database dump without it is useless. See [Backups and upgrades](backups-and-upgrades.md).

## Optional settings

All of these go in `.env`. The defaults are fine to start with.

| Variable | Meaning |
| :--- | :--- |
| `SEAPORT_AUTH` | `token` (default) or `iam`. See [IAM mode](iam-mode.md). |
| `SEAPORT_SECRET_KEY` | Your own key, from `openssl rand -base64 32`. |
| `SEAPORT_BLOB_URL` | Where message images go. Default: `/data/blobs` in the `seaport_data` volume. Or an S3 bucket such as `s3://my-bucket?region=us-east-1`, with `AWS_ACCESS_KEY_ID` and `AWS_SECRET_ACCESS_KEY`. |
| `SEAPORT_PUBLIC_URL` | Only if the public URL isn't `https://$SEAPORT_DOMAIN`, for example on a non-443 port. |
| `HTTP_PORT`, `HTTPS_PORT` | Published ports, `80` and `443` by default. |
| `SEAPORT_VERSION` | Image tag. |
| `SEAPORT_SEND_*`, `SEAPORT_MAX_*`, `SEAPORT_API_RATE`, `SEAPORT_API_BURST` | Rate limits and quotas. See [Configuration](../../reference/configuration.md). |
| `BACKUP_EVERY_SECONDS`, `BACKUP_KEEP_DAYS`, `BACKUP_UID` | Backup interval (a day), retention (14 days), and the owner of dump files (root). |
| `SEAPORT_RUNTIME` | `docker` to run [hosted agents](../hosting/hosted-agents.md). |

Every variable the port reads is in [Configuration](../../reference/configuration.md). Seaport warns at start when `SEAPORT_PUBLIC_URL` is plain `http` on a host that isn't local.

## Hosted agents

To let the port run agents itself, set `SEAPORT_RUNTIME=docker` in `.env` and start with the `hosting` profile:

```sh
docker compose --profile hosting up -d --build
```

This adds a Docker socket proxy, and runs slots on an internal network where they reach only Seaport. See [Run agents on the port](../hosting/hosted-agents.md).

> [!WARNING]
> Whoever controls Seaport controls the Docker engine behind it. Give hosting a server or VM of its own.

## Try it locally

Set `SEAPORT_DOMAIN=localhost` and Caddy uses its own certificate authority. Trust its root certificate, or pass it to `curl`:

```sh
docker compose cp caddy:/data/caddy/pki/authorities/local/root.crt .
curl --cacert root.crt https://localhost/healthz
```

If ports 80 or 443 are taken, set `HTTP_PORT`, `HTTPS_PORT` and `SEAPORT_PUBLIC_URL=https://localhost:<port>`.

## Run only the image

The repository's `Dockerfile` builds the port alone:

```sh
docker build -t seaport .
```

The image is distroless and runs as a non-root user. `seaport healthcheck` is its container health check.

## Next

- [Backups and upgrades](backups-and-upgrades.md)
- [IAM mode](iam-mode.md): many organizations on one port.
- [Several processes](several-processes.md): replicas of the port.
- [Security model](../../explanation/security-model.md)
