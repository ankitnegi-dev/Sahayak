-- +goose Up
-- +goose StatementBegin

-- updated_at maintenance trigger.
CREATE OR REPLACE FUNCTION set_updated_at()
RETURNS trigger AS $$
BEGIN
    NEW.updated_at = now();
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

DO $$
DECLARE t text;
BEGIN
    FOR t IN SELECT unnest(ARRAY['users','family_links','saved_places','limits','conversations','orders']) LOOP
        EXECUTE format(
            'CREATE TRIGGER trg_%I_updated_at BEFORE UPDATE ON %I FOR EACH ROW EXECUTE FUNCTION set_updated_at();',
            t, t
        );
    END LOOP;
END;
$$;

-- Order status transition enforcement. Terminal states cannot change.
CREATE OR REPLACE FUNCTION enforce_order_status_transition()
RETURNS trigger AS $$
BEGIN
    IF OLD.status = NEW.status THEN
        RETURN NEW;
    END IF;

    -- Terminal states: no outbound transitions at all.
    IF OLD.status IN ('completed', 'cancelled', 'failed') THEN
        RAISE EXCEPTION 'illegal order status transition: % -> % (terminal state)', OLD.status, NEW.status
            USING ERRCODE = 'check_violation';
    END IF;

    -- Legal forward edges.
    IF (OLD.status = 'created'   AND NEW.status IN ('confirmed', 'cancelled', 'failed')) OR
       (OLD.status = 'confirmed' AND NEW.status IN ('booked',     'cancelled', 'failed')) OR
       (OLD.status = 'booked'    AND NEW.status IN ('completed',  'cancelled', 'failed')) THEN
        RETURN NEW;
    END IF;

    RAISE EXCEPTION 'illegal order status transition: % -> %', OLD.status, NEW.status
        USING ERRCODE = 'check_violation';
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER trg_orders_status_transition
    BEFORE UPDATE OF status ON orders
    FOR EACH ROW
    EXECUTE FUNCTION enforce_order_status_transition();

-- Audit log is append-only: reject UPDATE and DELETE for everyone.
CREATE OR REPLACE FUNCTION reject_audit_mutation()
RETURNS trigger AS $$
BEGIN
    RAISE EXCEPTION 'audit_log is append-only: % is not permitted', TG_OP
        USING ERRCODE = 'insufficient_privilege';
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER trg_audit_log_no_update
    BEFORE UPDATE ON audit_log
    FOR EACH ROW
    EXECUTE FUNCTION reject_audit_mutation();

CREATE TRIGGER trg_audit_log_no_delete
    BEFORE DELETE ON audit_log
    FOR EACH ROW
    EXECUTE FUNCTION reject_audit_mutation();

-- Indexes for common lookups.
CREATE INDEX idx_users_phone ON users (phone);
CREATE INDEX idx_orders_user_created ON orders (user_id, created_at DESC);
CREATE INDEX idx_audit_log_entity ON audit_log (entity_type, entity_id);
CREATE INDEX idx_audit_log_created ON audit_log (created_at DESC);
CREATE INDEX idx_saved_places_user ON saved_places (user_id);
CREATE INDEX idx_family_links_parent ON family_links (parent_id);
CREATE INDEX idx_family_links_child ON family_links (child_id);
CREATE INDEX idx_processed_messages_received ON processed_messages (received_at);

-- Restricted app role: INSERT and SELECT on audit_log only. No UPDATE, no DELETE.
-- Other tables get full CRUD so the application can do its work.
--
-- Creating a role requires superuser privileges, so this block is wrapped in
-- an exception handler. On a database where the running user is not a
-- superuser (for example the application's own role), the role is skipped and
-- the GRANT statements below are attempted anyway. In production the role is
-- created once by a DBA or the migration runner connecting as a superuser.
DO $$
BEGIN
    IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'sahayak_app') THEN
        CREATE ROLE sahayak_app LOGIN PASSWORD 'sahayak_app';
    END IF;
EXCEPTION WHEN insufficient_privilege THEN
    RAISE NOTICE 'sahayak_app role not created: insufficient privileges (run as superuser to create it)';
END;
$$;

DO $$
BEGIN
    IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'sahayak_app') THEN
        RAISE NOTICE 'sahayak_app role does not exist; skipping grants. Run this migration as a superuser to create it.';
        RETURN;
    END IF;
    GRANT USAGE ON SCHEMA public TO sahayak_app;
    GRANT SELECT, INSERT, UPDATE, DELETE ON
        users, family_links, saved_places, limits, conversations,
        processed_messages, orders
        TO sahayak_app;
    GRANT SELECT, INSERT ON audit_log TO sahayak_app;
    GRANT USAGE, SELECT ON SEQUENCE audit_log_id_seq TO sahayak_app;
    GRANT USAGE, SELECT ON ALL SEQUENCES IN SCHEMA public TO sahayak_app;
EXCEPTION WHEN insufficient_privilege THEN
    RAISE NOTICE 'grants to sahayak_app skipped: insufficient privileges';
END;
$$;

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin

DROP TRIGGER IF EXISTS trg_audit_log_no_delete ON audit_log;
DROP TRIGGER IF EXISTS trg_audit_log_no_update ON audit_log;
DROP TRIGGER IF EXISTS trg_orders_status_transition ON orders;

DO $$
DECLARE t text;
BEGIN
    FOR t IN SELECT unnest(ARRAY['users','family_links','saved_places','limits','conversations','orders']) LOOP
        EXECUTE format('DROP TRIGGER IF EXISTS trg_%I_updated_at ON %I;', t, t);
    END LOOP;
END;
$$;

DROP FUNCTION IF EXISTS reject_audit_mutation();
DROP FUNCTION IF EXISTS enforce_order_status_transition();
DROP FUNCTION IF EXISTS set_updated_at();

DROP INDEX IF EXISTS idx_users_phone;
DROP INDEX IF EXISTS idx_orders_user_created;
DROP INDEX IF EXISTS idx_audit_log_entity;
DROP INDEX IF EXISTS idx_audit_log_created;
DROP INDEX IF EXISTS idx_saved_places_user;
DROP INDEX IF EXISTS idx_family_links_parent;
DROP INDEX IF EXISTS idx_family_links_child;
DROP INDEX IF EXISTS idx_processed_messages_received;

-- +goose StatementEnd
