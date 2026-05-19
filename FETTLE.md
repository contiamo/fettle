# Fettle

A harness for running LLM agents over a codebase to find and review
issues. You provide the instructions in markdown; fettle handles the
orchestration. Output is JSONL on disk, append-only, one line per
record.

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

`find` creates a run folder under `<project>/runs/`. `review` and
`outcome` are operations on existing runs — their results land
inside the target run, not in their own folders. Stages are
independent — you can stop after `find` if a report is all you need,
or hand the run to a human reviewer via the UI without ever invoking
the review agent.

## Project layout

`fettle init <path>` creates a fettle project at the path you name.
The project directory's name is up to you; its presence is signalled
by a `fettle.json` file at its root. That file is both the on-disk
config and the marker subsequent commands use to discover the
project.

```
audits/                         the directory you named at `fettle init` time
  fettle.json                   project config + discovery marker
  instructions/                 editable templates (seed for new runs)
    find.md
    review.md
  runs/
    run_3cdf6f_20260430T145233Z/
      run.json                  manifest: who/what/when
      instructions/find.md      snapshot of the find prompt that ran
      files.jsonl               per-file scan ledger (find-stage only)
      raw/                      verbatim agent output, one log per file
      findings_3cdf6f_20260430T145233Z.jsonl           emitted findings (one file per run)
      reviews_3cdf6f_20260430T145233Z_<author>.jsonl   review entries (one file per author)
      outcomes_3cdf6f_20260430T145233Z_<author>.jsonl  outcome events (one file per author)
```

`fettle init` semantics:

- The target's parent directory must already exist. Fettle uses
  `mkdir`, not `mkdir -p`, so a typo can't silently create a chain
  of nested directories.
- If the target doesn't exist, fettle creates it (one directory).
- If the target exists, it must be a directory and it must be empty.
- If the target already contains a `fettle.json`, init refuses —
  re-running `fettle init` on a project is rejected, not a silent
  overwrite.

```sh
fettle init audits --target ../repo --include '**/*.go'
fettle init . --target ../repo --include '**/*.go'
fettle init ../audits/api --target /abs/path/to/repo --include '**/*.go'
```

`--target` is the repository fettle should scan. If omitted, it
defaults to the project directory itself — useful only when you
want to audit fettle's own metadata folder, which is rarely what
you want. Most projects point `--target` at a separate code repo.

**Run folder naming:** `run_<slug>_<UTC-timestamp>/`. The slug is
6 random hex characters by default (16M possibilities — collisions
within one project are effectively impossible) or a name you pass
via `--name <slug>`. Timestamp format is `YYYYMMDDTHHMMSSZ` so
runs sort chronologically.

Run dirs are uniform regardless of stage — what kind of work a run
holds lives in `run.json`'s `stage` field, not the folder name.

**Reference runs by slug.** Anywhere a `--run` / `--resume` flag
is accepted, you can pass the run's short slug instead of the full
path: `fettle run find --resume 3cdf6f` finds the run whose slug
is `3cdf6f`. Prefix matches work too (`3cd`, `3cdf`, …) as long as
they're unambiguous; otherwise the CLI errors with the candidate
list, same UX as `git`.

## Discovering the project directory

Project-scoped commands — `fettle run <stage>`, `fettle list runs`,
`fettle show run`, `fettle list/show <noun> --run ...`, `fettle ui`
— find the project directory via the following chain (first source
that resolves wins):

1. `--project-dir <path>` flag.
2. `$FETTLE_PROJECT_DIR` env var.
3. Upward walk from the current working directory looking for a
   `fettle.json`; the directory containing it is the project.

So once you've `fettle init`'d a project, you can run any
project-scoped command from inside it (or any subdirectory) with no
flags — same UX as `git`, `hg`, `go`.

The record-write commands target a *run*, not a project, and use
two different conventions:

- `fettle add finding` and `fettle add review` read `FETTLE_RUN`
  from the environment. The harness sets it before spawning each
  agent, so an agent script just calls `fettle add <kind> ...` and
  the record lands in the right run.
- `fettle add outcome` takes a `--run <path>` flag instead.
  Outcomes are usually recorded by humans outside an agent stage
  (after a PR merges, etc.), so a flag is the natural surface.

## `run.json` manifest

