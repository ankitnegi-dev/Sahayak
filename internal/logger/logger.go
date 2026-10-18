// Package logger builds the process-wide slog logger.
//
// Log records are structured key/value pairs so they can be queried and
// aggregated later. Secrets are never attached to the logger directly; the
// redaction helper is used at call sites that handle user data.
package logger

import (
	"context"
	"io"
	"log/slog"
	"os"
	"strings"
)

// Options configures logger construction.
type Options struct {
	// Environment selects the default level: development logs at debug,
	// production logs at info to keep volume and cost down.
	Environment string
	// Level optionally overrides the environment-derived level.
	Level string
}

// New returns a slog.Logger writing JSON to stdout.
func New(opts Options) *slog.Logger {
	return NewWithWriter(opts, os.Stdout)
}

// NewWithWriter is New with an explicit destination. Tests use it to capture
// output; deployments could use it to ship logs to a collector.
func NewWithWriter(opts Options, w io.Writer) *slog.Logger {
	var level slog.Level
	switch {
	case opts.Level != "":
		level = parseLevel(opts.Level)
	case opts.Environment == "development":
		level = slog.LevelDebug
	default:
		level = slog.LevelInfo
	}

	h := slog.NewJSONHandler(w, &slog.HandlerOptions{
		Level:       level,
		ReplaceAttr: redact,
	})

	return slog.New(h)
}

func parseLevel(s string) slog.Level {
	switch strings.ToLower(s) {
	case "debug":
		return slog.LevelDebug
	case "warn", "warning":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}

// sensitiveKeys are attribute names whose values must never reach the log
// stream, even if a future refactor forgets to be careful at a call site.
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

// redact masks any attribute registered in sensitiveKeys. Defence in depth:
// call sites should also avoid passing secrets, but this guarantees that an
// accidental log of a token cannot leak it into a log aggregator.
func redact(groups []string, a slog.Attr) slog.Attr {
	if sensitiveKeys[strings.ToLower(a.Key)] {
		return slog.String(a.Key, "[REDACTED]")
	}
	return a
}

// RedactPhone returns a masked phone number safe for logs, e.g. "+919812345678"
// becomes "+9198*****78". Parents' phone numbers are personal data and must
// not be logged in full.
func RedactPhone(phone string) string {
	digits := []rune(phone)
	if len(digits) < 4 {
		return "****"
	}
	// Keep the first 5 and last 2 characters, mask the middle.
	head := 5
	tail := 2
	if len(digits) <= head+tail {
		return "****"
	}
	var b strings.Builder
	b.WriteString(string(digits[:head]))
	b.WriteString(strings.Repeat("*", len(digits)-head-tail))
	b.WriteString(string(digits[len(digits)-tail:]))
	return b.String()
}

type ctxKey struct{}

// WithRequestID attaches a request ID to the context so every log line
// emitted while handling that message can be traced.
func WithRequestID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, ctxKey{}, id)
}

// RequestID retrieves the request ID previously stored on the context.
func RequestID(ctx context.Context) string {
	if v, ok := ctx.Value(ctxKey{}).(string); ok {
		return v
	}
	return ""
}
