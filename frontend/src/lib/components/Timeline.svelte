<script>
  import { createEventDispatcher, onMount, afterUpdate, tick } from 'svelte';
  import EventCard from './EventCard.svelte';
  import Cluster from './Cluster.svelte';
  import { platform } from '#lib/platform.js';

  // Items newest → oldest: {kind:'event', …event} or {kind:'cluster', …} — a
  // run of repetitive events the server collapsed (GET /api/events?collapse=1);
  // its members are fetched by the Cluster card on demand.
  export let items = [];
  export let cursor = '';
  export let loading = false; // a "load more" request is in flight
  export let paused = false; // modal open: suspend keyboard navigation
  export let memberQS = ''; // the user's non-date filters; a cluster's members must satisfy them too

  const dispatch = createEventDispatcher();

  const DAY = 86400000;
  const DOW = ['SUN', 'MON', 'TUE', 'WED', 'THU', 'FRI', 'SAT'];
  const MON = ['JAN', 'FEB', 'MAR', 'APR', 'MAY', 'JUN', 'JUL', 'AUG', 'SEP', 'OCT', 'NOV', 'DEC'];
  const pad = (n) => String(n).padStart(2, '0');

  // A cluster is identified by its newest member, which stays the same while
  // the page after it merges into it — so expanded state survives "load more".
  const cid = (it) => 'c:' + it.first_id;
  const itemId = (it) => (it.kind === 'cluster' ? cid(it) : it.id);
  const itemAt = (it) => (it.kind === 'cluster' ? it.first_at : it.occurred_at);
  const isUndated = (iso) => !iso || iso.startsWith('0001');

  let expanded = new Set(); // cluster ids currently expanded in place
  let members = new Map(); // cluster id → loaded member events (for keyboard navigation)

  async function toggleCluster(id) {
    const el = document.querySelector(`[data-eid="${id}"]`);
    // collapsing a cluster whose header has scrolled off the top would yank
    // unrelated content under the cursor: keep the header in view instead
    const headerAbove = el && el.getBoundingClientRect().top < 100;
    if (expanded.has(id)) expanded.delete(id);
    else expanded.add(id);
    expanded = expanded;
    if (headerAbove) {
      await tick();
      el.scrollIntoView({ block: 'start', behavior: 'auto' });
    }
  }
  function onMembers(e) {
    members.set(e.detail.id, e.detail.events);
    members = members;
  }

  // ---- grouping: items (already newest→oldest) → day sections + gaps ----
  // Day headings are the viewer's LOCAL days (getFullYear/getMonth/getDate),
  // which is the same boundary the server filters, buckets and collapses on —
  // the page sends it the browser's zone as ?tz=. A cluster sits under the day
  // of its newest member, and a run never crosses that boundary, so a cluster
  // never spans two headings.
  $: ({ groups, undated } = build(items));

  function build(its) {
    const undatedItems = [];
    const map = new Map();
    for (const it of its) {
      const at = itemAt(it);
      if (isUndated(at)) {
        undatedItems.push(it);
        continue;
      }
      const d = new Date(at);
      const key = `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())}`;
      if (!map.has(key)) {
        map.set(key, {
          key,
          start: new Date(d.getFullYear(), d.getMonth(), d.getDate()).getTime(),
          dow: DOW[d.getDay()],
          dayNum: pad(d.getDate()),
          monYear: `${MON[d.getMonth()]} ${d.getFullYear()}`,
          items: [],
          srcs: [],
          gapDays: 0
        });
      }
      const g = map.get(key);
      g.items.push(it);
      const c = platform(it.source_id).color;
      if (!g.srcs.includes(c) && g.srcs.length < 3) g.srcs.push(c);
    }
    const groups = [...map.values()];
    for (let i = 1; i < groups.length; i++) {
      groups[i].gapDays = Math.round((groups[i - 1].start - groups[i].start) / DAY);
    }
    return { groups, undated: undatedItems };
  }

  // Elapsed time → vertical distance (log scale, capped).
  const gapH = (days) => Math.min(150, Math.round(26 + 26 * Math.log2(Math.max(days, 2))));

  function fmtGap(days) {
    if (days < 14) return `${days} days`;
    if (days < 70) return `${Math.round(days / 7)} weeks`;
    if (days < 420) return `${Math.round(days / 30.44)} months`;
    const y = Math.floor(days / 365);
    const m = Math.round((days - y * 365) / 30.44);
    return `${y} ${y > 1 ? 'years' : 'year'}${m ? ` ${m} months` : ''}`;
  }

  // ---- flat order for keyboard navigation: cards and cluster headers, plus
  // the cards inside expanded clusters ----
  $: flat = flatten(groups, undated, expanded, members);
  function flatten(gs, und, exp, mem) {
    const out = [];
    const push = (its) => {
      for (const it of its) {
        if (it.kind === 'event') out.push({ id: it.id, ev: it });
        else {
          const id = cid(it);
          out.push({ id, cluster: it });
          if (exp.has(id)) for (const ev of mem.get(id) || []) out.push({ id: ev.id, ev });
        }
      }
    };
    for (const g of gs) push(g.items);
    push(und);
    return out;
  }
  let sel = -1;
  $: if (sel >= flat.length) sel = flat.length - 1;
  $: selId = sel >= 0 && flat[sel] ? flat[sel].id : '';
  $: nClustered = items.reduce((a, it) => a + (it.kind === 'cluster' ? it.count : 0), 0);

  const reduceMotion = () =>
    typeof matchMedia !== 'undefined' && matchMedia('(prefers-reduced-motion: reduce)').matches;

  function onKey(e) {
    if (paused || e.metaKey || e.ctrlKey || e.altKey) return;
    const t = e.target;
    if (t && (t.tagName === 'INPUT' || t.tagName === 'SELECT' || t.tagName === 'TEXTAREA')) return;
    if (e.key === 'j' || e.key === 'k') {
      e.preventDefault();
      sel = e.key === 'j' ? Math.min(sel + 1, flat.length - 1) : Math.max(sel - 1, 0);
      const el = document.querySelector(`[data-eid="${flat[sel]?.id}"]`);
      el?.scrollIntoView({ block: 'center', behavior: reduceMotion() ? 'auto' : 'smooth' });
    } else if (
      (e.key === 'Enter' || e.key === 'o') &&
      sel >= 0 &&
      document.activeElement?.tagName !== 'BUTTON'
    ) {
      const it = flat[sel];
      if (!it) return;
      if (it.cluster) toggleCluster(it.id);
      else dispatch('open', it.ev);
    }
  }

  // ---- sticky month pill + jump menu ----
  let sectionEls = [];
  let currentMonth = '';
  let menuOpen = false;

  $: months = groups.reduce((acc, g, i) => {
    if (!acc.length || acc[acc.length - 1].label !== g.monYear)
      acc.push({ label: g.monYear, idx: i });
    return acc;
  }, []);

  let ticking = false;
  function onScroll() {
    if (ticking) return;
    ticking = true;
    requestAnimationFrame(() => {
      ticking = false;
      for (let i = 0; i < groups.length; i++) {
        const el = sectionEls[i];
        if (el && el.getBoundingClientRect().bottom > 130) {
          currentMonth = groups[i].monYear;
          return;
        }
      }
      currentMonth = groups.length ? groups[groups.length - 1].monYear : '';
    });
  }
  afterUpdate(onScroll);
  function onWinScroll() {
    chain = 0; // the user moved: auto-chaining may start over
    onScroll();
  }

  function jump(m) {
    menuOpen = false;
    sectionEls[m.idx]?.scrollIntoView({
      block: 'start',
      behavior: reduceMotion() ? 'auto' : 'smooth'
    });
  }

  // ---- drag on the timeline: select a day range (sets the date filters) ----
  let selDrag = null; // {y0, i0, i1, active}

  function dayIdxAtY(y) {
    let best = 0;
    let bestDist = Infinity;
    for (let i = 0; i < groups.length; i++) {
      const el = sectionEls[i];
      if (!el) continue;
      const r = el.getBoundingClientRect();
      if (y >= r.top && y <= r.bottom) return i;
      const d = y < r.top ? r.top - y : y - r.bottom;
      if (d < bestDist) {
        bestDist = d;
        best = i;
      }
    }
    return best;
  }

  function gutterDown(e) {
    // Mouse-only, primary button, and never from inside a card or button —
    // cards keep click-to-open and text selection.
    if (e.pointerType === 'touch' || e.button !== 0 || !groups.length) return;
    if (e.target.closest('.ev') || e.target.closest('.cl') || e.target.closest('button')) return;
    e.preventDefault(); // keep native text selection out of the range drag
    selDrag = { y0: e.clientY, i0: dayIdxAtY(e.clientY), i1: dayIdxAtY(e.clientY), active: false };
    window.addEventListener('pointermove', gutterMove);
    window.addEventListener('pointerup', gutterUp, { once: true });
  }
  function gutterMove(e) {
    if (!selDrag) return;
    if (!selDrag.active && Math.abs(e.clientY - selDrag.y0) < 8) return; // click ≠ select
    selDrag.active = true;
    selDrag.i1 = dayIdxAtY(e.clientY);
    selDrag = selDrag;
    e.preventDefault();
  }
  function gutterUp() {
    window.removeEventListener('pointermove', gutterMove);
    const d = selDrag;
    selDrag = null;
    if (!d || !d.active) return;
    const [a, b] = [Math.min(d.i0, d.i1), Math.max(d.i0, d.i1)];
    // groups run newest→oldest, so the lower index is the "to" side.
    dispatch('range', { from: groups[b].key, to: groups[a].key });
  }
  const inDrag = (i, d) => d && d.active && i >= Math.min(d.i0, d.i1) && i <= Math.max(d.i0, d.i1);

  // ---- infinite scroll ----
  // The observer fires on visibility CHANGES. When a page merges into a
  // cluster that is already on screen (a run cut by the page boundary), the
  // document barely grows, the sentinel stays visible and no new intersection
  // event arrives — so after each load we re-check and chain a few more pages
  // (bounded; a user scroll resets the budget, and the "Load more" button
  // always remains).
  //
  // Auto-loading is bounded to MAX_AUTO pages. Unbounded, it appends a page
  // every time the sentinel comes within rootMargin of the viewport, which
  // pushes whatever follows the timeline (the footer) down faster than anyone
  // can scroll to it — on a large archive the footer is never reachable — and
  // grows the DOM without limit. When the budget runs out the observer is
  // disconnected and only "Load more" continues; a click buys another MAX_AUTO.
  // Unlike `chain`, this budget is deliberately NOT reset by scrolling.
  let sentinel;
  let io;
  let chain = 0;
  const MAX_CHAIN = 3;
  let autoLoads = 0;
  const MAX_AUTO = 4;
  let wasLoading = false;

  // A shorter list than last render means a fresh query (the filters changed):
  // the user is starting over, so auto-loading starts over with them.
  let prevLen = 0;
  $: {
    if (items.length < prevLen) autoLoads = 0;
    prevLen = items.length;
  }

  $: {
    if (wasLoading && !loading) afterLoad();
    wasLoading = loading;
  }

  // autoMore spends one page of the budget, and reports whether it could.
  function autoMore() {
    if (autoLoads >= MAX_AUTO) {
      io?.disconnect();
      return false;
    }
    autoLoads++;
    dispatch('more');
    return true;
  }

  async function afterLoad() {
    await tick();
    if (
      sentinel &&
      cursor &&
      chain < MAX_CHAIN &&
      sentinel.getBoundingClientRect().top < window.innerHeight + 700
    ) {
      chain++;
      if (autoMore()) return; // spent a page; re-arm after that one settles
    }
    // Re-arm the observer when the budget allows. Observing an element that is
    // already observed is a no-op, so this only has an effect after a click.
    if (sentinel && io && autoLoads < MAX_AUTO) io.observe(sentinel);
  }

  // "Load more" is explicit intent: it loads a page and re-arms auto-loading.
  function loadMore() {
    autoLoads = 0;
    dispatch('more');
  }

  onMount(() => {
    io = new IntersectionObserver((entries) => entries[0].isIntersecting && autoMore(), {
      rootMargin: '700px'
    });
    io.observe(sentinel);
    return () => io.disconnect();
  });
