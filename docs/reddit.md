# Reddit ingestion — how it works

## No API keys for history

Reddit's developer API (OAuth apps + tokens) is built for *live* actions and
recent listings — it is not a way to pull your full history. Listings are capped
(you can page back only ~1000 items per view) and it exposes little of the long
tail (every vote, every save, subscriptions, messages). So Archivist does **not**
use Reddit API keys to build your archive.

Instead, Reddit is a **`Bundle`** provider: you request your official data export
from Reddit and upload the ZIP to Archivist. This is the only way to get
*complete* history, and it needs no credentials stored in the app.

## How you get the export (manual, one-time per refresh)

1. Go to **https://www.reddit.com/settings/data-request** (logged in).
2. Choose the scope (GDPR/CCPA "all time") and submit.
3. Reddit prepares it and emails a download link — this can take **up to 30 days**
   (often much faster). The download is a **ZIP of CSV files**.
4. Upload that ZIP to Archivist:

   ```
   POST /api/import?source=reddit        (multipart 'archive' = the ZIP)
   ```

   The upload is authenticated (session cookie) and answers `202` with a queued
   import job; see the README for the API.

   Re-import a fresh export any time — ingestion is idempotent: events upsert
   on a stable per-record id (`(user, account, source, external_id)`), and identical
   records within one archive are skipped by content hash, so overlapping data
   won't duplicate.

## What's in the export

Everything is CSV, one file per data type. Reddit doesn't publish an exhaustive,
stable schema and the exact set varies over time, but a typical export includes:

| Area | Files (typical) |
|---|---|
| Content | `posts.csv`, `comments.csv` |
| Reactions | `post_votes.csv`, `comment_votes.csv` (each row has a direction up/down), `poll_votes.csv` |
| Saved / hidden | `saved_posts.csv`, `saved_comments.csv`, `hidden_posts.csv` |
| Connections | `subscribed_subreddits.csv`, `friends.csv`, `moderated_subreddits.csv`, `approved_submitter_subreddits.csv`, `multireddits.csv` |
| Messages | `messages.csv` / `message_headers.csv`, `chat_history.csv` |
| Account | `statistics.csv`, `account_gender.csv`, `birthdate.csv`, `ip_logs.csv`, `linked_identities.csv`, `preferences`/settings |
| Other | `drafts.csv`, `scheduled_posts.csv`, `purchases.csv` / `transactions.csv`, `gilded`/gold, `announcements.csv` |

## What Archivist maps today

The `reddit` provider (`providers/reddit/reddit.go`) has one extractor per known
CSV and maps every row to an Event:

| File | Event type | Category |
|---|---|---|
| `posts.csv` | `reddit.post` | content |
| `comments.csv` | `reddit.comment` | content |
| `drafts.csv` | `reddit.draft` | content |
| `scheduled_posts.csv` | `reddit.scheduled_post` | content |
| `messages.csv`, `message_headers.csv` | `reddit.message` | message |
| `chat_history.csv` | `reddit.chat` | message |
| `post_votes.csv`, `comment_votes.csv` | `reddit.vote.up` / `reddit.vote.down` (`reddit.vote` when the direction is not recognised) | reaction |
| `poll_votes.csv` | `reddit.poll_vote` | reaction |
| `saved_posts.csv`, `saved_comments.csv` | `reddit.save` | save |
| `hidden_posts.csv` | `reddit.hide` | other |
| `subscribed_subreddits.csv` | `reddit.subscribe` | connection |
| `friends.csv` | `reddit.friend` | connection |
| `moderated_subreddits.csv`, `approved_submitter_subreddits.csv` | `reddit.moderate` | connection |
| `multireddits.csv` | `reddit.multireddit` | connection |
| `gilded_content.csv`, `gold_received.csv` (older: `gilded_posts.csv`, `gilded_comments.csv`) | `reddit.gild` | other |

The provider is tested against synthetic fixtures only
(`providers/reddit/testdata/`); see [`platforms.md`](./platforms.md).

**Respect-all-data:** any file *without* a mapper (e.g. `statistics.csv`,
`ip_logs.csv`, `purchases.csv`) is **not dropped** — it is captured as a generic
`reddit.unknown` Event (member name, size, content hash, short preview) and
listed in the import's coverage report. The uploaded ZIP itself is preserved
write-once, so after an extractor is added, **reprocessing** the import
(`POST /api/imports/{id}/reprocess`) maps those files with no re-upload.

Adding coverage for another file is one `csv(...)` extractor line + a
`Normalize` case — see `providers/reddit/`.

The `link` and `parent` columns of `comments.csv` (a post URL or a `t1_`/`t3_`
fullname) are reduced to the same `post:<id>` / `comment:<id>` ids the provider
uses for its own events, so replies and threads resolve within the archive.
