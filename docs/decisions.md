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

## ADR-009: `internal/` package layout

**Status:** Accepted

**Context:** The project will accumulate a lot of code, and package boundaries
that erode early are hard to restore later.

**Decision:** Everything lives under `internal/`, which the Go toolchain
enforces as private to this module.

**Consequences:** Import cycles and accidental public API become compile-time
problems rather than review-time ones. The codebase cannot be imported by
anyone else, which is correct for an application.
