<!-- GENERATED from frontend/src/lib/exportGuides.js by
     frontend/scripts/gen-export-docs.mjs (make export-docs). Do not edit by hand:
     change the data module, regenerate, and rebuild the UI (make web). -->

# Getting your data — how to request each export

Archivist imports the **data export** ("takeout") every service must hand you on
request. This guide says, per service, where to ask for it, **which options to
choose so Archivist can read it**, how long it takes and how to upload it. The
same guide is built into the app at `/help/exports` (linked from the footer and
from the import form).

> **Menus change.** The steps were checked against the vendors' help pages as of
> **2026-09**; labels and paths move without notice. Each section links the
> vendor's official help article — trust that over this page when they differ.
> What matters for Archivist is in the "Choose these options" lists: a wrong
> format (HTML or TXT instead of JSON) usually costs a multi-day wait for a new
> export.

## Quick reference

| Service | Where to request | Choose | Typical wait |
|---|---|---|---|
| [Reddit](#reddit) | reddit.com/settings/data-request | ZIP of CSV (no choice); pick "all time" | hours to a few days (up to 30 days) |
| [ChatGPT](#chatgpt) | Settings → Data controls → Export data | ZIP (no choice) | minutes to a few hours |
| [Claude](#claude) | Settings → Privacy → Export data | ZIP (no choice) | minutes to a few hours |
| [Twitter / X](#twitter) | Settings → Your account → Download an archive of your data | ZIP (no choice) | 24 hours or more |
| [Instagram](#instagram) | Accounts Center → Your information and permissions → Export your information | JSON (default is HTML), date range "All time" | hours to 2 days |
| [Facebook](#facebook) | Settings & privacy → Accounts Center → Export your information | JSON (default is HTML), date range "All time" | minutes to a few days |
| [Google Takeout (YouTube, Search, Maps, Chrome, Keep, Chat, Photos…)](#google) | takeout.google.com | ZIP (multi-part); "My Activity" and YouTube "History" as JSON (default is HTML) | minutes to a few days |
| [WhatsApp](#whatsapp) | In the phone app, per chat: Export chat | one ZIP per chat, with or without media | immediate |
| [TikTok](#tiktok) | Settings and privacy → Account → Download your data | JSON (default is TXT), all data | 1–4 days |
| [Telegram](#telegram) | Telegram DESKTOP → Settings → Advanced → Export Telegram data | "Machine-readable JSON"; zip the folder yourself | minutes to hours (24 h wait on a new login) |
| [Snapchat](#snapchat) | accounts.snapchat.com → My Data | keep "Export JSON files" on; all time | hours to 2 days |
| [Spotify](#spotify) | spotify.com/account/privacy → Download your data | "Extended streaming history" (whole lifetime) and/or "Account data" (last year) | Account data ≈ 5 days · Extended history up to 30 days |
| [LinkedIn](#linkedin) | Settings & Privacy → Data privacy → Get a copy of your data | "Download larger data archive" (ZIP of CSV) | 10 minutes (first part) · 24 hours (complete) |
| [Discord](#discord) | User Settings → Data & Privacy → Request all of my data | data package ZIP; keep "Messages" ticked | up to 30 days (often a few days) |

## Uploading to Archivist (all services)

1. Open **Import**, pick the **Source** that matches the export and give the
   connection a label (the account's name; for WhatsApp the chat's name).
2. Choose the ZIP **unchanged** — do not unpack or re-pack it (Telegram is the
   exception: you create the ZIP yourself).
3. **Multi-part exports** (Google Takeout, Facebook, Instagram, Snapchat, X):
   select *all* parts in the one file dialog; they are imported as one archive.
4. One upload (all parts together) may be at most **8 GiB** by default; the
   server operator can raise it with `MAX_UPLOAD_MB`.
5. Anything Archivist does not recognise is kept as `<source>.unknown`, and
   re-importing the same or a newer export never duplicates events.

<a id="reddit"></a>

## Reddit

Source id: `reddit` · Request page: <https://www.reddit.com/settings/data-request> · [Official help article](https://support.reddithelp.com/hc/en-us/articles/360043048352)

### Where to request it

1. Log in on the web and open `https://www.reddit.com/settings/data-request` (the mobile apps only link to this page: Settings → Account settings → Request data, where offered).
2. Pick the legal basis that applies to you: GDPR (EU/UK), CCPA (California) or "Other" — the archive is the same.
3. Choose "I want all of my data" rather than a date range.
4. Submit the request.

### Choose these options

- All time / all data — a date range silently leaves out older votes, saves and messages.
- There is no format option; Reddit always sends a ZIP of CSV files.

### How long it takes, how it arrives

Reddit sends a private message (and a notification e-mail) to the account with a download link, usually within hours to a few days; legally it may take up to 30 days. The link is only valid for a few days — download it promptly.

### Upload to Archivist

- Source: Reddit (`reddit`). Upload the ZIP unchanged.

**What you get:** `reddit.post`, `reddit.comment`, `reddit.draft`, `reddit.scheduled_post`, `reddit.message`, `reddit.chat`, `reddit.vote.up`, `reddit.vote.down`, `reddit.vote`, `reddit.poll_vote`, `reddit.save`, `reddit.hide`, `reddit.subscribe`, `reddit.friend`, `reddit.moderate`, `reddit.multireddit`, `reddit.gild` — everything else as `reddit.unknown`.

<a id="chatgpt"></a>

## ChatGPT

Source id: `chatgpt` · [Official help article](https://help.openai.com/en/articles/7260999-how-do-i-export-my-chatgpt-history-and-data)

### Where to request it

1. Open ChatGPT (web or mobile app) and click your profile picture / name → Settings.
2. Open "Data controls".
3. Next to "Export data" choose Export, then "Confirm export".
4. Alternatively file the request through OpenAI's privacy portal at `https://privacy.openai.com` ("Download my data").

### Choose these options

- No options to pick. The ZIP contains `conversations.json` (what Archivist reads) next to a `chat.html` viewer.
- Team/Enterprise workspaces may have export disabled by the workspace owner.

### How long it takes, how it arrives

A download link arrives by e-mail at the account's address, usually within minutes (large accounts: hours). The link expires after 24 hours.

### Upload to Archivist

- Source: ChatGPT (`chatgpt`). Upload the ZIP unchanged.

**What you get:** `chatgpt.message` — everything else as `chatgpt.unknown`.

<a id="claude"></a>

## Claude

Source id: `claude` · Request page: <https://claude.ai/settings/data-privacy-controls> · [Official help article](https://support.claude.com/en/articles/9450526-export-your-claude-data)

### Where to request it

1. Open claude.ai in a browser or the desktop app (the export cannot be started from the iOS/Android apps).
2. Click your initials / name (bottom left) → Settings → Privacy.
3. Click "Export data" and confirm.

### Choose these options

- No options to pick. The ZIP contains `conversations.json` (what Archivist reads), plus projects and account data.
- On Team/Enterprise plans only the organisation's owner can export.

### How long it takes, how it arrives

A download link arrives by e-mail, usually within minutes. The link expires after 24 hours and you must be logged in to use it.

### Upload to Archivist

- Source: Claude (`claude`). Upload the ZIP unchanged.

**What you get:** `claude.message`, `claude.project`, `claude.project_doc` — everything else as `claude.unknown`.

<a id="twitter"></a>

## Twitter / X

Source id: `twitter` · Request page: <https://x.com/settings/download_your_data> · [Official help article](https://help.x.com/en/managing-your-account/how-to-download-your-x-archive)

### Where to request it

1. Web: More → Settings and privacy → Your account → "Download an archive of your data" (`https://x.com/settings/download_your_data`). Mobile apps: profile picture → Settings and privacy → Your account → same entry.
2. Re-enter your password and pass the verification code sent by e-mail or SMS.
3. Click "Request archive".

### Choose these options

- No options to pick. Archivist reads the data/*.js files inside the ZIP (tweets, replies, retweets and their media, direct messages incl. group chats, likes, follows, blocks, mutes, lists, saved searches, Moments, long-form "note" tweets, and the account/profile records); leave the archive as it is. Bookmarks are not included in the X archive at all.

### How long it takes, how it arrives

X sends a push notification and an e-mail when the archive is ready — typically after 24 hours, sometimes several days. Download it from the same settings page; the download stays available for a few days. Large accounts receive several ZIP parts.

### Upload to Archivist

- Source: Twitter / X (`twitter`).
- If the archive came in several parts, select all of them in one upload.
- Media in data/tweets_media and data/direct_messages_media is stored and linked to the tweets and messages it belongs to.

**What you get:** `twitter.tweet`, `twitter.note_tweet`, `twitter.moment`, `twitter.dm`, `twitter.dm_event`, `twitter.like`, `twitter.follow`, `twitter.follower`, `twitter.block`, `twitter.mute`, `twitter.list`, `twitter.saved_search`, `twitter.account` — everything else as `twitter.unknown`.

<a id="instagram"></a>

## Instagram

Source id: `instagram` · Request page: <https://accountscenter.instagram.com/info_and_permissions/dyi/> · [Official help article](https://help.instagram.com/181231772500920)

### Where to request it

1. App: profile → menu (☰) → Accounts Center → "Your information and permissions" → "Export your information" (older label: "Download your information"). Web: `https://accountscenter.instagram.com/info_and_permissions/dyi/`.
2. Choose "Create export", select the Instagram profile, then "Export to device".
3. Set the options below BEFORE starting the export, then press "Start export" and confirm with your password.

### Choose these options

- Format: JSON. The default is HTML, which Archivist cannot read — an HTML export imports as `instagram.unknown` only and you would have to request it again.
- Date range: "All time" (the default is only the last year).
- Customize information: leave everything selected (messages, content, connections, Threads).
- Media quality: your choice. Higher quality keeps photos and videos at their best but makes the export larger and slower; it has no effect on which events are found.

### How long it takes, how it arrives

Meta e-mails you and shows the export under "Current activity / Available downloads" in the same Accounts Center page, usually within a few hours (big accounts: up to 2 days). It can be downloaded for 4 days and is split into several ZIPs when large.

### Upload to Archivist

- Source: Instagram (`instagram`).
- Select ALL parts of a split export in one upload — messages and media are spread across them.

**What you get:** `instagram.post`, `instagram.thread_post`, `instagram.comment`, `instagram.story`, `instagram.message`, `instagram.like`, `instagram.save`, `instagram.follow`, `instagram.follower`, `instagram.follow_request`, `instagram.unfollow`, `instagram.search`, `instagram.login` — everything else as `instagram.unknown`.

<a id="facebook"></a>

## Facebook

Source id: `facebook` · Request page: <https://www.facebook.com/dyi> · [Official help article](https://www.facebook.com/help/212802592074644)

### Where to request it

1. Web: `https://www.facebook.com/dyi`, or profile picture → Settings & privacy → Settings → Accounts Center → "Your information and permissions" → "Export your information" (older label: "Download your information"). The mobile app has the same path under Menu → Settings & privacy.
2. Choose "Create export", select the Facebook profile, then "Export to device".
3. Set the options below BEFORE starting the export, then press "Start export" and re-enter your password.

### Choose these options

- Format: JSON. The default is HTML, which Archivist cannot read — an HTML export imports as `facebook.unknown` only and you would have to request it again.
- Date range: "All time" (the default is only the last year).
- Customize information: leave everything selected. Messenger chats are part of this export.
- Media quality: your choice. Higher quality means a larger, slower export; it does not change which events are found.
- End-to-end encrypted Messenger chats are NOT in this export; they need Messenger's separate "secure storage" download, which Archivist does not read yet.

### How long it takes, how it arrives

Facebook notifies you in the app and by e-mail when the export is ready — minutes for small accounts, up to a few days for old ones. It is listed under "Available downloads" for 4 days and is split into several ZIPs when large.

### Upload to Archivist

- Source: Facebook (`facebook`).
- Select ALL parts of a split export in one upload.

**What you get:** `facebook.post`, `facebook.comment`, `facebook.app_post`, `facebook.message`, `facebook.reaction`, `facebook.friend`, `facebook.friend_request`, `facebook.follow`, `facebook.unfollow`, `facebook.group_join`, `facebook.event`, `facebook.app`, `facebook.search`, `facebook.photo`, `facebook.profile_update`, `facebook.login` — everything else as `facebook.unknown`.

<a id="google"></a>

## Google Takeout (YouTube, Search, Maps, Chrome, Keep, Chat, Photos…)

Source id: `google` · Request page: <https://takeout.google.com> · [Official help article](https://support.google.com/accounts/answer/3024190)

### Where to request it

1. Open `https://takeout.google.com` and check that the right Google account is active (top right).
2. Click "Deselect all", then tick the products you want. Mapped today: "My Activity" (all products: Search, Chrome, Maps, Play Store, Assistant, Gemini, Discover, Ads, Shopping, Books, YouTube…), "YouTube and YouTube Music", "Chrome", "Location History (Timeline)", "Maps (your places)", "Maps", "Saved", "Keep", "Google Chat", "Hangouts", "Google Photos". Anything else is kept as `google.unknown`.
3. On "My Activity" click "Multiple formats" and change "Activity records" from HTML to JSON. On "YouTube and YouTube Music" click "Multiple formats" and change "History" from HTML to JSON. Confirm with OK.
4. Optional: under "YouTube…" → "All YouTube data included" untick "videos"; under "Google Photos" pick only the albums you want — media makes the export take days and tens of GB.
5. Click "Next step". Destination: "Send download link via email"; Frequency: "Export once"; File type: .zip (not .tgz); File size: any — 2 GB parts are fine.
6. Click "Create export".
7. Location history: since 2024 the Maps Timeline lives on your phone. Google Maps → your avatar → Your Timeline → ⋯ → "Location and privacy settings" → "Export Timeline data" gives a `Timeline.json`; drop it into the Takeout ZIP (any folder) or upload it as its own ZIP.

### Choose these options

- My Activity → Multiple formats → Activity records: JSON. YouTube and YouTube Music → Multiple formats → History: JSON. With the HTML default these arrive as `MyActivity.html` / `watch-history.html` and import as one `google.unknown` event each — you would have to request the export again.
- File type: .zip. Archivist does not read .tgz.
- Account language: folder and file names are localized. English and German are recognized (some French/Spanish/Italian); if a product does not show up, switch the Google account language to English and export again.
- "Location History (Timeline)" → Records.json holds every raw location fix — often millions of events. Leave it out if you only want places and trips.

### How long it takes, how it arrives

Google e-mails a link to the "Manage exports" page — minutes for history only, hours or days with videos or photos. Each archive can be downloaded 5 times within about 7 days. Large exports come as `takeout-…-001.zip`, `-002.zip`, ….

### Upload to Archivist

- Source: Google Takeout (`google`).
- Select ALL `takeout-…-NNN.zip` parts in one upload; they are imported as one archive.

**What you get:** `google.youtube.watch`, `google.youtube.search`, `google.youtube.comment`, `google.youtube.subscribe`, `google.youtube.playlist_item`, `google.youtube.like`, `google.search`, `google.visit`, `google.chrome.search`, `google.chrome.visit`, `google.maps.activity`, `google.play.activity`, `google.assistant.query`, `google.gemini.prompt`, `google.discover.view`, `google.ads.view`, `google.shopping.activity`, `google.books.activity`, `google.activity`, `google.chrome.bookmark`, `google.location.visit`, `google.location.trip`, `google.location.point`, `google.maps.saved`, `google.maps.review`, `google.keep.note`, `google.chat.message`, `google.hangouts.message`, `google.photos.photo` — everything else as `google.unknown`.

<a id="whatsapp"></a>

## WhatsApp

Source id: `whatsapp` · [Official help article](https://faq.whatsapp.com/1180414079177245)

### Where to request it

**Android**

1. Open the chat → ⋮ (top right) → More → "Export chat".
2. Choose "Include media" or "Without media".
3. Share the result to a place you can reach from your computer (Files, Drive, e-mail to yourself). If Android hands you a loose `.txt` plus media files instead of a ZIP, put them into one ZIP yourself.

**iPhone**

1. Open the chat → tap the contact or group name at the top → scroll down → "Export Chat".
2. Choose "Attach Media" or "Without Media".
3. "Save to Files" (or AirDrop / mail it to yourself). Keep the file name `WhatsApp Chat - <name>.zip`: on iOS it is the only place the chat's name is recorded.

### Choose these options

- There is no account-wide export. "Settings → Account → Request account info" is a different report and contains NO messages.
- With media: photos, voice notes and documents are attached to their messages, but WhatsApp caps such an export at roughly the latest 10,000 messages (about 40,000 without media). For long chats export without media, or do both.
- Timestamps in the transcript have no time zone and are stored as UTC.

### How long it takes, how it arrives

Immediate — the phone creates the file on the spot. Nothing is e-mailed by WhatsApp.

### Upload to Archivist

- Source: WhatsApp (`whatsapp`). One upload per chat — do NOT select several chats at once (multiple files are treated as parts of ONE archive).
- Set the connection label to the chat's name (e.g. "Family group"). iOS transcripts are just `_chat.txt`, so without a label every iOS chat is titled "WhatsApp chat".

**What you get:** `whatsapp.message`, `whatsapp.system` — everything else as `whatsapp.unknown`.

<a id="tiktok"></a>

## TikTok

Source id: `tiktok` · Request page: <https://www.tiktok.com/setting> · [Official help article](https://support.tiktok.com/en/account-and-privacy/personalized-ads-and-data/requesting-your-data)

### Where to request it

1. App: Profile → ☰ → Settings and privacy → Account → "Download your data". Web: `https://www.tiktok.com/setting` → Privacy → Data → "Download your data".
2. On the "Request data" tab keep "All data" selected.
3. Set "File format" to JSON.
4. Tap "Request data".

### Choose these options

- File format: JSON — "machine-readable". The default TXT export cannot be read and imports as `tiktok.unknown` only; you would have to request it again.
- Data to download: All data (a custom selection works, you just get fewer event types).

### How long it takes, how it arrives

The request shows as "Pending" on the "Download data" tab of the same screen and TikTok sends an in-app notification when it is ready — usually 1–2 days, up to 4. The file can then be downloaded for 4 days.

### Upload to Archivist

- Source: TikTok (`tiktok`). Upload the ZIP (it holds one `user_data_tiktok.json`) unchanged.

**What you get:** `tiktok.post`, `tiktok.comment`, `tiktok.message`, `tiktok.like`, `tiktok.save`, `tiktok.share`, `tiktok.follow`, `tiktok.follower`, `tiktok.watch`, `tiktok.search`, `tiktok.block`, `tiktok.login` — everything else as `tiktok.unknown`.

<a id="telegram"></a>

## Telegram

Source id: `telegram` · Request page: <https://desktop.telegram.org/> · [Official help article](https://telegram.org/blog/export-and-more)

### Where to request it

1. Install Telegram Desktop from `https://desktop.telegram.org/` and log in. The phone apps, the web app and the macOS App Store "Telegram for macOS" (Swift) client do not have this export.
2. Open ☰ → Settings → Advanced → "Export Telegram data".
3. Tick what to include: Account information, Contacts, Personal chats, Bot chats, Private groups / channels (public ones if you want them), and the media types you want; raise the media size limit if you want large files.
4. At the bottom, under "Location and format", select "Machine-readable JSON" and note the download path.
5. Click Export. For security, a recently logged-in desktop session must wait 24 hours (or you can approve the request from your phone) before the export starts.
6. When it finishes, ZIP the resulting folder (the one that contains `result.json` and the `chats/` etc. media folders). Windows: right-click → Compress to ZIP file; macOS: right-click → Compress; Linux: `zip -r telegram.zip "DataExport_…"`.

### Choose these options

- Format: "Machine-readable JSON" (or "Both"). The default "Human-readable HTML" alone cannot be read.
- Keep the folder structure when zipping — media is found by its relative path from `result.json`.
- A single chat can be exported the same way (open the chat → ⋮ → "Export chat history" → format JSON); it is accepted too.
- Secret chats are never included (they exist only on the devices).

### How long it takes, how it arrives

Nothing is sent to you: Telegram Desktop downloads the data straight into the chosen folder (default `Downloads/Telegram Desktop/DataExport_<date>`). Minutes without media, hours with all media.

### Upload to Archivist

- Source: Telegram (`telegram`). Upload the ZIP you created.

**What you get:** `telegram.message`, `telegram.service`, `telegram.contact` — everything else as `telegram.unknown`.

<a id="snapchat"></a>

## Snapchat

Source id: `snapchat` · Request page: <https://accounts.snapchat.com/accounts/downloadmydata> · [Official help article](https://help.snapchat.com/hc/en-us/articles/7012305371156-How-do-I-download-my-data-from-Snapchat)

### Where to request it

1. Web: log in at `https://accounts.snapchat.com` → "My Data". App: profile → ⚙ Settings → Account actions → "My Data" (opens the same page).
2. Select the data to include — all categories; add "Export your Memories" if you want the saved media too.
3. Make sure "Export JSON files" is ON.
4. Date range: switch the filter off / choose "All time".
5. Confirm your e-mail address and press Submit.

### Choose these options

- "Export JSON files": ON. Archivist reads the `json/` folder; the `html/` twin is ignored.
- Date range: all time.
- Memories are listed as download links that expire — Archivist records them as events, but fetch the media itself soon after the export if you want to keep it.

### How long it takes, how it arrives

Snapchat e-mails you when the export is ready (hours, up to a day or two with Memories). Download it from the same "My Data" page; the links stay valid for about a week. Big exports are split into `mydata~…-1.zip`, `-2.zip`, ….

### Upload to Archivist

- Source: Snapchat (`snapchat`).
- Select all ZIP parts in one upload.

**What you get:** `snapchat.message`, `snapchat.snap`, `snapchat.memory`, `snapchat.friend`, `snapchat.friend_request`, `snapchat.search`, `snapchat.login`, `snapchat.call`, `snapchat.story`, `snapchat.subscription` — everything else as `snapchat.unknown`.

<a id="spotify"></a>

## Spotify

Source id: `spotify` · Request page: <https://www.spotify.com/account/privacy/> · [Official help article](https://support.spotify.com/us/article/understanding-your-data/)

### Where to request it

1. Open `https://www.spotify.com/account/privacy/` in a browser and log in (the apps only link here: Settings → Account → Privacy).
2. Scroll to "Download your data".
3. Tick the packages you want (see below) and click "Request data".
4. Confirm the request through the link in the e-mail Spotify sends right away — without that click nothing is prepared.

### Choose these options

- "Extended streaming history": every play since the account was created, with track URIs and milliseconds played. This is the one you want for a timeline.
- "Account data": the last year of plays plus searches, saved library and playlists. Quicker; worth requesting as well for the searches and playlists.
- "Technical log information" is not needed.
- The packages are requested with separate tick boxes and arrive as separate e-mails/ZIPs. Both history flavours are JSON — there is no format option.
- Importing both yields two `spotify.play` events for plays of the last year (they differ in precision and are told apart by the `export` metadata value).

### How long it takes, how it arrives

A download link arrives by e-mail: Account data usually within 5 days, the extended history within up to 30 days (often after a few days). The link is valid for 14 days.

### Upload to Archivist

- Source: Spotify (`spotify`). Upload `my_spotify_data.zip` unchanged.
- Import the two packages as two separate uploads (they are different archives, not parts of one).

**What you get:** `spotify.play`, `spotify.search`, `spotify.save`, `spotify.playlist`, `spotify.playlist_item` — everything else as `spotify.unknown`.

<a id="linkedin"></a>

## LinkedIn

Source id: `linkedin` · Request page: <https://www.linkedin.com/mypreferences/d/download-my-data> · [Official help article](https://www.linkedin.com/help/linkedin/answer/a1339364)

### Where to request it

1. Web: Me → Settings & Privacy → Data privacy → "Get a copy of your data" (`https://www.linkedin.com/mypreferences/d/download-my-data`). Mobile app: profile picture → Settings → Data privacy → same entry.
2. Select "Download larger data archive, including connections, verifications, contacts, account history, and information we infer about you…".
3. Click "Request archive" and enter your password.

### Choose these options

- The larger archive — not the "Want something in particular?" tick boxes, which give a partial export.
- LinkedIn sends TWO e-mails: a first, partial archive after about 10 minutes (messages, connections, profile) and the complete one within 24 hours. Wait for the complete one — it replaces the first.
- File names are English regardless of your UI language; leave the ZIP as it is.

### How long it takes, how it arrives

An e-mail links back to the same settings page, where the archive can be downloaded for 72 hours.

### Upload to Archivist

- Source: LinkedIn (`linkedin`). Upload the `Complete_LinkedInDataExport_….zip` unchanged.

**What you get:** `linkedin.post`, `linkedin.comment`, `linkedin.message`, `linkedin.reaction`, `linkedin.connection`, `linkedin.invitation`, `linkedin.follow`, `linkedin.save`, `linkedin.search`, `linkedin.position`, `linkedin.education` — everything else as `linkedin.unknown`.

<a id="discord"></a>

## Discord

Source id: `discord` · [Official help article](https://support.discord.com/hc/en-us/articles/360004027692-Requesting-a-Copy-of-your-Data)

### Where to request it

1. Desktop/web: ⚙ User Settings → "Data & Privacy" (older clients: "Privacy & Safety") → scroll to "Request all of my data" → "Request Data". Mobile: avatar → ⚙ → Data & Privacy → "Request all of my data".
2. If asked which categories to include, keep at least Account, Messages and Servers ticked.
3. Confirm the request.

### Choose these options

- Messages must be included — they are what Archivist maps.
- The package contains only YOUR OWN messages, not the other side of any conversation. That is a limit of Discord's export.
- You can only request a package once every 30 days.

### How long it takes, how it arrives

Discord e-mails a download link to the account's verified address — by its own account within up to 30 days, in practice usually a few days. Download `package.zip` promptly; the link expires.

### Upload to Archivist

- Source: Discord (`discord`). Upload `package.zip` unchanged.

**What you get:** `discord.message`, `discord.channel`, `discord.server`, `discord.friend` — everything else as `discord.unknown`.

## See also

- [`docs/platforms.md`](./platforms.md) — export formats, event types and the
  verification status of each provider.
- [`docs/reddit.md`](./reddit.md) — Reddit specifics.
