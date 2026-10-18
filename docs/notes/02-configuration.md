# 02. Configuration and Environment Variables

Covers `internal/config/config.go`: fail-fast config, error wrapping, sentinel
errors, and the production guard rails.

---

## The problem this solves

A parent sends a voice note, the bot tries to book a cab, and then the process
explodes because `WHATSAPP_ACCESS_TOKEN` was never set. That is a terrible
failure: the user sees silence, and you find out hours later.

The fix is to load and validate every piece of configuration **once, at
startup, before the server accepts a single request**. If config is wrong, the
process refuses to boot.

---

## Reading the environment

Go has no built-in config library in the standard library. The roadmap suggests
`caarlos0/env`, which uses struct tags:

```go
type Config struct {
	Env string `env:"APP_ENV" envDefault:"development"`
}
```

We did **not** use it. We wrote the loading by hand. Three reasons:

1. It is about 60 lines and you can read every line
2. One less dependency in a security-sensitive project
3. Our validation rules (production requires secrets, weak tokens rejected)
   do not fit a struct-tag model cleanly anyway

This is recorded as a deviation from the roadmap. If you later find yourself
copying a third config package, revisit this decision.

Here is the primitive:

```go
func getEnv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
```

**Python equivalent:**

```python
value = os.getenv("APP_ENV", "development")
```

Nearly identical. One Go-specific detail: `os.Getenv` returns `""` when the
variable is *unset*. This is also what you get when it is set to an empty
string, and Go cannot tell the difference. So "empty means use the default" is
the rule throughout.

That is why `.env.example` sets `REDIS_PASSWORD=` to empty, and why the
production check treats an empty `DATABASE_URL` as missing.

---

## Nested structs

```go
type Config struct {
	Env  string
	HTTP HTTPConfig

	DB      DBConfig
	Redis   RedisConfig
	Secrets Secrets
}
```

Configuration is grouped by concern, so `cfg.HTTP.Addr` reads better than a
flat `cfg.HTTPAddr`. Every nested struct has its own type, which keeps each
section testable on its own.

**Python equivalent:** nested dataclasses or pydantic models. The Go approach is
less ceremony because there is no runtime type validation happening here. We
validate manually in `validate()`.

### Why secrets live in their own struct

```go
type Config struct {
	// ...
	Secrets Secrets
}
```

Grouping credentials into one struct makes it possible to write a single
guard that stops them all being logged:

```go
func (c Config) LogValue() slog.Value {
	return slog.GroupValue(
		slog.String("env", c.Env),
		slog.String("addr", c.HTTP.Addr),
		slog.String("whatsapp_configured", c.Secrets.WhatsAppVerifyToken != ""),
	)
}
```

The `Config` value can be printed as a whole, and the `LogValue` method
controls what appears. Notice that secrets are reduced to booleans: you get to
see that the token is *set* without ever seeing it.

---

## Error wrapping with `%w`

This is the single most important Go idiom for you coming from Python.

```go
return fmt.Errorf("load config: %w", err)
```

The `%w` verb wraps the original error, producing a new error whose message is
`load config: <original>` and whose chain still contains the original.

Compare:

```go
fmt.Errorf("load config: %s", err)  // loses the cause
fmt.Errorf("load config: %w", err)  // keeps the cause
```

**Python equivalent:**

```python
raise ConfigError(f"load config: {e}") from e
```

The `from e` is the same idea as `%w`. It is the part people forget, and it is
the part that makes error handling debuggable.

### `errors.Is` and `errors.As`

Because the chain is preserved, you can ask questions about it:

```go
var missing config.ErrMissing
if errors.Is(err, config.ErrMissing) {
	// this failure was a missing variable
}
```

```go
if errors.Is(err, http.ErrServerClosed) {
	// expected during graceful shutdown, not a real error
}
```

`errors.Is` compares along the chain. `errors.As` finds the first error in the
chain assignable to a target type:

```go
var pathErr *fs.PathError
if errors.As(err, &pathErr) {
	log.Error("file failed", "path", pathErr.Path)
}
```

**Python equivalent:** catching by type with `except`, or checking message
substrings. In Go you never match on error strings. Never. Strings change;
identity does not.

---

## Sentinel errors

```go
// ErrMissing is returned when a required environment variable is not set.
var ErrMissing = errors.New("config: required variable missing")
```

A package-level error value. Callers can test for it with `errors.Is`.

We return a wrapped version:

```go
return fmt.Errorf("%w: %s", ErrMissing, strings.Join(missing, ", "))
```

which produces the message:

```
config: required variable missing: DATABASE_URL, WHATSAPP_ACCESS_TOKEN
```

and still matches `errors.Is(err, config.ErrMissing)`. You get a helpful
message and a testable condition at the same time.

This is a Go design choice worth noting: errors are values you can pass around,
compare, and attach data to. There is no exception hierarchy.

---

## Fail-fast validation

```go
func (c Config) validate(env string) error {
	var missing []string

	for _, v := range requiredVars(env) {
		if os.Getenv(v) == "" {
			missing = append(missing, v)
		}
	}

	if len(missing) > 0 {
		return fmt.Errorf("%w: %s", ErrMissing, strings.Join(missing, ", "))
	}

	if env == "production" {
		if len(c.Secrets.WhatsAppVerifyToken) < 16 {
			return errors.New("config: WHATSAPP_VERIFY_TOKEN must be at least 16 characters in production")
		}
		if c.HTTP.Addr == ":8080" {
			return errors.New("config: HTTP_ADDR must not be the default in production")
		}
	}
	return nil
}
```

Three behaviours worth studying:

1. **Collect everything, report once.** A developer setting up `.env` should
   see all five missing variables at once, not fix them one crash at a time.
2. **Differentiate by environment.** In development only `DATABASE_URL` is
   required, so unit and contract tests need no secrets. In production every
   credential is mandatory.
3. **Guard rails, not warnings.** A weak verify token is an authentication
   bypass. It is a hard failure in production, not a log line.

The second point is worth calling out because it shapes how we test. Our config
tests set `APP_ENV=production` and assert the process refuses to boot. During
setup this caught a real bug in our own test helper that was silently running
production tests in development mode.

---

## Malformed values fall back safely

```go
func getDuration(key string, fallback time.Duration) time.Duration {
	v := os.Getenv(key)
	if v == "" {
		return fallback
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		return fallback
	}
	return d
}
```

If `HTTP_READ_TIMEOUT=banana`, we fall back to `10s` instead of crashing.

**Is that the right call?** It is arguable. Silently ignoring a typo in a
duration is how you end up debugging a mysterious timeout for a week. An
argument exists for crashing instead, since a malformed value means the
operator made a mistake.

We chose fallback because the failure mode of crashing is worse than the failure
mode of a default. `TestLoad_InvalidValuesFallBackToDefaults` documents this
deliberately. If you disagree, this is a good change to make and discuss.

---

## Durations and parsing

```go
HTTP_READ_TIMEOUT=10s
```

Go durations are strings like `10s`, `1m30s`, `500ms`. `time.ParseDuration`
handles them. They are **not** bare numbers: `10` is invalid and falls back to
the default.

The equivalent type in Python is `datetime.timedelta`, but there is no standard
library parser for environment strings, so libraries like pydantic do this
conversion for you.

---

## Next

[03. Structured logging with `log/slog`](03-logging.md)
