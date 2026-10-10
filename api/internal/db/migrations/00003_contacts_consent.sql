-- +goose Up

-- Brand-owned child tables carry brand_id and point at their parent through
-- (brand_id, id), so a row can never link a contact of one brand to a topic,
-- tag or wording of another.

-- The exact text a person agreed to. key is versioned by hand
-- ("brightside-signup-v3"); text never changes once written — retire the
-- wording and add a new key instead.
CREATE TABLE consent_wordings (
    id         uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    brand_id   uuid NOT NULL REFERENCES brands ON DELETE CASCADE,
    key        text NOT NULL CHECK (key ~ '^[a-z0-9][a-z0-9-]{1,62}[a-z0-9]$'),
    text       text NOT NULL CHECK (text <> ''),
    status     text NOT NULL DEFAULT 'in_use' CHECK (status IN ('in_use', 'retired')),
    created_by uuid REFERENCES users ON DELETE SET NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (brand_id, key),
    UNIQUE (brand_id, id)
);

-- What a contact can subscribe to. Unsubscribing is per topic.
CREATE TABLE topics (
    id          uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    brand_id    uuid NOT NULL REFERENCES brands ON DELETE CASCADE,
    name        text NOT NULL CHECK (name <> ''),
    description text NOT NULL DEFAULT '',
    created_at  timestamptz NOT NULL DEFAULT now(),
    UNIQUE (brand_id, name),
    UNIQUE (brand_id, id)
);

CREATE TABLE tags (
    id         uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    brand_id   uuid NOT NULL REFERENCES brands ON DELETE CASCADE,
    name       citext NOT NULL CHECK (name <> ''),
    created_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (brand_id, name),
    UNIQUE (brand_id, id)
);

-- A CSV upload. Rows are staged in import_rows, checked, and only turned into
-- contacts once the gate passes and someone has signed the attestation.
CREATE TABLE imports (
    id               uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    brand_id         uuid NOT NULL REFERENCES brands ON DELETE CASCADE,
    created_by       uuid REFERENCES users ON DELETE SET NULL,
    file_name        text NOT NULL,
    list_kind        text NOT NULL DEFAULT 'subscribed' CHECK (list_kind IN ('subscribed', 'unsubscribed', 'cleaned')),
    mapping          jsonb NOT NULL DEFAULT '{}', -- CSV header → contact field
    source           text NOT NULL DEFAULT '', -- where the list came from, in the user's words
    wording_id       uuid,
    attestation_text text NOT NULL DEFAULT '',
    attested_by      uuid REFERENCES users ON DELETE SET NULL,
    attested_at      timestamptz,
    status           text NOT NULL DEFAULT 'uploaded'
                     CHECK (status IN ('uploaded', 'checking', 'checked', 'blocked', 'importing', 'done', 'failed', 'cancelled')),
    counts           jsonb NOT NULL DEFAULT '{}', -- rows per check result
    error            text NOT NULL DEFAULT '',
    created_at       timestamptz NOT NULL DEFAULT now(),
    finished_at      timestamptz,
    UNIQUE (brand_id, id),
    FOREIGN KEY (brand_id, wording_id) REFERENCES consent_wordings (brand_id, id),
    CHECK (attested_at IS NULL OR (attested_by IS NOT NULL AND attestation_text <> '' AND source <> ''))
);
CREATE INDEX imports_brand_created ON imports (brand_id, created_at DESC);

-- Staged CSV rows. Deleted once the import is done or cancelled.
CREATE TABLE import_rows (
    import_id  uuid NOT NULL REFERENCES imports ON DELETE CASCADE,
    line       integer NOT NULL, -- where the record starts in the file; the header is line 1
    email      text NOT NULL, -- as normalized, or raw when invalid
    fields     jsonb NOT NULL DEFAULT '{}',
    result     text NOT NULL DEFAULT 'pending',
    PRIMARY KEY (import_id, line)
);
CREATE INDEX import_rows_result ON import_rows (import_id, result);

