# Feed messages from your own systems

An `api` dock is a channel with no phone behind it: your systems post messages to it over HTTP, customs and routing treat them like any chat, an agent answers, and you read the answers from the history or the live event stream. Use it for a support form, a ticket queue, a Slack bridge you write yourself, or to try Seaport without a WhatsApp number.

## Create the dock

```sh
seaport dock add api -name support
seaport customs -dock <dock-id> default allow   # optional: every chat of this dock may wake an agent
```

A new `api` dock asks first like any other (`default ask`): its chats wait under customs until you allow them. When the dock only carries your own traffic, `default allow` lets them all in.

## Post a message

`POST /api/v1/docks/{id}/ships` with an admin credential (`ships:write` in iam mode):

```sh
curl -sS -X POST "$SEAPORT_URL/api/v1/docks/$DOCK/ships" \
  -H "Authorization: Bearer $SEAPORT_ADMIN_TOKEN" \
  -H 'Content-Type: application/json' \
  -d '{
        "chat_id": "ticket-42",
        "message_id": "ticket-42-comment-7",
        "sender_id": "ana@example.com",
        "sender_name": "Ana Torres",
        "text": "@pm what is the status of the export?"
      }'
```

| Field | Required | Meaning |
| :--- | :--- | :--- |
| `chat_id` | yes | your id of the conversation; the same id is the same conversation |
| `message_id` | yes | your id of the message; posting it again answers `200` with `"duplicate": true` and stores nothing |
| `text` | yes, unless `images` | the message (up to 64 KiB) |
| `sender_id`, `sender_name` | no | who wrote it |
| `chat_kind` | no | `direct` (default), `group` or `self` |
| `chat_title` | no | shown in the inbox |
| `chat_handles`, `sender_handles` | no | identities customs rules match (default: `chat_id`, `sender_id`), such as `+34600000001` |
| `addressed` | no | the message mentions you: in a group in `mentions` mode only these wake an agent |
| `sent_at` | no | RFC 3339 time (default: now) |
| `images` | no | `[{"data": "<base64>", "name": "photo.png"}]`, up to 10; the whole body stays within 1 MiB |

The answer is `201` with the stored message: its `status` is `queued` (an agent will get it) or `held` with a `hold_reason` (customs or routing). See [Message lifecycle](../explanation/message-lifecycle.md).

Retrying with the same `message_id` is always safe, so post at least once and let duplicates be dropped.

## Read the answers

The agent's replies are outbound messages of the conversation. They are stored and marked `sent`: an `api` dock has nowhere to deliver them, so you collect them.

- **Poll** the history: `GET /api/v1/conversations/{id}/ships` (newest first) or `seaport history <conversation-id>`.
- **Stream** them: `GET /api/v1/events` is a Server-Sent Events stream. Each new message is a `ship.created` event whose data has the `conversation_id`, `direction` (`out` for agents' replies) and `text`. Keep the last event id to resume after a disconnect; see [Events](../reference/events.md).

```sh
curl -sSN "$SEAPORT_URL/api/v1/events" -H "Authorization: Bearer $SEAPORT_ADMIN_TOKEN"
```

To find a conversation by your own id, list the dock's conversations (`GET /api/v1/conversations?dock_id=…`); each has its `external_id` (your `chat_id`).

## Send as the agent

`POST /api/v1/conversations/{id}/ships` with `{"text": "…"}` sends a message into an allowed conversation as the port (the web app's composer does this). Outbound limits apply.

## Next

- [HTTP API reference](../reference/http-api.md).
- [Customs](customs.md) to filter who may wake an agent.
