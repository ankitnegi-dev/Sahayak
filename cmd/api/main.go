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
	"github.com/ankitnegi-dev/Sahayak/internal/store"
	"github.com/ankitnegi-dev/Sahayak/internal/whatsapp"
	"github.com/jackc/pgx/v5/pgxpool"
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

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// The webhook is mounted only when credentials are present. In local
	// development without Meta credentials the process still starts, so the
	// health endpoints and the test suite work, but the webhook route is
	// absent rather than returning 503 on every delivery.
	var webhookDeps *httpapi.WebhookDeps
	if cfg.Secrets.WhatsAppAppSecret != "" && cfg.Secrets.WhatsAppVerifyToken != "" {
		// Prefer Postgres-backed dedup when a database is configured. The
		// unique constraint on processed_messages.wa_message_id makes the
		// guarantee hold across restarts and across replicas, which the
		// in-memory version cannot.
		var deduper whatsapp.Deduper
		if cfg.DB.URL != "" {
			pool, err := pgxpool.New(ctx, cfg.DB.URL)
			if err != nil {
				log.Error("webhook: cannot connect to database", "error", err)
				return fmt.Errorf("connect database: %w", err)
			}
			// Verify connectivity before serving traffic.
			if err := pool.Ping(ctx); err != nil {
				pool.Close()
				log.Error("webhook: database ping failed", "error", err)
				return fmt.Errorf("ping database: %w", err)
			}
			deduper = store.NewPostgresDeduper(pool)
			log.Info("webhook dedup: postgres-backed")
		} else {
			deduper = whatsapp.NewMemoryDeduper()
			log.Warn("webhook dedup: in-memory (development only, resets on restart)")
		}

		webhookDeps = &httpapi.WebhookDeps{
			Log:         log,
			VerifyToken: cfg.Secrets.WhatsAppVerifyToken,
			AppSecret:   cfg.Secrets.WhatsAppAppSecret,
			Deduper:     deduper,
			Publisher:   &jobs.LoggingPublisher{Log: log},
		}
		log.Info("webhook mounted")
	} else {
		log.Warn("webhook not mounted: WHATSAPP_APP_SECRET and WHATSAPP_VERIFY_TOKEN are required")
	}

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
