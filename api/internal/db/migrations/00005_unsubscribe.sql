-- +goose Up
-- Which campaign an unsubscribe came from (one-click header or footer link),
-- so the M4 brake can compute a campaign's unsubscribe rate.
ALTER TABLE consent_records ADD COLUMN campaign_id uuid;
CREATE INDEX consent_records_campaign_unsub ON consent_records (campaign_id) WHERE kind = 'unsubscribed';

-- +goose Down
DROP INDEX consent_records_campaign_unsub;
ALTER TABLE consent_records DROP COLUMN campaign_id;
