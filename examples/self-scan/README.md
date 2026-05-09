# self-scan — fettle scanning fettle

A worked example: fettle pointed at its own Go source. Useful for
dogfooding the harness and as a template you can copy into your own
project.

## Layout

```
.fettle/                      committed (config + instructions)
  config.json                 target_repo: "../.."
  instructions/
    find.md                   Go conventions / code-smells prompt
    review.md                 per-finding review prompt
  runs/                       gitignored; created on first stage run
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
Output goes to
`examples/self-scan/.fettle/runs/find_<UTC-ts>_<slug>/`, with each
finding written to its own `findings/<id>.json` doc inside that run
folder.

To resume a killed scan:

```sh
fettle --dir examples/self-scan run find --resume .fettle/runs/<run-folder>/
```

The snapshotted prompt at `<run>/instructions/find.md` is what runs
on resume — editing the template here doesn't retroactively affect
a running scan.

## Browse and inspect

```sh
fettle --dir examples/self-scan list runs
fettle --dir examples/self-scan show run .fettle/runs/<run>/
fettle --dir examples/self-scan list findings --run .fettle/runs/<run>/
fettle --dir examples/self-scan show finding --run .fettle/runs/<run>/ <id>
```

Or open the workspace UI:

```sh
fettle --dir examples/self-scan ui
```

## Review the findings

Either with an agent (one invocation per finding):

```sh
fettle --dir examples/self-scan run review --run .fettle/runs/<run>/
fettle --dir examples/self-scan list reviews --run .fettle/runs/<run>/
fettle --dir examples/self-scan show review --run .fettle/runs/<run>/ --finding <id>
```

…or by clicking through the UI as a human reviewer. Both append to
the same `reviews[]` array inside each finding doc; both flow
through the same atomic-rename write.

## Mark outcomes as you ship fixes

```sh
fettle --dir examples/self-scan add outcome --run .fettle/runs/<run>/ --finding <id> --status merged --pr <url>
fettle --dir examples/self-scan show outcome --run .fettle/runs/<run>/ --finding <id>
fettle --dir examples/self-scan list outcomes --run .fettle/runs/<run>/
```

## Adapting this example

Copy this directory into your own project, edit `.fettle/config.json`
(set `target_repo`, `agent`, `include`, `exclude`), then rewrite each
`.fettle/instructions/*.md` for your domain. Everything else is
fettle plumbing.