```json
{
  "name": "run_3cdf6f_20260430T145233Z",
  "stage": "find",
  "fettle_version": "0.1.0",
  "created_at":   "2026-04-30T14:52:33Z",
  "completed_at": "2026-04-30T15:08:11Z",
  "target_repo": "/abs/path/to/repo",
  "target_repo_git": { "head": "abc123def", "dirty": false },
  "include": ["**/*.go"],
  "exclude": ["vendor/**", "**/*_generated.go"],
  "agent":  { "name": "claude", "model": "sonnet" },
  "source_path":   "instructions/find.md",
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

**Review prompt snapshot:** when `fettle run review --run X` runs
the review agent for the first time, it writes the active prompt
into `<project>/runs/X/instructions/review.md`. Subsequent automated
reviews in X re-use that snapshot — one prompt per run, which is
what you want when multiple automated reviewers contribute to the
same run.

Human reviewers via the UI don't consume the prompt files (they're
making direct judgments, not following an LLM prompt), so the
snapshot isn't read for human reviews.

## `fettle.json`

`fettle.json` confirms the directory is a fettle project and records
the fettle version that created it. It also holds the project's
config:

```json
{
  "fettle_version": "0.1.0",
  "created_at": "2026-04-30T10:56:00Z",
  "target_repo": "/abs/path/to/repo",
  "agent": { "name": "claude", "model": "sonnet" },
  "walker": "git",
  "include": ["**/*.go", "**/*.ts"],
  "exclude": ["vendor/**", "**/*_generated.go"],
  "instructions": {
    "find":   "instructions/find.md",
    "review": "instructions/review.md"
  }
}
```

Paths in this config are resolved relative to the project directory.
Instructions can live anywhere — move them outside the project if
you'd rather have them tracked under your normal docs path.
`target_repo` is also relative to the project directory, so
`"../.."` portably points two levels up.

`walker` chooses how files are enumerated in the target repo:

- `"git"` (default): asks `git ls-files` for the union of tracked
  and untracked-not-ignored files, so `.gitignore` rules are
  honoured automatically. No need to repeat `node_modules/`, build
  artifacts, dependency caches, etc. in `exclude`. Requires the
  target to be a git repo.
- `"fs"`: walks the filesystem directly; only the user's `include`
  / `exclude` globs filter. Use for non-git targets or when you
  explicitly want to scan files that are gitignored.

`include` / `exclude` are doublestar globs evaluated against
repo-relative paths. `fettle init` requires at least one `--include`
glob — there's no project-independent default, and a permissive one
(`**/*`) would pull in lockfiles, vendored dependencies, generated
code, and binary blobs. Examples:

```sh
fettle init audits --include '**/*.go'
fettle init audits --include 'src/**/*.{ts,tsx}' --include '**/*.css'
fettle init audits --include '**/*.py' --exclude 'tests/**'
```

With `walker: "git"`, anything in `.gitignore` is dropped on top of
your `exclude` patterns.

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

`SUBJECT_JSON` is the finding the agent is being asked to review
(just the finding fields — prior reviews are deliberately *not*
surfaced; the review agent makes a fresh judgment per run).

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
  `outcome`). Called by spawned agents (via `FETTLE_RUN` env), or by
  humans via the UI.
- `fettle list <noun>` — list records of a kind in a run, or `runs`
  for all runs in the project.
- `fettle show <noun>` — print one record (or one run's status).
- `fettle init <path>` — create a new fettle project at `<path>`.

`fettle ui` serves the local web UI. `--project-dir` and `--json`
are global flags. `--json` switches read commands, stage runners,
and add commands to the `{"data": ...}` envelope; `fettle init` and
`fettle ui` ignore it (init prints a human "initialized in <path>"
line, ui logs its listen URL to stderr).

```
fettle init <path> --include GLOB [--include GLOB ...] [--exclude GLOB ...]
                   [--walker git|fs] [--target REPO] [--agent claude|codex]
    Create a new fettle project at <path>. <path> is `.` to init the
    current directory (which must be empty) or a name to create
    (whose parent must exist). At least one --include glob is
    required; --walker defaults to git (honour .gitignore). See
    `fettle init --help` for examples.

# Stage runners (agent-driven)

fettle run find [--name SLUG] [--include GLOB] [--exclude GLOB] [-c N] [--limit N]
    Create <project>/runs/run_<slug>_<UTC-timestamp>/, snapshot the
    find prompt into it, walk the target repo, and append every
    finding the agent emits to the run's single findings_*.jsonl
    stream. Prints the run path so you can pipe it into the next
    stage.

fettle run find --resume <project>/runs/<name>/
    Resume a killed find. Re-uses the snapshotted prompt — editing
    instructions/find.md after the run started has no effect. Flags
    that would change run identity (--include, --exclude, --limit,
    --agent, --model, --effort, --agent-script, --name) are
    rejected; the manifest is authoritative.

fettle run review --run <project>/runs/<name>/ [--agent NAME]
    For each finding in --run not yet reviewed by this agent, run
    the review agent. Append the agent's judgment to the run's
    reviews_*.jsonl stream. Resume keys on the agent's slug —
    switching the model (e.g. claude/sonnet → claude/opus) doesn't
    force re-review.

