# Wake agents from Datadog alerts

A `datadog` dock is an HTTPS endpoint for Datadog's [Webhooks integration](https://docs.datadoghq.com/integrations/webhooks/): each alert a monitor sends wakes an agent of the dock's team with the alert as a message. Alerts of the same monitor and scope are one conversation, so the recovery (and every re-notification) lands in the thread where the alert began.

A Datadog dock only triggers. Nothing is posted back to Datadog: the dock never takes replies, so the agent answers somewhere else. The usual place is the dock's **mirror**: a [destination](destinations.md) of another dock (a Slack channel, a WhatsApp group, an `api` chat) where Seaport itself posts each new alert and links that conversation to the alert's. People read the alert there, talk to the agent below it, and the agent's answers go there too. Without a mirror the alert still wakes the agent, which answers on a door someone [linked](linked-conversations.md) or stays silent.

The agent reads Datadog itself when it needs more than the alert says: give it Datadog's [MCP server](https://docs.datadoghq.com/bits_ai/mcp_server/) in its [profile](profiles.md), with the API and application keys as agent secrets.

> [!NOTE]
> Datadog must reach your port: use a public `https` `SEAPORT_PUBLIC_URL` (see [Deploy](operations/deploy.md)) and the [secret key](../reference/configuration.md#secret-key), which seals the dock's secret. For a port on your laptop, put a tunnel in front of it and set `SEAPORT_PUBLIC_URL` to the tunnel's address.

## Create the dock

1. Create it: **Muelles → Nuevo muelle → Datadog** in the web app, or `seaport dock add datadog -name monitors`. The dock is **linked** at once.
2. Make its secret: **Generar secreto** on the dock's page, or `seaport dock datadog <dock-id> -rotate`. Copy it: Seaport stores it sealed and shows it only once. Generating another one replaces it, and the old one stops working.
3. Give the dock a team and let its conversations in: `seaport agents assign <agent> <dock-id> -default`, then allow its conversations in [customs](customs.md) (`seaport customs -dock <dock-id> default allow`: only Datadog knows the secret).

The webhook URL is `<SEAPORT_PUBLIC_URL>/datadog/webhook/<dock-id>`.

## Configure Datadog

In Datadog, **Integrations → Webhooks → New**:

- **Name**: what you'll mention in monitors, for example `seaport`.
- **URL**: the dock's URL.
- **Payload**: the one the dock's page shows (or `seaport dock datadog <dock-id>` prints). It is Datadog's variables as a JSON object:

```json
{
  "id": "$ID",
  "alert_id": "$ALERT_ID",
  "transition": "$ALERT_TRANSITION",
  "title": "$ALERT_TITLE",
  "status": "$ALERT_STATUS",
  "scope": "$ALERT_SCOPE",
  "tags": "$TAGS",
  "message": "$EVENT_MSG",
  "link": "$LINK",
  "priority": "$PRIORITY",
  "date": "$DATE",
  "org": "$ORG_NAME"
}
```

- **Custom headers**: `{"X-Seaport-Token": "<secret>"}` (Datadog takes `Authorization: Bearer <secret>` too).

Then mention the webhook in the monitors that should wake agents: `@webhook-seaport` in the monitor's message (or in a [notification rule](https://docs.datadoghq.com/monitors/notify/notification_rules/) for many monitors at once). Only `alert_id` is required; the rest shapes the message.

A wrong secret and an unknown dock are the same `404`; a body that isn't a JSON object is `415`; one without the monitor's id is `400`. A dock takes 60 calls a minute (a burst of 60, then one a second); more are `429` with `Retry-After`. The answer is `202` with `{"accepted":true,"mirrored":false,"conversation_id":"…"}`; `accepted` is `false` for an event the dock already has (Datadog's retry).

## What agents see

The conversation is `monitor/<monitor id>/<scope>` (`*` when the monitor has no scope), titled after the alert; a group where every message is addressed, so agents wake without a mention. It carries handles [customs rules](customs.md#rules) can match: `monitor:<id>`, `scope:<scope>` and the alert's tags as `tag:<key>:<value>` (`tag:env:prod`, `tag:team:platform`; the first 12). The sender is `system:datadog` ("Datadog").

The message is the transition in bold (`Triggered`, `Recovered`, `Re-notify`, `Warn`, `No Data`), the title, the status line, the scope, the priority, the tags, the monitor's message and the link to the event in Datadog. Its id is `event-<Datadog event id>`, so Datadog's retries are one message.

The conversation is read-only: the prompt says so and sends the agent to a door of the thread that takes replies.

## Mirror the alerts

The mirror is the generic [dock mirror](mirrors.md): where a new alert is shown to people. Pick a dock whose replies are on and one of its [destinations](destinations.md): **Espejo** on the dock's page, or `seaport dock mirror <dock-id> <chat-dock-id>:oncall`, or

```sh
seaport dock destination <chat-dock-id> oncall -target C0123456789 -title "the on-call channel"
seaport dock datadog <dock-id> -mirror <chat-dock-id>:oncall
```

When an alert arrives whose conversation is a thread of one (the first alert of that monitor and scope, or the first after someone unlinked the thread), Seaport opens the destination with the alert's text as the first message, as itself (no agent speaks: the owner named the place), and links the two conversations. The agent wakes to a thread with both doors, so its answer goes to the mirror; people answer there and the same agent continues. Later alerts of the thread (a recovery, a re-notify) arrive on the alert's door and wake the agent in the same thread; Seaport posts nothing more to the mirror, the agent does (it says the alert recovered where people are reading). A Slack destination makes a thread per opening, so each monitor has its own; a WhatsApp group or an `api` chat is one conversation that every mirrored monitor shares. The answer's `mirrored` is `true` when a conversation was opened.

A mirror that fails (the destination's dock is unlinked, the send is refused by its [limits](../reference/limits.md#outbound-rate-limits)) is logged and the alert still wakes the agent, on its own door. A Datadog dock can't be a mirror (it takes no replies), and removing the destination removes the mirror. `-no-mirror` (or **Quitar el espejo**) clears it.

Held alerts (a customs policy that asks, a denied monitor) open nothing: a mirror only shows what the owner let in.

## Close it

`seaport dock unlink <dock-id>` (or **Cerrar el webhook**) deletes the secret and the mirror: the URL answers `404`. History stays. `seaport dock relink <dock-id>` opens it again with a new secret to generate.

## Limits

See [Limits](../reference/limits.md#datadog-docks): 1 MiB per body, 60 calls a minute per dock. The HTTP routes are in the [HTTP API](../reference/http-api.md#datadog-docks).
