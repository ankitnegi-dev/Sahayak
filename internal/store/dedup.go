package store

import (
	"context"
	"errors"
	"time"

	"github.com/ankitnegi-dev/Sahayak/internal/whatsapp"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// PostgresDeduper implements whatsapp.Deduper using processed_messages.
//
// The schema has wa_message_id as the primary key, so INSERT ... ON CONFLICT
// DO NOTHING gives us atomic dedup for free: two concurrent inserts of the
// same ID result in exactly one row being created, and RowsAffected tells us
// which caller won.
//
// This replaces MemoryDeduper in production. The interface is the same, so
// the webhook handler and its tests do not change.
type PostgresDeduper struct {
	q *Queries
}

// compile-time assertion that PostgresDeduper satisfies the interface.
var _ whatsapp.Deduper = (*PostgresDeduper)(nil)

// NewPostgresDeduper returns a deduper backed by the given pool.
//
// It accepts a pool rather than a Queries so that cmd/api can pass the pool
// directly without knowing about sqlc internals.
func NewPostgresDeduper(pool *pgxpool.Pool) *PostgresDeduper {
	return &PostgresDeduper{q: New(pool)}
}

// Claim implements whatsapp.Deduper.
//
// processed_messages has no state column, so Claim and Commit are both
// recorded as a single row. The protocol's distinction between "claimed but
// not yet published" and "accepted" is handled by the row's existence:
//
//   - If the row exists, the message has been accepted. Return
//     ErrDuplicateMessage.
//   - If the row does not exist, insert it. If the insert affects a row, we
//     won. If it affects zero rows (a concurrent insert won), it is a
//     duplicate.
//
// Abort deletes the row so a retry can claim it. This is the same recovery
// mechanism as MemoryDeduper, just persisted.
func (d *PostgresDeduper) Claim(ctx context.Context, messageID string, receivedAt time.Time) error {
	if messageID == "" {
		return errors.New("whatsapp: empty message id")
	}

	rows, err := d.q.MarkMessageSeen(ctx, messageID)
	if err != nil {
		return MapError(err)
	}

	if rows == 0 {
		// A concurrent insert won the race.
		return whatsapp.ErrDuplicateMessage
	}

	return nil
}

// Commit is a no-op for the Postgres implementation. The row inserted by Claim
// is already durable, which is what makes a duplicate detectable after a
// process restart.
func (d *PostgresDeduper) Commit(ctx context.Context, messageID string) error {
	return nil
}

// Abort removes the claim so a retry can proceed. This is called when
// publishing fails after a successful Claim.
func (d *PostgresDeduper) Abort(ctx context.Context, messageID string) error {
	// processed_messages has no DELETE query in the standard set, and the
	// dedup table is meant to be append-only for audit purposes. However,
	// Abort needs to remove the claim. Use a direct query.
	//
	// In practice this is rare: it only happens when Redis is down, and
	// even then the lease-based takeover in MemoryDeduper handles the case
	// without a delete. For Postgres, we delete the row.
	tag, err := d.q.db.Exec(ctx, "DELETE FROM processed_messages WHERE wa_message_id = $1", messageID)
	if err != nil {
		return MapError(err)
	}
	if tag.RowsAffected() == 0 {
		return whatsapp.ErrMessageNotClaimed
	}
	return nil
}

// WithTx is a helper for running dedup operations inside a transaction.
// Not used by the deduper itself, but exposed for the store package's
// transaction helper.
func (d *PostgresDeduper) WithTx(tx pgx.Tx) *PostgresDeduper {
	return &PostgresDeduper{q: d.q.WithTx(tx)}
}
