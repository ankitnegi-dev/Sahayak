package conversation

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/ankitnegi-dev/Sahayak/internal/conversation/replies"
	"github.com/ankitnegi-dev/Sahayak/internal/llm"
	"github.com/ankitnegi-dev/Sahayak/internal/store"
	"github.com/jackc/pgx/v5"
)

// Reply is what the engine returns to the caller.
type Reply struct {
	Text string
}

// Engine drives a conversation. It is the single entry point for handling a
// user message.
type Engine struct {
	store     *store.Store
	extractor llm.Extractor
	booker    Booker
	safety    SafetyChecker
	clock     Clock
}

// NewEngine returns an Engine with the given collaborators.
func NewEngine(s *store.Store, extractor llm.Extractor, booker Booker, safety SafetyChecker, clock Clock) *Engine {
	if clock == nil {
		clock = RealClock{}
	}
	if safety == nil {
		safety = PermissiveSafety{}
	}
	return &Engine{
		store:     s,
		extractor: extractor,
		booker:    booker,
		safety:    safety,
		clock:     clock,
	}
}

// newRequestID generates a random 16-byte hex string for use as a request ID.
func newRequestID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		// crypto/rand does not fail on supported platforms.
		return hex.EncodeToString([]byte(fmt.Sprintf("%d", time.Now().UnixNano())))
	}
	return hex.EncodeToString(b[:])
}

