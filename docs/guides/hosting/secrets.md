# Variables and secrets of hosted agents

Where a hosted agent's configuration lives: plain variables and secrets of the environment, the agent's own secrets, and what each slot's harness ends up with.

A hosted agent's harness runs in a container the port starts. What it finds in its environment comes from three places, and each has a job:

| Where | What | Set with |
| :--- | :--- | :--- |
| The environment's `env` | plain variables every slot gets: a time zone, a feature flag, a region | the spec (`seaport env apply`, the web app's **Variables** section) |
| The environment's secrets | the model keys and repo credentials the spec names as `${NAME}`; with `-env`, a credential every slot's harness needs as a variable | `seaport env secrets set <env> NAME [-env]` |
| The agent's secrets | what is one agent's own: a tracker token for its MCP headers, a key it must have apart from the others | `seaport agents secrets set <agent> NAME [-env]` |

The port's own variables (`SEAPORT_URL`, the slot token, the gateway and proxy settings) come last and can't be replaced by any of them. See [Environment spec](../../reference/environment-spec.md#env) for the limits.

## Plain variables

`env` holds values that aren't secret and are the same for every slot of the environment:

```yaml
env:
  TZ: America/Lima
  FEATURE_FLAGS: beta
```

They are part of the spec, so they are versioned with it and visible to anyone who can read the environment. A value that looks like a credential (a token, a key, a connection string with a password) is refused: that is a secret.

## Environment secrets

An environment's secrets are sealed with the port's secret key, like an agent's. The API, the CLI and the web app show their names and flags, never values.

```sh
printf %s "$ANTHROPIC_API_KEY" | seaport env secrets set dev ANTHROPIC_KEY
printf %s "$GITHUB_TOKEN"      | seaport env secrets set dev GITHUB_TOKEN
printf %s "$DATABASE_URL"      | seaport env secrets set dev DATABASE_URL -env
seaport env secrets dev
seaport env secrets rm dev GITHUB_TOKEN
```

Two uses:

- **Named by the spec.** `models.<provider>: ${ANTHROPIC_KEY}` and `repos[].credential: ${GITHUB_TOKEN}` take the value from the environment's secret of that name. The slot never sees it: the [gateway](network.md) forwards model calls with the key, and git asks the port for the credential each time. A secret the spec names can't be deleted.
- **A variable of every slot (`-env`).** A connection string or a service token the harness itself needs. It reaches every slot's harness under that name.

When you host an agent on an environment, or save a spec, every `${NAME}` the spec uses must exist: as a secret of the environment, or else as a secret of every agent hosted on it. Saving a spec that names a secret the environment doesn't have needs `agents:write` besides `environments:write`, since the agents' secrets would answer for it.

On the web, the environment's page has a **Modelos y secretos** section: one selector per model provider that picks the secret of the key among the environment's secrets (or creates one with **+**), next to the list of **Secretos del entorno** (each with its **Variable de entorno** switch; one the spec names shows **en uso** and can't be deleted). The checklist at the top of the page offers **Añadir clave de Anthropic**, which saves the secret and names it in a new version in one step. Plain variables go in the **Variables** section as `NAME=value` lines.

## Agent secrets

An [agent's secrets](../profiles.md#secrets) go wherever the agent works: its MCP headers resolve them, and the ones saved with `-env` reach the harness of each of its hosted slots. They exist for what is the agent's own. If every agent on an environment would carry the same secret, make it the environment's.

A model key can be an agent secret too, for an agent that must use its own key: `models.anthropic: ${ANTHROPIC_KEY}` with the environment lacking `ANTHROPIC_KEY` takes each hosted agent's. The gateway looks the environment's secret up first, then the agent's.

## On your own machine

`seaport connect` runs the harness on a computer of your own, where the environment is yours: your shell already has your model key. So the agent's `-env` secrets stay on the port unless you ask for them with `-env`:

```sh
seaport connect -env -- rness --acp
```

A hosted slot always gets them: there the port is the one setting the harness up.

## Precedence

What a slot's harness sees, lowest first:

1. the spec's `env`;
2. the environment's `-env` secrets;
3. the hosted agent's `-env` secrets;
4. the port's slot variables: `SEAPORT_*`, each provider's `_BASE_URL` and `_API_KEY`, the proxy variables. The agent's kit can't replace them, and for Claude Code the port pins them as managed settings too.

A name can't be set in `env` if it is one of the slot's own, and a value that looks like a credential can't go there.
