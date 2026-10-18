# 06. Writing Tests in Go

Covers the test suites in `internal/config`, `internal/logger` and
`internal/httpapi`: table-driven tests, `httptest`, `t.Setenv`, and coverage.

---

## No test framework needed

Go has testing in the standard library. You import `testing` and write
functions. The roadmap suggests `testify` as an option, and we deliberately did
not add it, for three reasons:

1. `testing` plus a table is enough for everything we need so far
2. One less dependency
3. You learn the standard library, which is what a Go job actually uses

`testify` is genuinely good, so this is a judgement call rather than a
correction. Add it later if the table boilerplate starts to hurt.

**Python equivalent:** `unittest` or `pytest`. The nearest thing to Go's
`testing` is `unittest`, minus the class requirement.

---

## A test function

```go
func TestHealthz_Healthy(t *testing.T) {
	r := newTestRouter(discardLogger(), nil)

	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/healthz", nil))

	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusOK)
	}
}
```

The convention is `TestXxx`, where `Xxx` is the thing being tested. We use
`TestFunctionName_Condition` so failures are self-describing. Use `t.Fatal` to
stop immediately, `t.Error` to record a failure and keep going.

**Python equivalent:** `def test_healthz_healthy():`, with `assert`. The
difference is that `assert` vanishes when Python runs with `-O`. Go's testing
checks are always compiled in.

---

## Table-driven tests

This is the Go idiom you will see most often.

```go
func TestRedactPhone(t *testing.T) {
	tests := []struct {
		name  string
		phone string
		want  string
	}{
		{"full indian mobile", "+919812345678", "+9198******78"},
		{"ten digit", "9812345678", "98123***78"},
		{"already short", "+91", "****"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := logger.RedactPhone(tt.phone); got != tt.want {
				t.Errorf("RedactPhone(%q) = %q, want %q", tt.phone, got, tt.want)
			}
		})
	}
}
```

The anonymous struct `struct{ name, phone, want string }` is the unit of test
data. `t.Run` creates a **subtest**, so a failure reads:

```
--- FAIL: TestRedactPhone/full_indian_mobile
```

rather than `FAIL: line 28`. With thirty test cases that difference is the
difference between a two-minute fix and a twenty-minute one.

Note each case has a `name`. Without it, subtests would be named `TestRedactPhone/#00`.

**Python equivalent:** `@pytest.mark.parametrize`. Very close, except Go's
version needs the struct definition written out longhand.

---

## Subtests are parallelisable

```go
for _, tt := range tests {
	t.Run(tt.name, func(t *testing.T) {
		t.Parallel()   // optional
		// ...
	})
}
```

Each subtest gets its own `*testing.T` and can run concurrently. **But** only
if it captures its own data. Go 1.22 changed loop variable semantics so
`tt` is per-iteration, meaning the `tt := tt` line that older Go tutorials
insist on is now unnecessary. We require Go 1.24 in `go.mod`, so we do not
write it.

We do not currently use `t.Parallel()` in the config tests, because
`t.Setenv` forbids it. See below.

---

## Testing environment variables

```go
func clearEnv(t *testing.T) {
	t.Helper()
	for _, k := range []string{
		"APP_ENV", "HTTP_ADDR", "DATABASE_URL", /* ... */
	} {
		t.Setenv(k, "")
	}
}
```

`t.Setenv(key, value)` sets an environment variable **and registers cleanup**
that restores the original value when the test and its subtests finish.

**Why not `os.Setenv`:** it leaks. Without cleanup, every test pollutes the
process environment for every test after it, which produces failures that
depend on execution order. Those are miserable to debug.

**`t.Helper()`** marks this function as a test helper. When a failure happens
inside it, Go reports the **caller's** line rather than this one. Without it,
every failure points at line 6 of the helper.

### The restriction that caught our bug

`t.Setenv` **panics if the test has called `t.Parallel()`**. Go enforces this
because parallel tests share the process environment and would race.

This is why our config tests are sequential. It also surfaced a real bug during
setup: `setProductionEnv` never set `APP_ENV=production`, so three tests
silently ran in development mode and passed for the wrong reason. The failure
output was:

```
config_test.go:148: Load() error = nil, want weak verify token error
```

