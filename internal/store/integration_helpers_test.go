//go:build integration

package store

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"
)

// testPool returns a connection pool for integration tests.
//
// It uses TEST_DATABASE_URL, never DATABASE_URL, so a developer's real
// database can never be wiped by running the integration suite. The database
// name must end in "_test"; testPool fails loudly otherwise.
func testPool(t *testing.T) *pgxpool.Pool {
	t.Helper()

	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		dsn = "postgres://parentbot:parentbot@127.0.0.1:5432/parentbot_test?sslmode=disable"
	}

	dbName, err := databaseName(dsn)
	if err != nil {
		t.Fatalf("parse TEST_DATABASE_URL: %v", err)
	}
	if !strings.HasSuffix(dbName, "_test") {
		t.Fatalf("refusing to run integration tests against database %q: name must end in _test (set TEST_DATABASE_URL to a *_test database)", dbName)
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

// databaseName extracts the database name from a Postgres connection string.
func databaseName(dsn string) (string, error) {
	// pgx accepts both "postgres://..." URLs and "key=value" strings. Handle
	// the URL form, which is what we use everywhere.
	if strings.HasPrefix(dsn, "postgres://") || strings.HasPrefix(dsn, "postgresql://") {
		u, err := url.Parse(dsn)
		if err != nil {
			return "", err
		}
		name := strings.TrimPrefix(u.Path, "/")
		if name == "" {
			return "", fmt.Errorf("no database name in URL path")
		}
		return name, nil
	}

	// key=value form: look for dbname=...
	for _, part := range strings.Fields(dsn) {
		if strings.HasPrefix(part, "dbname=") {
			return strings.TrimPrefix(part, "dbname="), nil
		}
	}
	return "", fmt.Errorf("no dbname found in connection string")
}

// cleanDB truncates all tables except audit_log so each test starts from a
// known state.
//
// audit_log is deliberately excluded. It is append-only by design (trigger +
// restricted role), and truncating it in a test helper would weaken the very
// guarantee the audit log exists to provide. Tests that need a clean audit_log
// (TestIntegration_AuditLogAppendOnly, TestIntegration_AuditLogRestrictedRole)
// manage their own rows explicitly and assert the trigger rejects mutation.
func cleanDB(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	ctx := context.Background()
	tables := []string{
		"orders", "processed_messages", "conversations",
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
