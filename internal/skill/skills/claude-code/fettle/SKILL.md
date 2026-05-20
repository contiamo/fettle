---
name: fettle
description: Drive fettle, the file-oriented LLM audit harness, from inside Claude Code. Use when the user wants to set up fettle on a repo, run a find pass over their codebase, add an agent review pass, launch the fettle UI, or otherwise work with fettle's find/review/close pipeline. Walks the user from zero through a running scan and into the UI without making them type fettle commands themselves.
allowed-tools:
  - Bash(fettle:*)
  - Bash(which:*)
  - Bash(ls:*)
  - Bash(pwd:*)
  - Bash(mkdir:*)
  - Bash(git rev-parse:*)
  - Bash(git ls-files:*)
  - Bash(cat:*)
  - Glob
  - Grep
  - Read
  - Edit
  - Write
---

# fettle — drive the audit harness end-to-end

You are running fettle for the user. The user has probably never run it before. They have a coding repo open. Your job is to take them from "I want to use fettle on this project" to "I'm browsing real findings in the UI" without them typing a fettle command themselves.

If you need the full design model, read `FETTLE.md` in the fettle source (or at https://github.com/contiamo/fettle/blob/main/FETTLE.md). You don't usually need to — this skill carries the workflow.

## What fettle is (one paragraph)

Fettle runs an LLM agent over every file in a target repo and records findings as JSONL. The user supplies the prompt (`instructions/find.md`); fettle handles spawning, output, resume. There's an optional second stage (`review`) where a different agent triages the findings. A local web UI (`fettle ui`) lets a human browse, label, comment, and close them. **One file at a time** is fettle's contract — it's good for refactor sweeps, convention enforcement, doc-comment audits, lint-shaped security checks; it's *not* for cross-file dataflow analysis.

## Vocabulary used in this skill

- **`<repo>`** — the **absolute path** of the user's code repo (the target being scanned). Set this to the git top-level: run `git rev-parse --show-toplevel` and use the absolute path it prints. If `git rev-parse` fails because the target isn't a git repo, `<repo>` is your starting cwd (use `pwd` to capture it); warn the user that you're treating cwd as the repo root.
- **`<project>`** — the **absolute path** of the fettle project directory, holding `fettle.json`, `instructions/`, `runs/`. Defaults to `<repo>/audits`. The user may pick a different name in step 1; from then on, `<project>` is the absolute path of whatever they chose.
- **`<slug>`** — the 6-character hex identifier in a run dir name like `run_3cdf6f_20260519T120000Z`. Anywhere a `--run` flag accepts a value, the slug (or a unique prefix) works.

**All filesystem paths in this skill are absolute** (`<project>`, `<repo>`, and any path you Read/Edit/list). The Bash tool's cwd doesn't affect any command here — `fettle init <project> --target <repo>` and `fettle --project-dir <project> ...` work from anywhere. Don't `cd`; resolve paths to absolute up front and pass them in.

**Exception: include/exclude globs are patterns, not paths.** They're matched against repo-relative paths inside fettle — e.g. `**/*.go`, `vendor/**`. Never put an absolute path in `--include` or `--exclude`.

## Must-ask gates — STOP and wait for the user

The skill below has three points where you **must** send a message and wait for the user's reply before doing anything else. These aren't advice; the user's judgment is load-bearing for the rest of the run.

1. **Project structure** (step 1) — which area to set up first; never decide alone for a monorepo.
2. **Find categories + prompt diff** (step 4) — which categories to scan for, and explicit acknowledgment of the rewritten `find.md` before any run. Two sub-gates.
3. **Smoke-test verdict** (step 5) — paste findings (or a "zero findings" note with a raw-log excerpt) and ask whether they match what the user wanted. Don't self-diagnose past the user.

A fourth conditional gate applies in step 6: if the estimated cost of the real run is high (~$50+), STOP and confirm before kicking off — and never silently rescope the run to dodge it.

At each gate: send your message, then **stop calling tools** until the user responds. **Do not pre-create a todo list that spans multiple gates** — each gate's outcome may invalidate the rest of the plan, and momentum from a pre-built list is the most reliable way to skip stops.

## Step 0 — orient

First, capture `<repo>` (you need it for the rest of the checks):

- Run `git rev-parse --show-toplevel`. Use the absolute path it prints.
- If it fails, the target isn't a git repo. Run `pwd` and use that; warn the user you're treating cwd as the repo root and you'll need `--walker fs` later.

Then run these in parallel:

1. `which fettle` — is it installed? If not, tell the user `go install github.com/contiamo/fettle/cmd/fettle@latest` and wait for them.
2. **Look for an existing project.** Glob `fettle.json`, `*/fettle.json`, and `*/*/fettle.json` rooted at `<repo>` (catches a project at the repo root, a flat `audits/fettle.json`, and the nested monorepo layout `audits/backend/fettle.json`). If exactly one match appears, set `<project>` to the absolute path of that match's directory and jump to **§ Existing project flow** below. If multiple match, ask the user which one to work with — you've discovered a monorepo with several fettle projects, and this skill walks one at a time.
3. Identify the primary language(s): look for `<repo>/go.mod`, `<repo>/package.json`, `<repo>/pyproject.toml` / `requirements.txt` / `setup.py`, `<repo>/Cargo.toml`, etc. Also note conventions / best-practices sources you might point the find prompt at later: `CLAUDE.md`, `AGENTS.md`, a `conventions/` folder, `docs/conventions.md`, `STYLE.md`, `CONTRIBUTING.md`, language-specific style guides, etc.

## Setup flow (fresh project)

### 1. Decide on one project or several, and where it lives

**Monorepo / mixed-language case first.** If step 0 found distinct language ecosystems or service trees (e.g. Go backend + React frontend, or several services under one root), separate fettle projects per area usually give better results — find prompts diverge (Go conventions vs. React component patterns), severity scales differ, and you'll often run them on different cadences. Use a nested layout that mirrors the repo's component names: `audits/backend/`, `audits/frontend/`, `audits/<service>/`, etc.

**STOP and ask** the user which area to set up first. Send a message like *"I see a Python backend and a TypeScript frontend — which should I set up first?"* and wait for their answer. Do not run `fettle init` until they reply. Mention that the others can be set up later by re-running the skill.

For nested layouts, `audits/` itself must exist before `fettle init audits/backend` runs — fettle init requires the parent directory. Run `mkdir -p <repo>/audits` once before the first nested init.

For a single-language repo, default to a flat `audits/` at the repo root. (Why `audits/`: matches `FETTLE.md`'s canonical examples, neutral about content — works for refactors, doc audits, convention enforcement, security passes — and short.) Explain to the user what this directory will hold (scan config, prompts, run results) and let them rename it if they want.

Whatever they pick is `<project>` for the rest of this skill — an absolute path, e.g. `<repo>/audits` (flat) or `<repo>/audits/backend` (nested).

### 2. Decide include / exclude globs

Auto-propose based on language. Show the list to the user and let them refine. **Defaults below.**

**Don't reflexively exclude tests** (`**/*_test.go`, `**/*.test.ts`, `test_*.py`, etc.). Tests are first-class scan targets — the default find prompt has a test-quality section that catches duplicate tests, trivial tests, and over-complex tests. Only exclude them if the user explicitly says they want test files out of scope (e.g. "I only care about production code right now"). If a *specific* test directory is genuinely out of scope, prefer excluding that directory by name over excluding the whole `_test.*` class.

With the default git walker (the one you usually want), a common `.gitignore` already drops `node_modules`, `dist`, `vendor` (if ignored), `__pycache__`, etc. You usually only need fettle-specific excludes: vendored UI components, generated code that's checked in.

| Language   | Suggested `--include`                       | Common `--exclude` (only what's not in `.gitignore`) |
|------------|---------------------------------------------|------------------------------------------------------|
| Go         | `**/*.go`                                   | `**/*_templ.go`, `**/*.pb.go`, `**/mock_*.go`, `**/mocks/**`, `vendor/**` |
| TypeScript | `src/**/*.{ts,tsx}`                         | `**/*.d.ts`, `**/__generated__/**`                   |
| Python     | `**/*.py`                                   | `**/migrations/**`, `**/_pb2.py`                     |

For mixed-language repos, pass `--include` multiple times. For anything not in the table, read the repo (Glob for the dominant extensions) and improvise the same shape: include the source globs, exclude generated and vendored code.

**Broad or non-code includes** (e.g. `**/*.md` for a documentation audit, `**/*` for a general pass): always exclude the fettle project directory so fettle doesn't scan its own config, prompts, and run output. Use the project's path *relative to* `<repo>`:

- Flat layout (`<repo>/audits`) → `--exclude 'audits/**'`.
- Nested layout (`<repo>/audits/backend`) → `--exclude 'audits/**'` covers all sibling projects under `audits/` and is usually what you want.

Code-only includes like `**/*.go` don't need this exclude because `<project>/` holds no Go files.

**Non-git target.** If step 0's `git rev-parse` failed, the user's repo isn't a git repo. You must add `--walker fs` to the init command — the default git walker shells to `git ls-files` and fails on non-git targets. Without git's `.gitignore` filter, also be more aggressive in `--exclude` (add `node_modules/**`, `dist/**`, `build/**`, `__pycache__/**`, etc. explicitly).

### 3. Run `fettle init`

```bash
fettle init <project> --target <repo> --include '<glob>' [--include '<glob>'...] [--exclude '<glob>'...]
```

Non-git target — add `--walker fs`:

```bash
fettle init <project> --target <repo> --walker fs --include '<glob>' --exclude '...'
```

Both `<project>` and `<repo>` are absolute paths captured in step 0 / step 1. Don't substitute `.` or `..` — fettle resolves them relative to the cwd of the Bash invocation, which is unreliable.

If init fails, offer the user one of these concrete options based on the error:

- **"already contains a fettle.json"** — this repo already has fettle. Set `<project>` to the directory the error names and jump to the Existing project flow.
- **"already exists and is not empty"** — the user picked a name that collides with an existing directory. Offer: (a) pick a different `<project>` name, or (b) `ls <project>/` together and have the user manually move the contents out before retrying.
- **"parent directory does not exist"** — they typed a nested path whose parent isn't there. Offer to create the parent, or pick a simpler path.
- **"at least one --include glob is required"** — you forgot the flag. Re-run with the language-appropriate glob.

### 4. Tailor `instructions/find.md` — the highest-leverage step

`fettle init` wrote a stub at `<project>/instructions/find.md` with placeholder sections. The stub is generic; the user's real value comes from rewriting it to their domain.

**STOP.** Before editing `find.md`, send the user the category menu below and ask which ones they want (multi-select is fine). Wait for their reply. **Don't pick categories on their behalf**, even if you're confident from reading the repo — pinning the wrong categories now means the whole run finds the wrong things and the user has to redo it.

- Convention enforcement (matches a conventions doc — you'll point the prompt at it)
- Refactoring opportunities (duplication, dead code, over-complex functions)
- Documentation audits (missing or stale comments on public APIs)
- Security smells (lint-shaped — concatenated SQL, unescaped exec, hardcoded creds)
- Test quality (duplicate / trivial / over-complex tests)
- Some combination

Once the user has picked, **edit `<project>/instructions/find.md` directly** (use the `Edit` tool — the stub is yours to rewrite):

1. **Rewrite `## Patterns to flag`.** Replace the example categories (`Category A — category:convention`, etc.) with the user's chosen categories. **Pin the category label names here.** The find agent should not invent new ones at run time. Use the `category:<bucket>` convention from the stub.
2. **Rewrite `## Severity scale`.** State what `high`/`medium`/`low` mean *in this project*. If the user uses a different scale (`P1`/`P2`/`P3`, numeric), use that — fettle doesn't enforce a scale.
3. **Fill in `## Required reading`** if the repo has a conventions doc. Point at it explicitly: `REPO_ROOT/conventions/go.md`, or wherever.
4. **Keep `## What NOT to flag`.** Tighten the language to the user's domain but don't drop the section — it's load-bearing for keeping noise out.

**STOP.** Show the user the diff of your edit and ask explicitly: *"Does this match what you had in mind?"* Wait for their reply before any run. Apply at most **one** round of revisions here; after that, commit to a run and iterate from real findings — abstract prompt review without data has diminishing returns.

### 5. Smoke test

```bash
fettle --project-dir <project> run find --limit 3 -c 1
```

This runs the find agent on 3 files with concurrency 1. Takes ~60–180s. Run it in the foreground and surface progress. When it finishes, the command prints a full run path like `<project>/runs/run_3cdf6f_20260519T120000Z` — **capture that path**; you'll need it to inspect raw logs. The slug (`3cdf6f`) is what `--run` flags accept.

Show the findings:

```bash
fettle --project-dir <project> list findings --run <slug>
```

**STOP.** Paste the findings (or, if zero, a "zero findings — here's what the agent reasoned about one file" note with a short excerpt from a raw log) and ask the user explicitly: *"Are these the kinds of findings you want?"* Wait for their reply. **Branch on what they tell you, not on your own read of the raw logs** — the whole point of the smoke test is to get the user's "yes/no" before spending real money on the full run.

After the user weighs in, branch on the answer:

- **Yes, looks right** → proceed to the real run (step 6).
- **No, the prompt is fundamentally wrong** → revise `<project>/instructions/find.md` once more (re-checking with the user via the step-4 diff gate), then commit to a real run regardless. Don't loop on smoke tests.
- **No findings at all** — the diagnostic options below help you give the user better context for *their* decision; they don't replace the ask-gate:
  1. Re-run with `--limit 10 -c 1` (more files might surface something).
  2. Read `<project>/runs/run_<slug>_<ts>/raw/` (use the full path you captured) to see what the agent actually said per file. If the agent reasoned correctly and concluded "nothing to flag," tell the user the prompt's bar looks right — the smoke set may just be clean files.
  3. If raw logs show the agent didn't engage with the prompt (e.g. it summarized the file instead of judging it), tell the user and offer to revise the prompt.

### 6. Launch the UI, then run the real find pass

Launch the UI *before* the real run so the user can watch findings stream in as files complete. Fettle writes findings to JSONL on disk as each file finishes; the UI reads from the same files.

```bash
fettle --project-dir <project> ui
```

Run with `run_in_background: true` on the Bash tool. The process logs its listen URL (typically `http://localhost:8765` or similar) to stderr — read it with `BashOutput` and share the URL with the user. Tell them they can pick the in-progress run from the run picker once it appears, and refresh as findings arrive.

Before starting the real find run, estimate **duration and cost** and surface both to the user:

- **File count:** use the `Glob` tool with `<repo>` as the base and one of your `--include` patterns; it overcounts vs. fettle's git walker (no `.gitignore` filtering), so present it as an upper bound.
- **Duration:** roughly `(file_count × 60s) / concurrency`.
- **Cost:** roughly `file_count × ~$0.30` at current Sonnet pricing (varies with file size and model).

**Cost gate.** If the estimated cost is over ~$50, **STOP and confirm** with the user before kicking off. Present the estimate plainly: *"~N files × ~$0.30 = ~$X. Proceed, or scope to a subtree?"* and wait for their answer. **Never silently rescope** to dodge the price — that hides the trade-off from the user. If the user wants to scope down, override `--include` at run time and give the run a recognizable name:

```bash
fettle --project-dir <project> run find -c 4 \
  --include 'backend/src/services/**/*.py' --name services
```

`--include` at run time overrides what's in `fettle.json` for this run only. `--name <slug>` replaces the random hex with a meaningful slug — useful when the user is running several scoped passes (`--name services`, `--name api`, etc.). Without `--name`, the slug is 6 random hex characters.

Once the user OKs the cost, kick off the full run:

```bash
fettle --project-dir <project> run find -c 4
```

Run in the foreground so the user sees progress. Higher `-c` (8, 16) goes faster but hits rate limits and uses more tokens — only suggest it if the user has Anthropic's higher-tier rate limits.

If the run is interrupted, resume with concurrency still set (resume doesn't infer it):

```bash
fettle --project-dir <project> run find --resume <slug> -c 4
```

### 7. Brief summary, then introduce review

When the run completes, give a short summary so the user has a sense of scale before opening the UI in detail. Read findings with `fettle --project-dir <project> list findings --run <slug>` and report:

- Total finding count.
- Breakdown by `severity` (and by `category:*` label if categories are pinned).
- A couple of representative or surprising findings — file paths and titles, not full descriptions.

Keep it tight (≤5 bullet points). The findings live in the UI; you're orienting, not narrating.

Then mention the review stage as a follow-up the user can run when they're ready. Explain (in your own words) that running review with a *different* agent than the find pass (Claude for finds → Codex for review, or vice versa) catches false positives the first model rubber-stamped. Show the command but don't run it automatically — review takes time and the user usually wants to skim findings first:

```bash
fettle --project-dir <project> run review --run <slug> --agent codex
```

If the user wants the review pass, **tailor `<project>/instructions/review.md` first** using the same approach as step 4: edit it to pin the triage vocabulary (`verdict:ship`, `verdict:drop`, `verdict:needs-human` are the stub defaults — keep them unless the user has a specific scheme). Then run the review command.

### 8. Recording outcomes as the user fixes findings

After the user reviews findings in the UI, they'll often ask you to fix some. Whenever you act on a finding — apply the fix, open a PR, or confirm it's not actionable — record an outcome so the run's status reflects what happened:

```bash
FETTLE_AUTHOR=agent:claude fettle --project-dir <project> add outcome \
  --run <slug> --finding <id> --status <status> [--pr <url>]
```

`<status>` is free-form. Common values: `fixed` (change applied locally), `merged` (PR/MR shipped), `wont-fix` (user decided not to), `not-applicable` (false positive, confirmed). Pass `--pr <url>` whenever there's an upstream change-request URL — GitHub PR, GitLab MR, Gerrit, etc.

**Author identity.** Outcomes carry an author. The CLI chains `FETTLE_AGENT` → `$FETTLE_AUTHOR` → `~/.config/fettle/identity` → error. Set `FETTLE_AUTHOR=agent:<model>` (e.g. `agent:claude`) before the call so outcomes you record are distinguishable from outcomes the user records directly in the UI. If neither env var nor identity file is set, the command errors — surface that and have the user set their identity once in the UI (or `export FETTLE_AUTHOR=<their-slug>` for the session).

## Existing project flow

You arrived here because step 0 found a `<project>/fettle.json`. Don't re-init. Offer the user concrete options, not an open question:

- **Run a new find pass** → `fettle --project-dir <project> run find -c 4` (or `--resume <slug> -c 4` for an interrupted run). Reuses the existing `instructions/find.md`. Prompt edits after a run starts only apply to the *next* run.
- **Edit the find prompt then run a new pass** → edit `<project>/instructions/find.md`, then run find as above.
- **Run a review pass on an existing find run** → `fettle --project-dir <project> list runs` to see slugs, then `fettle --project-dir <project> run review --run <slug> --agent <other-agent>`. Tailor `<project>/instructions/review.md` first if you haven't (same approach as step 4 in the setup flow).
- **Launch the UI** → `fettle --project-dir <project> ui` in the background; share the URL.
- **Fix findings and record outcomes** → see step 8 of the setup flow for the `add outcome` shape.
- **List or inspect findings from the CLI** → `fettle --project-dir <project> list findings --run <slug>`, `fettle --project-dir <project> show finding --run <slug> <id>`.

`fettle list runs` prints a JSON envelope: `{"data": [{...}, ...]}`. If `data` is empty, the project has been init'd but never run — jump back to step 4 (tailor the find prompt) of the setup flow.

## Things to never do

- **Don't invent fettle CLI commands or flags.** Stick to what's in `fettle --help` and what this skill shows. If something you need seems missing, tell the user — that's a fettle-side gap, not something to work around with shell glue.
- **Don't reflexively exclude tests** from `--include`/`--exclude` configs — only when the user explicitly asks. See the include/exclude section above.
- **Don't re-init an existing project.** `fettle init` refuses, and re-running would be a sign you didn't check.
- **Don't run project-scoped commands (`run find`, `run review`, `list`, `show`, `ui`) without `--project-dir <project>`**. Without it, fettle upward-walks from cwd for a `fettle.json` — fragile and cwd-dependent. Always be explicit.
- **Don't omit `--walker fs`** for a non-git target at `fettle init` time. `--walker` is an init-only flag; later runs inherit it from `fettle.json`. The default `--walker git` shells to `git ls-files` and will fail on non-git targets.
- **Don't run `fettle ui` in the foreground.** It blocks until killed; the user needs to keep talking to you.
- **Don't run `fettle run find` without `-c N`** on real (non-smoke) runs — be explicit about the concurrency the user wants, including on `--resume` (it isn't inferred from the original run).
- **Don't silently rescope a run** to make it cheaper or faster (e.g. quietly overriding `--include` to a subtree). If the cost or duration estimate worries you, present the number to the user and let them decide. They may accept the price; they may pick a different scope than you'd guess.
- **Don't pre-create a multi-step todo list** that spans the must-ask gates. Each gate's outcome may invalidate the rest of the plan, and todo-list momentum is the most reliable way to skip stops. Track the next step only; let the user's answer shape the one after.
- **Don't expect prompt edits to retroactively affect a running or resumed run.** Each run snapshots its prompt at start time. Edit the template, then start a *new* run to pick up changes.
- **Don't write long prose summaries** of the run. A short stats summary (counts, severity/category breakdown, 1–2 representative findings) is helpful and expected — see step 7. Beyond that the user reads the UI.
- **Don't edit `<project>/runs/`.** Run output is append-only and self-identifying. If something looks wrong, diagnose, don't rewrite.

## Reference: command shapes

All paths are absolute (`<repo>` and `<project>` captured in step 0 / step 1). Cwd doesn't matter.

```bash
# Setup
fettle init <project> --target <repo> --include '**/*.go' [--exclude '...'...]
fettle init <project> --target <repo> --walker fs --include '...' --exclude '...'   # non-git target

# Stages
fettle --project-dir <project> run find -c 4                          # real find pass
fettle --project-dir <project> run find --limit 3 -c 1                # smoke test
fettle --project-dir <project> run find --resume <slug> -c 4          # resume interrupted run
fettle --project-dir <project> run find -c 4 \
  --include '<subtree>/**' --name <slug>                              # scoped real run with named slug
fettle --project-dir <project> run review --run <slug> --agent <name> # review pass

# Reads
fettle --project-dir <project> list runs                              # all runs in the project
fettle --project-dir <project> list findings --run <slug>             # findings in a run
fettle --project-dir <project> show finding --run <slug> <id>         # one finding
fettle --project-dir <project> show run <slug>                        # run status

# Outcomes (when the user fixes a finding)
FETTLE_AUTHOR=agent:claude fettle --project-dir <project> add outcome \
  --run <slug> --finding <id> --status <fixed|merged|wont-fix|...> [--pr <url>]

# UI (background!)
fettle --project-dir <project> ui                                     # serves localhost, prints URL
```

All `--run <slug>` flags accept the short hex slug from the run dir name, or a unique prefix.

## When done

The user has a running scan, findings in the UI, the brief stats summary from step 7, and knows how to come back for review, a new run, or recording outcomes. Hand control back and stay available for follow-ups — labelling questions, "fix this finding for me" requests (record an outcome when you do), or running a review pass when the user is ready.
