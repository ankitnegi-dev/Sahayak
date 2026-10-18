# 04. HTTP Routing with chi

Covers `internal/httpapi/router.go`: handlers, routing, request parameters, and
response codes.

---

## A handler is just a function

```go
func healthHandler(check func() error) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")

		if check != nil {
			if err := check(); err != nil {
				w.WriteHeader(http.StatusServiceUnavailable)
				writeJSON(w, map[string]any{"status": "unhealthy", "error": err.Error()})
				return
			}
		}
		w.WriteHeader(http.StatusOK)
		writeJSON(w, map[string]any{"status": "ok"})
	}
}
```

That is the whole abstraction. A handler takes an `http.ResponseWriter` to
write to and an `*http.Request` describing what came in.

**Python equivalent:** Flask's `@app.route`, or FastAPI's `@app.get`. In Go
there is no decorator and no framework object. You have a function with a
known signature.

`http.HandlerFunc` is the named type for `func(http.ResponseWriter, *http.Request)`.
Returning one from `healthHandler` is a factory: it captures `check` and gives
you a handler you can register.

---

## `net/http` versus a router

The standard library can already do this:

```go
http.HandleFunc("/healthz", healthHandler(nil))
http.ListenAndServe(":8080", nil)
```

We use chi ([ADR-008](../../docs/decisions.md)) because Phase 2 and Phase 7 need
things `net/http` does not give you directly: grouped routes, path parameters
like `/api/users/{id}`, and per-group middleware.

chi stays very close to `net/http`. A handler is still
`http.HandlerFunc`. `httptest` works unchanged. There is no request object you
must learn and no context you must understand. That is the reason to prefer it
over gin or echo, which hide the standard library behind their own types.

---

## Registering routes

```go
func NewRouter(deps Deps) http.Handler {
	r := chi.NewRouter()

	r.Use(middleware.RealIP)
	r.Use(middleware.Recoverer)
	r.Use(requestID)
	r.Use(logRequests(deps.Log))
	r.Use(middleware.Timeout(30 * time.Second))

	r.Get("/healthz", healthHandler(deps.Health))
	r.Get("/readyz", healthHandler(deps.Health))

	return r
}
```

Note we `return r`, not `r`. The function returns the `http.Handler`
**interface**, which is the correct thing to return. Returning the concrete
`*chi.Mux` would leak the router type into every caller and make it impossible
to swap the implementation. This is the interface rule in practice: declare the
return type where it is used, not where it is implemented.

---

## Passing dependencies

```go
type Deps struct {
	Log *slog.Logger
	Health func() error
}
```

There are no package-level globals. Everything the router needs arrives as an
argument. This is what makes the tests possible:

```go
func TestHealthz_UnhealthyReturns503(t *testing.T) {
	wantErr := errors.New("database unreachable")
	r := newTestRouter(discardLogger(), func() error { return wantErr })
	// ...
}
```

The test injects a failing health check and asserts a 503. With a global, this
would require mutating shared state and would not run in parallel safely.

The `Health` field is a `func() error` rather than a concrete Postgres pool.
That is an interface defined by a function signature. The router needs to know
"can this check fail", and nothing more. It does not need to know about
Postgres, and this package deliberately has no database dependency.

---

## Status codes must be written explicitly

```go
w.WriteHeader(http.StatusServiceUnavailable)
writeJSON(w, ...)
```

**This is the number one Go HTTP mistake.** `WriteHeader` must be called
**before** the first write to the body. The first `Write` implicitly sends
`200 OK`, and any `WriteHeader` after it is ignored with a log message.

The idiomatic way to avoid it is to set the header and write in one place:

```go
func writeJSON(w http.ResponseWriter, body any) {
	if err := encodeJSON(w, body); err != nil {
		// logging only; the status is already sent
	}
}
```

Real-world shape:

```go
func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}
```

Always pass the status explicitly. Do not rely on defaults.

---

## `defer w.WriteHeader(...)` is a trap

A pattern that looks helpful and is not:

```go
defer func() {
	w.WriteHeader(status)  // too late, body already written
}()
```

`defer` runs at function **exit**, by which point the body has been written
and the status is locked at 200. Our health handler is written correctly:
header first, then body, then an early `return`.

---

## Query parameters versus path parameters

```go
// query: /webhook?hub.mode=subscribe
r.URL.Query().Get("hub.mode")

// path: /api/users/{id}
chi.URLParam(r, "id")
```

The Meta verification handshake puts everything in the query string, so Phase 2
uses the first form. Path parameters arrive via `chi.URLParam` and only work
inside a matched chi route.

`r.URL.Query()` always returns a `url.Values` map, never nil, so
`.Get("missing")` returns `""` rather than panicking.

---

## Interfaces are satisfied implicitly

We have not written a single `implements` keyword in this project, and we never
will. When we define the ASR interface in Phase 3:

```go
type Transcriber interface {
	Transcribe(ctx context.Context, audio []byte) (Result, error)
}
```

any type with that exact method signature is a `Transcriber`. There is no
declaration needed on the concrete type:

```go
type SarvamClient struct{ ... }

func (s *SarvamClient) Transcribe(ctx context.Context, audio []byte) (Result, error) {
	// ...
}
```

`SarvamClient` is now a `Transcriber`. That is the whole mechanism.

And the interface is declared in the package that **uses** it, which is
`internal/asr`, not in the concrete client's package. Follow this rule from the
roadmap: **define interfaces where they are used, not where they are
implemented.** It keeps the dependency pointing in one direction and avoids
import cycles.

---

## Next

[05. Middleware and graceful shutdown](05-middleware-and-shutdown.md)
