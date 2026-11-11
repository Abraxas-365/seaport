# AGENTS.md — Seaport

Go 1.26, hexagonal (ports & adapters). Read `README.md` for what Seaport is;
user-facing docs live in `docs/` (tutorials, guides, reference,
explanation; keep them current with behavior — see
`docs/contributing/documentation.md` for the format the website relies on).
`website/` (Astro + Starlight, npm) renders the landing page and `docs/` for
GitHub Pages (`.github/workflows/website.yml`); check with
`cd website && npm test && npm run build && npm run check:links`.
Design notes: `~/.rness/plans/puerto/puerto-design.md` (outside the repo).

## Commands

```bash
go build ./... && go vet ./...
go test ./...                                   # architecture tests always run
docker compose up -d                            # dev Postgres 127.0.0.1:5449
docker build -t seaport .                        # image (web + binary, distroless)
# deploy/: production compose (Postgres, Seaport, Caddy TLS, backups); docs/guides/operations/deploy.md
PGPASSWORD=seaport createdb -h 127.0.0.1 -p 5449 -U seaport seaport_test   # once
SEAPORT_TEST_DATABASE_URL="postgres://seaport:seaport@127.0.0.1:5449/seaport_test?sslmode=disable" \
  go test -count=1 -p 1 ./...                   # + integration tests (they wipe that database)
SEAPORT_TEST_IAMKIT=1 SEAPORT_TEST_DATABASE_URL=… go test -count=1 -p 1 ./internal/bootstrap/
                                                # + iam mode e2e against a real IAMKit container (Docker)
go generate ./internal/permission               # after editing iam/permissions.json (Go + web/src/lib/permissions.gen.ts)
scripts/iamkit-provision.sh --dry-run           # provision IAMKit for iam mode (docs/guides/operations/iam-mode.md)
goreleaser release --snapshot --clean           # release binaries into dist/ (goreleaser v2)
```

CI (`.github/workflows/ci.yml`): gofmt, vet, `go test -race` against a
Postgres service and with `SEAPORT_TEST_IAMKIT=1` (testcontainers starts
IAMKit + its Postgres; with `CI` set, a missing test database or IAMKit
fails instead of skipping), web lint + build, docker build. Releases: push a `v*` tag →
`release.yml` runs goreleaser (`.goreleaser.yaml`) and publishes the
binaries to GitHub Releases.

Run the integration tests before finishing any change to a repository, the
hub, the MCP tools or the HTTP API; the IAMKit e2e too for anything touching
auth, permissions, sessions or tenancy.

Web (`web/`, pnpm): `pnpm lint && pnpm build` (tsc + vite → `web/dist`,
embedded by `web/embed.go`; rebuild before `go build` to ship UI changes),
`pnpm dev` for hot reload against a running `seaport serve`.

## Web conventions

- React 19 + Vite, no framework; shadcn/ui primitives in `src/components/ui`
  (generated, edit sparingly), Seaport design system in
  `src/components/seaport` — pages compose these, catalogued at `/design`
  (`src/pages/design-system.tsx`); add new components there too.
