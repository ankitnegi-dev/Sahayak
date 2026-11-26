-- name: GetOrCreateUserByPhone :one
INSERT INTO users (phone, role, language)
VALUES ($1, $2, $3)
ON CONFLICT (phone) DO UPDATE SET phone = EXCLUDED.phone
RETURNING id, phone, role, language, created_at, updated_at;

-- name: GetUser :one
SELECT id, phone, role, language, created_at, updated_at
FROM users
WHERE id = $1;

-- name: GetUserByPhone :one
SELECT id, phone, role, language, created_at, updated_at
FROM users
WHERE phone = $1;

-- name: ListSavedPlaces :many
SELECT id, user_id, label, address, lat, lng, created_at, updated_at
FROM saved_places
WHERE user_id = $1
ORDER BY lower(label);

-- name: GetSavedPlaceByLabel :one
SELECT id, user_id, label, address, lat, lng, created_at, updated_at
FROM saved_places
WHERE user_id = $1 AND lower(label) = lower($2);

-- name: UpsertSavedPlace :one
INSERT INTO saved_places (user_id, label, address, lat, lng)
VALUES ($1, $2, $3, $4, $5)
ON CONFLICT (user_id, lower(label)) DO UPDATE
SET address = EXCLUDED.address,
    lat = EXCLUDED.lat,
    lng = EXCLUDED.lng,
    updated_at = now()
RETURNING id, user_id, label, address, lat, lng, created_at, updated_at;

-- name: GetLimits :one
SELECT user_id, per_ride_cap_paise, daily_cap_paise, allowed_hours_start, allowed_hours_end, kill_switch, updated_at
FROM limits
WHERE user_id = $1;

-- name: UpsertLimits :one
INSERT INTO limits (user_id, per_ride_cap_paise, daily_cap_paise, allowed_hours_start, allowed_hours_end, kill_switch)
VALUES ($1, $2, $3, $4, $5, $6)
ON CONFLICT (user_id) DO UPDATE
SET per_ride_cap_paise = EXCLUDED.per_ride_cap_paise,
    daily_cap_paise = EXCLUDED.daily_cap_paise,
    allowed_hours_start = EXCLUDED.allowed_hours_start,
    allowed_hours_end = EXCLUDED.allowed_hours_end,
    kill_switch = EXCLUDED.kill_switch,
    updated_at = now()
RETURNING user_id, per_ride_cap_paise, daily_cap_paise, allowed_hours_start, allowed_hours_end, kill_switch, updated_at;

-- name: SetKillSwitch :exec
UPDATE limits
SET kill_switch = $2, updated_at = now()
WHERE user_id = $1;

-- name: GetConversation :one
SELECT user_id, state, context, updated_at
FROM conversations
WHERE user_id = $1;

-- name: UpsertConversation :one
INSERT INTO conversations (user_id, state, context)
VALUES ($1, $2, $3)
ON CONFLICT (user_id) DO UPDATE
SET state = EXCLUDED.state,
    context = EXCLUDED.context,
    updated_at = now()
RETURNING user_id, state, context, updated_at;

-- name: CreateOrder :one
INSERT INTO orders (user_id, type, status, pickup, drop_location, amount_paise, provider, provider_ref, idempotency_key)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
ON CONFLICT (idempotency_key) DO NOTHING
RETURNING id, user_id, type, status, pickup, drop_location, amount_paise, provider, provider_ref, idempotency_key, created_at, updated_at;

-- name: GetOrder :one
SELECT id, user_id, type, status, pickup, drop_location, amount_paise, provider, provider_ref, idempotency_key, created_at, updated_at
FROM orders
WHERE id = $1;

-- name: GetOrderByUserAndIdempotencyKey :one
SELECT id, user_id, type, status, pickup, drop_location, amount_paise, provider, provider_ref, idempotency_key, created_at, updated_at
FROM orders
WHERE user_id = $1 AND idempotency_key = $2;

