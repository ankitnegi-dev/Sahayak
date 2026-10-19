package whatsapp_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/ankitnegi-dev/Sahayak/internal/whatsapp"
)

func jsonUnmarshal(body string, v any) error { return json.Unmarshal([]byte(body), v) }

func decode(t *testing.T, body string) whatsapp.WebhookPayload {
	t.Helper()
	var p whatsapp.WebhookPayload
	if err := jsonUnmarshal(body, &p); err != nil {
		t.Fatalf("decode fixture: %v", err)
	}
	return p
}

const audioFixture = `{
  "object": "whatsapp_business_account",
  "entry": [{"id": "1", "changes": [{"field": "messages", "value": {
    "messaging_product": "whatsapp",
    "metadata": {"display_phone_number": "15550001111", "phone_number_id": "15550001111"},
    "contacts": [{"profile": {"name": "Papa"}, "wa_id": "919812345678"}],
    "messages": [{"from": "919812345678", "id": "wamid.A1", "timestamp": "1603059201",
      "type": "audio",
      "audio": {"mime_type": "audio/ogg; codecs=opus", "sha256": "abc", "id": "MEDIA1", "voice": true}}]
  }}]}]
}`

const textFixture = `{
  "object": "whatsapp_business_account",
  "entry": [{"id": "1", "changes": [{"field": "messages", "value": {
    "messaging_product": "whatsapp",
    "metadata": {"display_phone_number": "15550001111", "phone_number_id": "15550001111"},
    "contacts": [{"profile": {"name": "Maa"}, "wa_id": "919876543210"}],
    "messages": [{"from": "919876543210", "id": "wamid.T1", "timestamp": "1603059201",
      "type": "text", "text": {"body": "ghar bulao"}}]
  }}]}]
}`

func TestNormalize_Audio(t *testing.T) {
	msgs, err := whatsapp.Normalize(decode(t, audioFixture))
	if err != nil {
		t.Fatalf("Normalize() error = %v", err)
	}
	if len(msgs) != 1 {
		t.Fatalf("got %d messages, want 1", len(msgs))
	}

	msg := msgs[0]
	if msg.Type != whatsapp.MessageTypeAudio {
		t.Errorf("Type = %q, want %q", msg.Type, whatsapp.MessageTypeAudio)
	}
	if msg.MediaID != "MEDIA1" {
		t.Errorf("MediaID = %q, want MEDIA1", msg.MediaID)
	}
	if msg.MimeType != "audio/ogg; codecs=opus" {
		t.Errorf("MimeType = %q", msg.MimeType)
	}
	if msg.ProfileName != "Papa" {
		t.Errorf("ProfileName = %q, want Papa", msg.ProfileName)
	}
	want := time.Unix(1603059201, 0).UTC()
	if !msg.Timestamp.Equal(want) {
		t.Errorf("Timestamp = %v, want %v", msg.Timestamp, want)
	}
}

func TestNormalize_Text(t *testing.T) {
	msgs, err := whatsapp.Normalize(decode(t, textFixture))
	if err != nil {
		t.Fatalf("Normalize() error = %v", err)
	}
	if msgs[0].Text != "ghar bulao" {
		t.Errorf("Text = %q", msgs[0].Text)
	}
}

// Meta sends several messages in one delivery. All must be normalised.
func TestNormalize_MultipleMessagesInOneDelivery(t *testing.T) {
	const fixture = `{"object":"whatsapp_business_account","entry":[{"id":"1","changes":[
	  {"field":"messages","value":{"messaging_product":"whatsapp","contacts":[
	    {"profile":{"name":"Papa"},"wa_id":"919812345678"}],
	    "messages":[
	      {"from":"919812345678","id":"wamid.1","timestamp":"1603059201","type":"text","text":{"body":"one"}},
	      {"from":"919812345678","id":"wamid.2","timestamp":"1603059202","type":"text","text":{"body":"two"}},
	      {"from":"919812345678","id":"wamid.3","timestamp":"1603059203","type":"text","text":{"body":"three"}}]}}]}]}`

	msgs, err := whatsapp.Normalize(decode(t, fixture))
	if err != nil {
		t.Fatalf("Normalize() error = %v", err)
	}
	if len(msgs) != 3 {
		t.Fatalf("got %d messages, want 3", len(msgs))
	}
	for i, want := range []string{"wamid.1", "wamid.2", "wamid.3"} {
		if msgs[i].ID != want {
			t.Errorf("msgs[%d].ID = %q, want %q", i, msgs[i].ID, want)
		}
	}
}

