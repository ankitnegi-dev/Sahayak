// Package jobs defines the contract between the API and the worker.
//
// The API never processes a message inline. It verifies the webhook, records
// the message ID for idempotency, enqueues here, and returns 200. The worker
// picks the job up and does the slow work: downloading audio, transcribing,
// running the conversation engine and creating bookings.
//
// This split is what stops a slow ASR provider from causing Meta to time out
// and retry the webhook. See ADR-002.
package jobs

import (
	"context"
	"errors"
	"time"

	"github.com/ankitnegi-dev/Sahayak/internal/whatsapp"
)

// TaskType names the kinds of work the worker handles.
type TaskType string

const (
	// TaskHandleInboundMessage is the Phase 2 task: transcribe, converse,
	// and reply.
	TaskHandleInboundMessage TaskType = "whatsapp:inbound_message"
)

// ErrQueueFull means the broker refused the job because a limit was reached.
// Callers should treat this as retryable by the sender.
var ErrQueueFull = errors.New("jobs: queue is full")

// ErrClosed means the publisher has been shut down and is no longer accepting
// work.
var ErrClosed = errors.New("jobs: publisher is closed")

// Task is one unit of work handed from the API to the worker.
//
// The payload is deliberately small and self-contained. Audio is NOT carried
// here: the worker fetches it using the access token, because storing voice
// data in the queue would undermine the promise that audio is deleted right
// after processing.
type Task struct {
	Type TaskType `json:"type"`

	// MessageID is the WhatsApp message ID, carried through so the worker can
	// write it to the audit log and reconcile retries.
	MessageID string `json:"message_id"`

	// From is the parent's WhatsApp ID, international format, no leading plus.
	From string `json:"from"`

	// ProfileName is the parent's display name if Meta sent one.
	ProfileName string `json:"profile_name,omitempty"`

	// Kind is the message type: text, audio, and so on.
	Kind string `json:"kind"`

	// Text is the body for text messages.
	Text string `json:"text,omitempty"`

	// MediaID is the Meta media ID for attachments. The worker resolves it
	// into a temporary URL at processing time.
	MediaID string `json:"media_id,omitempty"`

	// MimeType is the media MIME type, such as "audio/ogg; codecs=opus".
	MimeType string `json:"mime_type,omitempty"`

	// Filename is the original filename for documents.
	Filename string `json:"filename,omitempty"`

	// IsForwarded marks forwarded content, which the safety layer treats as
	// untrusted third-party input.
	IsForwarded bool `json:"is_forwarded"`

	// ReceivedAt is when the API accepted the message, used for timeout and
	// staleness decisions in the conversation engine.
	ReceivedAt time.Time `json:"received_at"`
}

// FromWhatsApp converts a normalised WhatsApp message into a Task.
//
// The conversion lives here rather than in the webhook handler so that the
// API does not need to know the queue's payload shape, and so that a new
// message type needs a change in exactly one place.
func FromWhatsApp(msg whatsapp.IncomingMessage) Task {
	return Task{
		Type:        TaskHandleInboundMessage,
		MessageID:   msg.ID,
		From:        msg.From,
		ProfileName: msg.ProfileName,
		Kind:        msg.Type,
		Text:        msg.Text,
		MediaID:     msg.MediaID,
		MimeType:    msg.MimeType,
		Filename:    msg.Filename,
		IsForwarded: msg.IsForwarded,
		ReceivedAt:  msg.Timestamp,
	}
}

// Publisher enqueues tasks for the worker.
//
// Declared in the package that uses it, so the webhook handler depends on the
// capability rather than on a specific queue implementation. Phase 2 ships a
// logging implementation for local development; the asynq-backed one replaces
// it once Redis wiring lands.
type Publisher interface {
	Publish(ctx context.Context, task Task) error
}

// LoggingPublisher records tasks to a logger instead of a real queue.
//
// This is the Phase 2 default so the webhook can be exercised end to end with
// no Redis running. It is deliberately obvious in the logs, so nobody mistakes
// it for a working queue in production. Phase 9 replaces it with the asynq
// publisher and this type is deleted.
type LoggingPublisher struct {
	Log interface {
		InfoContext(ctx context.Context, msg string, args ...any)
	}
}

// compile-time assertion that LoggingPublisher satisfies the interface.
var _ Publisher = (*LoggingPublisher)(nil)

// Publish implements Publisher.
func (p *LoggingPublisher) Publish(ctx context.Context, task Task) error {
	p.Log.InfoContext(ctx, "jobs: task enqueued (logging publisher, not a real queue)",
		"type", string(task.Type),
		"message_id", task.MessageID,
		"kind", task.Kind,
	)
	return nil
}
