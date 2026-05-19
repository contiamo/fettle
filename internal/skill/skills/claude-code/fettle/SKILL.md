---
name: fettle
description: Drive fettle, the file-oriented LLM audit harness, from inside Claude Code. Use when the user wants to set up fettle on a repo, run a find pass over their codebase, add an agent review pass, launch the fettle UI, or otherwise work with fettle's find/review/close pipeline. Walks the user from zero through a running scan and into the UI without making them type fettle commands themselves.
allowed-tools:
  - Bash(fettle:*)
  - Bash(which:*)
  - Bash(ls:*)
  - Bash(pwd:*)
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

## Step 0 — orient

First, capture `<repo>` (you need it for the rest of the checks):

- Run `git rev-parse --show-toplevel`. Use the absolute path it prints.
- If it fails, the target isn't a git repo. Run `pwd` and use that; warn the user you're treating cwd as the repo root and you'll need `--walker fs` later.

Then run these in parallel:

1. `which fettle` — is it installed? If not, tell the user `go install github.com/contiamo/fettle/cmd/fettle@latest` and wait for them.
2. **Look for an existing project.** Glob both `fettle.json` and `*/fettle.json` rooted at `<repo>` (catches a project at the repo root, plus `audits/`, `fettle/`, and any non-default name the user picked). If exactly one match appears, set `<project>` to the absolute path of that match's directory and jump to **§ Existing project flow** below. If multiple match, ask the user which one — you've discovered they keep more than one fettle project in this repo.
3. Identify the primary language(s): look for `<repo>/go.mod`, `<repo>/package.json`, `<repo>/pyproject.toml` / `requirements.txt` / `setup.py`, `<repo>/Cargo.toml`, etc. Note whether the repo has a `conventions/` or `docs/conventions.md` style file — you'll point the find prompt at it later.

## Setup flow (fresh project)

### 1. Decide where the project lives

