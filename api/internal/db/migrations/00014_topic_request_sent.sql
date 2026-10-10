-- +goose Up

-- When a topic request's confirmation email went out: jobs held back by the
-- brakes and run together later send it once.
ALTER TABLE contact_topic_requests ADD COLUMN sent_at timestamptz;

-- +goose Down
ALTER TABLE contact_topic_requests DROP COLUMN sent_at;
