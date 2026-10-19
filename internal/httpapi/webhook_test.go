package httpapi_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/ankitnegi-dev/Sahayak/internal/httpapi"
	"github.com/ankitnegi-dev/Sahayak/internal/jobs"
	"github.com/ankitnegi-dev/Sahayak/internal/whatsapp"
)

const (
	testAppSecret   = "test-app-secret"
	testVerifyToken = "test-verify-token"
	testMessageID   = "wamid.HBgLMTU0NzYzNDU5"
	testSender      = "919812345678"
)

// recordingPublisher captures published tasks so tests can assert on them.
type recordingPublisher struct {
	mu    sync.Mutex
	tasks []jobs.Task
	err   error
}

func (p *recordingPublisher) Publish(_ context.Context, task jobs.Task) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.err != nil {
		return p.err
	}
	p.tasks = append(p.tasks, task)
	return nil
}

func (p *recordingPublisher) count() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.tasks)
}

func (p *recordingPublisher) setError(err error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.err = err
}

func (p *recordingPublisher) first() jobs.Task {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.tasks[0]
}

// failingDeduper simulates a dedup store that is unavailable.
type failingDeduper struct{ err error }

func (d failingDeduper) Claim(context.Context, string, time.Time) error { return d.err }
func (d failingDeduper) Commit(context.Context, string) error           { return nil }
func (d failingDeduper) Abort(context.Context, string) error            { return nil }

func newWebhookRouter(t *testing.T, deduper whatsapp.Deduper, pub jobs.Publisher) http.Handler {
	t.Helper()
	return httpapi.NewRouter(httpapi.Deps{
		Log: discardLogger(),
		Webhook: &httpapi.WebhookDeps{
			Log:         discardLogger(),
			VerifyToken: testVerifyToken,
			AppSecret:   testAppSecret,
			Deduper:     deduper,
			Publisher:   pub,
		},
	})
}

// postWebhook sends a signed body to the webhook, exactly as Meta would.
func postWebhook(t *testing.T, r http.Handler, body string, secret string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/webhook", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Hub-Signature-256", whatsapp.Sign([]byte(body), secret))

	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	return rec
}

// audioPayload mirrors a real Meta delivery for a parent sending a voice note.
const audioPayload = `{
  "object": "whatsapp_business_account",
  "entry": [{
    "id": "102290129340398",
    "changes": [{
      "field": "messages",
      "value": {
        "messaging_product": "whatsapp",
        "metadata": {"display_phone_number": "15550001111", "phone_number_id": "15550001111"},
        "contacts": [{"profile": {"name": "Papa"}, "wa_id": "919812345678"}],
        "messages": [{
          "from": "919812345678",
          "id": "wamid.HBgLMTU0NzYzNDU5",
          "timestamp": "1603059201",
          "type": "audio",
          "audio": {"mime_type": "audio/ogg; codecs=opus", "sha256": "abc123", "id": "MEDIA_ID_1", "voice": true}
        }]
      }
    }]
  }]
}`

const textPayload = `{
  "object": "whatsapp_business_account",
  "entry": [{
    "id": "102290129340398",
    "changes": [{
      "field": "messages",
      "value": {
        "messaging_product": "whatsapp",
        "metadata": {"display_phone_number": "15550001111", "phone_number_id": "15550001111"},
        "contacts": [{"profile": {"name": "Papa"}, "wa_id": "919812345678"}],
        "messages": [{
          "from": "919812345678",
          "id": "wamid.TEXTMESSAGE1",
          "timestamp": "1603059201",
          "type": "text",
          "text": {"body": "mere liye cab book karo"}
        }]
      }
    }]
  }]
}`

// ---------------------------------------------------------------------------
// GET verification handshake
// ---------------------------------------------------------------------------

func TestWebhookVerify_ValidHandshakeEchoesChallenge(t *testing.T) {
	r := newWebhookRouter(t, whatsapp.NewMemoryDeduper(), &recordingPublisher{})

	req := httptest.NewRequest(http.MethodGet,
		"/webhook?hub.mode=subscribe&hub.verify_token="+testVerifyToken+"&hub.challenge=1158201444", nil)

	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	if got := rec.Body.String(); got != "1158201444" {
		t.Errorf("body = %q, want the bare challenge", got)
	}
}

