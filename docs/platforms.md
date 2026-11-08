# Platforms — supported today, and what to add next

Archivist ingests **data exports** (the ZIP a platform hands you on request), so
a platform is only a candidate if it has an export that is (a) available to
ordinary users, (b) machine-readable, and (c) contains the history, not just
account settings. Reach alone is not enough — WeChat has 1.4 billion users and
no usable export.

## Supported today

How to request each of these exports, and which options to choose: [`exports.md`](./exports.md).

| Provider | Export format | Event types |
|---|---|---|
| `reddit` | ZIP of CSV | post, comment, draft, scheduled_post, message, chat, vote(.up/.down), poll_vote, save, hide, subscribe, friend, moderate, multireddit, gild |
| `chatgpt` | `conversations.json` (+ `conversations-NNN.json` parts) | message (threaded; all branches with an `on_current_branch` flag; code/tool/multimodal content) |
| `claude` | `conversations.json`, `projects.json` | message (threaded), project, project_doc |
| `twitter` | `data/*.js` (`window.YTD…`; split parts `*-partN.js` read) | tweet (incl. deleted/community/circle, replies, RT-prefixed retweets, media), note_tweet, moment, dm, dm_event, like, follow, follower, block, mute, list, saved_search, account |
| `instagram` | Meta JSON (choose JSON, not HTML) | post (feed, reels `via: reels`, IGTV, archived), thread_post (Threads), comment, story, message (DMs incl. reactions, shares, attachments, calls), like, save, follow (following, close friends), follower, follow_request, unfollow (unfollowed, blocked), search, login |
| `facebook` | Meta JSON | post, comment, app_post, message (Messenger), reaction, friend, friend_request, follow, unfollow, group_join, event, app, search, photo, profile_update, login |
| `google` | Google Takeout ZIP (multi-part OK). Choose **JSON** for "My Activity" and for YouTube "History" — the HTML default stays `google.unknown`. Optional: the on-device Maps Timeline export (`Timeline.json`) can be added to the ZIP. | youtube.watch, youtube.search, youtube.comment, youtube.subscribe, youtube.playlist_item, youtube.like; search, visit, chrome.search, chrome.visit, chrome.bookmark, maps.activity, maps.saved, maps.review, play.activity, assistant.query, gemini.prompt, discover.view, ads.view, shopping.activity, books.activity, activity (any other product); location.visit, location.trip, location.point (Records.json is millions of events); keep.note; chat.message, hangouts.message; photos.photo |
| `whatsapp` | one "Export chat" ZIP per chat (`_chat.txt` / `WhatsApp Chat with ….txt` + media); any phone language/date format — d/m/y, m/d/y, y/m/d, 2- or 4-digit years, 12h/24h, with/without seconds, bracketed or dashed headers | message, system |
| `tiktok` | `user_data_tiktok.json` (JSON) or the TXT flavour (one `Key: Value` file per section) | watch, search, like, save, follow, follower, block, share, comment, post, message, login |
| `telegram` | Telegram Desktop export folder, zipped (`result.json` + media) | message (text, media, polls, locations, contacts, reactions, replies incl. cross-chat), service (all actions), contact |
| `snapchat` | "My Data" ZIP (`json/`; per-conversation and legacy Received/Sent shapes) | message, snap, memory, friend, friend_request, search, login, call, story, subscription |
| `spotify` | extended streaming history and/or account data ZIP | play, search, save, playlist, playlist_item |
| `linkedin` | ZIP of CSV | connection, message, post, comment, reaction, invitation, search, position, education, follow, save |
| `discord` | data package ZIP (`messages.json` and older `messages.csv`) | message (own messages only; attachments are CDN links), channel, server (index + guild.json), friend |

