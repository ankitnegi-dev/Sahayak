package store

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Store wraps the sqlc-generated Queries and provides transaction helpers.
//
// It is a thin layer: sqlc owns the SQL, this package owns error mapping
// and transaction discipline. There is no business logic here.
type Store struct {
	pool *pgxpool.Pool
	q    *Queries
}

// NewStore returns a Store backed by the given pool.
func NewStore(pool *pgxpool.Pool) *Store {
	return &Store{pool: pool, q: New(pool)}
}

// Queries returns the underlying sqlc Queries for direct use.
func (s *Store) Queries() *Queries { return s.q }

// Pool returns the underlying pool, for cases that need a raw connection.
func (s *Store) Pool() *pgxpool.Pool { return s.pool }

// WithTx runs fn inside a transaction. It commits on success, rolls back on
// error or panic, and returns whatever fn returns.
//
// The transaction is passed to fn as a *Queries so that every statement inside
// fn runs against the same transaction. Mixing q and tx.Queries() inside fn
// would silently escape the transaction.
func (s *Store) WithTx(ctx context.Context, fn func(q *Queries) error) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("store: begin transaction: %w", err)
	}

	defer func() {
		if p := recover(); p != nil {
			_ = tx.Rollback(ctx)
			panic(p)
		}
	}()

	q := s.q.WithTx(tx)

	if err := fn(q); err != nil {
		if rbErr := tx.Rollback(ctx); rbErr != nil {
			return fmt.Errorf("store: rollback failed: %v (original: %w)", rbErr, err)
		}
		return err
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("store: commit transaction: %w", err)
	}
	return nil
}

// MapError translates a Postgres error into one of the typed errors above.
// Returns the original error unchanged if it does not match a known class.
func MapError(err error) error {
	if err == nil {
		return nil
	}

	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}

	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		switch pgErr.Code {
		case "23505": // unique_violation
			return fmt.Errorf("%w: %s", ErrDuplicate, pgErr.Message)
		case "23503": // foreign_key_violation
			return fmt.Errorf("%w: %s", ErrForeignKey, pgErr.Message)
		case "23514": // check_violation
			// The order status trigger raises SQLSTATE 23514 with a
			// message about illegal transitions. Map it specifically.
			if contains(pgErr.Message, "illegal order status transition") {
				return fmt.Errorf("%w: %s", ErrIllegalTransition, pgErr.Message)
			}
			return fmt.Errorf("%w: %s", ErrConstraint, pgErr.Message)
		case "42501": // insufficient_privilege
			// The audit log trigger raises with this code.
			if contains(pgErr.Message, "append-only") {
				return fmt.Errorf("%w: %s", ErrAuditImmutable, pgErr.Message)
			}
		}
	}

	return err
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && indexOf(s, substr) >= 0
}

func indexOf(s, substr string) int {
	for i := 0; i+len(substr) <= len(s); i++ {
		if s[i:i+len(substr)] == substr {
			return i
		}
	}
	return -1
}