// Handle processes one user message and returns a reply.
func (e *Engine) Handle(ctx context.Context, userID string, text string) (Reply, error) {
	now := e.clock.Now()

	// Phase 1: single transaction for state machine + place resolution + intent.
	var reply Reply
	var nextState State
	var replyText string
	var cc ConversationContext
	var intent llm.Intent
	var lang replies.Language
	var state State
	var needsBooking bool
	var bookingDeepLink string

	err := e.store.WithTx(ctx, func(q *store.Queries) error {
		// Load or create conversation.
		convo, err := q.GetConversationForUpdate(ctx, pgtypeUUID(userID))
		if err != nil {
			if !errors.Is(err, pgx.ErrNoRows) {
				return fmt.Errorf("conversation: load: %w", err)
			}
			convo, err = q.CreateConversation(ctx, pgtypeUUID(userID))
			if err != nil {
				return fmt.Errorf("conversation: create: %w", err)
			}
		}

		cc, err = UnmarshalContext(convo.Context)
		if err != nil {
			return fmt.Errorf("conversation: unmarshal context: %w", err)
		}

		// Timeout check for collecting states.
		if IsTimedOut(State(convo.State), cc.LastPromptAt, now) {
			convo.State = string(StateIdle)
			cc = ConversationContext{}
		}

		// Booking timeout: if a booking has been pending too long, move to idle
		// for reconciliation. The order stays in its current state (confirmed
		// or booked) and Phase 9 reconciliation will handle it.
		if State(convo.State) == StateBooking && !cc.LastPromptAt.IsZero() && now.Sub(cc.LastPromptAt) > BookingTimeout {
			convo.State = string(StateIdle)
			if _, auditErr := q.AppendAudit(ctx, store.AppendAuditParams{
				Actor:      userID,
				Action:     "booking_timeout_needs_reconcile",
				EntityType: pgtypeText("conversation"),
				EntityID:   pgtypeText(userID),
				Details:    []byte("{}"),
			}); auditErr != nil {
				return fmt.Errorf("append audit: %w", auditErr)
			}
			cc = ConversationContext{}
		}

		state = State(convo.State)

		// Load user for language.
		user, err := q.GetUser(ctx, pgtypeUUID(userID))
		if err != nil {
			return fmt.Errorf("conversation: load user: %w", err)
		}
		lang = replies.FromUserLanguage(user.Language)

		// Load saved places.
		places, err := q.ListSavedPlaces(ctx, pgtypeUUID(userID))
		if err != nil {
			return fmt.Errorf("conversation: load places: %w", err)
		}

		// Extract intent.
		convoCtxJSON, _ := json.Marshal(cc)
		intent, err = e.extractor.Extract(ctx, text, string(convoCtxJSON))
		if err != nil {
			return fmt.Errorf("conversation: extract intent: %w", err)
		}
		if !isValidIntent(intent) {
			intent = llm.Intent{Action: llm.ActionUnknown, Confidence: 0}
		}

		// Resolve places from intent into context.
		if intent.Pickup != "" {
			if p, err := ResolvePlace(intent.Pickup, places); err == nil {
				if p.Address != "" {
					cc.Pickup = p.Address
				} else {
					cc.Pickup = p.Label
				}
				cc.PickupPlaceID = uuidToString(p.ID)
			} else {
				cc.ClarifyAttempts++
				if cc.ClarifyAttempts >= 3 {
					reply = Reply{Text: replies.RenderTooManyAttempts(lang)}
					cc = ConversationContext{}
					convo.State = string(StateIdle)
					cc.LastPromptAt = now
					return e.persist(ctx, q, userID, convo, cc, "clarification_exhausted", intent, StateIdle)
				}
				reply = Reply{Text: replies.RenderClarifyPlace(lang, intent.Pickup)}
				convo.State = string(state)
				cc.LastPromptAt = now
				convo.Context, _ = cc.Marshal()
				_, err = q.UpsertConversation(ctx, store.UpsertConversationParams{
					UserID:  pgtypeUUID(userID),
					State:   convo.State,
					Context: convo.Context,
				})
				if err != nil {
					return fmt.Errorf("conversation: persist: %w", err)
				}
				if _, auditErr := q.AppendAudit(ctx, store.AppendAuditParams{
					Actor:      userID,
					Action:     "clarify_place",
					EntityType: pgtypeText("conversation"),
					EntityID:   pgtypeText(userID),
					Details:    []byte("{}"),
				}); auditErr != nil {
					return fmt.Errorf("append audit: %w", auditErr)
				}
				return nil
			}
		}

		if intent.Drop != "" {
			if p, err := ResolvePlace(intent.Drop, places); err == nil {
				if p.Address != "" {
					cc.Drop = p.Address
				} else {
					cc.Drop = p.Label
				}
				cc.DropPlaceID = uuidToString(p.ID)
			} else {
				cc.ClarifyAttempts++
				if cc.ClarifyAttempts >= 3 {
					reply = Reply{Text: replies.RenderTooManyAttempts(lang)}
					cc = ConversationContext{}
					convo.State = string(StateIdle)
					cc.LastPromptAt = now
					return e.persist(ctx, q, userID, convo, cc, "clarification_exhausted", intent, StateIdle)
				}
				reply = Reply{Text: replies.RenderClarifyPlace(lang, intent.Drop)}
				convo.State = string(state)
				cc.LastPromptAt = now
				convo.Context, _ = cc.Marshal()
				_, err = q.UpsertConversation(ctx, store.UpsertConversationParams{
					UserID:  pgtypeUUID(userID),
					State:   convo.State,
					Context: convo.Context,
				})
				if err != nil {
					return fmt.Errorf("conversation: persist: %w", err)
				}
				if _, auditErr := q.AppendAudit(ctx, store.AppendAuditParams{
					Actor:      userID,
					Action:     "clarify_place",
					EntityType: pgtypeText("conversation"),
					EntityID:   pgtypeText(userID),
					Details:    []byte("{}"),
				}); auditErr != nil {
					return fmt.Errorf("append audit: %w", auditErr)
				}
				return nil
			}
		}

		// Reset clarify attempts on successful resolution.
		if intent.Pickup != "" || intent.Drop != "" {
			cc.ClarifyAttempts = 0
		}

		// Determine next state and reply.
		nextState = state
		replyText = ""

		// "haan" while booking: booking in progress.
		if state == StateBooking && intent.Action == llm.ActionConfirm {
			nextState = StateBooking
			replyText = replies.RenderBookingInProgress(lang)
		}

		// From error state, next message starts fresh.
		if state == StateError {
			state = StateIdle
			cc = ConversationContext{}
		}

		// Handle confirm from awaiting_confirmation: this is the ONLY path
		// that leads to booking. An explicit "haan" is required.
		if state == StateAwaitingConfirmation && intent.Action == llm.ActionConfirm && replyText == "" {
			// Safety check.
			if err := e.safety.Check(ctx, SafetyRequest{
				UserID: userID,
				Pickup: cc.Pickup,
				Drop:   cc.Drop,
			}); err != nil {
				nextState = StateError
				replyText = replies.RenderBookingFailed(lang)
				cc.RequestID = ""
			} else {
				// Generate request ID if not already set.
				if cc.RequestID == "" {
					cc.RequestID = newRequestID()
				}
				idempotencyKey := "booking:" + cc.RequestID

				// Create order (confirmed) in this transaction.
				_, dbErr := q.CreateOrder(ctx, store.CreateOrderParams{
					UserID:         pgtypeUUID(userID),
					Type:           "ride",
					Status:         "confirmed",
					Pickup:         pgtypeText(cc.Pickup),
					DropLocation:   pgtypeText(cc.Drop),
					AmountPaise:    cc.FarePaise,
					IdempotencyKey: idempotencyKey,
				})
				if dbErr != nil && !errors.Is(dbErr, store.ErrDuplicate) {
					nextState = StateError
					replyText = replies.RenderBookingFailed(lang)
					cc.RequestID = ""
				} else if errors.Is(dbErr, store.ErrDuplicate) {
					// Already booked for this request. Look up the existing order
					// and respond from its real status.
					existingOrder, lookupErr := q.GetOrderByUserAndIdempotencyKey(ctx, store.GetOrderByUserAndIdempotencyKeyParams{
						UserID:         pgtypeUUID(userID),
						IdempotencyKey: idempotencyKey,
					})
					if lookupErr == nil {
						switch existingOrder.Status {
						case "booked", "completed":
							nextState = StateActiveRide
							replyText = replies.RenderBooked(lang, "https://example.com/ride")
						case "failed":
							nextState = StateError
							replyText = replies.RenderBookingFailed(lang)
						default:
							nextState = StateBooking
							replyText = replies.RenderBookingInProgress(lang)
						}
					} else {
						nextState = StateError
						replyText = replies.RenderBookingFailed(lang)
					}
					cc.RequestID = ""
				} else {
					// Order created. Booking will happen in phase 2.
					nextState = StateBooking
					needsBooking = true
					replyText = "" // will be set after booking
				}
			}
		}

		// Handle decline from awaiting_confirmation.
		if state == StateAwaitingConfirmation && intent.Action == llm.ActionDecline {
			nextState = StateCancelled
			replyText = replies.RenderCancelled(lang)
			cc = ConversationContext{}
		}

		// Handle cancel. Only a plain reset in idle, awaiting_pickup,
		// awaiting_drop and awaiting_confirmation. In booking and
		// active_ride, cancel does NOT reset the conversation.
		if intent.Action == llm.ActionCancel {
			if state == StateBooking || state == StateActiveRide {
				// Do not reset. Reply honestly.
				if state == StateBooking {
					replyText = replies.RenderCancelDuringBooking(lang)
				} else {
					replyText = replies.RenderCancelDuringRide(lang)
				}
				nextState = state
				// Append audit row.
				if _, auditErr := q.AppendAudit(ctx, store.AppendAuditParams{
					Actor:      userID,
					Action:     "cancel_requested_during_booking",
					EntityType: pgtypeText("conversation"),
					EntityID:   pgtypeText(userID),
					Details:    []byte("{}"),
				}); auditErr != nil {
					return fmt.Errorf("append audit: %w", auditErr)
				}
				// Only call Booker.Cancel from active_ride.
				if state == StateActiveRide && e.booker != nil {
					_ = e.booker.Cancel(ctx, "")
				}
			} else {
				nextState = StateCancelled
				replyText = replies.RenderCancelled(lang)
				cc = ConversationContext{}
				cc.RequestID = ""
			}
		}

		// Handle confirm with nothing pending.
		if intent.Action == llm.ActionConfirm && state != StateAwaitingConfirmation {
			nextState = state
			replyText = replies.RenderNoPendingBooking(lang)
		}

		// Handle help.
		if intent.Action == llm.ActionHelp {
			nextState = state
			replyText = replies.RenderHelp(lang)
		}

		// Handle unknown.
		if intent.Action == llm.ActionUnknown && replyText == "" {
			nextState = state
			replyText = replies.RenderUnknown(lang)
		}

		// Handle book_ride intent (collecting places).
		if intent.Action == llm.ActionBookRide && replyText == "" {
			// A new booking from an active or terminal state starts fresh.
			if state == StateActiveRide || state == StateCompleted || state == StateCancelled || state == StateError {
				oldPickup := cc.Pickup
				oldDrop := cc.Drop
				cc = ConversationContext{}
				cc.Pickup = oldPickup
				cc.Drop = oldDrop
				state = StateIdle
			}
			ns, rt := e.handleBookRideIntent(state, intent, cc, lang)
			nextState = ns
			replyText = rt
			// Generate request ID when entering awaiting_confirmation.
			if nextState == StateAwaitingConfirmation && cc.RequestID == "" {
				cc.RequestID = newRequestID()
			}
		}

		// Clear request ID on terminal/reset paths.
		if nextState == StateCancelled || nextState == StateError || nextState == StateCompleted {
			cc.RequestID = ""
		}
		if nextState == StateIdle {
			cc.RequestID = ""
		}

		convo.State = string(nextState)
		cc.LastPromptAt = now
		convo.Context, _ = cc.Marshal()

		_, err = q.UpsertConversation(ctx, store.UpsertConversationParams{
			UserID:  pgtypeUUID(userID),
			State:   convo.State,
			Context: convo.Context,
		})
		if err != nil {
			return fmt.Errorf("conversation: persist: %w", err)
		}

		details, _ := json.Marshal(map[string]any{
			"intent": string(intent.Action),
			"state":  nextState,
		})
		if _, auditErr := q.AppendAudit(ctx, store.AppendAuditParams{
			Actor:      userID,
			Action:     "message_handled",
			EntityType: pgtypeText("conversation"),
			EntityID:   pgtypeText(userID),
			Details:    details,
		}); auditErr != nil {
			return fmt.Errorf("append audit: %w", auditErr)
		}

		reply = Reply{Text: replyText}
		return nil
	})

	if err != nil {
		return Reply{}, err
	}

	// Phase 2: booking (outside transaction).
	if needsBooking {
		idempotencyKey := "booking:" + cc.RequestID

		// Call Booker with timeout.
		bookCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
		defer cancel()

		result, err := e.booker.Book(bookCtx, BookingRequest{
			UserID:         userID,
			Pickup:         cc.Pickup,
			Drop:           cc.Drop,
			IdempotencyKey: idempotencyKey,
		})

		if err != nil {
			// Determine if this is a definite rejection or ambiguous.
			isRejected := errors.Is(err, ErrBookingRejected)

			if isRejected {
				// Definite failure: mark order as failed.
				err2 := e.store.WithTx(ctx, func(q *store.Queries) error {
					order, err := q.GetOrderByUserAndIdempotencyKey(ctx, store.GetOrderByUserAndIdempotencyKeyParams{
						UserID:         pgtypeUUID(userID),
						IdempotencyKey: idempotencyKey,
					})
					if err != nil {
						return err
					}
					if err := q.UpdateOrderStatus(ctx, store.UpdateOrderStatusParams{
						ID:     order.ID,
						Status: "failed",
					}); err != nil {
						return err
					}
					_, auditErr := q.AppendAudit(ctx, store.AppendAuditParams{
						Actor:      userID,
						Action:     "booking_failed",
						EntityType: pgtypeText("order"),
						EntityID:   pgtypeText(uuidToString(order.ID)),
						Details:    []byte("{}"),
					})
					if auditErr != nil {
						return fmt.Errorf("append audit: %w", auditErr)
					}
					// Update conversation state to error.
					convo, err := q.GetConversationForUpdate(ctx, pgtypeUUID(userID))
					if err != nil {
						return err
					}
					convo.State = string(StateError)
					cc.RequestID = ""
					convo.Context, _ = cc.Marshal()
					_, err = q.UpsertConversation(ctx, store.UpsertConversationParams{
						UserID:  pgtypeUUID(userID),
						State:   convo.State,
						Context: convo.Context,
					})
					return err
				})
				if err2 != nil {
					// Log but don't fail the request.
					slog.Error("mark failed transaction", "error", err2, "user_id", userID)
				}
				reply = Reply{Text: replies.RenderBookingFailed(lang)}
			} else {
				// Ambiguous (timeout, network, unknown): order stays confirmed.
				// Audit "booking_outcome_unknown".
				err2 := e.store.WithTx(ctx, func(q *store.Queries) error {
					order, err := q.GetOrderByUserAndIdempotencyKey(ctx, store.GetOrderByUserAndIdempotencyKeyParams{
						UserID:         pgtypeUUID(userID),
						IdempotencyKey: idempotencyKey,
					})
					if err != nil {
						return err
					}
					_, auditErr := q.AppendAudit(ctx, store.AppendAuditParams{
						Actor:      userID,
						Action:     "booking_outcome_unknown",
						EntityType: pgtypeText("order"),
						EntityID:   pgtypeText(uuidToString(order.ID)),
						Details:    []byte("{}"),
					})
					if auditErr != nil {
						return fmt.Errorf("append audit: %w", auditErr)
					}
					// State stays booking.
					return nil
				})
				if err2 != nil {
					slog.Error("audit unknown outcome", "error", err2, "user_id", userID)
				}
				reply = Reply{Text: replies.RenderBookingBeingChecked(lang)}
			}
		} else {
			// Success: update order to booked with retries.
			bookingDeepLink = result.DeepLink
			var updateErr error
			for attempt := 0; attempt < 3; attempt++ {
				if attempt > 0 {
					time.Sleep(time.Duration(attempt) * 100 * time.Millisecond)
				}
				updateErr = e.store.WithTx(ctx, func(q *store.Queries) error {
					order, err := q.GetOrderByUserAndIdempotencyKey(ctx, store.GetOrderByUserAndIdempotencyKeyParams{
						UserID:         pgtypeUUID(userID),
						IdempotencyKey: idempotencyKey,
					})
					if err != nil {
						return err
					}
					if err := q.UpdateOrderStatus(ctx, store.UpdateOrderStatusParams{
						ID:     order.ID,
						Status: "booked",
					}); err != nil {
						return err
					}
					_, auditErr := q.AppendAudit(ctx, store.AppendAuditParams{
						Actor:      userID,
						Action:     "booking_succeeded",
						EntityType: pgtypeText("order"),
						EntityID:   pgtypeText(uuidToString(order.ID)),
						Details:    []byte("{}"),
					})
					if auditErr != nil {
						return fmt.Errorf("append audit: %w", auditErr)
					}
					// Update conversation state to active_ride.
					convo, err := q.GetConversationForUpdate(ctx, pgtypeUUID(userID))
					if err != nil {
						return err
					}
					convo.State = string(StateActiveRide)
					cc.RequestID = ""
					convo.Context, _ = cc.Marshal()
					_, err = q.UpsertConversation(ctx, store.UpsertConversationParams{
						UserID:  pgtypeUUID(userID),
						State:   convo.State,
						Context: convo.Context,
					})
					return err
				})
				if updateErr == nil {
					break
				}
			}
			if updateErr != nil {
				// Failed after retries: log at error level, leave order confirmed.
				slog.Error("mark booked failed after retries", "error", updateErr, "user_id", userID)
				// Add booking timeout check later.
			}
			reply = Reply{Text: replies.RenderBooked(lang, bookingDeepLink)}
		}
	}

	if reply.Text == "" {
		reply.Text = replyText
	}

	return reply, nil
}