If you write a test helper, verify the helper actually sets up the state you
think it does. That was the lesson.

---

## `httptest`: no network needed

```go
rec := httptest.NewRecorder()
r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/healthz", nil))

rec.Code        // recorded status
rec.Body        // *bytes.Buffer of what was written
rec.Header()    // headers set by the handler
```

`httptest.NewRequest` builds a real `*http.Request` without a socket.
`httptest.NewRecorder` captures the response. Together they let you call a
handler as if it were serving HTTP, entirely in memory.

This is how the roadmap's "contract tests with fake servers" will work for
WhatsApp, ASR, Claude and booking providers: for those we start a real
`httptest.NewServer` and point the client's base URL at it, so actual HTTP
serialisation is exercised.

**Python equivalent:** Flask's `app.test_client()`, or FastAPI's
`TestClient` wrapping `httptest`. Very similar.

---

## Testing log output

```go
var buf bytes.Buffer
log := logger.NewWithWriter(logger.Options{Environment: "development"}, &buf)

log.Info("test", "token", "supersecret")

if strings.Contains(buf.String(), "supersecret") {
	t.Errorf("token was not redacted: %s", buf.String())
}
```

Because `NewWithWriter` takes an `io.Writer`, the whole logger is testable.
This is the single strongest argument for writing the constructor that way: the
redaction guarantee in [ADR-006](../../docs/decisions.md) is now enforced by a
test rather than by trust.

Always include the captured output in the failure message. A failing log
assertion is undebuggable without it.

---

## Coverage

```go
go test -cover ./...
```

```
internal/config    coverage: 100.0% of statements
internal/httpapi   coverage:  97.8% of statements
internal/logger    coverage:  91.2% of statements
```

```go
go test -coverprofile=coverage.out ./...
go tool cover -html=coverage.out    # opens a browser
```

Coverage measures which lines ran, **not whether the assertions were
meaningful**. 100% coverage with a test that only calls a function and asserts
nothing is worth less than 60% with real assertions.

The roadmap says: high coverage on safety, conversation and order code, do not
chase 100% everywhere. That is the right target. `cmd/api` and `cmd/worker`
show 0% right now, and that is fine, because they are wiring that gets
exercised end to end in Phase 11.

---

## The race detector

```go
go test -race ./...
```

This instruments every memory access and reports data races, which are
concurrent access where at least one is a write. It is the single highest-value
tool for this project, because Phase 2 onward is heavily concurrent: the
worker will run many message handlers in parallel.

**On Windows this requires a C compiler.** `runtime/cgo` needs `gcc`, so we
installed MinGW and `gcc` resolves to `C:\msys64\ucrt64\bin\gcc.exe`. CI runs
on Ubuntu, which has `gcc` preinstalled, so it works there without setup.

If `go test -race` fails with `runtime/cgo: exit status 2`, `gcc` is not on your
`PATH`. That is an environment problem, not a code problem.

---

## Where tests live

```
internal/config/
├── config.go
└── config_test.go        package config_test
```

Note `_test`. Our tests are in the **external test package**, meaning they see
only the exported API, exactly like a real caller.

This has two benefits. It proves the package has a usable public surface, and it
prevents tests from accidentally depending on unexported internals, which makes
refactoring far freer.

Go also supports an internal test package (`package config`) when you genuinely
need to test unexported functions. Use the external one by default.

---

## Testing philosophy for Sahayak

The roadmap asks for a scripted conversation library with adversarial cases:
wrong place, changed mind, abuse, gibberish, wrong language. Those become table
entries too. The pattern from Phase 5 will be:

```go
{name: "parent changes mind at confirmation", messages: [...], wantState: StateIdle},
{name: "parent says haan but pickup unknown",       messages: [...], wantState: StateAwaitingPickup},
{name: "parent pastes a link",                       messages: [...], wantState: StateAwaitingPickup},
```

And Phase 6's safety rules get the same treatment, where each case asserts that
a booking is **refused** even when the LLM output explicitly asks to proceed.
That is the test that proves the safety layer works.

---

## What comes next

Phase 2 tests will cover: valid signature, invalid signature, duplicate
message, malformed JSON. The last one matters more than it sounds, because a
malformed webhook body must produce a 400 and never reach the booking path.
