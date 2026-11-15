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

An agent only sees the docks it is assigned to. A conversation of another dock answers `conversation not found`. Each dock's [agent tools](../guides/agents-and-routing.md#what-agents-may-read) (`history`, `conversations`, `attachments`) narrow what the agent may read there; without an agent every switch reads as on.

## Tools

| Tool | Always there | What it does |
| :--- | :--- | :--- |
| `reply` | yes | Sends a message, with files if needed, to an allowed conversation. |
| `history` | unless every dock of the agent has `history` off | Reads the latest messages of a conversation. |
| `attachment` | unless every dock of the agent has `attachments` off | Returns a file attached to a message (an image or a document). |
| `conversations` | unless every dock of the agent has `conversations` off | Lists the allowed conversations of the agent's docks. |
| `ask_approval` | yes | Asks people to choose between options before the agent acts. |
| `approval` | yes | Reads an approval the agent asked, optionally waiting while it is pending. |
| `skill` | only with skills | Loads the instructions of one of the agent's profile skills. |
| `destinations` | only with destinations | Lists the places where the agent may open a conversation. |
| `open` | only with destinations | Opens a conversation at a destination with a first message, optionally in the thread of another conversation. |
| `vaults` | only with vaults | Lists the vaults the agent can use here, its mode on each and an index of their entries. |
| `memory_search` | only with vaults | Searches the entries of the agent's vaults by words. |
| `memory_read` | only with vaults | Reads one entry in full, with its version and who wrote it. |
| `memory_save` | only with vaults | Creates an entry or saves a new version of one, in a vault the agent can write. |
| `memory_delete` | only with vaults | Deletes an entry of a vault the agent can write. |
| `ask_takeover` | only with the browser on | Asks the agent's people to take over the managed browser at a site: a sign-in, a captcha, a 2FA code, anything only a person may do. |

### reply

