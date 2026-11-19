# Run agents on the port

Let the port run your agents in Docker containers, started when messages arrive and stopped when work runs out, so nobody has to keep a `seaport connect` open.

Without hosting, an agent runs wherever you run `seaport connect`: your laptop, a server you manage. That is fine for one person. When a team shares a port, or an agent should answer at 3 a.m., you want the port itself to start the agent, give it the repos and tools it needs, and stop it when it is idle. That is hosting.

An agent's [config](../../reference/agent-config.md) is all it takes: its harness and its **machine**, the description of where it runs (image, packages, repos, network, resources). Hosting the agent gives it **slots**, and a slot is one container, named `<agent>-<n>` (`pm-1`, `pm-2`) that keeps its own sessions and workspace. There is nothing else to create: no separate object to name, version or share.

## Requirements

- `SEAPORT_RUNTIME=docker` on the port. The default, `off`, refuses builds and starts.
- `SEAPORT_AUTH=token` or `iam`. Slots sign in with crew tokens, so `none` mode is refused at start.
- A Docker engine the port can reach, at `DOCKER_HOST`.

The [`deploy/`](../operations/deploy.md) compose file wires all of this. Set `SEAPORT_RUNTIME=docker` in `deploy/.env`, then start it with the `hosting` profile:

```sh
docker compose --profile hosting up -d --build
```

The profile adds a `docker-proxy` service (a Docker socket proxy that allows only the Engine calls Seaport makes: containers, exec, images, build, volumes, networks). The Docker socket is never mounted into Seaport. Slots run on the `internal` network `seaport-slots`, where the only thing they reach is Seaport.

> [!WARNING]
> Whoever controls Seaport controls that Docker engine. Give hosting a server or VM of its own.

The deploy compose file sets `SEAPORT_RUNTIME_NETWORK`, `SEAPORT_AGENT_URL` and `SEAPORT_EGRESS_ADDR` for you. Other hosting variables:

| Variable | Default | Meaning |
| :--- | :--- | :--- |
| `SEAPORT_AGENT_URL` | `SEAPORT_PUBLIC_URL` | How slots reach the port, such as `http://seaport:8740` on a shared Docker network. |
| `SEAPORT_RUNTIME_NETWORK` | `bridge` | The Docker network slots join. Make it `internal` when you use the egress proxy. |
| `SEAPORT_RUNTIME_HOST_GATEWAY` | `true` | Adds `host.docker.internal` to slots, so they can reach a port running on the host. |
| `SEAPORT_IMAGE_RNESS`, `SEAPORT_IMAGE_CLAUDE_CODE` | the release's image | Base images, see [Image builds](#image-builds). |
| `SEAPORT_MAX_SLOTS` | `10` | Slots per organization. `0` turns the limit off. |
| `SEAPORT_HOST_MAX_CPUS`, `SEAPORT_HOST_MAX_MEMORY_MB` | `4`, `8192` | The most one slot may ask for. `0` turns a limit off. |

The full list, with the network variables, is in [Configuration](../../reference/configuration.md).

## The machine

The machine is the `machine` part of the agent's config, next to the `harness`. This one runs rness with two apt packages, one repo, a plain variable and a model key:

```yaml
harness: rness              # rness | claude-code
instructions: |
  You work on the Example Co backend.
machine:
  packages: [jq, ripgrep]     # apt, into a derived image
  setup: |                    # bash as root at build time, never secrets
    npm i -g pnpm
  repos:
    - url: https://git.example.com/acme/api.git
      ref: main
      credential: ${GIT_TOKEN}   # a secret of the agent
      on_start: pnpm i
  repo_config: trust          # ignore: the harness skips the repos' own configuration
  home: ./rness-home          # your harness config, see "Harness homes"
  env:
    TZ: America/Lima          # plain variables of every slot; secrets go apart
  models:
    anthropic: ${ANTHROPIC_KEY}   # a secret of the agent, through the port's gateway
  network:
    egress: [registry.npmjs.org]
  resources: {size: M}
  workspace: persistent
  idle_stop: 30m
```

Fields, with their limits:

