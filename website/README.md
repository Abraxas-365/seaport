# Seaport website

Static landing page and documentation built with Astro + Starlight. Requires Node
22.12+ and npm. Run from a checkout of the whole repository:

```sh
cd website
npm ci
npm run dev
```

Open <http://127.0.0.1:4340/> for the landing page and `/docs/` for documentation.
For the production build, including Pagefind search, use
`npm run build && npm run preview`. Preview serves the last build; rebuild after
changes. Development mode does not provide the production search index.

## Content and styling

- `../docs/**/*.md` is the only documentation source. No generated Markdown,
  copies or front matter. The loader derives titles and descriptions from each
  page's H1 and first paragraph; the remark adapter (`scripts/docs.mjs`) removes
  the duplicate H1, rewrites local Markdown links and turns GitHub alerts
  (`> [!NOTE]`, `[!TIP]`, `[!IMPORTANT]`, `[!WARNING]`, `[!CAUTION]`) into
  Starlight asides.
- Documentation lives under `/docs/`. `src/pages/index.astro` is the custom
  landing page at `/`, styled with `src/styles/landing.css`. Its theme toggle
  persists the same preference as Starlight.
- Screenshots live in `../docs/assets/screenshots/` (the web app, dark theme,
  fictional data). Docs reference them relatively; the landing page imports
  them, so Astro resizes and fingerprints them. Never capture real numbers,
  names or tokens: the repository is public.
- Links to repository files outside `docs/` go to GitHub. A link to a missing
  file fails the build.
- `src/styles/tokens.css` owns the colors (the web app's palette from
  `web/src/index.css`), fonts (Geist, like the web app) and radius.
  `src/styles/theme.css` maps them to Starlight's variables.
- The sidebar is curated in `src/sidebar.mjs` (reading order, nested groups).
  Titles come from each page's H1. `npm test` fails when a document under
  `../docs` is missing from the sidebar or listed twice, so add new pages there.
- Landing copy follows the root README; keep install commands and claims in
  sync with it.
- Internal links on the landing page go through `u(...)`, which adds the deploy
  base. Do not hard-code `/docs/...` paths.

## Validation

```sh
npm test
npm run check
npm run build
npm run check:links
```

`check:links` checks rendered local links, anchors and assets, not remote URLs.
Starlight logs harmless warnings for its empty optional i18n collection and
default 404 fallback.

## Deployment

`.github/workflows/website.yml` publishes to GitHub Pages at
<https://abraxas-365.github.io/seaport/> on every push to `main` that touches
`website/`, `docs/` or the workflow. It runs `npm test`, the build and
`check:links` before deploying. Pages must be enabled once in the repository
settings with "GitHub Actions" as the source.

The site lives under `/seaport/`, so CI sets `SITE_BASE=/seaport`. Locally
`SITE_BASE` is unset and the site serves from `/`. To reproduce the Pages
build: `SITE_BASE=/seaport npm run build && SITE_BASE=/seaport npm run check:links`.
For a custom domain, set `SITE_URL` and drop `SITE_BASE` in the workflow.
