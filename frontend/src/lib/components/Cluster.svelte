<script>
  // One collapsed run of repetitive events (same source, type and category,
  // consecutive in time), as the server returns it on a collapsed page:
  // "▶ 37 tracks played · Spotify · 14:02–15:10" plus up to three sample
  // titles. The members are NOT in the payload; expanding fetches them from
  // the plain endpoint, bounded by the run's own time span, and renders them
  // in place. The parent owns the expanded state and keyboard selection so
  // j/k and scroll restoration keep working across clusters.
  import { createEventDispatcher } from 'svelte';
  import EventCard from './EventCard.svelte';
  import { api } from '#lib/api.js';
  import { platform } from '#lib/platform.js';

  // {source_id, type, category, count, first_at, last_at, sample_titles,
  // first_id, last_id}; first_* is the newest member, last_* the oldest.
  export let cluster;
  export let id; // the parent's stable id for this cluster
  export let expanded = false;
  export let selectedId = ''; // keyboard selection: the cluster id or one of its member ids
  export let filterQS = ''; // the user's non-date filters (q, …): members must satisfy them too

  const dispatch = createEventDispatcher();

  // Members per fetch. The server caps a page at 500 events; a run longer than
  // this shows a "show more" step instead of thousands of cards at once.
  const PAGE = 200;

  // Human nouns for the known high-volume types; anything else falls back to
  // the type's tail ("vote.up events"), which is still exact.
  const NOUNS = {
    'spotify.play': ['track played', 'tracks played'],
    'spotify.search': ['search', 'searches'],
    'google.youtube.watch': ['video watched', 'videos watched'],
    'google.youtube.search': ['search', 'searches'],
    'tiktok.watch': ['video watched', 'videos watched'],
    'google.chrome.visit': ['page visited', 'pages visited'],
    'google.location.point': ['location point', 'location points']
  };
  function noun(type, n) {
    const k = NOUNS[type];
    if (k) return k[n === 1 ? 0 : 1];
    const tail = type.split('.').slice(1).join('.') || type;
    return `${tail} ${n === 1 ? 'event' : 'events'}`;
  }

  const fmtT = (iso) =>
    new Date(iso).toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' });
  const isUndated = (iso) => !iso || iso.startsWith('0001');

  $: p = platform(cluster.source_id);
  $: n = cluster.count;
  $: dated = !isUndated(cluster.first_at);
  // members run newest → oldest, so the visible range is last → first
  $: span = dated
    ? n > 1 && cluster.last_at !== cluster.first_at
      ? `${fmtT(cluster.last_at)} – ${fmtT(cluster.first_at)}`
      : fmtT(cluster.first_at)
    : '';
  $: samples = (cluster.sample_titles || []).filter(Boolean);

  const toggle = () => dispatch('toggle', id);

  // ---- members ----
  let evs = []; // loaded members, newest → oldest
  let next = ''; // cursor after the last loaded member ("" = none / not started)
  let loading = false;
  let error = '';
  let loadedFor = ''; // the run (first_id..last_id) evs belong to

  $: runKey = cluster.first_id + '..' + cluster.last_id;
  $: remaining = Math.max(0, n - evs.length);
  $: if (expanded && loadedFor !== runKey && !loading) load(true);

  // A member lies within the run's span in timeline order (occurred_at desc,
  // id desc). The query is already bounded by the span; this only drops
  // neighbours that share a boundary timestamp.
  function inRun(ev) {
    if (!dated) return ev.id <= cluster.first_id && ev.id >= cluster.last_id;
    const t = Date.parse(ev.occurred_at);
    const f = Date.parse(cluster.first_at);
    const l = Date.parse(cluster.last_at);
    if (t > f || t < l) return false;
    if (t === f && ev.id > cluster.first_id) return false;
    if (t === l && ev.id < cluster.last_id) return false;
    return true;
  }

  async function load(fresh) {
    if (fresh) {
      // the run grew (a page merged into it): refetch from its top
      evs = [];
      next = '';
      loadedFor = runKey;
    }
    loading = true;
    error = '';
    try {
      const q = new URLSearchParams(filterQS);
      q.set('source', cluster.source_id);
      q.set('type', cluster.type);
      q.set('category', cluster.category);
      if (dated) {
        q.set('from', cluster.last_at); // inclusive RFC 3339 bounds
        q.set('to', cluster.first_at);
      } else if (!next) {
        // undated events form the timeline's tail; start at its top
        q.set('cursor', api.undatedCursor);
      }
      if (next) q.set('cursor', next);
      q.set('limit', String(Math.min(PAGE, Math.max(1, n - evs.length))));
      const d = await api.events(q.toString());
      const got = (d.events || []).filter(inRun);
      const seen = new Set(evs.map((e) => e.id));
      evs = evs.concat(got.filter((e) => !seen.has(e.id)));
      const atEnd = evs.length >= n || got.some((e) => e.id === cluster.last_id) || !d.next_cursor;
      next = atEnd ? '' : d.next_cursor;
      dispatch('members', { id, events: evs });
    } catch (e) {
      error = e.message || 'could not load';
    } finally {
      loading = false;
    }
  }
</script>

<div
  class="cl"
  class:expanded
  class:selected={selectedId === id}
  style="--src:{p.color}"
  data-eid={id}
