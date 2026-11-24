-- +goose Up
-- +goose StatementBegin

CREATE TABLE conversations (
    user_id     uuid PRIMARY KEY REFERENCES users (id) ON DELETE CASCADE,
    state       text NOT NULL DEFAULT 'idle' CHECK (state IN (
        'idle', 'awaiting_pickup', 'awaiting_drop', 'awaiting_confirmation',
        'booking', 'active_ride', 'completed', 'cancelled', 'error'
    )),
    context     jsonb NOT NULL DEFAULT '{}',
    updated_at  timestamptz NOT NULL DEFAULT now()
);

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin

DROP TABLE IF EXISTS conversations;

-- +goose StatementEnd
