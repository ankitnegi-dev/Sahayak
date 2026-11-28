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
func (FakeExtractor) Extract(_ context.Context, text string, convoContext string) (Intent, error) {
	text = strings.TrimSpace(strings.ToLower(text))
	if text == "" {
		return Intent{Action: ActionUnknown, Confidence: 0}, nil
	}

	// The context is passed as a string containing state info.

	// Commands take priority.
	if containsAny(text, "cancel", "ruko", "nahi jaana", "band karo", "mat karo") {
		return Intent{Action: ActionCancel, Confidence: 0.9}, nil
	}
	if text == "haan" || text == "yes" || text == "sahi hai" || text == "theek hai" || text == "ok" || text == "okay" {
		return Intent{Action: ActionConfirm, Confidence: 0.9}, nil
	}
	if text == "nahi" || text == "no" || text == "galat" {
		return Intent{Action: ActionDecline, Confidence: 0.9}, nil
	}
	if containsAny(text, "help", "madad", "kya kar", "kaise") {
		return Intent{Action: ActionHelp, Confidence: 0.9}, nil
	}

	// If the conversation state indicates we're awaiting a place, try to
	// extract places first. If the user provides both, use both. Otherwise
	// treat the whole text as the requested place.
	if strings.Contains(convoContext, "state=awaiting_pickup") {
		p, d := extractPlaces(text)
		if p != "" && d != "" {
			return Intent{Action: ActionBookRide, Pickup: p, Drop: d, Confidence: 0.9}, nil
		}
		if p != "" {
			return Intent{Action: ActionBookRide, Pickup: p, Confidence: 0.9}, nil
		}
		return Intent{Action: ActionBookRide, Pickup: text, Confidence: 0.9}, nil
	}
	if strings.Contains(convoContext, "state=awaiting_drop") {
		_, d := extractPlaces(text)
		if d != "" {
			return Intent{Action: ActionBookRide, Drop: d, Confidence: 0.9}, nil
		}
		return Intent{Action: ActionBookRide, Drop: text, Confidence: 0.9}, nil
	}

	// Book ride: look for cab/ride keywords.
	if containsAny(text, "cab", "taxi", "ride", "auto", "rickshaw", "book") {
		intent := Intent{Action: ActionBookRide, Confidence: 0.9}
		intent.Pickup, intent.Drop = extractPlaces(text)
		return intent, nil
	}

	// Place names alone might indicate a booking context.
	if containsAny(text, "ghar", "mandir", "hospital", "office", "station", "airport", "market") {
		intent := Intent{Action: ActionBookRide, Confidence: 0.9}
		p, d := extractPlaces(text)
		if p == "" && d == "" {
			// Single place name with no "se" pattern: treat as pickup.
			intent.Pickup = text
		} else {
			intent.Pickup = p
			intent.Drop = d
		}
		return intent, nil
	}

	return Intent{Action: ActionUnknown, Confidence: 0.3}, nil
}
