package httpapi

import (
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/ankitnegi-dev/Sahayak/internal/jobs"
	"github.com/ankitnegi-dev/Sahayak/internal/whatsapp"
)

// maxWebhookBody caps the webhook request body. Meta payloads are a few KB, so
// a hard limit means a hostile sender cannot exhaust memory.
const maxWebhookBody = 1 << 20 // 1 MiB

// WebhookDeps are the collaborators the webhook handlers need.
//
// Every dependency is an interface declared in the package that uses it. The
// handlers do not know whether dedup lives in memory or Postgres, nor how jobs
// are queued. That is what makes them testable with no infrastructure.
type WebhookDeps struct {
	// Log receives structured events for every delivery.
	Log *slog.Logger

	// VerifyToken is the token chosen when registering the webhook with Meta.
	// Meta echoes it back during the GET handshake.
	VerifyToken string

	// AppSecret verifies the X-Hub-Signature-256 header. This is the App
	// Secret from the dashboard, NOT the access token.
	AppSecret string

	// Deduper provides idempotency.
	Deduper whatsapp.Deduper

	// Publisher enqueues work for the worker.
	Publisher jobs.Publisher
}

// mountWebhook registers the GET verification route and the POST event route.
func mountWebhook(r chiRouter, deps WebhookDeps) {
	r.Get("/webhook", handleWebhookVerify(deps))
	r.Post("/webhook", handleWebhookEvent(deps))
}

// chiRouter is the subset of chi's router this package needs. Declaring it
// locally keeps mountWebhook testable without constructing a real chi.Mux.
type chiRouter interface {
	Get(pattern string, h http.HandlerFunc)
	Post(pattern string, h http.HandlerFunc)
}

// handleWebhookVerify implements Meta's GET verification handshake.
//
// Meta sends hub.mode, hub.verify_token and hub.challenge. We echo the
// challenge back, but only when the mode is "subscribe" and the token matches
// ours exactly. Echoing the challenge unconditionally would let anyone confirm
// that a domain is pointed at this service.
func handleWebhookVerify(deps WebhookDeps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()

		mode := q.Get("hub.mode")
		token := q.Get("hub.verify_token")
		challenge := q.Get("hub.challenge")

		if mode != "subscribe" {
			http.Error(w, "invalid hub.mode", http.StatusForbidden)
			return
		}

		if deps.VerifyToken == "" || token != deps.VerifyToken {
			http.Error(w, "invalid verify token", http.StatusForbidden)
			return
		}

		if challenge == "" {
			http.Error(w, "missing hub.challenge", http.StatusBadRequest)
			return
		}

		// Meta expects the bare challenge as the body, not JSON.
		w.Header().Set("Content-Type", "text/plain")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, challenge)
	}
}

