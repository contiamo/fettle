<!--
  fettle wraps this file in a frame that handles the agent contract
  (variable values, `fettle finding add` recording protocol, exit-code
  handling, output discipline). Keep this file scoped to the analysis
  criteria themselves.
-->

You are looking for Go convention violations and concrete code smells.
Be specific; skip vague advice. 0–5 findings per file is normal; on
trivial files (type-only, generated, tiny wrappers) record nothing.

## What to flag

Concrete, evidence-backed instances of:

- **Modern stdlib gaps.** `sort.Slice` where `slices.SortFunc` fits;
  manual loops where `slices.Contains` / `slices.Index` would do; manual
  `min` / `max`; `interface{}` instead of `any`; `for i := 0; i < n; i++`
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
- **Stringly-typed state.** `if kind == "user"` style control flow on
  raw strings; should be a typed enum or named constants.
- **Implicit input assumptions.** `path[5:]` "because it starts with
  /api/", indexing without length checks, `strings.Split(s, ":")[1]`.
- **Boolean flag parameters.** `DoThing(x, true)` where the bool flips
  behavior — almost always two functions in disguise.

## Severity scale

- `high` — bug, swallowed error with concrete impact, unsafe assumption.
- `medium` — solid improvement; idiomatic gap with material reader cost.
- `low` — nice-to-have.

## Label conventions

Always include one `category:<word>` from this set: `convention`,
`error-handling`, `logging`, `magic-literal`, `smell`, `context`. Add
others freely (e.g. `confidence:high`).

## What NOT to flag

- Stylistic preferences not on the list above.
- "Could add a doc comment" unless the symbol is exported and genuinely
  confusing.
- Performance micro-optimizations without evidence.
- Imagined future-proofing.
