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
  find    →    review       →    dedupe         →    group     →   resolve
  per-file     per-finding       (optional;          cluster        track
  agent        agent and/or      multi-run          findings into    closures
  scan         human review      consolidation)     PR-sized
                                                    batches
```

`find`, `dedupe`, and `group` each create their own **run folder**
under `runs/`. `review` and `resolve` are operations on existing
runs — their outputs (per-author review files, the resolutions log)
live inside the target run, not in their own folders. Stages are
independent — you can skip any, re-run any, or stop after the first
one if a report is all you need.

**Dedupe is a multi-input stage**: it consolidates findings from
multiple find runs (typically two agents scanning the same code) into
canonical findings with a `members[]` field listing the source
findings. Skip it if you only have one find run — within-run dedup is
already the find agent's job (via `--reference`).

**Group is single-input**: it takes one run (a find run, or a dedupe
run if you consolidated first) and clusters its findings into
PR-sized batches. Multi-run grouping isn't supported directly; dedupe
first, then group.

## Project layout

`fettle init` creates a project directory. **`find`, `dedupe`, and
`group` each create their own run folder** under `runs/`, named with
the stage prefix and a UTC timestamp. Each folder is self-describing
about its own data and points at any input runs for full provenance.

`review` and `resolve` are **operations on existing runs**, not
stages with their own folders. Their outputs (per-author review
files, the resolutions log) live inside the target run.

```
my-audit/
  .fettle.json                  marker + config; confirms this is a fettle dir
  instructions/                 editable templates (seed for new runs)
    find.md
    review.md
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
      resolutions.jsonl         written by `fettle resolve add --run …`

    dedupe_20260501T093000Z_consolidate/
      run.json                  manifest with input_runs: ["runs/find_X", "runs/find_Y"]
      instructions/dedupe.md    snapshot
      findings.jsonl            canonical findings (with members[] back-pointers)
      raw/                      single agent transcript
      reviews_<author>.jsonl    reviews of canonical findings live here
      resolutions.jsonl         closures of canonical findings live here

    group_20260501T100000Z_pr-scoped/
      run.json                  manifest with input_run: "runs/dedupe_X" (or a find run)
      instructions/group.md     snapshot
      groups.jsonl              clusters
      raw/                      single agent transcript
      reviews_<author>.jsonl    reviews of groups live here
      resolutions.jsonl         closures of groups live here
```

`find` creates a new run folder on each invocation, or continues an
existing one via `--resume runs/<name>/`. `dedupe` and `group` take
input runs via `--run` and create a *new* output run folder —
`dedupe` accepts multiple `--run` flags (one per input find run);
`group` accepts exactly one `--run` (the find or dedupe run to
cluster).

`fettle run review` and `fettle resolve add` / `fettle resolve show`
take `--run runs/<name>/` pointing at the target run; they read its
findings or groups and write their own append-only files inside it
(`reviews_<author>.jsonl` for review, `resolutions.jsonl` for
resolve). They never create a new run folder. The agent-facing
`fettle review add` resolves the run from `FETTLE_RUN` instead of
taking `--run` directly — same target, different entry point.

A run folder is *self-describing about its own data*, not
self-contained: a group folder references a finding/dedupe run for
member resolution; a dedupe folder references find runs for member
provenance. Archive the input-run chain alongside if you want full
data portability.

**Run folder naming**: `<stage>_<UTC-timestamp>_<slug>/` where
`<stage>` is `find`, `dedupe`, or `group`. Timestamp format is
`YYYYMMDDTHHMMSSZ` so runs sort chronologically and same-day runs
don't collide. The slug defaults to a short random suffix; `--name
<slug>` overrides just the slug portion. Resuming a killed `find` is
`fettle run find --resume runs/<name>/`. `dedupe` and `group` are
single-shot agent invocations — no resume; if they crash mid-output
the partial folder should be deleted and the stage re-run.

**`run.json`** is the manifest of the stage that created the folder
(find / dedupe / group). It captures everything needed to understand
or reproduce that run. Reviews and resolves do *not* update
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

`completed_at` is set when the stage finishes successfully:

- `find`: set only after every walked file has a final `ok` or
  `empty` row in `files.jsonl`. If any file's latest row is `error`,
  `completed_at` stays absent — the run is recoverable via
  `fettle run find --resume`.
- `dedupe` / `group`: set after the agent exits cleanly. Single-shot
  stages — partial runs (no `completed_at`) should be deleted and
  re-run.

Readers should treat the absence of `completed_at` differently
depending on context. UI browsing and `run review --watch` may read
in-progress find runs (that's the whole point of `--watch`). But
dedupe and group consumers should **require** their inputs to be
completed before consuming — feeding an in-progress find run into
dedupe is a footgun.

A **find** run additionally records:

```json
"target_repo": "/abs/path/to/repo",
"target_repo_git": { "head": "abc123def", "dirty": false },
"include": ["**/*.go"],
"exclude": ["vendor/**", "**/*_generated.go"],
"args": { "concurrency": 4, "limit": 0 }
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
**review agent** for the first time in X, it writes the active
`review.md` into `runs/X/instructions/review.md`. Subsequent
*automated* reviews in X re-use that snapshot — one rubric per run,
which is what you want when labels from multiple automated reviewers
are merged.

