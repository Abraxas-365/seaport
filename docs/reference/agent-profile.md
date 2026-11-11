# Agent profile

The schema of an agent's profile: the instructions, skills, MCP servers and options that the harness an agent runs in is told and given, with every limit and rule.

A profile is versioned. Each save makes a new, immutable version, and the agent runs its current one. You edit a profile in the web app (Agentes), with `seaport agents profile`, or with the API.

## How a profile is applied

- The connector fetches the agent's kit from `GET /crew/profile` when it starts, on each `welcome`, and on each `profile` frame. See [Crew protocol](crew-protocol.md).
- When the kit's `revision` is new, the connector sends `drain`, waits until its work is acked, and restarts the harness with the new kit. It resumes its sessions.
- `hello.profile_version` says which version a connection runs. The crew status marks it `outdated` when it isn't the agent's current version.
- Instructions go to the harness as the system prompt (appended) when the harness supports it, otherwise ahead of the first prompt of each session. A resumed session gets the instructions again with its next prompt when they changed.
- Skills never touch the agent's machine: the port's [MCP `skill` tool](mcp-tools.md#skill) serves them on demand.

For how to write a good profile, read [Profiles](../guides/profiles.md).

## Fields

| Field | Type | Default | Limit | Meaning |
| :--- | :--- | :--- | :--- | :--- |
| `base` | integer | none | the current version | Only when saving: the version you edited from. `0` for an agent with no profile yet. |
| `instructions` | string | empty | 32768 characters | Text given to the harness as system-level instructions. |
| `skills` | list of skill | empty | 50 skills | On-demand instructions. See [Skill](#skill). |
| `mcp_servers` | list of MCP server | empty | 20 servers | Extra MCP servers for every session. See [MCP server](#mcp-server). |
| `approve` | string | empty | `allow`, `reject` or empty | How the connector answers the harness's permission requests. Empty leaves it to the connector's own `-approve` flag, which rejects by default. |
| `model` | string | empty | one word, at most 100 characters | The model, picked among the harness's session options. Empty leaves the harness default. |
| `effort` | string | empty | one word, at most 100 characters | The reasoning effort, picked among the harness's session options. Empty leaves the harness default. |

Secrets are not a field of the profile: they belong to the agent and are set apart. See [Secrets](#secrets).

All text is trimmed, must be valid UTF-8 and may not contain control characters other than newline, tab and carriage return. `model` and `effort` can't contain spaces or control characters.

The unknown fields of a save request are refused.

### Skill

A skill is a `SKILL.md`: a name, when to use it, and its instructions.

| Field | Type | Required | Limit | Meaning |
| :--- | :--- | :--- | :--- | :--- |
| `name` | string | yes | `^[a-z0-9][a-z0-9_-]{0,63}$`: 1 to 64 lowercase letters, digits, `-` or `_`, starting with a letter or digit. Unique in the profile. | What the model passes to the `skill` tool. |
| `description` | string | yes | 1024 characters | When to use the skill. The model sees it in the tool's description. |
| `body` | string | yes | 65536 characters | The instructions the tool returns. |

### MCP server

A streamable-HTTP MCP server added to every session, next to the port's own `seaport` server.

| Field | Type | Required | Limit | Meaning |
| :--- | :--- | :--- | :--- | :--- |
| `name` | string | yes | `^[a-z0-9][a-z0-9_-]{0,31}$`: 1 to 32 lowercase letters, digits, `-` or `_`. Unique in the profile. `seaport` is reserved. | The server's name in the harness. |
| `url` | string | yes | `http` or `https`, with a host, no user info, at most 2048 characters | The server's address. |
| `headers` | list of header | no | 20 per server | Headers sent with every request. |

A header is `{"name", "value"}`:

- `name` matches an HTTP token (letters, digits and ``!#$%&'*+.^_`|~-``), 1 to 100 characters.
- `value` has at most 4096 characters and no carriage return, line feed or NUL character.
- A value may name a secret of the agent as `${NAME}`. The port fills it in for the agent only. A save is refused when a header names a secret the agent doesn't have.

## Secrets

Secrets belong to the agent, not to a profile version. They are stored sealed with the port's [secret key](configuration.md#secret-key), and the API answers names and flags only, never values.

| Item | Rule |
| :--- | :--- |
| Name | An environment variable name: `^[A-Z_][A-Z0-9_]{0,63}$`, and not starting with `SEAPORT_`. |
| Value | 1 to 16384 bytes, no NUL. |
| `env` | `true` also gives the secret to the harness as an environment variable of that name. |
| Count | At most 50 per agent. |
| Use in headers | `${NAME}` in an MCP header value. |
| Delete | Refused while an MCP header, a repo credential or a model key of the agent's environment uses it. |

Without a secret key on the port, setting a secret is refused. The kit that carries the resolved secrets reaches only the agent's own crew token (`GET /crew/profile`). No admin route, event or MCP tool returns a secret.

Set a secret with `PUT /api/v1/agents/{id}/secrets/{name}` and the body `{"value": "…", "env": false}`; list names with `GET /api/v1/agents/{id}/secrets`; delete one with `DELETE /api/v1/agents/{id}/secrets/{name}`. Or use `seaport agents secrets`; see [CLI](cli.md#secrets).

When a secret changes, the port tells the agent's connections to fetch their kit again, as it does for a saved profile.

## Versions

- A save needs `base` to be the current version. If another editor saved first, the save fails with `409 CONFLICT` (`the profile changed since it was loaded…`). Load the profile again and apply your changes.
- Every save makes version `current + 1` and points the agent at it. The history keeps every version.
- `GET /api/v1/agents/{id}/profile` returns the current version, or an empty profile (version `0`, no content) when there is none. `…/profile/versions/{version}` returns a past one, and `…/profile/versions` lists them (`version`, `created_by`, `created_at`), newest first.
- A saved profile is announced with the `agent.updated` event. See [Events](events.md).

## Example

```json
{
  "base": 2,
  "instructions": "You are the support agent of Example Co. Answer in the language of the customer. Keep replies short.",
  "skills": [
    {
      "name": "refunds",
      "description": "Use when a customer asks for a refund or a charge back.",
      "body": "1. Ask for the order number.\n2. Check the order in the billing tool.\n3. Refund up to 50 EUR; escalate above."
    }
  ],
  "mcp_servers": [
    {
      "name": "billing",
      "url": "https://billing.example.com/mcp",
      "headers": [{ "name": "Authorization", "value": "Bearer ${BILLING_TOKEN}" }]
    }
  ],
  "approve": "allow",
  "model": "sonnet",
  "effort": "medium"
}
```

Send it with:

```bash
curl -X PUT https://port.example.com/api/v1/agents/$AGENT_ID/profile \
  -H "Authorization: Bearer $SEAPORT_ADMIN_TOKEN" \
  -H "Content-Type: application/json" \
  -d @profile.json
```

The same document, without `base`, is what `seaport agents profile show -json` prints and `seaport agents profile set -f` reads. See [CLI](cli.md#profiles).

## Responses

`GET /api/v1/agents/{id}/profile` and the save answer the profile:

```json
{
  "agent_id": "…",
  "version": 3,
  "instructions": "…",
  "skills": [{ "name": "refunds", "description": "…", "body": "…" }],
  "mcp_servers": [{ "name": "billing", "url": "https://billing.example.com/mcp", "headers": [{ "name": "Authorization", "value": "Bearer ${BILLING_TOKEN}" }] }],
  "approve": "allow",
  "model": "sonnet",
  "effort": "medium",
  "created_by": "admin",
  "created_at": "2025-01-15T10:30:00Z"
}
```

The kit a connector gets from `GET /crew/profile` has `agent_id`, `version`, `instructions`, `skills`, `mcp_servers` (with their header secrets resolved), `approve`, `model` and `effort`, plus:

| Field | Meaning |
| :--- | :--- |
| `name` | The agent's name. |
| `skills` | Without `body`: the MCP tool serves it. |
| `env` | The secrets marked `env`, as `{"NAME": "value"}`, for the harness process. |
| `revision` | Changes whenever the profile or a secret does. A connector runs one revision and restarts its harness for another. |

Permissions: reading a profile, its versions and the secret names needs `seaport:agents:read`; saving a profile and setting or deleting secrets needs `seaport:agents:write`. See [Permissions](permissions.md) and [HTTP API](http-api.md#agents).
