# Schedules

Post a message into a conversation on a timetable, so an agent starts work without anyone writing: a daily summary, a weekly review, a Monday check of open pull requests.

A **schedule** belongs to one [conversation](../explanation/concepts.md#conversations-and-ships) and holds a cron expression, a time zone and a text. When the expression matches, the port stores the text in that conversation as a message from the system sender `system:schedule`, shown as **Seaport · \<schedule name\>**. From there it is an ordinary inbound message: [customs](customs.md), [routing](agents-and-routing.md#routing), the delivery queue, the thread and the agent's reply all work as for a message a person wrote.

## Create one

In the web app, open a conversation, press (i), and under **Programaciones** press **Nueva**: name, when (four presets or your own cron, with the next three times), time zone (your browser's by default), the agent to call and the text. Each schedule has an on/off switch, **Ahora** (post it now) and a delete button.

From the CLI:

```sh
seaport schedules add <conversation-id> standup \
  -cron "0 9 * * 1-5" -timezone America/Lima -agent pm \
  -text "Summarize yesterday's activity and propose today's priorities."
seaport schedules                       # all of them
seaport schedules run <schedule-id>     # post the message now, to test the prompt
seaport schedules preview "0 9 * * 1-5" -timezone Europe/Madrid
```

The full list of commands is in the [CLI reference](../reference/cli.md#schedules), and the routes in the [HTTP API](../reference/http-api.md#schedules).

## Cron syntax

Five fields, separated by spaces: `minute hour day-of-month month day-of-week`.

| Field | Values |
| :--- | :--- |
| minute | `0`–`59` |
| hour | `0`–`23` |
| day of month | `1`–`31` |
| month | `1`–`12` or `jan`–`dec` |
| day of week | `0`–`7` (`0` and `7` are Sunday) or `sun`–`sat` |

Each field takes `*`, a number, a range (`1-5`), a list (`1,15`) and a step (`*/15`, `10-50/10`). The macros `@hourly`, `@daily`, `@weekly` and `@monthly` also work. When both day of month and day of week are restricted, a day matching **either** runs (as in Vixie cron).

| Expression | Runs |
| :--- | :--- |
| `0 9 * * 1-5` | 09:00, Monday to Friday |
| `30 8 * * mon` | 08:30 every Monday |
| `0 */6 * * *` | every six hours, on the hour |
| `0 0 1 * *` | midnight on the 1st of each month |

Two refusals keep a schedule from becoming a flood or a no-op:

- **At most once every 5 minutes.** An expression that can fire closer together (`* * * * *`, `*/2 * * * *`) is refused with `422`.
- **It must fire sometime.** `0 0 31 2 *` (31 February) is refused.

## Time zone

The expression is read as wall-clock time in the schedule's IANA zone (`America/Lima`, `Europe/Madrid`, `UTC`; default `UTC`). `Local` is refused so a schedule means the same on every host. Around daylight-saving changes:

- A time that doesn't exist that day (02:30 when the clocks jump from 02:00 to 03:00) runs once, shifted by the gap.
- A time that happens twice runs once, at its first occurrence.

## How it runs

Every process of the port checks for due schedules every 30 seconds. Due schedules are claimed with `FOR UPDATE SKIP LOCKED` and moved to their next time in the same transaction, so with [several processes](operations/several-processes.md) exactly one fires each slot.

- **Missed slots are skipped.** If the port was down at 09:00 and comes back at 09:30, the 09:00 slot is not run (the grace is 2 minutes). The schedule simply continues with its next time. Nothing is queued up and replayed.
- **No duplicates.** The message's external id is `schedule-<id>-<slot>`, and the message pipeline drops an external id it already stored, so a retried slot never posts twice.
- **A broken schedule turns itself off.** If its expression can't be evaluated (for example a zone that the host no longer knows), or the agent it names was deleted, the schedule is disabled rather than failing at every slot, and a `schedule.updated` event tells the web. Fix it (pick another agent) and switch it on again.
- **Run now** (`POST /schedules/{id}/run`, **Ahora**, `seaport schedules run`) posts the message immediately through the same path and returns the stored message. It doesn't change the schedule's next time.

The message is subject to everything a message is. If the conversation is **pending** or **denied** in customs, the message is stored but held; if the conversation has no agent to route to, it is held for routing. Both show up in the conversation like any held message.

## Choosing the agent

Set an **agent** and the text is posted as `@<agent> <text>`, so [routing](agents-and-routing.md#routing) picks that agent (it must be on the conversation's dock team, or the message is held). Without one, the dock's team decides: the thread's previous agent, the only member, or the default.

## Where the agent answers

The agent answers like it does to any message: on the conversation the schedule posted into. That only works if the conversation's dock can reply. On a dock where [replies are off](../reference/http-api.md#docks) (trigger-only) or on a channel the agent can't reach, the agent is told which door to use: [link](linked-conversations.md) the conversation to the one where people read, and the answer goes to the door that takes replies. A common setup is an `api` dock used only for schedules, linked to the Slack channel where the summary should land.

## Limits and permissions

- At most 100 schedules per organization (`SEAPORT_MAX_SCHEDULES`, `0` = no limit); another is refused with `422 QUOTA_EXCEEDED`. See [Limits](../reference/limits.md).
- Names are unique per organization; the text is up to 65,280 characters.
- Reading needs `seaport:schedules:read`, writing needs `seaport:schedules:write` (Admin and Operator roles; Viewer reads). See [Permissions](../reference/permissions.md).
- Deleting a conversation (or its dock) deletes its schedules. Deleting the agent a schedule names switches that schedule off at its next slot (nothing is posted); give it another agent and switch it on.
