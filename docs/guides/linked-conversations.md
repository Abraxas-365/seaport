# Linked conversations

Link the conversations where one matter is discussed, on any channels, so an agent works them as a single thread: one session, one memory, and answers on the channel each message came from.

A pull request is reviewed on GitHub; the team asks about that review in a Slack thread; the customer who requested it writes on WhatsApp. For Seaport these are three [conversations](../explanation/concepts.md#conversations-and-ships), each on its own dock, and by default the agent sees three unrelated chats. Linking them makes one **thread**: the agent remembers what was said on the pull request when Slack asks, and it answers each person where they wrote.

## What a thread is

Every conversation belongs to a thread. A conversation linked with nothing is a thread of one. Linking two conversations merges their threads: every conversation of both gets the same `thread_id`. Each conversation in a thread is a **door**: it keeps its dock, its channel ids, its own customs status and its own group mode. Only the agent's work is shared:

- **One session.** The connector keeps one harness session per thread (not per conversation), so what the agent learned on one door it knows on the others. Hosted agents give the thread one git worktree, `wt/<thread>` on branch `seaport/<thread>`.
- **One delivery in flight.** At most one delivery of the thread is open at a time, so the agent never gets two prompts about one matter at once. A message on another door while the agent works is [steered](connector.md#steer-or-queue) into the running turn, like a message on the same door.
- **The previous agent is the thread's.** When a message names nobody, [routing](agents-and-routing.md#routing) picks the agent who last answered on any door of the thread, if it is on that dock's team.
- **One history.** The agent reads every door together with `history` and `thread: true`; you do with `?thread=1` on the ships route, `seaport history -thread` or **Hilo completo** in the web app.

The prompt the agent gets names the other doors ("This conversation is one door of a thread; the others, where the same matter is discussed: Slack `#dev` (group): conversation_id …") and tags each message with its door, and it is asked to answer on the door each message came from unless told otherwise. A reply to a door still needs the agent on that dock's team and the door allowed by customs.

A thread holds at most 16 conversations; linking a seventeenth is refused (`422`).

## Link and unlink

In the web app, open a conversation, press (i) and under **Hilo** press **Enlazar**: pick another conversation by name, or paste its id. The bar under the header then lists the thread's doors; click one to open it, or **Hilo** to read every door's messages together, each labelled with its channel and name. **Separar esta conversación del hilo** takes the current conversation out; the others stay linked.

From the CLI:

```sh
seaport thread <conversation-id>                        # the doors of its thread
seaport thread link <conversation-id> <conversation-id> # merge their threads
seaport thread unlink <conversation-id>                 # take one out
seaport history <conversation-id> -thread               # messages of every door, oldest first
```

From the [HTTP API](../reference/http-api.md#conversations): `GET /api/v1/conversations/{id}/thread`, `POST …/links` with `{"conversation_id":"…"}` and `DELETE …/links`. Linking takes `conversations:write`.

Linking an agent's own conversations from a tool is not available yet: a person links them.

## What linking doesn't change

- **Customs.** A pending or denied door stays so; linking it never lets its messages in. Allow it as usual.
- **Teams.** An agent answers on a door only if it is assigned to that dock. If the thread's previous agent isn't on a door's team, routing falls back to that dock's rules (mention, only member, default), and a message nobody can take is held as before.
- **Message ids and channels.** Each door still talks to its channel with its own ids; a reply names the door (`conversation_id`) it goes out on.
- **Web permissions.** Reading the thread takes `conversations:read`; the whole-thread messages, `ships:read`.

## See also

- [Agents and routing](agents-and-routing.md)
- [The connector](connector.md#sessions-one-per-thread)
- [MCP tools: history](../reference/mcp-tools.md#history)