-- One person at one brand. The same address at two brands is two contacts.
CREATE TABLE contacts (
    id               uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    brand_id         uuid NOT NULL REFERENCES brands ON DELETE CASCADE,
    email            citext NOT NULL CHECK (email::text = lower(email::text)),
    first_name       text NOT NULL DEFAULT '',
    last_name        text NOT NULL DEFAULT '',
    timezone         text NOT NULL DEFAULT '',
    country          text NOT NULL DEFAULT '' CHECK (country ~ '^([A-Z]{2})?$'),
    status           text NOT NULL CHECK (status IN ('pending_confirmation', 'subscribed', 'quarantined', 'unsubscribed')),
    tracking_allowed boolean NOT NULL DEFAULT true, -- false: EU without tracking consent, clicks are not recorded
    last_click_at    timestamptz, -- last human click; opens never count
    engaged_at       timestamptz, -- engagement learned elsewhere, counted like a click (4–5 star import)
    last_opt_in_at   timestamptz, -- last confirmed signup or preference change
    tier             text NOT NULL DEFAULT 'sunset' CHECK (tier IN ('active', 'warm', 'at_risk', 'sunset')),
    tier_updated_at  timestamptz NOT NULL DEFAULT now(),
    source           text NOT NULL CHECK (source IN ('api', 'import', 'manual')),
    import_id        uuid,
    unsubscribed_at  timestamptz,
    created_at       timestamptz NOT NULL DEFAULT now(),
    updated_at       timestamptz NOT NULL DEFAULT now(),
    UNIQUE (brand_id, email),
    UNIQUE (brand_id, id),
    FOREIGN KEY (brand_id, import_id) REFERENCES imports (brand_id, id) ON DELETE SET NULL (import_id)
);
CREATE INDEX contacts_brand_status_tier ON contacts (brand_id, status, tier);
CREATE INDEX contacts_brand_created ON contacts (brand_id, created_at DESC);
CREATE INDEX contacts_import ON contacts (import_id) WHERE import_id IS NOT NULL;

-- A row means subscribed to the topic until unsubscribed_at is set.
CREATE TABLE contact_topics (
    brand_id        uuid NOT NULL,
    contact_id      uuid NOT NULL,
    topic_id        uuid NOT NULL,
    subscribed_at   timestamptz NOT NULL DEFAULT now(),
    unsubscribed_at timestamptz,
    PRIMARY KEY (contact_id, topic_id),
    FOREIGN KEY (brand_id, contact_id) REFERENCES contacts (brand_id, id) ON DELETE CASCADE,
    FOREIGN KEY (brand_id, topic_id) REFERENCES topics (brand_id, id) ON DELETE CASCADE
);
CREATE INDEX contact_topics_topic ON contact_topics (topic_id) WHERE unsubscribed_at IS NULL;

CREATE TABLE contact_tags (
    brand_id   uuid NOT NULL,
    contact_id uuid NOT NULL,
    tag_id     uuid NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (contact_id, tag_id),
    FOREIGN KEY (brand_id, contact_id) REFERENCES contacts (brand_id, id) ON DELETE CASCADE,
    FOREIGN KEY (brand_id, tag_id) REFERENCES tags (brand_id, id) ON DELETE CASCADE
);
CREATE INDEX contact_tags_tag ON contact_tags (tag_id);

