-- +goose Up
-- +goose StatementBegin

CREATE TABLE limits (
    user_id               uuid PRIMARY KEY REFERENCES users (id) ON DELETE CASCADE,
    per_ride_cap_paise     bigint NOT NULL DEFAULT 0 CHECK (per_ride_cap_paise >= 0),
    daily_cap_paise       bigint NOT NULL DEFAULT 0 CHECK (daily_cap_paise >= 0),
    allowed_hours_start   smallint NOT NULL DEFAULT 0 CHECK (allowed_hours_start >= 0 AND allowed_hours_start <= 23),
    allowed_hours_end     smallint NOT NULL DEFAULT 23 CHECK (allowed_hours_end >= 0 AND allowed_hours_end <= 23),
    kill_switch           boolean NOT NULL DEFAULT false,
    updated_at            timestamptz NOT NULL DEFAULT now()
);

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin

DROP TABLE IF EXISTS limits;

-- +goose StatementEnd
