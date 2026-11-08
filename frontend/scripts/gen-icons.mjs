// Renders the icon set in frontend/static/ from ONE master, static/favicon.svg,
// so the favicon, the touch/app icons and the in-app logo cannot drift apart.
//   node frontend/scripts/gen-icons.mjs   (make icons)
// resvg (pinned in mise.toml) rasterizes deterministically; the outputs are
// committed, so neither the UI build nor CI needs it. Rebuild the UI afterwards
// (make web): static/ is copied into internal/webui/dist.
import { execFileSync } from 'node:child_process';
import { mkdtempSync, readFileSync, rmSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { fileURLToPath } from 'node:url';

const STATIC = fileURLToPath(new URL('../static/', import.meta.url));
const master = join(STATIC, 'favicon.svg');

// Full-bleed variant for platforms that cut their own shape out of the icon
// (iOS home screen, Android adaptive icons): a square tile without rounded
// corners or rim, and the mark shrunk to 75 % so it stays inside the maskable
// safe zone (a centred circle of 40 % radius).
function fullBleed(svg) {
  const defs = svg.match(/<defs>[\s\S]*?<\/defs>/)?.[0];
  const mark = svg.match(/<g id="mark">[\s\S]*?<\/g>/)?.[0];
  if (!defs || !mark) throw new Error(`${master}: expected <defs> and <g id="mark">`);
  return (
    '<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 64 64">' +
    `${defs}<rect width="64" height="64" fill="url(#tile)"/>` +
    `<g transform="translate(8 8) scale(.75)">${mark}</g></svg>\n`
  );
}

// ICO container holding PNG images (supported everywhere since Windows Vista):
// a 6-byte ICONDIR, one 16-byte entry per image, then the PNGs themselves.
function ico(images) {
  const head = Buffer.alloc(6 + 16 * images.length);
  head.writeUInt16LE(1, 2); // type: icon
  head.writeUInt16LE(images.length, 4);
  let offset = head.length;
  images.forEach(({ size, png }, i) => {
    const e = 6 + 16 * i;
    head.writeUInt8(size % 256, e); // width; 0 means 256
    head.writeUInt8(size % 256, e + 1); // height
    head.writeUInt16LE(1, e + 4); // colour planes
    head.writeUInt16LE(32, e + 6); // bits per pixel
    head.writeUInt32LE(png.length, e + 8);
    head.writeUInt32LE(offset, e + 12);
    offset += png.length;
  });
  return Buffer.concat([head, ...images.map((i) => i.png)]);
}

const tmp = mkdtempSync(join(tmpdir(), 'archivist-icons-'));
try {
  const bleed = join(tmp, 'full-bleed.svg');
  writeFileSync(bleed, fullBleed(readFileSync(master, 'utf8')));

  const render = (svg, size) => {
    const out = join(tmp, `${size}.png`);
    try {
      execFileSync('resvg', ['-w', String(size), '-h', String(size), svg, out]);
    } catch (err) {
      if (err.code === 'ENOENT')
        throw new Error('resvg not found: run `make tools` (mise.toml)', { cause: err });
      throw err;
    }
    return readFileSync(out);
  };
  const write = (name, data) => {
    writeFileSync(join(STATIC, name), data);
    console.log(`wrote frontend/static/${name} (${data.length} bytes)`);
  };

  write('favicon.ico', ico([16, 32, 48].map((size) => ({ size, png: render(master, size) }))));
  write('icon-192.png', render(master, 192));
  write('icon-512.png', render(master, 512));
  write('apple-touch-icon.png', render(bleed, 180));
  write('icon-maskable-512.png', render(bleed, 512));
} finally {
  rmSync(tmp, { recursive: true, force: true });
}