func TestNormalize_EmptyAndIrrelevantPayloads(t *testing.T) {
	tests := []struct {
		name string
		body string
	}{
		{"no entries", `{"object":"whatsapp_business_account","entry":[]}`},
		{"status only", `{"object":"whatsapp_business_account","entry":[{"id":"1","changes":[{"field":"messages","value":{"statuses":[{"id":"s1","status":"delivered"}]}}]}]}`},
		{"non messages field", `{"object":"whatsapp_business_account","entry":[{"id":"1","changes":[{"field":"account_update","value":{}}]}]}`},
		{"empty value", `{"object":"whatsapp_business_account","entry":[{"id":"1","changes":[{"field":"messages","value":{}}]}]}`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			msgs, err := whatsapp.Normalize(decode(t, tt.body))
			if err != nil {
				t.Fatalf("Normalize() error = %v, want nil", err)
			}
			if len(msgs) != 0 {
				t.Errorf("got %d messages, want 0", len(msgs))
			}
		})
	}
}

// A message with no ID cannot be deduplicated, so it must be reported rather
// than silently dropped. Losing a parent's message is worse than a 400.
func TestNormalize_MessageWithoutIDIsAnError(t *testing.T) {
	const fixture = `{"object":"whatsapp_business_account","entry":[{"id":"1","changes":[
	  {"field":"messages","value":{"messaging_product":"whatsapp","messages":[
	    {"from":"919812345678","id":"","timestamp":"1603059201","type":"text","text":{"body":"hi"}}]}}]}]}`

	_, err := whatsapp.Normalize(decode(t, fixture))
	if !errors.Is(err, whatsapp.ErrMalformedPayload) {
		t.Errorf("Normalize() error = %v, want ErrMalformedPayload", err)
	}
}

func TestNormalize_UnknownTypeBecomesUnsupported(t *testing.T) {
	// Meta adds message types regularly. An unknown one must degrade, not crash.
	const fixture = `{"object":"whatsapp_business_account","entry":[{"id":"1","changes":[
	  {"field":"messages","value":{"messaging_product":"whatsapp","messages":[
	    {"from":"919812345678","id":"wamid.X","timestamp":"1603059201","type":"some_future_type"}]}}]}]}`

	msgs, err := whatsapp.Normalize(decode(t, fixture))
	if err != nil {
		t.Fatalf("Normalize() error = %v", err)
	}
	if msgs[0].Type != whatsapp.MessageTypeUnsupported {
		t.Errorf("Type = %q, want %q", msgs[0].Type, whatsapp.MessageTypeUnsupported)
	}
}

func TestNormalize_BadTimestampDoesNotError(t *testing.T) {
	const fixture = `{"object":"whatsapp_business_account","entry":[{"id":"1","changes":[
	  {"field":"messages","value":{"messaging_product":"whatsapp","messages":[
	    {"from":"919812345678","id":"wamid.T","timestamp":"not-a-timestamp","type":"text","text":{"body":"hi"}}]}}]}]}`

	msgs, err := whatsapp.Normalize(decode(t, fixture))
	if err != nil {
		t.Fatalf("Normalize() error = %v, want nil for a bad timestamp", err)
	}
	if !msgs[0].Timestamp.IsZero() {
		t.Errorf("Timestamp = %v, want zero time", msgs[0].Timestamp)
	}
}

func TestNormalize_ReplyToMessage(t *testing.T) {
	const fixture = `{"object":"whatsapp_business_account","entry":[{"id":"1","changes":[
	  {"field":"messages","value":{"messaging_product":"whatsapp","messages":[
	    {"from":"919812345678","id":"wamid.R","timestamp":"1603059201","type":"text","text":{"body":"haan"},
	     "context":{"from":"15550001111","id":"wamid.PREV"}}]}}]}]}`

	msgs, err := whatsapp.Normalize(decode(t, fixture))
	if err != nil {
		t.Fatalf("Normalize() error = %v", err)
	}
	if msgs[0].ReplyToMessageID != "wamid.PREV" {
		t.Errorf("ReplyToMessageID = %q, want wamid.PREV", msgs[0].ReplyToMessageID)
	}
	if msgs[0].IsForwarded {
		t.Error("IsForwarded = true for a reply with a context ID")
	}
}

// ---------------------------------------------------------------------------
// Deduper
// ---------------------------------------------------------------------------

func TestMemoryDeduper_ClaimThenCommitIsDuplicateAfterwards(t *testing.T) {
	d := whatsapp.NewMemoryDeduper()
	ctx := context.Background()
	now := time.Now().UTC()

	if err := d.Claim(ctx, "wamid.A", now); err != nil {
		t.Errorf("Claim() error = %v, want nil", err)
	}

	// A second Claim while still pending is a duplicate: this handler is
	// presumably still publishing.
	if err := d.Claim(ctx, "wamid.A", now); !errors.Is(err, whatsapp.ErrDuplicateMessage) {
		t.Errorf("second Claim() error = %v, want ErrDuplicateMessage", err)
	}

	if err := d.Commit(ctx, "wamid.A"); err != nil {
		t.Errorf("Commit() error = %v, want nil", err)
	}

	if err := d.Claim(ctx, "wamid.A", now); !errors.Is(err, whatsapp.ErrDuplicateMessage) {
		t.Errorf("Claim() after Commit error = %v, want ErrDuplicateMessage", err)
	}
}

