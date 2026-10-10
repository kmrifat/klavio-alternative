-- +goose Up

-- People who can sign in. company_role covers the "Company" column in S9;
-- brand roles live in memberships.
CREATE TABLE users (
    id             uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    email          citext NOT NULL UNIQUE,
    name           text NOT NULL,
    company_role   text NOT NULL DEFAULT 'member' CHECK (company_role IN ('member', 'approver', 'owner')),
    webauthn_id    bytea NOT NULL UNIQUE, -- random user handle stored inside each passkey
    created_at     timestamptz NOT NULL DEFAULT now(),
    last_active_at timestamptz
);

CREATE TABLE passkeys (
    id            uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id       uuid NOT NULL REFERENCES users ON DELETE CASCADE,
    credential_id bytea NOT NULL UNIQUE,
    credential    jsonb NOT NULL, -- webauthn.Credential (public key, sign count, flags)
    created_at    timestamptz NOT NULL DEFAULT now(),
    last_used_at  timestamptz
);
CREATE INDEX passkeys_user_id ON passkeys (user_id);

-- The cookie holds a random token; only its SHA-256 is stored.
CREATE TABLE sessions (
    token_hash    bytea PRIMARY KEY,
    user_id       uuid NOT NULL REFERENCES users ON DELETE CASCADE,
    created_at    timestamptz NOT NULL DEFAULT now(),
    expires_at    timestamptz NOT NULL,
    last_seen_at  timestamptz NOT NULL DEFAULT now(),
    stepped_up_at timestamptz, -- last passkey confirmation, for sensitive actions
    ip            text NOT NULL DEFAULT '',
    user_agent    text NOT NULL DEFAULT ''
);
CREATE INDEX sessions_user_id ON sessions (user_id);

CREATE TABLE brands (
    id                   uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    slug                 text NOT NULL UNIQUE CHECK (slug ~ '^[a-z0-9][a-z0-9-]{1,30}[a-z0-9]$'),
    name                 text NOT NULL,
    color                text NOT NULL CHECK (color ~ '^#[0-9a-f]{6}$'),
    postal_address       text NOT NULL,
    sending_domain       citext NOT NULL UNIQUE, -- news.brand.com
    mail_from_domain     citext NOT NULL UNIQUE, -- bounce.news.brand.com
    link_domain          citext NOT NULL UNIQUE, -- go.brand.com
    ses_configuration_set text NOT NULL UNIQUE,
    ses_identity_status  text NOT NULL DEFAULT 'pending',
    status               text NOT NULL DEFAULT 'setup' CHECK (status IN ('setup', 'ready', 'paused')),
    dns_checked_at       timestamptz,
    created_at           timestamptz NOT NULL DEFAULT now(),
    updated_at           timestamptz NOT NULL DEFAULT now()
);

-- Records the brand's DNS must have before it may send (S2 "Authentication").
CREATE TABLE brand_dns_records (
    id         uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    brand_id   uuid NOT NULL REFERENCES brands ON DELETE CASCADE,
    purpose    text NOT NULL CHECK (purpose IN ('dkim', 'mail_from_mx', 'mail_from_spf', 'dmarc')),
    type       text NOT NULL CHECK (type IN ('CNAME', 'MX', 'TXT')),
    name       text NOT NULL,
    value      text NOT NULL,
    status     text NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'ok', 'warning', 'failing')),
    detail     text NOT NULL DEFAULT '',
    checked_at timestamptz,
    UNIQUE (brand_id, purpose, name)
);

CREATE TABLE memberships (
    user_id    uuid NOT NULL REFERENCES users ON DELETE CASCADE,
    brand_id   uuid NOT NULL REFERENCES brands ON DELETE CASCADE,
    role       text NOT NULL CHECK (role IN ('viewer', 'editor', 'sender', 'brand_admin')),
    created_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (user_id, brand_id)
);
CREATE INDEX memberships_brand_id ON memberships (brand_id);

-- One-time invite links. The user row is created when the passkey is registered.
CREATE TABLE invites (
    id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    token_hash   bytea NOT NULL UNIQUE,
    email        citext NOT NULL,
    name         text NOT NULL,
    company_role text NOT NULL DEFAULT 'member' CHECK (company_role IN ('member', 'approver', 'owner')),
    brand_roles  jsonb NOT NULL DEFAULT '{}', -- {"<brand_id>": "<role>"}
    webauthn_id  bytea NOT NULL UNIQUE,
    invited_by   uuid REFERENCES users ON DELETE SET NULL,
    created_at   timestamptz NOT NULL DEFAULT now(),
    expires_at   timestamptz NOT NULL,
    accepted_at  timestamptz
);
CREATE UNIQUE INDEX invites_open_email ON invites (email) WHERE accepted_at IS NULL;

-- Append-only. No foreign keys: entries must outlive the users and brands they mention.
CREATE TABLE audit_log (
    id          bigserial PRIMARY KEY,
    at          timestamptz NOT NULL DEFAULT now(),
    actor_id    uuid,
    actor_email text NOT NULL DEFAULT '',
    brand_id    uuid,
    action      text NOT NULL,
    target_type text NOT NULL DEFAULT '',
    target_id   text NOT NULL DEFAULT '',
    before      jsonb,
    after       jsonb,
    ip          text NOT NULL DEFAULT '',
    user_agent  text NOT NULL DEFAULT ''
);
CREATE INDEX audit_log_at ON audit_log (at DESC);
CREATE INDEX audit_log_brand_at ON audit_log (brand_id, at DESC);

-- +goose StatementBegin
CREATE FUNCTION audit_log_is_append_only() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    RAISE EXCEPTION 'audit_log is append-only';
END;
$$;
-- +goose StatementEnd

CREATE TRIGGER audit_log_append_only
    BEFORE UPDATE OR DELETE OR TRUNCATE ON audit_log
    FOR EACH STATEMENT EXECUTE FUNCTION audit_log_is_append_only();

-- +goose Down
DROP TABLE audit_log;
DROP FUNCTION audit_log_is_append_only();
DROP TABLE invites;
DROP TABLE memberships;
DROP TABLE brand_dns_records;
DROP TABLE brands;
DROP TABLE sessions;
DROP TABLE passkeys;
DROP TABLE users;
