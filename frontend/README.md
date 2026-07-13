# SAK Media Server — frontend

SolidJS + Vite single-page app, compiled at build time to static
HTML/JS/CSS. **No Node.js runs in production** — the Go binary embeds and
serves the built assets exactly as it always has (`internal/web`, via
`//go:embed static`). This directory is the source; the compiled output is
generated, gitignored, and never committed (plan Guardrail #6).

## Status

Complete and live. This is the production frontend — auth boot (3-way
setup/login/app branch, password + OIDC + break-glass), Discover with
one-click auto-grab per mode, Rename, Purge, Dedup, Tag, and full Settings
(incl. Advanced Settings) are all ported. The old vanilla-JS
`internal/web/static/index.html` was removed in the Stage 5 atomic cutover;
`pnpm build` output now IS the embedded `static/` tree. See
`.omc/plans/frontend-redesign-seerr.md` for the full history.

## Toolchain

| Tool | Version | Pinned in |
|---|---|---|
| Node | 22.x | `.nvmrc`, `package.json` `engines`, Dockerfile |
| pnpm | 9.15.9 | `package.json` `packageManager`, Dockerfile |
| Vite | 6.x | `package.json` |
| SolidJS | 1.9.x | `package.json` |
| Tailwind CSS | 4.x (via `@tailwindcss/vite`, no config file / no PostCSS) | `package.json` |

`pnpm-lock.yaml` is committed; CI/Docker installs use `--frozen-lockfile`.

## Bootstrap (local dev)

From this `frontend/` directory:

```sh
pnpm install        # installs deps from the committed lockfile
pnpm build          # type-checks, builds, and reports gzipped JS size
```

`pnpm build` writes the compiled bundle directly into
`../internal/web/static/` (index.html + assets/). That directory **is** the
production frontend now (Stage 5 atomic cutover — the old hand-written
`static/index.html` is gone) and is entirely generated/gitignored, so a bare
local `go build ./cmd/sakms` fails cleanly with `pattern static: no matching
files found` until `pnpm build` has populated it — run `pnpm build` first.

### Commands

| Command | What it does |
|---|---|
| `pnpm dev` | Vite dev server with HMR (source lives here; nothing is embedded). |
| `pnpm typecheck` | `tsc --noEmit`, strict mode. |
| `pnpm build` | `tsc --noEmit` → `vite build` → gzipped-JS size report. Output → `../internal/web/static/`. |
| `pnpm preview` | Serve the built bundle locally to sanity-check production output. |

## Layout

```
frontend/
├── index.html              Vite entry HTML
├── package.json            deps + scripts, pinned Node/pnpm
├── pnpm-lock.yaml          committed lockfile
├── tsconfig.json           strict TS, Solid JSX (jsxImportSource: solid-js)
├── vite.config.ts          Solid + Tailwind plugins; outDir → embed dir
├── .nvmrc                  Node 22
├── scripts/
│   └── report-size.mjs     build-time gzipped-JS size report (soft 200 KB)
└── src/
    ├── index.tsx           mount point (render → #root)
    ├── index.css           Tailwind import + dark-theme @theme tokens
    └── App.tsx             placeholder root component (replace with real views)
```

## For the next worker (building real views)

- **Dev loop:** `pnpm dev`, open the printed localhost URL. HMR is live; you
  do not need the Go backend running to iterate on pure UI, but API calls
  will 404 until you proxy or run the backend (wire a Vite dev proxy to the
  Go server when you start hitting `/api/*`).
- **Where components go:** `src/`. Keep `src/index.tsx` as the single mount
  point and `src/App.tsx` as the single root; replace `App`'s body with the
  real shell/router.
- **Routing:** none is set up yet — deliberately. When you add a client-side
  router (e.g. `@solidjs/router`), the Go side already supports SPA
  deep-links: `internal/web/web.go` serves the requested file if it exists,
  else falls back to `index.html`. Your router must never claim any `/api/*`
  path (reserved for the backend, incl. the OIDC callback).
- **Theme:** use the Tailwind utilities generated from `src/index.css`'s
  `@theme` block (`bg-bg`, `bg-surface`, `text-fg`, `text-muted`,
  `border-border`, `bg-accent`, …) instead of hard-coded hex, so the palette
  stays in one place.
- **API types:** the Go→TS DTO layer already exists at
  `internal/apidto/ts/dto.gen.ts` (regenerate with `go run ./cmd/gendto`).
  Import from there rather than hand-writing request/response shapes. Note
  the three-state secret rule (`ConnectionUpsertRequest.APIKey`): omit the
  field entirely on an untouched input — never send `""` — or stored secrets
  get wiped. See `internal/apidto/README.md`.
- **Bundle size:** `pnpm build` prints the gzipped JS total and flags (does
  not block) anything over the 200 KB soft ceiling.
