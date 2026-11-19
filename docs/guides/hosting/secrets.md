# Variables and secrets of hosted agents

Where a hosted agent's configuration lives: plain variables in its machine, its own secrets, and what each slot's harness ends up with.

A hosted agent's harness runs in a container the port starts. What it finds in its environment comes from two places, and each has a job:

| Where | What | Set with |
| :--- | :--- | :--- |
| The machine's `env` | plain variables every slot gets: a time zone, a feature flag, a region | the agent's config (`seaport agents config apply`, the web app's **Dónde corre** tab) |
| The agent's secrets | everything private: the model keys and repo credentials the machine names as `${NAME}`, tracker tokens for MCP headers, and, with `-env`, a credential the harness needs as a variable | `seaport agents secrets set <agent> NAME [-env]` |

The agent has one secret store. There are no separate secrets per machine or per group of agents: each agent keeps its own, so an agent never sees another's. The port's own variables (`SEAPORT_URL`, the slot token, the gateway and proxy settings) come last and can't be replaced by any of them. See [Agent config](../../reference/agent-config.md#env) for the limits.

> [!NOTE]
> Releases before this one kept secrets for shared environments as well. The upgrade moved them into the agents that were hosted on them; see [Backups and upgrades](../operations/backups-and-upgrades.md#upgrading-to-the-agent-centric-config).

## Plain variables

`env` holds values that aren't secret:

```yaml
machine:
  env:
    TZ: America/Lima
    FEATURE_FLAGS: beta
```

They are part of the agent's config, so they are versioned with it and visible to anyone who can read it. A value that looks like a credential (a token, a key, a connection string with a password) is refused: that is a secret.

## Agent secrets

The agent's secrets are sealed with the port's secret key. The API, the CLI and the web app show their names and flags, never values.

```sh
printf %s "$ANTHROPIC_API_KEY" | seaport agents secrets set pm ANTHROPIC_KEY
printf %s "$GIT_TOKEN"         | seaport agents secrets set pm GIT_TOKEN
printf %s "$DATABASE_URL"      | seaport agents secrets set pm DATABASE_URL -env
seaport agents secrets pm
seaport agents secrets rm pm GIT_TOKEN
```

Three uses:

- **Named by the machine.** `models.<provider>: ${ANTHROPIC_KEY}` and `repos[].credential: ${GIT_TOKEN}` take the value from the agent's secret of that name. The slot never sees it: the [gateway](network.md) forwards model calls with the key, and git asks the port for the credential each time. A secret the machine names can't be deleted, and the config can't be saved naming one the agent doesn't have.
- **Named by an MCP header.** `Authorization: Bearer ${BILLING_TOKEN}` in the profile; see [Agent profile](../../reference/agent-profile.md#secrets).
- **A variable of every slot (`-env`).** A connection string or a service token the harness itself needs. It reaches the harness of each of the agent's slots under that name. The names of the slot's own network variables (`ANTHROPIC_BASE_URL`, `ANTHROPIC_API_KEY`, the other providers', `HTTPS_PROXY`…) are refused with `-env`: the port sets those.

Setting or deleting a secret, and saving a config that names secrets, needs `agents:write`.

On the web, the agent's **Secretos** tab lists the secrets and lets you set and delete them, and says which ones are in use. Plain variables go with the machine, under **Dónde corre**.

## On your own machine

`seaport connect` runs the harness on a computer of your own, where the environment is yours: your shell already has your model key. So the agent's `-env` secrets stay on the port unless you ask for them with `-env`:

```sh
seaport connect -env -- rness --acp
```

A hosted slot always gets them: there the port is the one setting the harness up.

## Precedence

What a slot's harness sees, lowest first:

1. the machine's `env`;
2. the agent's `-env` secrets;
3. the port's slot variables: `SEAPORT_*`, each provider's `_BASE_URL` and `_API_KEY`, the proxy variables. The agent's kit can't replace them, and for Claude Code the port pins them as managed settings too.

A name can't be set in `env` if it is one of the slot's own, and a value that looks like a credential can't go there.