Sends a message to a Seaport conversation, with files if needed. Only allowed conversations accept replies, and only on docks that take them: a reply to a conversation of a dock with [replies off](../guides/linked-conversations.md#docks-that-only-trigger) is refused (the delivery names such conversations in `read_only`).

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

### ask_approval

Sends a question with numbered options to a conversation and records an [approval](../guides/approvals.md). The same rules as `reply` apply to the card (allowed conversation, dock takes replies, outbound limits).

Arguments:

| Name | Type | Required | Meaning |
| :--- | :--- | :--- | :--- |
| `conversation_id` | string | yes | The `conversation_id` given in the Seaport prompt. |
| `text` | string | yes | The question. At most 4000 characters. |
| `options` | array | yes | 2 to 6 choices, at most 64 characters each, unique ignoring case, none starting with a digit. |
| `wait_seconds` | number | no | Block up to this many seconds for the answer. At most 290, and always less than the server's request deadline. `0` (default) returns at once. |

Result:

```json
{ "approval_id": "…", "status": "answered", "answer": "Approve", "answered_by": "Ana", "options": ["Approve", "Reject"], "expires_at": "2026-01-02T15:04:05Z" }
```

`status` is `pending`, `answered`, `expired` or `cancelled`; `answer` is one of the options once answered. A second pending approval for the same conversation is refused (`422`).

### approval

Reads one approval the agent asked. Arguments: `approval_id` (required) and `wait_seconds` (optional, same cap as above: blocks while it is pending). Result as `ask_approval`. Another agent's approval is `not found`.

### history

Reads the latest messages of a conversation, newest first, inbound and outbound. This is how an agent reads messages that were held, or older ones. A conversation of a dock whose `history` switch is off is refused; with `thread: true` such doors are skipped (and left out of `total`).

Arguments:

| Name | Type | Required | Meaning |
| :--- | :--- | :--- | :--- |
| `conversation_id` | string | yes | The conversation. |
| `limit` | integer | no | How many messages: 1 to 100. Default 20; more than 100 is cut to 100. |
| `thread` | boolean | no | Read every conversation [linked](../guides/linked-conversations.md) with this one together; each message then names its `conversation_id`. |

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
- `withheld: true` marks a file of a dock whose `attachments` switch is off: it is named, without `attachment_id`, and can't be opened.
- `conversation_id` is present with `thread: true`: the conversation each message belongs to, the one to `reply` on.
- `total` is the number of messages in the conversation (or the thread), not the number returned.

### attachment

Returns a file attached to a message, by the id that `history` lists.

Arguments:

| Name | Type | Required | Meaning |
| :--- | :--- | :--- | :--- |
| `attachment_id` | string | yes | An `attachment_id` listed by `history`. |

Result: for an image (`image/jpeg`, `image/png`, `image/webp`, `image/gif`, up to 3.75 MiB), MCP image content. For a document (a file an agent sent), an embedded resource: its text for text files, else its bytes. An attachment of a dock the agent doesn't work on answers not found; one of a dock whose `attachments` switch is off is refused. The port keeps attachments only when a secret key is set; see [Secret key](configuration.md#secret-key).

### conversations

Lists the allowed conversations the agent can reply to: those of the docks it works on. Without an agent (`none` mode, no header) it lists the allowed conversations of every dock. Docks whose `conversations` switch is off are left out.

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

### destinations

Only added, with `open`, when a dock the agent works on has at least one [destination](../guides/destinations.md). Its description lists them as `name (channel): title`, so the model knows where it may speak up without a call. Without an agent (`none` mode, no header) it lists the destinations of every dock.

Arguments: none.

Result:

```json
{
  "destinations": [
    { "dock_id": "…", "name": "ops", "channel": "slack", "title": "the on-call channel" }
  ]
}
```

### open

Opens a conversation at a destination and sends its first message. The conversation is allowed from the start (the owner chose the place), its first ship goes out as the agent's, and people's answers there come back to the agent like any message. Opening again at the same destination continues the same conversation on channels where the place is the chat (WhatsApp, `api`); on Slack each `open` posts a new top-level message in the channel and its thread is the conversation.

Arguments:

| Name | Type | Required | Meaning |
| :--- | :--- | :--- | :--- |
| `destination` | string | yes | The destination's name, from `destinations`. |
| `dock_id` | string | no | The destination's dock; needed only when two of the agent's docks name a destination the same. |
| `text` | string | no* | The first message. |
| `files` | array | no* | Files sent with it, as in `reply`. |
| `thread_of` | string | no | A `conversation_id` whose [thread](../guides/linked-conversations.md) the new conversation joins. It must be a conversation of the agent's docks. |

\* `text` or `files` must be given.

Result:

```json
{ "conversation_id": "…", "ship_id": "…", "status": "sent" }
```

Errors: `no destination named <name> on your docks`, `several docks have a destination named <name>: give dock_id too`, the dock's replies are off, the dock isn't linked, the limits of `reply`, and a `thread_of` the agent can't see (not found).

### ask_takeover

Only added when the calling agent's current [profile](agent-profile.md) has `browser` on. Its description tells the model never to guess or ask for passwords or codes: when a site in the [managed browser](../guides/hosting/browser.md) needs a person (a login page, an expired session, a captcha, a 2FA prompt, a "verify you are human" wall, a payment), call this. With the profile's `browser_assist` set, Seaport tells a person itself (a message with a link to the agent's browser, once per host); otherwise the model tells the person in the conversation. With the profile's `browser_detect`, the connector asks a takeover itself when it sees such a page (`POST /crew/takeovers`), so the model may find one recorded already. The model may wait for the person in the call (`wait_seconds`) or go on with what doesn't need the browser: when the person presses **Listo**, a system message (`system:browser`) arrives in the conversation where the notice went.

Arguments:

| Name | Type | Required | Meaning |
| :--- | :--- | :--- | :--- |
| `host` | string | yes | The site's host name, as in the address bar (`app.example.com`), at most 253 characters. |
| `kind` | string | no | What the person must do: `login` (default), `captcha`, `2fa` or `other`. |
| `note` | string | no | What the agent was doing there, one line, at most 200 characters. Required when `kind` is `other`. |
| `conversation_id` | string | no | The conversation the agent is working (the one in the seaport prompt); it must be of a dock the agent is on. With `browser_assist: conversation`, the notice goes here; without it, nobody is told. |
| `wait_seconds` | integer | no | Block up to this many seconds (at most 290, and always short of the request's deadline) for the person to finish; `0` (default) returns at once. |

Result:

```json
{ "host": "app.example.com", "kind": "login", "recorded": true, "notified": true, "done": false, "pending_takeovers": 1 }
```

`notified` says a person was told (now, or when this host was first asked: one notice per host); `done` says the person finished while the call waited (the takeover is gone). The takeover appears on the agent's page (`takeovers` of the agent, `agent.updated`); a person opens the browser viewer, does what the site asks, and presses **Listo**: the agent gets a system message `@agent [takeover app.example.com] a person is done (login) at app.example.com. Go on in the browser.` in the conversation the notice went to (else the one the call named). An agent keeps at most 8 open takeovers, one per host (a new one for the same host replaces the older and keeps its notice). A takeover nobody attends within 1 h (`agent.TakeoverTTL`) is swept: it leaves the page and the agent gets `@agent [takeover app.example.com] nobody came within 1h0m0s (login) at app.example.com. Go on without it, or ask again.` the same way. Errors: `host must be a site's DNS name`, `kind must be one of login, captcha, 2fa, other`, `say what the person must do when kind is other`, `wait_seconds must be between 0 and 290`, a conversation the agent can't see (not found), and `no agent: a takeover belongs to an agent` when the call has no agent. A notice that can't go out (the conversation isn't allowed, the dock's replies are off, no dock of the agent has the destination) is logged and the takeover stays in the web.

### Vaults

`vaults`, `memory_search`, `memory_read`, `memory_save` and `memory_delete` are added when the calling agent has any [vault](../guides/vaults.md) grant, its own or on any dock it works (an agent with none sees none of them). They need an agent: without one (`none` mode, no header) they fail with `vaults belong to agents: connect with an agent's token`.

Which vaults a call may use is decided per call. The agent's own vaults are usable everywhere; the vaults of a dock only when the call passes a `conversation_id` of a conversation of a dock the agent is assigned to; a vault reachable both ways has the widest mode. Every tool takes this optional argument:

| Name | Type | Required | Meaning |
| :--- | :--- | :--- | :--- |
| `conversation_id` | string | no | The conversation the agent is working (the one in the prompt), to add its dock's vaults. |

Entries come with a `source`: `{"kind","by","conversation_id","description"}`, where `description` reads like `written by agent pm from a slack conversation` or `written by a person`, so the model can weigh them.

#### vaults

Arguments: `conversation_id` only. Result:

```json
{
  "vaults": [
    {
      "name": "company",
      "title": "About the company",
      "description": "Facts about customers and past decisions.",
      "mode": "write",
      "entries": 42,
      "index": [
        {
          "name": "support-hours",
          "first_line": "Support answers 09:00-18:00 CET.",
          "updated_at": "2025-01-15T10:30:00Z",
          "source": { "kind": "user", "by": "Ana", "description": "written by a person Ana" }
        }
      ]
    }
  ]
}
```

`index` lists the newest 200 entries of each vault; `entries` is the total. The rest is found with `memory_search`.

#### memory_search

| Name | Type | Required | Meaning |
| :--- | :--- | :--- | :--- |
| `query` | string | yes | The words to look for. |
| `vault` | string | no | Search only this vault (default: every vault usable here). |
| `conversation_id` | string | no | As above. |

Result: `{"hits":[{"vault","name","snippet","version","updated_at","source"}]}`, at most 20, best first. Search is full text (the `simple` configuration: no stemming, no embeddings). A `vault` that isn't usable here is `not found`.

#### memory_read

Arguments: `vault` and `name` (required), `conversation_id`. Result: `{"vault","name","body","version","mode","updated_at","source"}`. Errors: `no vault named <name> is available to you here` (a dock's vault needs `conversation_id`), `no entry named <name>`.

#### memory_save

| Name | Type | Required | Meaning |
| :--- | :--- | :--- | :--- |
| `vault` | string | yes | The vault's name. The agent needs `write` on it here. |
| `name` | string | yes | The entry's name: 1 to 64 lowercase letters, digits, `-` or `_`. |
| `body` | string | yes | The markdown text, up to 64 KiB. |
| `base` | integer | no | The version being edited. Omit it to create a new entry. |
| `conversation_id` | string | no | As above. |

Result: `{"vault","name","version","mode","updated_at","source"}` (no body). Errors: `409` conflict when the entry exists and `base` is missing, or when it changed since `base` (read it again and redo the edit); `not found` for `base` on an entry that doesn't exist; `403` `this vault is read-only for you here` when the mode is `read`; `400` when the text looks like a credential (it names the kind, never the match); `422 QUOTA_EXCEEDED` past 2000 entries. The entry records the agent, the conversation and the dock as its provenance.

#### memory_delete

Arguments: `vault` and `name` (required), `conversation_id`. Result: `{"deleted":"<name>","last_version":3}`. Needs `write` like `memory_save`. People can read the versions of an existing entry; those of a deleted one aren't served by the admin API.

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
