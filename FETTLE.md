# Fettle

A harness for running LLM agents over a codebase to find and review
issues. You provide the instructions in markdown; fettle handles the
orchestration. Output is JSON on disk, one file per finding.

Fettle is a **file-oriented** harness: it runs an agent per file matching
your globs, and a finding's primary anchor is `(file, line)`. That makes it a
good fit for refactor sweeps, convention enforcement, doc-comment audits,
license-header checks, and lint-shaped security checks. It is *not* a fit
for analyses where the natural unit is bigger than a file — cross-file data
flow, route/middleware authorization, runtime accessibility, dependency-
graph compliance. v0 trades that range for a sharper contract.

What you're looking for lives in your markdown prompts. The harness ships;
the knowledge is yours.

## Pipeline

```
  find        →     review          →     close
  per-file          per-finding            track
  agent scan        agent and/or human     outcomes
                    review
```

`find` creates a run folder under `.fettle/runs/`. `review` and
`outcome` are operations on existing runs — their results land
inside the target run, not in their own folders. Stages are
independent — you can stop after `find` if a report is all you
need, or hand the run to a human reviewer via the UI without ever
invoking the review agent.

## Project layout

`fettle init` creates a `.fettle/` directory inside the project's
host directory (typically your repo root). Every fettle artifact
lives there — config, instructions, runs — so fettle stays out of
the host repo's root the same way `.git/` does. **`find` creates a
run folder** under `.fettle/runs/`, named with the stage prefix and
a UTC timestamp. `review` and `outcome` write into an existing run.

```
my-repo/                        host directory (your repo root)
  .git/
  .fettle/                      everything fettle owns lives here
    config.json                 marker + project config
    instructions/               editable templates (seed for new runs)
      find.md
      review.md
    runs/
      find_20260430T145233Z_security-v1/
        run.json                manifest: who/what/when
        instructions/find.md    snapshot of the find prompt that ran
        files.jsonl             per-file scan ledger (find-stage only)
        raw/                    verbatim agent output, one log per file
        findings/
          abc123def456.json     one finding per file (see below)
          ...
```

`find` creates a new run on each invocation, or continues an
existing one via `--resume .fettle/runs/<name>/`. There are no
other agent-driven stages today — `review` runs against a `find`
run.

