-- +goose Up

-- Who a brand's email comes from. Campaigns never choose their own sender.
ALTER TABLE brands
    ADD COLUMN from_name  text NOT NULL DEFAULT '', -- '' = the brand name
    ADD COLUMN from_local text NOT NULL DEFAULT 'hello' CHECK (from_local ~ '^[a-z0-9][a-z0-9._-]{0,62}$'),
    ADD COLUMN reply_to   citext NOT NULL DEFAULT '';

-- Internal inboxes that get test sends (S10 "Send test"). SES mailbox
-- simulator addresses work here too.
CREATE TABLE seed_addresses (
    brand_id   uuid NOT NULL REFERENCES brands ON DELETE CASCADE,
    email      citext NOT NULL,
    added_by   uuid REFERENCES users ON DELETE SET NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (brand_id, email)
);

-- A campaign: one template sent to one topic's subscribers. Content and
-- sender are frozen when it's scheduled, so later template edits don't
-- change what goes out.
CREATE TABLE campaigns (
    id               uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    brand_id         uuid NOT NULL REFERENCES brands ON DELETE CASCADE,
    name             text NOT NULL CHECK (name <> '' AND length(name) <= 200),
    topic_id         uuid NOT NULL,
    template_id      uuid,
    subject          text NOT NULL DEFAULT '' CHECK (length(subject) <= 250),
    preheader        text NOT NULL DEFAULT '' CHECK (length(preheader) <= 250),
    include_at_risk  boolean NOT NULL DEFAULT false,
    state            text NOT NULL DEFAULT 'draft' CHECK (state IN
                     ('draft', 'needs_approval', 'scheduled', 'test_slice', 'holding', 'sending', 'paused', 'done', 'cancelled')),
    paused_from      text CHECK (paused_from IN ('test_slice', 'holding', 'sending')),
    pause_reason     text NOT NULL DEFAULT '',
    waiting_reason   text NOT NULL DEFAULT '', -- why a sending campaign isn't sending right now (warm-up cap)
    -- One worker at a time: a step takes the lease, and others back off.
    -- The holder's token lets only that step give it up.
    step_lease_until  timestamptz,
    step_lease_holder uuid,
    version          integer NOT NULL DEFAULT 1, -- bumped on every edit, for conflicts
    -- Frozen at scheduling:
    html             text NOT NULL DEFAULT '',
    text_body        text NOT NULL DEFAULT '',
    from_header      text NOT NULL DEFAULT '',
    reply_to         text NOT NULL DEFAULT '',
    test_slice_pct   numeric(5,2),
    hold_minutes     integer,
    scheduled_at     timestamptz, -- when the sender asked for it to go
    start_at         timestamptz, -- scheduled_at, but never inside the 10-minute cancel window
    confirmed_by     uuid REFERENCES users ON DELETE SET NULL,
    confirmed_at     timestamptz,
    approval_id      uuid,
    approval_reasons text[] NOT NULL DEFAULT '{}',
    hold_until       timestamptz,
    audience_count   integer NOT NULL DEFAULT 0,
    excluded_count   integer NOT NULL DEFAULT 0,
    created_by       uuid REFERENCES users ON DELETE SET NULL,
    updated_by       uuid REFERENCES users ON DELETE SET NULL,
    created_at       timestamptz NOT NULL DEFAULT now(),
    updated_at       timestamptz NOT NULL DEFAULT now(),
    finished_at      timestamptz,
    UNIQUE (brand_id, id),
    FOREIGN KEY (brand_id, topic_id) REFERENCES topics (brand_id, id),
    FOREIGN KEY (brand_id, template_id) REFERENCES templates (brand_id, id),
    CHECK ((state = 'paused') = (paused_from IS NOT NULL)),
    CHECK (state IN ('draft', 'needs_approval', 'cancelled') OR (html <> '' AND start_at IS NOT NULL AND confirmed_at IS NOT NULL))
);
CREATE INDEX campaigns_brand_updated ON campaigns (brand_id, updated_at DESC);
CREATE INDEX campaigns_running ON campaigns (state) WHERE state IN ('scheduled', 'test_slice', 'holding', 'sending');

-- Who a scheduled campaign goes to, decided once at scheduling (S5). A row
-- per topic member: included (stage set) or excluded (reason set).
-- contact_id has no foreign key: erasing a contact must not erase the
-- campaign's history.
CREATE TABLE campaign_audience (
    campaign_id     uuid NOT NULL REFERENCES campaigns ON DELETE CASCADE,
    contact_id      uuid NOT NULL,
    excluded_reason text CHECK (excluded_reason IN
                    ('suppressed', 'unsubscribed', 'quarantined', 'pending_confirmation', 'sunset', 'at_risk', 'frequency_cap')),
    stage           text CHECK (stage IN ('test', 'rest')),
    CHECK ((excluded_reason IS NULL) = (stage IS NOT NULL)),
    PRIMARY KEY (campaign_id, contact_id)
);
CREATE INDEX campaign_audience_reason ON campaign_audience (campaign_id, excluded_reason);

-- One email to one person. UNIQUE (campaign_id, contact_id): never twice.
-- A send is marked 'sending' when claimed and stamped called_at just before
-- SES is called; if the worker dies after that it becomes 'unknown' and is
-- never retried, because it may have gone out.
CREATE TABLE sends (
    id             bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    campaign_id    uuid NOT NULL REFERENCES campaigns ON DELETE CASCADE,
    brand_id       uuid NOT NULL,
    contact_id     uuid NOT NULL,
    stage          text NOT NULL CHECK (stage IN ('test', 'rest')),
    send_after     timestamptz NOT NULL, -- quiet hours: never 21:00–08:00 for the recipient
    status         text NOT NULL DEFAULT 'queued' CHECK (status IN ('queued', 'sending', 'sent', 'failed', 'skipped', 'unknown')),
    skip_reason    text NOT NULL DEFAULT '',
    attempts       integer NOT NULL DEFAULT 0,
    error          text NOT NULL DEFAULT '',
    ses_message_id text UNIQUE,
    claimed_at     timestamptz,
    -- Which claim holds it: a worker that stalled and lost the row to
    -- another can't mark, send or record it any more.
    claim_token    uuid,
    -- When this send took its place in the warm-up allowance and the
    -- frequency cap. A send that didn't go out gives both back.
    reserved_at    timestamptz,
    -- Set right before SES is called. A send stuck in 'sending' without it
    -- never reached SES and can safely go back to the queue; with it, the
    -- outcome is unknown and it is never retried.
    called_at      timestamptz,
    sent_at        timestamptz,
    UNIQUE (campaign_id, contact_id)
);
CREATE INDEX sends_queue ON sends (campaign_id, stage, send_after) WHERE status = 'queued';
CREATE INDEX sends_contact ON sends (contact_id);

-- +goose Down
DROP TABLE sends;
DROP TABLE campaign_audience;
DROP TABLE campaigns;
DROP TABLE seed_addresses;
ALTER TABLE brands DROP COLUMN reply_to, DROP COLUMN from_local, DROP COLUMN from_name;
