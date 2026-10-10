-- +goose Up

-- Warm-up (S1/S2): a new brand's daily volume rises over ~21 days. The
-- schedule starts on the first day it actually sends.
ALTER TABLE brands ADD COLUMN warmup_started_on date;

-- Marketing emails sent per brand per UTC day, recorded by the sending
-- worker. Warm-up caps are measured against it.
CREATE TABLE brand_daily_sends (
    brand_id uuid NOT NULL REFERENCES brands ON DELETE CASCADE,
    day      date NOT NULL,
    sent     int NOT NULL DEFAULT 0 CHECK (sent >= 0),
    PRIMARY KEY (brand_id, day)
);

-- The latest health score per brand (S2), recomputed hourly.
CREATE TABLE brand_health (
    brand_id    uuid PRIMARY KEY REFERENCES brands ON DELETE CASCADE,
    score       int NOT NULL CHECK (score BETWEEN 0 AND 100),
    components  jsonb NOT NULL, -- authentication, reputation, list_quality, engagement
    computed_at timestamptz NOT NULL DEFAULT now()
);

-- Domain blocklist lookups (Spamhaus DBL, SURBL, URIBL), cached.
CREATE TABLE blocklist_results (
    brand_id   uuid NOT NULL REFERENCES brands ON DELETE CASCADE,
    zone       text NOT NULL,
    domain     citext NOT NULL,
    status     text NOT NULL CHECK (status IN ('clean', 'listed', 'unknown')),
    detail     text NOT NULL DEFAULT '',
    checked_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (brand_id, zone, domain)
);

-- Google Postmaster Tools daily stats for each brand's sending domain.
CREATE TABLE postmaster_stats (
    brand_id          uuid NOT NULL REFERENCES brands ON DELETE CASCADE,
    day               date NOT NULL,
    spam_ratio        double precision, -- 0.0004 = 0.04%
    domain_reputation text NOT NULL DEFAULT '', -- HIGH | MEDIUM | LOW | BAD
    fetched_at        timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (brand_id, day)
);

-- +goose Down
DROP TABLE postmaster_stats;
DROP TABLE blocklist_results;
DROP TABLE brand_health;
DROP TABLE brand_daily_sends;
ALTER TABLE brands DROP COLUMN warmup_started_on;
