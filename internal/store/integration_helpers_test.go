//go:build integration

package store

import (
	"context"
	"os"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"
)

// testPool returns a connection pool for integration tests.
//
// It uses DATABASE_URL if set, otherwise falls back to the local Postgres
// instance that the Makefile's test-integration target starts. This keeps
// the tests runnable both in CI (where a service container provides
// Postgres) and on a developer machine (where Postgres runs locally).
func testPool(t *testing.T) *pgxpool.Pool {
	t.Helper()

	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		dsn = "postgres://parentbot:parentbot@localhost:5432/parentbot?sslmode=disable"
	}

	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	require.NoError(t, err, "connect to test database")

	t.Cleanup(func() { pool.Close() })

	if err := pool.Ping(ctx); err != nil {
		t.Fatalf("ping test database: %v (is Postgres running?)", err)
	}

	return pool
}

// cleanDB truncates all tables so each test starts from a known state.
func cleanDB(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	ctx := context.Background()
	tables := []string{
		"audit_log", "orders", "processed_messages", "conversations",
		"limits", "saved_places", "family_links", "users",
	}
	for _, table := range tables {
		_, err := pool.Exec(ctx, "TRUNCATE TABLE "+table+" CASCADE")
		require.NoError(t, err)
	}
}

// mustCreateUser inserts a user and returns its ID.
func mustCreateUser(t *testing.T, pool *pgxpool.Pool, phone, role string) string {
	t.Helper()
	ctx := context.Background()
	var id string
	err := pool.QueryRow(ctx,
		"INSERT INTO users (phone, role, language) VALUES ($1, $2, 'hi') RETURNING id::text",
		phone, role).Scan(&id)
	require.NoError(t, err)
	return id
}