> **Verification status.** `facebook` was developed against a real export
> (`Maturity: verified` in its descriptor); every other source is marked
> **experimental** in the import form until someone checks it against a real
> export. Found a gap? Open an issue with the coverage report ("Copy report" on
> the import page) — never the export itself.
>
> `facebook` was developed against a real export.
> `google`, `whatsapp`, `tiktok`, `telegram`, `snapchat`, `spotify`, `linkedin`,
> `discord` and the newer `instagram` types were written from the documented
> export formats, reconciled against vendor documentation and well-known
> open-source parsers of each format (see the provider package comments for the
> sources), fuzz-tested, and checked with **synthetic fixtures only**. Expect to adjust
> file names, keys and date formats on first contact with a real export —
> anything not recognised still lands in `<source>.unknown`, so nothing is lost
> and a re-import after a fix is idempotent.

Everything a provider does not recognise is kept as `<source>.unknown`.

### Gaps in providers we already have

- **Twitter/X**: bookmarks are not part of the X archive (no bookmarks data type
  in any known manifest; confirmed by the archive README and community parsers),
  so they cannot be imported. DM sender ids are kept raw (`sender_id`); the
  account's own id is in the `twitter.account` event from `account.js`.
  `tweetdeck.js`, `ad-*.js`, `personalization.js` and the `*-headers.js` files
  stay `twitter.unknown`.
- **Instagram**: personal information, ads/posts viewed, shopping, story polls and
  quizzes, saved collections are still captured generically.
- **WhatsApp**: two-digit-year-first dates (yy-mm-dd) are read as dd-mm-yy;
  timestamps have no timezone and are stored as UTC; day/month order is decided
  per file and flagged in metadata when it had to be guessed.
- **Snapchat**: chat media is not part of the export (media IDs only); memory
  download links expire.
- **Google Takeout**: Gmail (mbox), Calendar (ics), Contacts (vcf), Drive, Fit,
  Play Store purchases/installs, Pay, Tasks, Voice, Blogger, Home, Access Log
  and the other small products stay `google.unknown`; the HTML flavour of My
  Activity is not parsed (choose JSON). Google Chat's `created_date` and the
  My Activity sentence frames are only understood in English and German (the
  search query is still read from the URL in any language); other languages
  keep the raw sentence in `metadata.raw_title` / stay undated. Chrome visits
  appear twice when both `Chrome/History.json` and My Activity → Chrome are
  exported (different timestamp precision). Gemini responses are not in Takeout.
- **WhatsApp (iOS)**: the chat name exists only in the uploaded ZIP's file
  name. The provider reads it from there (`RawRecord.Archive`) as long as the
  upload keeps WhatsApp's own name (`WhatsApp Chat - <name>.zip`); a renamed
  ZIP is titled "WhatsApp chat", and because the chat name is part of the event
  ids, importing the same chat under both names yields two sets of events.
  Timestamps carry no zone and are stored as UTC.
- **Claude**: messages whose text is empty are skipped.

## Candidates (historical ranking)

Rows 1–8 are **implemented** (see above); the table is kept for the reasoning
and the usage figures.

Ordered by global reach weighed against how good the export is. Users are
monthly actives, rounded, from public 2026 rankings (sources below) — treat them
as an ordering, not as precise figures.

