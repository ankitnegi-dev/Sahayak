package conversation

import (
	"context"
	"time"
)

// SafetyRequest is what the engine sends to a SafetyChecker.
type SafetyRequest struct {
	UserID       string
	Pickup       string
	Drop         string
	IsSavedPlace bool
	FarePaise    int64
	Now          time.Time
	RequestID    string
	// Approval is the approved approval for this request, if any.
	Approval *Approval
}

// SafetyDecision is the result of a safety check.
type SafetyDecision struct {
	Type   SafetyDecisionType
	Reason string
}

type SafetyDecisionType string

const (
	SafetyAllow         SafetyDecisionType = "allow"
	SafetyDeny          SafetyDecisionType = "deny"
	SafetyNeedsApproval SafetyDecisionType = "needs_approval"
)

// SafetyChecker decides whether a booking is allowed.
//
// Declared here, in the package that uses it. Real rules (caps, hours, kill
// switch, approval) are implemented in internal/safety.
type SafetyChecker interface {
	Check(ctx context.Context, req SafetyRequest) SafetyDecision
}

// PermissiveSafety always allows. Used in tests.
type PermissiveSafety struct{}

// Check implements SafetyChecker.
func (PermissiveSafety) Check(_ context.Context, _ SafetyRequest) SafetyDecision {
	return SafetyDecision{Type: SafetyAllow}
}

// compile-time assertion.
var _ SafetyChecker = PermissiveSafety{}
