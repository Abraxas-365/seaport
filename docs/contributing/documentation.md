# Contributing documentation

How Seaport's documentation is organized, the format its pages must follow, and how the website renders them.

The Markdown files in `docs/` are the only documentation source. GitHub renders them as they are, and the website under `website/` renders the same files, so a page has to work in both.

## Organization

English is the canonical language. Pages are grouped by reader need (the [Diátaxis](https://diataxis.fr) split):

| Directory | For | Example |
| :--- | :--- | :--- |
| `tutorials/` | a guided path with an observable result | [First port](../tutorials/first-port.md) |
| `guides/` | one operational task (`guides/hosting/`, `guides/operations/` group related ones) | [Customs](../guides/customs.md) |
| `reference/` | exact contracts: configuration, CLI, HTTP API, events, protocol, limits | [Configuration](../reference/configuration.md) |
| `explanation/` | rationale and mental models | [Concepts](../explanation/concepts.md) |
| `project/` | limitations and compatibility | [Known limitations](../project/known-limitations.md) |
| `contributing/` | development workflows | [Contributing](README.md) |

[`architecture.md`](../architecture.md) sits at the top level for contributors. Create a page when there is verified content for it; don't scaffold empty pages.

## Format rules

The website build depends on these:

- **No front matter.** The first line is `# Title`. The first paragraph after it is the page description: one or two plain sentences (about 160 characters), ideally without links or code.
- **Plain GitHub Markdown.** Tables, fenced code blocks with a language (`sh`, `yaml`, `json`, `text`, `go`), and relative links between pages, such as `../reference/cli.md#connect`. Anchors are the GitHub slug of the heading.
- **Links are checked.** A relative link to a file that doesn't exist fails the website build. Links to files outside `docs/` (`../../AGENTS.md`, `../../iam/permissions.json`) are allowed and become GitHub links. Link to files, not to line numbers.
- **Callouts** use GitHub alert syntax: `> [!NOTE]`, `> [!TIP]`, `> [!WARNING]`. Use them sparingly.
- **Screenshots** live in `docs/assets/screenshots/` and are referenced relative to the page: `![Inbox](../assets/screenshots/inbox.png)`. They show the dark theme with fictional data only.
- **Style.** Concise and concrete, second person ("you"). Say why briefly, then how. No marketing language.

### Vocabulary

Use the product's terms: **port** (the Seaport server), **dock** (a channel account: a WhatsApp number or `api`), **ship** (a message), **crew** (connected agents), **customs** (who may wake an agent), **harness** (the coding agent program: rness, Claude Code). The web app is in Spanish, so name its screens as they appear: **Muelles** (Docks), **Aduana** (Customs), **Bandeja** (Inbox), **Agentes** (Agents), **Entornos** (Environments), **Tripulación** (Crew), **Tokens de agentes** (Agent tokens).

## Public repository rules

The repository is public. Never put in docs, examples or screenshots:

- real phone numbers, WhatsApp JIDs, domains, emails, tokens or keys. Use `+34600000000` to `+34600000009`, `example.com`, `port.example.com`, `sp_crew_…` (elided) and variable names such as `ANTHROPIC_API_KEY` without a value;
- customer names or message contents. Use made-up names such as Ana Torres.

## Source of truth

Read the code before documenting a contract, and trace behavior through the service and the adapter, not only a comment, which may describe an earlier design. Verify every flag, environment variable, route, limit and field against `cmd/seaport`, `internal/config`, the domain packages and [`iam/permissions.json`](../../iam/permissions.json), or run `seaport <command> -h`. If you can't verify a claim, leave it out.

[AGENTS.md](../../AGENTS.md) holds the engineering invariants. Keep it and the docs current with behavior in the same change: a new environment variable, route, permission, limit or invariant updates the matching reference page, [known limitations](../project/known-limitations.md) if relevant, and AGENTS.md.

## Examples

- Never include real secrets. Pass secret values through stdin or environment variables in examples.
- Say what each user-chosen name is (an agent, an environment, a dock).
- State external prerequisites, such as a running port or an installed harness.
- Show commands you run (`sh`) separately from YAML specs, JSON bodies and wire frames.

## Preview the website

`website/` is an Astro Starlight site that reads `../docs`. It needs Node 22.12 or later:

```sh
cd website
npm ci
npm run dev           # http://127.0.0.1:4330, search is not indexed in dev
npm run build         # production build with search
npm run check:links   # rendered links, anchors and assets (run after build)
```

`npm test` runs the loader tests, including one that fails when a page under `docs/` is missing from the sidebar or listed twice.

The page title and description come from the H1 and the first paragraph, and the build removes the duplicate H1 and rewrites relative links.

> [!WARNING]
> The sidebar is curated by hand in `website/src/sidebar.mjs`. Every new page must be listed there, or `npm test` fails.

The `website.yml` workflow runs the tests, the build and the link check, then publishes the site on pushes to `main` that touch `website/` or `docs/`.

## Before you submit

- [ ] The page starts with `# Title` and a plain description paragraph, with no front matter.
- [ ] Relative links resolve to existing files and headings; `npm run build` and `npm run check:links` pass.
- [ ] Commands, flags and variables agree with the code and `--help`.
- [ ] No real numbers, domains, emails or keys; screenshots use fictional data.
- [ ] The new page is in `website/src/sidebar.mjs`.
- [ ] [Known limitations](../project/known-limitations.md) and [AGENTS.md](../../AGENTS.md) are updated when behavior changed.
