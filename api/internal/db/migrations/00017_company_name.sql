-- +goose Up

-- The company's name, shown in the app's sidebar and on the sign-in page
-- (Settings → General). '' = not set yet: the app shows WEBAUTHN_RP_NAME.
ALTER TABLE company_state ADD COLUMN name text NOT NULL DEFAULT '' CHECK (length(name) <= 80);

-- +goose Down
ALTER TABLE company_state DROP COLUMN name;
