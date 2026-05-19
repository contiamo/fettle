<!--
  This file is the "what to look for" half of fettle's find-stage
  prompt. fettle wraps it in a frozen frame that handles variable
  values, the agent contract, and exit codes — you describe the
  analysis.

  Every section below is a sketch with placeholder content. Replace
  the placeholders with your domain. Sections marked OPTIONAL can be
  removed if they don't apply.
-->

You are analyzing **one file** for issues. Your job is to read the
file, decide whether each candidate finding meets the bar described
here, and record each one via `fettle add finding`. After
recording, stop.

## Required reading (OPTIONAL — point at a conventions doc here)

Most projects have written conventions somewhere. Reference them
here and tell the agent to read them first; the agent then judges
the file against rules *you* wrote, not against generic notions of
"good code."

1. `REPO_ROOT/conventions/<language>.md` — the codebase's coding
   conventions.
2. The patterns section below — what to look for, what to skip.
3. `TARGET_FILE` — the file under analysis.

## Method

A short, numbered method prevents the agent from wandering. Example:

1. Read your conventions doc and the patterns section below.
2. Read `TARGET_FILE` fully.
3. For each candidate finding, decide if it meets the bar in the
   patterns section. Skip noise.
4. **For duplication candidates:** actively search the repo with
   the tools you have (`Grep`/`Glob`/`rg`) for similar code in
   other files. Fettle's value over single-file linters is
   precisely cross-file duplication — make the agent earn it.
5. Record each finding via one `fettle add finding` call.

## Patterns to flag

Sketch your categories here. Each one becomes a
`category:<bucket>` label so findings can be filtered by topic in
the UI. Pick categories before writing the prompt and stick to
them — the agent should NOT invent new ones mid-run.

Example shape (replace with your own):

### Category A — `category:convention`

Convention violations against the rules in your conventions doc.
Be specific: "legacy idiom X used where modern idiom Y is required"
beats "doesn't follow conventions."

### Category B — `category:duplication`

Logic, structure, or error-wrap patterns repeated across files.
For every duplication finding, list every other site via
`--reference path` or `--reference path:line` — prefer one finding
naming N sites over N near-duplicate findings.

### Category C — `category:smell`

The catch-all for concrete cleanup paths that don't fit elsewhere.
Resist using this for things the project doesn't care about; if
the best suggestion is "document this", usually skip.

## What NOT to flag

The "skip" list is as important as the "flag" list. It prevents
churn. Examples:

- Stylistic preferences not in your conventions doc.
- Comments that just need rephrasing.
- "Could add more documentation" — only flag genuinely confusing
  public APIs with no doc comment.
- Performance micro-optimizations without evidence.
- Imagined future-proofing.
- Generated files, vendored code, build artifacts.

If a file is generated, trivial, or has no real logic: output zero
findings — that's the correct answer.

## Severity scale

Name what each level means in your project. Example:

- `high` — clear bug, swallowed error with concrete impact, unsafe
  assumption.
- `medium` — solid improvement worth doing; convention violation
  that would surface in code review.
- `low` — nice-to-have; style or doc-comment shape.

Be conservative. When in doubt, drop a level.

## Quality bar

- Be specific. "Could be cleaner" is not a finding. Cite line
  numbers, name destinations for refactors, show the fix shape.
- Prefer fewer, higher-quality findings. 0–5 per file is normal;
  more than 10 means you're including noise.
- Do not invent issues to fill a quota.

## Bugs and security (OPTIONAL drive-by exception)

If this isn't a security-focused audit but you want incidental
catches surfaced: tell the agent that anything clearly wrong or
unsafe seen *while doing the main pass* should be flagged with
`category:smell` and lead the description with `**Bug:**`,
`**Security:**`, or `**Perf:**`. Don't go hunting — flag only what
you trip over.

## Recording protocol

For each finding, run one shell command:

```bash
fettle add finding \
  --file 'TARGET_FILE_RELATIVE_PATH' \
  --line 42 \
  --title 'Short imperative title, ≤80 chars, no trailing period' \
  --description 'Two to five sentences. State the issue, where, the impact.' \
  --suggestion 'Concrete change. Name the target function/file/package.' \
  --severity medium \
  --label 'category:convention' \
  --reference 'internal/other/file.go:88'
```

Required flags: `--file` (repo-relative), `--line`, `--title`,
`--description`, `--suggestion`, `--severity`, and at least one
`--label category:<bucket>`. Optional: more `--label` flags
(e.g. `confidence:high`, `confidence:low`); `--reference
PATH[:LINE]` (repeatable; required for duplication findings).

**Shell quoting:** wrap every string flag in single quotes. For
literal single quotes inside, end the quote, escape the
apostrophe, re-open: `'don'\''t'`.

## When done

Stop. Don't summarise, don't explain. The harness reads what you
recorded.
