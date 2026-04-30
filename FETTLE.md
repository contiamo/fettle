# Fettle

A harness for running LLM agents over a codebase to find, review, and group
issues. You provide the instructions in markdown; fettle handles the
orchestration. Output is JSONL on disk.

Fettle is a **file-oriented** harness: it runs an agent per file matching
your globs, and an issue's primary anchor is `(file, line)`. That makes it a
good fit for refactor sweeps, convention enforcement, doc-comment audits,
license-header checks, and lint-shaped security checks. It is *not* a fit
for analyses where the natural unit is bigger than a file — cross-file data
flow, route/middleware authorization, runtime accessibility, dependency-
graph compliance. v0 trades that range for a sharper contract.

What you're looking for lives in your markdown prompts. The harness ships;
the knowledge is yours.

## Pipeline

```
  find       →    review        →    group       →    resolve
  per-file        per-issue          cluster into     track which
  agent scan      agent and/or       review-sized     issues/groups
                  human review       batches          are closed
```

Each stage reads and writes JSONL files in the project directory. Stages are
independent — you can skip any of them, re-run any of them, or stop after the
first one if a report is all you need.

## Project layout

`fettle init` creates a project directory. Each `fettle find` invocation
creates a self-contained **run folder** under `runs/`; everything that
follows (review, group, resolve) operates on a specific run. Each run is
copyable, archivable, and reproducible — the prompt that produced it is
snapshotted alongside the data.

```
my-audit/
  .fettle.json                  marker + config; confirms this is a fettle dir
  instructions/                 editable templates (seed for new runs)
    find.md
    review.md
    group.md
  runs/
    find_20260430T145233Z_security-v1/
      run.json                  manifest: who/what/when, fully self-describing
      instructions/             snapshots of the prompts that ran (frozen)
        find.md                 snapshotted at run start
        review.md               snapshotted on first `fettle review --run …`
        group.md                snapshotted on first `fettle group --run …`
      findings.jsonl
      files.jsonl               per-file scan ledger
      raw/                      verbatim agent output, one file per invocation
      reviews_<author>.jsonl    one per reviewer (agent or human)
      groups.jsonl
      resolutions.jsonl
    find_20260502T091812Z_refactor/
      ...
```

A run folder is the unit of work. All paths in the schemas below are
relative to a run folder. `review`, `group`, and `resolve` all take
`--run runs/<name>/` and stay scoped to that folder.

**Run folder naming**: `find_<UTC-timestamp>_<slug>/`. Timestamp format is
`YYYYMMDDTHHMMSSZ` so runs sort chronologically and same-day runs don't
collide. The slug defaults to a short random suffix; `--name <slug>`
overrides just the slug portion (the timestamp prefix is always
generated). Resuming a killed `find` is `fettle find --resume runs/<name>/`.

**`run.json`** is the manifest. It captures everything needed to
understand or reproduce the run after it's archived or copied:

```json
{
  "name": "find_20260430T145233Z_security-v1",
  "fettle_version": "0.1.0",
  "created_at": "2026-04-30T14:52:33Z",
  "target_repo": "/abs/path/to/repo",
  "target_repo_git": { "head": "abc123def", "dirty": false },
  "include": ["**/*.go"],
  "exclude": ["vendor/**", "**/*_generated.go"],
  "stages": {
    "find":   { "agent": "claude", "model": "sonnet",
                "source_path": "instructions/find.md",
                "snapshot_path": "instructions/find.md" },
    "review": { "agent": "codex",  "model": "gpt-5",
                "source_path": "instructions/review.md",
                "snapshot_path": "instructions/review.md" },
    "group":  { "agent": "claude", "model": "sonnet",
                "source_path": "instructions/group.md",
                "snapshot_path": "instructions/group.md" }
  },
  "args": { "concurrency": 4, "limit": 0 }
}
```

`target_repo_git` is best-effort — populated when the target is a git
repo, omitted otherwise. The `stages` block is built incrementally:
`find` writes its entry at run creation; `review` and `group` add theirs
the first time they run, snapshotting their prompts at the same moment.

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
call constructed with unescaped variables, every hardcoded credential or API
key. For each issue, emit one JSON object to `OUTPUT_PATH` with fields …"*.
Keep the checks file-local; cross-file data flow is out of scope for fettle.

Fettle substitutes a small, fixed set of variables when running each stage:

