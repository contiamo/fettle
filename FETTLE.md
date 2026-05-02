# Fettle

A harness for running LLM agents over a codebase to find, review, and group
findings. You provide the instructions in markdown; fettle handles the
orchestration. Output is JSONL on disk.

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
  find    →    review       →    merge | dedupe   →    group     →   close
  per-file     per-finding       (optional;            cluster        track
  agent        agent and/or      multi-run             findings into  outcomes
  scan         human review      consolidation)        PR-sized
                                                       batches
```

`find`, `merge`, `dedupe`, and `group` each create their own **run
folder** under `runs/`. `review` and `outcome` are operations on
existing runs — their outputs (per-author review files, the
outcomes log) live inside the target run, not in their own
folders. Stages are independent — you can skip any, re-run any, or
stop after the first one if a report is all you need.

**Merge and dedupe are siblings**, not alternatives — they handle
multi-run consolidation differently:

- **Merge** is purely deterministic. Use it for *non-overlapping*
  inputs (e.g. one find run on `**/*.go`, another on `**/*.ts`).
  Cardinality preserved (1+1 = 2). No agent invocation; the harness
  copies findings forward into a new run, stamps each with a
  `members[]` entry pointing at its source, and propagates source
  reviews verbatim with id remapping. Fast.

- **Dedupe** uses an LLM. Use it for *overlapping* inputs (e.g. the
  same code scanned by two different agents). Cardinality may
  reduce (1+1 → maybe 1 canonical finding). Source reviews fed to
  the agent as input context, but **not** copied onto canonical
  findings (would forge authorship under N-to-1 synthesis).

Both produce a run that downstream `group` can consume. Skip both
if you only have one find run.

**Group is single-input**: it takes one run (a find / merge / dedupe
run) and clusters its findings into PR-sized batches. Multi-run
grouping isn't supported directly; merge or dedupe first, then group.

## Project layout

`fettle init` creates a project directory. **`find`, `dedupe`, and
`group` each create their own run folder** under `runs/`, named with
the stage prefix and a UTC timestamp. Each folder is self-describing
about its own data and points at any input runs for full provenance.

`review` and `outcome` are **operations on existing runs**, not
stages with their own folders. Their outputs (per-author review
files, the outcomes log) live inside the target run.

```
my-audit/
  .fettle.json                  marker + config; confirms this is a fettle dir
  instructions/                 editable templates (seed for new runs)
    find.md
    review.md                   per-finding review rubric
    review_group.md             per-group (cluster-level) review rubric
    dedupe.md
    group.md
  runs/
    find_20260430T145233Z_security-v1/
      run.json                  manifest: who/what/when
      instructions/find.md      snapshot of the find prompt that ran
      findings.jsonl            scan results
      files.jsonl               per-file scan ledger
      raw/                      verbatim agent output, one file per file
      reviews_<author>.jsonl    written by `fettle run review --run …`; one per author
      outcomes.jsonl            written by `fettle add outcome --run …`

    merge_20260501T091500Z_combined/
      run.json                  manifest with input_runs: ["runs/find_go", "runs/find_ts"]
      findings.jsonl            findings copied verbatim, stamped with members[].length=1
      reviews_<author>.jsonl    propagated from input runs (subject ids remapped)
      outcomes.jsonl            outcomes of merged findings live here
                                (no instructions/ — merge is harness-only, no agent)
                                (no raw/ — no agent transcript to capture)

    dedupe_20260501T093000Z_consolidate/
      run.json                  manifest with input_runs: ["runs/find_X", "runs/find_Y"]
      instructions/dedupe.md    snapshot
      findings.jsonl            canonical findings (with members[] back-pointers)
      raw/                      single agent transcript
      reviews_<author>.jsonl    reviews of canonical findings live here
      outcomes.jsonl            outcomes of canonical findings live here

    group_20260501T100000Z_pr-scoped/
      run.json                  manifest with input_run: "runs/dedupe_X" (or a find run)
      instructions/group.md     snapshot
      groups.jsonl              clusters
      member_reviews_snapshot.json  input-run review state captured at group-creation
                                time; group review reads this verbatim so its
                                view of member reviews is byte-for-byte the
                                same as the grouping agent's
      raw/                      single agent transcript
      reviews_<author>.jsonl    reviews of groups live here
      outcomes.jsonl            outcomes of groups live here
