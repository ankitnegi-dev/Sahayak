// Command worker runs background jobs: transcribing voice notes, driving the
// conversation state machine, creating bookings and polling ride status.
//
// It is a separate process from the API so that a slow ASR call or a wedged
// provider cannot block webhook acknowledgements. Both binaries read the same
// configuration and connect to the same Postgres and Redis.
package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/ankitnegi-dev/Sahayak/internal/config"
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

	log.Info("worker starting", "env", cfg.Env, "version", version)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// Task queue wiring lands in Phase 2, once Redis and asynq are in place.
	// Until then, block until shutdown so the process can still be started,
	// health-checked and deployed end to end.
	log.Info("worker ready", "note", "no job handlers registered yet")

	<-ctx.Done()
	log.Info("shutdown signal received, draining")

	// Draining bound, mirroring the API's graceful shutdown.
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	<-shutdownCtx.Done()
	log.Info("worker stopped cleanly")
	return nil
}

var version = "dev"
