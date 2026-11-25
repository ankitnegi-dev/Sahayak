package conversation

// State is a conversation state, matching the CHECK constraint on
// conversations.state.
type State string

const (
	StateIdle                 State = "idle"
	StateAwaitingPickup       State = "awaiting_pickup"
	StateAwaitingDrop         State = "awaiting_drop"
	StateAwaitingConfirmation State = "awaiting_confirmation"
	StateBooking              State = "booking"
	StateActiveRide           State = "active_ride"
	StateCompleted            State = "completed"
	StateCancelled            State = "cancelled"
	StateError                State = "error"
)

// AllStates lists every valid state, in dependency order.
var AllStates = []State{
	StateIdle, StateAwaitingPickup, StateAwaitingDrop,
	StateAwaitingConfirmation, StateBooking, StateActiveRide,
	StateCompleted, StateCancelled, StateError,
}

// IsTerminal reports whether the state has no outbound transitions.
func (s State) IsTerminal() bool {
	return s == StateCompleted || s == StateCancelled || s == StateError
}

// IsCollecting reports whether the state is waiting for the parent to provide
// information. Collecting states are subject to the 10-minute idle timeout.
func (s State) IsCollecting() bool {
	return s == StateAwaitingPickup || s == StateAwaitingDrop || s == StateAwaitingConfirmation
}

// Event is something that happened to a conversation.
type Event string

const (
	EventUserMessage      Event = "user_message"
	EventConfirmed        Event = "confirmed"
	EventDeclined         Event = "declined"
	EventCancelled        Event = "cancelled"
	EventTimeout          Event = "timeout"
	EventBookingSucceeded Event = "booking_succeeded"
	EventBookingFailed    Event = "booking_failed"
	EventRideCompleted    Event = "ride_completed"
)
