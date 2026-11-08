<script>
  import { onMount } from 'svelte';
  import { pushState, replaceState } from '$app/navigation';
  import { api } from '#lib/api.js';
  import Login from '#lib/components/Login.svelte';
  import Header from '#lib/components/Header.svelte';
  import Sidebar from '#lib/components/Sidebar.svelte';
  import RangeBar from '#lib/components/RangeBar.svelte';
  import Overview from '#lib/components/Overview.svelte';
  import Timeline from '#lib/components/Timeline.svelte';
  import EventModal from '#lib/components/EventModal.svelte';

  let user = null;
  let ready = false;

  // Every category the API knows (domain.Category); "hide consumption" is
  // expressed with the existing `category` include-list as all-but-consumption.
  const ALL_CATEGORIES = [
    'content',
    'message',
    'reaction',
    'save',
    'connection',
    'media',
    'profile',
    'consumption',
    'other'
  ];
  const emptyFilters = () => ({
    sources: new Set(),
    categories: new Set(),
    types: new Set(),
    q: '',
    from: '',
    to: '',
    hideConsumption: false
  });

  let filters = emptyFilters();
  // Timeline items, newest → oldest. Each is either an event ({kind:'event',
  // …event fields}) or, when repeats are grouped, a server-side cluster of one
  // run ({kind:'cluster', source_id, type, category, count, first_at, last_at,
  // sample_titles, first_id, last_id}; first_* is the newest member).
  let items = [];
  let facets = { sources: [], categories: [], types: [] };
  let archFrom = ''; // effective chart range, mirrored into the sidebar dates
  let archTo = '';
  // Whole-archive day histogram under the non-date filters. Fetched here, once,
  // and shared by the range bar and the chart so their counts cannot disagree.
  let days = [];
  let total = 0;
  let cursor = '';
  let selected = null;
  let loading = false;

  // Per-browser view preference: collapse runs of repetitive events (default
  // on). The collapsing happens on the server (GET /api/events?collapse=1), so
  // flipping it refetches the list.
  const PREF_GROUP = 'archivist.groupRepeats';
  let groupRepeats = true;
  try {
    const v = localStorage.getItem(PREF_GROUP);
    if (v !== null) groupRepeats = v !== '0';
  } catch {
    /* storage unavailable: keep the default */
  }
  function onPref() {
    try {
      localStorage.setItem(PREF_GROUP, groupRepeats ? '1' : '0');
    } catch {
      /* ignore */
    }
    reload();
  }

  // Exact type → category map from the provider catalog, so the sidebar can
  // mute every consumption type's count, seen in the loaded page or not.
  let catalogConsumption = new Set();
  async function loadCatalog() {
    try {
      const d = await api.sources();
      const s = new Set();
      for (const src of d.sources || [])
        for (const t of src.event_types || []) if (t.category === 'consumption') s.add(t.type);
      catalogConsumption = s;
    } catch {
      /* the loaded page still tells us */
    }
  }

  onMount(async () => {
    applyURL(location.search); // bookmarked/shared filters apply BEFORE the first fetch
    try {
      user = await api.me();
      urlQS = filtersToQS(); // the address bar already says this; do not push it again
      await Promise.all([reload(), loadCatalog()]);
    } catch {
      user = null;
    } finally {
      ready = true;
    }
  });

  // ---------------------------------------------------------------- URL state
  // The filters live in the address bar under the API's own parameter names, so
  // a view can be bookmarked, shared and walked with Back/Forward. Transient
  // state (scroll, the group-repeats preference) stays out of it.
  const ISO = /^\d{4}-\d{2}-\d{2}$/;
  const listParam = (v) =>
    new Set(
      (v || '')
        .split(',')
        .map((s) => s.trim())
        .filter(Boolean)
    );

  function applyURL(search) {
    const p = new URLSearchParams(search);
    const from = p.get('from') || '';
    const to = p.get('to') || '';
    filters = {
      sources: listParam(p.get('source')),
      categories: listParam(p.get('category')),
      types: listParam(p.get('type')),
      q: p.get('q') || '',
      from: ISO.test(from) ? from : '',
      to: ISO.test(to) ? to : '',
      hideConsumption: listParam(p.get('hide')).has('consumption')
    };
  }

  function filtersToQS() {
    const p = new URLSearchParams();
    if (filters.from) p.set('from', filters.from);
    if (filters.to) p.set('to', filters.to);
    if (filters.sources.size) p.set('source', [...filters.sources].join(','));
    if (filters.categories.size) p.set('category', [...filters.categories].join(','));
    if (filters.types.size) p.set('type', [...filters.types].join(','));
    if (filters.q.trim()) p.set('q', filters.q.trim());
    if (filters.hideConsumption) p.set('hide', 'consumption');
    return p.toString();
  }

  // What the address bar currently shows (null until the first load settled).
  let urlQS = null;
  let qTimer;

  // A drill-down, a widening or a facet change is a navigation step — Back
  // should walk back up. Typing in the search box is not: it replaces, debounced.
  function onlyQChanged(prev, next) {
    const a = new URLSearchParams(prev);
    const b = new URLSearchParams(next);
    const keys = new Set([...a.keys(), ...b.keys()]);
    let diff = 0;
    let q = false;
    for (const k of keys) {
      if (a.get(k) === b.get(k)) continue;
      diff++;
      q = k === 'q';
    }
    return diff === 1 && q;
  }

  function syncURL() {
    if (urlQS === null) return; // still booting, or restoring from the URL
    const qs = filtersToQS();
    if (qs === urlQS) return;
    const replace = onlyQChanged(urlQS, qs);
    urlQS = qs;
    const href = location.pathname + (qs ? '?' + qs : '');
    clearTimeout(qTimer);
    if (replace) qTimer = setTimeout(() => replaceState(href, {}), 350);
    else pushState(href, {});
  }

  // Back/Forward: re-apply the URL and reload, without writing history again.
  function onPop() {
    if (!user) return;
    clearTimeout(qTimer);
    applyURL(location.search);
    urlQS = filtersToQS(); // == what the address bar says, so reload() pushes nothing
    reload();
  }

  // ---------------------------------------------------------------- histogram
  let histLoaded = null;
  let histSeq = 0;
  $: if (user && histQS !== histLoaded) loadHistogram(histQS);
  async function loadHistogram(q) {
    histLoaded = q;
    const seq = ++histSeq;
    try {
      const d = await api.histogram(q);
      if (seq === histSeq) days = d.buckets || [];
    } catch {
      if (seq === histSeq) days = [];
    }
  }

  function categoryParam() {
    let cats = [...filters.categories];
    if (filters.hideConsumption) {
      if (!cats.length) cats = ALL_CATEGORIES;
      cats = cats.filter((c) => c !== 'consumption');
      if (!cats.length) cats = ALL_CATEGORIES.filter((c) => c !== 'consumption');
    }
    return cats;
  }

  // The viewer's IANA time zone, read once. The server draws every day
  // boundary in it — which local day a histogram bucket is, where a date-only
  // from/to starts and ends, where a collapsed run is cut — so the chips, the
  // chart, the day headings and the sidebar dates all describe the same days.
  // It deliberately stays OUT of the URL: a zone is a property of the viewer,
  // not of the view, so a link shared across the world shows each reader their
  // own days. Without Intl (never, in a browser we support) the parameter is
  // simply omitted and the server answers in UTC, as it always did.
  const TZ = (() => {
    try {
      return Intl.DateTimeFormat().resolvedOptions().timeZone || '';
    } catch {
      return '';
    }
  })();

  // The non-date filters plus the viewer's zone, shared by the list, the chart
  // and a cluster's member fetch.
  function baseParams() {
    const p = new URLSearchParams();
    if (TZ) p.set('tz', TZ);
    if (filters.sources.size) p.set('source', [...filters.sources].join(','));
    const cats = categoryParam();
    if (cats.length) p.set('category', cats.join(','));
    if (filters.types.size) p.set('type', [...filters.types].join(','));
    if (filters.q.trim()) p.set('q', filters.q.trim());
    return p;
  }

  function buildQuery(withCursor) {
    const p = baseParams();
    if (filters.from) p.set('from', filters.from);
    if (filters.to) p.set('to', filters.to);
    p.set('limit', '25'); // items when collapsing, events otherwise
    if (withCursor && cursor) p.set('cursor', cursor);
    return p.toString();
  }

  // One page, in either mode, as items. Plain events are tagged so the
  // timeline renders both shapes the same way.
  async function fetchPage(qs) {
    if (groupRepeats) {
      const d = await api.eventsCollapsed(qs);
      return { ...d, items: d.items || [] };
    }
    const d = await api.events(qs);
    return { ...d, items: (d.events || []).map((e) => ({ kind: 'event', ...e })) };
  }

  // A run cut by a page boundary continues as a new cluster at the top of the
  // next page: fold it into the trailing one. Merging is only right when the
  // two pieces are one run by the server's own rule — same source, type and
  // category, same LOCAL day (the server cuts runs at midnight in the zone we
  // send it, so the comparison must be made in the same zone, not on the
  // timestamps' UTC prefix), and no more than the default 30 min gap between
  // them (or both undated) — otherwise two genuine runs (e.g. the last of one
  // day and the first of the day before) would show as one.
  const undated = (iso) => !iso || iso.startsWith('0001');
  const GAP_MS = 30 * 60 * 1000;
  const localDay = (iso) => {
    const d = new Date(iso);
    return `${d.getFullYear()}-${d.getMonth()}-${d.getDate()}`;
  };
  function sameRun(a, b) {
    if (a.kind !== 'cluster' || b.kind !== 'cluster') return false;
    if (a.source_id !== b.source_id || a.type !== b.type || a.category !== b.category) return false;
    if (undated(a.last_at) || undated(b.first_at)) return undated(a.last_at) && undated(b.first_at);
    if (localDay(a.last_at) !== localDay(b.first_at)) return false;
    return Date.parse(a.last_at) - Date.parse(b.first_at) <= GAP_MS;
  }
  function appendItems(prev, next) {
    if (!prev.length || !next.length || !sameRun(prev[prev.length - 1], next[0]))
      return prev.concat(next);
    const a = prev[prev.length - 1];
    const b = next[0];
    const merged = {
      ...a,
      count: a.count + b.count,
      last_at: b.last_at,
      last_id: b.last_id,
      sample_titles: [...new Set([...(a.sample_titles || []), ...(b.sample_titles || [])])].slice(
        0,
        3
      )
    };
    return [...prev.slice(0, -1), merged, ...next.slice(1)];
  }

  // Non-date filters only: the histogram behind the range bar and the chart
  // always covers the WHOLE archive, so a chip can say how many events a year
  // holds even while another year is pinned. The same string tells a cluster
  // which of the user's filters its members must satisfy.
  let histQS = '';

  // Rapid filter changes (e.g. From then To) race their responses; only the
  // newest request may write the list, and a "load more" that started under
  // older filters must not append to the new list.
  let reqSeq = 0;

  async function reload() {
    filters = filters; // sidebar mutates in place; retrigger our reactive props
    histQS = baseParams().toString();
    syncURL();
    const seq = ++reqSeq;
    loading = true;
    try {
      const d = await fetchPage(buildQuery(false));
      if (seq !== reqSeq) return; // superseded by a newer request
      items = d.items;
      facets = d.facets || facets;
      total = d.total || 0;
      cursor = d.next_cursor || '';
    } finally {
      if (seq === reqSeq) loading = false;
    }
  }

  // THE one way the active date range changes: the range bar's chips and trail,
  // a drag or click on the chart, a drag on the timeline. The sidebar's date
  // inputs write filters.from/to directly and then dispatch `change`, which is
  // the same code path — so the trail, the chart, the inputs, the list and the
  // URL always show the same range.
  function onRange(e) {
    filters.from = e.detail.from;
    filters.to = e.detail.to;
    filters = filters;
    cursor = '';
    reload();
  }

  let loadingMore = false;
  async function loadMore() {
    if (!cursor || loadingMore) return;
    loadingMore = true;
    const seq = reqSeq;
    try {
      const d = await fetchPage(buildQuery(true));
      if (seq !== reqSeq) return; // filters changed meanwhile: this page is stale
      items = appendItems(items, d.items);
      cursor = d.next_cursor || '';
    } finally {
      loadingMore = false;
    }
  }

  function clearFilters() {
    filters = emptyFilters();
    reload();
  }

  // Consumption types: the catalog's, plus any the loaded page classifies so
  // (a source may emit types the catalog does not list).
  $: consumptionTypes = new Set([
    ...catalogConsumption,
    ...items.filter((it) => it.category === 'consumption').map((it) => it.type)
  ]);
  $: consumptionTotal =
    (facets.categories || []).find((f) => f.value === 'consumption')?.count || 0;

  async function onAuthed(e) {
    user = e.detail;
    if (urlQS === null) urlQS = filtersToQS(); // start tracking the address bar
    await reload();
  }
  async function logout() {
    await api.logout();
    user = null;
    items = [];
    days = [];
    histLoaded = null;
  }
