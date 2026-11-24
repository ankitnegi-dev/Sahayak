-- +goose Up
-- +goose StatementBegin

CREATE TABLE audit_log (
    id           bigserial PRIMARY KEY,
    actor        text NOT NULL,
    action       text NOT NULL,
    entity_type  text,
    entity_id    text,
    details      jsonb NOT NULL DEFAULT '{}',
    created_at   timestamptz NOT NULL DEFAULT now()
);

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin

DROP TABLE IF EXISTS audit_log;

-- +goose StatementEnd
