//go:build integration

package safety

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/ankitnegi-dev/Sahayak/internal/conversation"
	"github.com/ankitnegi-dev/Sahayak/internal/store"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"
)

func testPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := "postgres://parentbot:parentbot@127.0.0.1:5432/parentbot_test?sslmode=disable"
	if v := os.Getenv("TEST_DATABASE_URL"); v != "" {
		dsn = v
	}
	pool, err := pgxpool.New(context.Background(), dsn)
	require.NoError(t, err)
	t.Cleanup(func() { pool.Close() })
	require.NoError(t, pool.Ping(context.Background()))
	return pool
}

func cleanDB(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	ctx := context.Background()
	for _, table := range []string{"orders", "approvals", "processed_messages", "conversations", "limits", "saved_places", "family_links", "users"} {
		_, err := pool.Exec(ctx, "TRUNCATE TABLE "+table+" CASCADE")
		require.NoError(t, err)
	}
}

func mustCreateUser(t *testing.T, pool *pgxpool.Pool, phone, role string) string {
	t.Helper()
	ctx := context.Background()
	var id string
	err := pool.QueryRow(ctx, "INSERT INTO users (phone, role, language) VALUES ($1, $2, 'hi') RETURNING id::text", phone, role).Scan(&id)
	require.NoError(t, err)
	return id
}

func TestSafety_KillSwitch(t *testing.T) {
	pool := testPool(t)
	cleanDB(t, pool)
	ctx := context.Background()
	userID := mustCreateUser(t, pool, "+91980000001", "parent")

	// Set kill switch on.
	pool.Exec(ctx, "INSERT INTO limits (user_id, kill_switch) VALUES ($1, true)", userID)

	checker := NewChecker(store.NewStore(pool), conversation.RealClock{}, DefaultConfig())
	decision := checker.Check(ctx, conversation.SafetyRequest{
		UserID: userID, Pickup: "ghar", Drop: "mandir", FarePaise: 10000,
		Now: time.Now(),
	})
	require.Equal(t, conversation.SafetyDeny, decision.Type)
	require.Equal(t, "kill_switch", decision.Reason)
}

func TestSafety_AllowedHours(t *testing.T) {
	pool := testPool(t)
	cleanDB(t, pool)
	ctx := context.Background()
	userID := mustCreateUser(t, pool, "+91980000002", "parent")

	pool.Exec(ctx, "INSERT INTO limits (user_id, allowed_hours_start, allowed_hours_end) VALUES ($1, 6, 22)", userID)

	checker := NewChecker(store.NewStore(pool), conversation.RealClock{}, DefaultConfig())

	// Within hours (10:00) -> allow (assuming other rules pass).
	now := time.Date(2026, 1, 15, 10, 0, 0, 0, time.FixedZone("IST", 5*3600+1800))
	decision := checker.Check(ctx, conversation.SafetyRequest{
		UserID: userID, Pickup: "ghar", Drop: "mandir", FarePaise: 10000,
		Now: now,
	})
	// Per-ride cap: 10000 <= 50000, daily cap: 10000 <= 150000, so should be allow
	// unless destination is unsaved. Let's check.
	t.Logf("decision: %v %s", decision.Type, decision.Reason)

	// Outside hours (23:00) -> needs approval.
	now2 := time.Date(2026, 1, 15, 23, 0, 0, 0, time.FixedZone("IST", 5*3600+1800))
	decision2 := checker.Check(ctx, conversation.SafetyRequest{
		UserID: userID, Pickup: "ghar", Drop: "mandir", FarePaise: 10000,
		Now: now2,
	})
	require.Equal(t, conversation.SafetyNeedsApproval, decision2.Type)
	require.Equal(t, "outside_hours", decision2.Reason)
}