# Record writes (called by spawned agents, sometimes by humans)

fettle add finding --file PATH --line N --title T --description D --suggestion S
                   [--severity X] [--label k:v ...] [--reference PATH[:LINE] ...]
    Append one finding to FETTLE_RUN's findings_*.jsonl stream. The
    finding id is generated server-side.

fettle add review --finding ID [--add-label L ...] [--remove-label L ...]
                  [--severity X] [--comment TEXT]
    Append one review entry to FETTLE_RUN's reviews_*.jsonl stream.
    --add-label / --remove-label name labels to add or drop from the
    finding's effective set; the same label in both is rejected.
    --severity overrides the finding's severity from this point
    forward; --comment is free text. At least one of these must be
    set.

fettle add outcome --run <project>/runs/<name>/ --finding ID
                   --status STATUS [--pr URL]
    Append an outcome event to the run's outcomes_*.jsonl stream.
    Marks the finding as disposed of (PR merged, won't fix, etc.).
    Author identity chains FETTLE_AGENT → $FETTLE_AUTHOR →
    ~/.config/fettle/identity.

# Record reads

fettle list runs
    Print all runs in the project as a JSON array, sorted newest
    first. Each entry has identity, provenance, and a counts block.

fettle show run PATH
    Print one run's summary (status + counts).

fettle list findings  --run <project>/runs/<name>/
fettle list reviews   --run <project>/runs/<name>/
fettle list outcomes  --run <project>/runs/<name>/
    Dump every record of that kind in --run as a JSON array.

fettle show finding   --run <project>/runs/<name>/ ID
fettle show review    --run <project>/runs/<name>/ --finding ID [--all]
fettle show outcome   --run <project>/runs/<name>/ --finding ID [--all]
    Print one record. For review and outcome, default emits the
    resolved current state (effective labels, latest severity,
    latest outcome); --all emits the full chronological history.

# Output

All read commands emit `{"data": <records>}` unconditionally. Stage
runners and add commands print plain text to stdout by default (path
/ id) so shell pipelines like `out=$(fettle run find)` keep working;
pass `--json` to switch them to the same envelope.

# Server-side metadata

`add finding` assigns `id` / `created_by` / `created_at`; `add
review` and `add outcome` assign only `at` (author derived from
FETTLE_AGENT, FETTLE_AUTHOR, or the identity file). All validate
fields before writing.
```

## Storage shape

Each run's records live in append-only JSONL streams under the run
directory:

```
runs/run_<slug>_<ts>/
  findings_<slug>_<ts>.jsonl                # one file per run
  reviews_<slug>_<ts>_<author>.jsonl        # one file per (run, author)
  outcomes_<slug>_<ts>_<author>.jsonl       # one file per (run, author)
```

The slug + timestamp embedded in every filename come from the run
folder itself — every artifact is self-identifying when copied out.
Findings carry no author segment because there's one findings file
per run regardless of which writer contributed; reviews and outcomes
carry the writer's author slug so each reviewer's file stays
distinct and shareable individually (`cp alice's reviews.jsonl`
into another teammate's checkout, the next read picks it up).

Every JSONL line is self-describing as `{kind, id, ...}`:

```jsonl
// findings_*.jsonl
{"kind":"finding","id":"abc123def456","file":"internal/foo/bar.go","line":42,"title":"...","description":"...","suggestion":"...","severity":"medium","labels":["category:duplication"],"references":[{"file":"internal/baz/qux.go","line":33}],"anchor_line":"    return foo(x, y)","created_by":"agent:claude/sonnet","created_at":"2026-04-30T15:01:22Z"}

// reviews_*.jsonl  (kind/id name the subject the entry targets)
{"kind":"finding","id":"abc123def456","author":"human:michael","at":"2026-04-30T16:00:00Z","add":["confirmed"],"remove":[],"severity":"high","comment":"Verified"}

// outcomes_*.jsonl  (kind/id name the subject)
{"kind":"finding","id":"abc123def456","author":"human:michael","at":"2026-05-01T10:00:00Z","status":"merged","pr_url":"https://github.com/.../pull/42"}
```

On a finding entry, `kind`+`id` is the entry's identity. On a
review or outcome entry, `kind`+`id` names the subject the entry
references. Today both resolve to the same finding identity; the
symmetry is forward-compat for additional subject kinds.

`severity` on a finding is a free-form string (or null). Fettle
doesn't enforce a scale — your prompt decides whether to use
`low`/`medium`/`high`, `P1`/`P2`/`P3`, a numeric `7.5`, or none.

`labels` on a finding is a list of plain strings. The convention for
structured tags is `prefix:value` — e.g. `["cwe:89", "wcag:1.1.1",
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

### Resolution

Reviews and outcomes are a chronological event log; the *effective*
state of a finding at any moment is what the resolver computes when
reads ask for "current."

- **Labels.** Start from the finding's `labels` seed. Walk every
  review entry targeting that finding in `at` order; for each entry
  apply `remove` then `add`. The result is the effective label set.
  The same label cannot appear in `add` and `remove` on one
  entry — that's rejected at write time, so order doesn't matter
  inside an entry.
- **Severity.** The latest review entry whose `severity` is non-null
  wins. Falls back to the finding's `severity` seed if no review
  asserted one.
- **Outcome.** The latest entry across all outcome files wins for
  "current state" display.

Identical timestamps tie-break by author slug, so the resolved
state is deterministic across machines.

### `files.jsonl`

Per-file scan ledger. One entry per file `find` has processed in
this run. Files with zero findings still get a row, so resume knows
to skip them.

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

Writes are append-only JSONL: `os.OpenFile(..., O_APPEND, ...)` plus
`json.Encoder.Encode`. The filename for each (run, kind, author)
combination is deterministic — every writer in the same run for the
same author resolves to the same file — so multiple processes (e.g.
the harness spawning N parallel `find` agents that each shell out
to `fettle add finding`) all append to the same on-disk stream.

Coherence comes from two layers:

- **Same `run.Path` (one process, one open stream):** writes
  serialise through an in-process mutex on the Path, so two
  goroutines sharing the same opened stream can't race on the JSON
  encoder. This covers the UI server and any in-process
  parallelism.
- **Across processes:** each write is one `write(2)` to a file
  opened with `O_APPEND`. POSIX guarantees the offset is atomically
  advanced to end-of-file before each write, so concurrent writers
  can't overwrite each other's bytes. POSIX does *not* strictly
  guarantee that one write's buffer is atomic against a concurrent
  writer's; on Linux and macOS that interleaving doesn't happen for
  entry-sized buffers in practice, but the design doesn't rely on
  it as an invariant.

The read path is the backstop that makes the whole story safe
regardless: a torn or interleaved line is skipped silently — the
malformed line is logged to stderr and the rest of the file
parses normally. So multi-writer correctness rests on two things
fettle does control (the `O_APPEND` offset guarantee + the
tolerant reader), not on platform-specific buffer-atomicity
promises.

## Web UI

`fettle ui` serves a small server-rendered web app on localhost —
Go binary with embedded assets (templ + Tailwind v4 + HTMX, no
separate build step at runtime). It opens on a run picker (one row
per `<project>/runs/<name>/`) and once you pick a run it loads that
run's findings, reviews, and outcomes from disk and writes new
entries back to the same streams for actions a human takes:

- Browse the run's findings by file, label, severity, or outcome.
- Add or remove labels, change severity, add comments — written as
  a new entry in the run's reviews stream.
- Mark findings closed — written as a new entry in the outcomes
  stream.

**Author identity (UI):** on the first edit in a fresh install,
the UI prompts once for a slug, prefilled with `git config
user.name` (or `$USER` if git isn't configured). The chosen slug is
persisted to `~/.config/fettle/identity` and used for every
subsequent UI session. A small "Reviewing as: <slug>" indicator
shows the active identity with a way to change it. Identity is
per-user, per-machine — never stored in `fettle.json`, since the
project directory may be shared or checked in.

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
   on findings, and `at` on review/outcome entries (deriving the
   author from `FETTLE_AGENT`).
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

**Caveats:** resume assumes the target repo snapshot hasn't changed
since the run started. If a file's contents move under `find`, the
`ok`/`empty` ledger entry will still cause it to be skipped —
re-scan by deleting the matching row from the run's `files.jsonl`.
A future version may track `content_sha`/`mtime` to detect drift
automatically.

## Non-goals

- **No auto-merge.** Outcomes are a tracking log, not a shipping
  pipeline. Humans drive the actual fix-and-merge step.
- **No build/lint/test integration.** If you want a stage that runs
  your test suite, write a prompt that tells the agent to do so —
  fettle won't invoke `task test` on your behalf.
- **No language awareness.** Fettle treats everything as text +
  globs. If your analysis depends on AST inspection, your prompt
  tells the agent how to do it (or pre-extracts the data outside
  fettle).

## Versioning and the project marker

`fettle.json` carries `fettle_version`, stamped at `fettle init`
time. The field is informational today — fettle doesn't gate runs
on it — but the presence of `fettle.json` itself is the protection
against accidentally writing into an unrelated folder: every
project-scoped command looks for that file before doing any work.
Migrations between schema versions and an explicit version-gate
will be added once there's a second version to migrate from.