| # | Platform | Users (MAU) | Export | What it would add | Effort |
|---|---|---|---|---|---|
| 1 | **Google Takeout** (YouTube, Search, Maps, Chrome, Gemini) | YouTube 2.5–2.85 B | ZIP, split into 2–50 GB parts; JSON/CSV/HTML selectable per product | `youtube.watch`, `.search`, `.comment`, `.subscribe`, `.like`, playlists; later `google.search`, Maps timeline, `gemini.message`. First big `consumption` source | Medium. Multi-part import already exists. Must require JSON for My Activity (the default is HTML) |
| 2 | **WhatsApp** | 2.8–3 B | Per-chat "Export chat" → ZIP with `_chat.txt` + media. No whole-account export (the "account info" report has no messages) | `whatsapp.message` — for most people the most valuable history there is | Medium. Plain-text parser; the line/date format depends on phone locale and OS, so it needs a tolerant parser and fixtures per variant. One upload per chat |
| 3 | **TikTok** | ~2 B | ZIP with one large `user_data_tiktok.json` (JSON must be chosen; default is TXT) | watch history, likes, favourites, comments, DMs, searches, own posts (as links), follows | Low–medium. Deeply nested arrays in a single file — what `bundle.StreamJSONArrays` is for |
| 4 | **Telegram** | ~1 B | Telegram Desktop → "Export Telegram data" → `result.json` + media folders (a folder; the user zips it) | `telegram.message` across private chats, groups and channels, contacts | Low–medium. One big JSON; message `text` is a string *or* an array of entities |
| 5 | **Snapchat** | 850–900 M | "My Data" ZIP of JSON (`chat_history.json`, `snap_history.json`, `memories_history.json`, friends, search) | messages, snap metadata, memories, friends | Low. Memories are download links that expire, not files |
| 6 | **Spotify** | ~780 M | "Extended streaming history" ZIP: `Streaming_History_Audio_*.json` | `spotify.play` (every track/podcast with ms played), searches, playlists | Low. Very high volume — hundreds of thousands of events per user; worth a think about timeline noise first |
| 7 | **LinkedIn** | ~310 M MAU (1 B+ members) | ZIP of CSV (`Connections.csv`, `messages.csv`, `Shares.csv`, `Reactions.csv`, `Comments.csv`, `Invitations.csv`, `SearchQueries.csv`) | connections, messages, posts, reactions, searches | Low. Same shape of work as `reddit`; some CSVs start with a notes preamble to skip |
| 8 | **Discord** | ~200 M | Data package ZIP: `messages/c<id>/messages.json` + `channel.json`, servers, activity | `discord.message` (own messages only), servers joined | Low. Only your side of each conversation is included |
| 9 | **Pinterest** | 540–580 M | Request form; delivered as loosely structured HTML | pins, boards | High for little value — parked until the export improves |

Small but nearly free, if wanted: **Mastodon** (ActivityPub `outbox.json` archive)
and **Bluesky** (repo export), both open, documented formats.

### Not feasible as export providers

| Platform | Users | Why |
|---|---|---|
| WeChat | ~1.4 B | No chat-history export; the data request covers profile data only |
| Messenger | ~1 B | Already covered — it ships inside the Facebook export (`facebook.message`). End-to-end-encrypted chats need the separate "secure storage" download |
| Douyin, Kuaishou | 700–750 M each | China-only, no practical user export |
| Signal | ~70 M | Only an encrypted device backup, by design |

## What is next

1. Verify each synthetic-fixture provider against a real export (see the
   verification note above) — this is worth more than any new platform.
2. (done) Twitter/X DMs — bookmarks are not in the archive.
3. Google Takeout: Gmail, Calendar, Play Store purchases, Access Log.
4. Decide how high-volume `consumption` events (`spotify.play`,
   `google.youtube.watch`, `tiktok.watch`) should appear on the timeline; they
   can outnumber everything else by orders of magnitude.
5. Pinterest, Mastodon, Bluesky if wanted.

## Sources

Usage figures (accessed September 2026):

- [DataReportal — Global Social Media Statistics](https://datareportal.com/social-media-users)
- [Visual Capitalist — The Biggest Social Media Platforms in 2026](https://www.visualcapitalist.com/the-biggest-social-media-platforms-2026/)
- [Backlinko — Social network usage & growth statistics (2026)](https://backlinko.com/social-media-users)
- [Exploding Topics — Most Popular Messaging Apps (2026)](https://explodingtopics.com/blog/messaging-apps-stats)
- [DemandSage — Spotify User Statistics 2026](https://www.demandsage.com/spotify-stats/)

Export formats are from each platform's own data-download feature as of 2026;
verify against a real export before writing a provider — they change without
notice (Facebook moved from a flat layout to `your_facebook_activity/…` and
renamed most files along the way).
