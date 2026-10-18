# ParentBot

A WhatsApp voice assistant that helps parents book cabs, in their own language.

A parent sends a voice note, *"mere liye cab book karo"*, and the bot
understands it, asks short follow-up questions, confirms the details, and
completes the booking or sends a one-tap booking link. The child sets up saved
places, spending limits and monitors activity from a web dashboard.

Built in Go. Built to learn Go properly by building something real.

---

## Status

The project follows the roadmap in `docs/roadmap-source.txt` (~20 weeks,
part-time).

| Phase | Scope | Status |
| --- | --- | --- |
| 0 | Learn Go: Tour of Go, Go by Example, mini HTTP server | not started |
| 1 | Foundations: layout, config, logging, CI, docker-compose | **in progress** |
| 2 | WhatsApp webhook: signature verification, idempotency, queueing | not started |
| 3 | Voice pipeline: ASR providers, word error rate benchmark | not started |
| 4 | Data layer: schema, goose migrations, sqlc | not started |
| 5 | Conversation engine: state machine, Claude tool use | not started |
| 6 | Safety layer: caps, approval rules, rate limits, kill switch | not started |
| 7 | Child dashboard: Next.js + Go API | not started |
| 8 | Booking: deep links, then Beckn/ONDC sandbox | not started |
| 9 | Reliability: retries, circuit breakers, reconciliation | not started |
| 10 | Observability and security | not started |
| 11 | Deploy and real-world family test | not started |

---

## Architecture

```
   Parent (WhatsApp)
        │  voice note / text
        ▼
   Meta Cloud API
        │  webhook POST, signed with X-Hub-Signature-256
        ▼
  ┌─────────────────────────────────────────┐
  │  cmd/api                               │
  │   1. verify HMAC signature (crypto/hmac)│
  │   2. INSERT processed_messages (unique) │  ← idempotency
  │   3. enqueue to Redis                   │
  │   4. return 200 immediately            │
  └─────────────────────────────────────────┘
        │  asynq job
        ▼
  ┌─────────────────────────────────────────┐
  │  cmd/worker                            │
  │   download audio → ASR → transcript    │
  │   conversation state machine           │
  │   LLM extracts intent (never decides)  │
  │   safety layer (caps, rules)           │
  │   booking provider → order + audit log │
  └─────────────────────────────────────────┘
        │
        ▼
  Postgres (users, conversations, orders, audit_log, limits)
```

**The golden rule: the LLM only extracts intent and phrases replies. Your code
decides every action, especially anything involving money.** See
[ADR-004](docs/decisions.md).

---

## Repository layout

```
parentbot/
├── cmd/
│   ├── api/          # webhook and dashboard API
│   └── worker/       # background jobs
├── internal/
│   ├── config/       # env loading and validation
│   ├── logger/       # structured logging and redaction
│   └── httpapi/      # router and middleware
├── migrations/       # goose SQL migrations (Phase 4)
├── docs/
│   ├── decisions.md  # architecture decision log
│   └── roadmap-source.txt
├── dashboard/        # Next.js app (Phase 7)
└── docker-compose.yml
```

Everything lives under `internal/`, which Go enforces as private to this
module. See [ADR-009](docs/decisions.md).

---

## Prerequisites

- **Go 1.24+**, check with `go version`
- **Docker Desktop**, for local Postgres and Redis
- **Git**
- Later: **Node 20+** for the dashboard, **ffmpeg** for audio conversion

On Windows, `go test -race` also needs a C compiler, because the race detector
uses cgo. Install MinGW and make sure `gcc` is on your `PATH`. CI runs on
Ubuntu, where `gcc` is already present.

## Getting started

```bash
# 1. Install dependencies
make setup

# 2. Create your local env file
cp .env.example .env

# 3. Start Postgres and Redis
make up

# 4. Verify
make test
make run-api
```

Then check the service:

```bash
curl http://localhost:8080/healthz
# {"status":"ok"}
```

---

## Configuration

All configuration comes from environment variables, validated once at startup
before the server begins listening.

