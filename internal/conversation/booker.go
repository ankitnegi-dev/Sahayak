package conversation

import (
	"context"
	"errors"
)

// BookingRequest is what the engine sends to a Booker.
type BookingRequest struct {
	UserID         string
	Pickup         string
	Drop           string
	IdempotencyKey string
}

// BookingResult is what a Booker returns.
type BookingResult struct {
	Provider    string
	ProviderRef string
	DeepLink    string
}

// ErrBookingRejected means the provider definitively rejected the booking.
// The order should be marked failed.
var ErrBookingRejected = errors.New("booking: rejected by provider")

// Booker creates a ride booking.
//
// Declared here, in the package that uses it. The engine depends on this
// capability, never on a concrete provider. Real providers (deep link, Beckn)
// arrive in Phase 8.
type Booker interface {
	Book(ctx context.Context, req BookingRequest) (BookingResult, error)
	Cancel(ctx context.Context, providerRef string) error
}

// FakeBooker returns a canned deep link. Used in tests and for running the
// engine without a real booking provider.
type FakeBooker struct {
	DeepLink string
	Err      error

	// CancelErr is returned by Cancel if set.
	CancelErr error

	// BookCalls counts how many times Book was called.
	BookCalls int
}

// Name returns the booker name.
func (FakeBooker) Name() string { return "fake" }

// Book implements Booker.
func (f *FakeBooker) Book(_ context.Context, req BookingRequest) (BookingResult, error) {
	f.BookCalls++
	if f.Err != nil {
		return BookingResult{}, f.Err
	}
	link := f.DeepLink
	if link == "" {
		link = "https://example.com/ride"
	}
	return BookingResult{
		Provider:    "fake",
		ProviderRef: "fake-ref",
		DeepLink:    link,
	}, nil
}

// Cancel implements Booker.
func (f *FakeBooker) Cancel(_ context.Context, providerRef string) error {
	return f.CancelErr
}

// compile-time assertion.
var _ Booker = (*FakeBooker)(nil)
