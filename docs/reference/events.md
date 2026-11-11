# Events

The live event stream of the port: how to connect to it, how to resume after a drop, and every event type with the permission that reads it.

The web app keeps itself up to date from this stream. You can read it from any HTTP client that speaks Server-Sent Events (SSE).

## Connect

```bash
curl -N -H "Authorization: Bearer $SEAPORT_ADMIN_TOKEN" \
  http://127.0.0.1:8740/api/v1/events
```

`GET /api/v1/events` is an admin route: it takes the same credentials as the rest of the [HTTP API](http-api.md) (bearer token, session cookie, or IAMKit credentials). Browsers can't set headers on `EventSource`, so they use the session cookie. It needs no permission of its own; what you receive depends on [the permission of each event type](#event-types).

A stream only carries the events of the caller's organization.

Every stream starts with these lines:

```text
retry: 3000
event: ready
data: {"boot":"9f2c41aa"}
```

- `retry: 3000` tells the client to reconnect after 3 seconds.
- `ready` carries `boot`, an identifier of the server process run you are connected to. It changes when that process restarts.

Each event after that looks like this:

```text
id: 1042
event: ship.created
data: {"id":"…","conversation_id":"…", …}
```

- `id` is the position of the event in the event log, as a number in text. The log is shared by every process of the port, so ids are global and increase; an organization sees gaps where other organizations' events were.
- `event` is the [event type](#event-types).
- `data` is the JSON payload.

While the stream is idle the server sends a comment line (`: ping`) every 25 seconds so proxies keep the connection open.

## Resume and reset

When a client reconnects it sends the last id it saw in the `Last-Event-ID` header (`EventSource` does this by itself). If you can't set the header, pass it as the `last_event_id` query parameter. The server replays what you missed, then continues live. Because ids come from the shared log, you can resume on any replica.

The server answers with a `reset` event instead of a replay when it can't give you what you missed:

```text
event: reset
data: {}
```

On `reset`, refetch your state from the API (lists, details) and continue from the stream. A `reset` is sent when:

- The id isn't a number, was never issued, or is older than the retention window (1 hour, `EventRetention`; older events are swept).
- You are more than 1000 events behind (`maxReplay`).
- Your connection was too slow and the server dropped it (more than 256 events queued): the next reconnect replays or resets.

`reset` arrives right after `ready` and has no `id`. [`crew.activity`](#agent-activity) events have no `id` either: they are live only, so they don't move the resume point.

## Lifetime of a stream

A stream is closed by the server, and the client is expected to reconnect and resume:

- After 10 minutes at most, so revoked keys and changed permissions apply on the next connection. Nothing is lost: the reconnect resumes from `Last-Event-ID`.
- When the credential expires (an IAMKit session or API key with an expiry).
- When the port shuts down.
- When the client falls too far behind.

## Permission filter

Each event type is sent only to callers that hold the permission in the table below. A caller without it never receives that type, with no error. A type that isn't in the table is never streamed. In `none` and `token` modes the caller holds every permission. See [Permissions](permissions.md).

> [!NOTE]
> Payloads are stored in the event log in clear for the retention window, so they never carry secrets. `dock.updated` omits the pairing QR code: fetch `GET /api/v1/docks/{id}` for it.

## Event types

The `data` of every event is JSON. The payload types are the same objects the matching API routes return.

| Event type | Reading permission | Payload | When |
| :--- | :--- | :--- | :--- |
| `dock.updated` | `seaport:docks:read` | Dock, without `qr_code` | A dock was created or changed: status, name, address, a new pairing QR. |
| `dock.deleted` | `seaport:docks:read` | `{"id"}` | A dock and everything under it is gone. |
| `conversation.updated` | `seaport:conversations:read` | Conversation | A conversation was created or changed: status, access, title, last activity. |
| `customs.updated` | `seaport:customs:read` | Customs (`{policy, rules}` of a dock) | A dock's customs policy or rules changed. |
| `ship.created` | `seaport:ships:read` | Ship | A ship was stored: inbound (queued or held) or an outbound reply (pending). |
| `ship.updated` | `seaport:ships:read` | Ship | An outbound ship's outcome (`sent` or `failed`). |
| `delivery.changed` | `seaport:ships:read` | Delivery event | A delivery opened, grew (steer), closed or was released. |
| `ship.turn` | `seaport:ships:read` | [Turn](#turn) | A delivery with tool calls ended and its summary was stored. |
| `crew.changed` | `seaport:crew:read` | Crew status (`{"agents":[…]}`) | An agent connected or disconnected, or its deliveries changed. Carries the whole crew. |
| `crew.activity` | `seaport:ships:read` | [Agent activity](#agent-activity) | An agent thought, wrote or called a tool on a delivery. Live only: no `id`, never stored or replayed. |
| `agent.updated` | `seaport:agents:read` | Agent, with its docks | An agent was created, changed, assigned to a dock or taken off one. |
| `agent.deleted` | `seaport:agents:read` | `{"id"}` | An agent left the library. |
| `environment.updated` | `seaport:environments:read` | Environment | An environment was created or got a new version. |
| `environment.deleted` | `seaport:environments:read` | `{"id"}` | An environment was deleted. |
| `slot.updated` | `seaport:environments:read` | Hosted agent (`agent`, `slots`) | An agent was hosted, moved or unhosted, or one of its slots changed state. When the agent is no longer hosted the payload has no environment and no slots. |
| `environment.build` | `seaport:environments:read` | Build, without its log | An environment version's build started, finished or failed. |

Two more event names are part of the stream protocol itself and aren't in the permission table: `ready` and `reset`.

### Delivery event

The payload of `delivery.changed`:

| Field | Type | Meaning |
| :--- | :--- | :--- |
| `state` | string | `claimed`, `joined`, `acked`, `released` or `expired`. |
| `delivery_id` | string | The delivery. Absent for `expired`. |
| `conversation_id` | string | The conversation. Absent for `expired`. |
| `ship_ids` | string list | The claimed or joined ships, or the handled ones of an ack. |
| `count` | number | Ships that went back to the queue (`acked`, `released`, `expired`). |
| `owner` | string | Names the claimer (for logs and debugging). |

Fields that are empty are left out.

### Agent activity

The payload of `crew.activity`: what an agent did on a delivery since its previous `crew.activity`, as its connector reported it with an [`activity` frame](crew-protocol.md#activity).

| Field | Type | Meaning |
| :--- | :--- | :--- |
| `delivery_id` | string | The delivery being worked. |
| `conversation_id` | string | Its conversation. |
| `agent_id`, `agent_name` | string | The agent working it. |
| `at` | string | When the port received it (RFC 3339). |
| `items` | list | The steps, in order: `{kind, text}` for `thought` and `message` chunks (append them to the previous chunk of the same kind), `{kind: "tool", tool, title, tool_kind, status}` for a tool call (update the call with the same `tool`). Tools carry no input or output. |

Activity events are live only. They have no `id` line, are not written to the event log, and a client that reconnects gets none of the ones it missed. Clear what you show when the delivery ends (`delivery.changed` with `acked`, `released` or `expired`) and on `reset`. Reading them takes `seaport:ships:read`, the permission that reads the conversation's messages.

### Turn

The payload of `ship.turn`, and an item of `GET /api/v1/conversations/{id}/turns`: what an agent did on one delivery, stored when the delivery ended. The port builds it from the delivery's [`activity` frames](crew-protocol.md#activity), keeping the tool calls only (no thinking, no text).

| Field | Type | Meaning |
| :--- | :--- | :--- |
| `delivery_id` | string | The delivery. |
| `conversation_id` | string | Its conversation. |
| `agent_id`, `agent_name` | string | The agent that worked it. |
| `outcome` | string | `acked` (finished), `released` (the agent gave up; its ships went back to the queue) or `dropped` (the connection closed mid-turn). |
| `started_at`, `ended_at` | string | When the delivery was handed out and when it ended (RFC 3339). |
| `tools` | list | `{title, kind, status, started_at, ended_at}` for each tool call, in order. Times are when the port saw the call start and finish; `ended_at` is absent for a call that never finished. Secret-looking strings in titles are replaced by `[redacted]`. |
| `omitted` | number | Tool calls past the first 200, not kept. Absent when 0. |

A delivery without tool calls stores no turn. A lease that expires (the sweeper requeues the ships) stores none either.

### Notes on payloads

- `agent.updated` is not sent for the agent that stops being a dock's default when another one becomes it. Refetch the agent list when you see an agent become a default.
- `crew.changed` is the whole list of connected agents (each with `session`, `agent_id`, `agent_name`, `name`, `concurrency`, `steer`, `profile_version`, `outdated`, `remote`, `connected_at` and `deliveries`), not a diff.
- `environment.build` never carries the build log. Fetch it from the build route (see the [HTTP API](http-api.md)).

For how events fit in the system, read [Architecture](../architecture.md).
