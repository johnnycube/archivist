import { describe, expect, it } from 'vitest';
import { attachmentName, externalUrl, isImage, mediaExt, mediaUrl } from './media.js';

describe('mediaExt', () => {
  it('takes the extension from the blob key, then the url, lower-cased', () => {
    expect(mediaExt({ blob_key: 'u/ab/photo.JPG' })).toBe('jpg');
    expect(mediaExt({ url: 'https://cdn/x/clip.mp4?sig=1#t' })).toBe('mp4');
    expect(mediaExt({ blob_key: 'u/ab/photo.png', url: 'x.gif' })).toBe('png');
  });

  it('is empty without an extension or a key', () => {
    expect(mediaExt({ blob_key: 'u/ab/noext' })).toBe('');
    expect(mediaExt({})).toBe('');
    expect(mediaExt(null)).toBe('');
  });
});

describe('isImage', () => {
  it('trusts the mime type when present', () => {
    expect(isImage({ mime: 'image/webp', blob_key: 'a.pdf' })).toBe(true);
    expect(isImage({ mime: 'application/pdf', blob_key: 'a.png' })).toBe(false);
  });

  it('falls back to the inline image extensions', () => {
    expect(isImage({ blob_key: 'a.heic' })).toBe(true);
    expect(isImage({ blob_key: 'a.pdf' })).toBe(false);
    expect(isImage(null)).toBe(false);
  });
});

describe('attachmentName', () => {
  it('is the last path segment of the blob key, or a placeholder', () => {
    expect(attachmentName({ blob_key: 'u/ab/contacts.vcf' })).toBe('contacts.vcf');
    expect(attachmentName({ blob_key: 'trailing/' })).toBe('attachment');
    expect(attachmentName(null)).toBe('attachment');
  });

  it('falls back to the file name of a safe url', () => {
    expect(attachmentName({ url: 'https://cdn.example/a/My%20Clip.mp4?sig=1' })).toBe(
      'My Clip.mp4'
    );
    expect(attachmentName({ url: 'javascript:alert(1)//x.png' })).toBe('attachment');
  });
});

describe('mediaUrl and externalUrl', () => {
  const event = {
    id: 'ev1',
    media: [
      { url: 'https://cdn/x.png', blob_key: 'u/ab/x.png' },
      { url: 'https://cdn/y.png' },
      { url: 'javascript:alert(document.cookie)' },
      { url: 'data:image/png;base64,iVBORw0KGgo=' },
      { url: 'data:image/svg+xml;base64,PHN2Zz4=' },
      { url: 'data:text/html,<script>alert(1)</script>' }
    ]
  };

  it('loads stored media from the server, even when the export had a url', () => {
    expect(mediaUrl(event, 0)).toBe('/api/events/ev1/media/0');
    expect(externalUrl(event, 0)).toBe('');
  });

  it('never loads an export url; offers http(s) ones as external links', () => {
    expect(mediaUrl(event, 1)).toBe('');
    expect(externalUrl(event, 1)).toBe('https://cdn/y.png');
  });

  it('drops script and non-raster urls entirely', () => {
    for (const i of [2, 4, 5]) {
      expect(mediaUrl(event, i)).toBe('');
      expect(externalUrl(event, i)).toBe('');
    }
  });

  it('shows inline raster data images', () => {
    expect(mediaUrl(event, 3)).toBe('data:image/png;base64,iVBORw0KGgo=');
  });
});
