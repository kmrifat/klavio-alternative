-- +goose Up

-- Blocklist checks: claiming a lookup and finishing it are separate, so a
-- worker that dies after claiming only blocks the next try for a few
-- minutes, not until the next interval.
ALTER TABLE blocklist_checks ADD COLUMN checked_at timestamptz; -- last lookup that finished
UPDATE blocklist_checks SET checked_at = claimed_at;

-- Clicks: when the recorder wrote it. A click settles only once its send
-- has been quiet for the settle window, by recorded time, so a lagging
-- recorder can't settle a click before the rest of its burst arrives.
ALTER TABLE clicks ADD COLUMN recorded_at timestamptz NOT NULL DEFAULT now();
CREATE INDEX clicks_send_recorded ON clicks (send_id, recorded_at);

-- Engagement evidence from outside our click tracking (the old tool's
-- last click or a good rating, at import). With it, a contact's engagement
-- can be recomputed when a click it counted turns out to be a scanner.
ALTER TABLE contacts ADD COLUMN imported_click_at timestamptz;
ALTER TABLE contacts ADD COLUMN imported_engaged_at timestamptz;
UPDATE contacts c SET imported_click_at = c.last_click_at
    WHERE c.last_click_at IS NOT NULL AND NOT EXISTS (SELECT 1 FROM clicks k WHERE k.contact_id = c.id AND k.at = c.last_click_at);
UPDATE contacts c SET imported_engaged_at = c.last_engaged_at
    WHERE c.last_engaged_at IS NOT NULL AND NOT EXISTS (SELECT 1 FROM clicks k WHERE k.contact_id = c.id AND k.at = c.last_engaged_at);

-- +goose Down
ALTER TABLE contacts DROP COLUMN imported_engaged_at;
ALTER TABLE contacts DROP COLUMN imported_click_at;
DROP INDEX clicks_send_recorded;
ALTER TABLE clicks DROP COLUMN recorded_at;
ALTER TABLE blocklist_checks DROP COLUMN checked_at;
