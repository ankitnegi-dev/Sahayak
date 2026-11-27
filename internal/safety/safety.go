package safety

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/ankitnegi-dev/Sahayak/internal/conversation"
	"github.com/ankitnegi-dev/Sahayak/internal/store"
	"github.com/jackc/pgx/v5/pgtype"
)

// Config holds safety configuration.
type Config struct {
	DefaultPerRideCapPaise   int64
	DefaultDailyCapPaise     int64
	DefaultAllowedHoursStart int
	DefaultAllowedHoursEnd   int
	MaxDailyBookings         int
	RapidBookingWindow       time.Duration
	RapidBookingThreshold    int
	ApprovalExpiry           time.Duration
	LocationToleranceKm      float64
}

// DefaultConfig returns conservative defaults.
func DefaultConfig() Config {
	return Config{
		DefaultPerRideCapPaise:   50000,  // Rs 500
		DefaultDailyCapPaise:     150000, // Rs 1500
		DefaultAllowedHoursStart: 6,
		DefaultAllowedHoursEnd:   22,
		MaxDailyBookings:         6,
		RapidBookingWindow:       10 * time.Minute,
		RapidBookingThreshold:    2,
		ApprovalExpiry:           15 * time.Minute,
		LocationToleranceKm:      5.0,
	}
}

// Checker implements conversation.SafetyChecker.
type Checker struct {
	store  *store.Store
	clock  conversation.Clock
	config Config
}

// NewChecker returns a safety checker.
func NewChecker(s *store.Store, clock conversation.Clock, cfg Config) *Checker {
	if clock == nil {
		clock = conversation.RealClock{}
	}
	return &Checker{store: s, clock: clock, config: cfg}
}

// compile-time assertion.
var _ conversation.SafetyChecker = (*Checker)(nil)

// Check implements conversation.SafetyChecker.
func (c *Checker) Check(ctx context.Context, req conversation.SafetyRequest) conversation.SafetyDecision {
	if c.store == nil {
		return conversation.SafetyDecision{Type: conversation.SafetyDeny, Reason: "safety_check_failed"}
	}

	now := req.Now
	if now.IsZero() {
		now = c.clock.Now()
	}

	// Load limits with defaults.
	limits, err := c.loadLimits(ctx, req.UserID)
	if err != nil {
		return conversation.SafetyDecision{Type: conversation.SafetyDeny, Reason: "safety_check_failed"}
	}

	// Load saved places.
	places, err := c.store.Queries().ListSavedPlaces(ctx, store.PgtypeUUID(req.UserID))
	if err != nil {
		return conversation.SafetyDecision{Type: conversation.SafetyDeny, Reason: "safety_check_failed"}
	}

	// Load user's family links.
	links, err := c.store.Queries().ListFamilyLinks(ctx, store.PgtypeUUID(req.UserID))
	if err != nil {
		return conversation.SafetyDecision{Type: conversation.SafetyDeny, Reason: "safety_check_failed"}
	}
	_ = links

	// Load today's spend.
	istStart := startOfISTDay(now)
	todaySpend, err := c.store.Queries().SumTodaySpend(ctx, store.SumTodaySpendParams{
		UserID:    store.PgtypeUUID(req.UserID),
		CreatedAt: pgtype.Timestamptz{Time: istStart, Valid: true},
	})
	if err != nil {
		return conversation.SafetyDecision{Type: conversation.SafetyDeny, Reason: "safety_check_failed"}
	}

	// Load today's booking count.
	todayBookings, err := c.store.Queries().CountTodayBookings(ctx, store.CountTodayBookingsParams{
		UserID:    store.PgtypeUUID(req.UserID),
		CreatedAt: pgtype.Timestamptz{Time: istStart, Valid: true},
	})
	if err != nil {
		return conversation.SafetyDecision{Type: conversation.SafetyDeny, Reason: "safety_check_failed"}
	}

	// Load recent booking attempts (last 10 minutes).
	recentAttempts, err := c.store.Queries().CountRecentBookingAttempts(ctx, store.CountRecentBookingAttemptsParams{
		UserID:  store.PgtypeUUID(req.UserID),
		Column2: "10",
	})
	if err != nil {
		return conversation.SafetyDecision{Type: conversation.SafetyDeny, Reason: "safety_check_failed"}
	}

	// Check if destination is a saved place.
	isSaved := isSavedPlace(req.Drop, places)
	if req.IsSavedPlace {
		isSaved = true
	}

	// a) Kill switch.
	if limits.KillSwitch {
		return conversation.SafetyDecision{Type: conversation.SafetyDeny, Reason: "kill_switch"}
	}

	// Helper: check if the approval is valid for a given reason.
	approvalValid := func(reason string) bool {
		if req.Approval == nil {
			return false
		}
		if req.Approval.Status != "approved" {
			return false
		}
		if req.Approval.ExpiresAt.Before(now) {
			return false
		}
		// Snapshot must match.
		if req.Approval.Pickup != req.Pickup || req.Approval.Drop != req.Drop || req.Approval.RequestID != req.RequestID {
			return false
		}
		return true
	}

	// b) Allowed hours (IST).
	if !withinAllowedHours(now, limits.AllowedHoursStart, limits.AllowedHoursEnd) {
		if !approvalValid("outside_hours") {
			return conversation.SafetyDecision{Type: conversation.SafetyNeedsApproval, Reason: "outside_hours"}
		}
	}

	// c) Per-ride cap (only if fare known). Deny, never overridden.
	if req.FarePaise > 0 && req.FarePaise > limits.PerRideCapPaise {
		return conversation.SafetyDecision{Type: conversation.SafetyDeny, Reason: "per_ride_cap"}
	}

	// d) Daily cap. Deny, never overridden.
	if req.FarePaise > 0 && todaySpend+req.FarePaise > limits.DailyCapPaise {
		return conversation.SafetyDecision{Type: conversation.SafetyDeny, Reason: "daily_cap"}
	}

	// e) Daily booking count (covers unknown fares).
	if todayBookings >= int64(c.config.MaxDailyBookings) {
		if !approvalValid("too_many_bookings") {
			return conversation.SafetyDecision{Type: conversation.SafetyNeedsApproval, Reason: "too_many_bookings"}
		}
	}

	// f) Unsaved destination.
	if !isSaved {
		if !approvalValid("unknown_destination") {
			return conversation.SafetyDecision{Type: conversation.SafetyNeedsApproval, Reason: "unknown_destination"}
		}
	}

	// g) Rapid repeats.
	if recentAttempts >= int64(c.config.RapidBookingThreshold) {
		if !approvalValid("rapid_bookings") {
			return conversation.SafetyDecision{Type: conversation.SafetyNeedsApproval, Reason: "rapid_bookings"}
		}
	}

	return conversation.SafetyDecision{Type: conversation.SafetyAllow}
}

