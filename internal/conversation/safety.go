package conversation

import "context"

// SafetyRequest is what the engine sends to a SafetyChecker.
type SafetyRequest struct {
	UserID string
	Pickup string
	Drop   string
}

// SafetyChecker decides whether a booking is allowed.
//
// Declared here, in the package that uses it. Real rules (caps, hours, kill
// switch, approval) arrive in Phase 6. The permissive stub lets Phase 5
// exercise the full flow without the safety layer.
type SafetyChecker interface {
	Check(ctx context.Context, req SafetyRequest) error
}

// PermissiveSafety always allows. Used in tests and until Phase 6.
type PermissiveSafety struct{}

// Check implements SafetyChecker.
func (PermissiveSafety) Check(_ context.Context, _ SafetyRequest) error { return nil }

// compile-time assertion.
var _ SafetyChecker = PermissiveSafety{}