```

`find` creates a new run folder on each invocation, or continues an
existing one via `--resume runs/<name>/`. `merge`, `dedupe`, and
`group` take input runs via `--run` and create a *new* output run
folder — `merge` and `dedupe` accept multiple `--run` flags (one
per input find run); `group` accepts exactly one `--run` (the find,
merge, or dedupe run to cluster).

`fettle run review` and `fettle add outcome` / `fettle show outcome`
take `--run runs/<name>/` pointing at the target run; they read its
findings or groups and write their own append-only files inside it
(`reviews_<author>.jsonl` for review, `outcomes.jsonl` for close).
They never create a new run folder. The agent-facing `fettle review
add` resolves the run from `FETTLE_RUN` instead of taking `--run`
directly — same target, different entry point.

A run folder is *self-describing about its own data*, not
self-contained: a group folder references a finding/dedupe run for
member resolution; a dedupe folder references find runs for member
provenance. Archive the input-run chain alongside if you want full
data portability.

**Run folder naming**: `<stage>_<UTC-timestamp>_<slug>/` where
`<stage>` is `find`, `merge`, `dedupe`, or `group`. Timestamp format is
`YYYYMMDDTHHMMSSZ` so runs sort chronologically and same-day runs
don't collide. The slug defaults to a short random suffix; `--name
<slug>` overrides just the slug portion. Resuming a killed `find` is
`fettle run find --resume runs/<name>/`. `merge`, `dedupe`, and
`group` are single-shot — no resume; if they crash mid-output the
partial folder should be deleted and the stage re-run.

**`run.json`** is the manifest of the stage that created the folder
(find / merge / dedupe / group). It captures everything needed to
understand or reproduce that run. Reviews and outcomes do *not* update
`run.json` — they're attachments to a run, not stage outputs of
their own.

Common spine:

```json
{
  "name": "find_20260430T145233Z_security-v1",
  "stage": "find",
  "fettle_version": "0.1.0",
  "created_at":   "2026-04-30T14:52:33Z",
  "completed_at": "2026-04-30T15:08:11Z",
  "agent":  { "name": "claude", "model": "sonnet" },
  "source_path":   "instructions/find.md",
  "snapshot_path": "instructions/find.md"
}
```

`completed_at` means **the harness finished what it was asked**, not
"the scan covered everything." It is set when the stage exits cleanly:

- `find`: set when every *attempted* file has a final `ok` or
  `empty` row in `files.jsonl`. A `--limit N` run that processed N
  files cleanly counts as completed; coverage (whether that
  truncated subset is enough) is the user's call, visible via the
  recorded `args.limit`. If any file's latest row is `error` or the
  run was interrupted, `completed_at` stays absent and the run is
  recoverable via `fettle run find --resume`.
- `dedupe` / `group`: set after the agent exits cleanly. Single-shot
  stages — partial runs (no `completed_at`) should be deleted and
  re-run.

Downstream stages (`merge` / `dedupe` / `group`) require their
inputs' `completed_at` to be set, so an interrupted or errored find
run can't silently feed in. They do **not** second-guess
`--limit`-truncated inputs — if you want to dedupe two partial
runs, that's your call.

A **find** run additionally records:

```json
"target_repo": "/abs/path/to/repo",
"target_repo_git": { "head": "abc123def", "dirty": false },
"include": ["**/*.go"],
"exclude": ["vendor/**", "**/*_generated.go"],
"args": { "concurrency": 4, "limit": 0 }
```

A **merge** run records its inputs and has no `agent` /
`source_path` / `snapshot_path` fields (no agent ran):

```json
{
  "name": "merge_20260501T091500Z_combined",
  "stage": "merge",
  "fettle_version": "0.1.0",
  "created_at": "...",
  "completed_at": "...",
  "input_runs": [
    "runs/find_20260430T145233Z_go",
    "runs/find_20260430T214041Z_ts"
  ]
}
```

A **dedupe** run records its inputs:

```json
"input_runs": [
  "runs/find_20260430T145233Z_security-v1",
  "runs/find_20260430T211903Z_codex"
]
```

A **group** run records its single input:

```json
"input_run": "runs/dedupe_20260501T093000Z_consolidate"
```

`target_repo_git` is best-effort — populated when the target is a git
repo, omitted otherwise.

**Review prompt snapshot**: when `fettle run review --run X` runs the
**review agent** for the first time in X, it writes the active rubric
into `runs/X/instructions/<file>.md`. The rubric file is stage-aware:
`review.md` for find / merge / dedupe runs (per-finding review),
`review_group.md` for group runs (cluster-level review). Subsequent
*automated* reviews in X re-use that snapshot — one rubric per run,
which is what you want when labels from multiple automated reviewers
are merged.

For group runs, fettle additionally captures the input run's review
state at group-creation time into
`runs/X/member_reviews_snapshot.json`. Group review reads this
verbatim (subset per group) so its view of member reviews is
byte-for-byte identical to what the grouping agent saw — late
reviews on the input run never drift the reviewer's context.

Human reviewers via the UI don't consume the rubric files (they're
making direct judgments, not following an LLM rubric), so the
snapshots aren't read for human reviews. To use a different automated
review prompt, do not edit the snapshot in place — start a new run
upstream or explicitly delete the run's `instructions/review.md` (or
`review_group.md`) and any existing `reviews_*.jsonl` you don't want
carried over.

`.fettle.json` confirms the directory is a fettle project and records the
fettle version that created it. It also holds the project's config:

```json
{
  "fettle_version": "0.1.0",
  "created_at": "2026-04-30T10:56:00Z",
  "target_repo": "/abs/path/to/repo",
  "agent": { "name": "claude", "model": "sonnet" },
  "include": ["**/*.go", "**/*.ts"],
  "exclude": ["vendor/**", "node_modules/**", "**/*_generated.go"],
  "instructions": {
    "find":         "instructions/find.md",
    "review":       "instructions/review.md",
    "review_group": "instructions/review_group.md",
    "dedupe":       "instructions/dedupe.md",
    "group":        "instructions/group.md"
  }
}
```

Instructions can live anywhere — fettle reads the paths verbatim.

## Instructions (you write these)

You supply one markdown document per stage. Each tells the spawned agent
what to look for, what fields to emit, what's out of scope.

A minimal `find.md` for a security pass might say: *"Read `TARGET_FILE`. Flag
every place that builds a SQL query by string concatenation, every `os/exec`
call constructed with unescaped variables, every hardcoded credential or
API key. For each finding, run `fettle add finding` with `--file`,
`--line`, `--title`, `--description`, `--suggestion`, and a relevant
`--severity` and `--label`."*. Keep the checks file-local; cross-file
data flow is out of scope for fettle.

Fettle substitutes a small, fixed set of variables when running each stage:

| Stage                                  | Variables available in the prompt                                                  |
|----------------------------------------|------------------------------------------------------------------------------------|
| find                                   | `TARGET_FILE`, `REPO_ROOT`                                                         |
| review (find / merge / dedupe target)  | `FINDING_JSON`, `REPO_ROOT`                                                        |
| review (group target)                  | `GROUP_JSON`, `MEMBERS_JSON`, `MEMBER_REVIEWS_JSON`, `REPO_ROOT`                   |
| merge                                  | (no prompt — harness-only, no agent invoked)                                       |
| dedupe                                 | `FINDINGS_JSON` (annotated with `from_run` and current review state)               |
| group                                  | `FINDINGS_JSON`, `REVIEWS_JSON`                                                    |

For group review, `MEMBERS_JSON` is the array of member findings
resolved from `group.finding_ids[]` against the input run's
findings; `MEMBER_REVIEWS_JSON` is the per-member subset of the
input run's review state, sourced from `member_reviews_snapshot.json`
in the group run (captured at group-creation time, not re-read live).

**No stage gets `OUTPUT_PATH`.** Every stage records its output by
shelling to `fettle add <kind>` (see the CLI section). This unifies
the agent contract: the harness owns ids, timestamps, validation, and
file appends; the agent never writes a fettle data file directly. The
prompt frame teaches the recording protocol; the user's
stage-specific instructions decide what to record.

`REPO_ROOT` is the `target_repo` recorded on the run reviewing
operates against. For find runs the value is direct from
`run.json`'s `target_repo`. For merge / dedupe / group runs (which
don't have their own `target_repo`), it's resolved by walking back
through `input_run`/`input_runs[]` to a find run. Dedupe rejects
input runs whose `target_repo` differs, so the value is unambiguous.

`REVIEWS_JSON` is the merged review state, keyed by finding id. It's `{}` if
no reviews exist — `group` runs fine without `review` having happened. For
each finding with reviews, fettle hands the agent the chronological per-author
entries plus a derived `current_labels` union (latest entry per author,
unioned across authors — matches the display rules below):

```json
{
  "abc123": {
    "current_labels": ["confirmed", "high-impact"],
    "entries": [
      { "author": "codex",   "labels": ["confirmed"], "comment": "...", "at": "..." },
      { "author": "michael", "labels": ["confirmed", "high-impact"], "comment": "...", "at": "..." }
    ]
  }
}
```

Your `group.md` decides what to do with it — typically: skip findings whose
`current_labels` include `false-positive` or `out-of-scope`, prefer
high-confidence findings, etc. If `REVIEWS_JSON` is empty, the prompt should
just cluster on findings directly.

Inside your markdown you're free to inline or reference additional knowledge
docs (conventions, checklists, threat models, style guides). Fettle doesn't
care what the prompt looks like beyond the variable substitution.

## The CLI

The CLI is organized verb-first. Five top-level verbs:

- `fettle run <stage>` — start an agent-driven stage (`find`, `review`, `merge`, `dedupe`, `group`). `find` / `dedupe` / `group` / `merge` create new run folders; `review` operates on an existing run via `--run` and writes per-author files into it.
- `fettle add <noun>` — append a record (`finding`, `group`, `review`, `outcome`). Called by spawned agents (FETTLE_RUN env), or by humans for `outcome`.
- `fettle list <noun>` — list records of a kind in a run, or `runs` for all runs in the project.
- `fettle show <noun>` — print one record (or one run's status).
- `fettle init` — create a new fettle project.

`fettle ui` is planned but not yet shipped. `--dir` and `--json` are global flags. Every command emits the `{"data": ...}` envelope when `--json` is passed; reads always emit it.

```
fettle init [--target REPO] [--agent claude|codex]
    Create a new fettle project in cwd. Writes .fettle.json and a
    stub instructions/ tree.

