# Message lifecycle

This page follows one message from the moment it reaches a dock to the moment an agent has handled it, and then follows the reply back out. Read it when a message doesn't do what you expect: every stop along the way is a state you can see in the web app.

## The journey of an inbound message

```text
arrives ─▶ customs ─┬─▶ held (customs reason)
                    └─▶ routing ─┬─▶ held (routing reason)
                                 └─▶ queued ─▶ claimed ─▶ handled
```

### 1. Arrival

A channel hands the port a message: a WhatsApp event, or a `POST` to an `api` dock. It carries the chat, the sender, the text, any images and a channel message id. Images are downloaded, checked and stored first (see [Messages and images](../guides/messages-and-images.md)).

**Duplicates** are dropped by that message id: the same message twice (a redelivery, WhatsApp's echo of your own send) stores one ship. The `api` endpoint answers a repeat with `200` and `"duplicate": true` instead of `201`.

### 2. Customs

Customs makes two decisions, under a lock per dock so concurrent arrivals and rule changes never disagree:

- **Decide**: the chat's status, `allowed`, `pending` or `denied`, from your override, your own chat, deny rules, allow rules and the dock's default, in that order.
- **Admit**: whether *this message* gets in. Even in an allowed chat a message can be held because its sender is denied by a rule, or because it is a group message that doesn't mention you (in `mentions` mode).

If either says no, the ship is stored as **held** with the reason, and the journey stops. See [Customs](../guides/customs.md).

### 3. Routing

For an admitted ship, the dock's team picks the agent, once, right now: a mention, the conversation's previous agent, the only member, or the default (see [Routing](../guides/agents-and-routing.md#routing)). If nobody qualifies, the ship is **held** with a routing reason.

Otherwise it is stored as **queued**, naming its agent, and that agent's connections are woken.

### 4. Claimed

A connection **claims** queued ships of its agent. A claim becomes a **delivery**: the queued ships of one conversation, in arrival order. Three rules apply:

- **One delivery per conversation at a time.** While a delivery is open, nobody claims more of that conversation, so a harness session never gets two prompts at once. A conversation is also worked by one agent at a time, in arrival order: it never skips an older ship queued for another agent, so it waits if that agent is offline.
- **Steering joins.** If the connection can steer, ships that arrive for the conversation during an open delivery join it (same delivery id) and go to the connection as `steer` messages, which the connector hands to the running turn. With `queue`, or a harness that can't steer, they wait for the next delivery.
- **A claim is a lease.** It lasts 10 minutes and the port renews it while the connection is alive. If the connection closes, the port releases everything it held right away. If a dead process leaves a claim behind, a sweeper (every 30 seconds) releases it once the lease runs out. Released ships go back to **queued** and are claimed again.

### 5. Handled

The connector sends the prompt to the harness. When the turn ends, it **acks** the delivery naming the ships the agent saw, and those become **handled**. Ships of the delivery that the ack doesn't name, for example a steer that arrived just as the turn ended, are requeued and form the next delivery.

If the turn fails, the connector **releases** the delivery instead, after a pause that grows with each failure in a row, and the ships are retried.

## Held reasons

A held ship keeps its reason, which the web app shows when you hover the message. A held message is never lost, and the agent can read it with the `history` tool.

| Reason | Set by | Meaning |
| :--- | :--- | :--- |
| `conversation pending` | customs | the chat waits for your decision (default `ask`) |
| `conversation denied` | customs | the chat is denied |
| `sender denied by a rule` | customs | a deny rule matches the sender, in a group |
| `group message without a mention` | customs | a group in `mentions` mode, not addressed to you |
| `no agent is assigned to this dock` | routing | the dock has no team |
| `no agent was called (mention one with @name)` | routing | several agents, no mention, no previous agent, no default |
| `its agent left the dock` | routing | the agent was unassigned or deleted while the ship waited for it |

### Release on allow

When a **direct** chat that was pending becomes allowed (you allowed it, a rule matches, or the policy changed), the ships held as `conversation pending` are routed and queued, oldest first. Only messages from the **last 24 hours** go, and at most the **newest 50**. Everything else stays held: older messages, a denied chat's messages, a group's held messages (whether a group message addressed you isn't stored), and ships held by routing, which is decided once.

## Outbound: replies

An agent replies with the `reply` tool, you reply from the web app's composer or `seaport send`. The port:

1. checks the conversation is **allowed** (and, for an agent, on one of its docks), else refuses;
2. checks the [send limits](../guides/messages-and-images.md#sending-limits), else refuses with `429`;
3. stores the ship as **pending**, with the channel message id it generated;
4. sends it through the dock's channel;
5. marks it **sent**, or **failed** with the error.

Storing before sending is what lets the port recognize the channel's echo of your own message as a duplicate.

## What the agent sees

For each delivery, the connector builds a prompt like this:

```text
[seaport] New whatsapp message(s)
conversation_id: <id>
conversation: Ana (direct)

[2025-06-01 10:32] Ana: Hi, do you have stock?
    [image photo.jpg attached below]

Reply with the seaport `reply` tool (conversation_id above) when a reply is needed; use `history` for earlier context. Your final text is not sent to anyone.
```

Images follow the text as image blocks when the harness takes them. Steered messages come as "More message(s) in conversation …, sent while you were working". The agent replies with `reply`, reads earlier messages with `history` (including held ones), lists chats with `conversations`, and looks at files with `attachment`. With `-auto-reply`, the connector sends the agent's final text as the reply instead.

The first prompt of a session also carries the agent's instructions when the harness can't take a system prompt; see [Agent profiles](../guides/profiles.md).

## Where to look

| You want to know | Look at |
| :--- | :--- |
| why a message is held | hover it in the [Bandeja](../guides/web-app.md#bandeja-inbox) |
| whether an agent is working on it | **Tripulación**, or `seaport crew` |
| everything that happened | the live [event stream](../reference/events.md) |
