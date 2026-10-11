-- +goose Up

-- GDPR erasure: one row per request carried out, as proof it was done. Keyed
-- by the address's hash, like suppressions: "was this person erased?" can be
-- answered without keeping the address.
CREATE TABLE erasures (
    id          uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    email_hash  bytea NOT NULL,
    contact_ref text NOT NULL,             -- masked address + short hash, as the audit log shows it
    reference   text NOT NULL,             -- where the request came from, e.g. "Email to privacy@, 3 Oct"
    summary     jsonb NOT NULL,            -- what was removed, kept and blanked
    erased_by   uuid NOT NULL REFERENCES users,
    erased_at   timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX erasures_person ON erasures (email_hash);
CREATE INDEX erasures_at ON erasures (erased_at DESC);

-- The audit log stays append-only, with one exception: inside an erasure
-- (app.erasure = 'on' for that transaction) an entry's target_id, before and
-- after may be rewritten to take the address out. Nothing else may change.
DROP TRIGGER audit_log_append_only ON audit_log;
CREATE TRIGGER audit_log_append_only
    BEFORE DELETE OR TRUNCATE ON audit_log
    FOR EACH STATEMENT EXECUTE FUNCTION audit_log_is_append_only();

-- +goose StatementBegin
CREATE FUNCTION audit_log_redact_only() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF current_setting('app.erasure', true) IS DISTINCT FROM 'on' THEN
        RAISE EXCEPTION 'audit_log is append-only';
    END IF;
    IF (NEW.id, NEW.at, NEW.actor_id, NEW.actor_email, NEW.brand_id, NEW.action, NEW.target_type, NEW.ip, NEW.user_agent)
       IS DISTINCT FROM (OLD.id, OLD.at, OLD.actor_id, OLD.actor_email, OLD.brand_id, OLD.action, OLD.target_type, OLD.ip, OLD.user_agent) THEN
        RAISE EXCEPTION 'an erasure may only redact target_id, before and after';
    END IF;
    RETURN NEW;
END;
$$;
-- +goose StatementEnd

CREATE TRIGGER audit_log_redact_only
    BEFORE UPDATE ON audit_log
    FOR EACH ROW EXECUTE FUNCTION audit_log_redact_only();

-- +goose Down
DROP TRIGGER audit_log_redact_only ON audit_log;
DROP FUNCTION audit_log_redact_only();
DROP TRIGGER audit_log_append_only ON audit_log;
CREATE TRIGGER audit_log_append_only
    BEFORE UPDATE OR DELETE OR TRUNCATE ON audit_log
    FOR EACH STATEMENT EXECUTE FUNCTION audit_log_is_append_only();
DROP TABLE erasures;
