# Find — Go conventions and code smells

You are analyzing **one Go file** for convention violations and concrete
code smells. Be specific; skip vague advice.

## Inputs (substituted by fettle)

- `TARGET_FILE` — absolute path to the .go file under analysis
- `OUTPUT_PATH` — write findings here, one JSON object per line (may be empty)
- `REPO_ROOT` — absolute path to the repo root

## Method

1. Read `TARGET_FILE` fully.
2. For each candidate finding, decide whether it meets the bar in the
   "What to flag" section below. Skip noise.
3. Write findings as JSONL to `OUTPUT_PATH`. Empty file is a valid
   "nothing to report."

## What to flag

Flag concrete, evidence-backed instances of:

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

## Output schema (one JSON object per line)

```json
{
  "file": "<repo-relative path>",
  "line": 42,
  "title": "<short imperative title, no trailing period, ≤80 chars>",
  "description": "<2-5 sentences: what's wrong and where>",
  "suggestion": "<concrete fix in 1-3 sentences>",
  "severity": "low" | "medium" | "high",
  "labels": ["category:<one-word category>"],
  "references": [
    {"file": "internal/foo/bar.go", "line": 12},
    {"file": "internal/baz/qux.go"}
  ]
}
```

Use `severity`:
- `high` — bug, swallowed error with concrete impact, unsafe assumption
- `medium` — solid improvement; idiomatic gap with material reader cost
- `low` — nice-to-have

For `labels`, always include at least one `category:<word>` tag from
this set: `convention`, `error-handling`, `logging`, `magic-literal`,
`smell`, `context`. Add others as you see fit (`severity:high`,
`confidence:high`, etc.).

For `references`, list any *other* code locations the issue points at —
e.g. duplicate sites of the same pattern, related callers. Each entry
**must be a JSON object** with a `file` field (repo-relative path) and
an optional `line` field (integer). **Never emit references as plain
strings** like `"path:line"` — strings will fail to parse. Empty array
`[]` is fine if there are no other locations. fettle uses these for
grouping; overlap across findings clusters them together.

## Quality bar

- Be specific. "Could be cleaner" is not a finding.
- Prefer fewer, higher-quality findings. 0–5 per file is normal.
- Don't invent issues to fill quota.
- For trivial files (type-only, generated, tiny wrappers): emit zero
  findings.

## When done

Stop. Don't summarize, don't explain, don't ask follow-ups. fettle
reads `OUTPUT_PATH` directly.
