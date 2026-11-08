# OpenID Connect login behind a provider port, identities linked to users

- Status: accepted; the email-linking rule is amended by [0014](0014-oidc-email-linking-needs-a-verified-account.md)
- Date: 2026-10-21

## Context

Password login alone does not fit a deployment where the people are already
in Gitea, Keycloak or a public identity provider, and the operator wants
several of those side by side. The use-case layer must stay free of the
protocol library, the session model must not change, and an identity must
never be attached to the wrong account.

## Decision

OpenID Connect, authorization-code flow with PKCE, behind an `OIDCProvider`
port in `internal/app` (authorization URL in, verified claims out);
`internal/adapters/oidc` implements it with go-oidc and x/oauth2 and
verifies the ID token's signature, issuer, audience, expiry and nonce. Any
number of providers are configured as `ARCHIVIST_OIDC_PROVIDERS` plus
`ARCHIVIST_OIDC_<ID>_*` and discovered at boot; discovery failure is a boot
failure. The REST adapter keeps one attempt's state, nonce and PKCE verifier
in a short-lived `__Host-` cookie, so only the browser that started a login
can finish it. Identities are rows of `user_identities (provider, subject)
→ user`; a login matches a linked account first, then the account with the
same email only when the provider marks it verified, then creates an account
if the provider allows it. Accounts created this way have no password until
their owner sets one. The existing session creation is reused unchanged.

## Consequences

- The protocol library is replaceable; the use case and its tests run
  against a fake provider.
- Linking by unverified email is refused (`email_in_use`), which is what
  stops an attacker's provider account from taking over a password account
  that shares the address.
- `ALLOW_SIGNUP=0` governs the password signup only; a provider's
  `AUTO_SIGNUP=0` restricts it to existing accounts.
- A user may hold several identities; there is no UI yet to list or unlink
  them, and no login through a provider without an email claim.
