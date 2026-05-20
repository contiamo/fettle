# CLAUDE.md — fettle

Fettle is an agent-driven LLM audit harness. The agent-led setup flow (`README.md` → `SETUP-WITH-AGENT.md` → bundled skill) is the front door, and most users will never type a fettle command themselves. Treat that flow as the primary UX — anything that makes it rougher should be reconsidered.

## Where to look

- [`context/FETTLE.md`](context/FETTLE.md) — design model, project layout, CLI surface, manifest shape, storage / atomicity / resolution rules. **Read first for anything touching the data model or CLI.**
- [`context/conventions/go.md`](context/conventions/go.md) — Go 1.26, modern idioms, mandatory patterns. **Required reading before writing Go in this repo.**
- [`context/UI.md`](context/UI.md) — UI stack (templ + Tailwind v4 + HTMX, embedded assets). **Required reading before touching `internal/ui/`.**
- [`internal/skill/skills/claude-code/fettle/SKILL.md`](internal/skill/skills/claude-code/fettle/SKILL.md) — the skill that drives the user's coding agent through fettle setup. The single most user-facing artifact in the repo; tread carefully.

## Build / test

`task` is the entry point. Common targets:

- `task go:install` — install the binary to `$GOBIN/fettle` (default `~/go/bin/`). Chains `go:templ`, `go:tailwind`, and `ts:build`.
- `task go:test` — Go test suite.
- `task go:templ` / `task go:tailwind` / `task ts:build` — regenerate templ output / Tailwind CSS / bundled JS. Must run after touching `*.templ` files, `internal/ui/static/input.css`, or `internal/ui/ts/*.ts` (or just use `task go:install`, which chains all three).
- `task setup:check` — verify tool prerequisites (templ, tailwindcss, esbuild, etc.).

**The generated UI artifacts under `internal/ui/static/dist/` (`styles.css`, `app.js`) are committed** — `go install ...@latest` builds from the module proxy, which only includes what's in git, so excluding generated assets would ship a styles-less UI to users. Run `task go:install` before committing any change to templ / input.css / ts sources to keep the committed artifacts in sync. (No CI guardrail yet; consider adding a `task go:install && git diff --exit-code` job.)

## Contributing

- **PRs only.** Never push to `main` directly; the auto-mode classifier will block it and we've agreed on the convention.
- **Branch name:** `<type>/<kebab-desc>` — e.g. `feat/skill-budget-gate`, `fix/install-skill-scope`. The `<type>` is a Conventional Commits type (`feat`, `fix`, `docs`, `refactor`, `perf`, `test`, `ci`, `chore`, `revert`).
- **Commit message:** [Conventional Commits](https://www.conventionalcommits.org/) — `<type>(<optional scope>): <description>`. Subject under 72 chars; body explains the *why*, not the *what*.
- **PR body:** `## Summary` with 1–3 bullets; `## Test plan` only for checks you've actually run (past tense, not TODO for the reviewer).
- **Self-scan** for non-trivial CLI or skill changes: `examples/self-scan/` is the canonical dogfood test bed. Run it locally before opening the PR to catch regressions in fettle's own conventions.

## The skill is embedded

`internal/skill/skills/claude-code/fettle/SKILL.md` is `//go:embed`'d into the binary. **Edits to the source have no effect on installed users until you rebuild *and* reinstall:**

```bash
task go:install
fettle install-skill claude-code --force
```

Easy to forget — the source file looks updated, but the installed copy at `~/.claude/skills/fettle/SKILL.md` doesn't change until you re-install. When validating skill changes, install to a temp dir to confirm: `fettle install-skill claude-code --output /tmp/skill-test`.

## Releases

Tags drive `go install ...@latest` resolution and what `fettle --version` reports. Cut a release from `main` after the relevant PRs are merged:

```bash
git checkout main && git pull
git tag v0.X.Y
git push origin v0.X.Y
```

Tags are effectively immutable once pushed (Go module proxies cache them). **Don't retag** — bump to the next number instead.

**Semver, pre-1.0:**

- **Patch (`v0.X.Y` → `v0.X.Y+1`):** skill content, docs, bug fixes, internal refactors, anything users won't notice in the CLI surface.
- **Minor (`v0.X.0` → `v0.X+1.0`):** new CLI flags or subcommands, new behavior users will see.
- **Major (`v1.0.0`):** breaking changes to CLI or on-disk format. Pre-1.0 the rules are loose; minor bumps can carry breaking changes if needed, but flag it in the PR.

After tagging, `go install github.com/contiamo/fettle/cmd/fettle@latest` resolves to the new tag. `fettle --version` reports the tag cleanly when installed via the module proxy.

## Repo layout (orientation)

```
cmd/fettle/               # cobra CLI
internal/project/         # project dir / fettle.json / stubs
internal/skill/skills/    # //go:embed'd skill bundles
internal/run/             # run folder model, manifest, summary
internal/walk/            # file enumeration (git ls-files or fs)
internal/agent/           # agent invocation contract
internal/schema/          # finding / review / outcome shapes
internal/ui/              # templ + HTMX UI, embedded assets
examples/self-scan/       # fettle scanning itself — dogfood test bed
context/                  # FETTLE.md, UI.md, conventions/
```
