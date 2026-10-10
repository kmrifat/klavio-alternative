-- +goose Up
-- Which campaign an unsubscribe came from (one-click header or footer link),
-- so the M4 brake can compute a campaign's unsubscribe rate.
ALTER TABLE consent_records ADD COLUMN campaign_id uuid;
CREATE INDEX consent_records_campaign_unsub ON consent_records (campaign_id) WHERE kind = 'unsubscribed';

-- What a contact was before unsubscribing. The preference center may only
-- re-subscribe people who were properly subscribed (not awaiting double
-- opt-in, not quarantined).
ALTER TABLE contacts ADD COLUMN status_before_unsubscribe text
    CHECK (status_before_unsubscribe IN ('pending_confirmation', 'subscribed', 'quarantined'));

-- +goose Down
ALTER TABLE contacts DROP COLUMN status_before_unsubscribe;
DROP INDEX consent_records_campaign_unsub;
ALTER TABLE consent_records DROP COLUMN campaign_id;