func TestSafety_PerRideCap(t *testing.T) {
	pool := testPool(t)
	cleanDB(t, pool)
	ctx := context.Background()
	userID := mustCreateUser(t, pool, "+91980000003", "parent")

	pool.Exec(ctx, "INSERT INTO limits (user_id, per_ride_cap_paise) VALUES ($1, 50000)", userID)
	// Add saved places so destination check passes.
	pool.Exec(ctx, "INSERT INTO saved_places (user_id, label, address) VALUES ($1, 'mandir', 'Temple Rd')", userID)
	pool.Exec(ctx, "INSERT INTO saved_places (user_id, label, address) VALUES ($1, 'ghar', 'Home')", userID)

	checker := NewChecker(store.NewStore(pool), conversation.RealClock{}, DefaultConfig())

	// Exactly at cap -> allowed.
	now := time.Date(2026, 1, 15, 10, 0, 0, 0, time.FixedZone("IST", 5*3600+1800))
	decision := checker.Check(ctx, conversation.SafetyRequest{
		UserID: userID, Pickup: "ghar", Drop: "mandir", FarePaise: 50000,
		Now: now,
	})
	require.Equal(t, conversation.SafetyAllow, decision.Type)

	// One paisa over -> denied.
	decision2 := checker.Check(ctx, conversation.SafetyRequest{
		UserID: userID, Pickup: "ghar", Drop: "mandir", FarePaise: 50001,
		Now: now,
	})
	require.Equal(t, conversation.SafetyDeny, decision2.Type)
	require.Equal(t, "per_ride_cap", decision2.Reason)
}

func TestSafety_DailyCap(t *testing.T) {
	pool := testPool(t)
	cleanDB(t, pool)
	ctx := context.Background()
	userID := mustCreateUser(t, pool, "+91980000004", "parent")

	pool.Exec(ctx, "INSERT INTO limits (user_id, daily_cap_paise) VALUES ($1, 100000)", userID)
	pool.Exec(ctx, "INSERT INTO saved_places (user_id, label, address) VALUES ($1, 'mandir', 'Temple Rd')", userID)
	pool.Exec(ctx, "INSERT INTO saved_places (user_id, label, address) VALUES ($1, 'ghar', 'Home')", userID)

	// Add an existing order today.
	pool.Exec(ctx, "INSERT INTO orders (user_id, type, status, amount_paise, idempotency_key) VALUES ($1, 'ride', 'completed', 60000, 'k1')", userID)

	checker := NewChecker(store.NewStore(pool), conversation.RealClock{}, DefaultConfig())

	now := time.Date(2026, 1, 15, 10, 0, 0, 0, time.FixedZone("IST", 5*3600+1800))
	// 60000 + 30000 = 90000 <= 100000 -> allowed.
	decision := checker.Check(ctx, conversation.SafetyRequest{
		UserID: userID, Pickup: "ghar", Drop: "mandir", FarePaise: 30000,
		Now: now,
	})
	require.Equal(t, conversation.SafetyAllow, decision.Type)

	// 60000 + 50000 = 110000 > 100000 -> denied.
	decision2 := checker.Check(ctx, conversation.SafetyRequest{
		UserID: userID, Pickup: "ghar", Drop: "mandir", FarePaise: 50000,
		Now: now,
	})
	require.Equal(t, conversation.SafetyDeny, decision2.Type)
	require.Equal(t, "daily_cap", decision2.Reason)
}

func TestSafety_DailyCapExcludesCancelledFailed(t *testing.T) {
	pool := testPool(t)
	cleanDB(t, pool)
	ctx := context.Background()
	userID := mustCreateUser(t, pool, "+91980000005", "parent")

	pool.Exec(ctx, "INSERT INTO limits (user_id, daily_cap_paise) VALUES ($1, 100000)", userID)
	pool.Exec(ctx, "INSERT INTO saved_places (user_id, label, address) VALUES ($1, 'mandir', 'Temple Rd')", userID)
	pool.Exec(ctx, "INSERT INTO saved_places (user_id, label, address) VALUES ($1, 'ghar', 'Home')", userID)

	// Cancelled and failed orders should not count.
	pool.Exec(ctx, "INSERT INTO orders (user_id, type, status, amount_paise, idempotency_key) VALUES ($1, 'ride', 'cancelled', 80000, 'k1')", userID)
	pool.Exec(ctx, "INSERT INTO orders (user_id, type, status, amount_paise, idempotency_key) VALUES ($1, 'ride', 'failed', 80000, 'k2')", userID)

	checker := NewChecker(store.NewStore(pool), conversation.RealClock{}, DefaultConfig())

	now := time.Date(2026, 1, 15, 10, 0, 0, 0, time.FixedZone("IST", 5*3600+1800))
	// Only 0 spent (cancelled+failed excluded), so 50000 should be allowed.
	decision := checker.Check(ctx, conversation.SafetyRequest{
		UserID: userID, Pickup: "ghar", Drop: "mandir", FarePaise: 50000,
		Now: now,
	})
	require.Equal(t, conversation.SafetyAllow, decision.Type)
}