# Stage runners (agent-driven)

fettle run find [--name SLUG] [--include GLOB] [--exclude GLOB] [-c N] [--limit N]
    Create runs/find_<YYYYMMDDTHHMMSSZ>_<slug>/, snapshot the find
    prompt into it, walk the target repo, and append findings to
    that run's findings.jsonl. Prints the run path so you can pipe
    it into the next stage.

fettle run find --resume runs/<name>/
    Resume a killed find. Re-uses the snapshotted prompt — editing
    instructions/find.md after the run started has no effect.
    Flags that would change run identity (--include, --exclude,
    --limit, --agent, --model, --effort, --agent-script, --name)
    are rejected; the manifest is authoritative.

fettle run review --run runs/<name>/ [--agent NAME]
    For each subject in --run not yet reviewed by this agent, run
    the review agent. Append to runs/<name>/reviews_<agent>.jsonl.
    Stage selects the subject and rubric: find / merge / dedupe runs
    iterate findings using `instructions/review.md`; group runs
    iterate groups using `instructions/review_group.md` (the agent
    additionally receives the cluster's member findings + their
    snapshotted member reviews).

fettle run merge --run RUN [--run RUN]... [--name SLUG]
    Concatenate non-overlapping runs. Harness-only — no agent
    invocation. Each finding from each input run is copied forward
    with a fresh id and members[{finding_id, from_run}] of length 1.
    Source reviews are propagated verbatim with subject ids
    remapped. Source outcomes are NOT propagated (they should be
    re-evaluated against the merged view). Creates
    runs/merge_<UTC-timestamp>_<slug>/.

    Use this for non-overlapping inputs (e.g. one find run on
    `**/*.go`, another on `**/*.ts`). For overlapping inputs (same
    code scanned by two different agents), use dedupe instead.

    Warns (not fails) on exact (file, line, title) duplicates
    across input runs — that's a hint you may have wanted dedupe.

