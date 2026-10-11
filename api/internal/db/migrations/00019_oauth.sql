-- +goose Up

-- MCP sign-in (OAuth 2.1) for AI clients such as claude.ai and Claude
-- Desktop connectors. A client registers itself (RFC 7591), sends a person
-- here to approve, and gets an agent token for that person.

-- Registered clients. Public clients only (no secret): PKCE protects them.
CREATE TABLE oauth_clients (
    id            text PRIMARY KEY,                       -- client_id
    name          text NOT NULL CHECK (length(name) <= 100), -- client_name, shown when approving
    redirect_uris text[] NOT NULL CHECK (cardinality(redirect_uris) BETWEEN 1 AND 5),
    created_at    timestamptz NOT NULL DEFAULT now()
);

-- One sign-in: asked for by a client, decided by a person in the console
-- within 10 minutes, then exchanged once for a token within 5 minutes.
CREATE TABLE oauth_grants (
    id              uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    client_id       text NOT NULL REFERENCES oauth_clients ON DELETE CASCADE,
    redirect_uri    text NOT NULL,
    state           text NOT NULL DEFAULT '',
    code_challenge  text NOT NULL,                -- PKCE, S256
    created_at      timestamptz NOT NULL DEFAULT now(),
    expires_at      timestamptz NOT NULL,         -- to approve or deny
    user_id         uuid REFERENCES users ON DELETE CASCADE,
    approved_at     timestamptz,
    denied_at       timestamptz,
    code_hash       bytea UNIQUE,
    code_expires_at timestamptz,
    exchanged_at    timestamptz,
    agent_token_id  uuid REFERENCES agent_tokens ON DELETE SET NULL
);
CREATE INDEX oauth_grants_created ON oauth_grants (created_at);

-- Which client a token was made for, when it came from a sign-in.
ALTER TABLE agent_tokens ADD COLUMN oauth_client_id text REFERENCES oauth_clients ON DELETE SET NULL;

-- +goose Down
ALTER TABLE agent_tokens DROP COLUMN oauth_client_id;
DROP TABLE oauth_grants;
DROP TABLE oauth_clients;
