package llm

import "context"

// Action is what the user wants to do.
type Action string

const (
	ActionBookRide Action = "book_ride"
	ActionCancel   Action = "cancel"
	ActionConfirm  Action = "confirm"
	ActionDecline  Action = "decline"
	ActionHelp     Action = "help"
	ActionUnknown  Action = "unknown"
)

// Intent is the structured output of the LLM extractor.
//
// The LLM fills this in. Go code decides what to do with it. The LLM never
// decides an action; it only reports what the user said.
type Intent struct {
	Action     Action  `json:"action" validate:"required,oneof=book_ride cancel confirm decline help unknown"`
	Pickup     string  `json:"pickup"`
	Drop       string  `json:"drop"`
	Time       string  `json:"time,omitempty"`
	Confidence float64 `json:"confidence" validate:"gte=0,lte=1"`
}

// Extractor turns free text into a structured Intent.
//
// Declared in the package that uses it (conversation), but defined here
// because the conversation package imports llm, not the other way around.
// Actually, the conversation package defines the interface it needs; this
// package provides implementations. The interface is in conversation.
type Extractor interface {
	Extract(ctx context.Context, text string, convoContext string) (Intent, error)
}

// IntentExtractor is an alias for clarity at call sites.
type IntentExtractor = Extractor
