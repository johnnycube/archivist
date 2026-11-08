# Changelog

All notable changes to this project are documented here.
Format: Keep a Changelog. Versioning: Semantic Versioning.
## [0.1.0] - 2026-11-08

First public release.

### Added
- Import the official data-export ZIPs of 14 services (Reddit, ChatGPT, Claude, X/Twitter, Instagram, Facebook, Google Takeout, WhatsApp, TikTok, Telegram, Snapchat, Spotify, LinkedIn, Discord) into one searchable, filterable timeline
- Keep every original archive write-once: reprocessing brings provider improvements to data imported earlier, and whatever a provider does not recognise is captured instead of dropped
- Store media under the hash of its content, so two exports with a file of the same name each keep their own
- Bound every import: file, record and event limits, a time budget, cancelling, and a storage quota that counts media
- Multi-user with closed signup by default, `archivist admin` commands for the operator, Argon2id passwords and optional OpenID Connect login
- Filesystem blob storage by default, S3-compatible storage through a compose overlay
- Container image for linux/amd64 and linux/arm64, with an SBOM and build provenance, signed keyless with cosign

Only the Facebook provider was developed against a real export. The other 13 are tested against synthetic fixtures and are marked experimental in the UI.