Human reviewers via the UI don't consume `review.md` (they're making
direct judgments, not following an LLM rubric), so the snapshot
isn't read for human reviews. To use a different automated review
prompt, do not edit the snapshot in place — start a new run upstream
or explicitly delete the run's `instructions/review.md` and any
existing `reviews_*.jsonl` you don't want carried over.

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
    "find":   "instructions/find.md",
    "review": "instructions/review.md",
    "dedupe": "instructions/dedupe.md",
    "group":  "instructions/group.md"
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
API key. For each finding, run `fettle find add` with `--file`,
`--line`, `--title`, `--description`, `--suggestion`, and a relevant
`--severity` and `--label`."*. Keep the checks file-local; cross-file
data flow is out of scope for fettle.

Fettle substitutes a small, fixed set of variables when running each stage:

| Stage                        | Variables available in the prompt                                            |
|------------------------------|------------------------------------------------------------------------------|
| find                         | `TARGET_FILE`, `REPO_ROOT`                                                   |
| review (find/dedupe target)  | `FINDING_JSON`, `REPO_ROOT`                                                  |
| review (group target)        | `GROUP_JSON`, `MEMBERS_JSON`, `REPO_ROOT`                                    |
| dedupe                       | `FINDINGS_JSON` (annotated with `from_run` and current review state)         |
| group                        | `FINDINGS_JSON`, `REVIEWS_JSON`                                              |

**No stage gets `OUTPUT_PATH`.** Every stage records its output by
shelling to `fettle <kind> add` (see the CLI section). This unifies
the agent contract: the harness owns ids, timestamps, validation, and
file appends; the agent never writes a fettle data file directly. The
prompt frame teaches the recording protocol; the user's
stage-specific instructions decide what to record.

`MEMBERS_JSON` (review of group runs) is the resolved finding records
the group references — so the reviewer has the actual evidence in
hand, not just `finding_ids[]` to chase.

`REPO_ROOT` is the `target_repo` recorded on the run reviewing
operates against. For find runs the value is direct from
`run.json`'s `target_repo`. For dedupe and group runs (which don't
have their own `target_repo`), it's resolved by walking back through
`input_run`/`input_runs[]` to a find run; **dedupe rejects input
runs whose `target_repo` differs**, so the value is unambiguous.

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

The CLI is organized into two namespaces:

- `fettle run <stage>` — start an agent-driven stage. `find`, `dedupe`, and `group` create a new run folder (find can also resume one with `--resume`). `review` is the exception: it operates on an existing run via `--run` and writes per-author files into it; no new folder.
- `fettle <noun> <verb>` — operate on a record kind (`find`, `group`, `review`, `resolve`); `add` is the agent-facing append, `show` is the read command. Same noun used regardless of which run kind the record lives in.