func (e *Engine) handleBookRideIntent(state State, intent llm.Intent, cc ConversationContext, lang replies.Language) (State, string) {
	switch state {
	case StateIdle, StateAwaitingPickup, StateAwaitingDrop:
		if cc.Pickup != "" && cc.Drop != "" {
			return StateAwaitingConfirmation, replies.RenderConfirm(lang, cc.Pickup, cc.Drop, cc.FarePaise)
		}
		if cc.Pickup != "" {
			return StateAwaitingDrop, replies.RenderAskDrop(lang)
		}
		return StateAwaitingPickup, replies.RenderAskPickup(lang)
	case StateAwaitingConfirmation:
		if cc.Pickup != "" && cc.Drop != "" {
			return StateAwaitingConfirmation, replies.RenderConfirm(lang, cc.Pickup, cc.Drop, cc.FarePaise)
		}
		return StateAwaitingConfirmation, replies.RenderUnknown(lang)
	default:
		return state, replies.RenderUnknown(lang)
	}
}

func isValidIntent(intent llm.Intent) bool {
	switch intent.Action {
	case llm.ActionBookRide, llm.ActionCancel, llm.ActionConfirm,
		llm.ActionDecline, llm.ActionHelp, llm.ActionUnknown:
	default:
		return false
	}
	if intent.Confidence < 0 || intent.Confidence > 1 {
		return false
	}
	return true
}

func (e *Engine) persist(ctx context.Context, q *store.Queries, userID string, convo store.Conversation, cc ConversationContext, action string, intent llm.Intent, state State) error {
	convo.State = string(state)
	cc.LastPromptAt = e.clock.Now()
	convo.Context, _ = cc.Marshal()
	_, err := q.UpsertConversation(ctx, store.UpsertConversationParams{
		UserID:  pgtypeUUID(userID),
		State:   convo.State,
		Context: convo.Context,
	})
	if err != nil {
		return fmt.Errorf("conversation: persist: %w", err)
	}
	details, _ := json.Marshal(map[string]any{
		"intent": string(intent.Action),
		"state":  state,
	})
	if _, auditErr := q.AppendAudit(ctx, store.AppendAuditParams{
		Actor:      userID,
		Action:     action,
		EntityType: pgtypeText("conversation"),
		EntityID:   pgtypeText(userID),
		Details:    details,
	}); auditErr != nil {
		return fmt.Errorf("append audit: %w", auditErr)
	}
	return nil
}
