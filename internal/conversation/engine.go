package conversation

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

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

// Handle processes one user message and returns a reply.
func (e *Engine) Handle(ctx context.Context, userID string, text string) (Reply, error) {
	var reply Reply
	err := e.store.WithTx(ctx, func(q *store.Queries) error {
		now := e.clock.Now()

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

		cc, err := UnmarshalContext(convo.Context)
		if err != nil {
			return fmt.Errorf("conversation: unmarshal context: %w", err)
		}

		// Timeout check for collecting states.
		if IsTimedOut(State(convo.State), cc.LastPromptAt, now) {
			convo.State = string(StateIdle)
			cc = ConversationContext{}
		}

		state := State(convo.State)

		// Load user for language.
		user, err := q.GetUser(ctx, pgtypeUUID(userID))
		if err != nil {
			return fmt.Errorf("conversation: load user: %w", err)
		}
		lang := replies.FromUserLanguage(user.Language)

		// Load saved places.
		places, err := q.ListSavedPlaces(ctx, pgtypeUUID(userID))
		if err != nil {
			return fmt.Errorf("conversation: load places: %w", err)
		}

		// Extract intent.
		convoCtxJSON, _ := json.Marshal(cc)
		intent, err := e.extractor.Extract(ctx, text, string(convoCtxJSON))
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
				cc.ClarifyAttempts = 0
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
				_, _ = q.AppendAudit(ctx, store.AppendAuditParams{
					Actor:      userID,
					Action:     "clarify_place",
					EntityType: pgtypeText("conversation"),
					EntityID:   pgtypeText(userID),
					Details:    []byte("{}"),
				})
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
				cc.ClarifyAttempts = 0
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
				_, _ = q.AppendAudit(ctx, store.AppendAuditParams{
					Actor:      userID,
					Action:     "clarify_place",
					EntityType: pgtypeText("conversation"),
					EntityID:   pgtypeText(userID),
					Details:    []byte("{}"),
				})
				return nil
			}
		}

		// Initialize next state.
		nextState := state
		replyText := ""

		// Reset clarify attempts on successful resolution.
		if intent.Pickup != "" || intent.Drop != "" {
			cc.ClarifyAttempts = 0
		}

		// Handle confirm from awaiting_confirmation: this is the ONLY path
		// that leads to booking. An explicit "haan" is required.
		if state == StateAwaitingConfirmation && intent.Action == llm.ActionConfirm {
			// Safety check.
			if err := e.safety.Check(ctx, SafetyRequest{
				UserID: userID,
				Pickup: cc.Pickup,
				Drop:   cc.Drop,
			}); err != nil {
				nextState = StateError
				replyText = replies.RenderBookingFailed(lang)
			} else {
				result, err := e.booker.Book(ctx, BookingRequest{
					UserID:         userID,
					Pickup:         cc.Pickup,
					Drop:           cc.Drop,
					IdempotencyKey: fmt.Sprintf("booking:%s:%s:%s", userID, cc.Pickup, cc.Drop),
				})
				if err != nil {
					nextState = StateError
					replyText = replies.RenderBookingFailed(lang)
				} else {
					// Create the order in the database.
					_, dbErr := q.CreateOrder(ctx, store.CreateOrderParams{
						UserID:         pgtypeUUID(userID),
						Type:           "ride",
						Status:         "booked",
						Pickup:         pgtypeText(cc.Pickup),
						DropLocation:   pgtypeText(cc.Drop),
						AmountPaise:    cc.FarePaise,
						Provider:       pgtypeText(result.Provider),
						ProviderRef:    pgtypeText(result.ProviderRef),
						IdempotencyKey: fmt.Sprintf("booking:%s:%s:%s", userID, cc.Pickup, cc.Drop),
					})
					if dbErr != nil && !errors.Is(dbErr, store.ErrDuplicate) {
						nextState = StateError
						replyText = replies.RenderBookingFailed(lang)
					} else {
						nextState = StateActiveRide
						replyText = replies.RenderBooked(lang, result.DeepLink)
					}
				}
			}
		}

		// Handle decline or cancel from awaiting_confirmation.
		if state == StateAwaitingConfirmation && (intent.Action == llm.ActionDecline || intent.Action == llm.ActionCancel) {
			nextState = StateCancelled
			replyText = replies.RenderCancelled(lang)
			cc = ConversationContext{}
		}

		// Handle cancel from any state.
		if intent.Action == llm.ActionCancel && state != StateAwaitingConfirmation {
			nextState = StateCancelled
			replyText = replies.RenderCancelled(lang)
			cc = ConversationContext{}
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
			ns, rt := e.handleBookRideIntent(state, intent, cc, lang)
			nextState = ns
			replyText = rt
		}

		// Transition for book_ride from idle/collecting states (already handled above).

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
		_, _ = q.AppendAudit(ctx, store.AppendAuditParams{
			Actor:      userID,
			Action:     "message_handled",
			EntityType: pgtypeText("conversation"),
			EntityID:   pgtypeText(userID),
			Details:    details,
		})

		reply = Reply{Text: replyText}
		return nil
	})

	return reply, err
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
	_, _ = q.AppendAudit(ctx, store.AppendAuditParams{
		Actor:      userID,
		Action:     action,
		EntityType: pgtypeText("conversation"),
		EntityID:   pgtypeText(userID),
		Details:    details,
	})
	return nil
}
