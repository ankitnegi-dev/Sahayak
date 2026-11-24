-- +goose Up
-- +goose StatementBegin

CREATE TABLE saved_places (
    id          uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id     uuid NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    label       text NOT NULL,
    address     text NOT NULL,
    lat         double precision,
    lng         double precision,
    created_at  timestamptz NOT NULL DEFAULT now(),
    updated_at  timestamptz NOT NULL DEFAULT now()
);

CREATE UNIQUE INDEX idx_saved_places_user_label ON saved_places (user_id, lower(label));

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin

DROP INDEX IF EXISTS idx_saved_places_user_label;
DROP TABLE IF EXISTS saved_places;

-- +goose StatementEnd