| Stage  | Variables available in the prompt                          |
|--------|------------------------------------------------------------|
| find   | `TARGET_FILE`, `OUTPUT_PATH`, `REPO_ROOT`                  |
| review | `ISSUE_JSON`, `OUTPUT_PATH`, `REPO_ROOT`                   |
| group  | `ISSUES_JSON`, `REVIEWS_JSON`, `OUTPUT_PATH`               |

`REVIEWS_JSON` is the merged review state, keyed by issue id. It's `{}` if
no reviews exist — `group` runs fine without `review` having happened. For
each issue with reviews, fettle hands the agent the chronological per-author
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

Your `group.md` decides what to do with it — typically: skip issues whose
`current_labels` include `false-positive` or `out-of-scope`, prefer
high-confidence issues, etc. If `REVIEWS_JSON` is empty, the prompt should
just cluster on issues directly.

Inside your markdown you're free to inline or reference additional knowledge
docs (conventions, checklists, threat models, style guides). Fettle doesn't
care what the prompt looks like beyond the variable substitution.

## The CLI

```
fettle init [--target REPO] [--agent claude|codex|gemini]
    Create a new fettle project in cwd. Writes .fettle.json and a stub
    instructions/ tree.

fettle find [--name SLUG] [--include GLOB] [--exclude GLOB] [-c N] [--limit N]
    Create runs/find_<YYYYMMDDTHHMMSSZ>_<slug>/, snapshot the find prompt into
    it, walk the target repo, and append findings to that run's
    findings.jsonl. Prints the run path so you can pipe it into the next
    stage.

fettle find --resume runs/<name>/
    Resume a killed find. Re-uses the snapshotted prompt — editing
    instructions/find.md after the run started has no effect on the
    resume.

fettle review --run runs/<name>/ [--agent NAME] [--watch]
    For each finding in the run not yet reviewed by this agent, run the
    review agent. Append to runs/<name>/reviews_<agent>.jsonl. --watch
    polls the run's findings.jsonl and picks up new entries (useful while
    a long find is still running).

fettle group --run runs/<name>/ [--agent NAME]
    Cluster the run's findings. If reviews_*.jsonl files exist in the
    run, merge them into REVIEWS_JSON; otherwise the agent gets `{}`.
    Write to runs/<name>/groups.jsonl.

fettle resolve --run runs/<name>/ {--issue ID | --group ID} [--pr URL] [--status STATUS]
    Mark a run's issue or group as resolved. Append to
    runs/<name>/resolutions.jsonl.

fettle ui [--port N]
    Launch the web UI. Shows a run picker; pick one to browse, label,
    and resolve.

fettle runs
    List all runs in the project with summary counts.

fettle status --run runs/<name>/
    Print counts for one run: findings, reviewed, grouped, resolved.

fettle show --run runs/<name>/ {issue|group} ID
    Print one record to stdout.
```

Every command operates on the fettle project in the current directory (or
`--dir`). Concurrency, resume, retries, and timelines are the harness's job.

## JSONL schemas

Fettle owns a small, stable shape. Everything else — labels, severity scale,
category names — is yours to define in your prompts.

All of these files live inside a run folder (`runs/<name>/`). The schemas
below describe one run's data; cross-run analysis is something you do
yourself by reading multiple folders.

**`findings.jsonl`** — one finding per line:

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
  "created_by": "agent:claude/sonnet",
  "created_at": "..."
}
```

`id` is content-derived and stable across re-runs.

`severity` is a free-form string (or null). Fettle doesn't enforce a scale —
your prompt decides whether to use `low`/`medium`/`high`, `P1`/`P2`/`P3`, a
numeric `7.5`, or none. The UI sorts lexically.

`labels` is a list of plain strings. The convention for structured tags is
`prefix:value` — e.g. `["cwe:89", "wcag:1.1.1", "category:duplication",
"confidence:high"]`. The UI treats prefixes as facets so you can filter by
"all `cwe:` labels" or "all `category:` labels."

`references` carries additional code locations the issue points to — the
duplication case being the obvious one ("this issue exists in N other
files"), but also "see also this related site" findings. Each entry:
`{ "file": "internal/baz/qux.go", "line": 33 }`. `line` is optional.
The grouping prompt sees these via `ISSUES_JSON` and can cluster on
reference overlap (multiple issues touching the same site) or hotspot
frequency (a file that appears in many issues' references).

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

`status` is `ok`, `empty`, or `error`. `find --resume` skips files whose
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
  "subject": { "kind": "issue", "id": "abc123" },
  "labels":  ["confirmed", "high-impact"],
  "comment": "Verified: untrusted input flows into Exec on line 47.",
  "at": "2026-04-30T11:02:14Z"
}
```