Default: a directory named `audits/` at the repo root. Use this unless the user names something else. (Why `audits/`: matches `FETTLE.md`'s canonical examples, neutral about content — works for refactors, doc audits, convention enforcement, security passes — and short.)

Tell the user: "I'll create `audits/` at the repo root. That's where the scan config, prompts, and run results will live. OK, or do you want a different name?"

Whatever they pick is `<project>` for the rest of this skill — substitute it for `audits` in every command below.

### 2. Decide include / exclude globs

Auto-propose based on language. Show the list to the user and let them refine. **Defaults below.**

You **must not** add `**/*_test.go`, `**/*.test.ts`, `test_*.py`, etc. to the exclude list. Tests are first-class scan targets — the find prompt has a test-quality section that catches duplicate tests, trivial tests, and over-complex tests. If a *specific* test directory is genuinely out of scope, exclude that directory by name, never the whole `_test.*` class.

With the default git walker (the one you want), `.gitignore` already drops `node_modules`, `dist`, `vendor` (if ignored), `__pycache__`, etc. You usually only need fettle-specific excludes: vendored UI components, generated code that's checked in.

| Language   | Suggested `--include`                       | Common `--exclude` (only what's not in `.gitignore`) |
|------------|---------------------------------------------|------------------------------------------------------|
| Go         | `**/*.go`                                   | `**/*_templ.go`, `**/*.pb.go`, `**/mock_*.go`, `**/mocks/**`, `vendor/**` |
| TypeScript | `src/**/*.{ts,tsx}`                         | `**/*.d.ts`, `**/__generated__/**`                   |
| Python     | `**/*.py`                                   | `**/migrations/**`, `**/_pb2.py`                     |

For mixed-language repos, pass `--include` multiple times. For anything not in the table, read the repo (Glob for the dominant extensions) and improvise the same shape: include the source globs, exclude generated and vendored code.

**Broad or non-code includes** (e.g. `**/*.md` for a documentation audit, `**/*` for a general pass): always add `--exclude '<project-basename>/**'` so fettle doesn't scan its own project directory (`fettle.json`, the stub instructions, your run output). `<project-basename>` is the last component of `<project>` — e.g. for the default `<repo>/audits`, the exclude is `--exclude 'audits/**'`. Code-only includes like `**/*.go` don't need this exclude because `<project>/` holds no Go files.

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

Ask the user: **"What do you want fettle to find?"** Give them concrete options to react to, not an open question:

- Convention enforcement (matches a `conventions/` doc — you'll point the prompt at it)
- Refactoring opportunities (duplication, dead code, over-complex functions)
- Documentation audits (missing or stale comments on public APIs)
- Security smells (lint-shaped — concatenated SQL, unescaped exec, hardcoded creds)
- Test quality (duplicate / trivial / over-complex tests)
- Some combination

Once the user picks, **edit `<project>/instructions/find.md` directly** (use the `Edit` tool — the stub is yours to rewrite):

1. **Rewrite `## Patterns to flag`.** Replace the example categories (`Category A — category:convention`, etc.) with the user's chosen categories. **Pin the category label names here.** The find agent should not invent new ones at run time. Use the `category:<bucket>` convention from the stub.
2. **Rewrite `## Severity scale`.** State what `high`/`medium`/`low` mean *in this project*. If the user uses a different scale (`P1`/`P2`/`P3`, numeric), use that — fettle doesn't enforce a scale.
3. **Fill in `## Required reading`** if the repo has a conventions doc. Point at it explicitly: `REPO_ROOT/conventions/go.md`, or wherever.
4. **Keep `## What NOT to flag`.** Tighten the language to the user's domain but don't drop the section — it's load-bearing for keeping noise out.

Show the user the diff of your edit before moving on. Ask: "Does this match what you want to find?" Apply at most **one** round of revisions here. After that, commit to a run and iterate from real findings — abstract prompt review without data has diminishing returns.

### 5. Smoke test

```bash
fettle --project-dir <project> run find --limit 3 -c 1
```

This runs the find agent on 3 files with concurrency 1. Takes ~60–180s. Run it in the foreground and surface progress. When it finishes, the command prints a full run path like `<project>/runs/run_3cdf6f_20260519T120000Z` — **capture that path**; you'll need it to inspect raw logs. The slug (`3cdf6f`) is what `--run` flags accept.

Show the findings:

```bash
fettle --project-dir <project> list findings --run <slug>
```

Walk the user through what came back. Ask: **"Are these the kinds of things you want to find?"**

- **Yes** → proceed to the real run.
- **No, the prompt is fundamentally wrong** → revise `<project>/instructions/find.md` once more, then commit to a real run regardless. Don't loop on smoke tests.
- **No findings at all** — try one of these in order, then **stop and commit to a real run regardless**:
  1. Re-run with `--limit 10 -c 1` (more files might surface something).
  2. Read `<project>/runs/run_<slug>_<ts>/raw/` (use the full path you captured) to see what the agent actually said per file. If the agent reasoned correctly and concluded "nothing to flag," your prompt's bar is fine — the smoke set just happened to be clean files. Proceed to real run.
  3. If raw logs show the agent didn't engage with the prompt (e.g. it summarized the file instead of judging it), revise the prompt once and go straight to the real run.

### 6. Real find run

Estimate duration: roughly `(file_count × 60s) / concurrency`. Tell the user the estimate. File count: use the `Glob` tool with `<repo>` as the base and one of your `--include` patterns (e.g. `**/*.go`). It overcounts vs. fettle's git walker (no `.gitignore` filtering), so present the number as an upper bound.

```bash
fettle --project-dir <project> run find -c 4
```

Run in the foreground so the user sees progress. Higher `-c` (8, 16) goes faster but hits rate limits and uses more tokens — only suggest it if the user has Anthropic's higher-tier rate limits.

If the run is interrupted, resume with concurrency still set (resume doesn't infer it):

```bash
fettle --project-dir <project> run find --resume <slug> -c 4
```

### 7. Launch the UI in the background

Once the run completes, launch the UI as a backgrounded process:

```bash
fettle --project-dir <project> ui
```

Run this with `run_in_background: true` on the Bash tool. The process logs its listen URL (typically `http://localhost:8765` or similar) to stderr — read it back with `BashOutput` and tell the user the URL to open.

The UI stays running while you and the user keep talking. The user reviews findings in the browser; you stay available for follow-ups ("add a label to all findings in `internal/foo/`", "what does this category mean?", etc.).

### 8. Introduce the review stage (don't run yet)

Once findings are in the UI, tell the user:

> You can run an adversarial review pass with a different agent — if your find run used Claude, run review with Codex; if it used Codex, run review with Claude. A second model reading the same findings catches false positives the first model rubber-stamped.

Show the command but **don't run it yet** — review takes time and the user may want to look at findings first:

```bash
fettle --project-dir <project> run review --run <slug> --agent codex
```

If the user wants the review pass, **tailor `<project>/instructions/review.md` first** using the same logic as step 4: edit it to pin the triage vocabulary (`verdict:ship`, `verdict:drop`, `verdict:needs-human` are the stub defaults — keep them unless the user has a specific scheme). Then run the review command.

## Existing project flow

You arrived here because step 0 found a `<project>/fettle.json`. Don't re-init. Ask the user what they want to do — give them options, not an open question:

- **Run a new find pass** → `fettle --project-dir <project> run find -c 4` (or `--resume <slug> -c 4` for an interrupted run). Reuses the existing `instructions/find.md`. Prompt edits made after the run starts have no effect on that run — they apply to the next one.
- **Edit the find prompt then run a new pass** → edit `<project>/instructions/find.md`, then run find as above.
- **Run a review pass on an existing find run** → `fettle --project-dir <project> list runs` to see slugs, then `fettle --project-dir <project> run review --run <slug> --agent <other-agent>`. Tailor `<project>/instructions/review.md` first if you haven't (same logic as step 4 in the setup flow).
- **Launch the UI** → `fettle --project-dir <project> ui` in the background; share the URL.
- **List or inspect findings from the CLI** → `fettle --project-dir <project> list findings --run <slug>`, `fettle --project-dir <project> show finding --run <slug> <id>`.

`fettle list runs` prints a JSON envelope: `{"data": [{...}, ...]}`. If `data` is empty, the project has been init'd but never run — jump back to step 4 (tailor the find prompt) of the setup flow.

## Things to never do

- **Don't invent fettle CLI commands or flags.** Stick to what's in `fettle --help` and what this skill shows. If something you need seems missing, tell the user — that's a fettle-side gap, not something to work around with shell glue.
- **Don't exclude tests** from `--include`/`--exclude` configs. See the include/exclude table above.
- **Don't re-init an existing project.** `fettle init` refuses, and re-running would be a sign you didn't check.
- **Don't run project-scoped commands (`run find`, `run review`, `list`, `show`, `ui`) without `--project-dir <project>`**. Without it, fettle upward-walks from cwd for a `fettle.json` — fragile and cwd-dependent. Always be explicit.
- **Don't omit `--walker fs`** for a non-git target at `fettle init` time. `--walker` is an init-only flag; later runs inherit it from `fettle.json`. The default `--walker git` shells to `git ls-files` and will fail on non-git targets.
- **Don't run `fettle ui` in the foreground.** It blocks until killed; the user needs to keep talking to you.
- **Don't run `fettle run find` without `-c N`** on real (non-smoke) runs — be explicit about the concurrency the user wants, including on `--resume` (it isn't inferred from the original run).
- **Don't expect prompt edits to retroactively affect a running or resumed run.** Each run snapshots its prompt at start time. Edit the template, then start a *new* run to pick up changes.
- **Don't summarise the run at the end** unless the user asks. They can read the findings themselves in the UI; you're not narrating.
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
fettle --project-dir <project> run review --run <slug> --agent <name> # review pass

# Reads
fettle --project-dir <project> list runs                              # all runs in the project
fettle --project-dir <project> list findings --run <slug>             # findings in a run
fettle --project-dir <project> show finding --run <slug> <id>         # one finding
fettle --project-dir <project> show run <slug>                        # run status

# UI (background!)
fettle --project-dir <project> ui                                     # serves localhost, prints URL
```

All `--run <slug>` flags accept the short hex slug from the run dir name, or a unique prefix.

## When done

The user has a running scan, findings in the UI, and knows how to come back for review or a new run. Hand control back. Don't summarise unless asked.