`fettle init`, `fettle ui`, and `--dir` are top-level. Every command operates on the fettle project in the current directory unless `--dir` overrides.

```
fettle init [--target REPO] [--agent claude|codex|gemini]
    Create a new fettle project in cwd. Writes .fettle.json and a stub
    instructions/ tree.

# Stage runners (agent-driven)

fettle run find [--name SLUG] [--include GLOB] [--exclude GLOB] [-c N] [--limit N]
    Create runs/find_<YYYYMMDDTHHMMSSZ>_<slug>/, snapshot the find prompt
    into it, walk the target repo, and append findings to that run's
    findings.jsonl. Prints the run path so you can pipe it into the
    next stage.

fettle run find --resume runs/<name>/
    Resume a killed find. Re-uses the snapshotted prompt — editing
    instructions/find.md after the run started has no effect on the
    resume.

fettle run review --run runs/<name>/ [--agent NAME] [--watch]
    For each finding (find/dedupe runs) or group (group runs) not yet
    reviewed by this agent, run the review agent. Append to
    runs/<name>/reviews_<agent>.jsonl. --watch polls the run's
    findings.jsonl and picks up new entries as they land — only
    meaningful for in-progress find runs. `--watch` is rejected
    against dedupe/group runs because their output is single-shot
    and only readable after `completed_at` is set.

fettle run dedupe --run RUN [--run RUN]... [--name SLUG] [--agent NAME]
    Cross-run consolidation. Reads findings (annotated with current
    review state from each input run) and asks the agent to merge
    equivalent findings into canonical entries — same shape, plus a
    members[] back-pointer. Creates runs/dedupe_<UTC-timestamp>_<slug>/.
    Single agent invocation; no resume — re-run if it fails.

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

fettle run list
    List all runs in the project with summary counts.

fettle run status --run runs/<name>/
    Print counts for one run, scoped to records that live in the run
    folder. For a find or dedupe run that's findings, reviews, and
    resolutions; for a group run that's groups, reviews, and
    resolutions. Downstream runs (e.g. groups derived from a find
    run) aren't counted — those are separate runs with their own
    status.

# Record CLIs (called by spawned agents, sometimes by humans)

fettle find add --file PATH --line N --title T --description D --suggestion S
                [--severity X] [--label k:v ...] [--reference PATH[:LINE] ...]
                [--canonical-of RUN:FINDING_ID ...]
    Append one finding to FETTLE_RUN's findings.jsonl. Used by both
    find runs (no --canonical-of) and dedupe runs (one or more
    --canonical-of entries make the new finding a canonical
    synthesis with members).

fettle find show --run runs/<name>/ ID
    Print one finding record to stdout.

fettle group add --title T --summary S --finding ID [--finding ID ...]
                 [--label k:v ...]
    Append one group to FETTLE_RUN's groups.jsonl. Members are
    --finding ID entries (repeatable).

fettle group show --run runs/<name>/ ID
    Print one group record to stdout.

fettle review add --finding ID | --group ID --label LABEL ... [--comment TEXT]
    Append one review entry to the run's reviews_<FETTLE_AGENT>.jsonl.

fettle resolve add --run runs/<name>/ {--finding ID | --group ID}
                   [--pr URL] [--status STATUS]
    Append a closure event to runs/<name>/resolutions.jsonl. Marks
    the subject as resolved (PR merged, won't fix, etc.). The target
    run is whichever folder owns the subject (find/dedupe run for
    findings; group run for groups).

fettle resolve show --run runs/<name>/ {--finding ID | --group ID} [--all]
    Print the current resolution state for one finding or group.
    Resolutions are keyed by (subject.kind, subject.id) — latest
    entry wins for display. Default prints the latest event only;
    `--all` prints the full chronological history of events for that
    subject (including overridden ones).

All record-add CLIs use server-side metadata generation. `find add`
and `group add` assign `id` / `created_by` / `created_at`; `review
add` has no id and assigns only `at` (author derived from
`FETTLE_AGENT`); `resolve add` assigns `at` and `marked_by` (same
author-resolution chain as review: `FETTLE_AGENT` if set, else
`$FETTLE_AUTHOR`, else `~/.config/fettle/identity`, else error). All
validate fields before append.

**Stage-aware guards** the harness enforces:

- `find add --canonical-of` is required in a dedupe run and rejected
  in a find run.
- `find add` is rejected in a group run; `group add` is rejected
  outside a group run.
- `--canonical-of RUN:FINDING_ID` provenance: `RUN` must appear in
  the dedupe run's `input_runs[]` and `FINDING_ID` must exist in
  that run's `findings.jsonl`.
- `group add --finding ID`: `ID` must exist in the group run's
  `input_run`'s `findings.jsonl`.
- `review add` and `resolve add`: `--finding ID` accepted only in
  find/dedupe runs (id must exist in `findings.jsonl`); `--group ID`
  accepted only in group runs (id must exist in `groups.jsonl`).
  Mismatched subject kind or unknown id is rejected.

# UI

fettle ui [--port N]
    Launch the web UI. Shows a run picker; pick one to browse, label,
    and resolve.
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
identifies a single agent observation. In dedupe-run findings, `id`
identifies the canonical synthesis and `members[]` lists the source
observations.

`members[]` is **omitted** in find-run findings (or empty array). In
dedupe-run findings, `members[]` is the provenance trail back to the
source observations.

For consensus signal, count carefully — `members.length` is
*observation count*, not agent count: one agent might flag the same
root cause at two adjacent lines and have both observations land in
the same canonical finding. The right counters:

- `members.length` — total source observations
- distinct `from_run` count — how many runs flagged it
- agent count — derived by reading each `from_run`'s `run.json` for
  its `agent.name` and de-duping

The dedupe agent receives source findings annotated with their
**current review state** (labels, latest comment per author) from
each input run. This prevents canonicalizing findings that source-run
reviewers already labeled `false-positive`/`out-of-scope`. Reviews on
source findings are **not** copied into the canonical finding's
review files — that would forge authorship; canonical findings start
with no reviews of their own. The agent should write the rejection
into its decision (skip the finding) or into the canonical finding's
`labels[]`/`description` synthesis.

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

**`resolutions.jsonl`** — one resolution event per line. Lives in
whichever run owns the subject:

- Resolutions of findings live in the find or dedupe run that produced
  them.
- Resolutions of groups live in the group run that produced them.

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
to the run's `reviews_<your-slug>.jsonl` and `resolutions.jsonl` for
actions a human takes:

- Browse the run's findings by file, label, or group.
- Add labels and comments — written as a new entry in the run's
  `reviews_<your-slug>.jsonl`.
- Mark findings or groups resolved — written as a new entry in the run's
  `resolutions.jsonl`.

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
   the appropriate fettle CLI (`fettle find add`, `fettle group add`,
   `fettle review add`) for each output record. `find add` and
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
- `fettle run review --run <run> --agent codex` skips findings
  already reviewed in `reviews_codex.jsonl`.
- `fettle run review --watch` polls a find run's `findings.jsonl`
  and picks up new entries as `find` appends them, so review can run
  concurrently with a long find scan. `--watch` is rejected against
  dedupe/group runs (their output is single-shot; review them after
  `completed_at` is set).

**Single-shot stages** (one agent invocation per run) are atomic at
the run level — successful runs have `completed_at` set in
`run.json`; partial runs do not.

- `dedupe` — one invocation reads all input runs, calls `fettle
  find add --canonical-of ...` per canonical record. The harness
  sets `completed_at` after the agent exits cleanly. If the agent
  crashes mid-output, partial findings are committed but
  `completed_at` is missing — delete the run folder and re-run.
- `group` — same shape; one invocation, calls `fettle group add`
  per cluster. Same atomic semantics.

(See the `run.json` description above for how readers should
interpret missing `completed_at` — UI/`--watch` may read in-progress
find runs, but dedupe and group inputs must be completed.)

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
run, just create a new group run.

## Non-goals

- **No auto-merge.** `resolutions.jsonl` is a closure log, not a shipping
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