`subject.kind` is `issue` or `group`. The same shape covers both. There is
no `source` field inside the entry — the filename carries that.

Display merges all `reviews_*.jsonl` files at read time and sorts by `at`
per subject, so you get one chronological feed per issue or group regardless
of which reviewers contributed.

Append-only. **Label semantics**: each entry is the writing author's
*current full label set* on that subject. The latest entry from a given
author replaces that author's earlier label set entirely (no union, no
per-label override). When merging across authors for display, fettle takes
the latest entry per `(author, subject)` and unions the resulting label
sets, so "label X is set" means at least one author has it set right now.

**`groups.jsonl`** — one group per line:

```json
{
  "id": "g_abc12345",
  "title": "...",
  "summary": "...",
  "issue_ids": ["abc123", "def456"],
  "labels": [],
  "created_by": "agent:claude/sonnet",
  "created_at": "..."
}
```

Same `labels` convention as issues — `prefix:value` strings, e.g.
`["effort:large", "category:duplication"]`. Reviews can attach to groups
too (`subject.kind: "group"` in `reviews_<author>.jsonl`); the same
display rules apply.

**`resolutions.jsonl`** — one resolution event per line. Records that an
issue or group is closed (PR merged, won't fix, deduped, etc.). Fettle does
not open or merge PRs; this is a tracking log.

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
  `reviews_<your-slug>.jsonl`. Your slug comes from `$FETTLE_AUTHOR`, or
  `~/.config/fettle/identity` as a fallback — never from `.fettle.json`,
  since the project directory may be shared or checked in.
- Mark issues or groups resolved — written as a new entry in the run's
  `resolutions.jsonl`.

There is no separate backend service. The UI process reads and writes the
project directory and that's all.

## Agent contract

Agents that fettle spawns are dead simple to write because the contract is
narrow:

1. Fettle invokes the agent CLI (`claude -p ...`, `codex exec ...`,
   `gemini ...`) with the markdown prompt as input.
2. Variables in the prompt (`TARGET_FILE`, `OUTPUT_PATH`, etc.) have already
   been substituted.
3. The agent reads what it needs, follows the instructions, and writes
   strict JSONL to `OUTPUT_PATH`.
4. The agent exits. No return value beyond the file. An empty file is a
   valid "no findings."

This works for any CLI agent that accepts a prompt and can write a file.
Fettle does not depend on any agent's tool-calling protocol or session model.

## Resumability

The run folder is the unit of resume. Every stage uses the prompt
snapshotted into the run, never the editable template — `find` from
`runs/<name>/instructions/find.md`, `review` from `…/review.md`, `group`
from `…/group.md`. Editing the project's templates after a stage has
started in a run has no effect on that run; start a new run to pick up
template edits.

`find --resume <run>` skips files whose last entry in `files.jsonl` is
`ok` or `empty`; `error` rows get retried. `review --run <run> --agent
codex` skips findings already reviewed in `reviews_codex.jsonl`. `group
--run <run>` skips findings already in some group. `review --watch`
polls the run's `findings.jsonl` and picks up new entries as `find`
appends them, so the two stages can run concurrently on a long scan.

JSONL files are append-only. You can hand-edit them between runs if you
need to correct something, but the normal flow is "let the tools and the
UI append; never mutate in place."

**Caveats**: resume assumes the target repo snapshot hasn't changed since
the run started. If a file's contents move under `find`, the `ok`/`empty`
ledger entry will still cause it to be skipped — re-scan by deleting the
matching row from the run's `files.jsonl`. (A future version may track
`content_sha`/`mtime` to detect drift automatically.) Similarly, `group`
only sees the findings and reviews that exist when it runs; if reviews
change after grouping, regenerate `groups.jsonl` to reflect them.

## Non-goals

- **No auto-merge.** `resolutions.jsonl` is a closure log, not a shipping
  pipeline. Humans drive the actual fix-and-merge step.
- **No build/lint/test integration.** If you want a stage that runs your
  test suite, write a prompt that tells the agent to do so — fettle won't
  invoke `task test` on your behalf.
- **No language awareness.** Fettle treats everything as text + globs. If
  your issue depends on AST analysis, your prompt tells the agent how to do
  it (or pre-extracts the data outside fettle).

## Versioning and the project marker

`.fettle.json` carries `fettle_version`, which fettle checks on every run
for compatibility. If the file is missing or malformed, fettle refuses to
write to the directory — protection against accidentally clobbering an
unrelated folder. Migrations between schema versions will be added once
there's a second version to migrate from.
