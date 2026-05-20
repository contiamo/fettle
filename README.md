# fettle

A file-oriented LLM audit harness. You write a markdown prompt that says what to look for; fettle runs an agent — Claude Code, Codex, Gemini, or any CLI you script — over every matching file in your repo and records findings to JSONL. A local web UI lets you triage.

*A great way to make use of the token budget you have left over at the end of the week ;)*

**To get started, give this to your agent:**
```
Read https://github.com/contiamo/fettle/blob/main/SETUP-WITH-AGENT.md 
Then walk me through setting up fettle on this project.
```

**Good for:** convention enforcement, refactor sweeps, doc-comment audits, test-quality passes, lint-shaped security checks — anything decidable from a single file plus your written rules.
**Not for:** cross-file dataflow, runtime analysis, route/middleware authorization, dependency-graph compliance.

Each finding lands as one JSONL line — `file`, `line`, `title`, `description`, `suggestion`, `severity`, `labels`. Append-only on disk, browsable in the UI, re-runnable.

The agent will install fettle, install the fettle skill into its own skill directory, then walk you through choosing what to scan, tailoring the find prompt to your domain, doing a smoke test, running the real find pass, and launching the UI. You don't type a fettle command yourself.

If you'd rather drive manually, see [`FETTLE.md`](FETTLE.md) for the full CLI. Quick install:

```bash
go install github.com/contiamo/fettle/cmd/fettle@latest   # latest tagged release
go install github.com/contiamo/fettle/cmd/fettle@main     # development tip
```

`fettle --version` reports the version, git commit, and dirty state of the binary you built.

## Key concepts

### Pipeline: find → review → close

```
find          →     review            →     close
per-file            per-finding              track
agent scan          agent and/or human       outcomes
                    review
```

Stages are independent. Stop after `find` if a report is all you need. Hand findings to a human reviewer via the UI without ever running the review agent. Run `review` with a *different* agent than `find` to catch the false positives the first model rubber-stamped (Claude finds → Codex reviews, or vice versa).

### Your prompt is the product

The harness is generic; the knowledge is yours. You supply `instructions/find.md` and (optionally) `instructions/review.md` — plain markdown telling the agent what to flag and what to skip. The agent reads each target file, decides what counts, and shells back to `fettle add finding` for each one. Fettle owns ids, timestamps, and disk writes; the agent never writes a fettle data file directly.

This is what the agent-driven setup spends most of its time on: tailoring the stub prompt to *your* conventions, *your* category labels, *your* severity scale.

### File-oriented contract

One agent invocation, one file. The primary anchor for any finding is `(file, line)`. v0 trades the range of cross-file analyses for a sharper contract: every finding points at a specific line, every run is resumable, parallelism is trivial.

### Agent-agnostic

Fettle invokes the agent as a CLI — `claude -p`, `codex exec`, or any script you write. The contract is: receive a prompt, decide what to record, shell to `fettle add finding|review|outcome`. No tool-calling protocol, no session model. Bring your own agent.

### Local-first storage

No service, no database. Every run is a folder under `<project>/runs/`. JSONL files append-only. The web UI is a Go binary with embedded assets running on localhost — it reads and writes the same files. Copy a run folder onto a teammate's checkout and the next read picks it up.

## See also

- [`FETTLE.md`](FETTLE.md) — full design doc: project layout, CLI reference, manifest shape, resolution rules, atomicity model.
- [`SETUP-WITH-AGENT.md`](SETUP-WITH-AGENT.md) — the bootstrap markdown your agent reads to drive setup.

## Status

Pre-1.0. The data shape and CLI surface are still moving; expect to re-init projects between versions. Don't depend on it for anything you can't re-run.
