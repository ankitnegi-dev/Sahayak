# 05. Middleware and Graceful Shutdown

Covers `internal/httpapi/middleware.go` and `cmd/api/main.go`: the middleware
chain, intercepting responses, signals, and draining connections.

---

## What middleware is

Middleware is a function that wraps a handler:

```go
func requestID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// before the handler runs
		next.ServeHTTP(w, r)
		// after the handler returns
	})
}
```

You pass a handler in, get a handler out. That is the entire concept, and it is
the reason middleware composes: `a(b(c(handler)))` works because each one takes
and returns the same type.

**Python equivalent:** Flask `@app.before_request` / `@app.after_request`, or
Django middleware classes. The Go version is a plain function, not a class with
lifecycle methods.

---

## The chain in our router

```go
r.Use(middleware.RealIP)
r.Use(middleware.Recoverer)
r.Use(requestID)
r.Use(logRequests(deps.Log))
r.Use(middleware.Timeout(30 * time.Second))
```

**Order matters, and it runs outside-in.** A request passes through `RealIP`
first, then `Recoverer`, then `requestID`, then `logRequests`, then `Timeout`,
then the handler. The response unwinds in reverse.

The important consequence: `logRequests` is registered **after** `requestID`,
so by the time it runs, the request ID already exists in the context and can be
included in the log line. Swap those two and every log line reads
`request_id=""`. This is the kind of thing that looks fine until you need it
at 2am.

`Recoverer` sits near the top so a panic anywhere below is caught.

---

## Interception requires a wrapper

`http.ResponseWriter` is an interface with only three methods: `Header`,
`Write`, `WriteHeader`. There is no way to know the status code or byte count
after the fact, unless you wrap it.

```go
ww := middleware.NewWrapResponseWriter(w, r.ProtoMajor)
next.ServeHTTP(ww, r)

log.Info("http_request",
	"status", ww.Status(),
	"bytes", ww.BytesWritten(),
)
```

`NewWrapResponseWriter` records the status and byte count as they pass through,
then delegates to the real writer. Note `r.ProtoMajor` is passed along so the
wrapper knows whether to implement optional interfaces like `http.Flusher`.

**This is the general pattern for intercepting an interface you do not own:**
write a type that embeds the interface, forwards every method, and adds your
logic. You will do this again in Phase 2 to capture the raw request body for
signature verification.

A subtlety worth knowing: if a handler type-asserts the writer to something like
`http.Flusher`, a wrapper that does not implement it will fail that assertion.
That is why the wrapper takes `ProtoMajor`.

---

## Route patterns versus raw paths

```go
route := ""
if rctx := chi.RouteContext(r.Context()); rctx != nil {
	route = rctx.RoutePattern()
}
if route == "" {
	route = r.URL.Path
}
```

This was a real bug during setup. We originally wrote:

```go
route := middleware.GetRoutePattern(r.Context())
```

That function does not exist in chi v5.5.0. The working API is
`chi.RouteContext(ctx)` returning a `*chi.Context`, which has `.RoutePattern()`.

The **why** matters more than the fix. We log the route pattern
(`/webhook`) rather than the raw path (`/webhook/wamid.ABC123`) so that metric
cardinality stays bounded. Without this, every WhatsApp message ID would create
a distinct time series and the metrics backend would fall over.

The nil check is needed because a 404 never matches a route, so there is no
route context.

---

## Generating unique request IDs

```go
func newRequestID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return hex.EncodeToString([]byte(time.Now().Format("20060102150405.000000")))
	}
	return hex.EncodeToString(b[:])
}
```

16 bytes from `crypto/rand`, hex encoded, giving 32 characters.

**Why `crypto/rand` and not `math/rand`:** `math/rand` is deterministic and
guessable. An attacker who can predict request IDs can correlate their requests
with a victim's. This is not academic for an app handling elderly users and
booking payments.

