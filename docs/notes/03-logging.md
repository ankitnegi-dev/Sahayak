# 03. Structured Logging with `log/slog`

Covers `internal/logger/logger.go`: handlers, attributes, automatic redaction,
and context-carried values.

---

## Why not fmt.Println

Structured logging means log output is machine readable. Instead of a sentence
you have to parse, each event is a set of key-value pairs:

```json
{"time":"2026-01-15T10:32:11Z","level":"INFO","msg":"http_request","method":"POST","route":"/webhook","status":200,"duration_ms":12}
```

That single line can be filtered, grouped, and charted. `grep` cannot do that
with prose.

You should use this from day one because retrofitting structured logging means
touching every call site. Every log statement is a decision about which fields
you will want to query later, and those decisions are much cheaper to make while
you are still writing the feature.

---

## `log/slog` is in the standard library

This was added in **Go 1.21**. You do not need logrus, zap, or zerolog. Go's
answer to structured logging is built in, which is one of the strongest
reasons the roadmap chose Go in the first place.

**Python equivalent:** the standard library has `logging`, but it is
freeform and awkward for structure. Most Python projects reach for `structlog`
or a JSON formatter. In Go the structured version is the default.

---

## Handlers

A handler decides where log records go and how they are rendered.

```go
h := slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
	Level:       level,
	ReplaceAttr: redact,
})

log := slog.New(h)
```

Two decisions here:

1. **JSON over text.** For production. A log aggregator parses JSON far more
   reliably than it parses a human-readable format.
2. **`ReplaceAttr: redact`.** A hook that lets us rewrite attributes on the way
   out. This is where redaction happens, and it is the subject of the next
   section.

For local development a `slog.NewTextHandler` would be friendlier to read. We
use JSON everywhere for consistency, since mixing formats makes log searching
painful.

---

## Log levels

```go
switch {
case opts.Level != "":
	level = parseLevel(opts.Level)
case opts.Environment == "development":
	level = slog.LevelDebug
default:
	level = slog.LevelInfo
}
```

Levels are `Debug`, `Info`, `Warn`, `Error`. Anything below the configured
level is dropped before it reaches the writer.

The level is **not** a severity you choose freely at the call site. Setting a
global threshold to `Info` means every `log.Debug(...)` in the codebase becomes
a no-op, and you cannot see at the call site that it is disabled. That is the
intended behaviour: `Debug` means "useful during development."

Our `logRequests` middleware picks the level from the HTTP status:

```go
switch {
case ww.Status() >= 500:
	level = slog.LevelError
case ww.Status() >= 400:
	level = slog.LevelWarn
}
```

A 500 means we broke. A 404 might just be someone probing, so `Warn` rather
than `Error`.

---

## Attributes

Two ways to pass key-value pairs:

```go
log.Info("api starting",
	"env", cfg.Env,
	"addr", cfg.HTTP.Addr,
	"version", version,
)
```

```go
log.LogAttrs(ctx, level, "http_request",
	slog.String("method", r.Method),
	slog.Int("status", ww.Status()),
)
```

The first is simpler. The second is faster because it skips reflection, and it
is the one to use in hot paths like per-request logging. `LogAttrs` also takes
a context, which is what allows handlers to pick up context-bound values.

The message itself should be a **stable identifier**, not a sentence. Write
`"http_request"`, not `"request completed successfully"`. You will grep for
`msg=http_request` thousands of times, and you cannot grep for prose.

---

## Redaction: the important part

Parents share phone numbers, addresses and voice messages. Logging any of that
in plain text would be a real privacy problem. See
[ADR-006](../../docs/decisions.md).

We redact in **two** places, on purpose.

### Layer 1: the handler, as a safety net

```go
var sensitiveKeys = map[string]bool{
	"token":         true,
	"access_token":  true,
	"app_secret":    true,
	"api_key":       true,
	"password":      true,
	"authorization": true,
	"body":          true,
	"raw_body":      true,
}

func redact(groups []string, a slog.Attr) slog.Attr {
	if sensitiveKeys[strings.ToLower(a.Key)] {
		return slog.String(a.Key, "[REDACTED]")
	}
	return a
}
```

