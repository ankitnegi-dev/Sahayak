package conversation

import "context"

// Notifier sends a message to a user. Real implementations (WhatsApp) arrive
// later. The fake records messages for tests.
type Notifier interface {
	Notify(ctx context.Context, toUserID string, text string) error
}

// FakeNotifier records notifications for tests.
type FakeNotifier struct {
	Messages []struct {
		To   string
		Text string
	}
}

// Notify implements Notifier.
func (f *FakeNotifier) Notify(_ context.Context, toUserID string, text string) error {
	f.Messages = append(f.Messages, struct {
		To   string
		Text string
	}{To: toUserID, Text: text})
	return nil
}

// compile-time assertion.
var _ Notifier = (*FakeNotifier)(nil)
