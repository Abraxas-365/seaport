# Agent alarms

Let an agent wake itself later in a conversation, instead of waiting or polling: "check again in 20 minutes whether the deploy finished".

An **alarm** is a one-shot [schedule](schedules.md) that an agent sets for itself with the `set_alarm` MCP tool. When it rings, the port posts the agent's own note into the conversation as a message from `system:schedule`, shown as **Seaport · alarma \<name\>**, addressed to the agent with an `@mention`. From there it is an ordinary inbound message: customs, routing and the delivery queue apply as for any message.

## Turn it on

Alarms are off for every agent. The owner turns them on per agent:

```sh
seaport agents profile set <agent> -alarms on
```

In a config file it is `alarms: true` (see [Agent config](../reference/agent-config.md)). While the switch is off, the agent doesn't get the alarm tools. Turning it off also stops the alarms the agent already set: each one is closed without firing when its time comes (a line is logged).

## What the agent does

- `set_alarm` with a `note` for its future self and exactly one of `in_minutes`, `at` (RFC 3339 with offset) or `cron` (recurring, with `timezone`). It names the conversation it is working; the agent must work on that conversation's dock.
- `alarms` lists its pending alarms; `cancel_alarm` deletes one. An agent manages only its own alarms.

The message it receives when the alarm rings is:

```text
@agent Alarm you set (<name>, set 2026-01-02T15:04:05Z): <note>
```

See [MCP tools](../reference/mcp-tools.md#set_alarm).

## Limits

An alarm over a limit is refused with `422 QUOTA_EXCEEDED` and a message that says what to do.

| Limit | Value |
| :--- | :--- |
| Pending alarms per agent in one conversation | 3 |
| Pending alarms per agent | 20 |
| Alarms an agent creates per rolling 24 hours (done ones count) | 50 |
| How far ahead a one-shot rings | 1 minute to 30 days |
| Cron alarms | at most once every 5 minutes |
| Note | 4000 characters |

Pending alarms count against the organization's schedule quota (`SEAPORT_MAX_SCHEDULES`); done ones don't. See [Limits](../reference/limits.md).

## How it runs

The runner of [schedules](schedules.md#how-it-runs) fires alarms. A one-shot fires up to 1 hour late (the port was down); a later one is closed without firing and logged. A one-shot that ran, or was missed, is **done**: it is switched off, kept for 7 days (shown as **Hecha** in the web, `seaport schedules list -done`) and then purged. A done alarm frees its name, so the agent can set another with the same one.

The alarm is taken when it rings: if posting its message fails (the conversation is gone, say), it is still done and isn't retried.

## People and alarms

People see alarms with the other schedules of a conversation (web **Programaciones**, `seaport schedules`, the API), marked as set by an agent. They can move, reword, switch off, run now or delete them, but not hand them to another agent (`422`): the message says "Alarm you set" to the agent that set it. **Run now** of a one-shot posts its message but doesn't mark it done; run now of a recurring alarm whose agent has alarms off switches it off. Creating a one-shot yourself is `seaport schedules add … -at TIME`, or `at` in the API.
