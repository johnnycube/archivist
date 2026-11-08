# Linking a provider login by email needs a verified account email

- Status: accepted; amends [0013](0013-oidc-login-behind-a-provider-port.md)
- Date: 2026-11-07

## Context

ADR 0013 attached a first provider login to the existing account with the
same email whenever the provider marked the email verified. That guards only
one side. A password signup never proves its address, so anyone could
register `victim@example.com` with a password before the victim; the victim's
first "Continue with Google" then landed in the attacker's account (account
pre-hijacking), with signup open and `LINK_BY_EMAIL=1` — both defaults. The
same gap existed for accounts created by a provider login: auto-signup did not
require the provider to have verified the address.

## Decision

- An account carries `email_verified` (migration 0014). It becomes true only
  when an identity provider vouches for the address: an account created by a
  provider login from a verified email, or a link whose verified email equals
  the account's.
- A first provider login is attached by email only when the provider verified
  the address **and** the account's own email is verified. Otherwise the
  login is refused with `email_in_use`.
- Auto-signup through a provider requires a verified email
  (`email_unverified` otherwise).
- A signed-in user links a provider explicitly from the account page:
  `POST /api/auth/oidc/{id}/link` (session + CSRF check) starts an attempt
  bound to that user; the callback links the identity to the session's user,
  and only when it is the user the attempt was started for. The attempt
  cookie is unsigned, so the user id in it is only compared, never trusted.
- No backfill: no existing account is marked verified, because before this
  change none of them was proven.

## Consequences

- A squatted password account can no longer capture a provider login; at
  worst it blocks that email's provider signup until the operator deletes it.
- Password accounts that relied on automatic email linking now link once from
  the account page.
- The account page lists linked providers; unlinking is not offered yet.