func TestWebhookVerify_Rejections(t *testing.T) {
	tests := []struct {
		name  string
		query string
		want  int
	}{
		{"wrong token", "?hub.mode=subscribe&hub.verify_token=nope&hub.challenge=123", http.StatusForbidden},
		{"wrong mode", "?hub.mode=unsubscribe&hub.verify_token=" + testVerifyToken + "&hub.challenge=123", http.StatusForbidden},
		{"missing mode", "?hub.verify_token=" + testVerifyToken + "&hub.challenge=123", http.StatusForbidden},
		{"missing challenge", "?hub.mode=subscribe&hub.verify_token=" + testVerifyToken, http.StatusBadRequest},
		{"empty query", "", http.StatusForbidden},
		{"token prefix only", "?hub.mode=subscribe&hub.verify_token=" + testVerifyToken[:4] + "&hub.challenge=123", http.StatusForbidden},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := newWebhookRouter(t, whatsapp.NewMemoryDeduper(), &recordingPublisher{})

			rec := httptest.NewRecorder()
			r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/webhook"+tt.query, nil))

			if rec.Code != tt.want {
				t.Errorf("status = %d, want %d", rec.Code, tt.want)
			}
			if rec.Body.String() == "123" {
				t.Error("challenge echoed on a rejected handshake")
			}
		})
	}
}

// ---------------------------------------------------------------------------
// POST signature verification
// ---------------------------------------------------------------------------

func TestWebhookEvent_ValidAudioAccepted(t *testing.T) {
	deduper := whatsapp.NewMemoryDeduper()
	pub := &recordingPublisher{}
	r := newWebhookRouter(t, deduper, pub)

	rec := postWebhook(t, r, audioPayload, testAppSecret)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d. body: %s", rec.Code, http.StatusOK, rec.Body.String())
	}
	if pub.count() != 1 {
		t.Fatalf("published %d tasks, want 1", pub.count())
	}

	task := pub.first()
	if task.MessageID != testMessageID {
		t.Errorf("MessageID = %q, want %q", task.MessageID, testMessageID)
	}
	if task.Kind != whatsapp.MessageTypeAudio {
		t.Errorf("Kind = %q, want %q", task.Kind, whatsapp.MessageTypeAudio)
	}
	if task.From != testSender {
		t.Errorf("From = %q, want %q", task.From, testSender)
	}
	if task.ProfileName != "Papa" {
		t.Errorf("ProfileName = %q, want %q", task.ProfileName, "Papa")
	}
	if task.MediaID != "MEDIA_ID_1" {
		t.Errorf("MediaID = %q, want %q", task.MediaID, "MEDIA_ID_1")
	}
	if task.Type != jobs.TaskHandleInboundMessage {
		t.Errorf("Type = %q, want %q", task.Type, jobs.TaskHandleInboundMessage)
	}
}

func TestWebhookEvent_ValidTextAccepted(t *testing.T) {
	pub := &recordingPublisher{}
	r := newWebhookRouter(t, whatsapp.NewMemoryDeduper(), pub)

	rec := postWebhook(t, r, textPayload, testAppSecret)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	task := pub.first()
	if task.Kind != whatsapp.MessageTypeText {
		t.Errorf("Kind = %q, want %q", task.Kind, whatsapp.MessageTypeText)
	}
	if task.Text != "mere liye cab book karo" {
		t.Errorf("Text = %q", task.Text)
	}
}

// The single most important test in the project: a retried delivery must not
// produce a second job, or the parent gets two cabs.
func TestWebhookEvent_DuplicateDeliveryEnqueuedOnce(t *testing.T) {
	deduper := whatsapp.NewMemoryDeduper()
	pub := &recordingPublisher{}
	r := newWebhookRouter(t, deduper, pub)

	for i := 0; i < 5; i++ {
		rec := postWebhook(t, r, audioPayload, testAppSecret)
		if rec.Code != http.StatusOK {
			t.Fatalf("attempt %d: status = %d, want 200", i, rec.Code)
		}
	}

	if pub.count() != 1 {
		t.Errorf("published %d tasks after 5 deliveries, want exactly 1", pub.count())
	}
	if deduper.Len() != 1 {
		t.Errorf("deduper holds %d IDs, want 1", deduper.Len())
	}
}

func TestWebhookEvent_DuplicateStillReturns200(t *testing.T) {
	// Returning an error for a duplicate would make Meta retry it forever.
	pub := &recordingPublisher{}
	r := newWebhookRouter(t, whatsapp.NewMemoryDeduper(), pub)

	postWebhook(t, r, textPayload, testAppSecret)
	rec := postWebhook(t, r, textPayload, testAppSecret)

	if rec.Code != http.StatusOK {
		t.Errorf("duplicate status = %d, want 200 so Meta stops retrying", rec.Code)
	}
}