// handleWebhookEvent handles Meta's POST delivery of an inbound message.
//
// The order of operations is the most important part of Phase 2:
//
//  1. Read the RAW body. Signature verification must run on the exact bytes
//     Meta sent, before any JSON decoding.
//  2. Verify the signature. An unverified request never reaches business logic.
//  3. Decode and normalise.
//  4. Claim the message ID for idempotency.
//  5. Enqueue.
//  6. Commit the claim, marking the message durably handled.
//  7. Return 200.
//
// Response codes are chosen deliberately. A verified request, including a
// duplicate, returns 200: returning an error for a duplicate would make Meta
// retry it forever. A forged request returns 403: returning 500 would make Meta
// retry something that can never succeed. A temporary internal failure returns
// 500 so Meta retries, and the claim protocol ensures the retry is processed
// rather than swallowed.
func handleWebhookEvent(deps WebhookDeps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		log := deps.Log
		if log == nil {
			log = slog.Default()
		}

		if deps.Deduper == nil || deps.Publisher == nil {
			log.ErrorContext(ctx, "webhook: not configured")
			http.Error(w, "webhook not configured", http.StatusServiceUnavailable)
			return
		}

		body, err := io.ReadAll(io.LimitReader(r.Body, maxWebhookBody))
		if err != nil {
			log.WarnContext(ctx, "webhook: read body failed", "error", err)
			http.Error(w, "cannot read body", http.StatusBadRequest)
			return
		}

		// Verify before decoding. Everything below trusts this check.
		if err := whatsapp.VerifySignature(body, r.Header.Get("X-Hub-Signature-256"), deps.AppSecret); err != nil {
			log.WarnContext(ctx, "webhook: signature rejected",
				"error", err,
				"request_id", RequestIDFrom(ctx),
			)
			http.Error(w, "invalid signature", http.StatusForbidden)
			return
		}

		if len(body) == 0 {
			http.Error(w, "empty body", http.StatusBadRequest)
			return
		}

		var payload whatsapp.WebhookPayload
		if err := json.Unmarshal(body, &payload); err != nil {
			log.WarnContext(ctx, "webhook: decode failed",
				"error", err,
				"request_id", RequestIDFrom(ctx),
			)
			http.Error(w, "malformed payload", http.StatusBadRequest)
			return
		}

		messages, err := whatsapp.Normalize(payload)
		if err != nil {
			log.WarnContext(ctx, "webhook: normalize failed",
				"error", err,
				"request_id", RequestIDFrom(ctx),
			)
			http.Error(w, "malformed payload", http.StatusBadRequest)
			return
		}

		accepted, duplicates := 0, 0
		for _, msg := range messages {
			// Claim BEFORE enqueue. Claiming first means a redelivery that
			// arrives while this handler is still running is rejected rather
			// than producing a second job.
			err := deps.Deduper.Claim(ctx, msg.ID, time.Now().UTC())
			if errors.Is(err, whatsapp.ErrDuplicateMessage) {
				duplicates++
				log.InfoContext(ctx, "webhook: duplicate ignored",
					"message_id", msg.ID,
					"from", maskPhone(msg.From),
				)
				continue
			}
			if err != nil {
				// The dedup store is unavailable. Do NOT ack: a 200 tells Meta
				// the message is handled. A 500 makes Meta retry, which is
				// the safe outcome once the store recovers.
				log.ErrorContext(ctx, "webhook: claim failed",
					"error", err,
					"message_id", msg.ID,
				)
				http.Error(w, "cannot record message", http.StatusInternalServerError)
				return
			}

			if err := deps.Publisher.Publish(ctx, jobs.FromWhatsApp(msg)); err != nil {
				// Abort is essential. Without it the claim stays pending,
				// Meta's retry is rejected as a duplicate, and the parent's
				// message is lost permanently. Releasing the claim lets the
				// retry proceed once the queue recovers.
				if abortErr := deps.Deduper.Abort(ctx, msg.ID); abortErr != nil {
					log.ErrorContext(ctx, "webhook: abort failed, message may be lost",
						"error", abortErr,
						"message_id", msg.ID,
					)
				}
				log.ErrorContext(ctx, "webhook: publish failed",
					"error", err,
					"message_id", msg.ID,
				)
				http.Error(w, "cannot enqueue message", http.StatusInternalServerError)
				return
			}

			// Commit makes the claim durable. Only after this does a further
			// delivery count as a real duplicate.
			if err := deps.Deduper.Commit(ctx, msg.ID); err != nil {
				// The message is safely queued. A commit failure means a retry
				// may publish a second job, which the worker must also guard
				// against on message ID. Duplication is recoverable. Message
				// loss is not, so log loudly and carry on.
				log.ErrorContext(ctx, "webhook: commit failed, job is queued but unmarked",
					"error", err,
					"message_id", msg.ID,
				)
			}

			accepted++
			log.InfoContext(ctx, "webhook: message accepted",
				"message_id", msg.ID,
				"type", msg.Type,
				"from", maskPhone(msg.From),
			)
		}

		log.InfoContext(ctx, "webhook: delivery handled",
			"accepted", accepted,
			"duplicates", duplicates,
			"request_id", RequestIDFrom(ctx),
		)

		writeJSONStatus(w, http.StatusOK, map[string]any{
			"status":     "ok",
			"accepted":   accepted,
			"duplicates": duplicates,
		})
	}
}

// writeJSONStatus writes a JSON body with an explicit status code.
//
// The status must be set before the first write, otherwise Go implicitly sends
// 200 and ignores a later WriteHeader.
func writeJSONStatus(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

// maskPhone hides the middle of a phone number so logs stay DPDP-friendly.
func maskPhone(phone string) string {
	if phone == "" {
		return ""
	}
	runes := []rune(phone)
	if len(runes) < 4 {
		return "****"
	}
	return string(runes[:2]) + strings.Repeat("*", len(runes)-4) + string(runes[len(runes)-2:])
}