**Run folder naming**: `<stage>_<UTC-timestamp>_<slug>/` where
`<stage>` is `find` (the only stage today). Timestamp format is
`YYYYMMDDTHHMMSSZ` so runs sort chronologically and same-day runs
don't collide. The slug defaults to a short random suffix; `--name
<slug>` overrides just the slug portion. Resuming a killed `find`
is `fettle run find --resume .fettle/runs/<name>/`.

## `run.json` manifest

```json
{
  "name": "find_20260430T145233Z_security-v1",
  "stage": "find",
  "fettle_version": "0.1.0",
  "created_at":   "2026-04-30T14:52:33Z",
  "completed_at": "2026-04-30T15:08:11Z",
  "target_repo": "/abs/path/to/repo",
  "target_repo_git": { "head": "abc123def", "dirty": false },
  "include": ["**/*.go"],
  "exclude": ["vendor/**", "**/*_generated.go"],
  "agent":  { "name": "claude", "model": "sonnet" },
  "source_path":   ".fettle/instructions/find.md",
  "snapshot_path": "instructions/find.md",
  "args": { "concurrency": 4, "limit": 0 }
}
```

`completed_at` is set when every *attempted* file has a final `ok`
or `empty` row in `files.jsonl`. A `--limit N` run that processed N
files cleanly counts as completed; coverage (whether that truncated
subset is enough) is the user's call, visible via the recorded
`args.limit`. If any file's latest row is `error` or the run was
interrupted, `completed_at` stays absent and the run is recoverable
via `fettle run find --resume`.

`target_repo_git` is best-effort — populated when the target is a git
repo, omitted otherwise.

**Review prompt snapshot**: when `fettle run review --run X` runs
the review agent for the first time, it writes the active prompt
into `.fettle/runs/X/instructions/review.md`. Subsequent automated reviews
in X re-use that snapshot — one prompt per run, which is what you
want when labels from multiple automated reviewers are merged.

Human reviewers via the UI don't consume the prompt files (they're
making direct judgments, not following an LLM prompt), so the
snapshot isn't read for human reviews.

## `.fettle/config.json`

`.fettle/config.json` confirms the directory is a fettle project
and records the fettle version that created it. It also holds the
project's config:

```json
{
  "fettle_version": "0.1.0",
  "created_at": "2026-04-30T10:56:00Z",
  "target_repo": "/abs/path/to/repo",
  "agent": { "name": "claude", "model": "sonnet" },
  "include": ["**/*.go", "**/*.ts"],
  "exclude": ["vendor/**", "node_modules/**", "**/*_generated.go"],
  "instructions": {
    "find":   ".fettle/instructions/find.md",
    "review": ".fettle/instructions/review.md"
  }
}
```

Paths in this config are resolved relative to the host directory
(the one containing `.fettle/`). Instructions can live anywhere —
move them outside `.fettle/` if you'd rather have them tracked
under your normal docs path. `target_repo` is also relative to the
host, so `"../.."` portably points two levels up.

`include` / `exclude` are doublestar globs evaluated against
repo-relative paths. `fettle init` requires at least one
`--include` glob — there's no project-independent default, and a
permissive one (`**/*`) would pull in lockfiles, vendored
dependencies, generated code, and binary blobs. Examples:

```sh
fettle init --include '**/*.go'
fettle init --include 'src/**/*.{ts,tsx}' --include '**/*.css'
fettle init --include '**/*.py' --exclude 'tests/**'
```

The walker hard-skips `.git/`, `.hg/`, `.svn/`, and `node_modules/`
regardless of globs.

## Instructions (you write these)

You supply one markdown document per agent stage (`find` and
`review`). Each tells the spawned agent what to look for, what
fields to emit, what's out of scope.

A minimal `find.md` for a security pass might say: *"Read
`TARGET_FILE`. Flag every place that builds a SQL query by string
concatenation, every `os/exec` call constructed with unescaped
variables, every hardcoded credential or API key. For each finding,
run `fettle add finding` with `--file`, `--line`, `--title`,
`--description`, `--suggestion`, and a relevant `--severity` and
`--label`."* Keep the checks file-local; cross-file data flow is out
of scope for fettle.

Fettle substitutes a small, fixed set of variables when running each stage:

| Stage  | Variables available in the prompt          |
|--------|--------------------------------------------|
| find   | `TARGET_FILE`, `REPO_ROOT`                 |
| review | `SUBJECT_JSON`, `REPO_ROOT`                |

`SUBJECT_JSON` is the finding doc the agent is being asked to
review (just the finding fields — prior reviews are deliberately
*not* surfaced; the review agent makes a fresh judgment per run).

**No stage gets `OUTPUT_PATH`.** Every stage records its output by
shelling to `fettle add <kind>` (see the CLI section). This unifies
the agent contract: the harness owns ids, timestamps, validation,
and file writes; the agent never writes a fettle data file directly.

`REPO_ROOT` is the `target_repo` recorded on the run reviewing
operates against, taken directly from `run.json`.

Inside your markdown you're free to inline or reference additional
knowledge docs (conventions, checklists, threat models, style
guides). Fettle doesn't care what the prompt looks like beyond the
variable substitution.

## The CLI

The CLI is organized verb-first. Five top-level verbs:

- `fettle run <stage>` — start an agent-driven stage (`find`, `review`).
- `fettle add <noun>` — append a record (`finding`, `review`,
  `outcome`). Called by spawned agents (FETTLE_RUN env), or by
  humans via the UI.
- `fettle list <noun>` — list records of a kind in a run, or `runs`
  for all runs in the project.
- `fettle show <noun>` — print one record (or one run's status).
- `fettle init` — create a new fettle project.

`fettle ui` serves the local web UI. `--dir` and `--json` are
global flags. Every command emits the `{"data": ...}` envelope when
`--json` is passed; reads always emit it.

```
fettle init --include GLOB [--include GLOB ...] [--exclude GLOB ...]
            [--target REPO] [--agent claude|codex]
    Create a new fettle project in cwd. Writes .fettle/config.json
    and a stub .fettle/instructions/ tree. At least one --include
    glob is required — see `fettle init --help` for examples.

