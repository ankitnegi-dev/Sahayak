//go:build integration

package store

import (
	"context"
	"errors"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"

	"github.com/ankitnegi-dev/Sahayak/internal/whatsapp"
)

func TestIntegration_MigrationsApplyAndRollback(t *testing.T) {
	// This test is run manually with goose. Here we just verify the schema
	// exists by querying a known table.
	pool := testPool(t)
	ctx := context.Background()

	var exists bool
	err := pool.QueryRow(ctx,
		"SELECT EXISTS (SELECT 1 FROM information_schema.tables WHERE table_name = 'users')").Scan(&exists)
	require.NoError(t, err)
	require.True(t, exists, "users table should exist after migrations")
}

func TestIntegration_DuplicateMessageID(t *testing.T) {
	pool := testPool(t)
	cleanDB(t, pool)
	ctx := context.Background()

	const msgID = "wamid.DUP001"

	// First insert succeeds.
	tag, err := pool.Exec(ctx, "INSERT INTO processed_messages (wa_message_id) VALUES ($1)", msgID)
	require.NoError(t, err)
	require.Equal(t, int64(1), tag.RowsAffected())

	// Second insert fails with unique violation.
	_, err = pool.Exec(ctx, "INSERT INTO processed_messages (wa_message_id) VALUES ($1)", msgID)
	require.Error(t, err)

	var pgErr *pgconn.PgError
	require.True(t, errors.As(err, &pgErr))
	require.Equal(t, "23505", pgErr.Code)
}

