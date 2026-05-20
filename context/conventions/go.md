# Go Conventions

This project targets **Go 1.26**. Use modern idioms; never reach for a legacy pattern when a current one exists.

These conventions record decisions. They don't restate what `gofmt`, `go vet`, or general Go idioms already enforce.

## Function Preamble

Local context and observability go at the top of the function body, before any work:

```go
func (s *Server) Handle(ctx context.Context, req Req) (Resp, error) {
    ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
    defer cancel()
    logger := slog.Default().With("workspace", req.Workspace, "doc", req.DocID)

    // ... actual work
}
```

Order: `ctx` first, then bound subloggers / span starts, then the body. Keeps shared objects consistent with the inheritance the rest of the function expects.

## Modern Go (1.26) — Mandatory

Use these. Do not write the older equivalents.

### Built-ins and core types

- `any`, never `interface{}`.
- `min(a, b)`, `max(a, b, c)`, `clear(m)`, `clear(s)`.
- `new(val)` instead of `x := val; &x` for pointer-to-literal:
  ```go
  cfg := Config{Timeout: new(30), Debug: new(true)}
  ```

### strings / bytes

- `strings.Cut`, `strings.CutPrefix`, `strings.CutSuffix` instead of `Index` + slice.
- `strings.SplitSeq` / `strings.FieldsSeq` (and `bytes.SplitSeq` / `bytes.FieldsSeq`) when iterating — no intermediate slice.
- `strings.Clone` / `bytes.Clone` to detach from the underlying buffer.
- `fmt.Appendf(buf, ...)` instead of `append(buf, []byte(fmt.Sprintf(...))...)`.

### slices / maps / cmp

- `slices.Contains`, `slices.Index`, `slices.SortFunc`, `slices.Sort`, `slices.Clone`, `slices.Reverse`.
- `maps.Clone`, `maps.Copy`, `maps.DeleteFunc`.
- Iterators (1.23): `slices.Collect(maps.Keys(m))`, `slices.Sorted(maps.Keys(m))`, `for k := range maps.Keys(m)`.
- `cmp.Or(a, b, "default")` for first-non-zero defaulting; `cmp.Compare(a, b)` for ordering.

### Loops

- `for i := range n` instead of three-clause counters.
- Loop variables are per-iteration since 1.22 — capture freely in goroutines.

### Concurrency

- `wg.Go(fn)` instead of `wg.Add(1)` + `go func(){ defer wg.Done(); ... }()`.
- Type-safe atomics: `atomic.Bool`, `atomic.Int64`, `atomic.Pointer[T]`.
- `sync.OnceFunc`, `sync.OnceValue` for lazy init.

### Context

- `context.WithCancelCause(parent)` + `cancel(err)`; read with `context.Cause(ctx)`.
- `context.AfterFunc(ctx, cleanup)` for cancellation-triggered cleanup.
- Never store `context.Context` on a struct. Pass it as the first parameter.

### JSON tags

Use `omitzero`, not `omitempty`:

```go
type Doc struct {
    Created time.Time     `json:"created,omitzero"`
    TTL     time.Duration `json:"ttl,omitzero"`
    Tags    []string      `json:"tags,omitzero"`
}
```

`omitempty` does the wrong thing for `time.Time`, `time.Duration`, structs, and many slices/maps.

### Testing

- `ctx := t.Context()` — never `context.Background()` in tests, never a manual cancel pair.
- Benchmarks: `for b.Loop() { ... }`, not `for i := 0; i < b.N; i++`.
- Test fixtures live under `testdata/`. Use `t.TempDir()` for scratch dirs.

### HTTP routing

Use 1.22 method+path patterns:

```go
mux.HandleFunc("GET /api/v1/orgs/{org}/docs/{id}", handler)
id := r.PathValue("id")
```

## Errors

- Wrap with `%w` and add caller-side context: `fmt.Errorf("read manifest %s: %w", path, err)`. Don't restate what the wrapped error already says.
- `%w` only when callers should `errors.Is` / `errors.As` it; otherwise `%v` and let the chain end.
- Sentinels for caller-inspectable conditions:
  ```go
  var ErrNotFound = errors.New("not found")
  ```
- Type extraction (1.26): `if pe, ok := errors.AsType[*os.PathError](err); ok { ... }`.
- Combine with `errors.Join` rather than building strings.
- Don't return `*MyError`. Return `error`. (Concrete pointer return types create silent nil-interface bugs.)

## Logging

- `log/slog` only. Never the standard `log` package, never `fmt.Println` for logs.
- Structured key/value pairs, no string interpolation:
  ```go
  slog.Info("push validated", "workspace", ws, "commits", n)
  ```
- Bind request/operation-scoped fields once at the top of the function via `logger := slog.Default().With(...)`.
- Errors go in an `"error"` key: `slog.Error("validate failed", "error", err)`.
- Levels: `Debug` diagnostics, `Info` milestones, `Warn` recoverable issues, `Error` failures that abort the operation.

## API & Type Design

- **Accept interfaces, return concrete types.** Define interfaces on the consumer side, small (1–3 methods).
- Don't prefix getters with `Get`: `doc.Title()`, not `doc.GetTitle()`.
- Don't repeat the package name in exported names: `run.Path`, not `run.RunPath`. `walk.Walk`, not `walk.WalkFiles` (unless it disambiguates).
- Constructors: `New` when the package exports one primary type; `NewX` when several.
- Prefer zero-value-usable structs (`var b Buffer`) over mandatory constructors.
- Field names required in struct literals across package boundaries and in table tests.

## Receivers

- Pointer receiver if the method mutates, the struct contains a `sync.Mutex`/`atomic.*`, or the struct is large. Otherwise value.
- Be consistent within a type — don't mix pointer and value receivers on the same type.
- Receiver name: 1–2 letters, abbreviation of the type (`func (p *Path)`, `func (s *Spec)`). Same name across all methods of that type.

## Packages

- Lowercase, single word, no underscores. No `util`, `helpers`, `common`.
- Test doubles live in a sibling `xxxtest` package when needed.
- Internal-only code under `internal/`. Don't add a top-level `pkg/`.
- Group imports: stdlib, third-party, this module — separated by blank lines. `goimports` handles it.

## Concurrency

- Default to channels for handoff between goroutines; mutex when guarding shared state in place.
- Goroutine lifetime must be obvious at the spawn site — bound by `ctx`, a `WaitGroup`, or a channel close. No fire-and-forget.
- Never call `t.Fatal` from a spawned goroutine in tests; use `t.Error` and return.

## Comments

- Document the **why**, not the **what**. Names and types describe what.
- Every exported identifier needs a doc comment, starting with the identifier name. Skip comments on obvious accessors only if the name fully explains them.
- Don't reference past behavior, tickets, or callers ("used by X", "added for Y", "previously did Z"). That belongs in the commit message and rots in the source tree.
- Package comment immediately above `package`, no blank line, on one file per package (typically `doc.go` or the primary file).