# Stage runners (agent-driven)

fettle run find [--name SLUG] [--include GLOB] [--exclude GLOB] [-c N] [--limit N]
    Create .fettle/runs/find_<UTC-timestamp>_<slug>/, snapshot the
    find prompt into it, walk the target repo, and write each
    finding the agent emits to its own findings/<id>.json. Prints
    the run path so you can pipe it into the next stage.

fettle run find --resume .fettle/runs/<name>/
    Resume a killed find. Re-uses the snapshotted prompt — editing
    .fettle/instructions/find.md after the run started has no effect.
    Flags that would change run identity (--include, --exclude,
    --limit, --agent, --model, --effort, --agent-script, --name)
    are rejected; the manifest is authoritative.

fettle run review --run .fettle/runs/<name>/ [--agent NAME]
    For each finding in --run not yet reviewed by this agent, run
    the review agent. Append the agent's judgment to the finding's
    reviews[] array via an atomic-rename rewrite of
    findings/<id>.json. Resume keys on the agent's slug — switching
    the model (e.g. claude/sonnet → claude/opus) doesn't force
    re-review.

# Record writes (called by spawned agents, sometimes by humans)

fettle add finding --file PATH --line N --title T --description D --suggestion S
                   [--severity X] [--label k:v ...] [--reference PATH[:LINE] ...]
    Write one new finding to FETTLE_RUN's findings/ directory. The
    new doc is created exclusively (os.Link) so two concurrent
    writers can't race on the same id.

fettle add review --finding ID --label LABEL ... [--severity X] [--comment TEXT]
    Append one review entry to the target finding's reviews[]
    array. Labels and severity follow nil-don't-touch semantics:
    omitting --label leaves prior labels in effect; --clear-labels
    explicitly clears; --severity '' (empty) leaves prior severity
    in effect.

fettle add outcome --run .fettle/runs/<name>/ --finding ID
                   --status STATUS [--pr URL]
    Append an outcome event to the target finding's outcomes[]
    array. Marks the finding as disposed of (PR merged, won't fix,
    etc.). Author identity chains FETTLE_AGENT → $FETTLE_AUTHOR →
    ~/.config/fettle/identity.

# Record reads

fettle list runs
    Print all runs in the project as a JSON array, sorted newest
    first. Each entry has identity, provenance, and a counts block.

fettle show run PATH
    Print one run's summary (status + counts).

fettle list findings  --run .fettle/runs/<name>/
fettle list reviews   --run .fettle/runs/<name>/
fettle list outcomes  --run .fettle/runs/<name>/
    Dump every record of that kind in --run as a JSON array.

fettle show finding   --run .fettle/runs/<name>/ ID
fettle show review    --run .fettle/runs/<name>/ --finding ID [--all]
fettle show outcome   --run .fettle/runs/<name>/ --finding ID [--all]
    Print one record. For review and outcome, default emits the
    derived current state per finding; --all emits the full
    chronological history (including superseded entries).

# Output

All read commands emit `{"data": <records>}` unconditionally. Stage
runners and add commands print plain text to stdout by default
(path / id) so shell pipelines like `out=$(fettle run find)` keep
working; pass `--json` to switch them to the same envelope.

# Server-side metadata

