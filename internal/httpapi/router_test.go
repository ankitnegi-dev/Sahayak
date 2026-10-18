package httpapi_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ankitnegi-dev/Sahayak/internal/httpapi"
	"github.com/ankitnegi-dev/Sahayak/internal/logger"
)

func newTestRouter(log *slog.Logger, health func() error) http.Handler {
	return httpapi.NewRouter(httpapi.Deps{Log: log, Health: health})
}

func discardLogger() *slog.Logger {
	return logger.NewWithWriter(logger.Options{Environment: "development"}, io.Discard)
}

func TestHealthz_Healthy(t *testing.T) {
	r := newTestRouter(discardLogger(), nil)

	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/healthz", nil))

	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", ct)
	}

	var body map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if body["status"] != "ok" {
		t.Errorf("status field = %q, want ok", body["status"])
	}
}

func TestHealthz_UnhealthyReturns503(t *testing.T) {
	wantErr := errors.New("database unreachable")
	r := newTestRouter(discardLogger(), func() error { return wantErr })

	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/readyz", nil))

	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusServiceUnavailable)
	}

	var body map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if body["status"] != "unhealthy" {
		t.Errorf("status field = %q, want unhealthy", body["status"])
	}
}

func TestRequestID_GeneratedWhenAbsent(t *testing.T) {
	r := newTestRouter(discardLogger(), nil)

	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/healthz", nil))

	id := rec.Header().Get("X-Request-ID")
	if id == "" {
		t.Fatal("X-Request-ID header missing")
	}
	if len(id) != 32 {
		t.Errorf("X-Request-ID length = %d, want 32 hex chars", len(id))
	}
}

func TestRequestID_PropagatesIncoming(t *testing.T) {
	r := newTestRouter(discardLogger(), nil)

	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	req.Header.Set("X-Request-ID", "trace-me-123")

	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if got := rec.Header().Get("X-Request-ID"); got != "trace-me-123" {
		t.Errorf("X-Request-ID = %q, want trace-me-123", got)
	}
}

func TestRequestID_UniquePerRequest(t *testing.T) {
	r := newTestRouter(discardLogger(), nil)
	seen := map[string]bool{}

	for i := 0; i < 50; i++ {
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/healthz", nil))
		id := rec.Header().Get("X-Request-ID")
		if seen[id] {
			t.Fatalf("duplicate request ID generated: %s", id)
		}
		seen[id] = true
	}
}

func TestRequestIDFrom_Context(t *testing.T) {
	// Middleware attaches the ID to the request context; a nil-safe accessor
	// must never panic on a bare context.
	if got := httpapi.RequestIDFrom(context.Background()); got != "" {
		t.Errorf("RequestIDFrom(empty) = %q, want empty", got)
	}
}

func TestNotFoundReturns404(t *testing.T) {
	r := newTestRouter(discardLogger(), nil)

	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/does-not-exist", nil))

	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusNotFound)
	}
}

func TestLoggingMiddleware_EmitsRequestLine(t *testing.T) {
	var buf bytes.Buffer
	log := logger.NewWithWriter(logger.Options{Environment: "development"}, &buf)
	r := newTestRouter(log, nil)

	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/healthz", nil))

	out := buf.String()
	for _, want := range []string{"http_request", `"status":200`, `"route":"/healthz"`, "request_id"} {
		if !strings.Contains(out, want) {
			t.Errorf("log output missing %q; got: %s", want, out)
		}
	}
}

// A 4xx should be logged at warn so it is visible without alarming on-call.
func TestLoggingMiddleware_4xxIsWarnLevel(t *testing.T) {
	var buf bytes.Buffer
	log := logger.NewWithWriter(logger.Options{Environment: "development"}, &buf)
	r := newTestRouter(log, nil)

	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/nope", nil))

	if !strings.Contains(buf.String(), `"level":"WARN"`) {
		t.Errorf("expected WARN level for 404; got: %s", buf.String())
	}
}
