package store

import (
	"context"
	"encoding/json"
	"fmt"
)

// UpdateOrderStatusAndAudit updates an order's status and appends an audit
// row in a single transaction. Either both succeed or neither does.
//
// This is the helper the task asks for. It is the pattern every money-touching
// state change should follow: the state change and its audit record are atomic.
func (s *Store) UpdateOrderStatusAndAudit(ctx context.Context, orderID, newStatus, actor, action string, details map[string]any) error {
	return s.WithTx(ctx, func(q *Queries) error {
		if err := q.UpdateOrderStatus(ctx, UpdateOrderStatusParams{
			ID:     pgtypeUUID(orderID),
			Status: newStatus,
		}); err != nil {
			return MapError(err)
		}

		detailsJSON, err := json.Marshal(details)
		if err != nil {
			return fmt.Errorf("store: marshal audit details: %w", err)
		}

		_, err = q.AppendAudit(ctx, AppendAuditParams{
			Actor:      actor,
			Action:     action,
			EntityType: pgtypeText("order"),
			EntityID:   pgtypeText(orderID),
			Details:    detailsJSON,
		})
		if err != nil {
			return MapError(err)
		}

		return nil
	})
}

// Note: pgtypeUUID and pgtypeText helpers are defined below.
// They are thin wrappers around pgtype.UUID and pgtype.Text to keep
// the call sites readable.