`add finding` assigns `id` / `created_by` / `created_at`; `add
review` and `add outcome` assign only `at` (author derived from
FETTLE_AGENT, FETTLE_AUTHOR, or the identity file). All validate
fields before writing.
```

## Storage shape

Each finding lives in `.fettle/runs/<run>/findings/<id>.json`. Reviews and
outcomes are embedded inside the same file as `reviews[]` and
`outcomes[]` arrays — the file path identifies the finding, so the
inline records don't carry a Subject field.

```json
{
  "id": "abc123def456",
  "file": "internal/foo/bar.go",
  "line": 42,
  "title": "...",
  "description": "...",
  "suggestion": "...",
  "severity": "medium",
  "labels": ["category:duplication"],
  "references": [
    { "file": "internal/baz/qux.go", "line": 33 }
  ],
  "anchor_line": "    return foo(x, y)",
  "created_by": "agent:claude/sonnet",
  "created_at": "2026-04-30T15:01:22Z",
  "reviews": [
    {
      "author": "human:michael",
      "labels": ["confirmed"],
      "severity": "high",
      "comment": "Verified",
      "at": "2026-04-30T16:00:00Z"
    }
  ],
  "outcomes": [
    {
      "author": "human:michael",
      "status": "merged",
      "pr_url": "https://github.com/.../pull/42",
      "at": "2026-05-01T10:00:00Z"
    }
  ]
}
```

`severity` is a free-form string (or null). Fettle doesn't enforce
a scale — your prompt decides whether to use `low`/`medium`/`high`,
`P1`/`P2`/`P3`, a numeric `7.5`, or none.

`labels` is a list of plain strings. The convention for structured
tags is `prefix:value` — e.g. `["cwe:89", "wcag:1.1.1",
"category:duplication", "confidence:high"]`. The UI treats prefixes
as facets so you can filter by "all `cwe:` labels" or "all
`category:` labels."

`references` carries additional code locations the finding points
to — duplication being the obvious case ("this finding exists in N
other files"), but also "see also this related site" findings. Each
entry: `{ "file": "internal/baz/qux.go", "line": 33 }`. `line` is
optional.

`anchor_line` is the exact text of `file[line]` at finding-creation
time, truncated. It lets readers detect drift later: if the file
changed, the same content may have shifted to a different line, or
disappeared entirely. `null` means "no anchor was captured" (no
target_repo at find-time, capture failure, etc.).

### Review entry semantics

Each review entry is an *update* to the finding from a single
author at a single moment. Three fields use **nil-don't-touch**
semantics:

- `labels`: `null` → this entry didn't touch labels, the author's
  prior override (if any) carries forward; `[]` → explicit clear;
  `[...]` → this is the author's new full label set, replacing any
  earlier override they made.
- `severity`: `null` → this entry didn't touch severity, prior
  override carries forward; non-null → this is the author's
  override.
- `comment`: free text or empty.

When merging across authors for display, fettle takes the latest
non-nil entry per `(author, axis)` and unions the label sets. So
"label X is set" means at least one author has it set right now —
and a comment-only edit doesn't accidentally wipe a prior override
on the other axis.

### Outcome entry semantics

Each outcome entry is one event recording disposition. Append-only;
the latest entry wins for "current state" display. `status` is one
of `merged`, `closed`, `wontfix`, or whatever your project agrees
on. Re-marking is allowed; humans drive that, fettle just records
it.

### `files.jsonl`

Per-file scan ledger. One entry per file `find` has processed in
this run. Files with zero findings still get a row, so resume
knows to skip them.

```json
{
  "file": "internal/foo/bar.go",
  "status": "ok",
  "finding_count": 3,
  "started": "...",
  "ended": "..."
}
```

`status` is `ok`, `empty`, or `error`. `fettle run find --resume`
skips files whose last entry is `ok` or `empty`; `error` rows get
retried.

### Atomicity and concurrency

`add finding` writes a brand-new doc via `os.Link(2)` (atomic
create-only on POSIX). Two concurrent writers on the same id can't
both succeed — the loser sees `fs.ErrExist`.

`add review` / `add outcome` mutate an existing doc via
read-modify-write: read `findings/<id>.json` → patch the relevant
array → write `<id>.json.<random>.tmp` → fsync → atomic-rename
over the target → fsync the parent dir.

There is **no flock** on the read-modify-write path. The race
window is sub-millisecond and fettle is intended for single-user
laptop use — two concurrent reviewers on the same finding could
silently lose one append. If you ever run multiple writers in
parallel against the same finding, wrap UpdateFinding in a flock
helper.

Stale `<id>.json.<random>.tmp` files from a crash are filtered out
by the read path; no explicit cleanup pass is needed.

## Web UI

`fettle ui` serves a small server-rendered web app on localhost —
Go binary with embedded assets (templ + Tailwind v4 + HTMX, no
separate build step at runtime). It opens on a run picker (one row
per `.fettle/runs/<name>/`), and once you pick a run it reads that run's
finding docs and writes back to the same docs for actions a human
takes:

- Browse the run's findings by file, label, severity, or outcome.
- Add labels, severity, and comments — written as a new entry in
  the finding's `reviews[]` array.
- Mark findings closed — written as a new entry in the finding's
  `outcomes[]` array.

**Author identity (UI):** on the first edit in a fresh install,
the UI prompts once for a slug, prefilled with `git config
user.name` (or `$USER` if git isn't configured). The chosen slug is
persisted to `~/.config/fettle/identity` and used for every
subsequent UI session. A small "Reviewing as: <slug>" indicator
shows the active identity with a way to change it. Identity is
per-user, per-machine — never stored in `.fettle/config.json`,
since the project directory may be shared or checked in.

**Author identity (CLI):** non-interactive contexts can't prompt,
so the CLI uses the standard chain: `FETTLE_AGENT` (set by the
harness during stages) → `$FETTLE_AUTHOR` →
`~/.config/fettle/identity` → error. Setting up identity once via
the UI populates the same file the CLI reads.

There is no separate backend service. The UI process reads and
writes the project directory and that's all.

## Agent contract

Agents that fettle spawns share a unified contract:

1. Fettle invokes the agent CLI (`claude -p ...`, `codex exec ...`,
   user-supplied script, etc.) with the substituted markdown prompt
   as input.
2. The agent reads its inputs, decides what to record, and shells
   to `fettle add finding` or `fettle add review` for each output
   record. The harness assigns `id` / `created_by` / `created_at`
   on findings, and `at` on review entries (deriving the author
   from `FETTLE_AGENT`).
3. Environment passed to the agent: `FETTLE_RUN` points at the run
   folder the output should land in; `FETTLE_AGENT` carries the
   agent label used for the source identity; `FETTLE_MODEL` and
   `FETTLE_EFFORT` are passed through when set; `PATH` is prepended
   with fettle's binary directory so the CLIs are callable without
   further setup.
4. The agent exits. Zero records emitted is a valid "nothing to
   report"; the per-stage ledger (or the `completed_at` mark on the
   manifest) tells the harness whether the run succeeded.

This works for any CLI agent that accepts a prompt and can shell
out. Fettle does not depend on any agent's tool-calling protocol or
session model — only that it can invoke `fettle add <kind>`.

## Resumability

The run folder is the unit of resume. Every stage uses the prompt
snapshotted into the run, never the editable template — editing
the project's templates after a stage has started has no effect;
start a new run to pick up template edits.

- `fettle run find --resume <run>` skips files whose last entry in
  `files.jsonl` is `ok` or `empty`; `error` rows get retried.
- `fettle run review --run <run> --agent codex` skips findings
  already reviewed by `codex` (any review entry whose author slug
  matches).

**Caveats**: resume assumes the target repo snapshot hasn't changed
since the run started. If a file's contents move under `find`, the
`ok`/`empty` ledger entry will still cause it to be skipped —
re-scan by deleting the matching row from the run's `files.jsonl`.
A future version may track `content_sha`/`mtime` to detect drift
automatically.

## Non-goals

- **No auto-merge.** Outcomes are a tracking log, not a shipping
  pipeline. Humans drive the actual fix-and-merge step.
- **No build/lint/test integration.** If you want a stage that
  runs your test suite, write a prompt that tells the agent to do
  so — fettle won't invoke `task test` on your behalf.
- **No language awareness.** Fettle treats everything as text +
  globs. If your analysis depends on AST inspection, your prompt
  tells the agent how to do it (or pre-extracts the data outside
  fettle).

## Versioning and the project marker

`.fettle/config.json` carries `fettle_version`, which fettle checks on
every run for compatibility. If the file is missing or malformed,
fettle refuses to write to the directory — protection against
accidentally clobbering an unrelated folder. Migrations between
schema versions will be added once there's a second version to
migrate from.