- Status colours come only from `tone.ts` (static class strings — Tailwind
  can't see dynamic class names); theme tokens live in `src/index.css`.
- Server state is React Query (`src/lib/hooks.ts`, keys in `query.ts`);
  `src/lib/live.ts` patches the cache from `/api/v1/events`. Invalidate the
  `keys.docks` list with `exact: true` (detail keys share the prefix).
- CSP is `script-src 'self'`: no inline scripts (`public/theme.js`).
- Permissions: `useCan(Perm.X)` from `src/lib/permissions.gen.ts`
  (generated; never write `"seaport:…"` literals — `TestWebUsesGeneratedPermissions`).
  Hide what the caller can't do; pages they can't read show `NoAccess`.

## Layout

```
cmd/seaport            serve | admin CLI | connect
internal/
  errx identity query text pgerr config httpx seal   foundation (import no domain)
  pglisten pglock     Postgres LISTEN connections / session advisory locks (foundation)
  blob (+adapters/    object storage port (immutable Put, tenant keys, image
   gocloudblob)       sniffing) + its gocloud adapter: file://, s3://, mem:// (foundation)
  permission          permission catalog generated from iam/permissions.json (foundation)
  event (+eventsse,   event bus over a Log (eventpg: Postgres table + LISTEN/
   eventpg)           NOTIFY) + SSE stream (foundation; payloads are domain
                      types, published by services via event.Publisher)
  bootstrap           composition root (+ architecture and e2e tests; events.go
                      maps each event type to the permission that reads it)
  auth/               iam mode: browser sessions, bearer checks; authsvc, authmodule,
                      adapters/{authiam (IAMKit SDK), authpg (web_sessions), authhttp
                      (httpx.Authenticator + OAuth routes)}
  dock/               channels: domain, ports.go, docksvc, dockmodule,
                      adapters/{dockpg, dockhttp, dockwa (whatsmeow), dockapi,
                      dockgithub (GitHub App: stdlib REST, no SDK),
                      dockjira (Jira Cloud: stdlib REST + goldmark)}
  conversation/       chats + customs (customs.go: policy, rules, Decide/Admit):
                      …svc, …module, adapters/{…pg, …http}
  ship/               messages + delivery queue: …svc, …module, adapters/{…pg, …http}
  crew/               wire protocol + crew tokens (token.go): crewsvc, crewmodule,
                      adapters/{crewws (hub), crewmcp (tools), crewpg, crewhttp}
  agent/              agent library + dock teams + routing (Route, pure) +
                      profiles/secrets/kit (profile.go): agentsvc, agentmodule,
                      adapters/{agentpg, agenthttp}
  hosting/            hosted agents: environments (versioned specs, environment.go)
                      + slots (slot.go) + image builds (build.go): hostingsvc,
                      hostingmodule, adapters/{hostingpg, hostinghttp,
                      hostingdocker (Docker Engine API: hosting.Runtime),
                      hostinggateway (model gateway), hostingegress
                      (slots' egress proxy)}
  connector           agent side (`seaport connect`): WS client + ACP client,
                      applies the agent's kit (kit.go, wire.go: systemPrompt)
iam/permissions.json  permissions + roles (prefix seaport), source of truth
action/               composite GitHub Action: a workflow hands a message to a
                      GitHub dock (OIDC, POST /github/actions/{dock})
images/               base images of hosted agents (Dockerfile targets rness,
                      claude-code; release.yml pushes ghcr.io/abraxas-365/seaport-*)
scripts/              iamkit-provision.sh (idempotent IAMKit setup for iam mode)
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
- Tenancy: every method of the `dock`/`conversation`/`ship`/`agent`/`hosting` `Commands`,
  `Queries`, `Kits`, `Messenger`, `Repository`, `Customs*`, `DockLocker`, `Owners`,
  `Relay`, `Gate` and `crew` `Token*`, `Presence` ports takes
  `(ctx, organization identity.OrganizationID, ...)`; the few system-wide
  ones (gateway callbacks, lease sweep, crew token sign-in, crew presence
  touch/reap/signals, dock lease renew/claim/release-all and relay serve)
  are listed with their reason in `systemWide` (`TestTenantFirstPorts`).
- Exported service methods call `Validate()` on their inputs
  (`TestServicesValidateInputs`).
- In `docksvc`, gateway `Link`/`Unlink`/`Restore`/`Send` are called only
  from the functions that run where ownership was checked (`ownerOnly`:
  `local`, `sendHere`, `ensureRunning`, `restore`); everything else goes
  through `route` (`TestOnlyTheOwnerDrivesADock`).
- `connector` runs on the user's machine: it imports only foundation and
  domain roots (wire types), never services/adapters/wiring; only `cmd`
  imports it (`TestLayerDependencies`, "client" role).
- Vendor SDKs stay in their adapter: whatsmeow → `dockwa`, MCP SDK →
  `crewmcp`, ACP SDK → `connector`, websocket → `crewws`/`connector`, goose →
  `bootstrap`, the embedded web app (`seaport/web`) → `bootstrap`, IAMKit SDK
  and golang-jwt → `authiam`, `golang.org/x/time` → `httpx` (rate limit),
  pgx itself → `bootstrap`, `pgerr`, `pglisten` (LISTEN connections),
  the Docker SDK (`moby/moby`, `containerd/errdefs`) → `hostingdocker`,
  `gocloud.dev` → `blob/adapters/gocloudblob`, goldmark → `dockjira`
  (`vendorSDKs` in `TestLayerDependencies`).
- Permissions (`permission_architecture_test.go`): every admin route is
  wrapped in `httpx.Require`/`RequireFunc` (`TestAdminRoutesRequirePermission`);
  permissions are named only through `permission.*` constants, never string
  literals (`TestNoPermissionLiterals`); every catalog permission is checked
  somewhere (`TestEveryPermissionIsChecked`); every event type maps to a
  permission in `bootstrap.EventPermissions` (`TestEveryEventTypeHasAPermission`);
  the generated Go/TS files match `iam/permissions.json` (drift tests in
  `internal/permission/gen`).
- Agent secrets (`secret_architecture_test.go`, `TestOnlyTheCrewEquips`):
  `Equip` (the kit, secrets resolved) is called only by `crewhttp`
  (`GET /crew/profile`, the agent's own crew credential) and `OpenSecrets`
  only by `agentsvc` (`secretCalls`), so no admin handler, event or MCP
  tool can hand a secret back. Hosting's openers are guarded the same way:
  `Reveal` only from `hostingsvc`, `OpenWorkspace` only from `hostinghttp`
  (the slot's own token) and `ModelKey` only from `hostinggateway` (it
  forwards with the key, never answers it).
- Blob store (`blob_architecture_test.go`, `TestOnlyOwnersReachBlobs`):
  only `blob` itself, its adapter, `bootstrap` (opens it), and the
  services/modules that own sealed blobs (`ship/shipsvc`,
  `hosting/hostingsvc` and their modules) import `internal/blob`
  (`blobOwners`); everyone else reads through the owner's checked queries
  (`OpenAttachment`, `HomeFile`/`HomeArchive`).
- Public routes (`permission_architecture_test.go`,
  `TestPublicRoutesAreListed`): every root route (outside `/api/v1/`) is
  wrapped in the admin or crew authenticator or listed with its reason in
  `publicRoutes` (`/`, `/healthz`, GitHub's redirects, webhook and
  Actions endpoint, Jira's webhook, which carry their own proof: a sealed
  state, the App's HMAC, a GitHub Actions OIDC token for this port or the
  Jira dock's secret as HMAC/bearer).
- Hold reasons (`web_architecture_test.go`): every non-empty `Hold… = "…"`
  constant of a domain root (`conversation`, `agent`) has a Spanish entry
  in `holdLabel` (`web/src/components/seaport/ship-bubble.tsx`), since
  held ships carry the reason string to the web
  (`TestEveryHoldReasonIsTranslated`).

Never weaken a rule to pass; fix the code, or change the rule here and in the
test together with the reason. Add a rule when you add a pattern.

## Conventions

- Errors: one system, `internal/errx`. `errx.Wrap` keeps an existing typed
  error's message (Op is logs only) and returns `error` (no typed nils;
  `TestErrxNoTypedNil` forbids `return nil` from an errx func returning
  `*Error`). 5xx bodies carry a generic message.
- Postgres errors go through `pgerr.Map/MapWith` in repositories.
- IDs are `identity.ID[T]` (`DockID`, `ConversationID`, `ShipID`, `DeliveryID`, `RuleID`, `AgentID`, `EnvironmentID`, `OrganizationID`).
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
  a per-dock lock (`conversation.DockLocker`, shared by every process). Adapters report normalized handles
  (`+<digits>`, JIDs) and `Addressed`; the engine knows no channel.
- Ship status: inbound `held` (customs or routing, `hold_reason`) | `queued` → `claimed`
  → `handled`; outbound `pending` → `sent|failed`. A direct chat that
  becomes allowed (`redecide`: override, rule or policy) releases its ships
  held with `conversation.HoldPending` within `ship.ReleaseWindow` (newest
  `ship.MaxRelease`) through `conversation.Gate` (= `ship.Commands.ReleaseHeld`,
  bound late): `shippg.ReleaseHeld` re-checks the chat `FOR SHARE`, locks
  the ships and the team like `CreateInbound` and routes them oldest first.
  `Receive` releases too when the chat turned allowed between customs and
  its insert. `HoldDenied` ships, older ones and groups' stay held (whether
  a group message addressed us isn't stored); the agent reads them with
  `history`.
- At most one delivery in flight per conversation (claim under
  `pg_advisory_xact_lock`), so an agent session never gets two prompts at once.
  For steering connectors (`hello.steer`) new ships of that conversation
  `Join` the open delivery (same lock, same delivery id) and go out as
  `steer` frames; their ack must name the handled `ship_ids` and every other
  ship still claimed by the delivery is requeued.
- Agents (`agent`, migration 00013): an organization's library
  (`agents`, soft delete, unique live name = its `@mention` handle, at
  most `agent.MaxAgents`, counted under `agentpg.createLock` `7_401_008`)
  and each dock's team (`dock_agents`, at most one `is_default`). An agent
  works only through docks it is assigned to. Every inbound ship that goes
  to the crew names its agent (`ships.agent_id`; CHECK: queued/claimed
  ships have one), decided once at arrival by the pure `agent.Route(team,
  msg, previous)`: no team → held `HoldNoAgent`; first `@name` of a
  member; the conversation's previous agent if still on the team; the
  only member; the default; else held `HoldNotCalled`.
  `shippg.CreateInbound` reads the team `FOR SHARE` in the insert's
  transaction, so an unassign/delete (which deletes the rows first, then
  holds the agent's waiting ships with `HoldAgentGone`) and an arrival
  never cross. Held ships keep their `hold_reason` (customs or routing).
  Claims are per agent: a connection claims only its agent's ships, and a
  conversation is worked in arrival order by one agent at a time
  (`claimable`: never past an older ship queued for another agent; so a
  conversation waits while the agent it owes a message is offline);
  `Notify` wakes the agent's connections. A connection works as its
  token's agent (`Principal.Agent`); in open mode `hello.as` names it, a
  token's `hello.as` must match, and a refused hello closes with
  `StatusPolicyViolation` + reason (the connector stops retrying).
  Deleting an agent revokes its tokens and closes its connections
  (`agent.Revoker` = `Hub.DisconnectAgent`; an open-mode connection records
  its agent on `pending` and re-checks it after the hello). Migration 00013 gave every
  organization in use an agent `default` (default of every dock, owner of
  every token).
- Agent profiles (migration 00014, `agent/profile.go`): `agent_profiles`
  keeps every version (PK org, agent, version; `agents.profile_version`
  is the current one, 0 = none). `SaveProfile` is optimistic: `Base` must
  be the current version (else `Conflict`), and MCP headers may name only
  secrets the agent has (`${NAME}`). `agent_secrets` are sealed
  (`internal/seal`, info `seaport agent secrets v1`, purpose
  `agent-secret/<org>/<agent>/<name>`); the admin API answers names and
  flags only, and a secret a header uses can't be deleted. `Equip` builds
  the agent's `Kit` (profile + resolved headers + `env` secrets +
  `Revision`, a hash of it all). A saved profile or secret calls
  `agent.Revoker.ProfileChanged` (`Hub`: `profile` frame to the agent's
  connections, everywhere via `crew.SignalProfile`) and publishes
  `agent.updated`. The connector fetches its kit at start and on welcome
  and `profile` frames; on a new revision it sends `drain` (the hub gives
  it no new deliveries), waits until idle and restarts its harness with
  the kit, resuming sessions. `hello.profile_version` is stored with the
  connection; the crew status marks it `outdated` when it isn't the
  agent's current version. Instructions go as ACP `systemPrompt` (append)
  when the agent advertises `sessionCapabilities.systemPrompt`, else with
  each session's first prompt. `sessions.json` records per conversation the
  hash of the instructions its session saw (`savedSession`; bare ids of
  older files read as unknown): a resumed session whose instructions
  changed (or are unknown) gets them again with its next prompt, since a
  system prompt on resume doesn't outweigh the history; a brief is marked
  seen only after a turn carrying it succeeds. Skills are served by the MCP `skill` tool
  (per request, from the current profile), never written to the agent's
  machine.
- Hosting (`hosting`, migration 00015): an organization's environments
  (`environments` + `environment_versions`, versioned and optimistic like
  profiles; an unchanged spec makes no version; at most
  `hosting.MaxEnvironments`, counted under `hostingpg.hostingLock`
  `7_401_009`) say where hosted agents run: harness (`rness` |
  `claude-code`), image (`FROM` Seaport's base), packages, setup, repos
  (https, `credential: ${SECRET}` = a secret of the hosted agent), egress
  hosts, resources (S/M/L or custom, within `SEAPORT_HOST_MAX_*`),
  workspace, idle stop. `hosting.Normalize` resolves defaults before
  storing, so a stored spec is complete. Hosting an agent (`agent_hosting`)
  gives it slots (`agent_slots`, stable `<agent>-<n>`; at most
  `SEAPORT_MAX_SLOTS` per organization, under the same lock); its
  environment's credentials must be secrets it has (checked on host and
  on each save of the spec), a secret they use can't be deleted, and
  naming credentials needs `agents:write` besides `environments:write`
  (`hostinghttp.credentials`). An environment with hosted agents can't be
  deleted; deleting an agent unhosts it (slots cascade); a running slot
  must be stopped before unhosting or dropping it.
- Environment builds (`hosting/build.go`, migration 00016,
  `SEAPORT_RUNTIME=docker`; `off`, the default, refuses builds): each
  environment version is built once into an image (`environment_builds`,
  state building → ready | failed) `FROM` its `image:` or the harness's
  base (`SEAPORT_IMAGE_*`, default `config.BaseImage`: the release's tag,
  else `:latest`), plus packages (apt) and setup (bash as root, home handed
  back to uid 1000), tagged `seaport-env-<env>:v<N>-<hash>` (harness, base,
  Dockerfile, setup) and labeled with org/environment/version. A base must
  carry `io.seaport.base` and the spec's `io.seaport.harness` (Seaport's
  base images set them; derived images inherit them). `ClaimBuild` makes
  one caller the builder (an insert, or taking over a failed, forced or
  stale one: no progress for `hosting.BuildStale`); the build runs in the
  background (`hostingsvc.runBuild`), stores its log's tail
  (`MaxBuildLog`) every `BuildStale/6`, and records its end even on
  shutdown (`App.Close` → `hosted.Close` cancels and waits, before the
  bus closes). `environment.build` events carry no log.
- Slot runs (`hosting/run.go`, migration 00017; `hostingsvc/slots.go`,
  `supervisor.go`): a slot is stopped → starting → running → stopping →
  stopped (or failed). `BeginStart` claims a stopped/failed slot under
  `FOR UPDATE` and gives the run an id (`agent_slots.run`); every later
  write of the starter matches that run, so one that lost the slot (stale
  start, `StartStale`) changes nothing. A start builds the environment's
  image if needed, mints the run's crew token (`crew_tokens.slot` = the
  index; 30 days, not counted against `crew.MaxTokens`, not listed with
  people's tokens) and runs the container (`hostingdocker`: read-only
  root, caps dropped, no-new-privileges, uid 1000, throwaway home volume,
  `<slot>-state` and `<slot>-ws` volumes, labeled `io.seaport.port` =
  `port_identity`, so ports sharing an engine leave each other's slots
  alone). Stop and failure revoke the token (its connections close).
  Every process runs the supervisor (`SupervisePeriod`, and on
  `ship.Notifier` through `bootstrap.fanout`): it wakes a hosted agent with
  queued ships and no slot up (lowest stopped slot, else a failed one
  after 5 min; one at a time per agent), fails a running slot whose
  container is gone or exited, restarts an idle outdated one, stops one
  idle past `idle_stop`, ends stale starts/stops, and removes runtime
  slots with no row after two rounds; each step is a conditional
  transition (`BeginStop(run)` only stops that run). Inside, `seaport
  agent-run` (`connector.Hosted`) reads `GET /crew/workspace` (slot tokens
  only; repo credentials resolved by `Kits.Reveal`, see `secretCalls`),
  links the harness's sessions/sign-in into the state volume, clones or
  fetches the repos (git asks `seaport git-credential`, which asks the port
  each time: no token on disk), gives each conversation a worktree
  (`wt/<conversation>`, branch `seaport/<conversation>`), relays the port's
  MCP on loopback (harnesses take plain-HTTP MCP only there) and runs the
  connector (Claude Code in session mode `default`: its `auto` mode needs
  Anthropic's classifier). A refused token ends it cleanly (no restart).
- Hosted network (`hosting/network.go`, migration 00018): an
  environment's `models: {anthropic: ${SECRET}}` names an agent secret
  (checked like repo credentials: must exist, can't be deleted while
  named, needs `agents:write`). Its slots get `SEAPORT_GATEWAY`,
  `ANTHROPIC_BASE_URL` (= `<agent URL>/gateway/anthropic`) and
  `ANTHROPIC_API_KEY` = the slot token, so the key never enters the
  container; the connector won't let the kit's env replace them
  (`Config.Protect`), and for Claude Code `agent-run` writes them as
  managed settings (`connector.pinClaudeSettings`, in
  `connector.ClaudeManaged` on the slot's tmpfs, which the base image
  links from `/etc/claude-code`; plus a blank `apiKeyHelper` and
  `ANTHROPIC_AUTH_TOKEN`), so no `settings.json` overrides them. The
  spec's `repo_config: ignore` keeps the repos' configuration out
  (`connector.harnessFor`: rness `--instructions none`, Claude Code
  `settingSources: ["user"]` in the session `_meta`). The gateway (`hostinggateway`, behind crew auth;
  `x-api-key` counts as the bearer) takes slot tokens only, forwards
  `/v1/messages[/count_tokens]`, `/v1/models*`, `/v1/files*` (else 404)
  with the real key (`Commands.ModelKey`), drops the caller's
  Authorization and Accept-Encoding, streams (`FlushInterval -1`) and
  meters successful Messages answers (JSON usage, or the stream's
  `message_start`/`message_delta`) into `model_usage` (per org, agent, UTC
  day, provider, model; `GET /api/v1/agents/{id}/usage`); errors go out in
  Anthropic's error shape. The egress proxy (`hostingegress`,
  `SEAPORT_EGRESS_ADDR`; off by default) is the slots' only way out when
  the runtime network is `internal` (the Docker adapter warns when it
  isn't): slots get `HTTP(S)_PROXY` with `<slot>:<slot token>` and
  `NO_PROXY` for the port; CONNECT and plain http reach only the
  environment's egress plus repo hosts (`Spec.EgressHosts`, cached
  `EgressCacheFor` per token), never private/loopback/link-local/CGNAT
  addresses unless `SEAPORT_EGRESS_PRIVATE`; refusals are counted per slot
  and host (`egress_denials`, at most once per `DenialEvery`;
  `GET /api/v1/agents/{id}/hosting/egress`).
- Subscription sign-in (experimental; `hosting/login.go`, migration
  00019, `hostingsvc/logins.go`): a `claude-code` spec with no
  `models.anthropic` (`Spec.SignsIn`) adds the subscription hosts to the
  egress and `Workspace.SignIn`, so `agent-run` waits (taking no work)
  until `claude auth status` says logged in, and restarts the harness on
  each new sign-in mark. `StartLogin` (one live per slot in `slot_logins`,
  keyed by the slot's run) runs `seaport agent-login` in the slot through
  `Runtime.Exec` (uid 1000) in a background relay of the process that
  started it: it takes only a Claude OAuth URL from the output
  (`LoginURL`: https, Anthropic's hosts, `/oauth/authorize`), feeds the
  code posted to `slot_logins` (`TakeLoginCode` clears it at once) to
  claude's stdin, beats every `LoginBeat` (a sign-in with no beat for
  `LoginStale` reads as failed) and ends within `LoginTimeout`. Tokens
  stay in the slot's state volume (`~/.claude` and `~/.claude.json`
  linked there); the port never sees them, and the code is not a secret
  opener (single use, bound to the PKCE of that login). `Account` asks
  the harness (`agent-account`). rness refuses (no headless sign-in);
  the fallback is an env secret `CLAUDE_CODE_OAUTH_TOKEN`.
- Message media (`ship/attachment.go`, migration 00020,
  `shipsvc/media.go`): a message is text plus attachments (images only so
  far); channels normalize to that (`ship.Inbound.Media`) and nothing downstream branches on a channel's message type. A channel
  gives the bytes (`Media.Data`) or a `Media.Fetch` the service calls
  within the arrival (`dockwa`: WhatsApp's URLs expire; the download goes
  to a `memFile` capped at `ship.MaxImage` plus the encryption overhead,
  whatever size the sender declared). `Receive` fetches nothing for a
  redelivery (`FindExternal` first) or a denied chat/sender
  (`shipsvc.refused`: anyone can message a dock; a note instead), then
  sniffs the type from the bytes (`blob.ImageType`, `ship.ImageTypes`), seals them (`internal/seal`,
  info `seaport media v1`, purpose `attachment/<org>/<id>`) and puts them
  in the blob store (`SEAPORT_BLOB_URL`, opened once in `bootstrap`; off
  without a secret key) under `blob.TenantKey(org, "attachments", id)`
  *before* the ship's transaction; what can't be kept becomes a
  `ship.MediaNote` in the text, never a lost message. Each key is staged
  in `blob_trash` (due after `ship.TrashGrace`) before its Put, and
  `CreateInbound` removes it from the trash in the insert's transaction,
  so a crash or a duplicate leaves a blob the sweeper deletes; deleting an
  attachment row (cascades from dock, conversation, ship) puts its key back
  in the trash by trigger, due now. `SweepTrash` runs with the claim
  sweeper (`config.SweepInterval`). Rows are the index (the bucket is
  never listed). Reads: `ship.Queries.OpenAttachment` (an agent only for
  conversations on its docks, else NotFound), served by
  `GET /crew/attachments/{id}` (crew), `GET /api/v1/attachments/{id}`
  (`ships:read`) and the MCP `attachment` tool, with `httpx.File`
  (sniffed type, `nosniff`, sandbox CSP, `no-store`). Ships carry attachment metadata
  (`Ship.Attachments`, filled by `shippg.attach` wherever ships are read);
  the connector fetches the images of a delivery or steer and sends them as
  ACP image blocks only when the agent advertises
  `promptCapabilities.image` (rness refuses them otherwise), up to
  `maxPromptImages`, naming the rest in the text.
- Outbound files (migration 00023, `shipsvc.keepFiles`): `ship.Outbound.Files`
  (MCP `reply` `files` base64, `POST /crew/reply` = `seaport reply -file`,
  body ≤ `crew.MaxReplyBody`; MCP handler's `MaxRequestBodyBytes` too) are
  checked by `ship.ValidateFiles` (≤ `MaxAttachments`, `MaxFile` each,
  `MaxFiles` together, plain unique names), sniffed (`blob.ImageType` →
  kind `image`; else `document` with `blob.FileType`, which maps anything
  renderable to `application/octet-stream`), sealed and staged like media
  *before* `CreateOutbound`, which inserts the attachments in its
  transaction; no blob store → `Business` refusal, never dropped. Documents
  are served with `httpx.Download`. `dock.Outgoing.Files` reach the
  gateway: `dockgithub` commits them in one commit (Git Data API) to the
  orphan branch `FilesBranch` (`seaport-files`, retried when the ref moved)
  under `pr-<n>/<message id>/` and links them at that commit in the
  comment (`filesBody`); `dockwa` sends the text with the stored id, then
  each file as an image/document message (ids remembered in
  `session.sentFiles` so the self-chat echo is dropped); `dockapi` keeps
  them. The App's own comments (`<slug>[bot]`) come back by webhook as
  `Inbound.Own`: `Receive` records them (`Repository.Record`: outbound,
  sent, agent = the conversation's claiming agent, wakes nobody); `Send`
  appends `<!-- seaport-message <id> -->` so the echo of a stored reply is
  a duplicate.
- Harness homes (`hosting/home.go`, migration 00021,
  `hostingsvc/homes.go`): an environment's `home: sha256:…` names an
  uploaded `~/.claude` or `~/.rness` (`POST /api/v1/homes?harness=…`, raw
  tar/tar.gz up to `MaxHomeUpload`, `httpx.ReadBody`; `seaport env home
  push|show|pull`, or `home: ./dir` in `env apply`; the web's
  "Configuración del harness" section, `web/src/pages/environment-home.tsx`).
  The web filters and checks a dropped folder or .zip/.tar.gz in the
  browser with the server's own rules (`GET /api/v1/homes/rules` =
  `hosting.HomeRulesOf`: paths, limits, `SecretPattern`s in RE2 syntax JS
  also parses), lists what it left out, then uploads a tar.gz the server
  checks again strictly (no lenient mode); edits re-upload the whole home.
  `ReadHome` allows only
  the harness's config paths (`HomePath`: Claude `CLAUDE.md`,
  `settings.json`, `keybindings.json`, `agents/ commands/ skills/ hooks/
  output-styles/`, `statusline*`; rness `init.lua`, `hooks.json`, `lua/
  plugins/ skills/`, never `lua/seaport.lua`), regular files only, within
  `MaxHomeFiles`/`MaxHomeFile`/`MaxHomeBytes`, and refuses secret-looking
  strings (`ScanSecrets`: secrets go as the agent's env secrets, files read
  the environment); sessions, sign-in, memory and Claude's `plugins/` are
  never uploaded. It normalizes to a sorted tar (uid/gid 1000, 0644/0755,
  mtime 0) whose SHA-256 with the harness is the digest, so the same files
  make the same home. The tar is sealed (info `seaport homes v1`, purpose
  `home/<org>/<digest>`) in the blob store, staged in `blob_trash` before
  the Put like media; `hosting_homes` (at most `MaxHomes`, counted under
  `hostingLock`) is the index, and deleting a row puts its blob back in
  the trash by trigger. `environment_versions.home` references it (FK);
  saving a version naming a home touches it under `hostingLock`, and
  `SweepHomes` (claim sweeper, before `SweepTrash`) deletes homes no
  version names, untouched for `HomeGrace`, under the same lock. Applied at
  build time: `runBuild` opens the home into `BuildRequest.Home`, the
  Dockerfile (whose `# home <digest>` line changes the tag) copies it over
  `/home/agent` after setup and runs `seaport home-setup`
  (`connector.HomeSetup`: rness keeps `require("seaport")` first in
  `init.lua`; Claude Code adds the marketplaces `settings.json` declares
  in `extraKnownMarketplaces` (github or https git only) and installs the
  plugins `enabledPlugins` turns on). At start `agent-run` seeds the
  state volume from the image (`seedDir`: changed files replaced, files a
  previous home shipped and this one doesn't deleted, via
  `.seaport-seeded`; the harness's own files stay); rness `memory/` lives
  in the state volume.
- Claims are leases (`config.ClaimLease`), renewed by the hub while the
  connection lives. A claim records its connection (`ships.claimed_by` =
  the `crew_connections` session; joined ships inherit it), so whatever a
  connection holds — tracked or not, e.g. a claim that committed as it
  closed — is released by session (`ReleaseSession`) on disconnect, on
  shutdown (`Hub.Close`, first in `App.Close`) and when a dead process's
  connection is reaped; the lease sweeper is the last resort. Start never
  requeues claims: other processes own them (migration 00010 requeued the
  claims of before it once).
- Crew across processes (`crew.Presence`, `crewpg`, migration 00010): each
  connection is a `crew_connections` row from before the WebSocket upgrade
  to its close, holding its process, credential, hello and deliveries in
  flight (status only; written under a per-connection mutex so an older
  snapshot never lands last). `GET /api/v1/crew` and `crew.changed` read
  it, so they show every process. Wakes (`Notify`) and revokes
  (`Disconnect`) act locally and queue a `crew.Signal` that one goroutine
  sends as `NOTIFY seaport_crew` (coalesced; callers never wait on the
  database); every LISTEN (re)connect wakes all local connections, since
  signals sent meanwhile are lost. Each process touches its rows every
  `min(lease/4, 10s)` on its own goroutine (never behind reaping); the
  touch returns rows it must close (reaped meanwhile, or opened with a
  token now revoked or expired: the backstop of a lost revoke). Rows
  untouched for 3 periods (database clock) are reaped by any process,
  which releases their claims by session (retried next round on failure).
  A connection whose release failed on close keeps its row for the reaper.
  After a crash the dead process's rows count against the agent quota
  until reaped (about 30 s).
- The per-dock customs lock is a Postgres session advisory lock
  (`conversationpg.DockLocker`, key `7_401_007`, via `internal/pglock`) held
  on a pool of its own (`openLockDB`, 10 connections, no
  `statement_timeout`: the caller's context bounds the wait), so a holder
  waiting for a work connection can't starve the pool. `pglock` queues a
  process's waiters of one key in memory first, so a busy dock holds one
  lock connection, not the pool. Session locks and LISTEN need session
  pooling: no PgBouncer in transaction mode in front of Seaport.
- Outbound ships are stored with the channel message id *before* sending, so
  WhatsApp's echo of our own message is dropped as a duplicate.
- Replies are only allowed to allowed conversations (`Forbidden` otherwise).
- Outbound limits (`ship.Limits.Check`, the rule; config `SEAPORT_SEND_*`):
  `shippg.CreateOutbound` counts the dock's/conversation's/organization's
  outbound ships of the last minute/hour and inserts under a
  per-organization advisory lock (`outboundLock`; the send happens after
  the commit), so concurrent replies can't overshoot; refusal is
  `errx.TooManyRequests` (429 + `Retry-After`), nothing stored or sent.
- Quotas per organization (config `SEAPORT_MAX_*`, 0 = off; same for every
  organization until plans exist): refusal is `errx.QuotaExceeded` (422
  `QUOTA_EXCEEDED`, details `quota`/`limit`). Counted resources follow the
  crew-token pattern: count + insert in one transaction under a
  per-organization advisory lock (`dockpg.createLock`, `crewpg.tokenLock`).
  Live agent connections (every process's `crew_connections` rows, opening
  ones included) are counted and inserted under `crewpg.connectionLock`
  (`7_401_006`) before the WebSocket upgrade. Advisory lock keys are
  `7_401_00N`; pick the next one for a new lock.
- API rate limit (`httpx.RateLimit`, `SEAPORT_API_RATE`/`_BURST`): a token
  bucket per principal (organization + role + subject + credential; agents
  sharing one crew token share its bucket) after the
  Authenticator on `/api/`, `/crew/ws`, `/mcp`; in memory, per process
  (with N replicas a caller may get up to N× the rate: accepted). Public routes are not limited. The web app retries
  reads on 429 after `Retry-After`.
- Live events (`/api/v1/events`): services publish after their write
  succeeds (dock/conversation/customs/ship/delivery), the hub publishes
  `crew.changed`. `Publish` appends to the `events` table (`eventpg`:
  insert + `pg_notify` under advisory lock `7_401_005`, so ids commit in id
  order and "id > last seen" never skips one); every process LISTENs on its
  own connection (plus a 2 s poll) and fans out to its subscribers, so any
  replica streams every replica's events. The SSE id is the row id: a client
  resumes on any replica; an id older than `config.EventRetention` (swept)
  or never issued gets `reset` (client refetches). Subscribe replays
  `(last, head]` from the table and its channel carries what comes after
  head, up to 1000 events, else `reset` (no gap, no duplicate). Slow
  subscribers are dropped, never block publishers; a failed append is
  logged (events notify, the change is already stored). The bus starts in
  `Build` (a port that can't read its log doesn't start). Payloads are
  stored in clear for an hour: never publish secrets — `dock.updated` goes
  through `docksvc.announce`, which drops the pairing QR (the web refetches
  a linking dock; `TestEventsNeverCarryTheQR`).
- Live-only events (`event.Flasher`, `Bus.Flash`): what an agent is doing
  (`crew.activity`) is flashed, not published: never stored, no SSE id
  (so it never moves a client's `Last-Event-ID`), never replayed. The bus
  fans a flash out to this process's subscribers of the organization
  (skipped for a full queue, never a drop) and, when its Log implements
  `event.Flashes` (`eventpg`: `NOTIFY seaport_flashes`), to every other
  process, which drop their own by `Boot`. Payloads stay under
  `event.MaxFlash` (NOTIFY's 8000 bytes). The connector's `feed` batches a
  turn's ACP updates (thought/message chunks, tool calls: title, kind and
  status only, never input or output) into `activity` frames every 300 ms
  and sends the last before its ack/release; the hub accepts them only for
  a delivery open on that connection (late ones dropped quietly), checks
  `crew.Activity.Validate` and flashes them with the conversation and
  agent filled in. The web keeps them outside React Query
  (`src/lib/activity.ts`), cleared on `reset` and, once the delivery
  ends, when its turn summary arrives (or after a short grace).
- Turn summaries (`ship/turn.go`, migration 00024): the hub gathers each
  delivery's tool calls from its activity frames (`crewws/turn.go`,
  `turnLog`: title/kind/status, timed by the hub, at most
  `ship.MaxTurnTools`, the rest counted) and on ack/release/close
  (outcome `acked`|`released`|`dropped`; none for an expired lease or a
  turn without tools) calls `ship.Commands.RecordTurn`, best effort,
  which redacts titles (`ship.ToolTitle`: `text.Redact` with
  `text.Secrets`, the patterns harness homes refuse), stores the row
  (`ship_turns`, keyed by delivery, idempotent) and publishes `ship.turn`.
  Thoughts and message text are never stored. `GET
  /api/v1/conversations/{id}/turns` (`ships:read`) lists them; the web
  folds each under the ships before its `ended_at`.
- Modes (`SEAPORT_AUTH`, plan `~/.rness/plans/puerto/modes-and-tenancy.md`):
  auth is the `httpx.Authenticator` port, picked once in `bootstrap`
  (`Open` for `none`, `Tokens` for `token`, `authhttp` for `iam`); it puts an
  `httpx.Principal` (organization, role, subject, permissions) in the request
  context. Handlers take the organization from it (`httpx.Organization(r)`)
  and routes check permissions with `httpx.Require`; single-owner modes
  grant every permission. Services never see the mode. `none` is loopback-only: `config.Validate` refuses other listen
  addresses, and `httpx.Open` refuses foreign `Host` names (DNS rebinding)
  and requests with forwarding headers (a local proxy/tunnel would make
  remote callers look local). Public routes (`/healthz`, `/api/v1/config`)
  reveal nothing about any organization.
- Tenancy: every tenant row has `organization_id`; single-owner modes use
  `config.DefaultOrganization`. Child rows reference parents by
  `(organization_id, id)` composite FKs, so a row can't point into another
  organization. Another organization's row is `NotFound`, never
  `Forbidden`. Events carry their organization and `/api/v1/events` streams
  only the caller's (and only event types its permissions may read,
  `bootstrap.EventPermissions`); a crew connection and each MCP call act for the token's
  organization; deliveries go only to that organization's agents.
- Crew tokens (`crew.Token`, `sp_crew_…`): only the SHA-256 is stored; the
  value is shown once. Revoking one closes its live connections in every
  process (the hub tracks `Principal.Credential`; see crew across processes). At most `crew.MaxTokens` active per
  organization: `crewpg.Create` counts and inserts under a per-organization
  advisory lock (`tokenLock`). Every crew token belongs to one agent
  (`agent_id`, required): there are no generic or static crew tokens (the
  server ignores `SEAPORT_CREW_TOKEN`, with a warning).
- Admin auth (token mode): bearer token, or the `seaport_session` cookie
  (stateless HMAC of the expiry keyed by the admin token) from
  `POST /api/v1/session`; cookie writes go through
  `http.CrossOriginProtection` (also in `none` mode, for every write).
- Admin auth (iam mode): `Authorization: Bearer` (IAMKit API key `ik_key_…`
  or access token, introspected per request) wins over the cookie. Browser
  sign-in is a server-side OAuth code + PKCE flow (`/api/v1/session/login` →
  IAMKit → `/auth/callback`, state in a sealed short-lived cookie); the
  id_token must carry the sign-in's nonce, name the access token's subject
  and match its `at_hash` when present. The
  `seaport_session` cookie is a random id whose hash keys `web_sessions`,
  where IAMKit's tokens are sealed (AES-GCM, key = SHA-256 of
  `"seaport-seal:"+SEAPORT_SECRET_KEY`, kept as is so existing sessions open
  — `TestSealerOpensOldValues`; placeholder-looking keys are refused); rows sealed
  with another key read as signed out. Refresh happens
  ahead of expiry under the row lock (`FOR UPDATE`): IAMKit rotates refresh
  tokens, so concurrent requests must not both use one. Sign-out deletes the
  row, revokes at IAMKit and returns its end-session URL. Tokens without an
  organization (service accounts) are refused.
- Dock ownership across processes (`dock.Owners`/`dock.Relay`, `dockpg`,
  `docksvc/cluster.go`, migration 00011): a dock of an exclusive channel
  (`Gateway.Exclusive`: WhatsApp; `api` is not) runs in exactly one process,
  the holder of its `dock_owners` lease (`config.DockLease`, 30 s; process
  id made in `bootstrap`, shared with the crew hub). Link, unlink and send
  `route` to it: here when this process holds (or just took, `Acquire`)
  the lease, else a `dock_requests` row + `NOTIFY seaport_docks` that the
  owner serves (`Relay.Serve`; reply and typed errors back by `NOTIFY`
  plus a 500 ms poll; the caller waits 2 min at most). A request still
  pending when its owner's lease ran out comes back `DOCK_MOVED` and is
  routed again (5 tries, growing pause); one already running is never
  retried (a send may have gone out). Request rows hold the outgoing text
  in clear until answered (minutes at most). The owner
  re-checks the lease under a per-dock mutex and stores the resulting
  status there. Each process renews its leases every TTL/5 (each renew
  bounded by TTL/5); a missed one is re-acquired or dropped and released
  (connection stopped). Fencing: while no renew succeeded for half a
  lease, the process owns nothing (`owns`) and a watchdog (every TTL/10,
  no dock lock) stops every connection, since another process may take
  the docks. Every TTL/3 it claims up to 10 ownerless linked/linking
  docks, restores its linked ones that aren't running (backoff up to
  5 min), marks a linking dock with no pairing anywhere for a minute
  failed (its process died), releases logged-out/failed ones and stops
  connections of docks it doesn't own (claiming is first come, not
  balanced). `App.Close` hands docks over (refuses routed work, waits up
  to 15 s for work in flight, stops each connection under its dock's
  lock, then `ReleaseAll`). e2e: `docks_replicas_e2e_test.go` runs
  several processes over a fake exclusive gateway (`world`, fails if two
  processes connect one dock; it also delivers inbound messages to the
  process running the dock: `Options.Gateways` with `BindReceiver`).
  The WhatsApp pairing code lives sealed in `wa_pairings` (purpose
  `pairing`/dock), so any process shows it; message ids need no session
  (`whatsmeow.GenerateMessageID`). `StreamReplaced` (another connection of
  the device) ends the local session and pauses its restore (30 s,
  doubling while it repeats, up to 10 min). Unlinking a linked dock the
  owner hasn't reconnected yet connects it briefly to log out (the phone
  drops the device); if that fails the keys are deleted anyway.
