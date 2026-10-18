package logger_test

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/ankitnegi-dev/Sahayak/internal/logger"
)

func TestRedactPhone(t *testing.T) {
	// The mask keeps the first 5 and last 2 characters and stars the middle,
	// so the star count is len(input) - 7. Anything 7 characters or shorter
	// is masked entirely.
	tests := []struct {
		name  string
		phone string
		want  string
	}{
		{"full indian mobile", "+919812345678", "+9198******78"},
		{"ten digit", "9812345678", "98123***78"},
		{"already short", "+91", "****"},
		{"empty", "", "****"},
		{"exactly masked boundary", "1234567", "****"},
		{"one over boundary", "12345678", "12345*78"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := logger.RedactPhone(tt.phone); got != tt.want {
				t.Errorf("RedactPhone(%q) = %q, want %q", tt.phone, got, tt.want)
			}
		})
	}
}

// A redacted phone must not reveal the middle digits.
func TestRedactPhone_HidesMiddleDigits(t *testing.T) {
	got := logger.RedactPhone("+919812345678")
	if strings.Contains(got, "12345") {
		t.Errorf("RedactPhone leaked middle digits: %q", got)
	}
	if strings.Contains(got, "*") == false {
		t.Errorf("RedactPhone(%q) = %q, want masked output", "+919812345678", got)
	}
}

func TestSensitiveKeysAreRedacted(t *testing.T) {
	var buf bytes.Buffer
	log := logger.NewWithWriter(logger.Options{Environment: "development"}, &buf)

	log.Info("test", "token", "supersecret")

	out := buf.String()
	if strings.Contains(out, "supersecret") {
		t.Errorf("token was not redacted: %s", out)
	}
	if !strings.Contains(out, "[REDACTED]") {
		t.Errorf("expected [REDACTED] marker, got: %s", out)
	}
}

func TestLevelFiltering(t *testing.T) {
	tests := []struct {
		name      string
		opts      logger.Options
		wantDebug bool
		wantError bool
	}{
		{"development defaults to debug", logger.Options{Environment: "development"}, true, true},
		{"production defaults to info", logger.Options{Environment: "production"}, false, true},
		{"explicit level wins", logger.Options{Environment: "production", Level: "debug"}, true, true},
		{"explicit error level", logger.Options{Environment: "development", Level: "error"}, false, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var buf bytes.Buffer
			log := logger.NewWithWriter(tt.opts, &buf)

			log.Debug("debug-line")
			log.Error("error-line")

			out := buf.String()
			if gotDebug := strings.Contains(out, "debug-line"); gotDebug != tt.wantDebug {
				t.Errorf("debug line present = %v, want %v", gotDebug, tt.wantDebug)
			}
			if !strings.Contains(out, "error-line") {
				t.Error("error line missing, want it always present")
			}
		})
	}
}

func TestRedactPhone_LogSafe(t *testing.T) {
	var buf bytes.Buffer
	log := logger.NewWithWriter(logger.Options{Environment: "development"}, &buf)

	log.Info("incoming", "from", logger.RedactPhone("+919812345678"))

	out := buf.String()
	if strings.Contains(out, "9812345678") {
		t.Errorf("full phone number leaked into logs: %s", out)
	}
}

func TestRequestIDRoundTrip(t *testing.T) {
	ctx := logger.WithRequestID(context.Background(), "req-123")
	if got := logger.RequestID(ctx); got != "req-123" {
		t.Errorf("RequestID() = %q, want %q", got, "req-123")
	}
	if got := logger.RequestID(context.Background()); got != "" {
		t.Errorf("RequestID() on empty context = %q, want empty", got)
	}
}
