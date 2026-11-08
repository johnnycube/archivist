import { describe, expect, it } from 'vitest';
import { safeHref } from './url.js';

describe('safeHref', () => {
  it('keeps http and https links, normalised', () => {
    expect(safeHref('https://example.com/a?b=1#c')).toBe('https://example.com/a?b=1#c');
    expect(safeHref('  http://example.com ')).toBe('http://example.com/');
  });

  it('refuses every other scheme so export data cannot run script', () => {
    expect(safeHref('javascript:alert(1)')).toBe('');
    expect(safeHref('data:text/html,hi')).toBe('');
    expect(safeHref('ftp://example.com/f')).toBe('');
  });

  it('returns an empty string for non-URLs and non-strings', () => {
    expect(safeHref('not a url')).toBe('');
    expect(safeHref('')).toBe('');
    expect(safeHref(null)).toBe('');
    expect(safeHref(42)).toBe('');
  });
});