func (c *Checker) loadLimits(ctx context.Context, userID string) (store.Limit, error) {
	limits, err := c.store.Queries().GetLimits(ctx, store.PgtypeUUID(userID))
	if err != nil {
		if errors.Is(store.MapError(err), store.ErrNotFound) {
			// Use conservative defaults.
			return store.Limit{
				PerRideCapPaise:   c.config.DefaultPerRideCapPaise,
				DailyCapPaise:     c.config.DefaultDailyCapPaise,
				AllowedHoursStart: int16(c.config.DefaultAllowedHoursStart),
				AllowedHoursEnd:   int16(c.config.DefaultAllowedHoursEnd),
				KillSwitch:        false,
			}, nil
		}
		return store.Limit{}, err
	}
	// Apply defaults for zero values (a limits row might have only some
	// fields set).
	if limits.PerRideCapPaise == 0 {
		limits.PerRideCapPaise = c.config.DefaultPerRideCapPaise
	}
	if limits.DailyCapPaise == 0 {
		limits.DailyCapPaise = c.config.DefaultDailyCapPaise
	}
	if limits.AllowedHoursStart == 0 && limits.AllowedHoursEnd == 0 {
		limits.AllowedHoursStart = int16(c.config.DefaultAllowedHoursStart)
		limits.AllowedHoursEnd = int16(c.config.DefaultAllowedHoursEnd)
	}
	return limits, nil
}

func isSavedPlace(name string, places []store.SavedPlace) bool {
	for _, p := range places {
		if strings.EqualFold(p.Label, name) {
			return true
		}
		// Check address too.
		if strings.EqualFold(p.Address, name) {
			return true
		}
	}
	return false
}

// withinAllowedHours checks if the current time (IST) is within [start, end).
// Equal start and end means no restriction.
// Supports windows that cross midnight (e.g., 22-6).
func withinAllowedHours(now time.Time, start, end int16) bool {
	if start == end {
		return true
	}
	ist := now.In(time.FixedZone("IST", 5*3600+1800))
	hour := ist.Hour()
	if start < end {
		return int(hour) >= int(start) && int(hour) < int(end)
	}
	// Crosses midnight.
	return int(hour) >= int(start) || int(hour) < int(end)
}

// startOfISTDay returns the start of the current IST calendar day.
func startOfISTDay(now time.Time) time.Time {
	ist := time.FixedZone("IST", 5*3600+1800)
	t := now.In(ist)
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, ist)
}
