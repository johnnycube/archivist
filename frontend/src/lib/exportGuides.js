// How to request each service's data export, and which options to pick so
// Archivist can read it. Single source for:
//   - the in-app help page   (routes/help/exports)
//   - the import form's hint (lib/components/ImportForm.svelte)
//   - the site footer's links (routes/+layout.svelte)
//   - docs/exports.md — GENERATED from this file. After editing, run
//       make export-docs        (node frontend/scripts/gen-export-docs.mjs)
//     so the Markdown and the app never disagree. Do not edit docs/exports.md
//     by hand.
//
// Plain data only (no Svelte/browser imports): the generator loads this module
// in Node. Text is rendered as text, never as HTML; `code` spans are written
// with backticks and rendered by the page/generator.
//
// Requirements per source come from docs/platforms.md and the package comments
// under providers/<id>/ — when a provider starts accepting another format,
// update the guide here.

// Menus move. Bump this when the steps are re-checked against the vendors.
export const GUIDES_VERIFIED = '2026-09';

// Default cap on one upload (all parts together); server env MAX_UPLOAD_MB.
export const UPLOAD_CAP = '8 GiB';

export const EXPORT_GUIDES = [
  {
    id: 'reddit',
    name: 'Reddit',
    requestUrl: 'https://www.reddit.com/settings/data-request',
    helpUrl: 'https://support.reddithelp.com/hc/en-us/articles/360043048352',
    where: 'reddit.com/settings/data-request',
    format: 'ZIP of CSV (no choice); pick "all time"',
    wait: 'hours to a few days (up to 30 days)',
    hint: 'Request "all time" on the data request form — the ZIP of CSVs is uploaded as is.',
    steps: [
      {
        items: [
          'Log in on the web and open `https://www.reddit.com/settings/data-request` (the mobile apps only link to this page: Settings → Account settings → Request data, where offered).',
          'Pick the legal basis that applies to you: GDPR (EU/UK), CCPA (California) or "Other" — the archive is the same.',
          'Choose "I want all of my data" rather than a date range.',
          'Submit the request.'
        ]
      }
    ],
    choose: [
      'All time / all data — a date range silently leaves out older votes, saves and messages.',
      'There is no format option; Reddit always sends a ZIP of CSV files.'
    ],
    delivery:
      'Reddit sends a private message (and a notification e-mail) to the account with a download link, usually within hours to a few days; legally it may take up to 30 days. The link is only valid for a few days — download it promptly.',
    upload: ['Source: Reddit (`reddit`). Upload the ZIP unchanged.'],
    events: [
      'post',
      'comment',
      'draft',
      'scheduled_post',
      'message',
      'chat',
      'vote.up',
      'vote.down',
      'vote',
      'poll_vote',
      'save',
      'hide',
      'subscribe',
      'friend',
      'moderate',
      'multireddit',
      'gild'
    ]
  },
  {
    id: 'chatgpt',
    name: 'ChatGPT',
    requestUrl: '',
    helpUrl:
      'https://help.openai.com/en/articles/7260999-how-do-i-export-my-chatgpt-history-and-data',
    where: 'Settings → Data controls → Export data',
    format: 'ZIP (no choice)',
    wait: 'minutes to a few hours',
    hint: 'Settings → Data controls → Export data; upload the e-mailed ZIP as is.',
    steps: [
      {
        items: [
          'Open ChatGPT (web or mobile app) and click your profile picture / name → Settings.',
          'Open "Data controls".',
          'Next to "Export data" choose Export, then "Confirm export".',
          'Alternatively file the request through OpenAI\'s privacy portal at `https://privacy.openai.com` ("Download my data").'
        ]
      }
    ],
    choose: [
      'No options to pick. The ZIP contains `conversations.json` (what Archivist reads) next to a `chat.html` viewer.',
      'Team/Enterprise workspaces may have export disabled by the workspace owner.'
    ],
    delivery:
      "A download link arrives by e-mail at the account's address, usually within minutes (large accounts: hours). The link expires after 24 hours.",
    upload: ['Source: ChatGPT (`chatgpt`). Upload the ZIP unchanged.'],
    events: ['message']
  },
  {
    id: 'claude',
    name: 'Claude',
    requestUrl: 'https://claude.ai/settings/data-privacy-controls',
    helpUrl: 'https://support.claude.com/en/articles/9450526-export-your-claude-data',
    where: 'Settings → Privacy → Export data',
    format: 'ZIP (no choice)',
    wait: 'minutes to a few hours',
    hint: 'Settings → Privacy → Export data (web or desktop app); upload the e-mailed ZIP as is.',
    steps: [
      {
        items: [
          'Open claude.ai in a browser or the desktop app (the export cannot be started from the iOS/Android apps).',
          'Click your initials / name (bottom left) → Settings → Privacy.',
          'Click "Export data" and confirm.'
        ]
      }
    ],
    choose: [
      'No options to pick. The ZIP contains `conversations.json` (what Archivist reads), plus projects and account data.',
      "On Team/Enterprise plans only the organisation's owner can export."
    ],
    delivery:
      'A download link arrives by e-mail, usually within minutes. The link expires after 24 hours and you must be logged in to use it.',
    upload: ['Source: Claude (`claude`). Upload the ZIP unchanged.'],
    events: ['message', 'project', 'project_doc']
  },
  {
    id: 'twitter',
    name: 'Twitter / X',
    requestUrl: 'https://x.com/settings/download_your_data',
    helpUrl: 'https://help.x.com/en/managing-your-account/how-to-download-your-x-archive',
    where: 'Settings → Your account → Download an archive of your data',
    format: 'ZIP (no choice)',
    wait: '24 hours or more',
    hint: 'Settings → Your account → "Download an archive of your data"; allow a day or more.',
    steps: [
      {
        items: [
          'Web: More → Settings and privacy → Your account → "Download an archive of your data" (`https://x.com/settings/download_your_data`). Mobile apps: profile picture → Settings and privacy → Your account → same entry.',
          'Re-enter your password and pass the verification code sent by e-mail or SMS.',
          'Click "Request archive".'
        ]
      }
    ],
    choose: [
      'No options to pick. Archivist reads the data/*.js files inside the ZIP (tweets, replies, retweets and their media, direct messages incl. group chats, likes, follows, blocks, mutes, lists, saved searches, Moments, long-form "note" tweets, and the account/profile records); leave the archive as it is. Bookmarks are not included in the X archive at all.'
    ],
    delivery:
      'X sends a push notification and an e-mail when the archive is ready — typically after 24 hours, sometimes several days. Download it from the same settings page; the download stays available for a few days. Large accounts receive several ZIP parts.',
    upload: [
      'Source: Twitter / X (`twitter`).',
      'If the archive came in several parts, select all of them in one upload.',
      'Media in data/tweets_media and data/direct_messages_media is stored and linked to the tweets and messages it belongs to.'
    ],
    events: [
      'tweet',
      'note_tweet',
      'moment',
      'dm',
      'dm_event',
      'like',
      'follow',
      'follower',
      'block',
      'mute',
      'list',
      'saved_search',
      'account'
    ]
  },
  {
    id: 'instagram',
    name: 'Instagram',
    requestUrl: 'https://accountscenter.instagram.com/info_and_permissions/dyi/',
    helpUrl: 'https://help.instagram.com/181231772500920',
    where: 'Accounts Center → Your information and permissions → Export your information',
    format: 'JSON (default is HTML), date range "All time"',
    wait: 'hours to 2 days',
    hint: 'Choose format JSON (not HTML) and date range "All time".',
    steps: [
      {
        items: [
          'App: profile → menu (☰) → Accounts Center → "Your information and permissions" → "Export your information" (older label: "Download your information"). Web: `https://accountscenter.instagram.com/info_and_permissions/dyi/`.',
          'Choose "Create export", select the Instagram profile, then "Export to device".',
          'Set the options below BEFORE starting the export, then press "Start export" and confirm with your password.'
        ]
      }
    ],
    choose: [
      'Format: JSON. The default is HTML, which Archivist cannot read — an HTML export imports as `instagram.unknown` only and you would have to request it again.',
      'Date range: "All time" (the default is only the last year).',
      'Customize information: leave everything selected (messages, content, connections, Threads).',
      'Media quality: your choice. Higher quality keeps photos and videos at their best but makes the export larger and slower; it has no effect on which events are found.'
    ],
    delivery:
      'Meta e-mails you and shows the export under "Current activity / Available downloads" in the same Accounts Center page, usually within a few hours (big accounts: up to 2 days). It can be downloaded for 4 days and is split into several ZIPs when large.',
    upload: [
      'Source: Instagram (`instagram`).',
      'Select ALL parts of a split export in one upload — messages and media are spread across them.'
    ],
    events: [
      'post',
      'thread_post',
      'comment',
      'story',
      'message',
      'like',
      'save',
      'follow',
      'follower',
      'follow_request',
      'unfollow',
      'search',
      'login'
    ]
  },
  {
    id: 'facebook',
    name: 'Facebook',
    requestUrl: 'https://www.facebook.com/dyi',
    helpUrl: 'https://www.facebook.com/help/212802592074644',
    where: 'Settings & privacy → Accounts Center → Export your information',
    format: 'JSON (default is HTML), date range "All time"',
    wait: 'minutes to a few days',
    hint: 'Choose format JSON (not HTML) and date range "All time".',
    steps: [
      {
        items: [
          'Web: `https://www.facebook.com/dyi`, or profile picture → Settings & privacy → Settings → Accounts Center → "Your information and permissions" → "Export your information" (older label: "Download your information"). The mobile app has the same path under Menu → Settings & privacy.',
          'Choose "Create export", select the Facebook profile, then "Export to device".',
          'Set the options below BEFORE starting the export, then press "Start export" and re-enter your password.'
        ]
      }
    ],
    choose: [
      'Format: JSON. The default is HTML, which Archivist cannot read — an HTML export imports as `facebook.unknown` only and you would have to request it again.',
      'Date range: "All time" (the default is only the last year).',
      'Customize information: leave everything selected. Messenger chats are part of this export.',
      'Media quality: your choice. Higher quality means a larger, slower export; it does not change which events are found.',
      'End-to-end encrypted Messenger chats are NOT in this export; they need Messenger\'s separate "secure storage" download, which Archivist does not read yet.'
    ],
    delivery:
      'Facebook notifies you in the app and by e-mail when the export is ready — minutes for small accounts, up to a few days for old ones. It is listed under "Available downloads" for 4 days and is split into several ZIPs when large.',
    upload: ['Source: Facebook (`facebook`).', 'Select ALL parts of a split export in one upload.'],
    events: [
      'post',
      'comment',
      'app_post',
      'message',
      'reaction',
      'friend',
      'friend_request',
      'follow',
      'unfollow',
      'group_join',
      'event',
      'app',
      'search',
      'photo',
      'profile_update',
      'login'
    ]
  },
  {
    id: 'google',
    name: 'Google Takeout (YouTube, Search, Maps, Chrome, Keep, Chat, Photos…)',
    requestUrl: 'https://takeout.google.com',
    helpUrl: 'https://support.google.com/accounts/answer/3024190',
    where: 'takeout.google.com',
    format: 'ZIP (multi-part); "My Activity" and YouTube "History" as JSON (default is HTML)',
    wait: 'minutes to a few days',
    hint: 'Set "My Activity" and YouTube "History" to JSON under "Multiple formats" — the HTML default cannot be imported.',
    steps: [
      {
        items: [
          'Open `https://takeout.google.com` and check that the right Google account is active (top right).',
          'Click "Deselect all", then tick the products you want. Mapped today: "My Activity" (all products: Search, Chrome, Maps, Play Store, Assistant, Gemini, Discover, Ads, Shopping, Books, YouTube…), "YouTube and YouTube Music", "Chrome", "Location History (Timeline)", "Maps (your places)", "Maps", "Saved", "Keep", "Google Chat", "Hangouts", "Google Photos". Anything else is kept as `google.unknown`.',
          'On "My Activity" click "Multiple formats" and change "Activity records" from HTML to JSON. On "YouTube and YouTube Music" click "Multiple formats" and change "History" from HTML to JSON. Confirm with OK.',
          'Optional: under "YouTube…" → "All YouTube data included" untick "videos"; under "Google Photos" pick only the albums you want — media makes the export take days and tens of GB.',
          'Click "Next step". Destination: "Send download link via email"; Frequency: "Export once"; File type: .zip (not .tgz); File size: any — 2 GB parts are fine.',
          'Click "Create export".',
          'Location history: since 2024 the Maps Timeline lives on your phone. Google Maps → your avatar → Your Timeline → ⋯ → "Location and privacy settings" → "Export Timeline data" gives a `Timeline.json`; drop it into the Takeout ZIP (any folder) or upload it as its own ZIP.'
        ]
      }
    ],
    choose: [
      'My Activity → Multiple formats → Activity records: JSON. YouTube and YouTube Music → Multiple formats → History: JSON. With the HTML default these arrive as `MyActivity.html` / `watch-history.html` and import as one `google.unknown` event each — you would have to request the export again.',
      'File type: .zip. Archivist does not read .tgz.',
      'Account language: folder and file names are localized. English and German are recognized (some French/Spanish/Italian); if a product does not show up, switch the Google account language to English and export again.',
      '"Location History (Timeline)" → Records.json holds every raw location fix — often millions of events. Leave it out if you only want places and trips.'
    ],
    delivery:
      'Google e-mails a link to the "Manage exports" page — minutes for history only, hours or days with videos or photos. Each archive can be downloaded 5 times within about 7 days. Large exports come as `takeout-…-001.zip`, `-002.zip`, ….',
    upload: [
      'Source: Google Takeout (`google`).',
      'Select ALL `takeout-…-NNN.zip` parts in one upload; they are imported as one archive.'
    ],
    events: [
      'youtube.watch',
      'youtube.search',
      'youtube.comment',
      'youtube.subscribe',
      'youtube.playlist_item',
      'youtube.like',
      'search',
      'visit',
      'chrome.search',
      'chrome.visit',
      'maps.activity',
      'play.activity',
      'assistant.query',
      'gemini.prompt',
      'discover.view',
      'ads.view',
      'shopping.activity',
      'books.activity',
      'activity',
      'chrome.bookmark',
      'location.visit',
      'location.trip',
      'location.point',
      'maps.saved',
      'maps.review',
      'keep.note',
      'chat.message',
      'hangouts.message',
      'photos.photo'
    ]
  },
  {
    id: 'whatsapp',
    name: 'WhatsApp',
    requestUrl: '',
    helpUrl: 'https://faq.whatsapp.com/1180414079177245',
    where: 'In the phone app, per chat: Export chat',
    format: 'one ZIP per chat, with or without media',
    wait: 'immediate',
    hint: "One ZIP per chat: open the chat → Export chat. Put the chat's name in the connection label.",
    steps: [
      {
        title: 'Android',
        items: [
          'Open the chat → ⋮ (top right) → More → "Export chat".',
          'Choose "Include media" or "Without media".',
          'Share the result to a place you can reach from your computer (Files, Drive, e-mail to yourself). If Android hands you a loose `.txt` plus media files instead of a ZIP, put them into one ZIP yourself.'
        ]
      },
      {
        title: 'iPhone',
        items: [
          'Open the chat → tap the contact or group name at the top → scroll down → "Export Chat".',
          'Choose "Attach Media" or "Without Media".',
          '"Save to Files" (or AirDrop / mail it to yourself). Keep the file name `WhatsApp Chat - <name>.zip`: on iOS it is the only place the chat\'s name is recorded.'
        ]
      }
    ],
    choose: [
      'There is no account-wide export. "Settings → Account → Request account info" is a different report and contains NO messages.',
      'With media: photos, voice notes and documents are attached to their messages, but WhatsApp caps such an export at roughly the latest 10,000 messages (about 40,000 without media). For long chats export without media, or do both.',
      'Timestamps in the transcript have no time zone and are stored as UTC.'
    ],
    delivery:
      'Immediate — the phone creates the file on the spot. Nothing is e-mailed by WhatsApp.',
    upload: [
      'Source: WhatsApp (`whatsapp`). One upload per chat — do NOT select several chats at once (multiple files are treated as parts of ONE archive).',
      'Set the connection label to the chat\'s name (e.g. "Family group"). iOS transcripts are just `_chat.txt`, so without a label every iOS chat is titled "WhatsApp chat".'
    ],
    events: ['message', 'system']
  },
  {
    id: 'tiktok',
    name: 'TikTok',
    requestUrl: 'https://www.tiktok.com/setting',
    helpUrl:
      'https://support.tiktok.com/en/account-and-privacy/personalized-ads-and-data/requesting-your-data',
    where: 'Settings and privacy → Account → Download your data',
    format: 'JSON (default is TXT), all data',
    wait: '1–4 days',
    hint: 'Choose file format JSON, not TXT.',
    steps: [
      {
        items: [
          'App: Profile → ☰ → Settings and privacy → Account → "Download your data". Web: `https://www.tiktok.com/setting` → Privacy → Data → "Download your data".',
          'On the "Request data" tab keep "All data" selected.',
          'Set "File format" to JSON.',
          'Tap "Request data".'
        ]
      }
    ],
    choose: [
      'File format: JSON — "machine-readable". The default TXT export cannot be read and imports as `tiktok.unknown` only; you would have to request it again.',
      'Data to download: All data (a custom selection works, you just get fewer event types).'
    ],
    delivery:
      'The request shows as "Pending" on the "Download data" tab of the same screen and TikTok sends an in-app notification when it is ready — usually 1–2 days, up to 4. The file can then be downloaded for 4 days.',
    upload: [
      'Source: TikTok (`tiktok`). Upload the ZIP (it holds one `user_data_tiktok.json`) unchanged.'
    ],
    events: [
      'post',
      'comment',
      'message',
      'like',
      'save',
      'share',
      'follow',
      'follower',
      'watch',
      'search',
      'block',
      'login'
    ]
  },
  {
    id: 'telegram',
    name: 'Telegram',
    requestUrl: 'https://desktop.telegram.org/',
    helpUrl: 'https://telegram.org/blog/export-and-more',
    where: 'Telegram DESKTOP → Settings → Advanced → Export Telegram data',
    format: '"Machine-readable JSON"; zip the folder yourself',
    wait: 'minutes to hours (24 h wait on a new login)',
    hint: 'Telegram Desktop only: format "Machine-readable JSON", then ZIP the export folder yourself.',
    steps: [
      {
        items: [
          'Install Telegram Desktop from `https://desktop.telegram.org/` and log in. The phone apps, the web app and the macOS App Store "Telegram for macOS" (Swift) client do not have this export.',
          'Open ☰ → Settings → Advanced → "Export Telegram data".',
          'Tick what to include: Account information, Contacts, Personal chats, Bot chats, Private groups / channels (public ones if you want them), and the media types you want; raise the media size limit if you want large files.',
          'At the bottom, under "Location and format", select "Machine-readable JSON" and note the download path.',
          'Click Export. For security, a recently logged-in desktop session must wait 24 hours (or you can approve the request from your phone) before the export starts.',
          'When it finishes, ZIP the resulting folder (the one that contains `result.json` and the `chats/` etc. media folders). Windows: right-click → Compress to ZIP file; macOS: right-click → Compress; Linux: `zip -r telegram.zip "DataExport_…"`.'
        ]
      }
    ],
    choose: [
      'Format: "Machine-readable JSON" (or "Both"). The default "Human-readable HTML" alone cannot be read.',
      'Keep the folder structure when zipping — media is found by its relative path from `result.json`.',
      'A single chat can be exported the same way (open the chat → ⋮ → "Export chat history" → format JSON); it is accepted too.',
      'Secret chats are never included (they exist only on the devices).'
    ],
    delivery:
      'Nothing is sent to you: Telegram Desktop downloads the data straight into the chosen folder (default `Downloads/Telegram Desktop/DataExport_<date>`). Minutes without media, hours with all media.',
    upload: ['Source: Telegram (`telegram`). Upload the ZIP you created.'],
    events: ['message', 'service', 'contact']
  },
  {
    id: 'snapchat',
    name: 'Snapchat',
    requestUrl: 'https://accounts.snapchat.com/accounts/downloadmydata',
    helpUrl:
      'https://help.snapchat.com/hc/en-us/articles/7012305371156-How-do-I-download-my-data-from-Snapchat',
    where: 'accounts.snapchat.com → My Data',
    format: 'keep "Export JSON files" on; all time',
    wait: 'hours to 2 days',
    hint: 'Keep "Export JSON files" switched on and do not limit the date range.',
    steps: [
      {
        items: [
          'Web: log in at `https://accounts.snapchat.com` → "My Data". App: profile → ⚙ Settings → Account actions → "My Data" (opens the same page).',
          'Select the data to include — all categories; add "Export your Memories" if you want the saved media too.',
          'Make sure "Export JSON files" is ON.',
          'Date range: switch the filter off / choose "All time".',
          'Confirm your e-mail address and press Submit.'
        ]
      }
    ],
    choose: [
      '"Export JSON files": ON. Archivist reads the `json/` folder; the `html/` twin is ignored.',
      'Date range: all time.',
      'Memories are listed as download links that expire — Archivist records them as events, but fetch the media itself soon after the export if you want to keep it.'
    ],
    delivery:
      'Snapchat e-mails you when the export is ready (hours, up to a day or two with Memories). Download it from the same "My Data" page; the links stay valid for about a week. Big exports are split into `mydata~…-1.zip`, `-2.zip`, ….',
    upload: ['Source: Snapchat (`snapchat`).', 'Select all ZIP parts in one upload.'],
    events: [
      'message',
      'snap',
      'memory',
      'friend',
      'friend_request',
      'search',
      'login',
      'call',
      'story',
      'subscription'
    ]
  },
  {
    id: 'spotify',
    name: 'Spotify',
    requestUrl: 'https://www.spotify.com/account/privacy/',
    helpUrl: 'https://support.spotify.com/us/article/understanding-your-data/',
    where: 'spotify.com/account/privacy → Download your data',
    format: '"Extended streaming history" (whole lifetime) and/or "Account data" (last year)',
    wait: 'Account data ≈ 5 days · Extended history up to 30 days',
    hint: 'Tick "Extended streaming history" for your whole listening history (takes up to 30 days); "Account data" covers the last year only.',
    steps: [
      {
        items: [
          'Open `https://www.spotify.com/account/privacy/` in a browser and log in (the apps only link here: Settings → Account → Privacy).',
          'Scroll to "Download your data".',
          'Tick the packages you want (see below) and click "Request data".',
          'Confirm the request through the link in the e-mail Spotify sends right away — without that click nothing is prepared.'
        ]
      }
    ],
    choose: [
      '"Extended streaming history": every play since the account was created, with track URIs and milliseconds played. This is the one you want for a timeline.',
      '"Account data": the last year of plays plus searches, saved library and playlists. Quicker; worth requesting as well for the searches and playlists.',
      '"Technical log information" is not needed.',
      'The packages are requested with separate tick boxes and arrive as separate e-mails/ZIPs. Both history flavours are JSON — there is no format option.',
      'Importing both yields two `spotify.play` events for plays of the last year (they differ in precision and are told apart by the `export` metadata value).'
    ],
    delivery:
      'A download link arrives by e-mail: Account data usually within 5 days, the extended history within up to 30 days (often after a few days). The link is valid for 14 days.',
    upload: [
      'Source: Spotify (`spotify`). Upload `my_spotify_data.zip` unchanged.',
      'Import the two packages as two separate uploads (they are different archives, not parts of one).'
    ],
    events: ['play', 'search', 'save', 'playlist', 'playlist_item']
  },
  {
    id: 'linkedin',
    name: 'LinkedIn',
    requestUrl: 'https://www.linkedin.com/mypreferences/d/download-my-data',
    helpUrl: 'https://www.linkedin.com/help/linkedin/answer/a1339364',
    where: 'Settings & Privacy → Data privacy → Get a copy of your data',
    format: '"Download larger data archive" (ZIP of CSV)',
    wait: '10 minutes (first part) · 24 hours (complete)',
    hint: 'Pick "Download larger data archive" and wait for the second, complete e-mail (about 24 hours).',
    steps: [
      {
        items: [
          'Web: Me → Settings & Privacy → Data privacy → "Get a copy of your data" (`https://www.linkedin.com/mypreferences/d/download-my-data`). Mobile app: profile picture → Settings → Data privacy → same entry.',
          'Select "Download larger data archive, including connections, verifications, contacts, account history, and information we infer about you…".',
          'Click "Request archive" and enter your password.'
        ]
      }
    ],
    choose: [
      'The larger archive — not the "Want something in particular?" tick boxes, which give a partial export.',
      'LinkedIn sends TWO e-mails: a first, partial archive after about 10 minutes (messages, connections, profile) and the complete one within 24 hours. Wait for the complete one — it replaces the first.',
      'File names are English regardless of your UI language; leave the ZIP as it is.'
    ],
    delivery:
      'An e-mail links back to the same settings page, where the archive can be downloaded for 72 hours.',
    upload: [
      'Source: LinkedIn (`linkedin`). Upload the `Complete_LinkedInDataExport_….zip` unchanged.'
    ],
    events: [
      'post',
      'comment',
      'message',
      'reaction',
      'connection',
      'invitation',
      'follow',
      'save',
      'search',
      'position',
      'education'
    ]
  },
  {
    id: 'discord',
    name: 'Discord',
    requestUrl: '',
    helpUrl:
      'https://support.discord.com/hc/en-us/articles/360004027692-Requesting-a-Copy-of-your-Data',
    where: 'User Settings → Data & Privacy → Request all of my data',
    format: 'data package ZIP; keep "Messages" ticked',
    wait: 'up to 30 days (often a few days)',
    hint: 'User Settings → Data & Privacy → "Request all of my data"; keep Messages ticked.',
    steps: [
      {
        items: [
          'Desktop/web: ⚙ User Settings → "Data & Privacy" (older clients: "Privacy & Safety") → scroll to "Request all of my data" → "Request Data". Mobile: avatar → ⚙ → Data & Privacy → "Request all of my data".',
          'If asked which categories to include, keep at least Account, Messages and Servers ticked.',
          'Confirm the request.'
        ]
      }
    ],
    choose: [
      'Messages must be included — they are what Archivist maps.',
      "The package contains only YOUR OWN messages, not the other side of any conversation. That is a limit of Discord's export.",
      'You can only request a package once every 30 days.'
    ],
    delivery:
      "Discord e-mails a download link to the account's verified address — by its own account within up to 30 days, in practice usually a few days. Download `package.zip` promptly; the link expires.",
    upload: ['Source: Discord (`discord`). Upload `package.zip` unchanged.'],
    events: ['message', 'channel', 'server', 'friend']
  }
];

const BY_ID = Object.fromEntries(EXPORT_GUIDES.map((g) => [g.id, g]));

export function exportGuide(sourceId) {
  return BY_ID[sourceId] || null;
}
