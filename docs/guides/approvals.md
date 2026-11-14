# Approvals

An agent can stop and ask people to choose before it does something consequential: deploy, delete, spend, send. The question goes out as a message on the conversation, people answer on the channel or in the web app, and the agent continues with the answer.

## How it works

1. The agent calls the MCP `ask_approval` tool (or `seaport ask`) with a conversation, a question and 2 to 6 options.
2. Seaport sends the question as the agent's message on that conversation, with the options numbered:

   ```text
   Deploy v2.3 to production?

   1. Approve
   2. Reject

   Reply with an option or its number.
   ```

   The card is an ordinary outbound message, so everything that applies to replies applies to it: the conversation must be allowed, the dock must take replies, and the [outbound limits](../reference/configuration.md#outbound-limits) count it.
3. Someone answers, in one of two ways:
   - **On the channel.** A message that is exactly an option (any case), its number (`2`), or `option 2` / `opción 2` answers it. A leading `@agent` mention is ignored. Anything else, such as `2 but wait`, is not an answer: it reaches the agent as a normal message and the approval stays pending.
   - **In the web app.** The card shows its options as buttons under the message (people with `ships:write`), or `POST /api/v1/approvals/{id}/answer`.
4. The agent finds out:
   - If an `ask_approval` call is blocked waiting (`wait_seconds`), it returns the answer.
   - Otherwise Seaport wakes the agent: a channel answer is the delivery of that very message; a web answer, a cancellation and an expiry are delivered as a system message from `system:approval` that names the approval, the answer and the question. Every delivery also carries `approvals`: the ones still pending and the ones that message answered or woke the agent for.

An agent should act only on an `answered` approval. `expired` and `cancelled` mean do not do it.

## Rules

- At most one pending approval per conversation and agent (`422`); answer, cancel or wait for expiry first.
- Options: 2 to 6, at most 64 characters each, one line, unique ignoring case, none starting with a digit (so a number is never ambiguous).
- The question is at most 4000 characters.
- An approval expires after `SEAPORT_APPROVAL_TTL` (default 24 hours, see [configuration](../reference/configuration.md#approvals)). An expired approval can't be answered.
- An agent reads and asks only for conversations of its own docks; another agent's approval is `404`.
- Answers are first come: a second answer is `422`.
- The channel matcher ignores messages from the agent itself, from `system:` senders and from other conversations.

## Waiting for the answer

`wait_seconds` blocks the call until an answer arrives. It is capped at 290 seconds, and further cut so the call returns before the server's request deadline (5 minutes). When the wait ends with no answer the approval is still `pending`: ask again with `approval` (`wait_seconds`) or just end the turn, and Seaport wakes the agent when someone answers. Prefer ending the turn for approvals that may take people hours.

## For people

The conversation view shows each approval under its card: pending ones with the options and cancel button, answered ones with the choice and who made it. `GET /api/v1/approvals?status=pending` lists them across conversations. The event `ship.approval` carries every change, so the web app updates live.

See [MCP tools](../reference/mcp-tools.md#ask_approval), [`seaport ask`](../reference/cli.md#ask-and-approval) and the [HTTP API](../reference/http-api.md#approvals).
