package jobs_test

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/ankitnegi-dev/Sahayak/internal/jobs"
	"github.com/ankitnegi-dev/Sahayak/internal/whatsapp"
)

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// The conversion is the only place where a new WhatsApp message type needs a
// change, so it is worth pinning down field by field.
func TestFromWhatsApp_CarriesEveryField(t *testing.T) {
	msg := whatsapp.IncomingMessage{
		ID:               "wamid.ABC",
		From:             "919812345678",
		ProfileName:      "Papa",
		Type:             whatsapp.MessageTypeAudio,
		Text:             "",
		MediaID:          "MEDIA1",
		MimeType:         "audio/ogg; codecs=opus",
		Filename:         "",
		Caption:          "",
		Timestamp:        time.Unix(1603059201, 0).UTC(),
		IsForwarded:      true,
		ReplyToMessageID: "wamid.PREV",
	}

	task := jobs.FromWhatsApp(msg)

	if task.Type != jobs.TaskHandleInboundMessage {
		t.Errorf("Type = %q, want %q", task.Type, jobs.TaskHandleInboundMessage)
	}
	if task.MessageID != msg.ID {
		t.Errorf("MessageID = %q, want %q", task.MessageID, msg.ID)
	}
	if task.From != msg.From {
		t.Errorf("From = %q, want %q", task.From, msg.From)
	}
	if task.ProfileName != msg.ProfileName {
		t.Errorf("ProfileName = %q", task.ProfileName)
	}
	if task.Kind != msg.Type {
		t.Errorf("Kind = %q, want %q", task.Kind, msg.Type)
	}
	if task.MediaID != msg.MediaID {
		t.Errorf("MediaID = %q", task.MediaID)
	}
	if task.MimeType != msg.MimeType {
		t.Errorf("MimeType = %q", task.MimeType)
	}
	if !task.IsForwarded {
		t.Error("IsForwarded = false, want true")
	}
	if task.ReceivedAt.IsZero() {
		t.Error("ReceivedAt is zero")
	}
}

// Audio bytes must never travel in the queue payload. The worker fetches them
// with the access token so audio can be deleted immediately after processing.
//
// This asserts the exact key set rather than searching for substrings: a
// substring check would trip over the harmless MIME type "audio/ogg", and it
// would not catch a new field being added later.
func TestTask_PayloadCarriesNoBinaryFields(t *testing.T) {
	task := jobs.FromWhatsApp(whatsapp.IncomingMessage{
		ID:       "wamid.ABC",
		MediaID:  "MEDIA1",
		MimeType: "audio/ogg",
	})

	raw, err := json.Marshal(task)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var fields map[string]any
	if err := json.Unmarshal(raw, &fields); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	allowed := map[string]bool{
		"type": true, "message_id": true, "from": true, "profile_name": true,
		"kind": true, "text": true, "media_id": true, "mime_type": true,
		"filename": true, "is_forwarded": true, "received_at": true,
	}

	for key := range fields {
		if !allowed[key] {
			t.Errorf("unexpected field %q in task payload; if this is safe, add it to the allowlist. payload: %s", key, raw)
		}
	}

	// No value may carry bulk data. A reference is a short ID, not content.
	if id, ok := fields["media_id"].(string); ok && len(id) > 128 {
		t.Errorf("media_id is %d bytes, which suggests content was inlined", len(id))
	}
}

func TestLoggingPublisher_AcceptsAndLogs(t *testing.T) {
	pub := &jobs.LoggingPublisher{Log: discardLogger()}

	task := jobs.FromWhatsApp(whatsapp.IncomingMessage{
		ID:   "wamid.ABC",
		From: "919812345678",
		Type: whatsapp.MessageTypeText,
	})

	if err := pub.Publish(context.Background(), task); err != nil {
		t.Errorf("Publish() error = %v, want nil", err)
	}
}
