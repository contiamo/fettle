# self-scan — fettle scanning fettle

A worked example: fettle pointed at its own Go source. Useful for
dogfooding the harness and as a template you can copy into your own
project.

## Layout

```
.fettle.json              committed; target_repo: "../.."
instructions/
  find.md                 real Go conventions / code-smells prompt
  review.md               domain rubric for the review stage
  dedupe.md               domain rubric for cross-run consolidation
  group.md                domain rubric for clustering into PR-sized batches
runs/                     gitignored; created on first stage run
```

`target_repo` is relative; fettle resolves it against this directory at
load time, so the example works on any clone.

## Run it

From the repo root, build and install once:

```sh
go install ./cmd/fettle
```

Then run a small find pass against this project:

```sh
fettle --dir examples/self-scan run find --limit 5
```

`--limit 5` keeps the first run cheap. Drop it for a full scan.
Output goes to `examples/self-scan/runs/find_<UTC-ts>_<slug>/`.

To resume a killed scan:

```sh
fettle --dir examples/self-scan run find --resume runs/<run-folder>/
```

The snapshotted prompt at `<run>/instructions/find.md` is what runs on
resume — editing the template here doesn't retroactively affect a
running scan.

## Play with the rest of the pipeline

Once you have a find run, every other CLI works against it. Browse:

```sh
fettle --dir examples/self-scan list runs
fettle --dir examples/self-scan show run runs/<run>/
fettle --dir examples/self-scan list findings --run runs/<run>/
fettle --dir examples/self-scan show finding --run runs/<run>/ <id>
```

Review the findings (one agent invocation per finding):

```sh
fettle --dir examples/self-scan run review --run runs/<run>/
fettle --dir examples/self-scan list reviews --run runs/<run>/
fettle --dir examples/self-scan show review --run runs/<run>/ --finding <id>
```

Cluster into PR-sized batches:

```sh
fettle --dir examples/self-scan run group --run runs/<run>/
fettle --dir examples/self-scan list groups --run runs/<group-run>/
```

Mark outcomes as you ship fixes:

```sh
fettle --dir examples/self-scan add outcome --run runs/<run>/ --finding <id> --status merged --pr <url>
fettle --dir examples/self-scan show outcome --run runs/<run>/ --finding <id>
fettle --dir examples/self-scan list outcomes --run runs/<run>/
```

For a second find run with a different agent, then dedupe:

```sh
fettle --dir examples/self-scan run find --agent codex --limit 5 --name codex-pass
fettle --dir examples/self-scan run dedupe --run runs/<find1>/ --run runs/<find2>/
```

Or merge two non-overlapping runs (e.g. one over `**/*.go`, another over `**/*.ts`):

```sh
fettle --dir examples/self-scan run merge --run runs/<go-run>/ --run runs/<ts-run>/
```

## Adapting this example

Copy this directory into your own project, edit `.fettle.json` (set
`target_repo`, `agent`, `include`, `exclude`), then rewrite each
`instructions/*.md` for your domain. Everything else is fettle plumbing.