`ReplaceAttr` is called for every attribute on the way out. Any attribute whose
key matches gets its value replaced, regardless of what the call site passed.

The reasoning: call-site discipline is one refactor away from a leak. Someone
adds `log.Debug("payload", "body", string(rawBody))` at 11pm while debugging an
ASR issue, and now every parent's voice note text is in your log aggregator. The
handler-level guard means that mistake fails safe.

Note `strings.ToLower(a.Key)`: key matching is case-insensitive, because
`API_KEY`, `api_key` and `Api_Key` should all be treated the same.

### Layer 2: the call site, for user data

Phone numbers are personal data, but they do not appear under a sensitive key,
so the handler cannot catch them. They need explicit masking:

```go
log.Info("incoming", "from", logger.RedactPhone("+919812345678"))
```

which produces `+9198******78`: first 5 characters, last 2, stars between.

The rule is simple and worth remembering: **if a value is a secret, redact it
by key. If a value is personal data, redact it at the call site.**

---

## Writing to a custom destination

```go
func NewWithWriter(opts Options, w io.Writer) *slog.Logger {
	// ...
	h := slog.NewJSONHandler(w, &slog.HandlerOptions{...})
	return slog.New(h)
}

func New(opts Options) *slog.Logger {
	return NewWithWriter(opts, os.Stdout)
}
```

This exists purely so tests can capture output:

```go
var buf bytes.Buffer
log := logger.NewWithWriter(logger.Options{Environment: "development"}, &buf)
log.Info("test", "token", "supersecret")

// assert that "supersecret" is absent from buf.String()
```

`os.Stdout` is hard-coded by default, which makes logging untestable. Any
package that writes output needs this seam. It is a small design concession
that pays for itself the first time you write a test against it.

---

## Context values

```go
type ctxKey struct{}

func WithRequestID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, ctxKey{}, id)
}

func RequestID(ctx context.Context) string {
	if v, ok := ctx.Value(ctxKey{}).(string); ok {
		return v
	}
	return ""
}
```

This stores a request ID so every log line emitted while handling one WhatsApp
message can be correlated.

### The unexported key type matters

The key is `ctxKey{}`, a private struct, not a plain string like
`"requestID"`.

**Why:** package-level string keys can collide. If two packages both use
`ctx.WithValue(ctx, "userID", ...)`, they overwrite each other. Using an
unexported type means the key can only be created inside this package, so
collisions are impossible and `go vet` can check for mistakes.

**Python equivalent:** `contextvars.ContextVar` or `contextvars.Context`, which
solve the same problem. The Go version is not thread-safe across goroutines,
which matters more here because the worker is concurrent.

### The cost, honestly

Context values are convenient and slightly dangerous. The honest guidance is
that `context.Context` is for request-scoped metadata crossing API boundaries:
request IDs, cancellation, deadlines. It is not a general purpose bag to pass
function arguments around.

For anything that is genuine function input, use a parameter. In the middleware
we get the request ID from context only because log calls deep in the call stack
need it and threading it through every signature would be noise.

---

## Setting the default logger

```go
log := logger.New(logger.Options{Environment: cfg.Env, Level: os.Getenv("LOG_LEVEL")})
slog.SetDefault(log)
```

`slog.SetDefault` installs the logger that all the package-level convenience
functions (`slog.Info`, `slog.Error`) use. We call it once in `main` so that
any package can log without threading a logger through every signature.

That said, we still **pass** `*slog.Logger` explicitly where we can, via the
`Deps` struct:

```go
type Deps struct {
	Log *slog.Logger
	Health func() error
}
```

Global state is hard to test. Explicit is better. `slog.SetDefault` is the
escape hatch for code paths where threading a logger is genuinely awkward.

---

## Next

[04. HTTP routing with chi](04-http-routing.md)
