import { describe, expect, it } from 'vitest';
import { CHART_STACK, PLATFORMS, chartColor, platform } from './platform.js';

describe('platform', () => {
  it('returns the brand entry for a known source', () => {
    expect(platform('reddit')).toEqual({ label: 'Reddit', color: '#ff4500', glyph: 'R' });
  });

  it('derives a neutral entry for an unknown source', () => {
    expect(platform('mastodon')).toEqual({ label: 'mastodon', color: '#8b93a7', glyph: 'M' });
    expect(platform('')).toEqual({ label: 'unknown', color: '#8b93a7', glyph: '?' });
    expect(platform(undefined).glyph).toBe('?');
  });
});

describe('chartColor', () => {
  it('uses the chart-safe color where one is defined, else the brand color', () => {
    expect(chartColor('claude')).toBe('#cf6a45');
    expect(chartColor('reddit')).toBe(PLATFORMS.reddit.color);
    expect(chartColor('unknown-source')).toBe('#8b93a7');
  });
});

describe('CHART_STACK', () => {
  it('lists every platform exactly once', () => {
    expect([...CHART_STACK].sort()).toEqual(Object.keys(PLATFORMS).sort());
    expect(new Set(CHART_STACK).size).toBe(CHART_STACK.length);
  });
});
