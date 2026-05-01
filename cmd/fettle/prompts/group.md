# Group — agent instructions

You are clustering findings from a single upstream run into PR-sized
batches. Each cluster you emit becomes one group; downstream the
groups drive the unit of review/PR/landing.

- Repo root: `{{.RepoRoot}}`
- Input run: `{{.InputRun}}` ({{.FindingCount}} findings)

## Input

`FINDINGS_JSON` below is the full set of findings from the input run.
Each entry is the verbatim finding record (id, file, line, title,
description, suggestion, severity, labels, references).

```json
{{.FindingsJSON}}
```

`REVIEWS_JSON` is the merged review state per finding id, or `{}`
if no reviews exist for this run. Each entry has a derived
`current_labels` (latest entry per author, unioned) and the
chronological per-author `entries[]`. **Skip findings whose
`current_labels` include `false-positive` / `out-of-scope` /
`needs-human`** — reviewers have already disposed of them. Cluster
only the live findings.

```json
{{.ReviewsJSON}}
```

## What to do

1. **Filter out review-rejected findings** (see above) — they don't
   belong in any group.

2. **Cluster the remaining findings.** Group findings that should
   reasonably ship in the same PR. Use whatever signal makes sense
   for your domain — typically file/directory locality, shared root
   cause, label overlap, or `references[]` overlap (multiple findings
   touching the same site).

3. **Keep groups PR-sized.** A group should be small enough that one
   reviewer can land it as a single PR — typically a handful of
   findings, occasionally up to a couple of dozen for a homogeneous
   sweep. If a candidate cluster grows past that, split it on a
   secondary signal (file, sub-category, severity).

4. **Every live finding belongs to exactly one group.** Singletons
   are valid — emit a group of size 1 for findings that don't
   cluster with anything else. Never duplicate a finding across
   groups.

## Recording

For each cluster, run a single shell command:

```bash
fettle add group \
  --title 'consensus title for this cluster' \
  --summary 'one-paragraph summary of what these findings have in common and why they should land together' \
  --finding 'abc123' \
  --finding 'def456' \
  --label 'category:duplication' \
  --label 'effort:medium'
```

**Required**: `--title`, `--summary`, and at least one `--finding ID`.
Each `--finding` id must exist in the input run's findings.jsonl;
the harness rejects unknown ids.

**Optional**: more `--finding` flags (repeatable); `--label` flags
(repeatable, `prefix:value` convention).

**Same shell-quoting rules** as `fettle add finding`: single quotes
around string flags; escape inner apostrophes as `'\''`; newlines
inside single quotes are literal.

**Error handling**: if `fettle add group` exits non-zero, read its
stderr and try again. Exit codes 0/1/2 same as elsewhere.

## Output discipline

- Don't fabricate findings — only cluster ids that appear in
  `FINDINGS_JSON`.
- Don't include review-rejected findings in any group.
- Zero groups is valid (every finding was rejected).
- Don't print anything to stdout. Don't summarize.
