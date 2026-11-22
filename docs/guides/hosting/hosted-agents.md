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
  concurrency: 2              # conversations each slot works at once; the rest wait
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
| `concurrency` | How many conversations each slot works at once, `1` to `32` (default `2`). See [Conversations at once](#conversations-at-once). |

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

Saving a change to the machine makes running slots outdated. The supervisor builds the new image in the background, and each slot restarts on it once the image is ready and the slot is idle. If that build fails, the slots keep running on the old machine (and still stop after `idle_stop`); `seaport agents build <agent>` shows why. A change to the instructions or skills touches no container: the harness inside restarts with them once idle.

## Image builds

Each machine is built once into a Docker image, shared by every agent whose machine builds the same. The image starts `FROM` Seaport's base image for the harness, installs your apt packages, runs your `setup` script as root, then copies your harness home over `/home/agent`.

| Harness | Base image |
| :--- | :--- |
| `rness` | `ghcr.io/abraxas-365/seaport-rness` |
| `claude-code` | `ghcr.io/abraxas-365/seaport-claude-code` |

The tag is the release's version, or `latest` for a build that isn't a release. Point `SEAPORT_IMAGE_RNESS` or `SEAPORT_IMAGE_CLAUDE_CODE` at another image to override it.

A custom `image:` must derive from one of these. The base images carry the labels `io.seaport.base` and `io.seaport.harness`, a derived image inherits them, and Seaport refuses an image without both (the harness label must match the config's). Install tools as root, then return to user `1000:1000`.

Builds are keyed by what goes into the image: the harness, base image, generated Dockerfile, setup script and home. The image is named `seaport-build-<organization>:<digest>` and labeled with `io.seaport.organization` and `io.seaport.digest`. Agents whose machines build the same therefore share one image, an edit that leaves the image alone (instructions, skills, repos, network, resources) builds nothing, and a changed base, packages, setup or home makes a new one.

You don't need to build by hand. The supervisor builds the image of every hosted agent's current config in the background, one build at a time per process, as soon as it sees one missing: after a machine change, after hosting an agent, and after a Seaport upgrade (a new base image means new images for every agent). A build it started that failed is tried again 30 minutes later, or at once by a slot start or `seaport agents build`.

While a new image builds, a slot that starts runs on the image its agent last ran on the same machine, if the engine still has it, and restarts on the new one once that is ready and the slot idle. With no such image (a new machine, or a pruned one), the start waits for the build.

To build now, or follow a build:

```sh
seaport agents build pm               # follows the build log
seaport agents build pm -force        # build again and pull the base anew (a moving tag such as latest)
seaport agents build pm -wait=false   # start it and return
```

A forced build that makes a different image marks running slots outdated, so they restart on it once idle. A build that stops reporting for a minute is treated as dead, and the next caller may start it again.

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

On the web, the agent's **Dónde corre** tab has one row per slot: state, **Arrancar** and **Detener** buttons, and **Registro** for the container's output. A slot's account for [Claude subscriptions](claude-subscriptions.md) is signed in from **Configuración** → **Acceso a la IA**. With a [browser](browser.md) on, the **Navegador** tab opens the slot's browser.

### Clone a hosted agent

`seaport agents clone pm pm-2 [-secrets]` copies the config (harness and machine included) into a new agent and hosts it with the same number of slots, all stopped. Without `-secrets` the clone has none of the secrets its machine names (repo credentials, model keys), and starting its slots is refused (`422`) naming the missing ones until you set them. If hosting the clone fails (for example the organization's slot limit), the error is returned and no clone is kept. See [Clone an agent](../agents-and-routing.md#clone-an-agent).

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

Starting uses the agent's image, or its previous one while a new image builds (see [Image builds](#image-builds)), and builds it first when there is neither. It then mints a crew token for this run (the slot signs in with it, and it is revoked when the slot stops), and runs the container. A slot that fails to start shows why in `seaport agents slots`.

### The supervisor

You rarely start slots by hand. Every Seaport process runs a supervisor that checks every 15 seconds and when new work is queued:

- **Builds** the image each hosted agent's current config needs, in the background, before any message asks for it.
- **Wakes** a hosted agent that has queued ships and no slot up. It starts the lowest stopped slot, or a failed one after 5 minutes, one at a time per agent. With [autoscale](#autoscale) it also starts another slot when conversations wait with every running slot full.
- **Stops** a running slot that has been idle longer than `idle_stop` (an extra slot autoscale started: after 5 minutes). A slot being [signed in](claude-subscriptions.md), or with a person in its [browser viewer](browser.md#the-viewer), isn't idle.
- **Restarts** an idle slot that is outdated, once the agent's image is built: its machine is no longer the agent's current one, or it runs another image than the agent's current build (after an upgrade or a forced build). `seaport agents slots` marks it. Slots a change of instructions leaves alone.
- **Fails** a running slot whose container is gone or exited.
- **Cleans up** starts and stops that stalled, and containers that no slot row owns.

`idle_stop` takes whole minutes or hours between `1m` and `168h` (`30m`, `2h`). `0` or `never` keeps slots running. The default is `30m`. The next message wakes the agent again, and its conversations resume from the state volume.

## Conversations at once

`concurrency` is how many conversations each slot works at the same time. The default is `2`, the limit `32`.

Say five people write to a WhatsApp dock at once and the agent has one slot with `concurrency: 2`:

1. The agent answers two of them in parallel, each conversation in its own harness session.
2. The other three wait in the queue, oldest first. Nothing is lost and nobody gets an error: they just get their answer later.
3. When one of the two finishes its turn, the next waiting conversation starts.

A new message in a conversation the agent is already working on doesn't take another place: it goes into the running turn (see [Steer or queue](../connector.md#steer-or-queue)). A thread of [linked conversations](../linked-conversations.md) is one conversation here.

Every conversation of a slot runs in the same container, with one harness process, so they share:

- **CPU and memory.** Each turn runs tools, builds, tests; more of them at once make each one slower. As a guide, size `S` takes 2 comfortably, `M` 4 and `L` 8.
- **The browser.** A slot has one [managed browser](browser.md). Two conversations using it at the same time step on each other's tabs.
- **The workspace.** With repos, each conversation has its own worktree. Without them, all of them work in `/workspace` itself.
- **Model limits.** Every turn calls the model; a Claude subscription's limits are reached sooner.

To answer more people at once, raise `concurrency` (within what the size takes) or give the agent more slots (`seaport agents host pm -slots 2`; each slot adds its own `concurrency`, with its own sessions and volumes) and turn on [autoscale](#autoscale). A changed `concurrency` makes the running slots outdated: each one restarts with it once idle, on the same image (nothing is rebuilt).

The **Dónde corre** tab shows how many conversations the agent is working on and how many are waiting, next to how many it can take at once.

### Autoscale

On its own the supervisor wakes one slot per agent; the others start only when you start them. With autoscale on, the extra slots follow the queue:

```bash
seaport agents autoscale pm on
```

- **Up.** When a conversation has waited a minute and every running slot works as many conversations as its `concurrency` allows, the next stopped slot starts (one start at a time, up to the agent's slot count). A conversation waiting for another reason, such as an older message owed to another agent, starts nothing.
- **Down.** An extra slot that has had no conversation for 5 minutes stops. The agent's lowest running slot is not an extra: it follows `idle_stop` as usual.
- **Sign-in.** A slot of a machine that [signs in to a Claude subscription](claude-subscriptions.md) needs a person the first time, so autoscale starts it only once it has been signed in, or when the agent has the harness's fallback secret (`CLAUDE_CODE_OAUTH_TOKEN` for Claude Code, `ANTHROPIC_API_KEY` for rness, with `env` on). Sign in each slot once and autoscale can use it from then on.

The switch is also on the **Dónde corre** tab (**Encender más instancias cuando hay cola**) and in the API (`PATCH /api/v1/agents/{id}/hosting` with `{"autoscale": true}`).

## Workspace and repos

Repos are cloned into `/workspace` when a slot starts. If the workspace is persistent and the repo is already there, Seaport fetches instead and leaves your agent's work untouched. A failed `on_start` is logged and the start goes on.

Each thread (a conversation and the ones [linked](../linked-conversations.md) with it) gets its own git worktree, on a branch named `seaport/<thread>`. Two threads worked at once don't edit one tree. Without repos, every thread works in `/workspace` itself.

Git gets credentials from the port each time it asks: inside the slot, git's credential helper calls the port with the slot's token, and the port answers with the secret named by `credential`. A repository with no `credential` gets a token of the GitHub App of one of the agent's [GitHub docks](../github.md#act-on-github-as-the-app), when the App is installed there. No token is written to disk.

The base images ship GitHub's CLI. `gh` in a slot runs it with the App's token for the repository the command is about, so an agent of a GitHub dock can review pull requests, comment on lines and push branches as the App.

`workspace: persistent` (the default) keeps what the agent does on disk between runs of a slot: `/workspace` in a per-slot volume, and the working parts of its home (`~/Downloads`, `~/.cache`, `~/.local`, `~/.npm`, `~/.config`) in the slot's state volume. A report it wrote, a file you sent it, a tool it installed with `pip install --user` are there when the next message wakes the slot. `ephemeral` starts every run clean. Each prompt tells the agent which it is, and that `/tmp` is never kept. The harness's own configuration always comes from the image, and its sessions and sign-in live in the state volume in both modes, so a woken slot resumes its conversations either way. For rness that includes its memory and its image store (`~/.rness/images`), where a session keeps the images it saw: screenshots and images sent to it. Volumes are per slot: with several slots, a conversation woken on another slot doesn't see the first one's files.

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
