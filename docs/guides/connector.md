# The connector

The connector runs a coding agent on your machine and bridges it to a port. It dials out, so your machine needs no public address and no open port.

The command is `seaport connect`. The port sends it messages; the harness thinks, uses tools and replies.

```text
port (seaport serve) ◀──WebSocket /crew/ws── seaport connect ──ACP (stdio)──▶ harness (rness, Claude Code…)
                                                                          └──▶ port's MCP (reply, history…)
```

A **harness** is the coding-agent program: [rness](https://github.com/Abraxas-365/rness) or Claude Code. It must speak the [Agent Client Protocol](https://agentclientprotocol.com) (ACP). The connector starts it as a child process and talks to it over stdin/stdout.

## Run it

```sh
seaport connect -agent pm -- rness --acp -m anthropic/claude-sonnet-4-5      # none mode
SEAPORT_URL=https://port.example.com SEAPORT_CREW_TOKEN=sp_crew_… \
  seaport connect -- rness --acp                                              # token or iam mode
```

Everything after `--` is the harness command. Run the connector from the directory the agent should work in (or pass `-cwd`).

On start, the connector:

1. fetches the agent's [profile](profiles.md) and secrets, then starts the harness with its secrets as environment variables;
2. opens a WebSocket to the port's `/crew/ws` and says hello (agent name, concurrency, whether it can steer, profile version);
3. waits. Each delivery of messages becomes a prompt to the harness.

The harness answers through the port's MCP tools, which the connector adds to every session: `reply` sends a message, `history` reads earlier ones, `conversations` lists the chats the agent can reply to, `attachment` shows an image, and `skill` loads a [skill](profiles.md#skills). The agent's final text is **not** sent to anyone unless you use `-auto-reply`.

If the connection drops, the connector reconnects, waiting 1 second and doubling up to 30 seconds. If the harness process exits, the connector stops with an error.

## Flags

| Flag | Default | Meaning |
| :--- | :--- | :--- |
| `-server URL` | `$SEAPORT_URL`, else `http://127.0.0.1:8740` | the port |
| `-token TOKEN` | `$SEAPORT_CREW_TOKEN` | the agent's [crew token](agents-and-routing.md#crew-tokens); none for a port in `none` mode |
| `-agent NAME` | `$SEAPORT_AGENT` | the agent to work as. Required in `none` mode; with a token it is optional (the token names the agent) and must match if given |
| `-name LABEL` | the harness command's name | the label this connection shows in **Tripulación** |
| `-concurrency N` | `2` | conversations worked at once (up to 32) |
| `-deliver steer\|queue` | `steer` | what to do with messages that arrive while the agent is working; see below |
| `-approve allow\|reject` | the profile's, else `reject` | answer to the harness's permission requests |
| `-auto-reply` | off | send the harness's final text as the reply, for harnesses without MCP |
| `-cwd DIR` | the current directory | the harness's working directory |
| `-v` | off | debug logs |

Put secrets in the environment or the profile, not on the command line.

## Sessions: one per conversation

The connector keeps one harness **session per conversation**, so each chat has its own context. It records them in `sessions.json` under your user config directory, in `seaport/` (on Linux `~/.config/seaport/sessions.json`). Sessions are kept per port, working directory, harness command and agent, so two agents in one directory don't share them.

After a restart, the connector resumes the saved session of a conversation when the harness can resume sessions. If it can't, it logs a warning and starts a new session; the agent can still read the chat with `history`.

## Steer or queue

Only one delivery per conversation is in flight at a time, so a session never gets two prompts at once. What about a message that arrives while the agent is already working on that conversation?

- **`steer`** (default): the port sends the new messages to the same delivery, and the connector hands them to the running turn. The agent sees them without waiting. This needs a harness that advertises it; today that is rness, through its `_rness/steer` ACP extension. If the harness can't, the connector warns and falls back to `queue`.
- **`queue`**: the new messages wait and go out as the next prompt after the turn ends.

If a steered message arrives just as the turn ends and the harness can't take it, the port delivers it again as a new prompt. Nothing is lost.

## Concurrency

`-concurrency` is how many conversations this connection works at the same time. A single conversation is always worked one turn at a time, so more concurrency helps only with several active chats. A higher number uses more of your machine and your model's rate limits. Pick what the harness and your API quota can take.

A failed turn (the harness errors, the model refuses) gives its messages back to the port, after a pause that grows with each failure in a row, so they are retried.

## Permissions

Harnesses ask before using a tool. The connector answers for them, so nothing waits on a person:

- `-approve reject` (the default) refuses the request.
- `-approve allow` accepts it.
- If you don't pass the flag, the agent profile's **approve** setting applies; the flag wins over it.

Requests for the port's own tools (`reply`, `history`…) are always allowed, otherwise the agent couldn't answer.

> [!WARNING]
> `allow` lets the agent run shell commands and edit files in `-cwd` for anyone who can get past [customs](customs.md). Use it on a machine or directory you can afford to lose, and keep customs strict.

## Examples

### rness

```sh
export ANTHROPIC_API_KEY=…     # the harness's own key; Seaport never sees it
seaport connect -agent pm -cwd ~/work/pm -- rness --acp -m anthropic/claude-sonnet-4-5
```

rness supports steering, takes the profile's instructions as a system prompt, and takes images if its model does.

### Claude Code

Claude Code speaks ACP through the [claude-agent-acp](https://www.npmjs.com/package/@agentclientprotocol/claude-agent-acp) adapter:

```sh
npm i -g @agentclientprotocol/claude-agent-acp
seaport connect -agent pm -approve allow -- claude-agent-acp
```

It asks permission for every tool, so you will usually want `-approve allow` (or the profile's approve). It has no steering: messages that arrive mid-turn go out as the next prompt. It takes images. See [Known limitations](../project/known-limitations.md).

The first time, follow the tutorial: [Your first agent](../tutorials/first-agent.md).

## Images

When a message has images, the connector downloads them from the port with the agent's token and sends them to the harness as image blocks next to the text, in order, up to 8 per prompt. It does this only if the harness advertises image support in ACP (`promptCapabilities.image`). Otherwise, or if one download fails, the prompt only names the attachment, and the agent can look at it with the `attachment` tool. See [Messages and images](messages-and-images.md).

## Running several

Run one connector per harness. For several agents, use a connector for each, each with its own token (or `-agent` name) and, ideally, its own `-cwd`. For one agent on several machines, use the same agent with one token per machine. See [Agents and routing](agents-and-routing.md#several-connections-per-agent). The organization limits live connections (`SEAPORT_MAX_CREW_CONNECTIONS`, 20 by default).

To have the port run the harness for you instead, see [Hosted agents](hosting/hosted-agents.md).

## Troubleshooting

| You see | Cause and fix |
| :--- | :--- |
| "the port requires a crew token" | the port isn't in `none` mode; pass `-token` or set `SEAPORT_CREW_TOKEN` |
| "the port rejected the crew token (revoked, expired or not of this port)" | create a new token ([Tokens de agentes](web-app.md#tokens-de-agentes-agent-tokens) or `seaport crew tokens create`). The connector stops; it doesn't retry a refused token |
| "the port refused the connection: …" (a refused hello) | the agent named by `-agent` isn't the token's agent, doesn't exist, or none was named in `none` mode. The reason follows the message. The connector stops |
| "this organization already has as many agents connected as it allows" | the connection limit is reached; disconnect one, or raise `SEAPORT_MAX_CREW_CONNECTIONS` |
| "ACP initialize (is the command an ACP agent?…)" | the command after `--` isn't an ACP agent, for example `rness` without `--acp` |
| "the agent can't use HTTP MCP servers" / "can't use the reply tool" | the harness lacks HTTP MCP; use `-auto-reply`, and expect profile skills and MCP servers to be unavailable |
| "the agent can't take messages mid-turn" | the harness can't steer; messages queue for the next turn (informational) |
| Messages wait and nothing happens | check that the agent is on the dock's team, the chat is allowed, and the agent shows up in **Tripulación**. See [Message lifecycle](../explanation/message-lifecycle.md) |
| "port connection lost, reconnecting" | transient; the connector retries on its own |

Run with `-v` for debug logs.