</script>

<svelte:window on:keydown={onKey} on:scroll={onWinScroll} on:click={() => (menuOpen = false)} />

<div class="tl" class:noselect={!!selDrag} on:pointerdown={gutterDown} role="presentation">
  {#if months.length}
    <div class="pillwrap">
      <div class="pillbox" on:click|stopPropagation role="presentation">
        <button
          class="pill"
          on:click={() => (menuOpen = !menuOpen)}
          aria-haspopup="menu"
          aria-expanded={menuOpen}
        >
          {currentMonth || months[0].label} <span class="caret">▾</span>
        </button>
        {#if menuOpen}
          <div class="menu" role="menu">
            {#each months as m (m.label)}
              <button role="menuitem" class:on={m.label === currentMonth} on:click={() => jump(m)}
                >{m.label}</button
              >
            {/each}
          </div>
        {/if}
      </div>
    </div>
  {/if}

  {#each groups as g, i (g.key)}
    {#if i > 0 && g.gapDays > 2}
      <div class="gap" style="height:{gapH(g.gapDays)}px">
        <div class="punch"></div>
        <span class="glabel">{fmtGap(g.gapDays)}</span>
      </div>
    {/if}
    <section class="day" class:selecting={inDrag(i, selDrag)} bind:this={sectionEls[i]}>
      <div class="rail">
        <div class="dow">{g.dow}</div>
        <div class="dnum">{g.dayNum}</div>
        <div class="dmy">{g.monYear}</div>
      </div>
      <div class="spine">
        <div class="dots">
          {#each g.srcs as c (c)}<i style="background:{c}"></i>{/each}
        </div>
      </div>
      <div class="cards">
        {#each g.items as it (itemId(it))}
          {#if it.kind === 'cluster'}
            <Cluster
              cluster={it}
              id={cid(it)}
              expanded={expanded.has(cid(it))}
              selectedId={selId}
              filterQS={memberQS}
              on:toggle={(e) => toggleCluster(e.detail)}
              on:members={onMembers}
              on:open
            />
          {:else}
            <EventCard event={it} selected={selId === it.id} on:open />
          {/if}
        {/each}
      </div>
    </section>
  {/each}

  {#if undated.length}
    <div class="gap" style="height:56px">
      <div class="punch"></div>
      <span class="glabel">undated</span>
    </div>
    <section class="day">
      <div class="rail">
        <div class="dow">—</div>
        <div class="dmy">NO DATE</div>
      </div>
      <div class="spine"></div>
      <div class="cards">
        {#each undated as it (itemId(it))}
          {#if it.kind === 'cluster'}
            <Cluster
              cluster={it}
              id={cid(it)}
              expanded={expanded.has(cid(it))}
              selectedId={selId}
              filterQS={memberQS}
              on:toggle={(e) => toggleCluster(e.detail)}
              on:members={onMembers}
              on:open
            />
          {:else}
            <EventCard event={it} selected={selId === it.id} on:open />
          {/if}
        {/each}
      </div>
    </section>
  {/if}

  <div class="sentinel" bind:this={sentinel}>
    {#if loading}
      <span class="muted mono">loading…</span>
    {:else if cursor}
      <button on:click={loadMore}>Load more</button>
    {:else if groups.length || undated.length}
      <span class="muted mono">· beginning of archive ·</span>
    {/if}
  </div>

  {#if flat.length}
    <div class="keys muted mono">
      {#if nClustered}{nClustered.toLocaleString()} repetitive events grouped ·
      {/if}drag dates to filter · j / k navigate · enter opens / toggles a group
    </div>
  {/if}
</div>

<style>
  .tl {
    --railw: 84px;
    --spinew: 28px;
    --colgap: 14px;
    position: relative;
    padding-top: 4px;
  }
  /* the continuous spine */
  .tl::before {
    content: '';
    position: absolute;
    top: 10px;
    bottom: 44px;
    left: calc(var(--railw) + var(--colgap) + var(--spinew) / 2 - 1px);
    width: 2px;
    background: var(--border);
  }

  .day,
  .gap {
    display: grid;
    grid-template-columns: var(--railw) var(--spinew) minmax(0, 1fr);
    column-gap: var(--colgap);
    position: relative;
  }
  .day {
    padding: 4px 0;
    scroll-margin-top: 90px;
  }
  .day.selecting {
    background: linear-gradient(90deg, rgba(110, 168, 254, 0.1), rgba(110, 168, 254, 0.03));
    border-radius: 10px;
  }
  .tl.noselect {
    user-select: none;
  }
  .day.selecting .rail {
    color: var(--accent);
  }
  .day .rail,
  .day .spine,
  .gap {
    cursor: crosshair;
  }

  .rail {
    text-align: right;
    font-family: ui-monospace, Menlo, monospace;
    position: sticky;
    top: 74px;
    align-self: start;
    padding-top: 6px;
    line-height: 1.25;
  }
  .dow {
    font-size: 10.5px;
    color: var(--muted);
    letter-spacing: 0.1em;
  }
  .dnum {
    font-size: 22px;
    font-weight: 700;
    letter-spacing: -0.02em;
  }
  .dmy {
    font-size: 10.5px;
    color: var(--muted);
    letter-spacing: 0.06em;
    white-space: nowrap;
  }

  .spine {
    position: relative;
  }
  .dots {
    position: absolute;
    top: 16px;
    left: 50%;
    transform: translateX(-50%);
    display: flex;
    align-items: center;
    z-index: 1;
  }
  .dots i {
    display: block;
    width: 11px;
    height: 11px;
    border-radius: 50%;
    border: 2px solid var(--bg);
  }
  .dots i + i {
    margin-left: -5px;
  }

  /* time gaps: dashed spine + duration label */
  .gap {
    align-items: center;
  }
  .punch {
    grid-column: 2;
    justify-self: center;
    width: 4px;
    height: 100%;
    background: repeating-linear-gradient(180deg, var(--bg) 0 5px, transparent 5px 10px);
  }
  .glabel {
    grid-column: 2;
    grid-row: 1;
    justify-self: center;
    z-index: 1;
    white-space: nowrap;
    font-family: ui-monospace, Menlo, monospace;
    font-size: 11px;
    color: var(--muted);
    background: var(--bg);
    border: 1px solid var(--border);
    border-radius: 999px;
    padding: 3px 10px;
  }

  /* floating month pill */
  .pillwrap {
    position: sticky;
    top: 64px;
    z-index: 6;
    display: flex;
    justify-content: flex-end;
    height: 32px;
    margin-bottom: 4px;
  }
  .pillbox {
    position: relative;
  }
  .pill {
    font-family: ui-monospace, Menlo, monospace;
    font-size: 12px;
    letter-spacing: 0.08em;
    padding: 6px 12px;
    border-radius: 999px;
    background: color-mix(in srgb, var(--panel) 82%, transparent);
    backdrop-filter: blur(6px);
  }
  .caret {
    color: var(--muted);
  }
  .menu {
    position: absolute;
    top: 38px;
    right: 0;
    display: flex;
    flex-direction: column;
    background: var(--panel);
    border: 1px solid var(--border);
    border-radius: 12px;
    padding: 6px;
    max-height: 320px;
    overflow-y: auto;
    box-shadow: 0 14px 34px rgba(0, 0, 0, 0.45);
  }
  .menu button {
    border: 0;
    background: transparent;
    text-align: right;
    font-family: ui-monospace, Menlo, monospace;
    font-size: 12px;
    letter-spacing: 0.06em;
    padding: 6px 12px;
    border-radius: 8px;
    white-space: nowrap;
  }
  .menu button:hover {
    background: var(--panel2);
  }
  .menu button.on {
    color: var(--accent);
  }

  .sentinel {
    display: flex;
    justify-content: center;
    padding: 18px 0 6px calc(var(--railw) + var(--colgap));
  }
  .mono {
    font-family: ui-monospace, Menlo, monospace;
    font-size: 11.5px;
    letter-spacing: 0.06em;
  }
  .keys {
    text-align: right;
    padding: 6px 0 2px;
    font-size: 10.5px;
  }

  @media (max-width: 900px) {
    .tl {
      --railw: 56px;
      --colgap: 10px;
    }
    .dnum {
      font-size: 17px;
    }
    .dmy {
      display: none;
    }
    .keys {
      display: none;
    }
  }
</style>
