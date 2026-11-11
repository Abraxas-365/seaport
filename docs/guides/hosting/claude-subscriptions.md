# Sign hosted Claude Code in to a subscription

Experimental: let a hosted Claude Code slot use a Claude account you sign in to, instead of an API key held by the port.

Normally a hosted agent calls models with a key you store as a secret and the [gateway](network.md) forwards. If you would rather use a Claude subscription, a `claude-code` environment with no `models.anthropic` signs each slot in to an account instead.

> [!WARNING]
> This feature is experimental. A slot uses that account's limits and terms, and Anthropic's terms decide whether an agent like this may use a personal plan: check them first. Model calls skip the gateway, so there is no usage metering for these slots.

## When it applies

Only when both are true:

- The environment's `harness` is `claude-code`.
- The spec has no `models.anthropic`. With a key, the gateway's key wins.

The proxy then also lets the slot reach `api.anthropic.com` and `platform.claude.com`, which Claude Code needs for its API and token refresh. You don't add them to `network.egress`.

The `rness` harness has no headless sign-in: the sign-in refuses it, so give an rness environment a model key.

## Sign a slot in

A slot of such an environment waits after it starts, taking no messages, until it is signed in. The slot must be running.

### From the web app

Open the agent's page. In **Alojamiento**, use **Conectar cuenta** on the slot's row. Claude Code's own login runs inside the slot:

1. Open the link the dialog shows and sign in with the account the slot should use.
2. The page shows a code. Paste it into the dialog.
3. The dialog reports when the slot is signed in.

### From the CLI

```sh
seaport agents login pm-1
```

It prints the URL, asks for the code, and waits for the result. A sign-in has 10 minutes from start to the harness's answer; after that, start it again.

Check how a slot is signed in:

```sh
seaport agents account pm-1
```

```text
pm-1: signed in (claude.ai, max)
```

Once signed in, the slot's harness restarts with the account when it is idle.

## What is stored where

- The tokens live in the slot's state volume, in the harness's own files. Claude Code keeps and refreshes them there.
- The port never sees or stores them. It relays the sign-in's URL to you and your code back to the harness. The code is single use and bound to that login.
- Stopping a slot keeps the sign-in in its state volume, so a woken slot is still signed in. Each slot signs in on its own.

## A token instead

If you prefer not to sign in slot by slot, create a long-lived token on your computer with `claude setup-token`, and store it as an environment secret of the agent:

```sh
seaport agents secrets set pm CLAUDE_CODE_OAUTH_TOKEN -env
```

The value is read from standard input. Every slot of the agent then uses it, and `seaport agents account` reports the method as `oauth_token`.

## Next

- [Network](network.md): the model gateway, the alternative with a key.
- [Run agents on the port](hosted-agents.md)