fettle run dedupe --run RUN [--run RUN]... [--name SLUG] [--agent NAME]
    Cross-run consolidation. Reads findings (annotated with current
    review state from each input run) and asks the agent to merge
    equivalent findings into canonical entries — same shape, plus a
    members[] back-pointer (length >= 1). Creates
    runs/dedupe_<UTC-timestamp>_<slug>/. Single agent invocation;
    no resume — re-run if it fails.

    Two or more --run flags is the typical case; single-input
    dedupe is allowed (degenerate but useful for re-canonicalizing
    a single find run with a different prompt, or for testing). If
    you only have one find run and don't need to re-canonicalize,
    just skip this stage and run `fettle run group --run <find_run>`
    directly.

fettle run group --run runs/<name>/ [--name SLUG] [--agent NAME]
    Cluster the input run's findings into PR-sized batches. Single
    input — if you have multiple find runs, dedupe first. If
    reviews_*.jsonl files exist in the input run, merge them into
    REVIEWS_JSON; otherwise the agent gets `{}`. Creates
    runs/group_<UTC-timestamp>_<slug>/.

# Record writes (called by spawned agents, sometimes by humans)

fettle add finding --file PATH --line N --title T --description D --suggestion S
                   [--severity X] [--label k:v ...] [--reference PATH[:LINE] ...]
                   [--canonical-of RUN:FINDING_ID ...]
    Append one finding to FETTLE_RUN's findings.jsonl. Used by both
    find runs (no --canonical-of) and dedupe runs (one or more
    --canonical-of entries make the new finding a canonical
    synthesis with members).