func TestIntegration_DuplicateMessageIDConcurrent(t *testing.T) {
	pool := testPool(t)
	cleanDB(t, pool)
	ctx := context.Background()

	const msgID = "wamid.RACE001"
	const goroutines = 50

	var wins int64
	var mu sync.Mutex
	var wg sync.WaitGroup

	wg.Add(goroutines)
	for i := 0; i < goroutines; i++ {
		go func() {
			defer wg.Done()
			tag, err := pool.Exec(ctx, "INSERT INTO processed_messages (wa_message_id) VALUES ($1) ON CONFLICT (wa_message_id) DO NOTHING", msgID)
			if err != nil {
				t.Errorf("unexpected error: %v", err)
				return
			}
			if tag.RowsAffected() == 1 {
				mu.Lock()
				wins++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()

	require.Equal(t, int64(1), wins, "exactly one goroutine should win the insert race")
}

func TestIntegration_OrderStatusTransitions(t *testing.T) {
	pool := testPool(t)
	cleanDB(t, pool)
	ctx := context.Background()

	userID := mustCreateUser(t, pool, "+919812345678", "parent")

	tests := []struct {
		name    string
		from    string
		to      string
		wantErr bool
	}{
		// Legal transitions
		{"created to confirmed", "created", "confirmed", false},
		{"confirmed to booked", "confirmed", "booked", false},
		{"booked to completed", "booked", "completed", false},
		{"created to cancelled", "created", "cancelled", false},
		{"confirmed to cancelled", "confirmed", "cancelled", false},
		{"booked to cancelled", "booked", "cancelled", false},
		{"created to failed", "created", "failed", false},
		{"confirmed to failed", "confirmed", "failed", false},
		{"booked to failed", "booked", "failed", false},
		// Illegal transitions
		{"created to booked", "created", "booked", true},
		{"created to completed", "created", "completed", true},
		{"confirmed to completed", "confirmed", "completed", true},
		{"confirmed to created", "confirmed", "created", true},
		{"booked to confirmed", "booked", "confirmed", true},
		{"booked to created", "booked", "created", true},
		// Terminal states
		{"completed to cancelled", "completed", "cancelled", true},
		{"cancelled to confirmed", "cancelled", "confirmed", true},
		{"failed to booked", "failed", "booked", true},
		{"completed to completed", "completed", "completed", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Create a fresh order in the "from" state for each case, because
			// terminal states cannot be transitioned into via UPDATE.
			var id string
			err := pool.QueryRow(ctx,
				"INSERT INTO orders (user_id, type, status, idempotency_key) VALUES ($1, 'ride', $2, $3) RETURNING id::text",
				userID, tt.from, "transition-"+tt.name).Scan(&id)
			require.NoError(t, err)

			_, err = pool.Exec(ctx, "UPDATE orders SET status = $1 WHERE id = $2", tt.to, id)
			if tt.wantErr {
				require.Error(t, err, "expected error for %s -> %s", tt.from, tt.to)
				var pgErr *pgconn.PgError
				if errors.As(err, &pgErr) {
					require.Equal(t, "23514", pgErr.Code)
				}
			} else {
				require.NoError(t, err, "expected success for %s -> %s", tt.from, tt.to)
			}
		})
	}
}

func TestIntegration_AuditLogAppendOnly(t *testing.T) {
	pool := testPool(t)
	cleanDB(t, pool)
	ctx := context.Background()

	userID := mustCreateUser(t, pool, "+919812345678", "parent")

	// Insert an audit row.
	var auditID int64
	err := pool.QueryRow(ctx,
		"INSERT INTO audit_log (actor, action, entity_type, entity_id) VALUES ($1, 'test', 'user', $2) RETURNING id",
		userID, userID).Scan(&auditID)
	require.NoError(t, err)

	// UPDATE should fail.
	_, err = pool.Exec(ctx, "UPDATE audit_log SET action = 'modified' WHERE id = $1", auditID)
	require.Error(t, err)
	var pgErr *pgconn.PgError
	require.True(t, errors.As(err, &pgErr))
	require.Equal(t, "42501", pgErr.Code)

	// DELETE should fail.
	_, err = pool.Exec(ctx, "DELETE FROM audit_log WHERE id = $1", auditID)
	require.Error(t, err)
	require.True(t, errors.As(err, &pgErr))
	require.Equal(t, "42501", pgErr.Code)
}

func TestIntegration_AuditLogRestrictedRole(t *testing.T) {
	// This test verifies the sahayak_app role cannot UPDATE or DELETE.
	// It requires the role to exist (created by a superuser).
	pool := testPool(t)
	cleanDB(t, pool)
	ctx := context.Background()

	// Check if the role exists.
	var exists bool
	err := pool.QueryRow(ctx, "SELECT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'sahayak_app')").Scan(&exists)
	require.NoError(t, err)
	if !exists {
		if os.Getenv("CI") == "true" {
			t.Fatal("sahayak_app role does not exist; in CI this is a failure, not a skip")
		}
		t.Skip("sahayak_app role does not exist (run migration as superuser to create it)")
	}

	// Insert as parentbot (superuser-ish for this test).
	userID := mustCreateUser(t, pool, "+919812345678", "parent")
	var auditID int64
	err = pool.QueryRow(ctx,
		"INSERT INTO audit_log (actor, action, entity_type, entity_id) VALUES ($1, 'test', 'user', $2) RETURNING id",
		userID, userID).Scan(&auditID)
	require.NoError(t, err)

	// Connect as sahayak_app.
	appPool, err := pgxpool.New(ctx, "postgres://sahayak_app:sahayak_app@127.0.0.1:5432/parentbot_test?sslmode=disable")
	require.NoError(t, err)
	defer appPool.Close()

	// SELECT should work.
	var count int64
	err = appPool.QueryRow(ctx, "SELECT count(*) FROM audit_log").Scan(&count)
	require.NoError(t, err)

	// INSERT should work.
	_, err = appPool.Exec(ctx, "INSERT INTO audit_log (actor, action) VALUES ('app', 'test')")
	require.NoError(t, err)

	// UPDATE should fail.
	_, err = appPool.Exec(ctx, "UPDATE audit_log SET action = 'x' WHERE id = $1", auditID)
	require.Error(t, err)

	// DELETE should fail.
	_, err = appPool.Exec(ctx, "DELETE FROM audit_log WHERE id = $1", auditID)
	require.Error(t, err)

	// Verify grants: sahayak_app should have SELECT/INSERT/UPDATE on normal
	// tables. We test this by attempting each operation on users.
	userID2 := mustCreateUser(t, pool, "+919999999999", "parent")
	var readBack string
	err = appPool.QueryRow(ctx, "SELECT phone FROM users WHERE id = $1", userID2).Scan(&readBack)
	require.NoError(t, err, "sahayak_app should have SELECT on users")
	require.Equal(t, "+919999999999", readBack)

	// UPDATE on a normal table should succeed.
	_, err = appPool.Exec(ctx, "UPDATE users SET language = 'en' WHERE id = $1", userID2)
	require.NoError(t, err, "sahayak_app should have UPDATE on users")

	// INSERT on a normal table should succeed.
	_, err = appPool.Exec(ctx, "INSERT INTO users (phone, role) VALUES ('+918888888888', 'parent')")
	require.NoError(t, err, "sahayak_app should have INSERT on users")

	// TRUNCATE should fail on every table.
	for _, table := range []string{"users", "orders", "audit_log", "processed_messages"} {
		_, err = appPool.Exec(ctx, "TRUNCATE TABLE "+table)
		require.Error(t, err, "sahayak_app should not have TRUNCATE on %s", table)
	}

	// audit_log: only SELECT and INSERT, no UPDATE or DELETE (already tested above).
	// Verify INSERT on audit_log works (already tested above via the INSERT).
	// Verify no UPDATE on audit_log (already tested above).

	// Verify that sahayak_app does NOT have UPDATE on audit_log by checking
	// the information_schema.
	var hasUpdate bool
	err = appPool.QueryRow(ctx,
		"SELECT EXISTS (SELECT 1 FROM information_schema.role_table_grants WHERE grantee = 'sahayak_app' AND table_name = 'audit_log' AND privilege_type = 'UPDATE')").Scan(&hasUpdate)
	require.NoError(t, err)
	require.False(t, hasUpdate, "sahayak_app must not have UPDATE on audit_log")

	var hasDelete bool
	err = appPool.QueryRow(ctx,
		"SELECT EXISTS (SELECT 1 FROM information_schema.role_table_grants WHERE grantee = 'sahayak_app' AND table_name = 'audit_log' AND privilege_type = 'DELETE')").Scan(&hasDelete)
	require.NoError(t, err)
	require.False(t, hasDelete, "sahayak_app must not have DELETE on audit_log")

	var hasInsert bool
	err = appPool.QueryRow(ctx,
		"SELECT EXISTS (SELECT 1 FROM information_schema.role_table_grants WHERE grantee = 'sahayak_app' AND table_name = 'audit_log' AND privilege_type = 'INSERT')").Scan(&hasInsert)
	require.NoError(t, err)
	require.True(t, hasInsert, "sahayak_app must have INSERT on audit_log")

	var hasSelect bool
	err = appPool.QueryRow(ctx,
		"SELECT EXISTS (SELECT 1 FROM information_schema.role_table_grants WHERE grantee = 'sahayak_app' AND table_name = 'audit_log' AND privilege_type = 'SELECT')").Scan(&hasSelect)
	require.NoError(t, err)
	require.True(t, hasSelect, "sahayak_app must have SELECT on audit_log")
}

func TestIntegration_CheckConstraints(t *testing.T) {
	pool := testPool(t)
	cleanDB(t, pool)
	ctx := context.Background()

	userID := mustCreateUser(t, pool, "+919812345678", "parent")

	// Bad role.
	_, err := pool.Exec(ctx, "INSERT INTO users (phone, role) VALUES ('+919999999999', 'admin')")
	require.Error(t, err)
	var pgErr *pgconn.PgError
	require.True(t, errors.As(err, &pgErr))
	require.Equal(t, "23514", pgErr.Code)

	// Bad order status.
	_, err = pool.Exec(ctx, "INSERT INTO orders (user_id, type, status, idempotency_key) VALUES ($1, 'ride', 'flying', 'k1')", userID)
	require.Error(t, err)
	require.True(t, errors.As(err, &pgErr))

	// Negative amount.
	_, err = pool.Exec(ctx, "INSERT INTO orders (user_id, type, status, amount_paise, idempotency_key) VALUES ($1, 'ride', 'created', -100, 'k2')", userID)
	require.Error(t, err)
	require.True(t, errors.As(err, &pgErr))

	// Negative cap.
	_, err = pool.Exec(ctx, "INSERT INTO limits (user_id, per_ride_cap_paise) VALUES ($1, -1)", userID)
	require.Error(t, err)
	require.True(t, errors.As(err, &pgErr))

	// Bad conversation state.
	_, err = pool.Exec(ctx, "INSERT INTO conversations (user_id, state) VALUES ($1, 'teleporting')", userID)
	require.Error(t, err)
	require.True(t, errors.As(err, &pgErr))

	// Bad family link status.
	childID := mustCreateUser(t, pool, "+919876543210", "child")
	_, err = pool.Exec(ctx, "INSERT INTO family_links (child_id, parent_id, status) VALUES ($1, $2, 'married')", childID, userID)
	require.Error(t, err)
	require.True(t, errors.As(err, &pgErr))
}

func TestIntegration_WithTxRollback(t *testing.T) {
	pool := testPool(t)
	cleanDB(t, pool)
	ctx := context.Background()

	userID := mustCreateUser(t, pool, "+919812345678", "parent")
	store := NewStore(pool)

	err := store.WithTx(ctx, func(q *Queries) error {
		_, err := q.CreateOrder(ctx, CreateOrderParams{
			UserID:         PgtypeUUID(userID),
			Type:           "ride",
			Status:         "created",
			IdempotencyKey: "rollback-test",
		})
		if err != nil {
			return err
		}
		// Simulate a failure.
		return errors.New("something went wrong")
	})
	require.Error(t, err)

	// The order should NOT exist because the transaction rolled back.
	_, err = store.Queries().GetOrderByUserAndIdempotencyKey(ctx, GetOrderByUserAndIdempotencyKeyParams{
		UserID:         PgtypeUUID(userID),
		IdempotencyKey: "rollback-test",
	})
	require.Error(t, err)
	require.True(t, errors.Is(err, pgx.ErrNoRows))
}

func TestIntegration_WithTxPanicRollback(t *testing.T) {
	pool := testPool(t)
	cleanDB(t, pool)
	ctx := context.Background()

	userID := mustCreateUser(t, pool, "+919812345678", "parent")
	store := NewStore(pool)

	func() {
		defer func() { recover() }()
		store.WithTx(ctx, func(q *Queries) error {
			_, err := q.CreateOrder(ctx, CreateOrderParams{
				UserID:         PgtypeUUID(userID),
				Type:           "ride",
				Status:         "created",
				IdempotencyKey: "panic-test",
			})
			require.NoError(t, err)
			panic("boom")
		})
	}()

	// The order should NOT exist because the panic triggered a rollback.
	_, err := store.Queries().GetOrderByUserAndIdempotencyKey(ctx, GetOrderByUserAndIdempotencyKeyParams{
		UserID:         PgtypeUUID(userID),
		IdempotencyKey: "panic-test",
	})
	require.Error(t, err)
	require.True(t, errors.Is(err, pgx.ErrNoRows))
}

func TestIntegration_SumTodaySpend(t *testing.T) {
	pool := testPool(t)
	cleanDB(t, pool)
	ctx := context.Background()

	userID := mustCreateUser(t, pool, "+919812345678", "parent")

	// Create orders with different statuses.
	statuses := []struct {
		status string
		amount int64
		key    string
	}{
		{"created", 10000, "k1"},
		{"confirmed", 20000, "k2"},
		{"booked", 30000, "k3"},
		{"completed", 40000, "k4"},
		{"cancelled", 50000, "k5"},
		{"failed", 60000, "k6"},
	}

	for _, s := range statuses {
		_, err := pool.Exec(ctx,
			"INSERT INTO orders (user_id, type, status, amount_paise, idempotency_key) VALUES ($1, 'ride', $2, $3, $4)",
			userID, s.status, s.amount, s.key)
		require.NoError(t, err)
	}

	store := NewStore(pool)
	total, err := store.Queries().SumTodaySpend(ctx, SumTodaySpendParams{
		UserID:    PgtypeUUID(userID),
		CreatedAt: pgtype.Timestamptz{Time: time.Now().Add(-24 * time.Hour), Valid: true},
	})
	require.NoError(t, err)

	// Sum should be 10000+20000+30000+40000 = 100000 (excluding cancelled and failed).
	require.Equal(t, int64(100000), total)
}

func TestIntegration_IdempotencyKeyPreventsDuplicateOrders(t *testing.T) {
	pool := testPool(t)
	cleanDB(t, pool)
	ctx := context.Background()

	userID := mustCreateUser(t, pool, "+919812345678", "parent")

	// First order succeeds.
	_, err := pool.Exec(ctx,
		"INSERT INTO orders (user_id, type, status, idempotency_key) VALUES ($1, 'ride', 'created', 'idem-1')",
		userID)
	require.NoError(t, err)

	// Second with same key fails.
	_, err = pool.Exec(ctx,
		"INSERT INTO orders (user_id, type, status, idempotency_key) VALUES ($1, 'ride', 'created', 'idem-1')",
		userID)
	require.Error(t, err)
	var pgErr *pgconn.PgError
	require.True(t, errors.As(err, &pgErr))
	require.Equal(t, "23505", pgErr.Code)
}

func TestIntegration_UpdateOrderStatusAndAudit(t *testing.T) {
	pool := testPool(t)
	cleanDB(t, pool)
	ctx := context.Background()

	userID := mustCreateUser(t, pool, "+919812345678", "parent")
	store := NewStore(pool)

	var orderID string
	err := pool.QueryRow(ctx,
		"INSERT INTO orders (user_id, type, status, idempotency_key) VALUES ($1, 'ride', 'created', 'audit-test') RETURNING id::text",
		userID).Scan(&orderID)
	require.NoError(t, err)

	err = store.UpdateOrderStatusAndAudit(ctx, orderID, "confirmed", "system", "order_confirmed", map[string]any{"fare": 15000})
	require.NoError(t, err)

	// Verify status changed.
	order, err := store.Queries().GetOrder(ctx, PgtypeUUID(orderID))
	require.NoError(t, err)
	require.Equal(t, "confirmed", order.Status)

	// Verify audit row exists.
	var auditCount int64
	err = pool.QueryRow(ctx, "SELECT count(*) FROM audit_log WHERE entity_id = $1", orderID).Scan(&auditCount)
	require.NoError(t, err)
	require.Equal(t, int64(1), auditCount)
}

func TestIntegration_PostgresDeduper(t *testing.T) {
	pool := testPool(t)
	cleanDB(t, pool)
	ctx := context.Background()

	deduper := NewPostgresDeduper(pool)

	// First claim succeeds.
	err := deduper.Claim(ctx, "wamid.PG001", time.Now().UTC())
	require.NoError(t, err)

	// Second claim is a duplicate.
	err = deduper.Claim(ctx, "wamid.PG001", time.Now().UTC())
	require.Error(t, err)
	require.True(t, errors.Is(err, whatsapp.ErrDuplicateMessage))

	// Abort releases the claim.
	err = deduper.Abort(ctx, "wamid.PG001")
	require.NoError(t, err)

	// After abort, claim succeeds again.
	err = deduper.Claim(ctx, "wamid.PG001", time.Now().UTC())
	require.NoError(t, err)

	// Commit is a no-op but should not error.
	err = deduper.Commit(ctx, "wamid.PG001")
	require.NoError(t, err)
}

func TestIntegration_PostgresDeduperConcurrent(t *testing.T) {
	pool := testPool(t)
	cleanDB(t, pool)
	ctx := context.Background()

	deduper := NewPostgresDeduper(pool)
	const msgID = "wamid.PGRACE"
	const goroutines = 50

	var wins int
	var mu sync.Mutex
	var wg sync.WaitGroup

	wg.Add(goroutines)
	for i := 0; i < goroutines; i++ {
		go func() {
			defer wg.Done()
			err := deduper.Claim(ctx, msgID, time.Now().UTC())
			if err == nil {
				mu.Lock()
				wins++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()

	require.Equal(t, 1, wins, "exactly one goroutine should win the claim race")
}
