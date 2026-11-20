# Agent config

The whole configuration of an agent as one versioned document: its harness, what the harness is told and given, and, for an agent the port hosts, the machine it runs on.

An agent's config has three parts:

| Part | Fields | Used by |
| :--- | :--- | :--- |
| Harness | `harness` | Picks the base image of a hosted agent. A remote agent runs whatever command you give `seaport connect`; the harness is recorded for it. |
| Profile | `instructions`, `skills`, `mcp_servers`, `approve`, `model`, `effort`, `browser`, `browser_assist`, `browser_detect`, `browser_ignore_hosts` | Both modes. Every field is in [Agent profile](agent-profile.md). |
| Machine | `machine` | Hosted agents only: image, tools, repositories, model access, network, resources and workspace. See [Machine](#machine). |

Each change makes a new, immutable version, and the agent runs its current one. You edit the config in the web app (the agent's page: **Configuración** and **Dónde corre**), with `seaport agents config apply -f agent.yaml`, or with the API (`PUT /api/v1/agents/{id}/config`). Hosting needs `SEAPORT_RUNTIME=docker` on the port to build and run; see [Hosted agents](../guides/hosting/hosted-agents.md).

## What a change does

- **A profile change** (instructions, skills, MCP servers, model…) changes the agent's kit. A connected harness, hosted or remote, drains and restarts itself with it once idle. A hosted slot's container is not restarted.
- **A machine or harness change** changes the machine digest: the hash of the normalized harness and machine. A running slot whose digest isn't the agent's current one is marked `outdated`. The supervisor builds the new image in the background and restarts the slot once the image is ready and the slot idle. Two agents whose machines build the same image share the build.
- **A change that leaves the config as it was** (the same normalized document) makes no version.

## The config file

`seaport agents config show <agent>` prints the current config as YAML, and `seaport agents config apply <agent> -f agent.yaml` saves one (`-f -` reads stdin; JSON also works). The file holds the whole config: a field you leave out is saved as empty, so start from `show`. The file is read strictly: an unknown key, at any level, is an error, so a typo doesn't silently drop a setting. See [CLI](cli.md#config).

| Key | Type | Default | Limit | Meaning |
| :--- | :--- | :--- | :--- | :--- |
| `harness` | string | empty | `rness`, `claude-code` or empty | The agent program. Required when there is a `machine` or the agent is hosted. |
| `instructions` | string | empty | 32768 characters | See [Agent profile](agent-profile.md#fields). |
| `skills` | list | empty | 50 | Each `name`, `description`, `body`. |
| `mcp_servers` | list | empty | 20 | Each `name`, `url`, `headers` (list of `name`, `value`). |
| `approve` | string | empty | `allow`, `reject` or empty | |
| `model` | string | empty | one word | |
| `effort` | string | empty | one word | |
| `browser` | boolean | `false` | | The agent uses the managed browser of its machine. |
| `browser_assist` | string | empty | empty, `conversation` or a destination name | Needs `browser`. |
| `browser_detect` | boolean | `false` | | Needs `browser`. |
| `browser_ignore_hosts` | list of string | empty | 32 | Needs `browser`. |
| `machine` | object | none | 256 KiB as JSON (`agent.MaxMachine`) | Where a hosted agent runs. See [Machine](#machine). |

A `machine` needs a `harness`. An agent the port hosts must keep both: the port refuses a save that drops them while it is hosted, and a save of `browser: true` whose machine has no browser engine.

```yaml
harness: claude-code
instructions: |
  You review pull requests for Example Co. Be concise.
approve: allow
model: sonnet
browser: true
machine:
  packages:
    - jq
    - ripgrep
  setup: |
    npm install -g typescript
  repos:
    - url: https://git.example.com/example/app.git
      ref: main
      path: app
      credential: ${GIT_TOKEN}
      on_start: npm ci
  repo_config: trust
  home: ./claude-home
  env:
    TZ: America/Lima
  models:
    anthropic: ${ANTHROPIC_KEY}
  network:
    egress:
      - registry.npmjs.org
      - "*.example.com"
  browser:
    engine: chromium
  resources:
    size: M
  workspace: persistent
  idle_stop: 1h
```

`machine.home: ./claude-home` is a directory, relative to the file: `config apply` uploads it first and saves its digest. Anything that starts with `sha256:` is taken as a digest already uploaded. See [Home](#home).

`config show` prints the resolved config, so `resources` has the size and no CPU or memory when a size is set, and the defaults the port fills in appear (`repo_config`, `workspace`, `idle_stop`, `models.anthropic: subscription` where it applies).

## Machine

Every field of `machine`. All of them are optional: the port fills the defaults in before it stores the machine, so a stored machine is complete, and two machines that run the same normalize to the same digest. The port refuses unknown fields and validates the whole machine before it stores anything. Text is trimmed.

| Field | Type | Default | Limit | Meaning |
| :--- | :--- | :--- | :--- | :--- |
| `image` | string | the harness's base image | 255 characters, an image reference | Replaces the base image. It must be built `FROM` Seaport's base image of the harness. |
| `packages` | list of string | empty | 100 | Debian packages installed in a derived image. |
| `setup` | string | empty | 16384 bytes, no NUL | A shell script run as root while the image is built. |
| `repos` | list of repo | empty | 10 | Git repositories cloned into the workspace. See [Repo](#repo). |
| `repo_config` | string | `trust` | `trust` or `ignore` | Whether the harness reads the repositories' own configuration. |
| `home` | string | empty | `sha256:` and 64 hex characters | The harness configuration baked into the image. See [Home](#home). |
| `models` | object | see [Models](#models) | | How the slots reach each model provider. |
| `env` | object | empty | 50 variables, values one line of at most 4096 bytes | Plain environment variables of the harness. See [Env](#env). |
| `network` | object | empty | | What a slot may reach. See [Network](#network). |
| `browser` | object | none | | A managed browser for the slots. See [Browser](#browser). |
| `resources` | object | size `S` | | CPUs and memory of a slot. See [Resources](#resources). |
| `workspace` | string | `persistent` | `persistent` or `ephemeral` | Whether what the agent keeps on disk survives between runs. |
| `idle_stop` | string | `30m` | `1m` to `168h`, whole minutes, or `0` | How long a slot may stay idle before it stops. |

### harness

`harness` is a field of the config, not of the machine, because it is what the agent is in both modes.

- `rness`: the rness agent harness.
- `claude-code`: Claude Code.

A hosted agent runs Seaport's base image of its harness. A remote agent (`seaport connect`) runs the command you give it, whatever it is; keep the config's harness in step with it. See [Connector](../guides/connector.md).

### image

A reference like `registry.example.com/team/base:1.2` or `…@sha256:<64 hex>`. The base image carries the user, home and entrypoint the connector needs. The port refuses a base that lacks the labels `io.seaport.base` and the config's harness when it builds. The default base is set by `SEAPORT_IMAGE_*`; see [Configuration](configuration.md).

### packages

Debian package names, lowercase, optionally pinned as `name=version`: `^[a-z0-9][a-z0-9+.-]{0,99}(=[A-Za-z0-9.+:~-]{1,100})?$`. Duplicates are removed.

### setup

A bash script that runs as root while the derived image is built, with `HOME=/home/agent`. The home is given back to uid 1000 afterwards. The script is stored and baked into the image: it must hold no secrets.

### repo_config

- `trust`: the harness reads the repositories' own `AGENTS.md`, `CLAUDE.md`, `.claude/` and `.rness/` configuration.
- `ignore`: it doesn't. rness runs without instruction files, and Claude Code reads user settings only.

### workspace

What the agent does and keeps on disk between runs of a slot (a slot that stops after `idle_stop` and wakes for the next message):

- `persistent` (the default): `/workspace` (repositories, each conversation's worktree, the files the agent creates there, the attachments it is handed) and the working parts of its home (`~/Downloads`, `~/.cache`, `~/.local`, `~/.npm`, `~/.config`) live in the slot's volumes and are there again at the next run. Each prompt tells the agent so.
- `ephemeral`: every run starts clean; the agent is told nothing persists.

In both, the harness's configuration comes from the image (a new image replaces it), its sessions and sign-in live in the slot's state volume (a woken slot resumes its conversations either way), and `/tmp` is never kept. Volumes are per slot: an agent with several slots doesn't see one slot's files from another.

### idle_stop

`"30m"`, `"2h"`, `"1h30m"`. A slot with no work for that long stops, and new work starts it again. `0` (or `never`) never stops it. The value must be whole minutes between one minute and 168 hours (7 days). The port stores it in canonical form.

## Repo

A git repository cloned into the workspace at start.

| Field | Type | Default | Limit | Meaning |
| :--- | :--- | :--- | :--- | :--- |
| `url` | string | required | `https`, with a host and a path, no user info, query or fragment, at most 2048 characters | The repository. |
| `ref` | string | the default branch | `^[A-Za-z0-9][A-Za-z0-9._/-]{0,199}$`, no `..` | A branch or tag. |
| `path` | string | the repository's name without `.git` | relative, no `.`-leading parts or `..`, characters `A-Za-z0-9._-` and `/`; not `wt` | Where it goes, relative to `/workspace`. |
| `credential` | string | empty (public) | `${NAME}` | A secret of the agent that holds a token for the URL's host. |
| `on_start` | string | empty | 4096 bytes, no NUL | A shell command run in the repository after each clone or fetch. |

- Two repositories can't go to the same `path`; set `path` on one.
- `wt` is Seaport's: each conversation gets a worktree under `wt/`.
- `credential` names one of the agent's [secrets](../guides/hosting/secrets.md) in the form `${NAME}`. The secret must exist when you save the config, and it can't be deleted while the machine uses it. The credential never goes in the config as a value. Git asks the port for it each time.

## Env

Plain, non-secret variables every slot's harness gets (a time zone, a feature flag, a region). Names are environment variable names (`A-Z`, `0-9`, `_`; not `SEAPORT_…`), and none of the slot's own network variables (`ANTHROPIC_BASE_URL`, `ANTHROPIC_API_KEY`, the other providers', `HTTPS_PROXY`…). A value that looks like a credential is refused: that is a secret, set with `seaport agents secrets set <agent> NAME -env`. What a slot's harness sees, lowest precedence first: `env`, the agent's `-env` secrets, the port's own slot variables.

```yaml
machine:
  env:
    TZ: America/Lima
    FEATURE_FLAGS: beta
```

## Models

`models` says, per provider, how the slots reach it: a key, as `${NAME}` naming one of the agent's secrets, or `subscription`, which signs the slot in to a Claude account. Providers:

| Field | Protocol | What the slot gets |
| :--- | :--- | :--- |
| `anthropic` | Anthropic | `ANTHROPIC_BASE_URL`, `ANTHROPIC_API_KEY` |
| `openai` | OpenAI | `OPENAI_BASE_URL`, `OPENAI_API_KEY` |
| `openrouter` | OpenAI | `OPENROUTER_BASE_URL`, `OPENROUTER_API_KEY` |
| `groq` | OpenAI | `GROQ_BASE_URL`, `GROQ_API_KEY` |
| `deepseek` | OpenAI | `DEEPSEEK_BASE_URL`, `DEEPSEEK_API_KEY` |

- `subscription` is for `anthropic` only. It is experimental; see [Claude subscriptions](../guides/hosting/claude-subscriptions.md).
- Access is explicit, and the port fills the default in: a `claude-code` machine with no `models.anthropic` gets `anthropic: subscription`, and an `rness` machine with no `models` at all gets it too. `anthropic: subscription` mixes with other providers' keys: the slot signs in to Anthropic and reaches the keyed providers through the gateway (`rness` uses them all; Claude Code only Anthropic). Such a slot also gets `SEAPORT_SIGN_IN=anthropic`.
- With a key set, the slots reach the provider through the port's model gateway (`/gateway/<provider>/`) with their own slot token as the key: the base URL points at the gateway and the key variable holds the slot token, so the real key never enters the container. `SEAPORT_GATEWAY_PROVIDERS` lists the providers served. The gateway meters the usage per agent; read it with `GET /api/v1/agents/{id}/usage`. A `rness` slot works with any of them; Claude Code uses only `anthropic`. For how a slot picks its model, see [Which model a slot uses](../guides/hosting/network.md#which-model-a-slot-uses).

```yaml
machine:
  models:
    anthropic: ${ANTHROPIC_KEY}
    openrouter: ${OPENROUTER_KEY}
```

## Network

| Field | Type | Default | Limit | Meaning |
| :--- | :--- | :--- | :--- | :--- |
| `egress` | list of string | empty | 100 | Hosts a slot may reach through the egress proxy. |

- Each host is a DNS name of at least two labels, such as `example.com`, or `*.example.com` for any subdomain. Hosts are lowercased and deduplicated.
- `*` alone allows any public host (web search, any page). Private, loopback, link-local and carrier-grade NAT addresses stay refused. See [Network](../guides/hosting/network.md#egress-proxy).
- The hosts of the `repos` are added on their own. A machine that signs in to a subscription also gets `api.anthropic.com` and `platform.claude.com`.
- The proxy filters only when the port runs it (`SEAPORT_EGRESS_ADDR`) and the runtime network is internal. See [Network and models](../guides/hosting/network.md) and [Configuration](configuration.md).

## Browser

| Field | Type | Default | Limit | Meaning |
| :--- | :--- | :--- | :--- | :--- |
| `engine` | string | empty | `chromium` or empty | The browser the slots run. Empty: none. |

With `chromium`, the build installs Chromium, Xvfb, Node and the browser MCP server in the image, and each slot of an agent whose config has `browser: true` runs it beside the harness: headed on a virtual display (Xvfb), CDP on loopback, profile in the slot's state volume. The sites it visits go through the egress proxy, so list them in `network.egress`. See [Browser](../guides/hosting/browser.md).

## Resources

Set a preset size, or CPUs and memory, not both.

| Field | Type | Default | Limit | Meaning |
| :--- | :--- | :--- | :--- | :--- |
| `size` | string | `S` | `S`, `M` or `L` | A preset. |
| `cpus` | number | from the size | 0.25 to 64 | CPUs of a slot. |
| `memory_mb` | integer | from the size | 256 to 262144 | Memory of a slot, in MB. |

| Size | CPUs | Memory |
| :--- | :--- | :--- |
| `S` | 1 | 2048 MB |
| `M` | 2 | 4096 MB |
| `L` | 4 | 8192 MB |

- Without `size`, `cpus` and `memory_mb`, the size is `S`.
- The port also refuses resources above the operator's maxima: `SEAPORT_HOST_MAX_CPUS` (default 4) and `SEAPORT_HOST_MAX_MEMORY_MB` (default 8192). `0` turns a maximum off. See [Configuration](configuration.md).
- The port stores the resolved CPUs and memory, and the size when you set one.

## Home

`home` names an uploaded copy of the harness's user configuration (`~/.claude` or `~/.rness`): instructions, settings, agents, skills, plugins. The port bakes it into the image.

- In the API the value is a digest: `sha256:<64 hex>`.
- With `seaport agents config apply`, `home` may be a directory (relative to the file): the command uploads it and uses its digest.
- The home must be for the config's harness.
- Upload and rules are in [Harness homes](../guides/hosting/harness-homes.md). The upload limits are in [Limits](limits.md#machines-and-homes).

## Versions and the API

- Read the current config with `GET /api/v1/agents/{id}/config`: the profile fields plus `harness`, `machine` (the normalized machine, or `null`), `machine_digest` and `machine_secrets` (the secrets the machine names). An agent with no config yet answers version `0`.
- Save with `PUT /api/v1/agents/{id}/config` and the whole config plus `base`, the version you edited from (`0` for an agent with no config). A stale base fails with `409 CONFLICT`. A config that normalizes equal to the current one makes no new version.
- List versions with `GET /api/v1/agents/{id}/config/versions` and read one with `GET /api/v1/agents/{id}/config/versions/{version}`.
- A machine that names a secret the agent doesn't have, or a home never uploaded, is refused. A machine that names a secret keeps it from being deleted.
- Saving needs `seaport:agents:write`, reading `seaport:agents:read`. Builds, hosting and slots use `seaport:hosting:read` and `seaport:hosting:write`; see [Permissions](permissions.md).
- The API stores a complete machine: the defaults above are filled in before it is stored. Routes are in [HTTP API](http-api.md#agents).
- Each distinct image is built once, keyed by its content. The first start builds it, or build it ahead with `POST /api/v1/agents/{id}/build`. See [HTTP API](http-api.md#hosting).

The same config as the JSON body of a save:

```json
{
  "base": 4,
  "harness": "claude-code",
  "instructions": "You review pull requests for Example Co.",
  "browser": false,
  "machine": {
    "repos": [{ "url": "https://git.example.com/example/app.git", "ref": "main", "path": "app", "credential": "${GIT_TOKEN}" }],
    "models": { "anthropic": "${ANTHROPIC_KEY}" },
    "network": { "egress": ["registry.npmjs.org"] },
    "resources": { "size": "M" },
    "idle_stop": "1h"
  }
}
```