fettle add group --title T --summary S --finding ID [--finding ID ...]
                 [--label k:v ...]
    Append one group to FETTLE_RUN's groups.jsonl. Members are
    --finding ID entries (repeatable).

fettle add review {--finding ID | --group ID} --label LABEL ... [--comment TEXT]
    Append one review entry to the run's reviews_<FETTLE_AGENT>.jsonl.

fettle add outcome --run runs/<name>/ {--finding ID | --group ID}
                   --status STATUS [--pr URL]
    Append an outcome event to runs/<name>/outcomes.jsonl. Marks
    the subject as disposed of (PR merged, won't fix, etc.). The
    target run is whichever folder owns the subject (find / merge /
    dedupe run for findings; group run for groups). Author identity
    chains FETTLE_AGENT → $FETTLE_AUTHOR → ~/.config/fettle/identity.

# Record reads

fettle list runs
    Print all runs in the project as a JSON array, sorted newest
    first. Each entry has identity, provenance, and a counts block.

fettle show run PATH
    Print one run's summary (status + counts). PATH is positional.

fettle list findings  --run runs/<name>/
fettle list groups    --run runs/<name>/
fettle list reviews   --run runs/<name>/
fettle list outcomes  --run runs/<name>/
    Dump every record of that kind in --run as a JSON array.

fettle show finding   --run runs/<name>/ ID
fettle show group     --run runs/<name>/ ID
fettle show review    --run runs/<name>/ {--finding ID | --group ID} [--all]
fettle show outcome   --run runs/<name>/ {--finding ID | --group ID} [--all]
    Print one record. For review and outcome, default emits the
    derived current state per subject; --all emits the full
    chronological history (including superseded entries).

# Output

All read commands emit `{"data": <records>}` unconditionally. Stage
runners and add commands print plain text to stdout by default
(path / id) so shell pipelines like `out=$(fettle run find)` keep
working; pass `--json` to switch them to the same envelope.

# Server-side metadata

`add finding` and `add group` assign `id` / `created_by` /
`created_at`; `add review` assigns only `at` (author derived from
`FETTLE_AGENT`); `add outcome` assigns `at` and `marked_by` (same
author-lookup chain as review). All validate fields before append.

# Stage-aware guards the harness enforces

- `add finding --canonical-of` is required in a dedupe run and
  rejected in a find run.
- `add finding` is rejected in a group run; `add group` is rejected
  outside a group run.
- `--canonical-of RUN:FINDING_ID` provenance: `RUN` must appear in
  the dedupe run's `input_runs[]` and `FINDING_ID` must exist in
  that run's `findings.jsonl`.
- `add group --finding ID`: `ID` must exist in the group run's
  `input_run`'s `findings.jsonl`.
- `add review` and `add outcome`: `--finding ID` accepted only in
  find / merge / dedupe runs (id must exist in `findings.jsonl`);
  `--group ID` accepted only in group runs (id must exist in
  `groups.jsonl`). Mismatched subject kind or unknown id is rejected.
```

Concurrency, resume, retries, and timelines are the harness's job.

## JSONL schemas

Fettle owns a small, stable shape. Everything else — labels, severity scale,
category names — is yours to define in your prompts.

All of these files live inside a run folder (`runs/<name>/`). Each
schema is described once and applies to whichever stage's run folder
contains it.

**Path conventions in the schemas below**:

- `file` and `references[].file` are repo-relative — i.e., relative
  to the run's `target_repo` (find runs) or the source run's
  `target_repo` (dedupe runs).
- `from_run` and `input_runs[]` are project-relative — i.e.,
  relative to the project directory containing `runs/`.
- `source_path` in `run.json` is whatever path the user put in
  `.fettle.json`'s `instructions` map — fettle reads it verbatim.
  Typical case is project-relative (`instructions/find.md`);
  absolute paths are also allowed.
- `snapshot_path` in `run.json` is run-relative (it points at the
  frozen copy inside this run folder, typically `instructions/<stage>.md`,
  resolved against the run dir).
- `input_run` (group) and `input_runs[]` (dedupe) are
  project-relative — they reference other run folders under
  `runs/`.

**`findings.jsonl`** — one finding per line. Lives in find runs (raw
findings) and dedupe runs (canonical findings synthesized from
multiple find runs).

```json
{
  "id": "abc123def456",
  "file": "internal/foo/bar.go",
  "line": 42,
  "title": "...",
  "description": "...",
  "suggestion": "...",
  "severity": null,
  "labels": [],
  "references": [],
  "members": [
    { "finding_id": "111", "from_run": "runs/find_20260430T214841Z_materiality" },
    { "finding_id": "222", "from_run": "runs/find_20260430T215611Z_codex" }
  ],
  "created_by": "agent:claude/sonnet",
  "created_at": "..."
}
```

`id` is generated server-side and unique. In find-run findings, `id`
identifies a single agent observation. In merge-run and dedupe-run
findings, `id` identifies the relocated/canonical record; `members[]`
lists the source observations.

`members[]` is **omitted** in find-run findings (or empty array). In
merge-run findings, `members[]` always has length 1 (one source per
relocated finding — merge is 1-to-1). In dedupe-run findings,
`members[]` has length >= 1 (canonical synthesis can fold one or
many sources together).

For consensus signal on **dedupe** output, count carefully —
`members.length` is *observation count*, not agent count: one agent
might flag the same root cause at two adjacent lines and have both
observations land in the same canonical finding. The right counters:

- `members.length` — total source observations
- distinct `from_run` count — how many runs flagged it
- agent count — derived by reading each `from_run`'s `run.json` for
  its `agent.name` and de-duping

The merge stage doesn't need a consensus signal — `members.length`
is always 1 by definition.

**Source reviews handling differs by stage**:

- **Merge** propagates each input run's `reviews_<author>.jsonl`
  verbatim into the merge run, with `subject.id` remapped from the
  source finding's id to the new merged id. Faithful since merge is
  1-to-1; no authorship forging concern.
- **Dedupe** does NOT copy source reviews onto canonical findings
  (that would forge authorship under N-to-1 synthesis). Instead the
  dedupe agent receives source findings annotated with their
  **current review state** (labels, latest comment per author) as
  input context. Canonical findings start with no reviews of their
  own; the agent writes any rejection it sees into the canonical
  finding's `labels[]`/`description` synthesis or skips the finding
  entirely.

`severity` is a free-form string (or null). Fettle doesn't enforce a scale —
your prompt decides whether to use `low`/`medium`/`high`, `P1`/`P2`/`P3`, a
numeric `7.5`, or none. The UI sorts lexically.

`labels` is a list of plain strings. The convention for structured tags is
`prefix:value` — e.g. `["cwe:89", "wcag:1.1.1", "category:duplication",
"confidence:high"]`. The UI treats prefixes as facets so you can filter by
"all `cwe:` labels" or "all `category:` labels."

`references` carries additional code locations the finding points to — the
duplication case being the obvious one ("this finding exists in N other
files"), but also "see also this related site" findings. Each entry:
`{ "file": "internal/baz/qux.go", "line": 33 }`. `line` is optional.
The grouping prompt sees these via `FINDINGS_JSON` and can cluster on
reference overlap (multiple findings touching the same site) or hotspot
frequency (a file that appears in many findings' references).

**`files.jsonl`** — per-file scan ledger. One entry per file `find` has
processed in this run. Files with zero findings still get a row, so resume
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

`status` is `ok`, `empty`, or `error`. `fettle run find --resume` skips files whose
last entry is `ok` or `empty`; `error` rows get retried. Because the run
folder is the unit of identity, this ledger only describes *this* run's
work — there's no cross-run staleness problem.

**`reviews_<author>.jsonl`** — one file per reviewer (agent or human). Each
file is owned by exactly one writer, so there's no concurrent-writer story
to design and no contention on append. Filename: `reviews_<slug>.jsonl`,
where the slug is the author's identifier — e.g. `reviews_claude-sonnet.jsonl`,
`reviews_codex.jsonl`, `reviews_michael.jsonl`. The slug is the source of
truth for who wrote each entry.

Each entry:

```json
{
  "subject": { "kind": "finding", "id": "abc123" },
  "labels":  ["confirmed", "high-impact"],
  "comment": "Verified: untrusted input flows into Exec on line 47.",
  "at": "2026-04-30T11:02:14Z"
}
```

`subject.kind` is `finding` or `group`, matching what the run
produces. The same shape covers both. There is no `source` field
inside the entry — the filename carries that.

Display merges all `reviews_*.jsonl` files at read time and sorts by `at`
per subject, so you get one chronological feed per finding or group
regardless of which reviewers contributed.

Append-only. **Label semantics**: each entry is the writing author's
*current full label set* on that subject. The latest entry from a given
author replaces that author's earlier label set entirely (no union, no
per-label override). When merging across authors for display, fettle takes
the latest entry per `(author, subject)` and unions the resulting label
sets, so "label X is set" means at least one author has it set right now.

**`groups.jsonl`** — one group per line. Lives in group runs only.

```json
{
  "id": "g_abc12345",
  "title": "...",
  "summary": "...",
  "finding_ids": ["abc123", "def456"],
  "labels": [],
  "created_by": "agent:claude/sonnet",
  "created_at": "..."
}
```

`finding_ids[]` references findings in the group run's input run
(specified in `run.json`'s `input_run`). Resolving "what's in this
group" means reading the input run's `findings.jsonl` for those ids.

Same `labels` convention as findings — `prefix:value` strings, e.g.
`["effort:large", "category:duplication"]`. Reviews can attach to
groups too (`subject.kind: "group"` in `reviews_<author>.jsonl`); the
same display rules apply.

**`outcomes.jsonl`** — one outcome event per line. Lives in
whichever run owns the subject:

- Outcomes of findings live in the find or dedupe run that produced
  them.
- Outcomes of groups live in the group run that produced them.

Records that a finding or group is closed (PR merged, won't fix,
deduped, etc.). Fettle does not open or merge PRs; this is a tracking
log.

```json
{
  "subject": { "kind": "group", "id": "g_abc12345" },
  "status":  "merged",
  "pr_url":  "https://github.com/.../pull/42",
  "at": "...",
  "marked_by": "human:michael"
}
```

`status` is one of `merged`, `closed`, `wontfix`, or whatever your project
agrees on. Re-marking is allowed; the latest entry wins.

## Web UI

`fettle ui` serves a small React app on localhost. It opens on a run
picker (one row per `runs/<name>/`), and once you pick a run it reads
that run's JSONL files (auto-refreshes when they change) and writes back
to the run's `reviews_<your-slug>.jsonl` and `outcomes.jsonl` for
actions a human takes:

- Browse the run's findings by file, label, or group.
- Add labels and comments — written as a new entry in the run's
  `reviews_<your-slug>.jsonl`.
- Mark findings or groups closed — written as a new entry in the run's
  `outcomes.jsonl`.

**Author identity (UI):** on the first edit in a fresh install, the
UI prompts once for a slug, prefilled with `git config user.name` (or
`$USER` if git isn't configured). The chosen slug is persisted to
`~/.config/fettle/identity` and used for every subsequent UI session.
A small "Reviewing as: <slug>" indicator shows the active identity
with a way to change it. Identity is per-user, per-machine — never
stored in `.fettle.json`, since the project directory may be shared
or checked in.

**Author identity (CLI):** non-interactive contexts can't prompt, so
the CLI uses the standard chain: `FETTLE_AGENT` (set by the harness
during stages) → `$FETTLE_AUTHOR` → `~/.config/fettle/identity` →
error. Setting up identity once via the UI populates the same file
the CLI reads.

There is no separate backend service. The UI process reads and writes the
project directory and that's all.

## Agent contract

Agents that fettle spawns share a unified contract across all stages:

1. Fettle invokes the agent CLI (`claude -p ...`, `codex exec ...`,
   user-supplied script, etc.) with the substituted markdown prompt as input.
2. The agent reads its inputs, decides what to record, and shells to
   the appropriate fettle CLI (`fettle add finding`, `fettle add group`,
   `fettle add review`) for each output record. `find add` and
   `group add` assign `id`, `created_by`, and `created_at`; `review
   add` assigns only `at` and derives the author from `FETTLE_AGENT`
   for the per-author filename. All three validate before appending —
   server-side identity is preserved across every stage.
3. Environment passed to the agent: `FETTLE_RUN` points at the run
   folder the output should land in; `FETTLE_AGENT` carries the
   agent label used for the source identity; `FETTLE_MODEL` and
   `FETTLE_EFFORT` are passed through when set; `PATH` is prepended
   with fettle's binary directory so the CLIs are callable without
   further setup.
4. The agent exits. Zero records emitted is a valid "nothing to
   report"; the per-stage ledger (or the `completed_at` mark on the
   manifest) tells the harness whether the run succeeded.

This works for any CLI agent that accepts a prompt and can shell out.
Fettle does not depend on any agent's tool-calling protocol or
session model — only that it can invoke `fettle <kind> add`.

## Resumability

The run folder is the unit of resume for per-unit stages. Every stage
uses the prompt snapshotted into the run, never the editable template
— editing the project's templates after a stage has started has no
effect; start a new run to pick up template edits.

**Per-unit stages** (one agent invocation per file/finding) support
resume:

- `fettle run find --resume <run>` skips files whose last entry in `files.jsonl`
  is `ok` or `empty`; `error` rows get retried.
- `fettle run review --run <run> --agent codex` skips subjects
  (findings or groups, depending on the run's stage) already
  reviewed in `reviews_codex.jsonl`.

**Single-shot stages** are atomic at the run level — successful runs
have `completed_at` set in `run.json`; partial runs do not.

- `merge` — harness-only. One pass reads all input runs and writes
  the merged outputs (findings + remapped reviews). No agent. If
  the harness is killed mid-write, `completed_at` is missing —
  delete the run folder and re-run.
- `dedupe` — one agent invocation reads all input runs, calls
  `fettle add finding --canonical-of ...` per canonical record. The
  harness sets `completed_at` after the agent exits cleanly. If the
  agent crashes mid-output, partial findings are committed but
  `completed_at` is missing — delete and re-run.
- `group` — same shape; one invocation, calls `fettle add group`
  per cluster. Same atomic semantics.

(See the `run.json` description above for how readers should
interpret missing `completed_at` — merge / dedupe / group inputs
must be completed before consumption.)

JSONL files are append-only. You can hand-edit them between runs if
you need to correct something, but the normal flow is "let the tools
and the UI append; never mutate in place."

**Caveats**: resume assumes the target repo snapshot hasn't changed
since the run started. If a file's contents move under `find`, the
`ok`/`empty` ledger entry will still cause it to be skipped — re-scan
by deleting the matching row from the run's `files.jsonl`. (A future
version may track `content_sha`/`mtime` to detect drift
automatically.) Similarly, dedupe and group only see the input that
exists when they run; if findings or reviews change after a group
run, just create a new group run. Group review is consistent with
this: it reads the `member_reviews_snapshot.json` captured at
group-creation time, so late reviews on the input run never drift
the reviewer's view — but that also means stale grouping context
isn't refreshed by re-running review. Re-create the group run to
pick up new input-run reviews.

## Non-goals

- **No auto-merge.** `outcomes.jsonl` is an outcome log, not a shipping
  pipeline. Humans drive the actual fix-and-merge step.
- **No build/lint/test integration.** If you want a stage that runs your
  test suite, write a prompt that tells the agent to do so — fettle won't
  invoke `task test` on your behalf.
- **No language awareness.** Fettle treats everything as text + globs. If
  your analysis depends on AST inspection, your prompt tells the agent how to do
  it (or pre-extracts the data outside fettle).

## Versioning and the project marker

`.fettle.json` carries `fettle_version`, which fettle checks on every run
for compatibility. If the file is missing or malformed, fettle refuses to
write to the directory — protection against accidentally clobbering an
unrelated folder. Migrations between schema versions will be added once
there's a second version to migrate from.
