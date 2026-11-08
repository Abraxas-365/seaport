# AGENTS.md — Puerto

Go 1.26, hexagonal (ports & adapters). Read `README.md` for what Puerto is.
Design notes: `~/.rness/plans/puerto/puerto-design.md` (outside the repo).

## Commands

```bash
go build ./... && go vet ./...
go test ./...                                   # architecture tests always run
docker compose up -d                            # Postgres 127.0.0.1:5449
PGPASSWORD=puerto createdb -h 127.0.0.1 -p 5449 -U puerto puerto_test   # once
PUERTO_TEST_DATABASE_URL="postgres://puerto:puerto@127.0.0.1:5449/puerto_test?sslmode=disable" \
  go test -count=1 -p 1 ./...                   # + integration tests (they wipe that database)
```

Run the integration tests before finishing any change to a repository, the
hub, the MCP tools or the HTTP API.

Web (`web/`, pnpm): `pnpm lint && pnpm build` (tsc + vite → `web/dist`,
embedded by `web/embed.go`; rebuild before `go build` to ship UI changes),
`pnpm dev` for hot reload against a running `puerto serve`.

## Web conventions

- React 19 + Vite, no framework; shadcn/ui primitives in `src/components/ui`
  (generated, edit sparingly), Puerto design system in
  `src/components/puerto` — pages compose these, catalogued at `/design`
  (`src/pages/design-system.tsx`); add new components there too.
- Status colours come only from `tone.ts` (static class strings — Tailwind
  can't see dynamic class names); theme tokens live in `src/index.css`.
- Server state is React Query (`src/lib/hooks.ts`, keys in `query.ts`);
  `src/lib/live.ts` patches the cache from `/api/v1/events`. Invalidate the
  `keys.docks` list with `exact: true` (detail keys share the prefix).
- CSP is `script-src 'self'`: no inline scripts (`public/theme.js`).

## Layout

```
cmd/puerto            serve | admin CLI | connect
internal/
  errx identity query text pgerr config httpx   foundation (import no domain)
  event (+eventsse)   in-memory event bus + SSE stream (foundation; payloads
                      are domain types, published by services via event.Publisher)
  bootstrap           composition root (+ architecture and e2e tests)
  dock/               channels: domain, ports.go, docksvc, dockmodule,
                      adapters/{dockpg, dockhttp, dockwa (whatsmeow), dockapi}
  conversation/       chats + customs (customs.go: policy, rules, Decide/Admit):
                      …svc, …module, adapters/{…pg, …http}
  ship/               messages + delivery queue: …svc, …module, adapters/{…pg, …http}
  crew/               wire protocol; adapters/{crewws (hub), crewmcp (tools)}, crewmodule
  connector           agent side (`puerto connect`): WS client + ACP client
migrations/           goose SQL, embedded, applied at start
web/                  React SPA, embedded (web.Dist) and served by httpx.SPA at /
```

## Rules (enforced by `internal/bootstrap/*architecture_test.go`)

- Foundation imports no domain; only `bootstrap` imports assemblers
  (`*module`); nobody imports another domain's `*svc` — depend on its
  `Commands`/`Queries` ports; adapters don't import other adapters; domain
  roots and `*svc` never import `net/http`, `database/sql`, `sqlx`, `pgx`
  (`TestLayerDependencies`).
- `ports.go` declares only interfaces (`TestPortsAreInterfaces`); services
  export no constants (`TestServicesExportNoConstants`).
- Tenancy: every method of the `dock`/`conversation`/`ship` `Commands`,
  `Queries`, `Messenger`, `Repository` and `Customs*` ports takes
  `(ctx, organization identity.OrganizationID, ...)`; the few system-wide
  ones (gateway callbacks, restore, lease sweep) are listed with their
  reason in `systemWide` (`TestTenantFirstPorts`).
- Exported service methods call `Validate()` on their inputs
  (`TestServicesValidateInputs`).
- `connector` runs on the user's machine: it imports only foundation and
  domain roots (wire types), never services/adapters/wiring; only `cmd`
  imports it (`TestLayerDependencies`, "client" role).
- Vendor SDKs stay in their adapter: whatsmeow → `dockwa`, MCP SDK →
  `crewmcp`, ACP SDK → `connector`, websocket → `crewws`/`connector`, goose →
  `bootstrap`, the embedded web app (`puerto/web`) → `bootstrap`
  (`vendorSDKs` in `TestLayerDependencies`).

Never weaken a rule to pass; fix the code, or change the rule here and in the
test together with the reason. Add a rule when you add a pattern.

## Conventions

- Errors: one system, `internal/errx`. `errx.Wrap` keeps an existing typed
  error's message (Op is logs only) and returns `error` (no typed nils;
  `TestErrxNoTypedNil` forbids `return nil` from an errx func returning
  `*Error`). 5xx bodies carry a generic message.
