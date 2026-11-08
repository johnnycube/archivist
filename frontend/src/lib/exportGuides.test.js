import { describe, expect, it } from 'vitest';
import { EXPORT_GUIDES, GUIDES_VERIFIED, UPLOAD_CAP, exportGuide } from './exportGuides.js';
import { PLATFORMS } from './platform.js';

describe('export guides', () => {
  it('cover every platform the UI knows, and nothing else', () => {
    const ids = EXPORT_GUIDES.map((g) => g.id);
    expect(new Set(ids).size).toBe(ids.length);
    expect([...ids].sort()).toEqual(Object.keys(PLATFORMS).sort());
  });

  it('each carry the fields the help page and docs/exports.md render', () => {
    for (const g of EXPORT_GUIDES) {
      for (const key of ['name', 'helpUrl', 'where', 'format', 'wait', 'hint']) {
        expect(g[key], `${g.id}.${key}`).toEqual(expect.any(String));
        expect(g[key].length, `${g.id}.${key}`).toBeGreaterThan(0);
      }
      // requestUrl is empty where the export is requested inside the app itself.
      if (g.requestUrl) expect(g.requestUrl, g.id).toMatch(/^https:\/\//);
      expect(g.helpUrl, g.id).toMatch(/^https:\/\//);
    }
  });

  it('look up a guide by source id', () => {
    expect(exportGuide('reddit')?.name).toBe('Reddit');
    expect(exportGuide('nope')).toBeNull();
  });

  it('state when the guides were verified and the upload cap', () => {
    expect(GUIDES_VERIFIED).toMatch(/^\d{4}-\d{2}$/);
    expect(UPLOAD_CAP).toMatch(/^\d+ [GM]iB$/);
  });
});