func TestWebhookEvent_BadSignatureRejected(t *testing.T) {
	tests := []struct {
		name   string
		secret string
		want   int
	}{
		{"signed with wrong secret", "attacker-secret", http.StatusForbidden},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pub := &recordingPublisher{}
			r := newWebhookRouter(t, whatsapp.NewMemoryDeduper(), pub)

			rec := postWebhook(t, r, audioPayload, tt.secret)

			if rec.Code != tt.want {
				t.Errorf("status = %d, want %d", rec.Code, tt.want)
			}
			if pub.count() != 0 {
				t.Error("a request with a bad signature reached the publisher")
			}
		})
	}
}

func TestWebhookEvent_TamperedBodyRejected(t *testing.T) {
	pub := &recordingPublisher{}
	r := newWebhookRouter(t, whatsapp.NewMemoryDeduper(), pub)

	// Sign the genuine payload, then send a different body under that
	// signature. This is the classic replay-and-tamper attack.
	req := httptest.NewRequest(http.MethodPost, "/webhook", bytes.NewBufferString(audioPayload))
	req.Header.Set("X-Hub-Signature-256", whatsapp.Sign([]byte(textPayload), testAppSecret))

	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusForbidden)
	}
	if pub.count() != 0 {
		t.Error("body that did not match its signature reached the publisher")
	}
}

func TestWebhookEvent_MissingSignatureRejected(t *testing.T) {
	pub := &recordingPublisher{}
	r := newWebhookRouter(t, whatsapp.NewMemoryDeduper(), pub)

	req := httptest.NewRequest(http.MethodPost, "/webhook", bytes.NewBufferString(audioPayload))
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusForbidden)
	}
	if pub.count() != 0 {
		t.Error("unsigned request reached the publisher")
	}
}

func TestWebhookEvent_MalformedJSONRejected(t *testing.T) {
	tests := []struct {
		name string
		body string
		want int
	}{
		{"truncated json", `{"object":"whatsapp`, http.StatusBadRequest},
		{"not json at all", `hello world`, http.StatusBadRequest},
		{"json array not object", `[1,2,3]`, http.StatusBadRequest},
		{"wrong types", `{"object":123,"entry":"nope"}`, http.StatusBadRequest},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pub := &recordingPublisher{}
			r := newWebhookRouter(t, whatsapp.NewMemoryDeduper(), pub)

			rec := postWebhook(t, r, tt.body, testAppSecret)

			if rec.Code != tt.want {
				t.Errorf("status = %d, want %d", rec.Code, tt.want)
			}
			if pub.count() != 0 {
				t.Error("malformed body reached the publisher")
			}
		})
	}
}

func TestWebhookEvent_EmptyBodyRejected(t *testing.T) {
	pub := &recordingPublisher{}
	r := newWebhookRouter(t, whatsapp.NewMemoryDeduper(), pub)

	rec := postWebhook(t, r, "", testAppSecret)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusBadRequest)
	}
	if pub.count() != 0 {
		t.Error("empty body reached the publisher")
	}
}

func TestWebhookEvent_StatusOnlyDeliveryIsNoOp(t *testing.T) {
	// Meta posts delivery receipts on the same endpoint. They are not inbound
	// messages and must not be enqueued.
	const statusPayload = `{
	  "object": "whatsapp_business_account",
	  "entry": [{
	    "id": "102290129340398",
	    "changes": [{
	      "field": "messages",
	      "value": {
	        "messaging_product": "whatsapp",
	        "metadata": {"display_phone_number": "15550001111", "phone_number_id": "15550001111"},
	        "statuses": [{
	          "id": "wamid.STATUS1",
	          "status": "delivered",
	          "timestamp": "1603059201",
	          "recipient_id": "919812345678"
	        }]
	      }
	    }]
	  }]
	}`

	pub := &recordingPublisher{}
	r := newWebhookRouter(t, whatsapp.NewMemoryDeduper(), pub)

	rec := postWebhook(t, r, statusPayload, testAppSecret)

	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	if pub.count() != 0 {
		t.Errorf("published %d tasks for a status-only delivery, want 0", pub.count())
	}
}

// A dedup store failure must NOT be acknowledged, otherwise the message is
// silently lost forever.
func TestWebhookEvent_DedupFailureReturns500(t *testing.T) {
	pub := &recordingPublisher{}
	deduper := failingDeduper{err: errors.New("database down")}
	r := newWebhookRouter(t, deduper, pub)

	rec := postWebhook(t, r, audioPayload, testAppSecret)

	if rec.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusInternalServerError)
	}
	if pub.count() != 0 {
		t.Error("task published even though dedup failed")
	}
}