- Postgres errors go through `pgerr.Map/MapWith` in repositories.
- IDs are `identity.ID[T]` (`DockID`, `ConversationID`, `ShipID`, `DeliveryID`, `RuleID`, `OrganizationID`).
- Text input: `text.Required/Optional` (limits, control chars) in `Validate()`,
  `text.Clean` in services.
- Late binding: the WA gateway and the crew hub are built before the ship
  service they call into; `Bind` wires them in `bootstrap.Build`.

## Invariants worth knowing

- Customs: `conversation.Customs.Decide` gives a chat's status (allowed |
  pending | denied) from owner override → self → deny rules → allow rules →
  dock default (ask = pending); `Admit` then holds single messages (sender
  denied, group not mentioning us in mentions mode). Status is stored and
  re-decided on every arrival and on every rule/policy/override change, under
  a per-dock lock in `conversationsvc`. Adapters report normalized handles
  (`+<digits>`, JIDs) and `Addressed`; the engine knows no channel.
- Ship status: inbound `held` (customs) | `queued` → `claimed`
  → `handled`; outbound `pending` → `sent|failed`. Allowing a conversation
  does not deliver its held history (the agent can read it with `history`).
- At most one delivery in flight per conversation (claim under
  `pg_advisory_xact_lock`), so an agent session never gets two prompts at once.
  For steering connectors (`hello.steer`) new ships of that conversation
  `Join` the open delivery (same lock, same delivery id) and go out as
  `steer` frames; their ack must name the handled `ship_ids` and every other
  ship still claimed by the delivery is requeued.
- Claims are leases (`config.ClaimLease`), renewed by the hub while the
  connection lives, released on disconnect and by the sweeper; a restart
  requeues all claims.
- Outbound ships are stored with the channel message id *before* sending, so
  WhatsApp's echo of our own message is dropped as a duplicate.
- Replies are only allowed to allowed conversations (`Forbidden` otherwise).
- Live events (`/api/v1/events`): services publish after their write
  succeeds (dock/conversation/customs/ship/delivery), the hub publishes
  `crew.changed`. The bus is in memory with a 512-event replay window keyed
  by `<boot>-<seq>`; an unknown `Last-Event-ID` gets `reset` (client
  refetches). Slow subscribers are dropped, never block publishers. Single
  process per database until the bus moves to Postgres LISTEN/NOTIFY.
- Modes (`PUERTO_AUTH`, plan `~/.rness/plans/puerto/modes-and-tenancy.md`):
  auth is the `httpx.Authenticator` port, picked once in `bootstrap`
  (`Open` for `none`, `Tokens` for `token`); it puts an `httpx.Principal`
  (organization, role) in the request context. Handlers take the
  organization from it (`httpx.Organization(r)`); services never see the
  mode. `none` is loopback-only: `config.Validate` refuses other listen
  addresses, and `httpx.Open` refuses foreign `Host` names (DNS rebinding)
  and requests with forwarding headers (a local proxy/tunnel would make
  remote callers look local). Public routes (`/healthz`, `/api/v1/config`)
  reveal nothing about any organization.
- Tenancy: every tenant row has `organization_id`; single-owner modes use
  `config.DefaultOrganization`. Child rows reference parents by
  `(organization_id, id)` composite FKs, so a row can't point into another
  organization. Another organization's row is `NotFound`, never
  `Forbidden`. Events carry their organization and `/api/v1/events` streams
  only the caller's; a crew connection and each MCP call act for the token's
  organization; deliveries go only to that organization's agents.
- Admin auth (token mode): bearer token, or the `puerto_session` cookie
  (stateless HMAC of the expiry keyed by the admin token) from
  `POST /api/v1/session`; cookie writes go through
  `http.CrossOriginProtection` (also in `none` mode, for every write).
- Unlinking a WhatsApp dock logs the device out and deletes its keys;
  deleting a dock cascades to conversations, ships and customs (FKs).
- whatsmeow keeps its signal store in the same database (`whatsmeow_*`
  tables, created by `sqlstore.New`). A dock's `address` is its device JID.
