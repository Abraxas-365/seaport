# Control what hosted agents reach

Keep your model key out of hosted containers and limit which hosts they can contact, with the port's model gateway and egress proxy.

A hosted agent runs code and reads text from strangers. If it also held your Anthropic key and could contact any host, one bad prompt could leak the key or send your data anywhere. Seaport narrows both: the key stays on the port, and the network leads only where you allow.

## Model gateway

Name the secret that holds your key in the environment:

```yaml
models:
  anthropic: ${ANTHROPIC_API_KEY}
```

The name is a secret of the hosted agent, created with `seaport agents secrets set <agent> ANTHROPIC_API_KEY`. The spec stores only the name.

With it, a slot never sees the key. Seaport gives the slot:

- `ANTHROPIC_BASE_URL`, pointing at the port's `/gateway/anthropic`.
- `ANTHROPIC_API_KEY`, set to the slot's own crew token (not your key).

The harness calls Anthropic through the port, authenticating with that token. The port checks it, forwards the call with the real key, and streams the answer back. Only slot tokens may use the gateway, and the port drops the caller's own authorization header before forwarding. Neither the agent's profile nor a settings file (including a repo's) can point the harness around the gateway: Seaport pins these settings after them.

The gateway forwards only:

| Path | Purpose |
| :--- | :--- |
| `/v1/messages` and `/v1/messages/count_tokens` | Model calls. |
| `/v1/models*` | Model lists. |
| `/v1/files*` | Files. |

Anything else is a 404. Errors come back in Anthropic's error shape.

`SEAPORT_GATEWAY_ANTHROPIC_URL` changes where the gateway forwards. It defaults to `https://api.anthropic.com`.

### Usage

The gateway counts tokens of every successful Messages call, per day (UTC), model and agent:

```sh
seaport agents usage pm -days 7
```

```text
DAY         MODEL              REQUESTS  INPUT  OUTPUT  CACHE WRITE  CACHE READ
2026-01-05  claude-example     12        41230  5120    0            38000
```

The same data is at `GET /api/v1/agents/{id}/usage?days=N` (30 days by default, needs `agents:read`) and in the web app, in **Uso de modelos** on the agent's page.

> [!NOTE]
> This applies to environments with `models.anthropic`. A `claude-code` environment without it signs in to a subscription instead and doesn't use the gateway. See [Claude subscriptions](claude-subscriptions.md).

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

The proxy never connects to loopback, link-local, private or carrier-grade NAT addresses, even for an allowed host. This stops a slot from reaching your internal services through a DNS name that points inside. Set `SEAPORT_EGRESS_PRIVATE=true` only for local testing.

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