- GitHub docks (`dockgithub`, migration 00022): a dock is one GitHub App
  made by the manifest flow (`POST /api/v1/docks/{id}/github/manifest` →
  form POST to GitHub → `GET /github/apps/created` trades the code →
  install → `GET /github/apps/installed`), both redirects bound to the
  dock by a sealed state (1 h, kind manifest|install). `github_apps` holds
  the App's private key, webhook and client secret sealed (info `seaport
  github v1`, purpose `github-app\x00<dock>\x00<field>`; counted by
  `bootstrap.SecretKey`), `github_installations` its installations; the
  dock is linked (address = App slug) while one installation is active,
  else logged_out; `installation` webhooks keep both in step. Not
  exclusive: any process takes webhooks and sends. `POST
  /github/webhook/{dock}` checks the HMAC with that dock's secret, then
  forwards only messages that mention `@<slug>` or an agent of the team
  (`dockgithub.Teams` = agent service, bound late), never bots' (no
  loops); thread = conversation `owner/repo#N` (group, addressed), ship
  ids `comment-/review-/review-comment-/issue-/pr-<id>`. `Send` posts an
  issue comment with an installation token (cached until 5 min before
  expiry). Off without a secret key. Agents act as the App through
  `POST /crew/github/token` (crew auth, agent tokens only;
  `Gateway.AgentToken`): a token scoped to one repository, only where the
  App of one of the agent's GitHub docks has an active installation (else
  NotFound); `seaport github-token`, the `gh` shim (seaport binary called
  as `gh`, real gh at `/usr/local/bin/gh` in the base images) and
  `git-credential` (fallback after the environment's credentials) ask it
  each time; nothing on disk. Workflows send messages through the public
  `POST /github/actions/{dock}` (`dockgithub/actions.go`, composite action
  `action/action.yml`): bearer = the job's GitHub Actions OIDC token
  (`oidc.go`: RS256 against the issuer's JWKS, `SEAPORT_GITHUB_OIDC_URL`;
  audience = `SEAPORT_PUBLIC_URL`), its repository must be served by an
  active installation of the dock's App; the ship is `@actor` +
  `github:action`, addressed, external id
  `action-<run>-<attempt>-<id|hash>`. Unlink uninstalls every installation
  and deletes the App row (the App itself stays on GitHub).
- Jira docks (`dockjira`, migration 00025): a dock is one Jira Cloud
  service account, linked by `PUT /api/v1/docks/{id}/jira` (`{site, email,
  api_token}`: the site must match `SEAPORT_JIRA_HOSTS`, default
  `*.atlassian.net`, since the credentials are only ever sent there; no
  redirects followed; verified with `GET /rest/api/3/myself` before
  anything is stored). `jira_sites` holds the token and a generated webhook
  secret sealed (info `seaport jira v1`, purpose `jira\x00<dock>\x00<field>`;
  counted by `bootstrap.SecretKey`); the secret is answered only when made
  (first link, `POST …/jira/secret`), never read back; the dock's address is
  the site's host. Not exclusive, stateless, off without a secret key. Public
  `POST /jira/webhook/{dock}` accepts `X-Hub-Signature: sha256=<hmac>` or
  `Authorization: Bearer <secret>` (Automation), both constant time, and
  forwards `comment_created` and `jira:issue_created` only when the text
  mentions the account or `@<agent>` of the team (`dockjira.Teams`, bound
  late), never other apps' comments (no loops); conversation = issue key
  (group, addressed, `Inbound.ChatHandles` key + project), ship ids
  `comment-<id>`/`issue-<id>`. Bodies are ADF (REST) or wiki markup (admin
  webhooks), both normalized to Markdown (`adf.go`). `Send` converts
  Markdown to ADF (goldmark, `md.go`), splits at `MaxComment` and stamps each
  comment with the comment property `seaport` = the message id, so the
  account's own comments come back as `Inbound.Own` (the stamp is the
  ExternalID, else `comment-<id>`). Files are a `Business` refusal. A 401/403
  from Jira makes the dock `logged_out`; unlink deletes the row (nothing is
  revoked in Jira).
