// Package httpapi contains the public HTTP surface: the WhatsApp webhook and
// the dashboard API. It wires routes and middleware but contains no business
// logic, so that handlers stay thin and testable.
package httpapi

import (
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
)

// Deps are the collaborators a router needs. They are passed explicitly
// instead of using package-level globals so tests can supply fakes.
type Deps struct {
	Log *slog.Logger
	// Health checks dependency readiness. A nil func reports healthy.
	Health func() error
	// Webhook configures the Meta webhook routes. When Deduper and Publisher
	// are both nil the routes are not mounted, which keeps Phase 1 style
	// deployments and health-only tests simple.
	Webhook *WebhookDeps
}

// NewRouter builds the application router.
func NewRouter(deps Deps) http.Handler {
	r := chi.NewRouter()

	r.Use(middleware.RealIP)
	r.Use(middleware.Recoverer)
	r.Use(requestID)
	r.Use(logRequests(deps.Log))
	r.Use(middleware.Timeout(30 * time.Second))

	r.Get("/healthz", healthHandler(deps.Health))
	r.Get("/readyz", healthHandler(deps.Health))

	// The webhook is mounted only when it is fully wired. A half-configured
	// webhook that returns 503 on every delivery would make Meta retry
	// forever, so it is better not to expose the route at all.
	if deps.Webhook != nil && deps.Webhook.Deduper != nil && deps.Webhook.Publisher != nil {
		hookDeps := *deps.Webhook
		if hookDeps.Log == nil {
			hookDeps.Log = deps.Log
		}
		mountWebhook(r, hookDeps)
	}

	return r
}

// healthHandler reports liveness and readiness.
//
// /healthz answers "is the process alive"; /readyz answers "can it serve
// traffic". Both currently share the dependency check because the process
// only starts listening after config validation succeeds.
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

func writeJSON(w http.ResponseWriter, body any) {
	// Errors here can only be an encoding failure of a map of primitives,
	// which cannot fail in practice.
	_ = encodeJSON(w, body)
}
