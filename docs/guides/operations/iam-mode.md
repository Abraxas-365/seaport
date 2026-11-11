# Host many organizations with IAM mode

Run one port for several organizations, with people, roles and API keys managed by an IAMKit server.

In `token` mode there is one admin token and one organization. That suits a single team. When you host several teams or customers on one port, each needs its own people, roles and data. `SEAPORT_AUTH=iam` hands identity to [IAMKit](https://github.com/Abraxas-365/iamkit), a self-hosted identity and authorization server, and keeps every organization's data apart.

## What changes

- **People** sign in to the web app through IAMKit's hosted login. The server does the OAuth code and PKCE exchange, so the browser holds only an HttpOnly session cookie. IAMKit's tokens are sealed in Postgres and refreshed there. Someone in several organizations picks one at sign-in and can switch from the account menu. Signing out ends the IAMKit session too.
- **Permissions** come from [`iam/permissions.json`](../../../iam/permissions.json). Every admin route requires one, the web app hides what your role can't do, and the live event stream carries only the event types you may read. See [Permissions](../../reference/permissions.md).
- **The CLI** authenticates with an IAMKit organization API key (`ik_key_…`) as `SEAPORT_ADMIN_TOKEN`. An IAMKit access token works too.
- **Agents** keep using crew tokens. People with `seaport:crew-tokens:write` create them, each for one agent.
- **Tenancy**: an object of another organization is always a 404, for people, agents, deliveries, MCP tools and the event stream.

## Roles

The manifest defines three roles:

| Role | What it can do |
| :--- | :--- |
| `Admin` | Everything. |
| `Operator` | Works the inbox: allows chats and sends replies. Sees docks, customs, agents, environments and the crew, without changing them or any tokens. |
| `Viewer` | Read-only. |

## Settings

Set these on the port (in `deploy/.env` when you use the compose file):

| Variable | Meaning |
| :--- | :--- |
| `SEAPORT_AUTH` | `iam`. |
| `SEAPORT_SECRET_KEY` | Required. It seals the stored sessions. Use `openssl rand -base64 32`, and keep it. |
| `SEAPORT_PUBLIC_URL` | The port's public URL. |
| `IAMKIT_URL` | How the port reaches IAMKit. |
| `IAMKIT_PUBLIC_URL` | How browsers reach IAMKit, when that differs. For example when IAMKit is on the same Docker network. |
| `IAMKIT_ISSUER` | The issuer of IAMKit's tokens, when it differs from the public URL. |
| `IAMKIT_ENVIRONMENT_ID`, `IAMKIT_APPLICATION_ID`, `IAMKIT_RESOURCE_ID`, `IAMKIT_AUDIENCE`, `IAMKIT_WEB_CLIENT_ID` | Identify Seaport inside IAMKit. The provisioning script writes them. |

`SEAPORT_ADMIN_TOKEN` must be unset: the port refuses to start with both. These URLs and `SEAPORT_PUBLIC_URL` must be `https`, except on localhost. `IAMKIT_URL` may also be plain `http` on a private network, such as `http://iamkit:8080`. `IAMKIT_AUDIENCE` is fixed when the resource is created. See [Configuration](../../reference/configuration.md) for each one.

## Provision IAMKit

`scripts/iamkit-provision.sh` sets IAMKit up for Seaport. It needs `curl`, `jq` and an IAMKit management key, and it is idempotent: run it again after editing `iam/permissions.json`.

```sh
IAMKIT_URL=https://iam.example.com IAMKIT_KEY=ik_mgmt_… \
APP_URL=https://port.example.com \
OWNER_EMAIL=you@example.com OWNER_PASSWORD='a long password' \
  scripts/iamkit-provision.sh          # --dry-run shows the changes first
```

It creates, when missing:

- a project and environment,
- a first organization,
- the application, and the Seaport resource with its permission catalog and roles,
- the browser OAuth client, with the redirect `APP_URL/auth/callback`,
- organization API keys, enabled and allowed every Seaport permission,
- a first user, `OWNER_EMAIL`, made an `Admin` (when you give it).

It writes `.iamkit.env` with `SEAPORT_AUTH`, `SEAPORT_PUBLIC_URL` and the `IAMKIT_*` settings. Keep that file out of git. Seaport reads only those settings from it; the role and organization ids are for you.

Then start the port with them and a secret key:

```sh
set -a; . ./.iamkit.env; set +a
export SEAPORT_SECRET_KEY=$(openssl rand -base64 32)   # keep it: it opens the stored sessions
seaport serve
```

With the compose file, paste the `.iamkit.env` values into `deploy/.env` instead, leave the admin token empty, and set `SEAPORT_SECRET_KEY`. See [Deploy](deploy.md).

### Removing a permission

Removing a permission from the manifest keeps it in IAMKit until you pass `--prune`, because IAMKit strips a removed permission from every role and key at once. Add the new permission, deploy, and only then run the script with `--prune`.

## Sign in

Open the port's URL. Seaport sends you to IAMKit, you sign in, and IAMKit sends you back with a session. The first sign-in is the `OWNER_EMAIL` you provisioned. Add other people to the organization in IAMKit and give them one of the roles.

For the CLI, create an organization API key in IAMKit's admin with the permissions you want, then:

```sh
export SEAPORT_URL=https://port.example.com
export SEAPORT_ADMIN_TOKEN=ik_key_…
seaport crew
```

## When changes take effect

Seaport asks IAMKit about people's tokens and API keys on every request:

- **Removing someone, suspending them, or revoking an API key** works on their next request.
- **Revoking a crew token** closes that agent's connections at once.
- **A role change** reaches a person at their next access token: at the session's next refresh, at most the access token's lifetime (15 minutes by IAMKit's default), or when they sign in again.
- **An access token used as a bearer** keeps the permissions it was issued with until it expires.
- **An API key** keeps the permissions it was created with. A role change never touches it: revoke it and create another.

## Next

- [Deploy](deploy.md)
- [Several processes](several-processes.md)
- [Security model](../../explanation/security-model.md)
