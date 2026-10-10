-- +goose Up

-- What people can subscribe to per brand ("Product updates"). Unsubscribe is per topic.
CREATE TABLE topics (
    id         uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    brand_id   uuid NOT NULL REFERENCES brands ON DELETE CASCADE,
    name       text NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (brand_id, name),
    UNIQUE (brand_id, id) -- target of contact_topics' same-brand foreign key
);

-- Every brand starts with one topic.
INSERT INTO topics (brand_id, name) SELECT id, 'Newsletter' FROM brands;

CREATE TABLE contacts (
    id              uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    brand_id        uuid NOT NULL REFERENCES brands ON DELETE CASCADE,
    email           citext NOT NULL,
    email_hash      bytea NOT NULL, -- sha256(lower(email)); joins with suppressions
    first_name      text NOT NULL DEFAULT '',
    last_name       text NOT NULL DEFAULT '',
    status          text NOT NULL CHECK (status IN ('pending_confirmation', 'subscribed', 'quarantined', 'unsubscribed')),
    -- Consent summary for the list view; consent_records has the full history.
    consent_basis   text NOT NULL CHECK (consent_basis IN ('double_opt_in', 'single_opt_in', 'soft_opt_in')),
    consent_source  text NOT NULL DEFAULT '',
    consented_at    timestamptz,
    last_click_at   timestamptz,
    -- Latest sign of life: click, consent, or a 4–5 star rating from the old
    -- tool at import. Drives the engagement tier. When tracking isn't allowed
    -- it is the consent date only.
    last_engaged_at timestamptz,
    -- false: (EU) no consent to click tracking. No clicks are recorded, so the
    -- tier comes from the opt-in date alone and is never Active.
    tracking_allowed boolean NOT NULL DEFAULT true,
    tags            text[] NOT NULL DEFAULT '{}',
    timezone        text NOT NULL DEFAULT '',
    country         text NOT NULL DEFAULT '',
    import_id       uuid,
    created_at      timestamptz NOT NULL DEFAULT now(),
    updated_at      timestamptz NOT NULL DEFAULT now(),
    UNIQUE (brand_id, email),
    UNIQUE (brand_id, id) -- target of contact_topics' same-brand foreign key
);
CREATE INDEX contacts_brand_engaged ON contacts (brand_id, last_engaged_at);
CREATE INDEX contacts_brand_status ON contacts (brand_id, status);
CREATE INDEX contacts_email_hash ON contacts (email_hash);
CREATE INDEX contacts_tags ON contacts USING gin (tags);

-- brand_id is part of both foreign keys, so a contact can only be linked to
-- its own brand's topics.
CREATE TABLE contact_topics (
    brand_id   uuid NOT NULL,
    contact_id uuid NOT NULL,
    topic_id   uuid NOT NULL,
    PRIMARY KEY (contact_id, topic_id),
    FOREIGN KEY (brand_id, contact_id) REFERENCES contacts (brand_id, id) ON DELETE CASCADE,
    FOREIGN KEY (brand_id, topic_id) REFERENCES topics (brand_id, id) ON DELETE CASCADE
);
CREATE INDEX contact_topics_topic ON contact_topics (topic_id);

-- Proof of consent and every change to it. Append-only, kept even if the contact is deleted.
CREATE TABLE consent_records (
    id          bigserial PRIMARY KEY,
    brand_id    uuid NOT NULL,
    contact_id  uuid,
    email_hash  bytea NOT NULL,
    kind        text NOT NULL CHECK (kind IN ('opt_in', 'confirmed', 'import_attested', 'unsubscribed', 'resubscribed')),
    method      text NOT NULL CHECK (method IN ('import', 'manual', 'api', 'link', 'header')),
    source      text NOT NULL DEFAULT '',
    wording     text NOT NULL DEFAULT '',
    page_url    text NOT NULL DEFAULT '',
    ip          text NOT NULL DEFAULT '',
    occurred_at timestamptz NOT NULL DEFAULT now(),
    recorded_by uuid,
    import_id   uuid,
    created_at  timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX consent_records_contact ON consent_records (contact_id, occurred_at DESC);

-- +goose StatementBegin
CREATE FUNCTION consent_records_are_append_only() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    RAISE EXCEPTION 'consent_records is append-only';
END;
$$;
-- +goose StatementEnd

CREATE TRIGGER consent_records_append_only
    BEFORE UPDATE OR DELETE OR TRUNCATE ON consent_records
    FOR EACH STATEMENT EXECUTE FUNCTION consent_records_are_append_only();

-- Company-wide: a hard bounce or complaint at any brand blocks the address everywhere.
CREATE TABLE suppressions (
    email_hash   bytea PRIMARY KEY,
    reason       text NOT NULL CHECK (reason IN ('hard_bounce', 'complaint', 'manual', 'erasure')),
    brand_id     uuid, -- where it happened, if anywhere
    note         text NOT NULL DEFAULT '',
    created_by   uuid,
    created_at   timestamptz NOT NULL DEFAULT now()
);

-- CSV imports (S4). The file is kept until the import finishes so each step can re-read it.
CREATE TABLE imports (
    id               uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    brand_id         uuid NOT NULL REFERENCES brands ON DELETE CASCADE,
    created_by       uuid NOT NULL,
    filename         text NOT NULL,
    file             bytea,
    row_count        int NOT NULL DEFAULT 0,
    headers          text[] NOT NULL DEFAULT '{}',
    mapping          jsonb NOT NULL DEFAULT '{}',
    consent          jsonb NOT NULL DEFAULT '{}',
    topic_ids        uuid[] NOT NULL DEFAULT '{}',
    status           text NOT NULL DEFAULT 'uploaded'
                     CHECK (status IN ('uploaded', 'checking', 'checked', 'importing', 'done', 'failed', 'cancelled')),
    check_result     jsonb,
    import_result    jsonb,
    error            text NOT NULL DEFAULT '',
    created_at       timestamptz NOT NULL DEFAULT now(),
    updated_at       timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX imports_brand ON imports (brand_id, created_at DESC);

-- +goose Down
DROP TABLE imports;
DROP TABLE suppressions;
DROP TABLE consent_records;
DROP FUNCTION consent_records_are_append_only();
DROP TABLE contact_topics;
DROP TABLE contacts;
DROP TABLE topics;
