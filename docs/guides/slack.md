# Answer in Slack

A `slack` dock is a Slack app of your own: mention one of your agents in a channel, or message it directly, and it answers in the same thread.

Each channel thread is one conversation, and so is each direct message chat. Customs decides who may wake an agent, the dock's team decides which agent answers, and the agent's `reply` is posted in the thread. Use it for support, triage or questions to the agents you already run on WhatsApp, GitHub or the `api` dock.

> [!NOTE]
> Slack must reach your port: the dock needs a public `https` `SEAPORT_PUBLIC_URL` (see [Deploy](operations/deploy.md)) and the [secret key](../reference/configuration.md#secret-key), which seals the app's tokens and signing secret. For a port on your laptop, put a tunnel in front of it and set `SEAPORT_PUBLIC_URL` to the tunnel's address.

## Create the dock and connect the app

Create the dock first: **Muelles → Nuevo muelle → Slack** in the web app, or `seaport dock add slack -name support`, which prints the dock's page. Then connect a Slack app to it in one of two ways. Both end with the dock **linked**, its address being the workspace name and URL.

### Flow A: Seaport creates the app

1. In [api.slack.com/apps](https://api.slack.com/apps), under **Your App Configuration Tokens**, generate a token for your workspace. It starts with `xoxe.xoxp-`.
2. On the dock's page choose **Crear automáticamente**, paste the token, optionally change the app's name and press **Crear e instalar**. Seaport uses the token once to create the app from its manifest and doesn't keep it.
3. Slack asks you to allow the app in your workspace. Confirm, and Slack sends you back to the dock's page.

Seaport keeps the app's client secret, signing secret and bot token sealed. If you leave before step 3, the dock's page offers **Instalar la App** to continue.

### Flow B: you create the app

Use this when your workspace doesn't allow configuration tokens.

1. On the dock's page choose **Ya tengo la App** and copy the manifest (also `GET /api/v1/docks/{id}/slack`).
2. In [api.slack.com/apps](https://api.slack.com/apps) choose **Create New App → From a manifest**, pick the workspace and paste the manifest. Install the app.
3. From **Basic Information** copy the **Signing Secret**, and from **OAuth & Permissions** the **Bot User OAuth Token** (`xoxb-…`). Paste both on the dock's page and press **Conectar**. Seaport checks the token with Slack before keeping anything.

The manifest sets the events request URL to `<SEAPORT_PUBLIC_URL>/slack/events/<dock-id>` and the redirect URL to `<SEAPORT_PUBLIC_URL>/slack/oauth/callback`. Slack verifies the request URL when the manifest is saved; Seaport answers its challenge even before the signing secret is known.

The app asks for these bot scopes:

| Scope | Why |
| :--- | :--- |
| `app_mentions:read` | see mentions in channels |
| `channels:history`, `groups:history` | see replies in threads of public and private channels |
| `im:history`, `im:read`, `im:write` | direct messages |
| `chat:write` | post replies |
| `reactions:write` | react with 👀 when a message was taken |
| `users:read`, `users:read.email` | show names and give customs the sender's email |
| `files:read`, `files:write` | read images people send, upload files agents attach |

It subscribes to the bot events `app_mention`, `message.channels`, `message.groups` and `message.im`.

## Invite the app and call an agent

Slack only sends a channel's messages once the app is in it: in the channel, type `/invite @<app name>` (private channels too). Then assign agents to the dock as usual ([Agents and routing](agents-and-routing.md)).

| What happens in Slack | What Seaport does |
| :--- | :--- |
| You mention the app (`@app`) or an agent by handle (`@reviewer`) in a channel | A conversation for that thread starts, addressed to the agent named, else the default. |
| You write in a thread where an agent already took part | Addressed, no mention needed. |
| You write in the channel outside such threads | Ignored and not stored. |
| You message the app directly | A direct conversation, always addressed. |
| A bot writes (including the app itself) | Dropped, so agents never answer each other in a loop. |

When Seaport takes a message it adds a 👀 reaction to it. Slack retries events it thinks failed; Seaport recognizes them by the message id and never queues a message twice.

## Let the right people in

A new dock asks first (`default ask`), like any other: the first thread that calls an agent waits under customs. In a workspace where everyone may use your agents, allow the dock by default:

```sh
seaport customs -dock <dock-id> default allow
```

Each message carries these handles for [customs rules](customs.md):

| Kind | Handles |
| :--- | :--- |
| Thread (chat) | the channel id (`C0123ABCD`), and `#channel-name` when Slack answers it; a direct chat: its conversation id (`D0123ABCD`) |
| Author (sender) | the Slack user id (`U0123ABCD`) and, when the app may read it, the email |

So `seaport customs deny U0123ABCD` holds one person's messages everywhere, and `seaport customs allow "#support"` lets in one channel.

## What agents see

The conversation id is `<channel>/<thread timestamp>` for a channel thread and `<channel>` for a direct chat, titled `#channel` or the person's name. Message ids are `msg-<channel>-<timestamp>`. Slack's formatting is converted to Markdown: `*bold*` becomes `**bold**`, `~strike~` `~~strike~~`, `<url|label>` `[label](url)`, and user and channel mentions become `@name` and `#channel`.

Images people attach (up to the [image limit](../reference/limits.md#messages-and-media)) reach the agent like any other [image](messages-and-images.md); other files appear in the text as `[file: name]`.

## What replies look like

A reply is posted in the thread (a direct chat: in the chat) as a Slack `markdown` block, so Markdown renders as written, with the text repeated as the notification fallback. Slack limits one message's markdown to 12 000 characters, so a longer reply is split into several messages in the thread. Files an agent attaches are uploaded to the thread. If Slack refuses them, the reply fails with the reason instead of dropping them silently; add the `files:write` scope and install the app again if that is the cause.

## Unlink

**Desconectar la Slack App** (or `POST /api/v1/docks/{id}/unlink`) uninstalls the app from the workspace when Seaport created it (flow A; best effort) and forgets its credentials and known threads. An app you made by hand (flow B) stays in Slack: delete it at [api.slack.com/apps](https://api.slack.com/apps). Conversations are kept, and you can connect another app to the dock.

## Troubleshooting

| Symptom | Likely cause |
| :--- | :--- |
| Slack can't verify the request URL | `SEAPORT_PUBLIC_URL` isn't public `https`, or the dock id in the URL is wrong. The dock page warns when the address is unreachable. |
| Flow A: "Slack refused to create the app" | The configuration token is wrong, expired (they last 12 hours) or its workspace doesn't allow apps. |
| `Slack does not accept this bot token` | You pasted a user token or the wrong app's token; use the `xoxb-` Bot User OAuth Token. |
| Mentions do nothing | The app isn't in the channel (`/invite`), the dock isn't linked, or customs holds the chat: check **Aduana**. |
| Events answered `401` | A wrong signing secret, or the port's clock is off by more than 5 minutes. |
| Replies fail after a while | The app was uninstalled or its token revoked: the dock turns logged out; install it again. |

See also the [HTTP API](../reference/http-api.md#slack-docks) and the [configuration](../reference/configuration.md) of `SEAPORT_SLACK_URL` and `SEAPORT_SLACK_API_URL` (only changed for tests or Enterprise Grid proxies).
