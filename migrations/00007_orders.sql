-- +goose Up
-- +goose StatementBegin

CREATE TABLE orders (
    id                uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id           uuid NOT NULL REFERENCES users (id) ON DELETE RESTRICT,
    type              text NOT NULL CHECK (type IN ('ride', 'food')),
    status            text NOT NULL DEFAULT 'created' CHECK (status IN (
        'created', 'confirmed', 'booked', 'completed', 'cancelled', 'failed'
    )),
    pickup            text,
    drop_location     text,
    amount_paise      bigint NOT NULL DEFAULT 0 CHECK (amount_paise >= 0),
    provider          text,
    provider_ref      text,
    idempotency_key   text NOT NULL UNIQUE,
    created_at        timestamptz NOT NULL DEFAULT now(),
    updated_at        timestamptz NOT NULL DEFAULT now()
);

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin

DROP TABLE IF EXISTS orders;

-- +goose StatementEnd
