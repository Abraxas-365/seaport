# Architecture

How Seaport is laid out for contributors: the hexagonal package structure, the composition root, the architecture tests that keep it honest, and how several processes coordinate through Postgres.

Seaport is one Go binary (`seaport`) that is both the **port** (`seaport serve`) and the agent-side **connector** (`seaport connect`). The port keeps all state in Postgres. The engineering invariants behind every rule here live in [AGENTS.md](../AGENTS.md); this page is the map.

## Package layout

Everything lives under `internal/`. Each package has one role, and the role decides what it may import.

| Role | Packages | May import |
| :--- | :--- | :--- |
| **Foundation** | `errx`, `identity`, `query`, `text`, `config`, `httpx`, `pgerr`, `seal`, `pglisten`, `pglock`, `blob` (+ `gocloudblob` adapter), `permission`, `event` (+ `eventsse`, `eventpg`) | other foundation only, never a domain |
| **Domain** | `dock`, `conversation`, `ship`, `crew`, `agent`, `hosting`, `auth` | foundation and other domains' root packages |
| **Composition root** | `bootstrap` | everything |
| **Client** | `connector` | foundation and domain roots (wire types) only |

What each domain owns:

| Domain | Owns |
| :--- | :--- |
| `dock` | channel accounts (WhatsApp, `api`, GitHub, Jira, Slack), gateways, dock ownership across processes |
| `conversation` | chats and customs (`Decide`, `Admit`: who may wake an agent) |
| `ship` | messages, the delivery queue, attachments, outbound limits |
| `crew` | the wire protocol, crew tokens, presence of connected agents, the MCP tools |
| `agent` | the agent library, dock teams, routing (`Route`), profiles, secrets, kits |
| `hosting` | environments, image builds, slots, the model gateway, the egress proxy, harness homes |
| `auth` | iam mode: browser sessions and bearer checks |
| `connector` | the agent side: WebSocket client, ACP client, the agent's kit |

## Inside a domain

A domain is a directory with a fixed shape (`dock/` as the example):

```text
internal/dock/
  dock.go ports.go events.go    domain types, rules, ports (interfaces only)
  docksvc/                      service: implements the ports' Commands and Queries
  dockmodule/                   assembler: builds the adapters and the service
  adapters/
    dockpg/                     Postgres repository
    dockhttp/                   admin HTTP handlers
    dockwa/ dockapi/            channel gateways (whatsmeow, api)
    dockgithub/ dockjira/       channel gateways (GitHub App, Jira Cloud,
    dockslack/                  Slack app: stdlib REST, no SDK)
```

- **Domain root** (`dock`): types, pure rules and `ports.go`, which declares only interfaces: `Commands` and `Queries` (what the service offers), `Repository` (what it needs from storage), and others such as `Gateway` or `Owners`. No `net/http`, no SQL.
- **Service** (`docksvc`): the use cases. It validates input (`Validate()`), calls repositories and other domains' ports, and publishes events after its writes succeed. It never sees the auth mode.
- **Adapters**: the only place for transport and vendor code (HTTP, `sqlx`, whatsmeow). An adapter never imports another adapter.
- **Module** (`dockmodule`): assembles a domain's adapters and service. Only `bootstrap` imports modules.

Domains talk to each other only through ports: `shipsvc` uses `conversation`'s ports and `agent`'s types, never `conversationsvc`. When two services need each other, one is built first and the other is bound late (`Bind`): the WhatsApp gateway and the crew hub are built before the ship service they call into.

## Composition root

`internal/bootstrap` wires everything in `Build`: opens the Postgres pool (plus a pool of its own for session advisory locks), runs the embedded goose migrations, builds the event bus, picks the authenticator for the mode (`SEAPORT_AUTH`), creates every module, binds the late dependencies, and mounts the routes on a Fiber app (`bootstrap/routes.go`; the shell is `bootstrap/server.go` and `httpx/fiber.go`):

- `/api/` admin routes, each wrapped in `httpx.Require` with a permission.
- `/crew/ws`, `/mcp`, `/crew/profile`, `/crew/attachments/{id}`, `/crew/workspace`: authenticated with a crew credential.
- the model gateway under `hosting.GatewayPath` (a net/http reverse proxy mounted on a Fiber route, like `/mcp`, through `httpx/stdhttp`).
- `/` the embedded web app (`httpx.SPA`), with client routes falling back to `index.html`. Unknown `/api/` paths are a JSON 404 behind authentication, never the web app.

`App.Close` shuts down in order: the crew hub first (releasing its claims), then dock handover, hosted builds, and finally the event bus.

The web app (`web/`, React + Vite) is built into `web/dist` and embedded by `web/embed.go`. Only `bootstrap` imports it, so build the web app before `go build` to ship UI changes.

## Request flow: an inbound message