- Unlinking a WhatsApp dock logs the device out and deletes its keys;
  deleting a dock cascades to conversations, ships and customs (FKs).
- WhatsApp store (`dockwa/store*.go`, migration 00008): Seaport's own
  implementation of whatsmeow's store interfaces in the `wa_*` tables, one
  device per dock (rows keyed by `dock_id`, cascade from `docks`; the
  phone↔LID map is per dock too, so organizations share nothing). Every
  secret (private keys, signal sessions, sender keys, app-state keys,
  message secrets, privacy tokens, NCT salt, buffered plaintexts) is
  sealed (`internal/seal`, info `seaport whatsapp v1`) with a purpose naming
  table, dock and row, so a value copied to another row doesn't open.
  whatsmeow's `sqlstore` keeps keys in clear and is banned outside tests
  (`forbiddenEverywhere`); `store_test.go` runs one script against both and
  requires identical answers. Migration 00012 dropped whatsmeow's own
  `whatsmeow_*` tables (clear text, before 00008; the release before it
  copied their devices in at start); it refuses while a linked dock's
  device is only there (`TestMigrationDropsTheOldStore`, via
  `bootstrap.MigrateTo`). A dock's `address` is its device JID.
- `SEAPORT_SECRET_KEY` (required in iam mode) else `SEAPORT_SECRET_KEY_FILE`,
  generated on first start (`bootstrap.SecretKey`: temp file 0600, fsync,
  link into place) — never while `wa_devices`, `agent_secrets`,
  `ship_attachments`, `hosting_homes`, `github_apps` or `jira_sites` has rows (a new key can't open them: start refuses). `dockwa.Open` opens one device first (`CheckKey`),
  so a changed key stops the start instead of failing every dock.
  `internal/seal` derives the WhatsApp key by HKDF (info
  `seaport whatsapp v1`); the auth sealer's legacy derivation differs, so
  the two never share a key.
