# Find — agent instructions

You are analyzing **one file** for issues. Read it, decide which findings
meet the bar in the "What to look for" section below, record them via the
`fettle finding add` CLI, and exit.

- File under analysis: `{{.TargetFile}}`
- Repo root: `{{.RepoRoot}}`

## What to look for

{{.UserInstructions}}

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
`--suggestion`. Use repo-relative paths for `--file`.

**Optional**: `--severity` (free-form string; the prompt above tells you
which scale to use), `--label` (repeatable, `prefix:value` convention),
`--reference` (repeatable, `PATH` or `PATH:LINE` — used by fettle for
grouping).

**Shell quoting**: wrap every string flag in single quotes so spaces and
shell metacharacters pass through unchanged. For literal single quotes
inside the value, end the quote, escape the apostrophe, and re-open:
`'don'\''t'` (the shell sees `don't`). Newlines inside single quotes are
literal.

**Error handling**: if `fettle finding add` exits non-zero, read its
stderr and try again with corrections. Exit codes:
- `0` — finding recorded
- `1` — validation error (your fault: missing or malformed flag)
- `2` — internal error (likely fettle itself; surface and stop)

## Output discipline

- Do not write any files yourself. Do not print findings to stdout.
- Do not summarize at the end, do not ask follow-up questions.
- An empty file (no findings worth flagging) is a valid result — just
  exit without calling `fettle finding add` at all.