| Variable | Required | Default | Purpose |
| --- | --- | --- | --- |
| `APP_ENV` | no | `development` | `development`, `staging` or `production` |
| `DATABASE_URL` | yes | | Postgres connection string |
| `HTTP_ADDR` | no | `:8080` | Listen address |
| `HTTP_READ_TIMEOUT` | no | `10s` | Request read timeout |
| `HTTP_WRITE_TIMEOUT` | no | `15s` | Response write timeout |
| `HTTP_SHUTDOWN_TIMEOUT` | no | `20s` | Graceful shutdown budget |
| `DB_MAX_CONNS` | no | `10` | Pool maximum |
| `DB_MIN_CONNS` | no | `2` | Pool minimum |
| `REDIS_ADDR` | no | `localhost:6379` | Redis host:port |
| `REDIS_PASSWORD` | no | empty | Redis password |
| `REDIS_DB` | no | `0` | Redis database index |
| `LOG_LEVEL` | no | by env | `debug`, `info`, `warn`, `error` |
| `WHATSAPP_ACCESS_TOKEN` | prod only | | Meta access token |
| `WHATSAPP_VERIFY_TOKEN` | prod only | | Webhook verification token |
| `WHATSAPP_APP_SECRET` | prod only | | Used for signature verification |
| `WHATSAPP_PHONE_NUMBER_ID` | prod only | | Test number ID |
| `ANTHROPIC_API_KEY` | prod only | | Claude access |

In `production` every credential is mandatory, the verify token must be at least
16 characters, and `HTTP_ADDR` must not be the default. The process refuses to
boot otherwise. See [ADR-007](docs/decisions.md).

`.env` is gitignored. Never commit a secret.

---

## Make targets

| Command | Description |
| --- | --- |
| `make setup` | Download Go dependencies |
| `make test` | Run tests with the race detector and coverage |
| `make cover` | Open the coverage report |
| `make lint` | Run `go vet` |
| `make fmt` | Format all files |
| `make build` | Build both binaries into `bin/` |
| `make run-api` | Run the API server |
| `make run-worker` | Run the worker |
| `make up` / `make down` | Start or stop Postgres and Redis |
| `make psql` | Open a database shell |

---

## Testing strategy

- **Unit**: table-driven tests for the state machine, safety rules and parsers
- **Integration**: `testcontainers-go` with real Postgres and Redis
- **Contract**: `httptest` fake servers for WhatsApp, ASR, Claude and booking
- **Conversation**: a library of scripted dialogues, including adversarial ones
- **ASR benchmark**: word error rate tracked as a number in the repo

Coverage should be high on safety, conversation and order code. Do not chase
100% everywhere.

---

---

## Design principles

These are rules for this codebase, not suggestions:

1. Pass `context.Context` as the first argument to anything doing I/O.
2. Wrap errors with `fmt.Errorf("...: %w", err)` so the cause survives.
3. Define interfaces where they are used, not where they are implemented.
4. Keep packages small. Avoid global state.
5. Write the test first for state machine and safety logic.
6. Update `docs/decisions.md` whenever a choice changes.
7. When unsure, ask again or escalate to the child. **Never guess an address.**

---

## Scope

**In v1:** WhatsApp voice and text in one language, cab booking in one city via
deep links, saved places, spend caps, confirmation before every booking, child
dashboard.

**Out of scope:** movies (BookMyShow has no open path), handling money, public
launch, multiple cities.

Uber, Ola, Rapido, Zomato and BookMyShow do not offer public booking APIs. That
is why the plan is deep links first and the open ONDC/Beckn network second.
Scraping or automating their apps is brittle and against their terms, so we do
not do it.

---

## Privacy

Parents are elderly users sharing location and voice data. The rules:

- Delete audio immediately after transcription. Keep only the text.
- Redact phone numbers and addresses in logs. Enforced in the logger itself.
- Never guess an address; ask.
- Support a delete-my-data command (DPDP-style).
- Be open with family about exactly what is stored.
