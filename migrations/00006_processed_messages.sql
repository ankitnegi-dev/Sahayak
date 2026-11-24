-- +goose Up
-- +goose StatementBegin

CREATE TABLE processed_messages (
    wa_message_id  text PRIMARY KEY,
    received_at    timestamptz NOT NULL DEFAULT now()
);

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin

DROP TABLE IF EXISTS processed_messages;

-- +goose StatementEnd
