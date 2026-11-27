package conversation

import "errors"

// ErrIllegalTransition is returned when a state+event pair is not in the
// transition table.
var ErrIllegalTransition = errors.New("conversation: illegal state transition")

// ErrUnknownPlace is returned when a place name cannot be resolved.
var ErrUnknownPlace = errors.New("conversation: unknown place")

// ErrTooManyClarifications is returned when the user has failed to provide a
// resolvable place three times.
var ErrTooManyClarifications = errors.New("conversation: too many clarification attempts")

// ErrNoPendingBooking is returned when a confirmation is requested but there
// is no pending booking.
var ErrNoPendingBooking = errors.New("conversation: no pending booking")

// ErrConversationNotFound is returned when a conversation row does not exist.
var ErrConversationNotFound = errors.New("conversation: conversation not found")

// TransitionTable maps (state, event) to the next state.
type TransitionTable map[State]map[Event]State

// Transitions is the explicit transition table. It is the single source of
// truth for what state changes are allowed.
//
// For EventUserMessage the next state is computed by the engine based on
// intent and context, so the table entry is a placeholder. The table's role
// for user messages is to declare that the event is legal from that state.
var Transitions = TransitionTable{
	StateIdle: {
		EventUserMessage: StateIdle,
		EventTimeout:     StateIdle,
		EventCancelled:   StateCancelled,
	},
	StateAwaitingPickup: {
		EventUserMessage: StateAwaitingPickup,
		EventTimeout:     StateIdle,
		EventCancelled:   StateCancelled,
	},
	StateAwaitingDrop: {
		EventUserMessage: StateAwaitingDrop,
		EventTimeout:     StateIdle,
		EventCancelled:   StateCancelled,
	},
	StateAwaitingConfirmation: {
		EventUserMessage: StateAwaitingConfirmation,
		EventConfirmed:   StateBooking,
		EventDeclined:    StateCancelled,
		EventCancelled:   StateCancelled,
		EventTimeout:     StateIdle,
	},
	StateAwaitingApproval: {
		EventUserMessage: StateAwaitingApproval,
		EventTimeout:     StateIdle,
		EventCancelled:   StateCancelled,
	},
	StateBooking: {
		EventBookingSucceeded: StateActiveRide,
		EventBookingFailed:    StateError,
	},
	StateActiveRide: {
		EventUserMessage:   StateActiveRide,
		EventRideCompleted: StateCompleted,
		EventCancelled:     StateCancelled,
	},
	StateCompleted: {},
	StateCancelled: {},
	StateError: {
		EventUserMessage: StateIdle,
	},
}

// IsLegal reports whether (from, event) is a valid transition.
func (t TransitionTable) IsLegal(from State, event Event) bool {
	if events, ok := t[from]; ok {
		_, ok := events[event]
		return ok
	}
	return false
}

// Next returns the next state for (from, event). For EventUserMessage the
// caller must compute the actual next state; this returns the placeholder.
func (t TransitionTable) Next(from State, event Event) (State, error) {
	if events, ok := t[from]; ok {
		if next, ok := events[event]; ok {
			return next, nil
		}
	}
	return "", ErrIllegalTransition
}
