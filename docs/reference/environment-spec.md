# Environment spec

Every field of an environment spec: where a hosted agent runs, with its harness, image, tools, repositories, network, resources and workspace, and the limits and checks the port applies.

An environment is versioned. Each change to its spec makes a new version, and the agents hosted on it run the current one. You edit environments in the web app (Entornos), with `seaport env apply -f env.yaml`, or with the API. Hosting needs `SEAPORT_RUNTIME=docker` on the port to build and run; see [Hosted agents](../guides/hosting/hosted-agents.md).

## Fields

| Field | Type | Default | Limit | Meaning |
| :--- | :--- | :--- | :--- | :--- |
| `name` | string | required | `^[a-z0-9][a-z0-9_-]{0,63}$` | Only in files and on create. Unique in the organization. `seaport env apply` keys on it. |
| `description` | string | empty | 1000 characters | A note for people. |
| `harness` | string | required | `rness` or `claude-code` | The agent program the slots run. |
| `image` | string | the harness's base image | 255 characters, an image reference | Replaces the base image. It must be built `FROM` Seaport's base image of the harness. |
| `packages` | list of string | empty | 100 | Debian packages installed in a derived image. |
| `setup` | string | empty | 16384 bytes, no NUL | A shell script run as root while the image is built. |
| `repos` | list of repo | empty | 10 | Git repositories cloned into the workspace. See [Repo](#repo). |
| `repo_config` | string | `trust` | `trust` or `ignore` | Whether the harness reads the repositories' own configuration. |
| `home` | string | empty | `sha256:` and 64 hex characters | The harness configuration baked into the image. See [Home](#home). |
| `models` | object | empty | | Model keys for the port's gateway. See [Models](#models). |
| `network` | object | empty | | What a slot may reach. See [Network](#network). |
| `resources` | object | size `S` | | CPUs and memory of a slot. See [Resources](#resources). |
| `workspace` | string | `persistent` | `persistent` or `ephemeral` | Whether `/workspace` is kept across restarts. |
| `idle_stop` | string | `30m` | `1m` to `168h`, whole minutes, or `0` | How long a slot may stay idle before it stops. |

The port refuses unknown fields and validates the whole spec before it stores anything. Text is trimmed.

### harness

- `rness`: the rness agent harness.
- `claude-code`: Claude Code.

A `claude-code` environment with no `models.anthropic` signs its slots in to a subscription. This is experimental; see [Claude subscriptions](../guides/hosting/claude-subscriptions.md).

### image

A reference like `registry.example.com/team/base:1.2` or `…@sha256:<64 hex>`. The base image carries the user, home and entrypoint the connector needs. The port refuses a base that lacks the labels `io.seaport.base` and the spec's harness when it builds. The default base is set by `SEAPORT_IMAGE_*`; see [Configuration](configuration.md).

### packages

Debian package names, lowercase, optionally pinned as `name=version`: `^[a-z0-9][a-z0-9+.-]{0,99}(=[A-Za-z0-9.+:~-]{1,100})?$`. Duplicates are removed.

### setup

A bash script that runs as root while the derived image is built, with `HOME=/home/agent`. The home is given back to uid 1000 afterwards. The script is stored and baked into the image: it must hold no secrets.

### repo_config

- `trust`: the harness reads the repositories' own `AGENTS.md`, `CLAUDE.md`, `.claude/` and `.rness/` configuration.
- `ignore`: it doesn't. rness runs without instruction files, and Claude Code reads user settings only.

### workspace

- `persistent`: `/workspace` lives in a volume that survives restarts of the slot.
- `ephemeral`: it doesn't.

### idle_stop

`"30m"`, `"2h"`, `"1h30m"`. A slot with no work for that long stops, and new work starts it again. `0` (or `never`) never stops it. The value must be whole minutes between one minute and 168 hours (7 days). The port stores it in canonical form.

## Repo

A git repository cloned into the workspace at start.

| Field | Type | Default | Limit | Meaning |
| :--- | :--- | :--- | :--- | :--- |
| `url` | string | required | `https`, with a host and a path, no user info, query or fragment, at most 2048 characters | The repository. |
| `ref` | string | the default branch | `^[A-Za-z0-9][A-Za-z0-9._/-]{0,199}$`, no `..` | A branch or tag. |
| `path` | string | the repository's name without `.git` | relative, no `.`-leading parts or `..`, characters `A-Za-z0-9._-` and `/`; not `wt` | Where it goes, relative to `/workspace`. |
| `credential` | string | empty (public) | `${NAME}` | An agent secret that holds a token for the URL's host. |
| `on_start` | string | empty | 4096 bytes, no NUL | A shell command run in the repository after each clone or fetch. |

- Two repositories can't go to the same `path`; set `path` on one.
- `wt` is Seaport's: each conversation gets a worktree under `wt/`.
- `credential` names a secret of the agent that is hosted on the environment, in the form `${NAME}`. The secret must exist when you host the agent and when you save the spec, and it can't be deleted while a spec uses it. The credential never goes in the spec as a value. Git asks the port for it each time.

## Models

| Field | Type | Default | Meaning |
| :--- | :--- | :--- | :--- |
| `anthropic` | string | empty | An agent secret that holds an Anthropic API key, as `${NAME}`. |

With a key set, the slots reach the provider through the port's model gateway (`/gateway/anthropic/`) with their own slot token. The key never enters the container. The gateway meters the usage per agent; read it with `GET /api/v1/agents/{id}/usage`. See [Network and models](../guides/hosting/network.md).

## Network

| Field | Type | Default | Limit | Meaning |
| :--- | :--- | :--- | :--- | :--- |
| `egress` | list of string | empty | 100 | Hosts a slot may reach through the egress proxy. |

- Each host is a DNS name of at least two labels, such as `example.com`, or `*.example.com` for any subdomain. Hosts are lowercased and deduplicated.
- The hosts of the `repos` are added on their own. A `claude-code` environment that signs in to a subscription also gets `api.anthropic.com` and `platform.claude.com`.
- The proxy filters only when the port runs it (`SEAPORT_EGRESS_ADDR`) and the runtime network is internal. See [Network and models](../guides/hosting/network.md) and [Configuration](configuration.md).

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
- With `seaport env apply`, `home` may be a directory (relative to the file): the command uploads it and uses its digest.
- Upload and rules are in [Harness homes](../guides/hosting/harness-homes.md). The upload limits are in [Limits](limits.md#environments-and-homes).

## Versions and limits

- Create with `POST /api/v1/environments` and the body `{"name": "…", "spec": {…}}`. The answer is `201` with the environment (version 1).
- Save with `PUT /api/v1/environments/{id}` and the body `{"base": N, "spec": {…}}`. `base` is the current version and must be 1 or more. A stale base fails with `409 CONFLICT`. A spec that normalizes equal to the current one makes no new version.
- `{id}` is an environment id or its name.
- Each organization may have 50 environments (`hosting.MaxEnvironments`); past that the port answers `422 QUOTA_EXCEEDED` (`environments`). See [Limits](limits.md).
- An environment that has hosted agents can't be deleted.
- A spec that names credentials or model keys needs `seaport:agents:write` besides `seaport:environments:write`. See [Permissions](permissions.md).
- The API stores a complete spec: the defaults above are filled in before it is stored. Routes are in [HTTP API](http-api.md#hosting).
- Each version is built once into an image. The first start of a new version builds it, or start it from `POST /api/v1/environments/{id}/build`.

## Example

A file for `seaport env apply -f env.yaml`:

```yaml
name: dev
description: Reviews pull requests for Example Co.
harness: claude-code
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
models:
  anthropic: ${ANTHROPIC_KEY}
network:
  egress:
    - registry.npmjs.org
    - "*.example.com"
resources:
  size: M
workspace: persistent
idle_stop: 1h
```

The same spec as the JSON body of an API create:

```json
{
  "name": "dev",
  "spec": {
    "harness": "claude-code",
    "repos": [{ "url": "https://git.example.com/example/app.git", "ref": "main", "path": "app", "credential": "${GIT_TOKEN}" }],
    "models": { "anthropic": "${ANTHROPIC_KEY}" },
    "network": { "egress": ["registry.npmjs.org"] },
    "resources": { "size": "M" },
    "idle_stop": "1h"
  }
}
```

`seaport env apply` rejects an unknown key in the file, so a typo doesn't silently drop a setting. `seaport env export <env>` prints a stored spec in the same format. See [CLI](cli.md#environments).
