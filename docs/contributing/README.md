# Contributing

Set up a development environment for Seaport, run the checks CI runs, and follow the conventions that keep the codebase consistent.

Seaport is a Go service (the **port**) with an embedded React web app and a connector in the same binary. [AGENTS.md](../../AGENTS.md) is the engineering reference: layout, rules and invariants with their reasons. Read it before a non-trivial change, and read the [architecture](../architecture.md) page for the map.

## Prerequisites

- Go 1.26 (see `go.mod`).
- Docker, for the development Postgres, the IAMKit end-to-end tests and the image build.
- Node 22 and pnpm, for the web app in `web/`.
- Optional: [goreleaser](https://goreleaser.com) v2 to try release builds.

## Set up

```sh
git clone https://github.com/Abraxas-365/seaport.git
cd seaport
docker compose up -d                      # development Postgres on 127.0.0.1:5449
(cd web && pnpm install && pnpm build)    # the web app is embedded into the binary
go build ./... && go vet ./...
```

Run the port against the development database:

```sh
export DATABASE_URL="postgres://seaport:seaport@127.0.0.1:5449/seaport?sslmode=disable"
SEAPORT_AUTH=none go run ./cmd/seaport serve
```

For UI work, `cd web && pnpm dev` serves the app with hot reload on `:5173`, proxying `/api` to a running `seaport serve` (`$SEAPORT_URL`, default `:8740`). The design system is catalogued at `/design`. Rebuild with `pnpm build` before `go build` to ship UI changes.

## Test

```sh
go test ./...                             # unit and architecture tests; always run
```

The integration tests need a database of their own and **wipe it**. Create it once, then point the tests at it:

```sh
PGPASSWORD=seaport createdb -h 127.0.0.1 -p 5449 -U seaport seaport_test
SEAPORT_TEST_DATABASE_URL="postgres://seaport:seaport@127.0.0.1:5449/seaport_test?sslmode=disable" \
  go test -count=1 -p 1 ./...
```

Use `-p 1`: packages share that database. Without the variable, the integration tests skip.

The iam mode end-to-end tests start a real IAMKit container (and its Postgres) through testcontainers, so they need Docker:

```sh
SEAPORT_TEST_IAMKIT=1 SEAPORT_TEST_DATABASE_URL=… go test -count=1 -p 1 ./internal/bootstrap/
```

Run the integration tests before finishing any change to a repository, the hub, the MCP tools or the HTTP API. Add the IAMKit tests for anything touching auth, permissions, sessions or tenancy.

### Web

```sh
cd web
pnpm lint && pnpm build                   # eslint, then tsc + vite into web/dist
```

### Generated code and releases

```sh
go generate ./internal/permission         # after editing iam/permissions.json
scripts/iamkit-provision.sh --dry-run     # preview IAMKit provisioning (iam mode)
docker build -t seaport .                 # the image: web + binary, distroless
goreleaser release --snapshot --clean     # release binaries into dist/
```

`go generate` writes the Go permission catalog and `web/src/lib/permissions.gen.ts` from [`iam/permissions.json`](../../iam/permissions.json); drift tests fail if they disagree.

## Continuous integration

[`.github/workflows/ci.yml`](../../.github/workflows/ci.yml) runs on pushes to `main` and on pull requests:

| Job | What it checks |
| :--- | :--- |
| `go` | `gofmt`, `go vet`, then `go test -race -count=1 -p 1 ./...` against a Postgres 16 service and with `SEAPORT_TEST_IAMKIT=1`. In CI a missing test database or IAMKit fails instead of skipping. |
| `web` | `pnpm lint` and `pnpm build` in `web/`. |
| `docker` | builds the image and checks that `seaport version` prints the commit. |
| `images` | builds the hosted agents' base images (`rness`, `claude-code`), checks their contract (uid 1000, `io.seaport.*` labels, entrypoint, harness binary) and an ACP smoke test. |

Pushing a `v*` tag runs [`release.yml`](../../.github/workflows/release.yml), which builds the binaries with goreleaser and publishes them to GitHub Releases. A separate [`website.yml`](../../.github/workflows/website.yml) workflow builds and publishes the documentation site (see [documentation](documentation.md)).

## Conventions

- **Layering.** Domain, ports, service, module, adapters: see [architecture](../architecture.md). Depend on another domain's `Commands`/`Queries` ports, never on its service. Vendor SDKs stay in their adapter.
- **Tenancy.** Tenant-port methods take `(ctx, organization identity.OrganizationID, …)`. Another organization's row is `NotFound`, never `Forbidden`.
- **Errors.** One system, `internal/errx`. `errx.Wrap` keeps an existing typed error's message and returns `error`. 5xx bodies carry a generic message. Postgres errors go through `pgerr.Map` in repositories.
- **IDs.** `identity.ID[T]` (`DockID`, `ConversationID`, `ShipID`, …), never bare strings.
- **Text input.** `text.Required` / `text.Optional` in `Validate()`, `text.Clean` in services. Exported service methods call `Validate()` on their inputs.
- **Permissions.** Name them through `permission.*`, wrap admin routes in `httpx.Require`, and use `useCan(Perm.X)` in the web app. Never write `"seaport:…"` literals.
- **Events.** Services publish after their write succeeds, and every event type maps to a permission in `bootstrap.EventPermissions`. Never publish secrets in a payload.
- **Migrations.** goose SQL in `migrations/`, embedded and applied at start. A new advisory lock takes the next `7_401_00N` key.
- **Web.** shadcn primitives in `components/ui`, Seaport components in `components/seaport` (add new ones to `/design`), status colors only from `tone.ts`, server state through React Query, no inline scripts (CSP is `script-src 'self'`).
- **Tests.** Add a regression that fails without your change. Hold reasons need a Spanish label in `holdLabel` (`TestEveryHoldReasonIsTranslated`).

> [!WARNING]
> Never weaken an architecture test to make a change pass. Fix the code. If a rule is wrong, change the rule in its test and in [AGENTS.md](../../AGENTS.md) together, with the reason. When you introduce a new pattern worth protecting, add a rule.

## Before you open a pull request

1. Explain the user-visible behavior and the boundary it touches.
2. Run `gofmt`, `go vet ./...` and `go test ./...`; run the integration tests (and the IAMKit tests when relevant) for the areas listed above; run `pnpm lint && pnpm build` for web changes.
3. Update the docs and [AGENTS.md](../../AGENTS.md) when behavior or an invariant changes (see [documentation](documentation.md)), and [known limitations](../project/known-limitations.md) when a limit appears or disappears.
4. Say which checks you ran and what remains unverified.

Don't commit secrets, real phone numbers or customer data in code, tests, fixtures or screenshots.
