# Architecture Decision Log

Every non-obvious choice goes here, written at the time it is made. The goal is
that a future reader (including future me) understands *why*, not just *what*.

Format: one entry per decision, newest last. Keep them short.

---

## ADR-001: Go for the backend

**Status:** Accepted

**Context:** The goal is to learn Go properly by building something real.

**Decision:** Go 1.23+ for all product code. A small Python script remains for
the ASR word-error-rate benchmark (jiwer), since that is tooling, not product.

**Consequences:** One static binary per process, which makes the eventual
Docker/Fly.io deploy trivial. Goroutines map onto Python's asyncio but with
built-in scheduling. The cost is a stricter, more verbose error-handling style
that has to be learned deliberately.

---

## ADR-002: Separate `api` and `worker` binaries

**Status:** Accepted

**Context:** WhatsApp expects a fast 200 response to a webhook. Transcribing
audio and calling an LLM take seconds, not milliseconds.

**Decision:** Two commands: `cmd/api` (accept, verify, deduplicate, enqueue,
return 200) and `cmd/worker` (transcribe, converse, book). They communicate
only through Redis.

**Consequences:** A slow or wedged ASR provider can never cause Meta to time
out and retry, which would otherwise produce duplicate bookings. Costs one
extra process to deploy and monitor.

---

## ADR-003: Deep links before network booking

**Status:** Accepted

**Context:** Uber, Ola, Rapido, Zomato and BookMyShow expose no public booking
APIs to third parties. There is no legitimate way to create a real ride from
our own backend.

**Decision:** Ship a `Provider` interface with two implementations in order:
first a deep-link provider that hands the parent a pre-filled one-tap URL,
then a Beckn/ONDC sandbox provider once the ecosystem is verified as live in
the target city.

**Consequences:** The bot can create a genuine ride on day one without
scraping or automating anyone's app. The interface keeps the door open for
real network booking later. We never handle money: cash or the provider's own
payment only.

---

## ADR-004: The LLM extracts, code decides

**Status:** Accepted

**Context:** Elderly users are prime targets for scams. Letting a model decide
whether a ride gets booked, or how much it costs, is an unacceptable risk.

**Decision:** The model may only produce structured intent (pickup, drop, time)
and reply text. Every action is decided by Go code, and anything touching money
is decided by a state machine, the safety layer, and an explicit confirmation.

**Consequences:** More code, and some conversations feel stiffer than free-form
chat. That is the correct trade. Testable guarantees are worth more than
conversational fluency when the user cannot read the screen easily.

---

## ADR-005: Idempotency at the webhook boundary

**Status:** Accepted

**Context:** Meta retries webhook deliveries on timeout or non-2xx. Without
deduplication this becomes duplicate bookings, which is the single worst bug
this product could have.

**Decision:** The WhatsApp message ID is inserted into `processed_messages`
with a unique constraint before any work is enqueued. A conflict means the
message was already seen and is dropped.

**Consequences:** A duplicate delivery costs one cheap INSERT and nothing else.
This must be in place before the worker is ever written, not after.

---

## ADR-006: Log redaction in the logger, not just at call sites

**Status:** Accepted

**Context:** Phone numbers are personal data under DPDP. Redaction only at
call sites is one refactor away from a leak.

**Decision:** The slog handler masks a fixed set of sensitive attribute keys
(`token`, `access_token`, `app_secret`, `api_key`, `password`, `authorization`,
raw bodies) as a backstop, and `logger.RedactPhone` masks numbers explicitly at
call sites.

**Consequences:** A secret logged by mistake under a sensitive key still does
not reach the log store. Non-sensitive keys carrying user data still rely on
call-site discipline.

---

## ADR-007: Configuration validated once at startup

**Status:** Accepted

**Context:** A missing secret discovered mid-conversation means a parent sends
a voice note and gets silence.

**Decision:** `config.Load()` runs before anything starts listening. It applies
defaults, rejects malformed values safely, and in production refuses to boot
without every credential or with a weak verify token.

**Consequences:** Misconfiguration is a crash on deploy, not a runtime surprise.
Credentials are mandatory only in production so unit and contract tests need no
secrets.

---

## ADR-008: chi over the standard library router

**Status:** Accepted

**Context:** The API needs path parameters, grouped routes and middleware.

**Decision:** `go-chi/chi`. It stays close to `net/http`: handlers are plain
`http.HandlerFunc`, `httptest` works directly, and nothing is hidden behind
framework magic.

