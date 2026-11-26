-- +goose Up
-- +goose StatementBegin

-- Add awaiting_approval to the conversations state CHECK constraint.
ALTER TABLE conversations DROP CONSTRAINT conversations_state_check;
ALTER TABLE conversations ADD CONSTRAINT conversations_state_check CHECK (state IN (
    'idle', 'awaiting_pickup', 'awaiting_drop', 'awaiting_confirmation',
    'awaiting_approval', 'booking', 'active_ride', 'completed', 'cancelled', 'error'
));

-- Approvals table: a record of a safety check that required child approval.
CREATE TABLE approvals (
    id              uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id         uuid NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    child_id        uuid NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    order_snapshot  jsonb NOT NULL DEFAULT '{}',
    reason          text NOT NULL,
    status          text NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'approved', 'denied', 'expired')),
    short_code      text NOT NULL UNIQUE,
    created_at      timestamptz NOT NULL DEFAULT now(),
    expires_at      timestamptz NOT NULL,
    decided_at      timestamptz,
    CHECK (expires_at > created_at)
);

CREATE INDEX idx_approvals_user ON approvals (user_id);
CREATE INDEX idx_approvals_child ON approvals (child_id);
CREATE INDEX idx_approvals_status ON approvals (status, expires_at);

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin

DROP INDEX IF EXISTS idx_approvals_status;
DROP INDEX IF EXISTS idx_approvals_child;
DROP INDEX IF EXISTS idx_approvals_user;
DROP TABLE IF EXISTS approvals;

ALTER TABLE conversations DROP CONSTRAINT conversations_state_check;
ALTER TABLE conversations ADD CONSTRAINT conversations_state_check CHECK (state IN (
    'idle', 'awaiting_pickup', 'awaiting_drop', 'awaiting_confirmation',
    'booking', 'active_ride', 'completed', 'cancelled', 'error'
));

-- +goose StatementEnd
