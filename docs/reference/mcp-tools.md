# MCP tools

The MCP server the port offers to agents at /mcp: every tool with its arguments and results, how access is scoped to an agent, and how a connector attaches it.

The agent's harness uses these tools to answer people and to read context. They are the way an agent talks back through a dock.

## Endpoint

```text
POST /mcp
Authorization: Bearer sp_crew_…
X-Seaport-Agent: ci          (optional with a token; required in none mode)
```

- Transport: MCP streamable HTTP, stateless, with JSON responses. Every request gets a server of its own, so a changed profile shows on the next request.
- Server name `seaport`. Its `instructions` tell the model that each prompt from Seaport names a `conversation_id`, to answer people with `reply`, and to read earlier messages with `history`.
- Auth: a crew token, like the [crew WebSocket](crew-protocol.md). See [HTTP API](http-api.md#crew-routes). The route counts against the [rate limit](limits.md) of the token.
- The calls act for the organization of the token. A conversation of another organization doesn't exist for them.

### Which agent a call acts as

| Credential | Agent |
| :--- | :--- |
| A crew token | The token's agent. `X-Seaport-Agent`, when sent, must name that agent; otherwise the request is refused with `403`. |
| `none` mode, no token | The agent named by `X-Seaport-Agent` (required for the tools to be scoped). |
| `none` mode, no header | No agent: the tools see every dock of the port, and `skill` doesn't exist. |

The header is `crew.AgentHeader`. An agent that doesn't exist answers `404`.

An agent only sees the docks it is assigned to. A conversation of another dock answers `conversation not found`.

## Tools

| Tool | Always there | What it does |
| :--- | :--- | :--- |
| `reply` | yes | Sends a message, with files if needed, to an allowed conversation. |
| `history` | yes | Reads the latest messages of a conversation. |
| `attachment` | yes | Returns a file attached to a message (an image or a document). |
| `conversations` | yes | Lists the allowed conversations of the agent's docks. |
| `skill` | only with skills | Loads the instructions of one of the agent's profile skills. |

### reply

Sends a message to a Seaport conversation, with files if needed. Only allowed conversations accept replies.

Arguments:

| Name | Type | Required | Meaning |
| :--- | :--- | :--- | :--- |
| `conversation_id` | string | yes | The `conversation_id` given in the Seaport prompt. |
| `text` | string | unless `files` | The message to send. At most 65536 characters. |
| `files` | array | no | Files sent with it: `{ "name": "qa-report.md", "content_base64": "…" }` each. |

Files: at most 10, 10 MiB each and 25 MiB together (the request body is capped at about 35 MiB). Names use letters, digits, spaces and `. _ ( ) + -`, without paths, unique in the reply. The type comes from the bytes. Each channel delivers files its own way; see [Files agents send](../guides/messages-and-images.md#files-agents-send). For files on disk, `seaport reply <conversation_id> -file PATH… "text"` sends them without the model writing base64 ([CLI](cli.md)).

Result:

```json
{ "ship_id": "…", "status": "sent" }
```

`status` is the outcome of the send: `sent`, or `failed` when the channel refused it (the call then returns an error too).

Errors:

- The conversation isn't allowed: `conversation is not allowed; the port owner must allow it first`.
- The conversation belongs to a dock the agent doesn't work on: `conversation not found`.
- The dock, conversation or organization sent too much lately: a rate-limit error. See [Limits](limits.md).
- The channel failed to send: the message is stored as `failed` and the tool returns an error.
- Files on a port without blob storage: `this port keeps no files`. Invalid files (a path as name, empty, too large, two with one name, bad base64) are refused and nothing is sent.

### history

Reads the latest messages of a conversation, newest first, inbound and outbound. This is how an agent reads messages that were held, or older ones.

Arguments:

| Name | Type | Required | Meaning |
| :--- | :--- | :--- | :--- |
| `conversation_id` | string | yes | The conversation. |
| `limit` | integer | no | How many messages: 1 to 100. Default 20; more than 100 is cut to 100. |

Result:

```json
{
  "messages": [
    {
      "direction": "in",
      "from": "Ana",
      "text": "Can you check the build?",
      "sent_at": "2025-01-15T10:30:00Z",
      "status": "handled",
      "attachments": [
        { "attachment_id": "…", "kind": "image", "mime": "image/png", "size": 48213, "name": "screenshot.png" }
      ]
    }
  ],
  "total": 41
}
```

- `direction` is `in` or `out`.
- `from` is the sender's name, else the sender id; for outbound messages it is `agent`.
- `status` is the ship status (inbound `held`, `queued`, `claimed`, `handled`; outbound `pending`, `sent`, `failed`).
- `attachments` is left out when the message has none. `kind` is `image` or `document` (a file an agent sent). `name` is left out when unknown.
- `total` is the number of messages in the conversation, not the number returned.

### attachment

Returns a file attached to a message, by the id that `history` lists.

Arguments:

| Name | Type | Required | Meaning |
| :--- | :--- | :--- | :--- |
| `attachment_id` | string | yes | An `attachment_id` listed by `history`. |

Result: for an image (`image/jpeg`, `image/png`, `image/webp`, `image/gif`, up to 3.75 MiB), MCP image content. For a document (a file an agent sent), an embedded resource: its text for text files, else its bytes. An attachment of a dock the agent doesn't work on answers not found. The port keeps attachments only when a secret key is set; see [Secret key](configuration.md#secret-key).

### conversations

Lists the allowed conversations the agent can reply to: those of the docks it works on. Without an agent (`none` mode, no header) it lists the allowed conversations of every dock.

Arguments: none.

Result:

```json
{
  "conversations": [
    { "conversation_id": "…", "channel": "whatsapp", "kind": "direct", "title": "Ana" }
  ]
}
```

It returns at most 100 conversations. `kind` is `direct` or `group`.

### skill

Only added when the calling agent's current [profile](agent-profile.md) has at least one skill. Its description lists every skill as `name: description`, and tells the model to call the tool with a skill's name before a task that matches it. Skills reach every harness this way, without files on the agent's machine.

Arguments:

| Name | Type | Required | Meaning |
| :--- | :--- | :--- | :--- |
| `name` | string | yes | The skill's name, from the list in the tool's description. |

Result:

```json
{ "name": "release-notes", "instructions": "…the skill body…" }
```

Errors: `no skill named <name>`, and `no agent: skills belong to an agent's profile` when the call has no agent.

## Errors

A tool failure comes back as an MCP result with `isError: true` and one text message. The message is the error text for errors the caller can fix (4xx class), and `an unexpected error occurred` for internal ones. Failures before the tool runs (no credential, the wrong `X-Seaport-Agent`) are HTTP errors with the [standard error body](http-api.md#errors).

Malformed ids (`conversation_id`, `attachment_id`) fail with a validation message.

## How connectors attach it

You don't configure this by hand with `seaport connect` or a hosted agent.

1. The port's `welcome` frame carries `mcp_url`: `SEAPORT_PUBLIC_URL` plus `/mcp`.
2. When the connector opens a session in the agent's harness (over ACP), it passes an MCP server named `seaport` with that URL, the header `Authorization: Bearer <crew token>` and, when the agent was named, `X-Seaport-Agent: <agent>`.
3. It does so only when the harness says it accepts HTTP MCP servers (`mcpCapabilities.http`). Without that, the harness gets no MCP servers.
4. After Seaport's, the connector adds the [profile](agent-profile.md)'s `mcp_servers`, with their headers resolved from the agent's secrets.

A hosted slot's connector relays the port's MCP over loopback, because harnesses take plain-HTTP MCP servers only there.

To use the tools from another MCP client, point it at `https://port.example.com/mcp` with a crew token as bearer. See [Connect an agent](../guides/connector.md).
