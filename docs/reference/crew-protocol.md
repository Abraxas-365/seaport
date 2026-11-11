# Crew protocol

The WebSocket protocol between the port and the agents that work for it: the endpoint, every frame, the limits and the reasons a connection closes.

`seaport connect` speaks this protocol for you, and so do hosted agents. Read this page if you write your own connector. Connectors dial out to the port, so an agent behind NAT needs no public address.

## Endpoint

```text
GET /crew/ws        (WebSocket upgrade)
Authorization: Bearer sp_crew_…
```

- The bearer is a crew token. Each token belongs to one agent. See [Connect an agent](../guides/connector.md) and [HTTP API](http-api.md#crew-routes).
- In `none` mode the port needs no token, and the agent comes from `hello.as`.
- The port counts open connections per organization. Over the quota the upgrade fails with `422 QUOTA_EXCEEDED` (`crew_connections`; see [Limits](limits.md)).
- A shutting-down port answers the upgrade with an error body and the connector reconnects.
- Every frame is one WebSocket text message holding a JSON object. `type` names the frame, and fields that are empty are left out.

```json
{"type":"hello","agent":"claude-code","as":"ci","concurrency":4,"steer":true,"profile_version":3}
```

## Flow

```text
client                                  port
  │── hello ───────────────────────────▶│
  │◀──────────────────────── welcome ───│
  │◀────────────────────── delivery ────│  (up to hello.concurrency in flight)
  │◀──────────────────────── steer ─────│  (only if hello.steer)
  │── ack / release ───────────────────▶│
  │◀──────────────────────── profile ───│  (the kit changed)
  │── drain ───────────────────────────▶│  (no new deliveries)
```

1. The client sends `hello` within 10 seconds of connecting. Anything else as the first frame closes the connection.
2. The port answers `welcome`.
3. The port claims deliveries for the agent and pushes them as `delivery` frames while the connection has room.
4. The client works each delivery and answers with `ack` (handled) or `release` (give back).

## Frames

| Frame | Direction | Fields |
| :--- | :--- | :--- |
| `hello` | client → port | `agent`, `as`, `concurrency`, `steer`, `profile_version` |
| `welcome` | port → client | `mcp_url` |
| `delivery` | port → client | `delivery` |
| `steer` | port → client | `delivery` |
| `profile` | port → client | none |
| `ack` | client → port | `delivery_id`, `ship_ids` |
| `release` | client → port | `delivery_id` |
| `reply` | client → port | `conversation_id`, `text` |
| `drain` | client → port | none |
| `activity` | client → port | `delivery_id`, `items` |
| `error` | port → client | `message` |

### hello

The first frame of every connection.

| Field | Type | Default | Meaning |
| :--- | :--- | :--- | :--- |
| `agent` | string | none | A free label shown in the crew status (the harness or host name). At most 200 characters. |
| `as` | string | the token's agent | The library agent the connection works as. A valid agent name: lowercase letters, digits, `-` and `_`. Required without a token's agent (`none` mode). With a crew token it may be left out, and when you send it, it must match the token's agent. |
| `concurrency` | integer | 4 | How many conversations the connection works at once. Between 1 and 32; `0` or absent means 4. |
| `steer` | boolean | `false` | The agent can take new messages in the middle of a turn. See [steer](#steer). |
| `profile_version` | integer | `0` | The [agent profile](agent-profile.md) version the harness runs. `0` means none, or a connector that doesn't apply profiles. It can't be negative. The crew status marks the connection `outdated` when it isn't the agent's current version. |

An invalid hello, an unknown agent or a token that belongs to another agent closes the connection (see [Close reasons](#close-reasons)).

### welcome

```json
{"type":"welcome","mcp_url":"https://port.example.com/mcp"}
```

`mcp_url` is the port's MCP endpoint: `SEAPORT_PUBLIC_URL` plus `/mcp`. Attach it to the agent's harness as an MCP server with the same bearer. See [MCP tools](mcp-tools.md).

### delivery

A batch of queued messages of one conversation, handed to the agent.

```json
{
  "type": "delivery",
  "delivery": {
    "id": "…",
    "conversation": { "id": "…", "dock_id": "…", "channel": "whatsapp", "kind": "direct", "title": "Ana", "…": "…" },
    "ships": [
      { "id": "…", "direction": "in", "sender_name": "Ana", "text": "Hi", "status": "claimed", "…": "…" }
    ]
  }
}
```

- `delivery.id` is the claim token. Send it back in `ack` and `release`.
- `delivery.conversation` is the [conversation](http-api.md#conversations) object.
- `delivery.ships` are the ships (messages) of the batch, with their `attachments` metadata. Fetch an image from `GET /crew/attachments/{id}` or with the MCP `attachment` tool.
- A conversation has at most one delivery in flight, so the agent never gets two prompts for one conversation at once.
- A delivery is a lease. The port renews it while the connection lives, and puts the ships back in the queue when the connection ends or the lease runs out.

### steer

A connection that sent `steer: true` gets new messages of a conversation that is already in flight as a `steer` frame instead of waiting for the next delivery.

```json
{"type":"steer","delivery":{"id":"…","conversation":{"…":"…"},"ships":[{"…":"…"}]}}
```

- `delivery.id` is the id of the open delivery, and `ships` holds only the new ships.
- A steering connection must name the handled ships in its `ack` (`ship_ids`). A joined ship the agent couldn't take, because its turn had already ended, goes back to the queue.
- A connection without `steer` never gets this frame.

### ack

Tells the port the agent handled the delivery.

```json
{"type":"ack","delivery_id":"…","ship_ids":["…","…"]}
```

- `delivery_id` is required, and the delivery must belong to this connection. Otherwise the port answers an `error` frame (`delivery not found on this connection`).
- `ship_ids` are the ships the agent handled. A connection without `steer` may leave it out, and every ship of the delivery counts as handled. A steering connection must send it (`steering connectors must name the handled ship_ids`).
- Every ship still claimed by the delivery and not named goes back to the queue.
- An ack frees a place in the connection's concurrency.

### release

```json
{"type":"release","delivery_id":"…"}
```

Gives the delivery back unhandled: its ships return to the queue. Same rules for `delivery_id` as in `ack`.

### reply

```json
{"type":"reply","conversation_id":"…","text":"On my way."}
```

For connectors that can't use [MCP](mcp-tools.md): sends the agent's answer to a conversation as the connection's agent. `conversation_id` is required and `text` is required, at most 65536 characters. The conversation must be allowed (customs); otherwise the port refuses. Failures come back as an `error` frame.

### drain

```json
{"type":"drain"}
```

Tells the port to give this connection no new deliveries. Steers still join the deliveries in flight. The connector sends it before it restarts its harness with a new kit: once its work is acked, it closes and reconnects. Drain lasts until the connection closes.

### activity

```json
{"type":"activity","delivery_id":"…","items":[
  {"kind":"thought","text":"Let me read the diff first."},
  {"kind":"tool","tool":"call-1","title":"Bash: git diff","tool_kind":"execute","status":"in_progress"}
]}
```

Optional. What the agent did on a delivery since the previous `activity` frame of that delivery, so people watching the conversation in the web app see it work. The port checks the frame and sends it on as a live [`crew.activity` event](events.md#agent-activity). The thinking and text are never stored: a page opened mid-turn sees only what comes after. The tool calls are also gathered into a [turn summary](events.md#turn), stored when the delivery ends (`ack`, `release` or the connection closing).

- `delivery_id` must be a delivery open on this connection. Activity for a delivery that just ended (acked or released) is dropped without an error.
- `items` holds 1 to 32 items, at most 6000 bytes once encoded as JSON (`crew.MaxActivityItems`, `crew.MaxActivityBytes`). Texts are UTF-8 without control characters (newlines and tabs are fine).

| Item field | Meaning |
| :--- | :--- |
| `kind` | `thought` (a chunk of the agent's reasoning), `message` (a chunk of its own text) or `tool` (a tool call started or changed). |
| `text` | The chunk, for `thought` and `message`. Chunks of one kind follow on from the previous ones. |
| `tool` | For `tool`: the call's id within the turn, at most 128 bytes. Later items with the same id update it. |
| `title` | For `tool`: what the call does, as the harness names it (`Read src/main.go`). |
| `tool_kind` | For `tool`: the ACP tool kind (`read`, `edit`, `delete`, `move`, `search`, `execute`, `think`, `fetch`, `other`). |
| `status` | For `tool`: `pending`, `in_progress`, `completed` or `failed`; empty when unchanged. |

Send titles and status only, never a tool's input or output: those can hold file contents or secrets. `seaport connect` sends the ACP `agent_thought_chunk`, `agent_message_chunk`, `tool_call` and `tool_call_update` updates of each turn this way, batched every 300 ms, and sends the last batch before its `ack` or `release`.

### profile

```json
{"type":"profile"}
```

The agent's kit changed (a profile saved, or a secret changed). The frame has no fields: fetch the kit again from `GET /crew/profile` and, if its `revision` changed, send `drain` and reconnect with the new kit. The port sends it to every connection of that agent, in every process of the port. If the connection's send queue is full the frame is dropped; a connector also reads its kit again on each connection.

### error

```json
{"type":"error","message":"delivery not found on this connection"}
```

The port couldn't apply a frame. The connection stays open. `message` is the error text for 4xx-class errors, and a generic message for internal ones. A frame that isn't valid JSON answers `frame is not valid JSON`, and a frame type the port doesn't accept from clients answers `unexpected frame type <type>`.

## Keep-alive and limits

| Item | Value | Constant |
| :--- | :--- | :--- |
| Largest frame | 4 MiB | `crew.MaxFrame` |
| Ping interval | 30 seconds | `crew.PingInterval` |
| Time to send `hello` | 10 seconds | in the hub |
| Default concurrency | 4 | `crew.DefaultConcurrency` |
| Largest concurrency | 32 | `crew.MaxConcurrency` |
| Time the port waits for one operation | 15 seconds | in the hub |
| Claim lease | 10 minutes, renewed while the connection lives | `config.ClaimLease` |

The port pings every 30 seconds with WebSocket ping frames to find dead links. Answer pings (most WebSocket libraries do). A frame over 4 MiB closes the connection. The full list is in [Limits](limits.md).

## Close reasons

The port closes the connection with a WebSocket close frame and a reason text. A connector should stop retrying on a policy violation: the hello or the token won't become valid by waiting.

| Close code | Reason | Cause |
| :--- | :--- | :--- |
| 1008 (policy violation) | `expected hello`, `first frame must be hello` | The first frame didn't arrive in 10 seconds, or wasn't a valid `hello`. |
| 1008 | `concurrency must be between 1 and 32`, `profile_version must not be negative`, `as must be an agent name (…)`, … | `hello` failed validation. |
| 1008 | `name the agent to work as: seaport connect -agent <name>` | No agent: no token's agent and no `hello.as`. |
| 1008 | `this crew token belongs to agent X, not Y` | `hello.as` doesn't match the token's agent. |
| 1008 | The not-found message of the agent | `hello.as` names an agent that doesn't exist, or was deleted during the handshake. |
| 1008 | `the crew token is no longer valid` | The token was revoked or expired, or the port was closing, during the handshake. |
| 1011 (internal error) | `try again` | The port couldn't record the connection. Reconnect. |
| 1000 (normal) | none | The connection ended: the client closed, the token was revoked or expired, the agent was deleted, or the port shut down. The port releases the deliveries the connection held. |

Revoking a crew token, or deleting its agent, closes the live connections of that token in every process of the port. A token that has an expiry also closes its connection when it expires.

For the whole message cycle, read [Message lifecycle](../explanation/message-lifecycle.md). To run a connector, read [Connect an agent](../guides/connector.md).
