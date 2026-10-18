# 01. Project Layout and the Module System

Covers `go.mod`, packages, the `internal/` rule, and import paths.

---

## What a Go module is

`go.mod` at your project root declares the module path and the Go version.

```go
module github.com/ankitnegi-dev/Sahayak

go 1.24

require github.com/go-chi/chi/v5 v5.5.0
```

The module path is the canonical import path for your code. Because your repo
lives at `github.com/ankitnegi-dev/Sahayak`, any file anywhere in the world
imports your packages with exactly that prefix.

**Python equivalent:** nothing. pip and Python have no built-in notion of a
project being one addressable unit. In Go it is mandatory, and it is what makes
`go get` work without a registry.

### The casing trap you just hit

The module path must match your repository name **exactly**, including capital
letters. Your repo is `Sahayak`, so the module is `github.com/ankitnegi-dev/Sahayak`.

This bit us during setup. We had `sahayak` in `go.mod` and `Sahayak` in the
imports, and Go rejected it:

```
case-insensitive import collision: "github.com/ankitnegi-dev/Sahayak/internal/config"
and "github.com/ankitnegi-dev/sahayak/internal/config"
```

Go treats `Sahayak` and `sahayak` as different modules, but your Windows
filesystem treats them as the same directory. That combination is unresolvable.
On a case-sensitive Linux filesystem the same mistake might have half-worked,
which is worse. **Match your repo name exactly.**

---

## Packages and directories

Every directory containing `.go` files is a package. The directory name is
conventionally the package name.

```
internal/
├── config/       package config
├── logger/       package logger
└── httpapi/      package httpapi
```

**Python equivalent:** roughly a package, but Go has no `__init__.py`. A
directory with `.go` files *is* a package. There is no "implicit export."

### The `internal/` rule

This is the part worth internalizing.

```go
import "github.com/ankitnegi-dev/Sahayak/internal/config"
```

Anything under `internal/` is **only importable from within this module**. The
Go compiler enforces it. If someone tries to import `Sahayak/internal/config`
from another repository, the build fails.

This is why we put everything in `internal/`:

- You cannot accidentally create a public API you then have to maintain
- Import cycles become compile errors instead of review comments
- It signals intent: this is an application, not a library

**Python equivalent:** you would need a linter rule or a code review to stop
someone importing your private helpers. Go makes it a compile error.

**Trade-off:** this repo cannot be imported by anyone else. For an application
that is exactly right. We would move code to a top-level `pkg/` only if a
second project needed it.

---

## Imports

```go
import (
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/ankitnegi-dev/Sahayak/internal/config"
)
```

Three groups, separated by blank lines, which `gofmt` preserves:

1. **Standard library** (no domain in the path)
2. **Third party** (has a domain)
3. **Your own module**

`gofmt` sorts each group alphabetically and does not reorder between groups.

**Python equivalent:** `import os`, `from pathlib import Path`. The main
difference is that Go imports a whole package, not individual names. You do not
write `from log import slog`. You import `"log/slog"` and refer to `slog.Info`.

### Named imports for collisions

```go
import (
	sqlpg "github.com/jackc/pgx/v5"
	sqlm  "github.com/taklabs/pg_bridge"
)
```

Both packages are named `sql`. You alias them to disambiguate. You will need
this in Phase 4, since both `pgx` and `sqlc` generated code speak `database/sql`.

---

## Visibility: capital or not

Go has exactly one visibility rule: **the first letter of the identifier.**

```go
func Load() (Config, error)        // exported, callable from anywhere
func getEnv(key, fallback string) string  // unexported, this package only
```

This applies to types, fields, functions, methods, and constants.

```go
type Config struct {
	Env  string          // exported
	HTTP HTTPConfig      // exported
}

// inside the package:
type httpConfig struct {   // unexported type
	url string              // unexported field
}
```

**Python equivalent:** `_leading_underscore` is a *convention* that nothing
enforces. `__dunder__` is name mangling. In Go, capitalization is a hard
compiler rule, which is why exported names must be documented.

We rely on this constantly. In `internal/config`, `getEnv` and `getInt` are
private helpers, while `Load` is the package's entire public surface. The
package's test file is `package config_test`, so it can only reach `Load` and
`Config`, exactly like a real external caller.

---

## The `cmd/` convention

```
cmd/
├── api/main.go       package main
└── worker/main.go    package main
```

Two separate `package main` programs, each compiling to its own binary:

- `cmd/api` is the HTTP server
- `cmd/worker` is the background job processor

**Why not one binary with a subcommand?** Either works. Separate binaries make
the deployment and process model obvious: they scale and restart independently,
and a crash in the worker cannot take down webhook handling. That decision is
recorded as [ADR-002](../../docs/decisions.md).

Each `main.go` has the same shape:

```go
func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "fatal: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	// real work, returns errors instead of calling os.Exit
}
```

**The trick:** `main()` does nothing but convert an error into an exit code.
All logic lives in `run()` which returns `error`. This is necessary because
`defer` statements do not run when you call `os.Exit`. By keeping `run()` free
of `os.Exit`, defers inside it always execute. You will see this pattern again
in Phase 2 when we add signal handling.

---

## Functions and multiple returns

```go
func Load() (Config, error)
```

Go returns multiple values, comma-separated. The convention is that `error` is
always last, so callers can do:

```go
cfg, err := config.Load()
if err != nil {
	return Config{}, fmt.Errorf("load config: %w", err)
}
```

`:=` is short variable declaration, equivalent to Python's unpacking. Note the
shadowing risk: `err :=` inside an `if` block creates a *new* variable. We
always use `=` for `err` in that position.

---

## Next

[02. Configuration and environment variables](02-configuration.md)