// Regression test for the message-loss bug.
//
// When publishing fails, the handler must release its dedup claim. If it does
// not, Meta's retry is rejected as a duplicate and the parent's message is
// never queued, yet Meta has been told 200 and stops retrying. The message is
// then lost forever with no error anywhere.
func TestWebhookEvent_RetryAfterPublishFailureIsNotLost(t *testing.T) {
	deduper := whatsapp.NewMemoryDeduper()
	pub := &recordingPublisher{err: errors.New("redis unavailable")}
	r := newWebhookRouter(t, deduper, pub)

	// First attempt: the queue is down.
	first := postWebhook(t, r, audioPayload, testAppSecret)
	if first.Code != http.StatusInternalServerError {
		t.Fatalf("first attempt status = %d, want 500", first.Code)
	}

	// The queue recovers and Meta retries.
	pub.setError(nil)
	second := postWebhook(t, r, audioPayload, testAppSecret)

	if second.Code != http.StatusOK {
		t.Fatalf("retry status = %d, want 200", second.Code)
	}
	if pub.count() != 1 {
		t.Fatalf("published %d tasks after a retry, want exactly 1. "+
			"A retry swallowed as a duplicate means the message was lost forever.", pub.count())
	}
}

// After a successful delivery, a redelivery must not publish again.
func TestWebhookEvent_CommitMakesRedeliveryADuplicate(t *testing.T) {
	deduper := whatsapp.NewMemoryDeduper()
	pub := &recordingPublisher{}
	r := newWebhookRouter(t, deduper, pub)

	postWebhook(t, r, audioPayload, testAppSecret)
	second := postWebhook(t, r, audioPayload, testAppSecret)

	if second.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", second.Code)
	}
	if pub.count() != 1 {
		t.Errorf("published %d tasks, want 1", pub.count())
	}
}

// A second publish failure after a claim must leave the message re-claimable.
func TestWebhookEvent_TwoPublishFailuresThenSuccess(t *testing.T) {
	deduper := whatsapp.NewMemoryDeduper()
	pub := &recordingPublisher{err: errors.New("redis unavailable")}
	r := newWebhookRouter(t, deduper, pub)

	for i := 0; i < 3; i++ {
		if rec := postWebhook(t, r, audioPayload, testAppSecret); rec.Code != http.StatusInternalServerError {
			t.Fatalf("attempt %d status = %d, want 500", i, rec.Code)
		}
	}

	pub.setError(nil)
	if rec := postWebhook(t, r, audioPayload, testAppSecret); rec.Code != http.StatusOK {
		t.Fatalf("final attempt status = %d, want 200", rec.Code)
	}
	if pub.count() != 1 {
		t.Errorf("published %d tasks, want 1", pub.count())
	}
}

func TestWebhookEvent_PublishFailureReturns500(t *testing.T) {
	pub := &recordingPublisher{err: errors.New("redis unavailable")}
	r := newWebhookRouter(t, whatsapp.NewMemoryDeduper(), pub)

	rec := postWebhook(t, r, audioPayload, testAppSecret)

	if rec.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusInternalServerError)
	}
}

// The webhook route must not be mounted at all when it is not fully wired.
func TestRouter_WebhookNotMountedWhenUnconfigured(t *testing.T) {
	r := httpapi.NewRouter(httpapi.Deps{Log: discardLogger()})

	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/webhook", bytes.NewBufferString(audioPayload)))

	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusNotFound)
	}
}

// Two simultaneous deliveries of the same message must enqueue exactly once.
func TestWebhookEvent_ConcurrentDuplicatesEnqueuedOnce(t *testing.T) {
	deduper := whatsapp.NewMemoryDeduper()
	pub := &recordingPublisher{}
	r := newWebhookRouter(t, deduper, pub)

	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			postWebhook(t, r, audioPayload, testAppSecret)
		}()
	}
	wg.Wait()

	if pub.count() != 1 {
		t.Errorf("published %d tasks from 20 concurrent duplicate deliveries, want 1", pub.count())
	}
}

func TestWebhookEvent_ResponseIsJSON(t *testing.T) {
	r := newWebhookRouter(t, whatsapp.NewMemoryDeduper(), &recordingPublisher{})

	rec := postWebhook(t, r, textPayload, testAppSecret)

	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", ct)
	}

	var body struct {
		Status    string `json:"status"`
		Accepted  int    `json:"accepted"`
		Duplicate int    `json:"duplicates"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if body.Status != "ok" || body.Accepted != 1 || body.Duplicate != 0 {
		t.Errorf("response = %+v, want status ok accepted 1 duplicates 0", body)
	}
}
