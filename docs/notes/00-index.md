# Sahayak: Learning Notes

These notes are written for someone who knows Python and is learning Go by
building this project. Every note explains:

1. What the Go concept actually is
2. How it differs from what you already know in Python
3. Where it shows up in *this* codebase, with real file references
4. The gotchas that will bite you

New notes are added as each phase introduces new concepts. Read them in order.

| Note | Covers | Phase |
| --- | --- | --- |
| [01. Project layout and the module system](01-project-layout.md) | packages, `internal/`, imports, `go.mod` | 1 |
| [02. Configuration and environment variables](02-configuration.md) | struct tags, error wrapping, sentinel errors, fail-fast | 1 |
| [03. Structured logging with `log/slog`](03-logging.md) | handlers, attributes, redaction, context values | 1 |
| [04. HTTP routing with chi](04-http-routing.md) | handlers, routing, path vs query params | 1 |
| [05. Middleware and graceful shutdown](05-middleware-and-shutdown.md) | wrapping, interceptors, signals, draining | 1 |
| [06. Writing tests in Go](06-testing.md) | table-driven tests, `httptest`, `t.Setenv`, coverage | 1 |

---

## The 30 second version

If you remember nothing else from Phase 1, remember these five:

1. **Errors are values, not exceptions.** Every function that can fail returns
   `error` as its last return value. You check it immediately.
2. **There is no inheritance.** You compose. Small structs that hold other
   structs, or plain functions.
3. **Interfaces are satisfied implicitly.** No `implements` keyword. If your
   type has the right methods, it already qualifies.
4. **`nil` is a real value that will surprise you.** Maps and slices read fine
   when empty, but writing to a nil map panics.
5. **Everything is a value.** Structs are copied on assignment. Passing a large
   struct to a function copies it. Use pointers when you mean mutation.

---

## How to read code in this repo

Every file here follows the same conventions, so once you learn them once they
apply everywhere:

| Convention | What it means |
| --- | --- |
| Package doc comment | The comment above `package x` explains what the package is for |
| Exported names are `CamelCase` | `Load`, `Config`. Unexported are `lowerCase` |
| `// Deps` structs | Dependencies passed explicitly, never globals |
| Constructors named `New*` | `config.Load()`, `logger.New()`, `httpapi.NewRouter()` |
| Context as first parameter | `func Foo(ctx context.Context, ...)` for anything doing I/O |
| Errors wrapped with `%w` | Preserves the original cause for `errors.Is` |
| Tests in `_test` package | `package config_test`, exercises only the public API |
| Table-driven tests | A slice of structs, one loop, many cases |
