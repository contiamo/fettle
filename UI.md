# Fettle UI — stack & setup notes

This document is for someone landing on the UI to do styling and layout
work. It's not a tutorial; it's "what you need to know before touching
anything."

## Stack

Server-rendered Go binary. No SPA, no React. Single embedded HTTP server.

| Layer | Tool | Notes |
|---|---|---|
| HTML templating | [templ](https://templ.guide) `v0.3.1001` | `.templ` files compile to `*_templ.go` via `templ generate` |
| CSS | Tailwind CSS v4 (`tailwindcss` CLI) | Single `input.css` entry; output minified to `dist/styles.css` |
| Component primitives | [templui](https://templui.io) `v1.9.5` | Installed components live as `.templ` files in `internal/ui/components/` |
| TS bundling | esbuild | One entry `app.ts` → `dist/app.js`, ESM, ES2020 |
| Interactivity | HTMX 1.x (vendored) | Used for review/outcome form swaps; **no JS framework** |
| Router | go-chi/v5 | Plain handlers, no controller layer |

The dev server listens on `127.0.0.1:7878` by design — local-only, no
auth, no CSRF, no fonts/CDN, no service worker.

## File map

```
internal/ui/
├── init.go                    Load-bearing init() — copies static asset hashes
│                              into templates.CSSVersion / templates.JSVersion.
│                              Blank-imported by server.go. DO NOT remove.
├── static/
│   ├── input.css              Tailwind v4 entrypoint. Theme tokens + dark mode here.
│   ├── embed.go               //go:embed all:dist; computes asset hashes
│   └── dist/                  Embedded into the binary
│       ├── .gitkeep           Forces dir into git
│       ├── styles.css         Built by `task go:tailwind` (gitignored)
│       ├── app.js             Built by `task ts:build` (gitignored)
│       └── js/                templui per-component scripts + htmx.min.js
│           ├── popover.min.js / dropdown.min.js   (templui-installed, committed)
│           ├── floating_ui_*.js                    (templui deps, committed)
│           └── htmx.min.js                         (vendored from sibling consider)
├── templates/                 Page templates
│   ├── types.go               Go-side view structs + context keys (ReviewerContextKey, etc.)
│   ├── layout.templ           Layout shell, header, breadcrumb, theme menu, reviewer indicator
│   ├── runs.templ             Run picker (landing page)
│   ├── run.templ              Per-run findings table / group cards
│   ├── finding.templ          Finding detail (preview, sections, review, outcome)
│   ├── group.templ            Group detail (members, review, outcome)
│   ├── identity.templ         /identity prompt form
│   ├── review.templ           ReviewSection + form + history feed (HTMX swap target)
│   ├── outcome.templ          OutcomeSection + form + history feed (HTMX swap target)
│   └── *_templ.go             GENERATED — `templ generate` writes these. Committed
│                              so plain `go build` works on a fresh worktree.
├── components/                templui-installed primitives. Each subdir is one
│                              component (button, badge, card, dropdown, popover,
│                              icon, table, aspectratio, utils). Installing a
│                              new component drops a folder here AND a script
│                              into static/dist/js/ (see .templui.json).
├── server/
│   ├── server.go              chi router, middleware wiring
│   ├── middleware.go          withReviewer (resolves identity → ctx)
│   ├── runs.go                GET /
│   ├── run.go                 GET /runs/{name}
│   ├── finding.go             GET /runs/{name}/finding/{id}
│   ├── group.go               GET /runs/{name}/group/{id}
│   ├── identity.go            GET/POST /identity
│   ├── mutations.go           POST /runs/{name}/{kind}/{id}/{review|outcome}
│   ├── sections.go            buildReviewView / buildOutcomeView (shared GET+POST)
│   └── preview.go             Code preview reader + safeJoin
└── ts/
    ├── app.ts                 Theme toggle + delegated row-link click handler
    ├── dom.ts                 targetEl helper (Text-node-safe)
    └── tsconfig.json          ES2020, strict, bundler resolution
```

## Build pipeline

Three independent generators feed into one binary:

```
*.templ ──templ generate──▶ *_templ.go ──┐
                                          ├─▶ go build ─▶ bin/fettle
input.css + *.templ ──tailwindcss──▶ styles.css ──▶ embed ─┤
*.ts ──esbuild──▶ app.js ──▶ embed ──────┘
```

**You must regenerate after editing any `.templ` file** before
`go build` will pick up the change. The Taskfile handles this:

```sh
task go:generate     # templ + tailwind + ts in parallel
task go:build        # generate + go build (most useful)
task go:check        # generate + build + vet + test
task ts:check        # tsc --noEmit
task dev:ui          # build + run on examples/self-scan
```

If you edit a `.templ` and forget, `go build` succeeds against the
stale `*_templ.go` and you'll see no change in the browser. The
fastest dev loop is `task dev:ui` — it rebuilds before serving.

A subtle gotcha: `task go:templ` uses Taskfile's `sources`/`generates`
freshness check. If it reports `updates=0` but you definitely edited
a template, `touch` the `.templ` file and re-run, or run the templ
command directly:

```sh
go run github.com/a-h/templ/cmd/templ@v0.3.1001 generate \
  ./internal/ui/templates/ ./internal/ui/components/
```

## Tailwind v4 conventions

- **Entrypoint**: `internal/ui/static/input.css`. Read it before
  changing colors — it defines the entire palette as CSS custom
  properties under `:root` and `.dark`, then bridges them to Tailwind
  utility classes via `@theme inline`.
- **Tokens, not raw colors**: use `bg-primary`, `text-foreground`,
  `border-border`, etc. Avoid `bg-blue-500`-style literals. They
  bypass dark mode.
- **Dark mode**: class-based (`.dark`), driven by `@custom-variant
  dark (&:where(.dark, .dark *))`. The toggle in
  `layout.templ:themeMenu` flips `<html class="dark">`. A pre-paint
  script in `themeInitTag` reads `localStorage["theme"]` to avoid
  light-mode flash on dark sessions.
- **`@source` directives** in `input.css` tell v4 where to scan for
  utility classes. Currently `../templates/` and `../components/`.
  Adding a new directory of `.templ` files? Add an `@source` line.
- **Custom utility extensions**: declared in `@theme` and `@theme
  inline` blocks. `text-2xs` is the only custom size right now.
  Add new ones in `@theme`.
- The `@layer base` block applies `border-border` to every element
  and sets the body's bg/fg. Don't put per-page overrides there.

## templui

templui is NOT a runtime dependency — it's a code generator. The
`templui` CLI (`v1.9.5`, pinned in Taskfile) installs components by
copying `.templ` files into `internal/ui/components/<name>/`. Once
installed, you own them. Edit freely.

**To install a new component:**

```sh
go run github.com/templui/templui/cmd/templui@v1.9.5 add <name>
task go:generate
```

The config (`.templui.json` at repo root) tells templui where to write
the `.templ` and any per-component JS. Per-component JS lands in
`internal/ui/static/dist/js/` — those files are committed (so a fresh
clone builds without templui being on PATH).

**Currently installed**: `aspectratio`, `badge`, `button`, `card`,
`dropdown`, `icon`, `popover`, `table`, plus the shared `utils`
helper.

**Do not** redefine button/badge/card primitives inline. Reach for the
templui component first — it's already wired to the theme tokens.

## HTMX

HTMX is loaded once in `Layout` with `defer` and `htmx-config:
allowScriptTags=false`. The script lives at
`internal/ui/static/dist/js/htmx.min.js`. It's vendored, not on a CDN.

**Where it's used today**: review and outcome form submissions. Each
form has `hx-post` / `hx-target` / `hx-swap="outerHTML"`. The server
returns just the section HTML; HTMX replaces the section in-place.

**Pattern for new mutations**:
1. Form's `hx-target` points at an element with `id="my-section"`.
2. Server-side handler builds a view, calls
   `templates.MySection(view).Render(r.Context(), w)`.
3. Identity check: `requireIdentity(w, r)` — handles both browser
   (303 redirect) and HTMX (`HX-Redirect` header).

**No CSRF**. Server is loopback-only by design. If we ever expose the
UI beyond `127.0.0.1`, this needs CSRF tokens (consider's `csrf.go`
is the reference pattern).

## Theme tokens — quick reference

| Token | Use |
|---|---|
| `bg-background` / `text-foreground` | Default page surface + text |
| `bg-card` / `text-card-foreground` | Card-like raised surfaces |
| `bg-muted` / `text-muted-foreground` | Subdued areas (table headers, hints) |
| `bg-primary` / `text-primary-foreground` | Primary action (currently a soft blue) |
| `bg-secondary` / `text-secondary-foreground` | Less-prominent action |
| `bg-accent` / `text-accent-foreground` | Hover backgrounds |
| `bg-destructive` | Error / danger |
| `border-border` | Default border color |
| `text-destructive` | Inline error text |

Light/dark variants are automatic — these tokens resolve to different
oklch values under `.dark`.

## Conventions

- **Whole-row click**: any element with `data-row-link="<href>"` is
  clickable. Handler in `app.ts` bails on `closest("a")` /
  `closest("button")` so middle-click and inline interactives keep
  working. Use this for table rows and group cards. Always include a
  proper `<a>` inside as well so screen readers + keyboard nav work.
- **Asset cache busting**: links and scripts in `Layout` use
  `assetURL("/static/dist/foo.js", JSVersion)` which appends
  `?v=<sha>`. Hashes are computed at init from the embedded files.
- **Path validation**: route params (`{name}`, `{id}`) get
  regex-checked at the handler level (`runNamePattern`,
  `findingIDPattern`, `inputRunPattern`) before any filesystem join.
  Keep this when adding routes that take user-controlled segments.
- **No silent feature flags / fallback paths**: this codebase prefers
  failing loudly. If you find yourself adding "if A else B" defensive
  branches, ask first.
- **Comments**: only the WHY. Don't narrate WHAT. Existing code is
  consistent — match the tone.

## Reviewer indicator

The header pill ("Reviewing as: `<slug>`" or "Set identity") reads
from `templates.ReviewerFromContext(ctx)`. The `withReviewer`
middleware (`internal/ui/server/middleware.go`) resolves identity once
per request and stashes it in context. Don't re-resolve in templates.

`identityChangeHref(ctx)` builds the link — it pulls
`RequestURIContextKey` to bounce back to the current page after save.

## Iterating on styles

Fast loop:

```sh
task dev:ui &           # rebuilds, starts server on 127.0.0.1:7878
# edit templates/*.templ or input.css
# Ctrl-C the dev server, re-run task dev:ui
```

There's no live-reload. SSE was deliberately deferred (see the plan
file at `.claude/plans/peppy-splashing-piglet.md`). Hard refresh
(`Cmd-Shift-R`) is your friend — the cache-bust query strings on
assets only update after `task go:generate` rebuilds them.

For the run picker and find runs, point `--dir` at
`examples/self-scan/`. To populate it with realistic data:

```sh
task selfscan -- --agent claude --concurrency 4
```

## What's deliberately missing

- **Live reload / SSE**: full-page reload only. Don't add fsnotify.
- **Syntax highlighting** in code preview: deferred. Server-side
  chroma is the planned next step.
- **Walkback for merge/dedupe code preview**: the preview reads
  `manifest.target_repo` directly, which is empty for merge/dedupe.
  Resolving via `members[].from_run` is deferred.
- **Label autocomplete**: review form takes a free-text labels input.
- **Indicator avatar / "team" view**: no notion of who else is reviewing.
- **CSRF**: see HTMX section above.

If you find yourself needing one of these, raise it — don't sneak it
in under styling work.

## Common stumbles

1. Edited a `.templ` and nothing changed in the browser → `task
   go:generate` didn't rerun. `touch` the file and try again, or run
   templ directly.
2. Tailwind classes not applying → either you typo'd, or you used a
   class on a file under a directory that's not in `@source`. Check
   `input.css`.
3. Dark-mode color drifted from light mode → both palettes live in
   `input.css`. Edit them as a pair.
4. New templui component installed but its JS doesn't load → ensure
   the `Script()` tag for that component is in `Layout`'s `<head>`.
   See how `popover.Script()` and `dropdown.Script()` are wired.
5. `_templ.go` shows up in `git diff` after pulling main → run
   `task go:templ` to align with the source `.templ` files. The
   committed `_templ.go` should never drift; if you're seeing
   regenerated diffs, your generator version may not match the
   pinned `v0.3.1001`.

## Reference: where to start reading

- Visual hierarchy and typography decisions live in `layout.templ` +
  `input.css`. Read those two files together.
- For component patterns (badges, buttons, severity coloring), see
  `run.templ:severityVariant` and `run.templ:labelChips`.
- For form patterns (HTMX swap, validation, error inline), see
  `review.templ` + `mutations.go:reviewPostHandler`.
