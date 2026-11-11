# Your first port

In this tutorial you run Seaport on your own computer, open its web app, create an `api` dock, send it a message as if you were a customer, and let that conversation through customs. It takes about ten minutes and needs no WhatsApp number and no model key.

You will end with a port that holds one allowed conversation, ready for the [first agent](first-agent.md).

## Before you start

- Docker (for Postgres), or a Postgres 16 you can create a database in.
- The `seaport` binary on your `PATH`. Take it from [Releases](https://github.com/Abraxas-365/seaport/releases) or see [Install Seaport](../guides/installation.md).
- `curl`.

## 1. Start Postgres

Seaport keeps everything in Postgres: docks, conversations, messages, agents, customs and the WhatsApp keys (sealed). From a checkout of the repository, the compose file starts one on `127.0.0.1:5449`:

```sh
docker compose up -d
export DATABASE_URL="postgres://seaport:seaport@127.0.0.1:5449/seaport?sslmode=disable"
```

Without a checkout, start any Postgres 16 and point `DATABASE_URL` at an empty database. Seaport runs its migrations at start.

## 2. Run the port

```sh
SEAPORT_AUTH=none seaport serve
```

`none` mode is for your own computer: it listens only on loopback (`127.0.0.1:8740`), has no login, and the CLI and connector need no tokens. The log ends with a line like:

```text
level=INFO msg="seaport listening" addr=127.0.0.1:8740 public_url=http://127.0.0.1:8740 version=…
```

Leave it running and use a second terminal for the rest.

> [!NOTE]
> On first start Seaport generates a secret key next to its configuration (`~/.config/seaport/secret.key` on Linux, `~/Library/Application Support/seaport/secret.key` on macOS). It seals WhatsApp keys, agent secrets and images. Keep it: see [Configuration](../reference/configuration.md#secret-key).

## 3. Open the web app

Open <http://127.0.0.1:8740>. The web app is in Spanish; [Web app tour](../guides/web-app.md) has the names in English. It is empty for now: no docks, no conversations.

## 4. Create an `api` dock

A **dock** is a channel account where messages arrive. An `api` dock has no phone behind it: your own systems post messages to it over HTTP, which makes it the easiest way to try Seaport.

```sh
seaport dock add api -name support
seaport dock list
```

Copy the dock id from the list. The dock appears at once in the web app's **Muelles** (Docks) page.

## 5. Send a message as a customer

Post a message to the dock as if a person named Ana had written from a chat called `ticket-42`:

```sh
DOCK=<dock-id>
curl -sS -X POST "http://127.0.0.1:8740/api/v1/docks/$DOCK/ships" \
  -H 'Content-Type: application/json' \
  -d '{"chat_id":"ticket-42","message_id":"m-1","sender_id":"ana","sender_name":"Ana Torres","text":"Hi! My invoice is missing. Can you help?"}'
```

The answer is the stored message (a **ship**). Sending the same `message_id` again returns `"duplicate": true` instead of a second message.

## 6. Let the conversation through customs

Every dock has **customs**: it decides which chats may wake an agent. A new dock asks you first, so the conversation is **pending** and its message is **held**:

```sh
seaport conversations -pending
```

In the web app the chat shows under **Aduana** (Customs) with the reason it is waiting. Allow it, from the web app or the CLI:

```sh
seaport allow <conversation-id>
seaport conversations -allowed
```

Allowing a pending direct chat also releases what it sent while it waited. There is no agent on the dock yet, so Ana's message is held again, this time for routing: the web app shows *no agent is assigned to this dock*. That is expected; the next tutorial adds the agent, and Ana's next message goes straight to it.

For chats you always trust, set a rule instead of allowing them one by one:

```sh
seaport customs -dock $DOCK default allow    # every chat of this api dock may wake an agent
```

## Check what you built

```sh
seaport history <conversation-id>
```

You should see Ana's message, inbound. The web app's **Bandeja** (Inbox) shows the same conversation, live: post another message with a new `message_id` and watch it appear without reloading.

## If something went wrong

| Symptom | Cause |
| :--- | :--- |
| `DATABASE_URL is required` | Export it in the terminal that runs `seaport serve`. |
| `connection refused` from the CLI | The port isn't running, or it listens elsewhere: set `SEAPORT_URL`. |
| `403` from `curl` through a tunnel or proxy | `none` mode refuses forwarded requests. Use `token` mode for anything beyond your computer. |
| The message stays held after `allow` | It was held for routing, not customs: no agent is assigned to the dock yet. The [next tutorial](first-agent.md) fixes that. |

## Next

- [Your first agent](first-agent.md): connect rness or Claude Code and let it answer Ana.
- [Customs](../guides/customs.md): rules, group modes and overrides.
- [Link a WhatsApp number](../guides/whatsapp.md) when you want a real channel.
