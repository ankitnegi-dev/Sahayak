package llm

import (
	"context"
	"strings"
)

// FakeExtractor is a rule-based extractor for tests and for running without an
// API key. It recognises Hindi, Hinglish and English keywords.
//
// It is deliberately simple. It does not understand nuance, and it does not
// need to: the conversation engine handles ambiguity by asking clarifying
// questions, not by guessing.
type FakeExtractor struct{}

// Name returns the extractor name.
func (FakeExtractor) Name() string { return "fake" }

// Extract implements Extractor.
func (FakeExtractor) Extract(_ context.Context, text string, _ string) (Intent, error) {
	text = strings.TrimSpace(strings.ToLower(text))
	if text == "" {
		return Intent{Action: ActionUnknown, Confidence: 0}, nil
	}

	intent := Intent{Confidence: 0.9}

	// Cancel
	if containsAny(text, "cancel", "ruko", "nahi jaana", "band karo", "mat karo") {
		intent.Action = ActionCancel
		return intent, nil
	}

	// Confirm / decline
	if containsAny(text, "haan", "yes", "sahi hai", "theek hai", "ok", "okay", "karo", "book karo") {
		// "book karo" is a booking request, not a confirmation. But
		// "haan" alone is a confirmation.
		if text == "haan" || text == "yes" || text == "sahi hai" || text == "theek hai" || text == "ok" || text == "okay" {
			intent.Action = ActionConfirm
			return intent, nil
		}
	}

	if containsAny(text, "nahi", "no", "mat", "galat") {
		if text == "nahi" || text == "no" || text == "galat" {
			intent.Action = ActionDecline
			return intent, nil
		}
	}

	// Help
	if containsAny(text, "help", "madad", "kya kar", "kaise") {
		intent.Action = ActionHelp
		return intent, nil
	}

	// Book ride: look for cab/ride keywords
	if containsAny(text, "cab", "taxi", "ride", "auto", "rickshaw", "book") {
		intent.Action = ActionBookRide
		// Try to extract pickup and drop from "X se Y" pattern
		intent.Pickup, intent.Drop = extractPlaces(text)
		return intent, nil
	}

	// Place names alone might indicate a booking context
	if containsAny(text, "ghar", "mandir", "hospital", "office", "station", "airport", "market") {
		intent.Action = ActionBookRide
		intent.Pickup, intent.Drop = extractPlaces(text)
		return intent, nil
	}

	intent.Action = ActionUnknown
	intent.Confidence = 0.3
	return intent, nil
}

func containsAny(text string, keywords ...string) bool {
	for _, kw := range keywords {
		if strings.Contains(text, kw) {
			return true
		}
	}
	return false
}

// extractPlaces tries to find pickup and drop from "X se Y" or "X to Y" patterns.
func extractPlaces(text string) (pickup, drop string) {
	// Pattern: "X se Y" or "X se Y jaana"
	if idx := strings.Index(text, " se "); idx >= 0 {
		pickup = strings.TrimSpace(text[:idx])
		rest := strings.TrimSpace(text[idx+4:])
		// Remove trailing verbs
		for _, verb := range []string{"jaana hai", "jaana", "jaana hai", "aana hai", "aana", "chahiye", "book karo", "book"} {
			rest = strings.TrimSuffix(rest, verb)
			rest = strings.TrimSpace(rest)
		}
		if rest != "" {
			drop = rest
		}
	}

	// Pattern: "from X to Y"
	if pickup == "" && drop == "" {
		if idx := strings.Index(text, " from "); idx >= 0 {
			rest := text[idx+6:]
			if toIdx := strings.Index(rest, " to "); toIdx >= 0 {
				pickup = strings.TrimSpace(rest[:toIdx])
				drop = strings.TrimSpace(rest[toIdx+4:])
			}
		}
	}

	// Pattern: "pickup X, drop Y"
	if pickup == "" && drop == "" {
		if idx := strings.Index(text, "pickup "); idx >= 0 {
			rest := text[idx+7:]
			if commaIdx := strings.Index(rest, ","); commaIdx >= 0 {
				pickup = strings.TrimSpace(rest[:commaIdx])
				dropPart := strings.TrimSpace(rest[commaIdx+1:])
				dropPart = strings.TrimPrefix(dropPart, "drop ")
				drop = strings.TrimSpace(dropPart)
			}
		}
	}

	return pickup, drop
}
