<!--
  fettle wraps this file in a frame that handles the agent contract
  (variable values, `fettle finding add` recording protocol, exit-code
  handling, output discipline). Keep this file scoped to the analysis
  criteria themselves.
-->

You are looking for **violations of the project's Go conventions** plus
concrete code smells. Be specific; skip vague advice. 0–5 findings per
file is normal; on trivial files (type-only, generated, tiny wrappers)
record nothing.

## Read first

Read the project's Go conventions at `conventions/go.md` (relative to
the repo root). Those conventions are mandatory — anything that
contradicts them is a finding.

## Also flag (concrete smells, file-local)

Beyond convention violations, flag:

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
- `medium` — solid improvement; convention violation with material
  reader cost.
- `low` — nice-to-have.

## Label conventions

Always include one `category:<word>` from this set:

- `convention` — violates `conventions/go.md`
- `error-handling` — wrap/swallow/sentinel issues
- `logging` — slog usage, log levels, structured fields
- `magic-literal` — unnamed strings/numbers
- `smell` — anything else (boolean flags, stringly-typed state, etc.)
- `context` — ctx-on-struct, missing ctx, wrong ctx ordering

Add others freely (e.g. `confidence:high`).

## What NOT to flag

- Stylistic preferences not in `conventions/go.md` and not on the smells
  list above.
- "Could add a doc comment" unless the symbol is exported and genuinely
  confusing.
- Performance micro-optimizations without evidence.
- Imagined future-proofing.
