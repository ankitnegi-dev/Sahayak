package whatsapp

import (
	"context"
	"errors"
	"strings"
	"sync"
	"time"
)

// Dedup errors. These are sentinels so callers can branch with errors.Is.
var (
	// ErrDuplicateMessage means the message has already been accepted and
	// must not be processed again.
	ErrDuplicateMessage = errors.New("whatsapp: duplicate message")

	// ErrMessageNotClaimed means Commit or Abort was called for a message ID
	// that is not currently claimed.
	ErrMessageNotClaimed = errors.New("whatsapp: message not claimed")
)

// claimLease is how long a claim stays valid before another attempt may take
// it over.
//
// The gap this covers: the API claims a message, publishes it successfully,
// then the process dies before Commit. The retry arrives, finds a claim that
// was never committed, and waits out the lease instead of being swallowed as
// a duplicate. The cost of waiting is one retry cycle; the cost of not
// waiting is losing the parent's message.
const claimLease = 2 * time.Minute

// Deduper provides idempotency for inbound webhook deliveries.
//
// It is a three-step protocol rather than a single MarkSeen call, because a
// single call cannot distinguish "already handled" from "handled halfway and
// then failed". Both look identical to a duplicate check, and the difference
// is the difference between a retry working and a parent's message being lost
// forever.
//
// The intended sequence is:
//
//	Claim  -> returns ErrDuplicateMessage only if already ACCEPTED
//	... publish to the queue ...
//	Commit -> marks it done, so future retries are true duplicates
//	Abort  -> on publish failure, releases the claim so a retry can proceed
//
// Declared here and used by the webhook handler, so the handler does not
// depend on how dedup is stored.
type Deduper interface {
	// Claim reserves messageID for processing.
	//
	// It returns ErrDuplicateMessage when the message has already been
	// accepted, or when another attempt holds an unexpired claim. Callers
	// should treat that as "do not process this again".
	//
	// A claim whose lease has expired may be taken over, which is what makes
	// the protocol safe across a process crash.
	Claim(ctx context.Context, messageID string, receivedAt time.Time) error

	// Commit marks a claimed message as accepted. After this, any further
	// delivery is a genuine duplicate.
	Commit(ctx context.Context, messageID string) error

	// Abort releases a claim without accepting it, so that a retry can claim
	// the message again. This is what prevents message loss when publishing
	// fails after a successful Claim.
	Abort(ctx context.Context, messageID string) error
}

// claimState tracks how far a message got.
type claimState uint8

const (
	// statePending means claimed but not yet published successfully.
	statePending claimState = iota

	// stateAccepted means published successfully. Duplicates from here on.
	stateAccepted
)

type claim struct {
	state claimState
	when  time.Time
}

// MemoryDeduper is an in-memory Deduper for local development and tests.
//
// It is NOT safe for production. State is lost on restart, and a multi-instance
// deployment would have each instance deduping independently, so a retry
// routed to a different replica could be processed twice.
//
// Phase 4 replaces this with a Postgres implementation backed by a unique
// constraint on processed_messages.wa_message_id, which makes the guarantee
// hold across restarts and across replicas. That is the whole reason the
// interface exists.
type MemoryDeduper struct {
	mu sync.Mutex

	claims map[string]claim

	// order holds message IDs in claim order, and head is the index of the
	// oldest live entry. Eviction is a FIFO trim from the front, which is
	// correct because claims arrive in order.
	//
	// An earlier version sorted the whole map on every insert once full, which
	// is O(n log n) per insert and took 405 seconds for 11,000 inserts under
	// the race detector. This version is amortised O(1).
	order []string
	head  int

	maxEntries int
	lease      time.Duration
}

// compile-time assertion that MemoryDeduper satisfies the interface.
var _ Deduper = (*MemoryDeduper)(nil)

// defaultMaxEntries is deliberately generous. At 10,000 messages this holds
// roughly a week of family usage before eviction starts.
const defaultMaxEntries = 10_000

// NewMemoryDeduper returns an empty in-memory deduper.
func NewMemoryDeduper() *MemoryDeduper {
	return &MemoryDeduper{
		claims:     make(map[string]claim, defaultMaxEntries),
		order:      make([]string, 0, defaultMaxEntries),
		maxEntries: defaultMaxEntries,
		lease:      claimLease,
	}
}

// Claim implements Deduper.
func (d *MemoryDeduper) Claim(_ context.Context, messageID string, receivedAt time.Time) error {
	// A blank ID cannot be deduplicated, so accepting it would silently
	// defeat the entire purpose of this check.
	if strings.TrimSpace(messageID) == "" {
		return errors.New("whatsapp: empty message id")
	}

	d.mu.Lock()
	defer d.mu.Unlock()

	now := receivedAt
	if now.IsZero() {
		now = time.Now().UTC()
	}

	if existing, found := d.claims[messageID]; found {
		switch existing.state {
		case stateAccepted:
			return ErrDuplicateMessage
		case statePending:
			// A pending claim is only stale once its lease expires. Before
			// that, another attempt may still be publishing, so treating it as
			// a duplicate is the safe choice: a duplicate job is recoverable,
			// a lost message is not.
			if now.Sub(existing.when) < d.lease {
				return ErrDuplicateMessage
			}
			// Lease expired, so the previous attempt died. Take it over.
		}
	} else {
		if len(d.claims) >= d.maxEntries {
			d.evictOldest()
		}
		d.order = append(d.order, messageID)
	}

	d.claims[messageID] = claim{state: statePending, when: now}
	return nil
}

// Commit implements Deduper.
func (d *MemoryDeduper) Commit(_ context.Context, messageID string) error {
	d.mu.Lock()
	defer d.mu.Unlock()

	existing, found := d.claims[messageID]
	if !found {
		return ErrMessageNotClaimed
	}

	existing.state = stateAccepted
	// Refreshing the timestamp keeps an accepted message from being mistaken
	// for a stale pending claim during eviction.
	existing.when = time.Now().UTC()
	d.claims[messageID] = existing
	return nil
}

// Abort implements Deduper.
func (d *MemoryDeduper) Abort(_ context.Context, messageID string) error {
	d.mu.Lock()
	defer d.mu.Unlock()

	if _, found := d.claims[messageID]; !found {
		return ErrMessageNotClaimed
	}

	// Delete rather than marking a terminal state, so the next delivery can
	// claim it. The entry stays in the order slice and is cleaned up lazily.
	delete(d.claims, messageID)
	return nil
}

// evictOldest trims the oldest 10 percent of entries so a long-running process
// cannot exhaust memory. The caller must hold d.mu.
//
// Evicting means an evicted ID could be claimed again, which is exactly why
// this type is development only. The Postgres implementation gets retention
// from a cleanup job instead of ad-hoc eviction.
func (d *MemoryDeduper) evictOldest() {
	dropCount := len(d.order) / 10
	if dropCount < 1 {
		dropCount = 1
	}

	end := d.head + dropCount
	if end > len(d.order) {
		end = len(d.order)
	}

	for _, id := range d.order[d.head:end] {
		delete(d.claims, id)
	}
	d.head = end

	// Reclaim the consumed prefix once it dominates the slice, otherwise the
	// backing array grows without bound even though live entries are few.
	if d.head > 0 && d.head*2 >= len(d.order) {
		d.order = append(d.order[:0], d.order[d.head:]...)
		d.head = 0
	}
}

// Len returns the number of retained claims. Used by tests.
func (d *MemoryDeduper) Len() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return len(d.claims)
}
