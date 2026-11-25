package conversation

import "time"

// Clock is an injectable time source. Tests use a fake clock so they can
// advance time without sleeping.
type Clock interface {
	Now() time.Time
}

// RealClock returns the system time.
type RealClock struct{}

// Now implements Clock.
func (RealClock) Now() time.Time { return time.Now().UTC() }