**Consequences:** Small dependency, easy to read, easy to drop. An extra router
such as gin or echo would buy little for this traffic profile.

---

## ADR-010: Interfaces are declared where they are used, not where implemented

**Status:** Accepted

**Context:** Phase 2 needs the webhook handler to deduplicate messages and
publish jobs, without knowing whether those live in memory or Postgres.

**Decision:** Each dependency is an interface declared in the package that
consumes it. `whatsapp.Deduper` is declared in the `whatsapp` package but
consumed by `httpapi`. `jobs.Publisher` is declared in `jobs` and consumed by
`httpapi`. Neither package imports a concrete implementation.

**Consequences:** The dependency arrow points one way and import cycles are
structurally impossible. A test supplies a fake without any mocking library.
Phase 4 swaps in a Postgres deduper by changing one line in `cmd/api`, and the
handler does not change at all.

---

## ADR-011: Deduplicate before enqueue, and ack duplicates with 200

**Status:** Accepted

**Context:** Meta retries a webhook delivery whenever it does not get a fast
2xx. A retry processed as new work is a duplicate cab booking, which is the
worst bug this product could have.

**Decision:** Three rules, in this order:

1. Mark the message ID seen BEFORE enqueueing. Reversed, a retry could enqueue
   a second job even though the ID was recorded.
2. Return 200 for a verified duplicate. Returning an error would make Meta
   retry it forever.
3. Return 500 when the dedup store itself fails. A 200 would lose the message
   permanently, so the safe outcome is to make Meta retry.

**Consequences:** A retried delivery costs one INSERT and nothing else. When
the queue is down and we return 500, Meta retries, and the retry is caught by
dedup, so the message is enqueued exactly once. The two mechanisms are designed
to interact correctly under failure.

---

## ADR-012: Verify the signature over raw bytes, before decoding

**Status:** Accepted

**Context:** Meta signs the exact request body with HMAC-SHA256 and sends the
digest in `X-Hub-Signature-256`.

**Decision:** Read the body with `io.ReadAll`, verify against those exact bytes,
and only then decode JSON. Never decode and re-encode, because key order and
whitespace change and the digest will not match.

**Consequences:** A tampered body is rejected with 403 before any business logic
runs. Returning 403 rather than 500 is deliberate: 500 would make Meta retry
something that can never succeed, forever.

---

## ADR-013: Defer Postgres dedup to Phase 4 behind an interface

**Status:** Accepted

**Context:** Phase 2 is the webhook and Phase 4 is the data layer, but dedup
needs storage from day one.

**Decision:** Ship `MemoryDeduper` now, with the `Deduper` interface already in
place, and replace it with a Postgres implementation backed by a unique
constraint in Phase 4.

**Consequences:** Phase 2 is fully testable with no database, and the dedup
behaviour is proven by tests now rather than discovered in production later.
The in-memory version is explicitly documented as development-only, because it
loses state on restart and does not dedup across replicas. Either failure mode
would produce duplicate bookings, so the log line says so loudly.

---

## ADR-014: No audio bytes in the queue payload

**Status:** Accepted

**Context:** Parents send voice notes. It would be simplest to put the audio in
the job so the worker does not have to fetch it.

**Decision:** The queue payload carries only the Meta media ID. The worker
resolves that into a temporary URL and downloads the audio itself.

**Consequences:** Audio never sits in Redis, which upholds the promise that it
is deleted immediately after transcription. The cost is one extra API call per
voice note, and the worker must hold the access token. The media URL is short
lived, so storing it would have been wrong anyway.

---

## ADR-015: A test handler must never block on r.Context().Done()

**Status:** Accepted

**Context:** A cancellation test hung for the full suite timeout and had to be
diagnosed from a goroutine dump.

**Decision:** The fake Graph API server blocks on an explicit `release`
channel that the test closes, never on the request context.

**Consequences:** Go's HTTP/1.1 server only cancels the request context when
the connection actually closes, so a handler waiting on it deadlocks
`httptest.Server.Close()`. This pattern is a trap worth remembering for every
future contract test, not just this one.


---

## ADR-009: `internal/` package layout

**Status:** Accepted

**Context:** The project will accumulate a lot of code, and package boundaries
that erode early are hard to restore later.

**Decision:** Everything lives under `internal/`, which the Go toolchain
enforces as private to this module.

**Consequences:** Import cycles and accidental public API become compile-time
problems rather than review-time ones. The codebase cannot be imported by
anyone else, which is correct for an application.
