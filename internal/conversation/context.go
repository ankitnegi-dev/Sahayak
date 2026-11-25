package conversation

import (
	"encoding/json"
	"time"
)

// ConversationContext is the JSON stored in conversations.context.
//
// It is the working memory of a conversation: what has been collected so far,
// how many clarification attempts have been made, and when the last prompt
// was shown. It is never shown to the user directly.
type ConversationContext struct {
	Pickup          string    `json:"pickup,omitempty"`
	Drop            string    `json:"drop,omitempty"`
	PickupPlaceID   string    `json:"pickup_place_id,omitempty"`
	DropPlaceID     string    `json:"drop_place_id,omitempty"`
	ClarifyAttempts int       `json:"clarify_attempts,omitempty"`
	LastPromptAt    time.Time `json:"last_prompt_at,omitempty"`
	PendingAction   string    `json:"pending_action,omitempty"`

	// FarePaise is the estimated fare, if known. Stored as integer paise.
	FarePaise int64 `json:"fare_paise,omitempty"`
}

// Marshal serialises the context to JSON for storage.
func (c ConversationContext) Marshal() ([]byte, error) {
	return json.Marshal(c)
}

// UnmarshalContext parses a stored context. A nil or empty slice yields an
// empty context, never an error, so a missing context is not fatal.
func UnmarshalContext(data []byte) (ConversationContext, error) {
	if len(data) == 0 {
		return ConversationContext{}, nil
	}
	var c ConversationContext
	if err := json.Unmarshal(data, &c); err != nil {
		return ConversationContext{}, err
	}
	return c, nil
}

// IdleTimeout is how long a collecting conversation may sit idle before it
// resets to idle.
const IdleTimeout = 10 * time.Minute

// IsTimedOut reports whether a collecting conversation has been idle too long.
func IsTimedOut(state State, lastPromptAt time.Time, now time.Time) bool {
	if !state.IsCollecting() {
		return false
	}
	if lastPromptAt.IsZero() {
		return false
	}
	return now.Sub(lastPromptAt) > IdleTimeout
}
