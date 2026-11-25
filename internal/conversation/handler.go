package conversation

import (
	"context"
	"fmt"

	"github.com/ankitnegi-dev/Sahayak/internal/jobs"
	"github.com/ankitnegi-dev/Sahayak/internal/whatsapp"
)

// TextHandler processes a text message job by running it through the engine.
//
// It is the worker's entry point for text messages. Voice messages are handled
// by a separate handler in Phase 3's ASR pipeline (not yet wired).
type TextHandler struct {
	Engine *Engine
	// ReplySender sends the reply back to the parent. Nil means log only.
	ReplySender func(ctx context.Context, to, text string) error
}

// Handle processes one job.
func (h *TextHandler) Handle(ctx context.Context, task jobs.Task) error {
	if task.Kind != "text" {
		return nil // not a text message, skip
	}

	reply, err := h.Engine.Handle(ctx, task.From, task.Text)
	if err != nil {
		return fmt.Errorf("conversation: handle: %w", err)
	}

	if h.ReplySender != nil && reply.Text != "" {
		if err := h.ReplySender(ctx, task.From, reply.Text); err != nil {
			return fmt.Errorf("conversation: send reply: %w", err)
		}
	}

	return nil
}

// HandleMessage is a convenience that processes a WhatsApp message directly,
// without going through the job queue. Used in tests.
func HandleMessage(ctx context.Context, engine *Engine, msg whatsapp.IncomingMessage, replySender func(ctx context.Context, to, text string) error) error {
	handler := &TextHandler{Engine: engine, ReplySender: replySender}
	return handler.Handle(ctx, jobs.FromWhatsApp(msg))
}