>
  <div
    class="hd"
    role="button"
    tabindex="0"
    aria-expanded={expanded}
    aria-label="{n} {noun(cluster.type, n)}, {p.label}{span ? ', ' + span : ''}. {expanded
      ? 'Collapse'
      : 'Expand'}"
    on:click={toggle}
    on:keydown={(e) =>
      (e.key === 'Enter' || e.key === ' ') &&
      e.target === e.currentTarget &&
      (e.preventDefault(), toggle())}
  >
    <span class="caret" aria-hidden="true">{expanded ? '▼' : '▶'}</span>
    <span class="glyph" aria-hidden="true">{p.glyph}</span>
    <span class="count">{n.toLocaleString()} {noun(cluster.type, n)}</span>
    <span class="sep">·</span>
    <span class="src">{p.label}</span>
    {#if span}<span class="sep">·</span><span class="mono">{span}</span>{/if}
    <span class="mono dim">{cluster.type}</span>
    <span class="grow"></span>
    <span class="badge cat-{cluster.category || 'other'}">{cluster.category || 'other'}</span>
  </div>
  {#if !expanded && samples.length}
    <div class="samples">
      {#each samples as t, i (i)}<span class="smp">{t}</span>{/each}
      {#if n > samples.length}<span class="more mono">…</span>{/if}
    </div>
  {/if}
  {#if expanded}
    <div class="kids">
      {#each evs as ev (ev.id)}
        <EventCard event={ev} selected={selectedId === ev.id} on:open />
      {/each}
      {#if loading}
        <div class="state mono" role="status">
          loading {evs.length
            ? `${remaining.toLocaleString()} more`
            : `${n.toLocaleString()} ${noun(cluster.type, n)}`}…
        </div>
      {:else if error}
        <div class="state mono err" role="alert">
          could not load members ({error}) ·
          <button class="lnk" on:click={() => load(!evs.length)}>retry</button>
        </div>
      {:else if next && remaining > 0}
        <button class="fold" on:click={() => load(false)}
          >▼ show {Math.min(PAGE, remaining).toLocaleString()} more · {remaining.toLocaleString()} remaining</button
        >
      {:else if !evs.length}
        <div class="state mono">no members match the current filters</div>
      {/if}
      <button class="fold" on:click={toggle}
        >▲ collapse {n.toLocaleString()} {noun(cluster.type, n)}</button
      >
    </div>
  {/if}
</div>

<style>
  .cl {
    position: relative;
    background: color-mix(in srgb, var(--panel) 72%, var(--bg));
    border: 1px solid var(--border);
    border-left: 3px solid var(--src);
    border-radius: 12px;
    margin-bottom: 8px;
    /* two offset shadow edges: a stack of cards */
    box-shadow:
      0 3px 0 -1px var(--border),
      0 6px 0 -2px color-mix(in srgb, var(--border) 60%, var(--bg));
  }
  .cl.expanded {
    background: transparent;
    box-shadow: none;
  }
  .cl.selected .hd,
  .hd:focus-visible {
    outline: none;
    border-radius: 10px;
    box-shadow: 0 0 0 2px color-mix(in srgb, var(--src) 35%, transparent);
  }
  .hd {
    display: flex;
    align-items: center;
    gap: 8px;
    flex-wrap: wrap;
    padding: 9px 14px;
    cursor: pointer;
    min-width: 0;
    user-select: none;
  }
  .hd:hover {
    background: color-mix(in srgb, var(--src) 6%, transparent);
    border-radius: 10px;
  }
  .caret {
    color: var(--muted);
    font-size: 10px;
    width: 10px;
    flex-shrink: 0;
  }
  .glyph {
    flex-shrink: 0;
    width: 20px;
    height: 20px;
    border-radius: 50%;
    text-align: center;
    font:
      700 10.5px/18px ui-monospace,
      Menlo,
      monospace;
    color: var(--src);
    background: color-mix(in srgb, var(--src) 16%, var(--panel2));
    border: 1px solid color-mix(in srgb, var(--src) 45%, transparent);
  }
  .count {
    font-weight: 600;
  }
  .src {
    color: var(--src);
    font-weight: 600;
    font-size: 12.5px;
  }
  .sep {
    color: var(--muted);
  }
  .mono {
    font-family: ui-monospace, Menlo, monospace;
    font-size: 11.5px;
    color: var(--muted);
  }
  .dim {
    opacity: 0.75;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  .grow {
    flex: 1;
  }
  .samples {
    display: flex;
    flex-wrap: wrap;
    gap: 4px 10px;
    padding: 0 14px 10px 46px;
    color: var(--muted);
    font-size: 12.5px;
    min-width: 0;
  }
  .smp {
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
    max-width: 100%;
  }
  .smp + .smp::before {
    content: '· ';
    opacity: 0.6;
  }
  .more {
    opacity: 0.8;
  }
  .kids {
    padding: 6px 0 2px 18px;
    border-left: 1px dashed color-mix(in srgb, var(--src) 40%, var(--border));
    margin-left: 18px;
  }
  .state {
    padding: 8px 12px 10px;
    letter-spacing: 0.06em;
  }
  .state.err {
    color: var(--danger);
  }
  .lnk {
    border: 0;
    background: transparent;
    padding: 0;
    color: var(--accent);
    font: inherit;
    text-decoration: underline;
    cursor: pointer;
  }
  .fold {
    width: 100%;
    font-family: ui-monospace, Menlo, monospace;
    font-size: 11px;
    letter-spacing: 0.06em;
    color: var(--muted);
    background: transparent;
    padding: 6px 10px;
    margin-bottom: 6px;
  }
  .fold:hover {
    color: var(--ink);
  }
  @media (max-width: 900px) {
    .dim {
      display: none;
    }
    .samples {
      padding-left: 14px;
    }
  }
</style>