The fallback branch is unreachable on any supported platform, but `crypto/rand`
returns an `error`, and ignoring it silently is bad practice. The fallback is a
timestamp, which is worse but still functional. `TestRequestID_UniquePerRequest`
generates 50 and asserts no collisions.

### Honouring an incoming ID

```go
id := r.Header.Get("X-Request-ID")
if id == "" {
	id = newRequestID()
}
w.Header().Set("X-Request-ID", id)
```

If an upstream proxy or load balancer already assigned an ID, we reuse it so
traces span the whole system. Otherwise we mint one. We echo it back so
support can correlate a user's screenshot with our logs.

---

## Graceful shutdown

This is the part that matters most in production, and it is where
`context.Context` earns its keep.

```go
ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
defer stop()
```

`NotifyContext` returns a context that is **cancelled** when the process
receives SIGINT (Ctrl+C) or SIGTERM (what Docker and Kubernetes send on
deploy). Everything downstream can watch that one context and unwind.

```go
serveErr := make(chan error, 1)
go func() {
	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		serveErr <- err
		return
	}
	serveErr <- nil
}()

select {
case err := <-serveErr:
	if err != nil {
		return fmt.Errorf("listen and serve: %w", err)
	}
	return nil
case <-ctx.Done():
	log.Info("shutdown signal received")
}
```

**The shape to recognise:** start the blocking call in a goroutine, communicate
the result over a channel, then `select` between "the server failed" and "we
were told to stop". `ListenenAndServe` blocks forever, so this is how you make
it interruptible.

The channel is **buffered** (`make(chan error, 1)`). Without the buffer, the
goroutine would block forever trying to send after we stop reading, and the
goroutine would leak. This is a real bug in code that looks fine in testing.

The `errors.Is(err, http.ErrServerClosed)` check matters: when we call
`Shutdown`, `ListenAndServe` returns `ErrServerClosed`. That is expected, not
an error, so it must not be reported as a failure.

### Draining in-flight requests

```go
shutdownCtx, cancel := context.WithTimeout(context.Background(), cfg.HTTP.ShutdownTimeout)
defer cancel()

if err := srv.Shutdown(shutdownCtx); err != nil {
	return fmt.Errorf("graceful shutdown: %w", err)
}
```

`Shutdown` stops accepting new connections and waits for in-flight requests to
finish, bounded by the timeout.

**Why a fresh context rather than the cancelled one:** `ctx` is already
cancelled at this point. Passing it to `Shutdown` would make the timeout fire
immediately and kill in-flight requests, which is the opposite of graceful.

**Why this matters for Sahayak specifically:** if we sever a webhook
mid-flight, Meta retries. Combined with a missing dedup check, that is a
duplicate booking. Draining properly is a safety feature, not just politeness.

---

## The `main` pattern

```go
func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "fatal: %v\n", err)
		os.Exit(1)
	}
}
```

All logic lives in `run()`. This exists because **`defer` does not run when you
call `os.Exit`**. Every `defer cancel()` and `defer stop()` in this file would
be skipped if we called `os.Exit` directly from inside the function holding
them.

**Python equivalent:** Python has `finally`, which does run on `sys.exit()`.
This is a genuine difference worth remembering.

---

## Context deadlines everywhere

```go
middleware.Timeout(30 * time.Second)
```

and in config:

```go
HTTP_READ_TIMEOUT=10s
HTTP_WRITE_TIMEOUT=15s
```

Three layers, and they are not redundant:

| Layer | Protects against |
| --- | --- |
| Read timeout | A client that opens a connection and sends nothing |
| Write timeout | A client that stops reading while we write a large response |
| Middleware timeout | A handler that hangs on a slow external call |

In Phase 3 the ASR call will get its own context deadline, because an
unbounded call to a third-party API is one of the easiest ways to exhaust
connection pools.

---

## Next

[06. Writing tests in Go](06-testing.md)
