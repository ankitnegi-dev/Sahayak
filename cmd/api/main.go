// Command api runs the public HTTP server: the WhatsApp webhook and the
// dashboard API. It owns no business logic; long-running work is handed to
// the worker process via Redis.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"

	"github.com/ankitnegi-dev/Sahayak/internal/config"
	"github.com/ankitnegi-dev/Sahayak/internal/httpapi"
	"github.com/ankitnegi-dev/Sahayak/internal/jobs"
	"github.com/ankitnegi-dev/Sahayak/internal/logger"
	"github.com/ankitnegi-dev/Sahayak/internal/whatsapp"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "fatal: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}

	log := logger.New(logger.Options{
		Environment: cfg.Env,
		Level:       os.Getenv("LOG_LEVEL"),
	})
	slog.SetDefault(log)

	log.Info("api starting",
		"env", cfg.Env,
		"addr", cfg.HTTP.Addr,
		"version", version,
	)

	// The webhook is mounted only when credentials are present. In local
	// development without Meta credentials the process still starts, so the
	// health endpoints and the test suite work, but the webhook route is
	// absent rather than returning 503 on every delivery.
	var webhookDeps *httpapi.WebhookDeps
	if cfg.Secrets.WhatsAppAppSecret != "" && cfg.Secrets.WhatsAppVerifyToken != "" {
		webhookDeps = &httpapi.WebhookDeps{
			Log:         log,
			VerifyToken: cfg.Secrets.WhatsAppVerifyToken,
			AppSecret:   cfg.Secrets.WhatsAppAppSecret,
			// Phase 4 swaps this for a Postgres-backed deduper with a unique
			// constraint. Until then, dedup is per-process and resets on
			// restart, which is acceptable only for development.
			Deduper:   whatsapp.NewMemoryDeduper(),
			Publisher: &jobs.LoggingPublisher{Log: log},
		}
		log.Info("webhook mounted", "dedupe", "in-memory (development only)")
	} else {
		log.Warn("webhook not mounted: WHATSAPP_APP_SECRET and WHATSAPP_VERIFY_TOKEN are required")
	}

	// Signal-aware context: a deploy or a local Ctrl-C triggers shutdown.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	router := httpapi.NewRouter(httpapi.Deps{
		Log:     log,
		Webhook: webhookDeps,
	})

	srv := &http.Server{
		Addr:         cfg.HTTP.Addr,
		Handler:      router,
		ReadTimeout:  cfg.HTTP.ReadTimeout,
		WriteTimeout: cfg.HTTP.WriteTimeout,
		IdleTimeout:  cfg.HTTP.IdleTimeout,
	}

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
		log.Info("shutdown signal received", "timeout", cfg.HTTP.ShutdownTimeout.String())
	}

	// Drain in-flight requests so an in-progress webhook is not severed.
	shutdownCtx, cancel := context.WithTimeout(context.Background(), cfg.HTTP.ShutdownTimeout)
	defer cancel()

	if err := srv.Shutdown(shutdownCtx); err != nil {
		return fmt.Errorf("graceful shutdown: %w", err)
	}

	log.Info("api stopped cleanly")
	return nil
}

// version is overridden at build time:
//
//	go build -ldflags "-X main.version=$(git rev-parse --short HEAD)"
var version = "dev"