| Field | Meaning |
| :--- | :--- |
| `harness` | `rness` or `claude-code` (a field of the config, not of the machine). |
| `image` | Your own base image. It must derive from Seaport's base image of the same harness. |
| `packages` | Up to 100 apt packages, optionally pinned as `name=version`. |
| `setup` | A bash script run as root at build time, up to 16384 bytes. It is stored and baked into the image: no secrets. |
| `repos` | Up to 10 repos: `url` (https), `ref`, `path` (under `/workspace`), `credential`, `on_start`. |
| `repo_config` | `trust` (default): the harness reads the repos' `AGENTS.md`, `CLAUDE.md`, `.claude/` and `.rness/`. `ignore`: it doesn't. |
| `home` | A harness home, see [Harness homes](harness-homes.md). |
| `env` | Up to 50 plain variables every slot's harness gets. Not for credentials, see [Variables and secrets](secrets.md). |
| `models` | One entry per provider (`anthropic`, `openai`, `openrouter`, `groq`, `deepseek`): the agent secret holding the model key, see [Network](network.md). |
| `network.egress` | Up to 100 allowed hosts, see [Network](network.md). |
| `browser.engine` | `chromium` gives the slots a managed browser, see [Browser](browser.md). |
| `resources` | `size: S`, `M` or `L`, or `cpus` and `memory_mb`. |
| `workspace` | `persistent` (default) or `ephemeral`. |
| `idle_stop` | Idle time before a slot stops. |

A machine can't hold secrets. `credential` and `models.<provider>` name one of the agent's own secrets as `${NAME}`. Seaport checks that the secret exists each time you save the config, and the secret can't be deleted while the machine names it. Saving the config, and with it the secrets it names, needs `agents:write`; see [Variables and secrets](secrets.md) and [Permissions](../../reference/permissions.md). The full schema is in [Agent config](../../reference/agent-config.md).

The config is versioned like the profile: each change makes a new version, and a file identical to the current one makes none.

### From the CLI

```sh
seaport agents config show pm > pm.yaml   # the current config, in the format apply reads
seaport agents config apply pm -f pm.yaml # save it as a new version (- reads stdin)
seaport agents config versions pm
seaport agents config show pm -version 2  # print a past version
```

`config apply` rejects unknown keys.

### From the web app

Open the agent (**Agentes**, then the agent) and use its **Dónde corre** tab. It holds the harness and the machine's settings, and where you host the agent and see its slots. The model key and the other secrets the machine names are kept on the **Secretos** tab. Saving makes a new version; the exact controls may change between releases, and `seaport agents config show` always has the complete picture.

Saving a change to the machine makes running slots outdated: they restart once they are idle, and the image is built if it isn't yet. A change to the instructions or skills touches no container: the harness inside restarts with them once idle.

## Image builds

Each machine is built once into a Docker image, shared by every agent whose machine builds the same. The image starts `FROM` Seaport's base image for the harness, installs your apt packages, runs your `setup` script as root, then copies your harness home over `/home/agent`.

| Harness | Base image |
| :--- | :--- |
| `rness` | `ghcr.io/abraxas-365/seaport-rness` |
| `claude-code` | `ghcr.io/abraxas-365/seaport-claude-code` |

The tag is the release's version, or `latest` for a build that isn't a release. Point `SEAPORT_IMAGE_RNESS` or `SEAPORT_IMAGE_CLAUDE_CODE` at another image to override it.

