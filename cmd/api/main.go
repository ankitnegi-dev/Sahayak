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
	"github.com/ankitnegi-dev/Sahayak/internal/logger"
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

	// Signal-aware context: a deploy or a local Ctrl-C triggers shutdown.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	router := httpapi.NewRouter(httpapi.Deps{Log: log})

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
