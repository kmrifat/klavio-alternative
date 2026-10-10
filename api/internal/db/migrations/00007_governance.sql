-- +goose Up

-- Two-person rule (S15). Any feature files a request; another eligible admin
-- approves or rejects it; the approved action runs in the same transaction.
CREATE TABLE approvals (
    id            uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    kind          text NOT NULL,
    brand_id      uuid REFERENCES brands ON DELETE CASCADE, -- NULL = company-wide
    requested_by  uuid NOT NULL REFERENCES users,
    requested_at  timestamptz NOT NULL DEFAULT now(),
    summary       text NOT NULL,        -- one line for the approver ("Loosen bounce pause 2% → 2.5%")
    reason        text NOT NULL DEFAULT '',
    payload       jsonb NOT NULL,       -- what the action needs to run
    status        text NOT NULL DEFAULT 'pending'
                  CHECK (status IN ('pending', 'approved', 'rejected', 'cancelled', 'expired')),
    decided_by    uuid REFERENCES users,
    decided_at    timestamptz,
    decision_note text NOT NULL DEFAULT '',
    result        jsonb,
    expires_at    timestamptz NOT NULL,
    -- Nobody approves or rejects their own request.
    CHECK (decided_by IS NULL OR decided_by <> requested_by)
);
CREATE INDEX approvals_pending ON approvals (requested_at) WHERE status = 'pending';
CREATE INDEX approvals_requester ON approvals (requested_by, requested_at DESC);

-- One-way flag: has this company ever had two admins (owners/approvers)?
-- Until then a lone first owner may make admin changes alone (bootstrap);
-- after that, admin changes always need a second person — demoting or
-- freezing the other admins can't reopen the shortcut. Set by a trigger, so
-- no code path can forget it.
CREATE TABLE company_state (
    id               int PRIMARY KEY DEFAULT 1 CHECK (id = 1),
    two_admins_since timestamptz
);
INSERT INTO company_state (id, two_admins_since)
SELECT 1, CASE WHEN (SELECT count(*) FROM users WHERE company_role IN ('owner', 'approver')) >= 2 THEN now() END;

-- +goose StatementBegin
CREATE FUNCTION note_two_admins() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF (SELECT count(*) FROM users WHERE company_role IN ('owner', 'approver')) >= 2 THEN
        UPDATE company_state SET two_admins_since = now() WHERE id = 1 AND two_admins_since IS NULL;
    END IF;
    RETURN NULL;
END;
$$;
-- +goose StatementEnd

CREATE TRIGGER users_note_two_admins
    AFTER INSERT OR UPDATE OF company_role ON users
    FOR EACH STATEMENT EXECUTE FUNCTION note_two_admins();

-- Company-wide safety limits (S17). Rows exist only for values changed from
-- the default; defaults and hard limits live in code.
CREATE TABLE safety_limits (
    key        text PRIMARY KEY,
    value      numeric NOT NULL,
    updated_by uuid REFERENCES users,
    updated_at timestamptz NOT NULL DEFAULT now()
);

-- Invites that grant admin rights wait for a second admin.
ALTER TABLE invites ADD COLUMN approval_id uuid REFERENCES approvals;

-- Exports of personal data: always approved, downloadable for 24 hours by the
-- requester only, watermarked.
CREATE TABLE exports (
    id            uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    brand_id      uuid NOT NULL REFERENCES brands ON DELETE CASCADE,
    requested_by  uuid NOT NULL REFERENCES users,
    approval_id   uuid NOT NULL REFERENCES approvals,
    filter        jsonb NOT NULL,
    row_count     int NOT NULL DEFAULT 0,
    file          bytea,
    ready_at      timestamptz,
    expires_at    timestamptz,
    download_count int NOT NULL DEFAULT 0,
    created_at    timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX exports_requester ON exports (requested_by, created_at DESC);

-- Sign-ins, for "new country / new device" anomaly detection.
CREATE TABLE sign_ins (
    id         bigserial PRIMARY KEY,
    user_id    uuid NOT NULL REFERENCES users ON DELETE CASCADE,
    at         timestamptz NOT NULL DEFAULT now(),
    ip         text NOT NULL DEFAULT '',
    country    text NOT NULL DEFAULT '', -- from Cloudflare's CF-IPCountry
    device     text NOT NULL DEFAULT ''  -- passkey credential id (hex)
);
CREATE INDEX sign_ins_user ON sign_ins (user_id, at DESC);

-- Frozen accounts can't sign in until an owner unfreezes them.
ALTER TABLE users ADD COLUMN frozen_at timestamptz;
ALTER TABLE users ADD COLUMN frozen_reason text NOT NULL DEFAULT '';

CREATE TABLE alerts (
    id              uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    kind            text NOT NULL,      -- new_country | new_device | mass_export | import_spike
    severity        text NOT NULL CHECK (severity IN ('info', 'warning', 'critical')),
    user_id         uuid REFERENCES users ON DELETE SET NULL,
    brand_id        uuid REFERENCES brands ON DELETE CASCADE,
    message         text NOT NULL,
    details         jsonb NOT NULL DEFAULT '{}',
    created_at      timestamptz NOT NULL DEFAULT now(),
    acknowledged_by uuid REFERENCES users,
    acknowledged_at timestamptz
);
CREATE INDEX alerts_open ON alerts (created_at DESC) WHERE acknowledged_at IS NULL;

-- +goose Down
DROP TABLE alerts;
DROP TRIGGER users_note_two_admins ON users;
DROP FUNCTION note_two_admins();
DROP TABLE company_state;
ALTER TABLE users DROP COLUMN frozen_reason;
ALTER TABLE users DROP COLUMN frozen_at;
DROP TABLE sign_ins;
DROP TABLE exports;
ALTER TABLE invites DROP COLUMN approval_id;
DROP TABLE safety_limits;
DROP TABLE approvals;
