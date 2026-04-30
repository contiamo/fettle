# Find — instructions for the agent

You are analyzing **one file** for issues. Replace this stub with what
you actually want fettle to find — convention violations, security
smells, missing docs, license-header gaps, anything.

## Inputs (substituted by fettle)

- `TARGET_FILE` — absolute path to the file under analysis
- `REPO_ROOT` — absolute path to the target repo root

## Method

1. Read `TARGET_FILE` fully.
2. For each candidate finding, decide whether it meets your bar.
   Skip noise.
3. Record each finding via `fettle finding add` (see below). Do not
   write anything to disk yourself; do not print findings to stdout.
4. When done, exit.

Replace the rest of this section with your domain — the patterns you
want flagged, what's out of scope, severity guidance, etc.

## Recording findings

For each finding, run a single shell command:

```bash
fettle finding add \
  --file 'path/relative/to/repo.go' \
  --line 42 \
  --title 'short imperative title' \
  --description '2-5 sentences describing the issue' \
  --suggestion '1-3 sentences with a concrete fix' \
  --severity medium \
  --label 'category:something' \
  --reference 'other/file.go:12'
```

**Required**: `--file`, `--line`, `--title`, `--description`,
`--suggestion`.

**Optional**: `--severity` (free-form string; you choose the scale),
`--label` (repeatable, prefix:value convention), `--reference`
(repeatable, `PATH` or `PATH:LINE` — used for grouping).

**Shell quoting**: wrap every string flag in single quotes so spaces
and shell metacharacters pass through unchanged. For literal single
quotes inside the value, end the quote, escape the apostrophe, and
re-open: `'don'\''t'` (the shell sees `don't`). Newlines inside single
quotes are literal.

**Error handling**: if `fettle finding add` exits non-zero, read the
stderr message and try again with corrections. Exit codes:
- `0` — finding recorded
- `1` — validation error (your fault: missing or malformed flag)
- `2` — internal error (likely fettle itself; surface and stop)

## When done

Stop. Don't summarize, don't print findings to stdout, don't ask
follow-ups. fettle reads findings.jsonl directly.
