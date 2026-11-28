package conversation

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/ankitnegi-dev/Sahayak/internal/store"
)

// Approval is a snapshot of a safety check that required child approval.
type Approval struct {
	ID        string
	UserID    string
	ChildID   string
	Reason    string
	Status    string
	ShortCode string
	Pickup    string
	Drop      string
	RequestID string
	CreatedAt time.Time
	ExpiresAt time.Time
	DecidedAt *time.Time
}

// DecideApproval processes an approval decision from a child.
func (e *Engine) DecideApproval(ctx context.Context, actorUserID, code string, approve bool) (Approval, error) {
	var approval Approval
	err := e.store.WithTx(ctx, func(q *store.Queries) error {
		now := e.clock.Now()

		row, err := q.GetApprovalByCode(ctx, code)
		if err != nil {
			return fmt.Errorf("approval not found: %w", err)
		}

		// Check expiry.
		if row.Status == "pending" && row.ExpiresAt.Time.Before(now) {
			// Update in a committed transaction before returning error.
			e.store.WithTx(ctx, func(q *store.Queries) error {
				_ = q.UpdateApprovalStatus(ctx, store.UpdateApprovalStatusParams{
					ID:     row.ID,
					Status: "expired",
				})
				convo, err := q.GetConversationForUpdate(ctx, row.UserID)
				if err == nil && convo.UserID.Valid {
					convo.State = string(StateIdle)
					convo.Context, _ = (ConversationContext{}).Marshal()
					q.UpsertConversation(ctx, store.UpsertConversationParams{
						UserID:  row.UserID,
						State:   convo.State,
						Context: convo.Context,
					})
				}
				return nil
			})
			return fmt.Errorf("approval expired")
		}

		if row.Status != "pending" {
			return fmt.Errorf("approval already decided: %s", row.Status)
		}

		// Verify the actor is an active family link child of this parent.
		links, err := q.ListFamilyLinks(ctx, store.PgtypeUUID(uuidToString(row.UserID)))
		if err != nil {
			return fmt.Errorf("load family links: %w", err)
		}

		isActiveChild := false
		for _, link := range links {
			if uuidToString(link.ChildID) == actorUserID && link.Status == "active" && uuidToString(link.ParentID) == uuidToString(row.UserID) {
				isActiveChild = true
				break
			}
		}
		if !isActiveChild {
			return fmt.Errorf("actor is not an active family link child of this parent")
		}

		// Decide.
		status := "denied"
		if approve {
			status = "approved"
		}

		// Parse snapshot BEFORE updating conversation state.
		var snapshotPickup, snapshotDrop, snapshotRequestID string
		var snapshot map[string]any
		if err := json.Unmarshal(row.OrderSnapshot, &snapshot); err == nil {
			if v, ok := snapshot["pickup"].(string); ok {
				snapshotPickup = v
			}
			if v, ok := snapshot["drop"].(string); ok {
				snapshotDrop = v
			}
			if v, ok := snapshot["request_id"].(string); ok {
				snapshotRequestID = v
			}
		}

		// Update approval status.
		if err := q.UpdateApprovalStatus(ctx, store.UpdateApprovalStatusParams{
			ID:     row.ID,
			Status: status,
		}); err != nil {
			return fmt.Errorf("update approval: %w", err)
		}

		// If approved, move the conversation back to awaiting_confirmation.
		if approve {
			convo, err := q.GetConversationForUpdate(ctx, row.UserID)
			if err != nil {
				return fmt.Errorf("load conversation: %w", err)
			}
			convo.State = string(StateAwaitingConfirmation)
			// Update context with the snapshot data.
			cc, _ := UnmarshalContext(convo.Context)
			cc.RequestID = snapshotRequestID
			if snapshotPickup != "" {
				cc.Pickup = snapshotPickup
			}
			if snapshotDrop != "" {
				cc.Drop = snapshotDrop
			}
			convo.Context, _ = cc.Marshal()
			_, err = q.UpsertConversation(ctx, store.UpsertConversationParams{
				UserID:  row.UserID,
				State:   convo.State,
				Context: convo.Context,
			})
			if err != nil {
				return fmt.Errorf("update conversation: %w", err)
			}
		}

		// If denied, move the conversation back to idle.
		if !approve {
			convo, err := q.GetConversationForUpdate(ctx, row.UserID)
			if err != nil {
				return fmt.Errorf("load conversation: %w", err)
			}
			convo.State = string(StateIdle)
			convo.Context, _ = (ConversationContext{}).Marshal()
			_, err = q.UpsertConversation(ctx, store.UpsertConversationParams{
				UserID:  row.UserID,
				State:   convo.State,
				Context: convo.Context,
			})
			if err != nil {
				return fmt.Errorf("update conversation: %w", err)
			}
		}

		// Audit.
		details, _ := json.Marshal(map[string]any{
			"approval_id": uuidToString(row.ID),
			"decided_by":  actorUserID,
			"status":      status,
		})
		if _, auditErr := q.AppendAudit(ctx, store.AppendAuditParams{
			Actor:      actorUserID,
			Action:     "approval_decided",
			EntityType: pgtypeText("approval"),
			EntityID:   pgtypeText(uuidToString(row.ID)),
			Details:    details,
		}); auditErr != nil {
			return fmt.Errorf("append audit: %w", auditErr)
		}

		approval = Approval{
			ID:        uuidToString(row.ID),
			UserID:    uuidToString(row.UserID),
			ChildID:   uuidToString(row.ChildID),
			Reason:    row.Reason,
			Status:    status,
			ShortCode: row.ShortCode,
			ExpiresAt: row.ExpiresAt.Time,
		}

		// Set approval fields from parsed snapshot.
		approval.Pickup = snapshotPickup
		approval.Drop = snapshotDrop
		approval.RequestID = snapshotRequestID

		return nil
	})

	return approval, err
}

// ActiveFamilyChild returns the child ID of an active family link for the
// given parent, or empty string if none.
func ActiveFamilyChild(ctx context.Context, q *store.Queries, parentID string) string {
	links, err := q.ListFamilyLinks(ctx, pgtypeUUID(parentID))
	if err != nil {
		return ""
	}
	for _, link := range links {
		if uuidToString(link.ParentID) == parentID && link.Status == "active" {
			return uuidToString(link.ChildID)
		}
	}
	return ""
}
