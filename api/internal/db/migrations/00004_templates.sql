-- +goose Up

-- One locked layout per brand: header, footer, unsubscribe link and postal
-- address. Templates are compiled inside it. No row = the default layout.
CREATE TABLE brand_layouts (
    brand_id   uuid PRIMARY KEY REFERENCES brands ON DELETE CASCADE,
    mjml       text NOT NULL CHECK (mjml LIKE '%{{content}}%'),
    version    integer NOT NULL DEFAULT 1 CHECK (version > 0),
    updated_by uuid REFERENCES users ON DELETE SET NULL,
    updated_at timestamptz NOT NULL DEFAULT now()
);

-- Domains a brand's emails may link to, besides its own. Subdomains match.
CREATE TABLE link_allowlist (
    brand_id   uuid NOT NULL REFERENCES brands ON DELETE CASCADE,
    domain     citext NOT NULL CHECK (domain ~ '^([a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?\.)+[a-z]{2,63}$'),
    added_by   uuid REFERENCES users ON DELETE SET NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (brand_id, domain)
);

-- Bumped whenever a brand's layout or allowlist changes. Templates record the
-- generation they were built against; older ones are rebuilt in the
-- background (a template with built_generation < generation is stale).
CREATE TABLE template_generations (
    brand_id   uuid PRIMARY KEY REFERENCES brands ON DELETE CASCADE,
    generation bigint NOT NULL DEFAULT 0 CHECK (generation >= 0)
);

-- The body of an email. html/text_body are the compiled result inside the
-- brand layout (merge tags still in place); issues are the last check run.
CREATE TABLE templates (
    id             uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    brand_id       uuid NOT NULL REFERENCES brands ON DELETE CASCADE,
    name           text NOT NULL CHECK (name <> '' AND length(name) <= 200),
    mjml           text NOT NULL,
    html           text NOT NULL DEFAULT '',
    text_body      text NOT NULL DEFAULT '',
    issues         jsonb NOT NULL DEFAULT '[]',
    error_count    integer NOT NULL DEFAULT 0,
    warning_count  integer NOT NULL DEFAULT 0,
    layout_version integer NOT NULL DEFAULT 0, -- 0 = compiled with the default layout
    built_generation bigint NOT NULL DEFAULT 0, -- template_generations.generation it was compiled against
    version        integer NOT NULL DEFAULT 1, -- bumped on every save, for edit conflicts
    created_by     uuid REFERENCES users ON DELETE SET NULL,
    updated_by     uuid REFERENCES users ON DELETE SET NULL,
    created_at     timestamptz NOT NULL DEFAULT now(),
    updated_at     timestamptz NOT NULL DEFAULT now(),
    archived_at    timestamptz,
    UNIQUE (brand_id, id),
    -- A template with errors has no usable HTML to send.
    CHECK (error_count > 0 OR html <> '')
);
CREATE UNIQUE INDEX templates_brand_name ON templates (brand_id, lower(name)) WHERE archived_at IS NULL;
CREATE INDEX templates_brand_updated ON templates (brand_id, updated_at DESC) WHERE archived_at IS NULL;

-- +goose Down
DROP TABLE templates;
DROP TABLE template_generations;
DROP TABLE link_allowlist;
DROP TABLE brand_layouts;
