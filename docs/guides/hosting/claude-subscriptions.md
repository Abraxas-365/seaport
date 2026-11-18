# Sign hosted agents in to a Claude subscription

Experimental: let a hosted slot (Claude Code or rness) use a Claude account you sign in to, instead of an API key held by the port.

Normally a hosted agent calls models with a key you store as a secret and the [gateway](network.md) forwards. If you would rather use a Claude subscription, an environment with no model key signs each slot in to an account instead.

> [!WARNING]
> This feature is experimental. A slot uses that account's limits and terms, and Anthropic's terms decide whether an agent like this may use a personal plan: check them first. Model calls skip the gateway, so there is no usage metering for these slots.

## When it applies

| Harness | Signs in when |
|---|---|
| `claude-code` | The spec has no `models.anthropic`. With a key, the gateway's key wins. |
| `rness` | The spec has no `models` at all. With any key, the gateway serves those providers and sessions start there. |

The proxy then also lets the slot reach `api.anthropic.com` and `platform.claude.com`, which the harness needs for its API and token refresh. You don't add them to `network.egress`.

A signed-in rness slot works on Anthropic models (Claude Sonnet unless the agent's profile picks another `anthropic/…` model). To give it other providers, use `models` and the gateway instead.

## Sign a slot in

A slot of such an environment waits after it starts, taking no messages, until it is signed in. The sign-in runs in the slot, so the slot must be running. While a sign-in is under way the slot counts as busy: `idle_stop` doesn't stop it and an environment update doesn't restart it.

### From the web app

Open the agent's page. In **Alojamiento**, a slot that isn't signed in shows **Sin cuenta**. Use **Conectar cuenta** (the key) on its row. On a stopped slot, the same button starts it first ("Arrancando la instancia…") and the login begins once it runs. The harness's own login runs inside the slot:

1. Open the link the dialog shows and sign in with the account the slot should use.
2. The page shows a code. Paste it into the dialog.
3. The dialog reports when the slot is signed in. On a first sign-in the agent starts working with the account. When you change the account of a slot that was already working, it switches once its current turn ends.

### From the CLI

```sh
seaport agents login pm-1
```

It prints the URL, asks for the code, and waits for the result. A sign-in has 10 minutes from start to the harness's answer; after that, start it again. A wrong code ends the sign-in with the harness's reason: start a new one.

Check how a slot is signed in:

```sh
seaport agents account pm-1
```

```text
pm-1: signed in (claude.ai, max)
```

Once signed in, the slot's harness restarts with the account when it is idle.

## What is stored where

- The tokens live in the slot's state volume, in the harness's own files (Claude Code's `~/.claude`, rness's `~/.rness/credentials.json`). The harness keeps and refreshes them there.
- The port never sees or stores them. It relays the sign-in's URL to you and your code back to the harness. The code is single use and bound to that login.
- Stopping a slot keeps the sign-in in its state volume, so a woken slot is still signed in. Each slot signs in on its own.

## A key or token instead

If you prefer not to sign in slot by slot, store a credential as an `-env` secret of the environment (every slot gets it) or of one agent. The value is read from standard input.

For Claude Code, create a long-lived token on your computer with `claude setup-token`:

```sh
seaport env secrets set dev CLAUDE_CODE_OAUTH_TOKEN -env
```

`seaport agents account` then reports the method as `oauth_token`.

For rness, an Anthropic API key:

```sh
seaport env secrets set dev ANTHROPIC_API_KEY -env
```

The slot uses the key instead of a sign-in, and `seaport agents account` reports `api_key`. (A key the port holds and the gateway forwards, with `models`, is usually better: it never enters the container and its use is metered.)

## Next

- [Network](network.md): the model gateway, the alternative with a key.
- [Run agents on the port](hosted-agents.md)
