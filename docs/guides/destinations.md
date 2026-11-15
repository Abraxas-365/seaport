# Destinations

Name the places where an agent may start a conversation on its own: a Slack channel, a WhatsApp number or group, a chat your system reads. The agent opens the door there with its first message and people answer in the thread it made.

Agents answer where they are written to. Sometimes they should speak first: an alert came in on a webhook and the on-call channel should hear about it; a review on GitHub needs a decision from someone who lives in Slack; a daily summary goes to the team's WhatsApp group. A **destination** is a place the owner names on a dock, and the only kind of place an agent can open a conversation at. Without destinations an agent can't post anywhere it wasn't addressed.

## Add a destination

A destination belongs to a dock and has a **name** (lowercase letters, digits, `-` and `_`; it is how the agent names it), a **target** (the place, in the channel's own id) and an optional **title** that tells agents what the place is for.

| Dock channel | Target |
| :--- | :--- |
| `slack` | the channel id (`C0123ABCDEF`, in the channel's details in Slack). The app must be in the channel. |
| `whatsapp` | a phone `+34600000000`, or a group's JID (`…@g.us`; `seaport conversations` shows it for groups the number is in). |
| `api` | the `chat_id` your system will read the conversation under. |
| `github`, `jira`, `email` | not yet: these docks have no destinations. |
| `webhook` | never: a webhook dock only triggers. |

In the web app, on the dock's page under **Destinos**, press **Añadir**. From the CLI:

```sh
seaport dock destination set <dock-id> ops C0123ABCDEF -title "the on-call channel"
seaport dock destinations <dock-id>
seaport dock destination rm <dock-id> ops
```

Over the [HTTP API](../reference/http-api.md#docks): `PUT /api/v1/docks/{id}/destinations/{name}` with `{"target":"…","title":"…"}` (`docks:write`; it also updates an existing name), `GET …/destinations` and `DELETE …/destinations/{name}`. A dock holds at most 64 destinations, and a dock whose [replies are off](linked-conversations.md#docks-that-only-trigger) takes none: nothing may be posted there.

## How an agent opens a conversation

An agent sees the destinations of the docks it works on. Its MCP server adds two tools only when there is at least one ([MCP tools](../reference/mcp-tools.md#destinations)): `destinations` lists them, and `open` starts a conversation at one:

```json
{ "destination": "ops", "text": "PR 7 is failing on main since 10:40; details in the thread.", "thread_of": "<conversation_id>" }
```

The port creates the conversation on the destination's dock, **allowed** from the start (the owner chose the place, so customs doesn't ask), sends the text (and files, as in `reply`) as the agent's first message, and answers the new `conversation_id`. From then on it is a conversation like any other: what people write there wakes the agent, appears in the inbox and in the history, and the agent replies there with `reply`.

`thread_of` names a conversation whose [thread](linked-conversations.md) the new one joins: the GitHub review and the Slack thread about it become one matter, with one session and one history. It must be a conversation of the agent's docks.

What the place becomes depends on the channel:

- **Slack:** each `open` posts a new top-level message in the channel; the thread under it is the conversation, and answers in it reach the agent without a mention. The conversation's title is the channel's name.
- **WhatsApp:** the first `open` creates the conversation with that number or group; later ones continue it. Groups follow the dock's [group mode](customs.md#groups) for what wakes the agent afterwards.
- **`api`:** the conversation is stored under the target as its chat id; your system reads it like any reply ([API docks](api-docks.md#read-the-answers)).

Owners open too: **Abrir** next to a destination in the web app, `seaport dock open <dock-id> <name> "text"` (`-thread-of <conversation-id>` to link it), or `POST /api/v1/docks/{id}/destinations/{name}/ships` with `{"text":"…","thread_of":"…"}` (`ships:write`).

## Limits and refusals

- The agent can open only at destinations of its own docks; another dock's name is `not found`.
- Two of an agent's docks may share a destination name; then `open` needs `dock_id` too.
- The dock must be linked and take replies. Opening counts against the dock's [send limits](../reference/limits.md#outbound-rate-limits) like a reply.
- Removing a destination keeps the conversations opened there; it only closes the way to open new ones.

## See also

- [Linked conversations](linked-conversations.md)
- [MCP tools: destinations and open](../reference/mcp-tools.md#destinations)
- [CLI: docks](../reference/cli.md#docks)