-- Proof of consent. Append-only: rows are never updated. They are deleted
-- only with their contact (GDPR erasure).
CREATE TABLE consent_records (
    id         bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    brand_id   uuid NOT NULL,
    contact_id uuid NOT NULL,
    wording_id uuid NOT NULL,
    method     text NOT NULL CHECK (method IN ('signup_form', 'double_opt_in', 'preference_center', 'import_attestation', 'manual')),
    source     text NOT NULL CHECK (source IN ('api', 'import', 'manual')),
    page_url   text NOT NULL DEFAULT '',
    ip         inet,
    import_id  uuid,
    created_at timestamptz NOT NULL DEFAULT now(),
    FOREIGN KEY (brand_id, contact_id) REFERENCES contacts (brand_id, id) ON DELETE CASCADE,
    FOREIGN KEY (brand_id, wording_id) REFERENCES consent_wordings (brand_id, id),
    FOREIGN KEY (brand_id, import_id) REFERENCES imports (brand_id, id) ON DELETE SET NULL (import_id),
    CHECK (import_id IS NULL OR source = 'import')
);
CREATE INDEX consent_records_contact ON consent_records (contact_id, created_at DESC);

-- Company-wide: an address suppressed by any brand is never sent to by any
-- brand. Keyed by sha256(lower(email)) so it survives erasing the contact.
CREATE TABLE suppressions (
    email_hash bytea PRIMARY KEY CHECK (length(email_hash) = 32),
    reason     text NOT NULL CHECK (reason IN ('hard_bounce', 'complaint', 'manual', 'erasure')),
    brand_id   uuid REFERENCES brands ON DELETE SET NULL, -- where it came from, if anywhere
    note       text NOT NULL DEFAULT '',
    created_by uuid REFERENCES users ON DELETE SET NULL,
    created_at timestamptz NOT NULL DEFAULT now()
);

-- +goose StatementBegin
CREATE FUNCTION forbid_update() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    RAISE EXCEPTION '% is append-only', TG_TABLE_NAME;
END;
$$;
-- +goose StatementEnd

CREATE TRIGGER consent_records_append_only BEFORE UPDATE ON consent_records
    FOR EACH ROW EXECUTE FUNCTION forbid_update();

-- +goose StatementBegin
CREATE FUNCTION consent_wording_text_fixed() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF NEW.text IS DISTINCT FROM OLD.text OR NEW.key IS DISTINCT FROM OLD.key THEN
        RAISE EXCEPTION 'consent wording % cannot be changed; retire it and add a new one', OLD.key;
    END IF;
    RETURN NEW;
END;
$$;
-- +goose StatementEnd

CREATE TRIGGER consent_wordings_text_fixed BEFORE UPDATE ON consent_wordings
    FOR EACH ROW EXECUTE FUNCTION consent_wording_text_fixed();

-- Same rules as contacts.TierAt in Go; a test keeps the two in step.
-- +goose StatementBegin
CREATE FUNCTION engagement_tier(last_click_at timestamptz, engaged_at timestamptz,
                                last_opt_in_at timestamptz, tracking_allowed boolean, at timestamptz)
RETURNS text LANGUAGE sql IMMUTABLE PARALLEL SAFE AS $$
    WITH s AS (
        -- GREATEST skips NULLs.
        SELECT CASE
            WHEN tracking_allowed THEN GREATEST(last_click_at, engaged_at, last_opt_in_at)
            ELSE last_opt_in_at
        END AS last
    )
    SELECT CASE
        WHEN last IS NULL THEN 'sunset'
        WHEN at - last <= interval '30 days' AND tracking_allowed THEN 'active'
        WHEN at - last <= interval '90 days' THEN 'warm'
        WHEN at - last <= interval '180 days' THEN 'at_risk'
        ELSE 'sunset'
    END FROM s
$$;
-- +goose StatementEnd

-- +goose Down
DROP TABLE suppressions;
DROP TABLE consent_records;
DROP TABLE contact_tags;
DROP TABLE contact_topics;
DROP TABLE contacts;
DROP TABLE import_rows;
DROP TABLE imports;
DROP TABLE tags;
DROP TABLE topics;
DROP TABLE consent_wordings;
DROP FUNCTION engagement_tier(timestamptz, timestamptz, timestamptz, boolean, timestamptz);
DROP FUNCTION consent_wording_text_fixed();
DROP FUNCTION forbid_update();
