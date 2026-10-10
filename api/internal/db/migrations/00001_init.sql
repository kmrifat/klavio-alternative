-- +goose Up
-- Case-insensitive text for email addresses (used from M2 on).
CREATE EXTENSION IF NOT EXISTS citext;

-- +goose Down
DROP EXTENSION IF EXISTS citext;
