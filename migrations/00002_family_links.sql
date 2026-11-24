-- +goose Up
-- +goose StatementBegin

CREATE TABLE family_links (
    id          uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    child_id    uuid NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    parent_id   uuid NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    status      text NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'active', 'revoked')),
    created_at  timestamptz NOT NULL DEFAULT now(),
    updated_at  timestamptz NOT NULL DEFAULT now(),
    UNIQUE (child_id, parent_id),
    CHECK (child_id <> parent_id)
);

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin

DROP TABLE IF EXISTS family_links;

-- +goose StatementEnd
