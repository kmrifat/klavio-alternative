-- +goose Up

-- Segments (S12): saved rules, evaluated when used. Rules are JSON in a
-- fixed shape; the server turns them into parameterised SQL from a whitelist.
CREATE TABLE segments (
    id         uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    brand_id   uuid NOT NULL REFERENCES brands ON DELETE CASCADE,
    name       text NOT NULL,
    rules      jsonb NOT NULL,
    created_by uuid REFERENCES users ON DELETE SET NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (brand_id, name)
);

-- Consent wordings (S14): the exact sentence people agreed to on a signup
-- form. The text never changes once saved, so consent records that point at
-- it stay true; a new wording is added and the old one retired instead.
CREATE TABLE consent_wordings (
    id         uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    brand_id   uuid NOT NULL REFERENCES brands ON DELETE CASCADE,
    label      text NOT NULL,
    text       text NOT NULL CHECK (length(text) BETWEEN 10 AND 2000),
    created_by uuid REFERENCES users ON DELETE SET NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    retired_at timestamptz,
    UNIQUE (brand_id, id)
);

-- +goose StatementBegin
CREATE FUNCTION consent_wordings_immutable() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF TG_OP = 'DELETE' THEN
        RAISE EXCEPTION 'consent wordings are never deleted; retire them';
    END IF;
    IF NEW.text IS DISTINCT FROM OLD.text OR NEW.label IS DISTINCT FROM OLD.label
       OR NEW.brand_id IS DISTINCT FROM OLD.brand_id OR NEW.created_at IS DISTINCT FROM OLD.created_at THEN
        RAISE EXCEPTION 'consent wording text is immutable; add a new wording instead';
    END IF;
    IF OLD.retired_at IS NOT NULL AND NEW.retired_at IS DISTINCT FROM OLD.retired_at THEN
        RAISE EXCEPTION 'a retired consent wording stays retired';
    END IF;
    RETURN NEW;
END;
$$;
-- +goose StatementEnd

CREATE TRIGGER consent_wordings_immutable
    BEFORE UPDATE OR DELETE ON consent_wordings
    FOR EACH ROW EXECUTE FUNCTION consent_wordings_immutable();

ALTER TABLE consent_records ADD COLUMN wording_id uuid REFERENCES consent_wordings;

-- Contacts API keys (S14). Only a hash of the key is stored; the prefix is
-- what people see. A key works once approved (approval_id decided) and
-- until it expires or is revoked.
CREATE TABLE api_keys (
    id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    brand_id     uuid NOT NULL REFERENCES brands ON DELETE CASCADE,
    name         text NOT NULL,
    prefix       text NOT NULL UNIQUE,
    key_hash     bytea NOT NULL UNIQUE,
    scopes       text[] NOT NULL,
    ip_allowlist text[] NOT NULL DEFAULT '{}', -- CIDRs; empty = any address
    created_by   uuid REFERENCES users ON DELETE SET NULL,
    created_at   timestamptz NOT NULL DEFAULT now(),
    expires_at   timestamptz NOT NULL,
    approval_id  uuid REFERENCES approvals,
    active       boolean NOT NULL DEFAULT false, -- false until approved
    revoked_at   timestamptz,
    revoked_by   uuid REFERENCES users ON DELETE SET NULL,
    last_used_at timestamptz,
    CHECK (expires_at <= created_at + interval '90 days')
);
CREATE INDEX api_keys_brand ON api_keys (brand_id, created_at DESC);

-- The last 30 days of API calls (S14 "Recent calls"). Emails are masked.
CREATE TABLE api_calls (
    id         bigserial PRIMARY KEY,
    key_id     uuid REFERENCES api_keys ON DELETE SET NULL,
    brand_id   uuid NOT NULL REFERENCES brands ON DELETE CASCADE,
    at         timestamptz NOT NULL DEFAULT now(),
    method     text NOT NULL,
    path       text NOT NULL,
    status     int NOT NULL,
    ip         text NOT NULL DEFAULT '',
    email_mask text NOT NULL DEFAULT '',
    error_code text NOT NULL DEFAULT ''
);
CREATE INDEX api_calls_brand ON api_calls (brand_id, at DESC);

-- Idempotency-Key replays (24 hours): the same key and body get the same
-- answer; the same key with a different body is refused.
CREATE TABLE api_idempotency (
    key_id       uuid NOT NULL REFERENCES api_keys ON DELETE CASCADE,
    idem_key     text NOT NULL,
    request_hash bytea NOT NULL,
    status       int NOT NULL,
    response     jsonb NOT NULL,
    created_at   timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (key_id, idem_key)
);

-- Marketing emails per person (by address, across brands), for the
-- cross-brand frequency cap. Kept 8 days.
CREATE TABLE marketing_sends (
    id         bigserial PRIMARY KEY,
    email_hash bytea NOT NULL,
    brand_id   uuid NOT NULL REFERENCES brands ON DELETE CASCADE,
    contact_id uuid,
    sent_at    timestamptz NOT NULL
);
CREATE INDEX marketing_sends_person ON marketing_sends (email_hash, sent_at DESC);
CREATE INDEX marketing_sends_at ON marketing_sends (sent_at);

-- Double opt-in (P1/P2) and re-permission emails.
ALTER TABLE contacts ADD COLUMN confirmation_sent_at timestamptz;
ALTER TABLE contacts ADD COLUMN repermission_sent_at timestamptz;
CREATE INDEX contacts_repermission ON contacts (brand_id, repermission_sent_at) WHERE status = 'quarantined';
-- Re-permission emails go out (200/day) only once a brand admin turns them on.
ALTER TABLE brands ADD COLUMN repermission_on boolean NOT NULL DEFAULT false;

-- +goose Down
ALTER TABLE brands DROP COLUMN repermission_on;
DROP INDEX contacts_repermission;
ALTER TABLE contacts DROP COLUMN repermission_sent_at;
ALTER TABLE contacts DROP COLUMN confirmation_sent_at;
DROP TABLE marketing_sends;
DROP TABLE api_idempotency;
DROP TABLE api_calls;
DROP TABLE api_keys;
ALTER TABLE consent_records DROP COLUMN wording_id;
DROP TRIGGER consent_wordings_immutable ON consent_wordings;
DROP FUNCTION consent_wordings_immutable();
DROP TABLE consent_wordings;
DROP TABLE segments;
