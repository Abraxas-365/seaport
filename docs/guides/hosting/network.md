# Control what hosted agents reach

Keep your model key out of hosted containers and limit which hosts they can contact, with the port's model gateway and egress proxy.

A hosted agent runs code and reads text from strangers. If it also held your model key and could contact any host, one bad prompt could leak the key or send your data anywhere. Seaport narrows both: the key stays on the port, and the network leads only where you allow.

## Model gateway

Store the key as a secret of the environment and name it in the spec, one per provider:

```sh
printf %s "$ANTHROPIC_API_KEY" | seaport env secrets set dev ANTHROPIC_KEY
```

```yaml
models:
  anthropic: ${ANTHROPIC_KEY}
  openrouter: ${OPENROUTER_KEY}   # optional: more providers
```

The spec stores only the name. The secret belongs to the environment (every agent hosted on it uses it); a secret of the hosted agent with that name works too, for agents that need their own key. See [Variables and secrets](secrets.md).

With it, a slot never sees the key. For each provider named, Seaport gives the slot:

- `<PROVIDER>_BASE_URL` (`ANTHROPIC_BASE_URL`, `OPENROUTER_BASE_URL`…), pointing at the port's `/gateway/<provider>`.
- `<PROVIDER>_API_KEY`, set to the slot's own crew token (not your key).
- `SEAPORT_GATEWAY_PROVIDERS`, the providers served, comma separated.

The harness calls the provider through the port, authenticating with that token. The port checks it, forwards the call with the real key, and streams the answer back. Only slot tokens may use the gateway, and the port drops the caller's own authorization header before forwarding. Neither the agent's profile nor a settings file (including a repo's) can point the harness around the gateway: Seaport pins these settings after them.

The gateway forwards only:

| Provider | Protocol | Paths |
| :--- | :--- | :--- |
| `anthropic` | Anthropic | `/v1/messages` and `/v1/messages/count_tokens`, `/v1/models*`, `/v1/files*`. |
| `openai`, `openrouter`, `groq`, `deepseek` | OpenAI | `/chat/completions`, `/responses*`, `/embeddings`, `/models*` (the base URL already ends in the gateway's provider path, like each provider's own `…/v1`). |

Anything else is a 404. Errors come back in the provider's error shape.

`SEAPORT_GATEWAY_<PROVIDER>_URL` changes where the gateway forwards; see [Configuration](../../reference/configuration.md#hosting). The defaults are the providers' public APIs.

### Usage

The gateway counts tokens of every successful model call (Messages; Chat Completions, Responses and embeddings), per day (UTC), provider, model and agent. A streamed Chat Completions call is metered when it asks for usage (`stream_options.include_usage`, which the OpenAI SDKs do by default; Responses streams always carry it):

```sh
seaport agents usage pm -days 7
```

```text
DAY         PROVIDER   MODEL              REQUESTS  INPUT  OUTPUT  CACHE WRITE  CACHE READ
2026-01-05  anthropic  claude-example     12        41230  5120    0            38000
```

The same data is at `GET /api/v1/agents/{id}/usage?days=N` (30 days by default, needs `agents:read`) and in the web app, in **Uso de modelos** on the agent's page.

> [!NOTE]
> This applies to environments with `models`. A `claude-code` environment without `models.anthropic` signs in to a subscription instead and doesn't use the gateway. See [Claude subscriptions](claude-subscriptions.md).

## Egress proxy

The egress proxy decides which hosts slots may contact. It is off by default. Turn it on with a listen address:

```sh
SEAPORT_EGRESS_ADDR=:8741
```

It needs `token` or `iam` mode, and the deploy compose file sets it for you.

Slots get `HTTP_PROXY` and `HTTPS_PROXY` pointing at the proxy and carrying the slot's token as credentials. The port itself is excluded through `NO_PROXY`, so the harness still reaches it directly. Plain HTTP and `CONNECT` tunnels then reach only:

- the hosts in the environment's `network.egress`, and
- the hosts of its repos' URLs.

```yaml
network:
  egress:
    - registry.npmjs.org
    - "*.example.com"       # subdomains of example.com
```

Each entry is a host name with at least two labels. `*.example.com` matches subdomains, `example.com` the host itself. Up to 100 entries.

`*` alone allows any public host. Use it when an agent must search the web or read any page: rness's `web_search` (DuckDuckGo) and `web_fetch`, Claude Code's `WebFetch`, or the [browser](browser.md) on sites you can't list ahead of time. Claude Code's `WebSearch` runs inside Anthropic's API, so it goes through the model gateway and needs no egress entry. Even with `*`, the proxy still refuses [private addresses](#private-addresses), so slots can't reach your internal services.

```yaml
network:
  egress:
    - "*"                   # the open web; still no private addresses
```

The harnesses' web tools go through the proxy on their own: rness's base image always hands it `HTTPS_PROXY` as `rness.web.proxy`, and behind the proxy Claude Code's `WebFetch` skips its preflight call to `api.anthropic.com` (the port turns on `skipWebFetchPreflight` in its managed settings), so the hosts you list are the whole policy. That preflight is also Anthropic's check against known-bad domains. With `*`, an agent that reads untrusted content can be told to send data to any public host, so give `*` only to agents whose inputs you trust, or list hosts.

| Variable | Default | Meaning |
| :--- | :--- | :--- |
| `SEAPORT_EGRESS_ADDR` | off | Listen address, such as `:8741`. Must be `host:port`. |
| `SEAPORT_EGRESS_URL` | the agent URL's host with the proxy's port | How slots reach the proxy. Must be `http://`, without credentials. |
| `SEAPORT_EGRESS_PRIVATE` | `false` | Let allowed hosts resolve to private addresses. |

### It only works on an internal network

A proxy is a boundary only if slots can't go around it. Slots must run on a Docker network created with `--internal`, with no route to the internet. Then the proxy, which is on that network and also has outside access, is their only way out.

```sh
docker network create --internal seaport-slots
```

Set `SEAPORT_RUNTIME_NETWORK=seaport-slots` and attach the Seaport container to the same network. The [deploy compose file](../operations/deploy.md) does all of this. If the slots' network isn't internal, the port warns at start.

### Private addresses

The proxy never connects to loopback, link-local, private or carrier-grade NAT addresses, or to the other special-purpose ranges (documentation, benchmarking, reserved, and IPv6 forms such as NAT64 and 6to4 that embed one of these), even for an allowed host. This stops a slot from reaching your internal services through a DNS name that points inside. Set `SEAPORT_EGRESS_PRIVATE=true` only for local testing: together with `*` it opens every address the port can reach, cloud metadata endpoints included.

### What was refused

The proxy records each host it refused, per slot, with a count and the time it was last tried:

```sh
seaport agents egress pm
```

The same list is at `GET /api/v1/agents/{id}/hosting/egress` (needs `environments:read`), and the web app shows it under **Salidas bloqueadas** on the agent's page. When an agent needs a host, add it to `network.egress` and save a new version.

> [!TIP]
> A failing `npm install` or `git clone` inside a slot is usually a missing host. Check the denials first.

## Next

- [Run agents on the port](hosted-agents.md)
- [Security model](../../explanation/security-model.md)
- [Configuration](../../reference/configuration.md)
