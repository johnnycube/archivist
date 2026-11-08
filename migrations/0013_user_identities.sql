-- +goose Up
-- (Migration-runner annotation. No Down section: migrations are forward-only.
-- The runner wraps this file in a transaction.)
--
-- OIDC login: an external identity — an identity provider's configured id and
-- the subject claim it asserts — linked to a user. A user may log in through
-- several providers; one identity belongs to exactly one user (the primary
-- key). email is the address the provider reported when the link was made,
-- kept for the operator's record; users.email stays the account's address.
CREATE TABLE user_identities (
    provider   text NOT NULL,
    subject    text NOT NULL,
    user_id    uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    email      text,
    created_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (provider, subject)
);
CREATE INDEX user_identities_user_idx ON user_identities (user_id);
