-- +goose Up

-- AI agents (MCP). A person creates a token for their agent; the agent then
-- acts as that person, with at most their role and never a passkey
-- confirmation. Only a hash of the token is stored; the prefix is what
-- people see.
CREATE TABLE agent_tokens (
    id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id      uuid NOT NULL REFERENCES users ON DELETE CASCADE,
    name         text NOT NULL CHECK (name <> '' AND length(name) <= 60),
    prefix       text NOT NULL UNIQUE,
    token_hash   bytea NOT NULL UNIQUE,
    created_at   timestamptz NOT NULL DEFAULT now(),
    expires_at   timestamptz NOT NULL,
    revoked_at   timestamptz,
    last_used_at timestamptz,
    CHECK (expires_at <= created_at + interval '90 days')
);
CREATE INDEX agent_tokens_user ON agent_tokens (user_id, created_at DESC);

-- Which agent token did it, for entries made through an agent: the token's
-- name and prefix, e.g. 'Claude Code (kag_3f2a9c1d)'.
ALTER TABLE audit_log ADD COLUMN via_agent text NOT NULL DEFAULT '';

-- An erasure may rewrite only target_id, before and after; via_agent is
-- now one of the columns it must leave alone.
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION audit_log_redact_only() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF current_setting('app.erasure', true) IS DISTINCT FROM 'on' THEN
        RAISE EXCEPTION 'audit_log is append-only';
    END IF;
    IF (NEW.id, NEW.at, NEW.actor_id, NEW.actor_email, NEW.brand_id, NEW.action, NEW.target_type, NEW.ip, NEW.user_agent, NEW.via_agent)
       IS DISTINCT FROM (OLD.id, OLD.at, OLD.actor_id, OLD.actor_email, OLD.brand_id, OLD.action, OLD.target_type, OLD.ip, OLD.user_agent, OLD.via_agent) THEN
        RAISE EXCEPTION 'an erasure may only redact target_id, before and after';
    END IF;
    RETURN NEW;
END;
$$;
-- +goose StatementEnd

-- A send an agent prepared may be approved by the person whose agent it is:
-- the agent preparing and a person approving are already two parties. Every
-- other request still needs someone other than the requester.
ALTER TABLE approvals DROP CONSTRAINT approvals_check;
ALTER TABLE approvals ADD CONSTRAINT approvals_not_own CHECK (decided_by IS NULL OR decided_by <> requested_by OR kind = 'agent_send');

-- +goose Down
ALTER TABLE approvals DROP CONSTRAINT approvals_not_own;
ALTER TABLE approvals ADD CONSTRAINT approvals_check CHECK (decided_by IS NULL OR decided_by <> requested_by);
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION audit_log_redact_only() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF current_setting('app.erasure', true) IS DISTINCT FROM 'on' THEN
        RAISE EXCEPTION 'audit_log is append-only';
    END IF;
    IF (NEW.id, NEW.at, NEW.actor_id, NEW.actor_email, NEW.brand_id, NEW.action, NEW.target_type, NEW.ip, NEW.user_agent)
       IS DISTINCT FROM (OLD.id, OLD.at, OLD.actor_id, OLD.actor_email, OLD.brand_id, OLD.action, OLD.target_type, OLD.ip, OLD.user_agent) THEN
        RAISE EXCEPTION 'an erasure may only redact target_id, before and after';
    END IF;
    RETURN NEW;
END;
$$;
-- +goose StatementEnd
ALTER TABLE audit_log DROP COLUMN via_agent;
DROP TABLE agent_tokens;
