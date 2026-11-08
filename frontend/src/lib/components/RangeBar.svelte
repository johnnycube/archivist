<script>
  import { createEventDispatcher } from 'svelte';

  const dispatch = createEventDispatcher();

  // The full-archive day histogram under the NON-date filters — the very array
  // the chart is drawn from ({day:'YYYY-MM-DD', by_source:{src:n}}), so the
  // counts on the chips and the bars always agree and nothing is fetched twice.
  export let days = [];

  // The pinned date range (inclusive, YYYY-MM-DD). This is the ONLY state: the
  // level, the breadcrumb and the chips are all derived from it, so the bar
  // stays correct when the range is set from the sidebar's date inputs or by a
  // drag on the timeline — not just by its own chips.
  export let from = '';
  export let to = '';

  const DAY = 86400000;
  const MON = ['Jan', 'Feb', 'Mar', 'Apr', 'May', 'Jun', 'Jul', 'Aug', 'Sep', 'Oct', 'Nov', 'Dec'];
  const MONL = [
    'January',
    'February',
    'March',
    'April',
    'May',
    'June',
    'July',
    'August',
    'September',
    'October',
    'November',
    'December'
  ];
  const DOW = ['Mo', 'Tu', 'We', 'Th', 'Fr', 'Sa', 'Su'];

  // ---- date helpers ----
  // The API's days are the VIEWER'S local days (it buckets in the zone the page
  // sends as ?tz=), but they arrive as plain 'YYYY-MM-DD' labels and the only
  // thing done with them here is calendar arithmetic. That arithmetic is done
  // in UTC on purpose: parsing and formatting both go through UTC midnight, so
  // they round-trip exactly and a day never shifts under a west-of-Greenwich
  // browser the way `new Date('2024-03-01')` + `getDate()` would.
  const pad = (n) => String(n).padStart(2, '0');
  const iso = (t) => new Date(t).toISOString().slice(0, 10);
  const ts = (d) => Date.parse(d); // 'YYYY-MM-DD' parses as UTC midnight
  const wd = (d) => (new Date(ts(d)).getUTCDay() + 6) % 7; // Monday = 0
  const shift = (d, n) => iso(ts(d) + n * DAY);
  const weekStart = (d) => shift(d, -wd(d));
  const monthEnd = (y, m) => `${y}-${pad(m)}-${pad(new Date(Date.UTC(y, m, 0)).getUTCDate())}`;
  const endOfMonth = (d) => {
    const [y, m] = d.split('-').map(Number);
    return monthEnd(y, m);
  };
  const startOfMonth = (d) => d.slice(0, 8) + '01';
  const lo = (a, b) => (a < b ? a : b);
  const hi = (a, b) => (a > b ? a : b);
  const isDate = (d) => /^\d{4}-\d{2}-\d{2}$/.test(d || '');

  // ISO-8601 week number of the week `d` falls in.
  function weekNo(d) {
    const t = ts(weekStart(d)) + 3 * DAY; // the Thursday decides the year
    const y = new Date(t).getUTCFullYear();
    const jan4 = Date.UTC(y, 0, 4);
    const w1 = jan4 - ((new Date(jan4).getUTCDay() + 6) % 7) * DAY;
    return Math.floor((t - w1) / (7 * DAY)) + 1;
  }

  // ---- the derived level ------------------------------------------------
  // from/to exactly covering a calendar period IS that level; a week may be
  // clipped to its month (that is what the month grid's W-chips select), so a
  // clipped week still reads as a week rather than as a custom range.
  function levelOf(f, t) {
    if (!f && !t) return 'all';
    if (!isDate(f) || !isDate(t) || t < f) return 'custom';
    const [y, m] = f.split('-').map(Number);
    if (f === `${y}-01-01` && t === `${y}-12-31`) return 'year';
    if (f === startOfMonth(f) && t === monthEnd(y, m)) return 'month';
    if (f === t) return 'day';
    if (
      weekStart(f) === weekStart(t) &&
      (wd(f) === 0 || f === startOfMonth(f)) &&
      (wd(t) === 6 || t === endOfMonth(t))
    )
      return 'week';
    return 'custom';
  }

  $: level = levelOf(from, to);
  $: pinned = !!(from || to);

  // ---- counts ------------------------------------------------------------
  $: totals = buildTotals(days);
  function buildTotals(ds) {
    const day = new Map(),
      month = new Map(),
      year = new Map();
    let min = '',
      max = '';
    for (const d of ds) {
      let n = 0;
      for (const v of Object.values(d.by_source || {})) n += v;
      day.set(d.day, (day.get(d.day) || 0) + n);
      const mk = d.day.slice(0, 7),
        yk = d.day.slice(0, 4);
      month.set(mk, (month.get(mk) || 0) + n);
      year.set(yk, (year.get(yk) || 0) + n);
      if (!min || d.day < min) min = d.day;
      if (!max || d.day > max) max = d.day;
    }
    return { day, month, year, min, max };
  }

  function countDays(f, t, tot) {
    let n = 0;
    for (let d = f; d <= t; d = shift(d, 1)) n += tot.day.get(d) || 0;
    return n;
  }

  // 12,345 → "12.3k": the chips stay narrow enough for a 360 px screen; the
  // exact number is always in the chip's title.
  function short(n) {
    if (n < 1000) return String(n);
    if (n < 10000) return (n / 1000).toFixed(1).replace(/\.0$/, '') + 'k';
    if (n < 1000000) return Math.round(n / 1000) + 'k';
    return (n / 1000000).toFixed(1) + 'M';
  }

  const fmt = (n) => n.toLocaleString();

  // ---- breadcrumb --------------------------------------------------------
  $: trail = buildTrail(level, from, to, totals);
  function buildTrail(lv, f, t, tot) {
    const all = { label: 'All time', from: '', to: '', title: 'The whole archive' };
    if (lv === 'all') return [{ ...all, current: true }];
    if (lv === 'custom') {
      const label = f && t ? `${f} – ${t}` : f ? `from ${f}` : `until ${t}`;
      return [all, { label, from: f, to: t, title: `Custom range: ${label}`, current: true }];
    }
    const [y, m] = f.split('-').map(Number);
    const segs = [
      all,
      {
        label: String(y),
        from: `${y}-01-01`,
        to: `${y}-12-31`,
        title: `${y} · ${fmt(tot.year.get(String(y)) || 0)} events`
      }
    ];
    if (lv !== 'year') {
      const mk = f.slice(0, 7);
      segs.push({
        label: MON[m - 1],
        from: startOfMonth(f),
        to: monthEnd(y, m),
        title: `${MONL[m - 1]} ${y} · ${fmt(tot.month.get(mk) || 0)} events`
      });
      if (lv !== 'month') {
        const ws = weekStart(f);
        // the week as the month grid offers it: Mon–Sun clipped to the month
        const wf = lv === 'week' ? f : hi(ws, startOfMonth(f));
        const wt = lv === 'week' ? t : lo(shift(ws, 6), endOfMonth(f));
        segs.push({
          label: `W${weekNo(ws)}`,
          from: wf,
          to: wt,
          title: `Week ${weekNo(ws)}, ${wf} – ${wt} · ${fmt(countDays(wf, wt, tot))} events`
        });
        if (lv === 'day')
          segs.push({
            label: String(+f.slice(8)),
            from: f,
            to: f,
            title: `${f} · ${fmt(tot.day.get(f) || 0)} events`
          });
      }
    }
    segs[segs.length - 1].current = true;
    return segs;
  }

  // ---- chips for the NEXT level down ------------------------------------
  // { mode:'years'|'months'|'grid'|'none', items:[chip], rows:[{week, cells}] }
  $: view = buildChips(level, from, totals);

  const mkChip = (label, f, t, n, title) => ({
    label,
    from: f,
    to: t,
    count: n,
    empty: n === 0,
    title,
    f: -1
  });

  function buildChips(lv, f, tot) {
    let items = [];
    let rows = [];
    let mode = 'none';
    if (lv === 'all') {
      mode = 'years';
      if (tot.min) {
        for (let y = +tot.min.slice(0, 4); y <= +tot.max.slice(0, 4); y++) {
          const n = tot.year.get(String(y)) || 0;
          items.push(
            mkChip(
              String(y),
              `${y}-01-01`,
              `${y}-12-31`,
              n,
              `${y} · ${n ? fmt(n) + ' events' : 'no events'}`
            )
          );
        }
      }
    } else if (lv === 'year') {
      mode = 'months';
      const y = +f.slice(0, 4);
      for (let m = 1; m <= 12; m++) {
        const n = tot.month.get(`${y}-${pad(m)}`) || 0;
        items.push(
          mkChip(
            MON[m - 1],
            `${y}-${pad(m)}-01`,
            monthEnd(y, m),
            n,
            `${MONL[m - 1]} ${y} · ${n ? fmt(n) + ' events' : 'no events'}`
          )
        );
      }
    } else if (lv === 'month' || lv === 'week') {
      mode = 'grid';
      // a month shows every one of its weeks; a week shows just its own row
      const first = lv === 'month' ? startOfMonth(f) : from;
      const last = lv === 'month' ? endOfMonth(f) : to;
      for (let ws = weekStart(first); ws <= last; ws = shift(ws, 7)) {
        const wf = hi(ws, first);
        const wt = lo(shift(ws, 6), last);
        const wn = countDays(wf, wt, tot);
        const week = mkChip(
          `W${weekNo(ws)}`,
          wf,
          wt,
          wn,
          `Week ${weekNo(ws)}, ${wf} – ${wt} · ${wn ? fmt(wn) + ' events' : 'no events'}`
        );
        const cells = [];
        for (let i = 0; i < 7; i++) {
          const d = shift(ws, i);
          if (d < first || d > last) {
            cells.push(null);
            continue;
          } // keep the columns under Mo…Su
          const n = tot.day.get(d) || 0;
          cells.push(
            mkChip(
              String(+d.slice(8)),
              d,
              d,
              n,
              `${DOW[i]} ${d} · ${n ? fmt(n) + ' events' : 'no events'}`
            )
          );
        }
        rows.push({ key: ws, week, cells });
      }
    }
    // roving-tabindex order = DOM order, selectable chips only
    let i = 0;
    for (const c of items) if (!c.empty) c.f = i++;
    for (const r of rows) {
      if (!r.week.empty) r.week.f = i++;
      for (const c of r.cells) if (c && !c.empty) c.f = i++;
    }
    return { mode, items, rows, n: i };
  }

  const isCurrent = (c) => c.from === from && c.to === to;

  // ---- interaction -------------------------------------------------------
  function pick(f, t) {
    if (f === from && t === to) return;
    dispatch('range', { from: f, to: t });
  }

  // One tab stop per chip row; ← → Home End move focus inside it (a month row
  // can hold 31 buttons, which would otherwise be 31 tab stops).
  let boxEl;
  let focusIdx = 0;
  $: if (view) focusIdx = Math.min(focusIdx, Math.max(0, view.n - 1));

  function onKey(e) {
    if (!view.n) return;
    let next = focusIdx;
    if (e.key === 'ArrowRight' || e.key === 'ArrowDown') next = Math.min(view.n - 1, focusIdx + 1);
    else if (e.key === 'ArrowLeft' || e.key === 'ArrowUp') next = Math.max(0, focusIdx - 1);
    else if (e.key === 'Home') next = 0;
    else if (e.key === 'End') next = view.n - 1;
    else return;
    e.preventDefault();
    focusIdx = next;
    boxEl?.querySelector(`button[data-f="${next}"]`)?.focus();
  }
