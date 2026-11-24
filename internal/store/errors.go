package store

import (
	"errors"
)

// Typed errors returned by the store package. Callers branch with errors.Is
// rather than inspecting Postgres error codes.
var (
	// ErrNotFound means the requested row does not exist.
	ErrNotFound = errors.New("store: not found")

	// ErrDuplicate means a unique constraint was violated, for example a
	// duplicate phone number, idempotency key, or message ID.
	ErrDuplicate = errors.New("store: duplicate")

	// ErrIllegalTransition means an order status change was rejected by the
	// database trigger. Terminal states cannot change.
	ErrIllegalTransition = errors.New("store: illegal status transition")

	// ErrForeignKey means a referenced row does not exist.
	ErrForeignKey = errors.New("store: foreign key violation")

	// ErrConstraint means a CHECK constraint was violated.
	ErrConstraint = errors.New("store: constraint violation")

	// ErrAuditImmutable means an attempt was made to modify or delete an
	// audit log row. The table is append-only.
	ErrAuditImmutable = errors.New("store: audit log is append-only")
)
