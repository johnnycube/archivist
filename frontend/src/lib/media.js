import { safeHref } from '#lib/url.js';

// Which attachments the server serves inline (see internal/adapters/rest/media.go):
// images/video/audio by extension; everything else (pdf, vcf, docx, …) comes back
// as a download, so the UI must not try to render it as an <img>.
const IMAGE_EXT = new Set(['jpg', 'jpeg', 'png', 'gif', 'webp', 'avif', 'bmp', 'heic']);

export function mediaExt(m) {
  const key = (m && (m.blob_key || m.url)) || '';
  const clean = key.split(/[?#]/)[0];
  const i = clean.lastIndexOf('.');
  return i >= 0 ? clean.slice(i + 1).toLowerCase() : '';
}

export function isImage(m) {
  if (!m) return false;
  if (m.mime) return m.mime.startsWith('image/');
  return IMAGE_EXT.has(mediaExt(m));
}

export function attachmentName(m) {
  const key = (m && m.blob_key) || '';
  const fromKey = key.slice(key.lastIndexOf('/') + 1);
  if (fromKey) return fromKey;
  const url = (m && typeof m.url === 'string' && safeHref(m.url)) || '';
  if (url) {
    const path = new URL(url).pathname;
    const last = decodeURIComponent(path.slice(path.lastIndexOf('/') + 1));
    if (last) return last;
  }
  return 'attachment';
}

// Raster images a page may show inline from a data: URL (no SVG: it can carry
// script; the img-src CSP allows data:).
const DATA_IMAGE = /^data:image\/(png|jpeg|gif|webp|avif);base64,[A-Za-z0-9+/=]+$/;

// mediaUrl is where the page may load attachment idx from: the server's media
// route for anything stored at import (blob_key), or an inline raster data:
// image. A URL that came with the export is untrusted and off-site, so it is
// never loaded (externalUrl offers it as a link instead); '' = nothing to load.
export function mediaUrl(event, idx) {
  const m = event.media[idx];
  if (m.blob_key) return `/api/events/${event.id}/media/${idx}`;
  if (typeof m.url === 'string' && DATA_IMAGE.test(m.url)) return m.url;
  return '';
}

// externalUrl is the export's own http(s) URL of an attachment that was not
// stored (a CDN link, say), to offer as an external link; '' otherwise.
export function externalUrl(event, idx) {
  const m = event.media[idx];
  if (mediaUrl(event, idx)) return '';
  return safeHref(m.url);
}