A custom `image:` must derive from one of these. The base images carry the labels `io.seaport.base` and `io.seaport.harness`, a derived image inherits them, and Seaport refuses an image without both (the harness label must match the config's). Install tools as root, then return to user `1000:1000`.

Builds are keyed by what goes into the image: the harness, base image, generated Dockerfile, setup script and home. The image is named `seaport-build-<organization>:<digest>` and labeled with `io.seaport.organization` and `io.seaport.digest`. Agents whose machines build the same therefore share one image, an edit that leaves the image alone (instructions, skills, repos, network, resources) builds nothing, and a changed base, packages, setup or home makes a new one.

Build ahead of time so the first message doesn't wait for it:

```sh
seaport agents build pm               # follows the build log
seaport agents build pm -force        # build again and pull the base anew (a moving tag such as latest)
seaport agents build pm -wait=false   # start it and return
```

A build that stops reporting for a minute is treated as dead, and the next caller may start it again.

## Host an agent

Give the agent the secrets its machine names, save its config, then host it:

```sh
printf %s "$GIT_TOKEN" | seaport agents secrets set pm GIT_TOKEN
printf %s "$ANTHROPIC_API_KEY" | seaport agents secrets set pm ANTHROPIC_KEY
seaport agents config apply pm -f pm.yaml
seaport agents host pm -slots 2
seaport agents slots pm
```

`-slots` defaults to 1. Hosting needs a config with a harness and a machine, and a browser-on profile needs `machine.browser.engine`. An agent's secrets marked `-env` (`seaport agents secrets set pm NAME -env`) reach its slots as variables too.

A hosted agent works only through its slots, and an agent that people connect from their machines works only through crew tokens, so the two modes exclude each other:

- Hosting an agent that has live crew tokens is refused (`409`): revoke them first.
- Minting a crew token for a hosted agent is refused (`409`): stop hosting it first.

On the web, the agent's **Dónde corre** tab has one row per slot: state, **Arrancar** and **Detener** buttons, **Registro** for the container's output, and **Conectar cuenta** for [Claude subscriptions](claude-subscriptions.md). With a [browser](browser.md) on, the **Navegador** tab opens the slot's browser.

### Slot states

A slot moves through these states:

```text
stopped → starting → running → stopping → stopped
                 ↘ failed
```

```sh
seaport agents start pm-1        # slot 1 when you give just "pm"
seaport agents stop pm-1
seaport agents logs pm-1 -lines 500   # last lines, up to 2000 (default 300)
seaport agents unhost pm              # stop its slots first
```

Starting builds the machine's image if needed, mints a crew token for this run (the slot signs in with it, and it is revoked when the slot stops), and runs the container. A slot that fails to start shows why in `seaport agents slots`.

### The supervisor

You rarely start slots by hand. Every Seaport process runs a supervisor that checks every 15 seconds and when new work is queued:

- **Wakes** a hosted agent that has queued ships and no slot up. It starts the lowest stopped slot, or a failed one after 5 minutes, one at a time per agent.
- **Stops** a running slot that has been idle longer than `idle_stop`. A slot being [signed in](claude-subscriptions.md), or with a person in its [browser viewer](browser.md#the-viewer), isn't idle.
- **Restarts** an idle slot that is outdated: its machine is no longer the agent's current one (`seaport agents slots` marks it). Slots a change of instructions leaves alone.
- **Fails** a running slot whose container is gone or exited.
- **Cleans up** starts and stops that stalled, and containers that no slot row owns.

`idle_stop` takes whole minutes or hours between `1m` and `168h` (`30m`, `2h`). `0` or `never` keeps slots running. The default is `30m`. The next message wakes the agent again, and its conversations resume from the state volume.

## Workspace and repos

Repos are cloned into `/workspace` when a slot starts. If the workspace is persistent and the repo is already there, Seaport fetches instead and leaves your agent's work untouched. A failed `on_start` is logged and the start goes on.

Each thread (a conversation and the ones [linked](../linked-conversations.md) with it) gets its own git worktree, on a branch named `seaport/<thread>`. Two threads worked at once don't edit one tree. Without repos, every thread works in `/workspace` itself.

Git gets credentials from the port each time it asks: inside the slot, git's credential helper calls the port with the slot's token, and the port answers with the secret named by `credential`. A repository with no `credential` gets a token of the GitHub App of one of the agent's [GitHub docks](../github.md#act-on-github-as-the-app), when the App is installed there. No token is written to disk.

The base images ship GitHub's CLI. `gh` in a slot runs it with the App's token for the repository the command is about, so an agent of a GitHub dock can review pull requests, comment on lines and push branches as the App.

`workspace: persistent` (the default) keeps what the agent does on disk between runs of a slot: `/workspace` in a per-slot volume, and the working parts of its home (`~/Downloads`, `~/.cache`, `~/.local`, `~/.npm`, `~/.config`) in the slot's state volume. A report it wrote, a file you sent it, a tool it installed with `pip install --user` are there when the next message wakes the slot. `ephemeral` starts every run clean. Each prompt tells the agent which it is, and that `/tmp` is never kept. The harness's own configuration always comes from the image, and its sessions and sign-in live in the state volume in both modes, so a woken slot resumes its conversations either way. Volumes are per slot: with several slots, a conversation woken on another slot doesn't see the first one's files.

## Resources

Resources apply to each slot, not to the agent:

| Size | CPUs | Memory |
| :--- | :--- | :--- |
| `S` (default) | 1 | 2048 MB |
| `M` | 2 | 4096 MB |
| `L` | 4 | 8192 MB |

For something else, give `cpus` (0.25 to 64) and `memory_mb` (256 to 262144) instead of `size`. Don't combine them. Both are capped by `SEAPORT_HOST_MAX_CPUS` and `SEAPORT_HOST_MAX_MEMORY_MB`, and the port runs at most `SEAPORT_MAX_SLOTS` slots per organization.

## Next

- [Network](network.md): the model gateway and the egress proxy.
- [Harness homes](harness-homes.md): bring your own `~/.claude` or `~/.rness`.
- [Claude subscriptions](claude-subscriptions.md): sign slots in without an API key.
- [Browser](browser.md): a managed Chromium the agent drives and you sign in to.
- [Security model](../../explanation/security-model.md): how slots are hardened.
