// Generates docs/exports.md from src/lib/exportGuides.js so the Markdown guide
// and the in-app help page (/help/exports) cannot drift apart.
//   node frontend/scripts/gen-export-docs.mjs          write docs/exports.md
//   node frontend/scripts/gen-export-docs.mjs --check  exit 1 if it is stale
import { readFileSync, writeFileSync } from 'node:fs';
import { fileURLToPath } from 'node:url';
import { EXPORT_GUIDES, GUIDES_VERIFIED, UPLOAD_CAP } from '../src/lib/exportGuides.js';

const out = fileURLToPath(new URL('../../docs/exports.md', import.meta.url));
const cell = (s) => s.replace(/\|/g, '\\|');
const link = (url, text) => `[${text}](${url})`;

let md = `<!-- GENERATED from frontend/src/lib/exportGuides.js by
     frontend/scripts/gen-export-docs.mjs (make export-docs). Do not edit by hand:
     change the data module, regenerate, and rebuild the UI (make web). -->

# Getting your data — how to request each export

Archivist imports the **data export** ("takeout") every service must hand you on
request. This guide says, per service, where to ask for it, **which options to
choose so Archivist can read it**, how long it takes and how to upload it. The
same guide is built into the app at \`/help/exports\` (linked from the footer and
from the import form).

> **Menus change.** The steps were checked against the vendors' help pages as of
> **${GUIDES_VERIFIED}**; labels and paths move without notice. Each section links the
> vendor's official help article — trust that over this page when they differ.
> What matters for Archivist is in the "Choose these options" lists: a wrong
> format (HTML or TXT instead of JSON) usually costs a multi-day wait for a new
> export.

## Quick reference

| Service | Where to request | Choose | Typical wait |
|---|---|---|---|
${EXPORT_GUIDES.map((g) => `| [${g.name}](#${g.id}) | ${cell(g.where)} | ${cell(g.format)} | ${cell(g.wait)} |`).join('\n')}

## Uploading to Archivist (all services)

1. Open **Import**, pick the **Source** that matches the export and give the
   connection a label (the account's name; for WhatsApp the chat's name).
2. Choose the ZIP **unchanged** — do not unpack or re-pack it (Telegram is the
   exception: you create the ZIP yourself).
3. **Multi-part exports** (Google Takeout, Facebook, Instagram, Snapchat, X):
   select *all* parts in the one file dialog; they are imported as one archive.
4. One upload (all parts together) may be at most **${UPLOAD_CAP}** by default; the
   server operator can raise it with \`MAX_UPLOAD_MB\`.
5. Anything Archivist does not recognise is kept as \`<source>.unknown\`, and
   re-importing the same or a newer export never duplicates events.
`;

for (const g of EXPORT_GUIDES) {
  md += `\n<a id="${g.id}"></a>\n\n## ${g.name}\n\n`;
  md += `Source id: \`${g.id}\``;
  if (g.requestUrl) md += ` · Request page: <${g.requestUrl}>`;
  md += ` · ${link(g.helpUrl, 'Official help article')}\n`;
  md += `\n### Where to request it\n`;
  for (const block of g.steps) {
    md += block.title ? `\n**${block.title}**\n\n` : '\n';
    block.items.forEach((it, i) => (md += `${i + 1}. ${it}\n`));
  }
  md += `\n### Choose these options\n\n`;
  for (const c of g.choose) md += `- ${c}\n`;
  md += `\n### How long it takes, how it arrives\n\n${g.delivery}\n`;
  md += `\n### Upload to Archivist\n\n`;
  for (const u of g.upload) md += `- ${u}\n`;
  md += `\n**What you get:** ${g.events
    .map((e) => {
      const [type, ...rest] = e.split(' ');
      return `\`${g.id}.${type}\`` + (rest.length ? ' ' + rest.join(' ') : '');
    })
    .join(', ')} — everything else as \`${g.id}.unknown\`.\n`;
}

md += `
## See also

- [\`docs/platforms.md\`](./platforms.md) — export formats, event types and the
  verification status of each provider.
- [\`docs/reddit.md\`](./reddit.md) — Reddit specifics.
`;

if (process.argv.includes('--check')) {
  let cur = '';
  try {
    cur = readFileSync(out, 'utf8');
  } catch {
    /* missing */
  }
  if (cur !== md) {
    console.error('docs/exports.md is stale — run: make export-docs');
    process.exit(1);
  }
  console.log('docs/exports.md is up to date');
} else {
  writeFileSync(out, md);
  console.log('wrote ' + out);
}
