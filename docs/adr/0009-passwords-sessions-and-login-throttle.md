# Argon2id passwords, opaque hashed sessions, throttle in the shared store

- Status: accepted
- Date: 2026-10-21 (records a decision made at the start of the project)

## Context

The app holds people's complete social and chat history, is multi-user, and
is exposed to the internet by its operator. Early builds hashed passwords
with PBKDF2 and had no login throttling.

## Decision

Passwords are hashed with Argon2id; legacy PBKDF2 hashes still verify and are
rehashed on login (until the first public release, which dropped PBKDF2 verification); a dummy hash is computed for unknown emails so timing does
not reveal accounts. Sessions are opaque random tokens stored as SHA-256
hashes, with absolute and idle timeouts, replaced atomically on login, and
carried in a `__Host-` cookie when Secure. Login attempts are throttled
through the `AttemptLimiter` port in the shared store (per account and
client, and per account), not in process memory, so every instance sees the
same counts.

## Consequences

- A leaked database does not yield usable sessions or passwords.
- The throttle counts and records in two steps, so parallel attempts can
  overshoot a limit by their number, bounded by the per-IP rate limit.
- MFA, WebAuthn and OIDC remain open; the session model does not preclude
  them.