</script>

<section class="rb" aria-label="Date range navigator">
  <div class="trail">
    <span class="mono muted lbl">RANGE</span>
    {#each trail as s, i (s.label + i)}
      {#if i > 0}<span class="sep" aria-hidden="true">›</span>{/if}
      <button
        class="crumb"
        class:on={s.current}
        aria-current={s.current ? 'true' : undefined}
        title={s.title}
        on:click={() => pick(s.from, s.to)}>{s.label}</button
      >
    {/each}
    {#if pinned}
      <button
        class="reset"
        on:click={() => pick('', '')}
        title="Clear the date range (back to all time)"
        aria-label="Clear the date range">✕</button
      >
    {/if}
  </div>

  {#if !days.length}
    <div class="chips sk" aria-hidden="true">
      {#each [0, 1, 2, 3, 4, 5] as i (i)}<span class="skchip"></span>{/each}
    </div>
  {:else if view.mode === 'years' || view.mode === 'months'}
    <div
      class="chips"
      bind:this={boxEl}
      role="group"
      aria-label={view.mode === 'years' ? 'Years' : 'Months'}
    >
      {#each view.items as c (c.label)}
        <button
          class="chip"
          class:wide={view.mode === 'years'}
          class:empty={c.empty}
          class:on={isCurrent(c)}
          aria-current={isCurrent(c) ? 'true' : undefined}
          aria-disabled={c.empty ? 'true' : undefined}
          disabled={c.empty}
          data-f={c.empty ? undefined : c.f}
          tabindex={c.empty ? -1 : c.f === focusIdx ? 0 : -1}
          title={c.title}
          on:focus={() => (focusIdx = c.f)}
          on:keydown={onKey}
          on:click={() => pick(c.from, c.to)}
        >
          <span class="cl">{c.label}</span>
          <span class="cn mono">{c.empty ? '—' : short(c.count)}</span>
        </button>
      {/each}
    </div>
  {:else if view.mode === 'grid'}
    <div class="grid" bind:this={boxEl} role="group" aria-label="Weeks and days">
      <div class="hdr" aria-hidden="true">
        <span class="wh"></span>
        {#each DOW as d (d)}<span class="dh">{d}</span>{/each}
      </div>
      {#each view.rows as r (r.key)}
        <div class="wrow">
          <button
            class="chip wk"
            class:empty={r.week.empty}
            class:on={isCurrent(r.week)}
            aria-current={isCurrent(r.week) ? 'true' : undefined}
            aria-disabled={r.week.empty ? 'true' : undefined}
            disabled={r.week.empty}
            data-f={r.week.empty ? undefined : r.week.f}
            tabindex={r.week.empty ? -1 : r.week.f === focusIdx ? 0 : -1}
            title={r.week.title}
            on:focus={() => (focusIdx = r.week.f)}
            on:keydown={onKey}
            on:click={() => pick(r.week.from, r.week.to)}
          >
            <span class="cl">{r.week.label}</span>
            <span class="cn mono">{r.week.empty ? '—' : short(r.week.count)}</span>
          </button>
          {#each r.cells as c, i (i)}
            {#if c}
              <button
                class="chip dy"
                class:empty={c.empty}
                class:on={isCurrent(c)}
                aria-current={isCurrent(c) ? 'true' : undefined}
                aria-disabled={c.empty ? 'true' : undefined}
                disabled={c.empty}
                data-f={c.empty ? undefined : c.f}
                tabindex={c.empty ? -1 : c.f === focusIdx ? 0 : -1}
                title={c.title}
                on:focus={() => (focusIdx = c.f)}
                on:keydown={onKey}
                on:click={() => pick(c.from, c.to)}
              >
                <span class="cl">{c.label}</span>
                <span class="cn mono">{c.empty ? '' : short(c.count)}</span>
              </button>
            {:else}
              <span class="pad" aria-hidden="true"></span>
            {/if}
          {/each}
        </div>
      {/each}
    </div>
  {/if}
</section>

<style>
  .rb {
    margin-bottom: 12px;
  }
  .mono {
    font-family: ui-monospace, Menlo, monospace;
  }

  .trail {
    display: flex;
    align-items: center;
    flex-wrap: wrap;
    gap: 2px 4px;
    margin-bottom: 6px;
  }
  .lbl {
    font-size: 10.5px;
    letter-spacing: 0.08em;
    margin-right: 6px;
  }
  .sep {
    color: var(--muted);
    opacity: 0.6;
    font-size: 12px;
  }

  .crumb {
    border: 0;
    background: transparent;
    color: var(--muted);
    padding: 2px 7px;
    border-radius: 8px;
    font-size: 12.5px;
    line-height: 1.5;
  }
  .crumb:hover {
    background: var(--panel2);
    color: var(--ink);
    border-color: transparent;
  }
  .crumb.on {
    color: var(--ink);
    background: var(--chip);
    font-weight: 600;
  }

  .reset {
    border: 1px solid var(--border);
    background: transparent;
    color: var(--muted);
    border-radius: 999px;
    font-size: 11px;
    line-height: 1;
    padding: 4px 8px;
    margin-left: 6px;
  }
  .reset:hover {
    border-color: var(--danger);
    color: var(--danger);
  }

  .chips {
    display: flex;
    flex-wrap: wrap;
    gap: 4px;
    max-width: 620px;
  }

  .chip {
    display: flex;
    flex-direction: column;
    align-items: center;
    justify-content: center;
    gap: 1px;
    min-width: 34px;
    padding: 3px 7px;
    border: 1px solid var(--border);
    border-radius: 8px;
    background: var(--chip);
    color: var(--ink);
    line-height: 1.15;
  }
  .chip.wide {
    min-width: 52px;
  }
  .cl {
    font-size: 12px;
  }
  .cn {
    font-size: 9.5px;
    color: var(--muted);
    letter-spacing: 0.02em;
    font-variant-numeric: tabular-nums;
    min-height: 11px;
  }
  .chip:hover:not(.empty) {
    border-color: var(--accent);
  }
  .chip.on {
    background: var(--accent);
    border-color: var(--accent);
    color: #08111f;
  }
  .chip.on .cn {
    color: #08111f;
    opacity: 0.7;
  }
  .chip.empty {
    opacity: 0.32;
    background: transparent;
    cursor: default;
  }
  .chip:focus-visible {
    outline: 2px solid var(--accent);
    outline-offset: 1px;
  }

  /* month → week rows + day columns */
  .grid {
    display: flex;
    flex-direction: column;
    gap: 3px;
    min-width: 0;
    max-width: 560px;
  }
  .hdr,
  .wrow {
    display: grid;
    grid-template-columns: 3.1rem repeat(7, minmax(0, 1fr));
    gap: 3px;
  }
  .dh,
  .wh {
    font-size: 9.5px;
    color: var(--muted);
    text-align: center;
    letter-spacing: 0.06em;
    font-family: ui-monospace, Menlo, monospace;
  }
  .chip.wk {
    flex-direction: row;
    gap: 5px;
    justify-content: space-between;
    padding: 3px 6px;
    min-width: 0;
  }
  .chip.wk .cl {
    font-size: 11px;
    font-family: ui-monospace, Menlo, monospace;
  }
  .chip.dy {
    min-width: 0;
    padding: 3px 2px;
  }
  .pad {
    min-height: 10px;
  }

  @media (max-width: 460px) {
    .hdr,
    .wrow {
      grid-template-columns: 2.5rem repeat(7, minmax(0, 1fr));
      gap: 2px;
    }
    .chip {
      min-width: 0;
      padding: 3px 4px;
    }
    .chip.wide {
      min-width: 40px;
    }
    .chip.wk .cn {
      display: none;
    }
    .cl {
      font-size: 11px;
    }
  }

  /* first load: hold the height so the chart below does not jump */
  .sk .skchip {
    display: block;
    width: 46px;
    height: 32px;
    border-radius: 8px;
    border: 1px solid var(--border);
    background: color-mix(in srgb, var(--panel2) 60%, transparent);
  }
</style>