func TestSafety_UnsavedDestination(t *testing.T) {
	pool := testPool(t)
	cleanDB(t, pool)
	ctx := context.Background()
	userID := mustCreateUser(t, pool, "+91980000006", "parent")

	pool.Exec(ctx, "INSERT INTO limits (user_id) VALUES ($1)", userID)
	pool.Exec(ctx, "INSERT INTO saved_places (user_id, label, address) VALUES ($1, 'ghar', 'Home')", userID)

	checker := NewChecker(store.NewStore(pool), conversation.RealClock{}, DefaultConfig())

	now := time.Date(2026, 1, 15, 10, 0, 0, 0, time.FixedZone("IST", 5*3600+1800))
	// "airport" is not a saved place -> needs approval.
	decision := checker.Check(ctx, conversation.SafetyRequest{
		UserID: userID, Pickup: "ghar", Drop: "airport", FarePaise: 10000,
		Now: now,
	})
	require.Equal(t, conversation.SafetyNeedsApproval, decision.Type)
	require.Equal(t, "unknown_destination", decision.Reason)
}

func TestSafety_MissingLimitsDefaults(t *testing.T) {
	pool := testPool(t)
	cleanDB(t, pool)
	ctx := context.Background()
	userID := mustCreateUser(t, pool, "+91980000007", "parent")

	// No limits row. Should use defaults.
	checker := NewChecker(store.NewStore(pool), conversation.RealClock{}, DefaultConfig())

	now := time.Date(2026, 1, 15, 10, 0, 0, 0, time.FixedZone("IST", 5*3600+1800))
	// Default per-ride cap is 50000. 60000 should be denied.
	decision := checker.Check(ctx, conversation.SafetyRequest{
		UserID: userID, Pickup: "ghar", Drop: "mandir", FarePaise: 60000,
		Now: now,
	})
	require.Equal(t, conversation.SafetyDeny, decision.Type)
	require.Equal(t, "per_ride_cap", decision.Reason)
}

func TestSafety_FailClosed(t *testing.T) {
	// A nil store should cause a deny, not a panic or allow.
	checker := NewChecker(nil, conversation.RealClock{}, DefaultConfig())
	decision := checker.Check(context.Background(), conversation.SafetyRequest{
		UserID: "nonexistent", Pickup: "ghar", Drop: "mandir", FarePaise: 10000,
	})
	require.Equal(t, conversation.SafetyDeny, decision.Type)
	require.Equal(t, "safety_check_failed", decision.Reason)
}

func TestSafety_CrossMidnightHours(t *testing.T) {
	pool := testPool(t)
	cleanDB(t, pool)
	ctx := context.Background()
	userID := mustCreateUser(t, pool, "+91980000008", "parent")

	// Window 22:00-06:00 (crosses midnight).
	pool.Exec(ctx, "INSERT INTO limits (user_id, allowed_hours_start, allowed_hours_end) VALUES ($1, 22, 6)", userID)
	pool.Exec(ctx, "INSERT INTO saved_places (user_id, label, address) VALUES ($1, 'mandir', 'Temple Rd')", userID)
	pool.Exec(ctx, "INSERT INTO saved_places (user_id, label, address) VALUES ($1, 'ghar', 'Home')", userID)

	checker := NewChecker(store.NewStore(pool), conversation.RealClock{}, DefaultConfig())

	// 23:00 should be allowed (within 22-6).
	now := time.Date(2026, 1, 15, 23, 0, 0, 0, time.FixedZone("IST", 5*3600+1800))
	decision := checker.Check(ctx, conversation.SafetyRequest{
		UserID: userID, Pickup: "ghar", Drop: "mandir", FarePaise: 10000,
		Now: now,
	})
	require.Equal(t, conversation.SafetyAllow, decision.Type)

	// 12:00 should need approval (outside 22-6).
	now2 := time.Date(2026, 1, 15, 12, 0, 0, 0, time.FixedZone("IST", 5*3600+1800))
	decision2 := checker.Check(ctx, conversation.SafetyRequest{
		UserID: userID, Pickup: "ghar", Drop: "mandir", FarePaise: 10000,
		Now: now2,
	})
	require.Equal(t, conversation.SafetyNeedsApproval, decision2.Type)
	require.Equal(t, "outside_hours", decision2.Reason)
}

