# Upgrading

v0.1.0 is the first public release, so there is nothing to upgrade from yet. A
release that needs more than pulling the new image gets a section here.

## Before any upgrade

**Back up first** (README, "Backups"). Migrations run at startup and are
forward-only, so a backup is the way back.

A migration that builds an index on `events` makes writes wait while it runs.
As a guide: on 600,000 short test events, a timeline index took half a second
to build and the search index 1.5 seconds. Long message texts make the search
index slower to build.
