-- +goose Up

-- A finished campaign's feedback totals, saved before its raw SES events are
-- deleted (they're kept 13 months; these forever). Reports read these once
-- they exist, so an old campaign's numbers never change.
CREATE TABLE campaign_stats (
    campaign_id     uuid PRIMARY KEY REFERENCES campaigns ON DELETE CASCADE,
    delivered       int NOT NULL,
    bounces         int NOT NULL, -- permanent
    complaints      int NOT NULL,
    soft_bounces    int NOT NULL,
    delivery_delays int NOT NULL,
    providers       jsonb NOT NULL, -- the report's per-provider rows
    saved_at        timestamptz NOT NULL DEFAULT now()
);

-- +goose Down
DROP TABLE campaign_stats;