// Abort must release the claim so a retry can proceed. Without this, a publish
// failure would cause the parent's message to be lost forever.
func TestMemoryDeduper_AbortReleasesClaim(t *testing.T) {
	d := whatsapp.NewMemoryDeduper()
	ctx := context.Background()
	now := time.Now().UTC()

	if err := d.Claim(ctx, "wamid.A", now); err != nil {
		t.Fatalf("Claim() error = %v", err)
	}
	if err := d.Abort(ctx, "wamid.A"); err != nil {
		t.Fatalf("Abort() error = %v", err)
	}

	if err := d.Claim(ctx, "wamid.A", now); err != nil {
		t.Errorf("Claim() after Abort error = %v, want nil", err)
	}
}

// A claim abandoned by a crashed process must become re-claimable once its
// lease expires, otherwise the message is stuck forever.
func TestMemoryDeduper_ExpiredClaimIsTakenOver(t *testing.T) {
	d := whatsapp.NewMemoryDeduper()
	ctx := context.Background()
	start := time.Now().UTC()

	if err := d.Claim(ctx, "wamid.A", start); err != nil {
		t.Fatalf("Claim() error = %v", err)
	}

	// Still inside the two minute lease: rejected.
	if err := d.Claim(ctx, "wamid.A", start.Add(time.Minute)); !errors.Is(err, whatsapp.ErrDuplicateMessage) {
		t.Errorf("Claim() within lease error = %v, want ErrDuplicateMessage", err)
	}

	// Lease expired: a retry may take over.
	if err := d.Claim(ctx, "wamid.A", start.Add(3*time.Minute)); err != nil {
		t.Errorf("Claim() after lease expiry error = %v, want nil", err)
	}
}

func TestMemoryDeduper_CommitAndAbortUnknownID(t *testing.T) {
	d := whatsapp.NewMemoryDeduper()
	ctx := context.Background()

	if err := d.Commit(ctx, "never-claimed"); !errors.Is(err, whatsapp.ErrMessageNotClaimed) {
		t.Errorf("Commit() error = %v, want ErrMessageNotClaimed", err)
	}
	if err := d.Abort(ctx, "never-claimed"); !errors.Is(err, whatsapp.ErrMessageNotClaimed) {
		t.Errorf("Abort() error = %v, want ErrMessageNotClaimed", err)
	}
}

func TestMemoryDeduper_DifferentIDsAreIndependent(t *testing.T) {
	d := whatsapp.NewMemoryDeduper()
	ctx := context.Background()

	for _, id := range []string{"a", "b", "c", "d", "e"} {
		if err := d.Claim(ctx, id, time.Now().UTC()); err != nil {
			t.Errorf("Claim(%q) error = %v", id, err)
		}
		if err := d.Commit(ctx, id); err != nil {
			t.Errorf("Commit(%q) error = %v", id, err)
		}
	}
	if d.Len() != 5 {
		t.Errorf("Len() = %d, want 5", d.Len())
	}
}

func TestMemoryDeduper_EmptyIDRejected(t *testing.T) {
	d := whatsapp.NewMemoryDeduper()
	if err := d.Claim(context.Background(), "  ", time.Now()); err == nil {
		t.Error("Claim with empty ID returned nil, want error")
	}
}

// Concurrent duplicates must yield exactly one success. This is what stops two
// webhook deliveries racing into two bookings.
func TestMemoryDeduper_ConcurrentDuplicateExactlyOneWins(t *testing.T) {
	d := whatsapp.NewMemoryDeduper()
	ctx := context.Background()
	now := time.Now().UTC()

	const goroutines = 100
	var wg sync.WaitGroup
	var mu sync.Mutex
	successes := 0

	wg.Add(goroutines)
	for i := 0; i < goroutines; i++ {
		go func() {
			defer wg.Done()
			if err := d.Claim(ctx, "wamid.RACE", now); err == nil {
				mu.Lock()
				successes++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()

	if successes != 1 {
		t.Errorf("%d goroutines claimed successfully, want exactly 1", successes)
	}
}

func TestMemoryDeduper_EvictionBoundsMemory(t *testing.T) {
	d := whatsapp.NewMemoryDeduper()
	ctx := context.Background()
	base := time.Now().UTC()

	// Insert well past the 10,000 entry cap.
	for i := 0; i < 11_000; i++ {
		id := fmt.Sprintf("wamid.%d", i)
		if err := d.Claim(ctx, id, base.Add(time.Duration(i)*time.Second)); err != nil {
			t.Fatalf("Claim() error = %v", err)
		}
		if err := d.Commit(ctx, id); err != nil {
			t.Fatalf("Commit() error = %v", err)
		}
	}

	if d.Len() > 10_000 {
		t.Errorf("Len() = %d, want at most 10000 after eviction", d.Len())
	}
	if d.Len() < 9_000 {
		t.Errorf("Len() = %d, eviction dropped too much", d.Len())
	}
}
