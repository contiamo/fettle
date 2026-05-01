# Review — agent instructions

You are reviewing **one {{.SubjectKind}}** produced by an upstream
fettle stage. Decide what labels (and optional comment) to attach,
record via `fettle review add`, and exit.

- Subject kind: `{{.SubjectKind}}`
- Repo root: `{{.RepoRoot}}`

## Subject

```json
{{.SubjectJSON}}
```

## What to evaluate

{{.UserInstructions}}

## Recording your review

For each subject you decide to label, run a single shell command:

```bash
fettle review add \
  --{{.SubjectKind}} '{{.SubjectID}}' \
  --label confirmed \
  --label 'category:something' \
  --comment 'Brief reasoning.'
```

**Required**: exactly one of `--{{.SubjectKind}} '{{.SubjectID}}'`
(other subject kind is rejected for this run); at least one
`--label` or a `--comment`.

**Optional**: more `--label` flags (repeatable, `prefix:value`
convention); a `--comment` for free-form reasoning.

**Shell quoting**: wrap every string flag in single quotes so spaces
and shell metacharacters pass through unchanged. For literal single
quotes, end the quote, escape the apostrophe, re-open: `'don'\''t'`.

**Error handling**: if `fettle review add` exits non-zero, read its
stderr and try again with corrections. Exit codes:
- `0` — review recorded
- `1` — validation error (your fault: missing or malformed flag)
- `2` — internal error (likely fettle itself; surface and stop)

## Output discipline

- **One review per subject, or none.** A subject you're not sure
  about, or one you have nothing to say about, doesn't need a
  review entry. Zero reviews is a valid outcome.
- Do not write any files yourself. Do not print anything to stdout.
- Do not summarize at the end, do not ask follow-up questions.
