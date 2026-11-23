# Show a trigger dock's threads where people talk

A mirror makes Seaport post each new thread of a dock that only triggers at a place where people and the agent can talk, and links the two conversations.

Some docks wake an agent but never take its answer: a [webhook](webhooks.md), a [Datadog](datadog.md) endpoint, or any dock whose [replies are off](linked-conversations.md#docks-that-only-trigger). An alert there reaches the agent, but nobody reads it and the agent has nowhere to answer. The dock's **mirror** fixes that: it names a [destination](destinations.md) of another dock (a Slack channel, a WhatsApp group, an `api` chat), and Seaport opens it for you.

## Which docks can have one

Only a dock that doesn't reply: a channel that never takes replies (`webhook`, `datadog`) or any dock with its **replies** switched off. A dock that replies answers in place and needs no mirror, so setting one is refused (`422`). The reverse holds too: while a dock has a mirror, turning its replies on is refused until you remove the mirror.

The target is a destination of a *different* dock of the same organization, and that dock must take replies. Removing the destination (or deleting its dock) removes the mirror.

## Set it

Name the destination first, then point the dock at it:

```sh
seaport dock destination set <slack-dock-id> oncall C0123456789 -title "the on-call channel"
seaport dock mirror <webhook-dock-id> <slack-dock-id>:oncall
seaport dock mirror <webhook-dock-id>          # shows it
seaport dock mirror <webhook-dock-id> off      # clears it
```

In the web app it is the **Espejo** card on the dock's page (shown on every dock that can't reply). Over the API: `PUT /api/v1/docks/{id}/mirror` with `{"mirror":{"dock_id":"…","destination":"oncall"}}` or `{"mirror":null}` (`docks:write`); the dock's `mirror` field shows it. `seaport dock datadog -mirror` and `PUT /docks/{id}/datadog` set the same mirror.

## When it opens

When a message arrives on the dock and its conversation, after [customs](customs.md), is allowed and is a thread of one (the first message of that chat, or the first after someone unlinked the thread), Seaport opens the destination **before** storing the message:

- The first message there is the inbound text, posted as Seaport itself: no agent speaks, the owner named the place. If the message carried files, a line says how many and that they are in Seaport.
- The new conversation joins the thread of the inbound one, so the agent wakes to a thread with both doors. Its answer goes to the mirror, people answer there, and the same agent continues. The trigger door stays read-only.
- Later messages of that chat arrive on the trigger door in the same thread. Seaport posts nothing more to the mirror: the agent does, saying what changed where people are reading.

A Slack destination makes a thread per opening, so each chat of the trigger dock gets its own. A WhatsApp group or an `api` chat is one conversation that every mirrored chat shares.

It does **not** open when:

- customs holds the message (a chat that is pending or denied, a denied sender). Allowing the chat later does not open a mirror late: a mirror shows what the owner let in as it arrives.
- the message is the dock's own, or one Seaport wrote itself (a [schedule](schedules.md), an [approval](approvals.md)'s answer, a browser takeover).
- the thread already has another door.
- the message was already stored (a sender's retry of one whose mirror failed).

Two messages of a new chat arriving at once open one mirror: the check and the opening run under a lock per conversation, shared by every process.

## Failures

A mirror that can't open (the destination's dock is unlinked, its send [limits](../reference/limits.md#outbound-rate-limits) refuse, Slack answers an error) is logged with the dock, conversation and destination, and the message is stored anyway: the agent still wakes, on the trigger door. The opening is bounded by 30 s ([limits](../reference/limits.md#mirrors)).

The conversation is still a thread of one, so the next new message of that chat tries again and, if it works, opens the mirror with its own text (the earlier message stays on the trigger door; the agent reads it with `history`).

## See also

- [Destinations](destinations.md)
- [Docks that only trigger](linked-conversations.md#docks-that-only-trigger)
- [CloudWatch alarms](cloudwatch.md): a webhook dock with a mirror to Slack