-- name: UpdateOrderStatus :exec
UPDATE orders
SET status = $2, updated_at = now()
WHERE id = $1;

-- name: ListOrdersByUser :many
SELECT id, user_id, type, status, pickup, drop_location, amount_paise, provider, provider_ref, idempotency_key, created_at, updated_at
FROM orders
WHERE user_id = $1
ORDER BY created_at DESC
LIMIT $2 OFFSET $3;

-- name: SumTodaySpend :one
-- $2 is the start of the IST day (passed by the caller).
SELECT COALESCE(SUM(amount_paise), 0)::bigint AS total
FROM orders
WHERE user_id = $1
  AND status NOT IN ('cancelled', 'failed')
  AND created_at >= $2;

-- name: CountTodayBookings :one
-- Counts non-cancelled, non-failed orders created since the start of the IST day.
SELECT count(*)::bigint AS total
FROM orders
WHERE user_id = $1
  AND status NOT IN ('cancelled', 'failed')
  AND created_at >= $2;

-- name: CountRecentBookingAttempts :one
-- Counts booking attempts in the last N minutes (for rapid-repeat detection).
SELECT count(*)::bigint AS total
FROM orders
WHERE user_id = $1
  AND created_at >= now() - ($2::text || ' minutes')::interval;

-- name: AppendAudit :one
INSERT INTO audit_log (actor, action, entity_type, entity_id, details)
VALUES ($1, $2, $3, $4, $5)
RETURNING id, actor, action, entity_type, entity_id, details, created_at;

-- name: LinkFamily :one
INSERT INTO family_links (child_id, parent_id, status)
VALUES ($1, $2, $3)
ON CONFLICT (child_id, parent_id) DO UPDATE
SET status = EXCLUDED.status, updated_at = now()
RETURNING id, child_id, parent_id, status, created_at, updated_at;

-- name: ListFamilyLinks :many
SELECT id, child_id, parent_id, status, created_at, updated_at
FROM family_links
WHERE child_id = $1 OR parent_id = $1
ORDER BY created_at DESC;

-- name: MarkMessageSeen :execrows
INSERT INTO processed_messages (wa_message_id)
VALUES ($1)
ON CONFLICT (wa_message_id) DO NOTHING;

-- name: CreateApproval :one
INSERT INTO approvals (user_id, child_id, order_snapshot, reason, short_code, expires_at)
VALUES ($1, $2, $3, $4, $5, $6)
RETURNING id, user_id, child_id, order_snapshot, reason, status, short_code, created_at, expires_at, decided_at;

-- name: GetApprovalByCode :one
SELECT id, user_id, child_id, order_snapshot, reason, status, short_code, created_at, expires_at, decided_at
FROM approvals
WHERE short_code = $1;

-- name: GetApproval :one
SELECT id, user_id, child_id, order_snapshot, reason, status, short_code, created_at, expires_at, decided_at
FROM approvals
WHERE id = $1;

-- name: UpdateApprovalStatus :exec
UPDATE approvals
SET status = $2, decided_at = now()
WHERE id = $1 AND status = 'pending';

-- name: ExpireStaleApprovals :exec
UPDATE approvals
SET status = 'expired', decided_at = now()
WHERE status = 'pending' AND expires_at < now();

-- name: ListPendingApprovalsForChild :many
SELECT id, user_id, child_id, order_snapshot, reason, status, short_code, created_at, expires_at, decided_at
FROM approvals
WHERE child_id = $1 AND status = 'pending' AND expires_at > now()
ORDER BY created_at DESC;

-- name: CountProcessedMessages :one
SELECT count(*)::bigint FROM processed_messages;
-- name: GetConversationForUpdate :one
SELECT user_id, state, context, updated_at
FROM conversations
WHERE user_id = $1
FOR UPDATE;

-- name: CreateConversation :one
INSERT INTO conversations (user_id, state, context)
VALUES ($1, 'idle', '{}')
ON CONFLICT (user_id) DO NOTHING
RETURNING user_id, state, context, updated_at;