A WhatsApp message travels through the layers like this (GitHub, Jira and Slack messages enter the same way, from their adapters' webhook handlers):

```text
WhatsApp ──► dockwa session (adapter)
               normalizes: handles (+34600000001, JIDs), Addressed, Media
             │  dockmodule.Receiver (bound late)
             ▼
         shipsvc.Receive ───► conversationsvc.Arrive
             │                  customs: Decide (allowed|pending|denied), Admit (held?)
             │                  under the per-dock lock (session advisory lock)
             ├─ media: download, sniff, seal, Put to blob store
             ▼
         shippg.CreateInbound  (one transaction)
             │                  agent.Route(team, text, previous) → agent or hold reason
             │                  status: queued | held, ships.agent_id
             ▼
         events.Publish(ship.created) ──► events table + NOTIFY ──► every process's SSE
         Notifier.Notify(agent)       ──► crew hub, in every process
             ▼
         crewws hub: claim (advisory lock, one delivery in flight per conversation)
             │  `delivery` frame over WebSocket   (or `steer` into an open delivery)
             ▼
         connector ──► ACP harness (rness, Claude Code) ──► MCP `reply` tool
             ▼
         ship outbound: stored with the channel id, limits checked, then
         docksvc.route ──► the dock's owner process ──► gateway.Send
```

The adapter knows the channel; nothing downstream branches on it. See [message lifecycle](explanation/message-lifecycle.md) for the statuses and [crew protocol](reference/crew-protocol.md) for the frames.

## Architecture tests

`go test ./...` always runs the architecture tests in `internal/bootstrap`. They parse the source with `go/ast` (no extra dependencies), and each rule also has a fixture test proving it fires on bad code (`Test…CatchViolations`). A failure message names the file and the rule.

> [!WARNING]
> Never weaken a rule to make a change pass. Fix the code, or, if the rule is wrong, change the rule in the test and in [AGENTS.md](../AGENTS.md) together, with the reason.

| Test | Rule it enforces |
| :--- | :--- |
| `TestLayerDependencies` | Foundation imports no domain. Only `bootstrap` imports modules. Nobody imports another domain's `*svc`: depend on its `Commands`/`Queries`. Adapters don't import other adapters. Domain roots and `*svc` never import `net/http`, `database/sql`, `sqlx`, `pgx`. `connector` imports only foundation and domain roots, and only `cmd` imports it. Vendor SDKs stay in their adapter (`vendorSDKs`: whatsmeow → `dockwa`, MCP SDK → `crewmcp`, ACP SDK → `connector`, `coder/websocket` → `connector`, Fiber's websocket → `crewws`, goose, pgx, IAMKit and JWT → `authiam`, Docker SDK → `hostingdocker`, `gocloud.dev` → `gocloudblob`, Fiber/fasthttp → `httpx`, `bootstrap` and the adapters that serve routes, the embedded web app → `bootstrap`). whatsmeow's own `sqlstore` is banned in production code, because it keeps keys in clear. |
| `TestPortsAreInterfaces` | `ports.go` declares only interfaces. |
| `TestServicesExportNoConstants` | `*svc` packages export no constants; they belong to the domain root. |
| `TestErrxNoTypedNil` | A function returning `*errx.Error` never does `return nil` (no typed nils). |
| `TestTenantFirstPorts` | Every method of the tenant ports (`Commands`, `Queries`, `Repository`, `Customs*`, `Token*`, `Presence`, `DockLocker`, `Owners`, `Relay`, `Kits`, `Gate`, …) of `dock`, `conversation`, `ship`, `crew`, `agent` and `hosting` takes `(ctx, organization identity.OrganizationID, …)`. The system-wide exceptions (gateway callbacks, sweepers, crew sign-in, presence) are listed with a reason in `systemWide`. |
| `TestServicesValidateInputs` | An exported service method taking a type with `Validate() error` calls it, and a `Validate()` method validates each field that has its own. |
| `TestOnlyTheOwnerDrivesADock` | In `docksvc`, gateway `Link`/`Unlink`/`Restore`/`Send` are called only from `local`, `sendHere`, `ensureRunning` and `restore`, the functions that run where ownership was checked. Everything else goes through `route`. A second connection would replace the owner's. |
| `TestOnlyTheCrewEquips` | Functions that hand out agent secrets in clear are called from one package each (`secretCalls`): `Equip` only by `crewhttp`, `OpenSecrets` only by `agentsvc`, `Reveal` only by `hostingsvc`, `OpenWorkspace` only by `hostinghttp`, `ModelKey` only by `hostinggateway`. No admin handler, event or MCP tool can return a secret. |
| `TestOnlyOwnersReachBlobs` | Only `blob`, its adapter, `bootstrap` and the services/modules that own sealed blobs (`shipsvc`, `hostingsvc`) import `internal/blob`. Others read through the owner's checked queries. |
| `TestAdminRoutesRequirePermission` | Every admin route is wrapped in `httpx.Require` or `RequireFunc`. |
| `TestNoNetHTTPRouting` | `http.ServeMux`, `http.Server` and `ListenAndServe` stay out of production code except the connector's loopback relay and the egress proxy; `httpx/stdhttp` is imported only by the packages that mount the MCP and gateway handlers. |
| `TestNoPermissionLiterals` | Permissions are named through `permission.*` constants, never string literals. |
| `TestEveryPermissionIsChecked` | Every permission in the catalog is checked somewhere. |
| `TestWebUsesGeneratedPermissions` | The web app never writes `"seaport:…"` literals; it uses the generated `permissions.gen.ts`. |
| `TestEveryEventTypeHasAPermission` | Every `Event…` constant maps to a catalog permission in `bootstrap.EventPermissions`, which decides who may read it on `/api/v1/events`. |
| `TestEveryHoldReasonIsTranslated` | Every non-empty `Hold…` constant of `conversation` and `agent` has a Spanish label in `holdLabel` of the web app, since held ships carry the reason string to the UI. |

Tests outside `internal/bootstrap` guard other contracts: the generated Go and TypeScript permission files must match [`iam/permissions.json`](../iam/permissions.json) (drift tests in `internal/permission/gen`), `TestEventsNeverCarryTheQR` keeps the WhatsApp pairing QR out of events, `TestSealerOpensOldValues` keeps existing web sessions readable, and `TestMigrationDropsTheOldStore` refuses a migration that would lose a linked device.

When you add a pattern worth protecting, add a rule and a fixture test with it.

## Multi-process design

You can run several `seaport serve` processes against one database. Postgres is the only coordination layer: there is no other broker, cache or lock service.

| Concern | Mechanism |
| :--- | :--- |
| **Live events** | `event.Publish` appends to the `events` table (insert plus `pg_notify` under an advisory lock, so ids commit in order). Every process `LISTEN`s on its own connection (plus a 2 s poll) and fans out to its SSE subscribers. The SSE id is the row id, so a client resumes on any replica. Events older than an hour are swept, and an unknown id gets `reset`. |
| **Crew presence** | Each agent connection is a `crew_connections` row. `Notify` and `Disconnect` act locally and queue a signal sent as `NOTIFY seaport_crew`. Each process touches its rows every `min(lease/4, 10s)`; rows untouched for three periods are reaped by any process, which releases their claims. |
| **Claims** | A claim is a lease (`config.ClaimLease`) recorded with its connection, so a dropped connection, a shutdown or a reaped process releases exactly what it held. A lease sweeper is the last resort. |
| **Dock ownership** | An exclusive channel (WhatsApp) runs in exactly one process: the holder of the dock's `dock_owners` lease (30 s, renewed every TTL/5). Link, unlink and send `route` to the owner, locally or through a `dock_requests` row plus `NOTIFY seaport_docks`. A process that can't renew for half a lease stops every connection (fencing). `api`, GitHub, Jira and Slack docks are not exclusive: they hold no connection, so any process takes their webhooks and sends. |
| **Customs lock** | Per-dock Postgres *session* advisory lock on a pool of its own, so a dock's decisions are serialized across processes without starving the work pool. |
| **Quotas and limits** | Counted resources are counted and inserted in one transaction under a per-organization advisory lock, so concurrent requests can't overshoot. |

Advisory lock keys are `7_401_00N`; pick the next free number for a new lock.

| Key | Where | Protects |
| :--- | :--- | :--- |
| `7_401_001` | `shippg.claimLock` | claiming deliveries |
| `7_401_002` | `shippg.outboundLock` | outbound limits |
| `7_401_003` | `crewpg.tokenLock` | crew token quota |
| `7_401_004` | `dockpg.createLock` | dock quota |
| `7_401_005` | `eventpg.appendLock` | event ids committing in order |
| `7_401_006` | `crewpg.connectionLock` | live agent connection quota |
| `7_401_007` | `conversationpg` customs lock | per-dock customs decisions (session lock) |
| `7_401_008` | `agentpg.createLock` | agent library quota |
| `7_401_009` | `hostingpg.hostingLock` | environments, slots and homes quotas |

Session advisory locks and `LISTEN` need session pooling, so a transaction-mode pooler such as PgBouncer can't sit in front of Seaport. To run replicas, see [several processes](guides/operations/several-processes.md).

## Tenancy

Every tenant row has `organization_id`, and child rows reference parents through composite `(organization_id, id)` foreign keys, so a row can't point into another organization. Single-owner modes (`none`, `token`) use `config.DefaultOrganization`; iam mode takes the organization from the caller's IAMKit token.

The organization travels explicitly: handlers read it from the request's `httpx.Principal` and pass it as the second argument of every service call (`TestTenantFirstPorts`). A row of another organization is `NotFound`, never `Forbidden`. Events carry their organization, and a crew connection and each MCP call act for their token's organization. Services never see the auth mode. See the [security model](explanation/security-model.md).

## Where to go next

- [AGENTS.md](../AGENTS.md): every invariant, with the reason.
- [Contributing](contributing/README.md): build, test and CI.
- [Concepts](explanation/concepts.md) and [message lifecycle](explanation/message-lifecycle.md): the product-level model.
