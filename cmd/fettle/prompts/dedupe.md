# Dedupe — agent instructions

You are consolidating findings from **multiple find runs** into
canonical findings. Two or more runs scanned the same code, and you
need to identify which findings refer to the same underlying issue
and merge them, while keeping run-unique findings as their own
canonical records.

- Repo root: `{{.RepoRoot}}`
- Input runs ({{.InputRunCount}}): {{.InputRunsList}}

## Input

`FINDINGS_JSON` below contains every finding from every input run.
Each entry includes the original `id`, the `from_run` it came from,
and the **current review state** for that finding (labels and the
latest comment per author from that run's `reviews_<author>.jsonl`).

```json
{{.FindingsJSON}}
```

## What to do

1. **Skip findings already rejected by reviewers.** If a finding has
   labels like `false-positive`, `out-of-scope`, or `needs-human`
   from any reviewer, treat it as rejected: don't include it in any
   canonical finding. The agent's job is to consolidate the *valid*
   findings, not to relitigate review decisions.

2. **Cluster equivalent findings.** Two findings refer to the same
   underlying issue when they describe the same root cause at the
   same site, even if line numbers differ slightly or wording
   varies. Use `file`, `line`, `title`, `description`, and
   `references[]` to judge.

3. **Emit one canonical finding per cluster.** The canonical
   finding's `--canonical-of` flags list every source finding it
   subsumes (one per cluster member, format `RUN:FINDING_ID`).
   Singletons (run-unique findings) are still canonical findings —
   they just have a single `--canonical-of` entry.

4. **Synthesize the canonical content.** Write a `--title`,
   `--description`, and `--suggestion` that capture the consensus
   view. Pick the strongest `--severity`. Union `--label` and
   `--reference` across cluster members. Don't blindly copy from
   one source — blend.

## Recording

For each canonical finding, run a single shell command:

```bash
fettle find add \
  --canonical-of 'runs/find_a:abc123' \
  --canonical-of 'runs/find_b:def456' \
  --file 'internal/foo/bar.go' \
  --line 42 \
  --title 'consensus title' \
  --description '...' \
  --suggestion '...' \
  --severity high \
  --label 'category:something' \
  --label 'consensus:both'
```

**Required**: at least one `--canonical-of RUN:FINDING_ID`. The
harness validates that RUN appears in this dedupe run's
`input_runs[]` and that FINDING_ID exists in that run's
findings.jsonl. Plus the usual `--file`, `--line`, `--title`,
`--description`, `--suggestion`.

**Same shell-quoting rules** as `fettle find add` from a find run:
single quotes around string flags; escape inner apostrophes as
`'\''`; newlines inside single quotes are literal.

**Error handling**: if `fettle find add` exits non-zero, read its
stderr and try again. Exit codes 0/1/2 same as elsewhere.

## Output discipline

- Don't create canonical findings for review-rejected sources.
- Don't fabricate findings that weren't in any input run.
- Zero canonical findings is valid (every input was rejected).
- Don't print anything to stdout. Don't summarize.
