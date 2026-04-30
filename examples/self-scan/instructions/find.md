# Find — Go conventions and code smells

You are analyzing **one Go file** for convention violations and concrete
code smells. Be specific; skip vague advice.

## Inputs (substituted by fettle)

- `TARGET_FILE` — absolute path to the .go file under analysis
- `REPO_ROOT` — absolute path to the repo root

## Method

1. Read `TARGET_FILE` fully.
2. For each candidate finding, decide whether it meets the bar in the
   "What to flag" section below. Skip noise.
3. Record each finding by running `fettle finding add` (see "Recording
   findings" below). Do not write anything to disk yourself; do not
   print findings to stdout. The CLI handles persistence.
4. When done, exit. fettle reads findings.jsonl directly.

## What to flag

Concrete, evidence-backed instances of:

- **Modern stdlib gaps.** `sort.Slice` where `slices.SortFunc` fits;
  manual loops where `slices.Contains`/`slices.Index` would do; manual
  `min`/`max`; `interface{}` instead of `any`; `for i := 0; i < n; i++`
  where `for i := range n` works (Go 1.22+).
- **Logging.** `fmt.Println` / `log.Printf` for runtime logging instead
  of `log/slog`. Skip CLI output prints (Println in main is fine).
- **Error handling.** Missing `%w` when wrapping; swallowed errors
  (`_ = foo()`, `if err != nil { return nil }` with no comment); naked
  `recover()`; `fmt.Errorf` building context as plain string instead of
  `%w`-wrapping.
- **Context.** `context.Context` stored on a struct; `ctx` not the first
  parameter; `context.Background()` used inside functions that have a
  `ctx` available.
- **Magic literals.** Repeated string/number literals that encode an
  invariant (timeouts, limits, statuses). Skip one-off test data and
  obvious HTTP statuses.
- **Stringly-typed state.** `if kind == "user"`-style control flow on
  raw strings; should be a typed enum or named constants.
- **Implicit input assumptions.** `path[5:]` "because it starts with
  /api/", indexing without a length check, `strings.Split(s, ":")[1]`.
- **Dead code paths / branch dilution.** A function that's a `switch
  kind` over N branches sharing little logic — usually two functions in
  disguise.
- **Boolean flag parameters.** `DoThing(x, true)` where the bool flips
  behavior — almost always two functions.

## What NOT to flag

- Stylistic preferences not on the list above.
- "Could add a doc comment" unless the symbol is exported and genuinely
  confusing.
- Performance micro-optimizations without evidence.
- Imagined future-proofing.
- Generated files (skip with zero findings).

## Recording findings

For each finding, run a single shell command:

```bash
fettle finding add \
  --file 'internal/foo/bar.go' \
  --line 42 \
  --title 'Replace sort.Strings with slices.Sort' \
  --description '...' \
  --suggestion '...' \
  --severity medium \
  --label 'category:convention' \
  --label 'confidence:high' \
  --reference 'internal/baz/qux.go:33'
```

**Required**: `--file`, `--line`, `--title`, `--description`,
`--suggestion`. Use repo-relative paths for `--file`.

**Optional**: `--severity` (`low` / `medium` / `high`), `--label` (any
number of `prefix:value` strings — repeatable), `--reference` (any
number of `PATH` or `PATH:LINE` strings — repeatable, used for grouping).

**Severity guide**:
- `high` — bug, swallowed error with concrete impact, unsafe assumption.
- `medium` — solid improvement; idiomatic gap with material reader cost.
- `low` — nice-to-have.

**Label convention**: always include at least one `category:<word>` tag
from `convention`, `error-handling`, `logging`, `magic-literal`,
`smell`, `context`. Add others freely.

**Shell quoting**:
- Wrap every string flag in single quotes so shell metacharacters and
  whitespace pass through unchanged.
- For literal single quotes inside the value, end the quote, escape the
  apostrophe, and re-open: `'doesn'\''t'` (which the shell sees as
  `doesn't`).
- Multi-line descriptions are fine — newlines inside single quotes are
  literal.

**Error handling**: if `fettle finding add` exits non-zero, read its
stderr message and try again with the corrections. Do not silently move
on. Exit codes:
- `0` — finding recorded.
- `1` — validation error (your fault: missing or malformed flag).
- `2` — internal error (likely fettle itself; surface and stop).

## Quality bar

- Be specific. "Could be cleaner" is not a finding.
- Prefer fewer, higher-quality findings. 0–5 per file is normal.
- Don't invent issues to fill quota.
- For trivial files (type-only, generated, tiny wrappers): emit zero
  findings (record nothing, just exit).

## When done

Stop. Don't summarize, don't explain, don't ask follow-ups.
