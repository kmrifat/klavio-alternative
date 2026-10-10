-- +goose Up

-- SES sending events (S7, S11), one row per event and recipient, read from
-- SQS. event_id is EventBridge's id: a redelivered message is stored once.
-- send_id is filled once the message is matched to a send.
CREATE TABLE email_events (
    id             bigserial PRIMARY KEY,
    event_id       text NOT NULL,
    type           text NOT NULL CHECK (type IN ('send', 'delivery', 'bounce', 'complaint', 'reject', 'delivery_delay',
                                                 'rendering_failure', 'open', 'click', 'subscription')),
    -- bounce: "permanent/General", "transient/MailboxFull", …; complaint: feedback type
    subtype        text NOT NULL DEFAULT '',
    brand_id       uuid REFERENCES brands ON DELETE CASCADE,
    ses_message_id text NOT NULL,
    send_id        bigint REFERENCES sends ON DELETE SET NULL, -- from the send_id tag, when it's one of ours
    email_hash     bytea NOT NULL DEFAULT '\x', -- the recipient this row is about ('' = not recipient-specific)
    occurred_at    timestamptz NOT NULL,
    received_at    timestamptz NOT NULL DEFAULT now(),
    detail         jsonb NOT NULL DEFAULT '{}',
    UNIQUE (event_id, email_hash)
);
CREATE INDEX email_events_brand ON email_events (brand_id, type, occurred_at);
CREATE INDEX email_events_message ON email_events (ses_message_id);
CREATE INDEX email_events_person ON email_events (email_hash, type, occurred_at);
CREATE INDEX email_events_send ON email_events (send_id, type) WHERE send_id IS NOT NULL;

-- Feedback that removes people: soft bounces (3 in 30 days) suppress too.
ALTER TABLE suppressions DROP CONSTRAINT suppressions_reason_check;
ALTER TABLE suppressions ADD CONSTRAINT suppressions_reason_check
    CHECK (reason IN ('hard_bounce', 'soft_bounce', 'complaint', 'manual', 'erasure'));
ALTER TABLE consent_records DROP CONSTRAINT consent_records_kind_check;
ALTER TABLE consent_records ADD CONSTRAINT consent_records_kind_check
    CHECK (kind IN ('opt_in', 'confirmed', 'import_attested', 'unsubscribed', 'resubscribed', 'complained', 'bounced'));
ALTER TABLE consent_records DROP CONSTRAINT consent_records_method_check;
ALTER TABLE consent_records ADD CONSTRAINT consent_records_method_check
    CHECK (method IN ('import', 'manual', 'api', 'link', 'header', 'ses'));

-- Clicks through the redirect (/c/<token>) and opens of the pixel. Bots are
-- marked, never deleted, so reports can show what was filtered.
CREATE TABLE clicks (
    id         bigserial PRIMARY KEY,
    send_id    bigint NOT NULL REFERENCES sends ON DELETE CASCADE,
    brand_id   uuid NOT NULL REFERENCES brands ON DELETE CASCADE,
    contact_id uuid NOT NULL,
    link_no    smallint NOT NULL,
    url        text NOT NULL,
    at         timestamptz NOT NULL DEFAULT now(),
    method     text NOT NULL DEFAULT 'GET',
    ip         text NOT NULL DEFAULT '',
    user_agent text NOT NULL DEFAULT '',
    is_bot     boolean NOT NULL DEFAULT false,
    bot_reason text NOT NULL DEFAULT ''
);
CREATE INDEX clicks_send ON clicks (send_id, at);
CREATE INDEX clicks_brand ON clicks (brand_id, at);

CREATE TABLE opens (
    id         bigserial PRIMARY KEY,
    send_id    bigint NOT NULL REFERENCES sends ON DELETE CASCADE,
    brand_id   uuid NOT NULL REFERENCES brands ON DELETE CASCADE,
    contact_id uuid NOT NULL,
    at         timestamptz NOT NULL DEFAULT now(),
    ip         text NOT NULL DEFAULT '',
    user_agent text NOT NULL DEFAULT '',
    is_machine boolean NOT NULL DEFAULT false, -- Apple Mail Privacy Protection, scanners
    reason     text NOT NULL DEFAULT ''
);
CREATE INDEX opens_send ON opens (send_id, at);

-- Brakes (§6.4). A brand's brake state; paused also sets brands.status.
ALTER TABLE brands ADD COLUMN brake_state text NOT NULL DEFAULT 'healthy'
    CHECK (brake_state IN ('healthy', 'watch', 'slowed', 'paused'));
ALTER TABLE company_state ADD COLUMN account_brake_at timestamptz;
ALTER TABLE company_state ADD COLUMN account_brake_reason text NOT NULL DEFAULT '';

-- What the brakes did (S2 "Recent safety events", S7, S8).
CREATE TABLE safety_events (
    id          bigserial PRIMARY KEY,
    at          timestamptz NOT NULL DEFAULT now(),
    scope       text NOT NULL CHECK (scope IN ('campaign', 'brand', 'account')),
    brand_id    uuid REFERENCES brands ON DELETE CASCADE,
    campaign_id uuid,
    action      text NOT NULL CHECK (action IN ('watch', 'slowed', 'paused', 'cleared', 'stopped')),
    from_state  text NOT NULL DEFAULT '',
    to_state    text NOT NULL DEFAULT '',
    reason      text NOT NULL,
    metrics     jsonb NOT NULL DEFAULT '{}'
);
CREATE INDEX safety_events_brand ON safety_events (brand_id, at DESC);
CREATE INDEX safety_events_campaign ON safety_events (campaign_id, at DESC) WHERE campaign_id IS NOT NULL;

-- +goose Down
DROP TABLE safety_events;
ALTER TABLE company_state DROP COLUMN account_brake_reason;
ALTER TABLE company_state DROP COLUMN account_brake_at;
ALTER TABLE brands DROP COLUMN brake_state;
DROP TABLE opens;
DROP TABLE clicks;
ALTER TABLE consent_records DROP CONSTRAINT consent_records_method_check;
ALTER TABLE consent_records ADD CONSTRAINT consent_records_method_check
    CHECK (method IN ('import', 'manual', 'api', 'link', 'header'));
ALTER TABLE consent_records DROP CONSTRAINT consent_records_kind_check;
ALTER TABLE consent_records ADD CONSTRAINT consent_records_kind_check
    CHECK (kind IN ('opt_in', 'confirmed', 'import_attested', 'unsubscribed', 'resubscribed'));
ALTER TABLE suppressions DROP CONSTRAINT suppressions_reason_check;
ALTER TABLE suppressions ADD CONSTRAINT suppressions_reason_check
    CHECK (reason IN ('hard_bounce', 'complaint', 'manual', 'erasure'));
DROP TABLE email_events;