</script>

<svelte:window on:popstate={onPop} />

{#if !ready}
  <div class="center muted">Loading…</div>
{:else if !user}
  <Login on:authed={onAuthed} />
{:else}
  <Header {user} on:logout={logout} />

  <div class="layout">
    <Sidebar
      {facets}
      {filters}
      {archFrom}
      {archTo}
      {consumptionTypes}
      bind:groupRepeats
      on:pref={onPref}
      on:change={reload}
      on:clear={clearFilters}
    />

    <main>
      <div class="bar mono muted">
        {total.toLocaleString()} events{#if consumptionTotal && !filters.hideConsumption}<span
            class="dimmer"
            title="high-volume consumption events (plays, watches, visits)"
          >
            · {consumptionTotal.toLocaleString()} consumption</span
          >{/if}
      </div>

      <RangeBar {days} from={filters.from} to={filters.to} on:range={onRange} />

      <Overview
        {days}
        from={filters.from}
        to={filters.to}
        on:domain={(e) => ({ from: archFrom, to: archTo } = e.detail)}
        on:range={onRange}
      />

      {#if loading && !items.length}
        <div class="muted">Loading…</div>
      {:else if !items.length}
        <div class="muted">No events yet — <a href="/import">import an export</a>.</div>
      {:else}
        <Timeline
          {items}
          {cursor}
          loading={loadingMore}
          paused={!!selected}
          memberQS={histQS}
          on:open={(e) => (selected = e.detail)}
          on:more={loadMore}
          on:range={onRange}
        />
      {/if}
    </main>
  </div>

  <EventModal event={selected} on:close={() => (selected = null)} />
{/if}

<style>
  .center {
    text-align: center;
    margin-top: 20vh;
  }
  .layout {
    display: grid;
    grid-template-columns: 300px minmax(0, 1fr);
  }
  main {
    padding: 14px 22px 18px;
    max-width: 1000px;
  }
  .bar {
    padding: 2px 0 10px;
  }
  .mono {
    font-family: ui-monospace, Menlo, monospace;
    font-size: 11.5px;
    letter-spacing: 0.08em;
    text-transform: uppercase;
  }
  .dimmer {
    opacity: 0.6;
  }
  @media (max-width: 820px) {
    .layout {
      grid-template-columns: minmax(0, 1fr);
    }
  }
</style>
