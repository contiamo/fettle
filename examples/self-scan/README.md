# self-scan — fettle scanning fettle

A worked example: fettle pointed at its own Go source. Useful for
dogfooding the harness and as a template you can copy into your own
project.

## Layout

```
.fettle.json              committed; target_repo: "../.."
instructions/
  find.md                 real Go conventions / code-smells prompt
  review.md               stub (review command not yet implemented)
  group.md                stub
runs/                     gitignored; created on first `fettle find`
```

`target_repo` is relative; fettle resolves it against this directory at
load time, so the example works on any clone.

## Run it

From the repo root:

```
go build -o /tmp/fettle ./cmd/fettle
/tmp/fettle --dir examples/self-scan find --limit 5
```

`--limit 5` keeps the first run cheap. Drop it for a full scan. Output
goes to `examples/self-scan/runs/find_<timestamp>_<slug>/findings.jsonl`.

To resume a killed scan:

```
/tmp/fettle --dir examples/self-scan find --resume <run-folder>
```

The snapshotted prompt at `<run>/instructions/find.md` is what runs on
resume — editing the template here doesn't retroactively affect a
running scan.

## Adapting this example

Copy this directory into your own project, edit `.fettle.json` (set
`target_repo`, `agent`, `include`, `exclude`), then rewrite
`instructions/find.md` for what you actually want to find. Everything
else is fettle plumbing.