func TestSafety_EqualHoursNoRestriction(t *testing.T) {
	pool := testPool(t)
	cleanDB(t, pool)
	ctx := context.Background()
	userID := mustCreateUser(t, pool, "+91980000009", "parent")

	// Equal start and end means no restriction.
	pool.Exec(ctx, "INSERT INTO limits (user_id, allowed_hours_start, allowed_hours_end) VALUES ($1, 10, 10)", userID)
	pool.Exec(ctx, "INSERT INTO saved_places (user_id, label, address) VALUES ($1, 'mandir', 'Temple Rd')", userID)
	pool.Exec(ctx, "INSERT INTO saved_places (user_id, label, address) VALUES ($1, 'ghar', 'Home')", userID)

	checker := NewChecker(store.NewStore(pool), conversation.RealClock{}, DefaultConfig())

	now := time.Date(2026, 1, 15, 3, 0, 0, 0, time.FixedZone("IST", 5*3600+1800))
	decision := checker.Check(ctx, conversation.SafetyRequest{
		UserID: userID, Pickup: "ghar", Drop: "mandir", FarePaise: 10000,
		Now: now,
	})
	require.Equal(t, conversation.SafetyAllow, decision.Type)
}

func TestSafety_UnknownFareSkipsPerRideCap(t *testing.T) {
	pool := testPool(t)
	cleanDB(t, pool)
	ctx := context.Background()
	userID := mustCreateUser(t, pool, "+91980000010", "parent")

	pool.Exec(ctx, "INSERT INTO limits (user_id, per_ride_cap_paise) VALUES ($1, 1000)", userID)
	pool.Exec(ctx, "INSERT INTO saved_places (user_id, label, address) VALUES ($1, 'mandir', 'Temple Rd')", userID)
	pool.Exec(ctx, "INSERT INTO saved_places (user_id, label, address) VALUES ($1, 'ghar', 'Home')", userID)

	checker := NewChecker(store.NewStore(pool), conversation.RealClock{}, DefaultConfig())

	now := time.Date(2026, 1, 15, 10, 0, 0, 0, time.FixedZone("IST", 5*3600+1800))
	// Fare unknown (0) -> skip per-ride cap, but daily booking count check applies.
	decision := checker.Check(ctx, conversation.SafetyRequest{
		UserID: userID, Pickup: "ghar", Drop: "mandir", FarePaise: 0,
		Now: now,
	})
	// Should not be denied for per_ride_cap since fare is unknown.
	if decision.Reason == "per_ride_cap" {
		t.Error("per-ride cap should be skipped when fare is unknown")
	}
}

func TestSafety_UnsavedDestinationNeedsApproval(t *testing.T) {
	pool := testPool(t)
	cleanDB(t, pool)
	ctx := context.Background()
	userID := mustCreateUser(t, pool, "+91980000011", "parent")

	pool.Exec(ctx, "INSERT INTO limits (user_id) VALUES ($1)", userID)
	pool.Exec(ctx, "INSERT INTO saved_places (user_id, label, address) VALUES ($1, 'ghar', 'Home')", userID)

	checker := NewChecker(store.NewStore(pool), conversation.RealClock{}, DefaultConfig())

	now := time.Date(2026, 1, 15, 10, 0, 0, 0, time.FixedZone("IST", 5*3600+1800))
	decision := checker.Check(ctx, conversation.SafetyRequest{
		UserID: userID, Pickup: "ghar", Drop: "unknown_place", FarePaise: 10000,
		Now: now,
	})
	require.Equal(t, conversation.SafetyNeedsApproval, decision.Type)
	require.Equal(t, "unknown_destination", decision.Reason)
}
