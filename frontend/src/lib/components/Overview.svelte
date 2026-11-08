<script>
  import { createEventDispatcher } from 'svelte';
  import { platform, chartColor, CHART_STACK } from '#lib/platform.js';

  const dispatch = createEventDispatcher();

  // The whole-archive day histogram under the non-date filters, fetched once by
  // the page and shared with the range bar: [{day:'YYYY-MM-DD', by_source:{}}].
  // The days are the viewer's local days (the page sends its zone as ?tz=);
  // here they are opaque labels, and every date computation below parses and
  // formats them through UTC midnight so they round-trip exactly whatever the
  // browser's own offset is.
  export let days = [];
  export let from = ''; // date filters (YYYY-MM-DD): they define the chart's fixed range
  export let to = '';

  const DAY = 86400000;
  const MON = ['JAN', 'FEB', 'MAR', 'APR', 'MAY', 'JUN', 'JUL', 'AUG', 'SEP', 'OCT', 'NOV', 'DEC'];

  let scaleMode = 'auto'; // auto | linear | log  (see useLog below)

  // ---- fixed range: the date filters pin the domain; no filter = whole archive ----
  $: domFrom = from || (days.length ? days[0].day : '');
  $: domTo = to || (days.length ? days[days.length - 1].day : '');
  $: domainDays = days.filter((d) => d.day >= domFrom && d.day <= domTo);
  // let the page mirror the effective range (e.g. in the sidebar date inputs)
  $: if (domFrom && domTo) dispatch('domain', { from: domFrom, to: domTo });
  $: spanDays = domFrom && domTo ? (Date.parse(domTo) - Date.parse(domFrom)) / DAY : 0;
  // Bucket size follows the pinned span automatically. There is no manual
  // override any more: the range bar above is the one control that decides what
  // the chart covers, and re-bucketing a range you had not narrowed only looked
  // like a broken drill-down.
  $: gran = spanDays > 1100 ? 'year' : spanDays > 200 ? 'month' : spanDays > 45 ? 'week' : 'day';

  // ---- bucketing (granularity changes re-slice the same fixed range) ----
  function weekStart(day) {
    const t = Date.parse(day);
    const wd = (new Date(t).getUTCDay() + 6) % 7; // Monday = 0
    return new Date(t - wd * DAY).toISOString().slice(0, 10);
  }

  const keyOf = (day, g) =>
    g === 'year'
      ? day.slice(0, 4)
      : g === 'month'
        ? day.slice(0, 7)
        : g === 'week'
          ? weekStart(day)
          : day;

  function nextKey(key, g) {
    if (g === 'year') return String(+key + 1);
    if (g === 'month') {
      const [y, m] = key.split('-').map(Number);
      return m === 12 ? `${y + 1}-01` : `${y}-${String(m + 1).padStart(2, '0')}`;
    }
    return new Date(Date.parse(key) + (g === 'week' ? 7 : 1) * DAY).toISOString().slice(0, 10);
  }

  const bucketStart = (key, g) =>
    g === 'year' ? key + '-01-01' : g === 'month' ? key + '-01' : key;
  function bucketEnd(key, g) {
    if (g === 'year') return key + '-12-31';
    if (g === 'month') {
      const [y, m] = key.split('-').map(Number);
      return `${key}-${String(new Date(Date.UTC(y, m, 0)).getUTCDate()).padStart(2, '0')}`;
    }
    if (g === 'week') return new Date(Date.parse(key) + 6 * DAY).toISOString().slice(0, 10);
    return key;
  }

  function labelOf(key, g) {
    if (g === 'year') return key;
    if (g === 'month') {
      const [y, m] = key.split('-');
      return `${MON[+m - 1]} ${y.slice(2)}`;
    }
    const [, m, d] = key.split('-');
    return `${MON[+m - 1]} ${d}`;
  }

  $: buckets = buildBuckets(domainDays, gran, domFrom, domTo);
  function buildBuckets(ds, g, lo, hi) {
    if (!lo || !hi) return [];
    const agg = new Map();
    for (const d of ds) {
      const k = keyOf(d.day, g);
      const cur = agg.get(k) || {};
      for (const [src, n] of Object.entries(d.by_source)) cur[src] = (cur[src] || 0) + n;
      agg.set(k, cur);
    }
    // cover the whole pinned range, empty buckets included
    const out = [];
    const last = keyOf(hi, g);
    let k = keyOf(lo, g);
    for (let guard = 0; guard < 4000; guard++) {
      const by = agg.get(k) || {};
      out.push({ key: k, by, total: Object.values(by).reduce((a, b) => a + b, 0) });
      if (k === last) break;
      k = nextKey(k, g);
    }
    return out;
  }

  $: sources = [
    ...CHART_STACK.filter((s) => buckets.some((b) => b.by[s])),
    ...[...new Set(buckets.flatMap((b) => Object.keys(b.by)))]
      .filter((s) => !CHART_STACK.includes(s))
      .sort()
  ];
  $: maxTotal = Math.max(1, ...buckets.map((b) => b.total));

  // ---- scale: linear stack vs. log small multiples ----
  // High-volume "consumption" sources (Spotify plays, YouTube watches, …) can
  // outnumber everything else by orders of magnitude; in a linear stack they
  // flatten every other source to a sliver. AUTO switches to the log form when
  //   (a) the busiest bucket holds ≥ 20× the median non-empty bucket, or
  //   (b) one source is ≥ 90 % of all events in range (and it is not alone).
  // The user can always override, and the active scale is shown as a badge.
  $: perSource = Object.fromEntries(
    sources.map((s) => [s, buckets.reduce((a, b) => a + (b.by[s] || 0), 0)])
  );
  $: grand = Object.values(perSource).reduce((a, b) => a + b, 0);
  $: topShare = grand ? Math.max(0, ...Object.values(perSource)) / grand : 0;
  $: nonZero = buckets
    .map((b) => b.total)
    .filter(Boolean)
    .sort((a, b) => a - b);
  $: medianTotal = nonZero.length ? nonZero[Math.floor(nonZero.length / 2)] : 0;
  $: autoLog =
    nonZero.length >= 3 &&
    ((medianTotal > 0 && maxTotal >= 20 * medianTotal) || (sources.length > 1 && topShare >= 0.9));
  $: useLog = scaleMode === 'log' || (scaleMode === 'auto' && autoLog);
  $: dominant = sources.length > 1 ? sources.find((s) => perSource[s] / grand >= 0.9) : null;

  // Symlog-style transform: log1p keeps 0 at 0 and 1 → 0.69, so a bucket with a
  // single event is still visibly drawn while 5,000 plays only reach ~8.5.
  const slog = Math.log1p;

  // FORM IN LOG MODE — small multiples, not a log-stacked bar.
  // A stacked bar encodes part-to-whole: segment heights are additive and the
  // bar top is the total. Under a log transform neither holds (log(a+b) ≠
  // log a + log b), so a "log stacked bar" reads as a lie about shares. The
  // dataviz guidance for series of very different scale is small multiples on
  // ONE shared axis (never a dual axis, never occluding overlays), and for ≥ 5
  // series "legend or small multiples". So log mode draws one mini row per
  // source — a strip of single-series bar charts on a shared time axis and a
  // shared symlog y-scale (so rows stay comparable and small sources remain
  // visible next to the dominant one), each row named by a text label + swatch
  // rather than by colour adjacency, which the palette was only validated for
  // in stack order. Tooltips and the row peak labels carry the TRUE counts.
  $: maxSingle = Math.max(1, ...buckets.flatMap((b) => Object.values(b.by)));
  $: rowH = useLog ? Math.max(14, Math.min(28, Math.floor(170 / Math.max(1, sources.length)))) : 0;
  const ROWGAP = 3;
  $: rowPeak = Object.fromEntries(
    sources.map((s) => [s, Math.max(0, ...buckets.map((b) => b.by[s] || 0))])
  );

  // ---- geometry ----
  let plotW = 600;
  $: H = useLog ? sources.length * (rowH + ROWGAP) : 92; // plot height
  $: n = buckets.length;
  $: bw = n ? plotW / n : 0;
  $: barW = Math.max(1, Math.min(bw - 2, 48));

  // linear: stacked segments, additive heights
  function segs(b, srcs, max, h) {
    let y = h;
    const out = [];
    for (const s of srcs) {
      const v = b.by[s] || 0;
      if (!v) continue;
      const sh = (v / max) * (h - 4);
      y -= sh;
      out.push({ s, y, h: sh, v });
    }
    return out;
  }

  // log: one rect per (source row, bucket); never stacked
  function rows(b, srcs, max, rh) {
    const out = [];
    srcs.forEach((s, r) => {
      const v = b.by[s] || 0;
      if (!v) return;
      const sh = Math.max(1, (slog(v) / slog(max)) * (rh - 2));
      out.push({ s, y: r * (rh + ROWGAP) + rh - sh, h: sh, v });
    });
    return out;
  }

  $: tickEvery = Math.max(1, Math.ceil(n / 6));

  // ---- brush + hover ----
  // A drag (or a click on one bucket) SETS the pinned range, exactly like the
  // range bar's chips and the sidebar's date inputs — one visible truth for the
  // chart, the trail, the sidebar inputs, the list and the URL.
  let plotEl;
  let dragA = -1,
    dragB = -1,
    dragging = false,
    dragXA = 0,
    dragXB = 0;
  let hoverIdx = -1,
    hoverX = 0;

  const idxAt = (clientX) => {
    const r = plotEl.getBoundingClientRect();
    return Math.max(0, Math.min(n - 1, Math.floor(((clientX - r.left) / r.width) * n)));
  };
  const xAt = (clientX) => {
    const r = plotEl.getBoundingClientRect();
    return Math.max(0, Math.min(r.width, clientX - r.left));
  };

  // Map a plot x-position to a calendar day inside the pinned range.
  function dayAtX(x) {
    const t0 = Date.parse(bucketStart(buckets[0].key, gran));
    const t1 = Date.parse(bucketEnd(buckets[n - 1].key, gran)) + DAY - 1;
    const t = t0 + (x / Math.max(1, plotW)) * (t1 - t0);
    return new Date(Math.max(t0, Math.min(t1, t))).toISOString().slice(0, 10);
  }

  function down(e) {
    if (!n) return;
    dragging = true;
    dragA = dragB = idxAt(e.clientX);
    dragXA = dragXB = xAt(e.clientX);
    plotEl.setPointerCapture(e.pointerId);
  }
  function move(e) {
    if (!n) return;
    const r = plotEl.getBoundingClientRect();
    hoverX = e.clientX - r.left;
    hoverIdx = idxAt(e.clientX);
    if (dragging) {
      dragB = hoverIdx;
      dragXB = xAt(e.clientX);
    }
  }
  function up() {
    if (!dragging) return;
    dragging = false;
    if (Math.abs(dragXB - dragXA) < 4) {
      // plain click: zoom into the bucket under the cursor
      dispatch('range', {
        from: bucketStart(buckets[dragA].key, gran),
        to: bucketEnd(buckets[dragA].key, gran)
      });
    } else {
      const [xa, xb] = [Math.min(dragXA, dragXB), Math.max(dragXA, dragXB)];
      dispatch('range', { from: dayAtX(xa), to: dayAtX(xb) });
    }
    dragA = dragB = -1;
  }
  const leave = () => {
    hoverIdx = -1;
  };

  $: brushLo = dragging ? Math.min(dragA, dragB) : -1;
  $: brushHi = dragging ? Math.max(dragA, dragB) : -1;

  $: hovered = hoverIdx >= 0 ? buckets[hoverIdx] : null;
  $: tipX = Math.max(70, Math.min(plotW - 70, hoverX));

  const scaleTitle = {
    auto: 'Auto: log scale when one bucket or source dwarfs the rest (≥ 20× the median bucket, or ≥ 90 % of all events)',
    linear: 'Linear stacked bars — segments add up to the bucket total',
    log: 'Log scale — one row per source on a shared log1p axis; true counts in the tooltip'
  };
</script>

{#if buckets.length}
  <section class="ov" aria-label="Archive overview: events over time by source">
    <div class="head">
      <span class="mono muted"
        >ARCHIVE · {domFrom} → {domTo} · peak {maxTotal.toLocaleString()} / {gran}</span
      >
      {#if useLog}
        <span
          class="lgbadge mono"
          title={scaleMode === 'auto'
            ? `Switched to a log scale automatically: ${dominant ? platform(dominant).label + ' is ≥ 90 % of all events' : 'the busiest bucket is ≥ 20× the median bucket'}. Rows are not stacked; heights are log1p(count) on a shared axis.`
            : 'Log scale: one row per source, heights are log1p(count) on a shared axis.'}
        >
          LOG SCALE{scaleMode === 'auto' ? ' · auto' : ''}
        </span>
      {/if}
      <span class="grow"></span>
      <div class="seg" role="group" aria-label="Value scale">
        {#each [['auto', 'AUTO'], ['linear', 'LIN'], ['log', 'LOG']] as [m, lbl] (m)}
          <button
            class:on={scaleMode === m}
            on:click={() => (scaleMode = m)}
            title={scaleTitle[m]}
            aria-pressed={scaleMode === m}>{lbl}</button
          >
        {/each}
      </div>
    </div>

    <div class="plotwrap" class:log={useLog}>
      {#if useLog}
        <div class="rowlabels" style="padding-bottom:18px" aria-hidden="true">
          {#each sources as s (s)}
            <div
              class="rowlabel"
              style="height:{rowH + ROWGAP}px"
              title="{platform(s).label}: {perSource[
                s
              ].toLocaleString()} events in range, peak {rowPeak[s].toLocaleString()} / {gran}"
            >
              <i style="background:{chartColor(s)}"></i>
              <span class="rl-name">{platform(s).label}</span>
              <span class="rl-peak mono">{rowPeak[s].toLocaleString()}</span>
            </div>
          {/each}
        </div>
      {/if}

      <div class="plotarea" bind:clientWidth={plotW}>
        <svg
          bind:this={plotEl}
          width={plotW}
          height={H + 18}
          on:pointerdown={down}
          on:pointermove={move}
          on:pointerup={up}
          on:pointerleave={leave}
          role="img"
          aria-label={useLog
            ? `Small multiples: one bar chart row per source (${sources.map((s) => platform(s).label).join(', ')}) of archived events per ${gran}, log scale, shared time axis. Drag or click to narrow the date range.`
            : `Stacked bar chart of archived events per ${gran}, colored by source. Drag or click to narrow the date range.`}
        >
          {#if useLog}
            {#each sources as s, r (s)}
              <line
                x1="0"
                y1={r * (rowH + ROWGAP) + rowH + 0.5}
                x2={plotW}
                y2={r * (rowH + ROWGAP) + rowH + 0.5}
                class="baseline"
              />
            {/each}
            {#if hoverIdx >= 0 && !dragging}
              <rect x={hoverIdx * bw} y="0" width={Math.max(1, bw)} height={H} class="col-hot" />
            {/if}
          {:else}
            <line x1="0" y1={H + 0.5} x2={plotW} y2={H + 0.5} class="baseline" />
          {/if}
          {#each buckets as b, i (b.key)}
            <g
              class="bar"
              class:dim={dragging && (i < brushLo || i > brushHi)}
              class:hot={i === hoverIdx && !dragging && !useLog}
            >
              {#if useLog}
                {#each rows(b, sources, maxSingle, rowH) as sg (sg.s)}
                  <rect
                    x={i * bw + (bw - barW) / 2}
                    y={sg.y}
                    width={barW}
                    height={sg.h}
                    fill={chartColor(sg.s)}
                    class="seg-rect"
                  />
                {/each}
              {:else}
                {#each segs(b, sources, maxTotal, H) as sg (sg.s)}
                  <rect
                    x={i * bw + (bw - barW) / 2}
                    y={sg.y}
                    width={barW}
                    height={sg.h}
                    fill={chartColor(sg.s)}
                    class="seg-rect"
                  />
                {/each}
              {/if}
            </g>
            {#if i % tickEvery === 0}
              <text x={i === 0 ? 2 : i * bw + bw / 2} y={H + 13} class="tick" class:first={i === 0}
                >{labelOf(b.key, gran)}</text
              >
            {/if}
          {/each}
          {#if dragging}
            <rect
              x={Math.min(dragXA, dragXB)}
              y="0"
              width={Math.max(1, Math.abs(dragXB - dragXA))}
              height={H}
              class="brush"
            />
          {/if}
        </svg>

        {#if hovered && !dragging}
          <div class="tip" style="left:{tipX}px">
            <div class="mono tiplabel">
              {labelOf(hovered.key, gran)} · {hovered.total.toLocaleString()} events
            </div>
            {#each sources as s (s)}
              {#if hovered.by[s]}
                <div class="tiprow">
                  <i style="background:{chartColor(s)}"></i>{platform(s).label}<span class="grow"
                  ></span>{hovered.by[s].toLocaleString()}
                </div>
              {/if}
            {/each}
          </div>
        {/if}
      </div>
    </div>

    <div class="legend">
      {#each sources as s (s)}
        <span class="lg"><i style="background:{chartColor(s)}"></i>{platform(s).label}</span>
      {/each}
      <span class="grow"></span>
      <span class="mono muted hint"
        >drag or click to narrow the range{useLog ? ' · rows share a log axis' : ''}</span
      >
    </div>
  </section>
{/if}

<style>
  .ov {
    margin-bottom: 16px;
  }
  .head {
    display: flex;
    align-items: center;
    flex-wrap: wrap;
    gap: 6px 10px;
    margin-bottom: 6px;
  }
  .mono {
    font-family: ui-monospace, Menlo, monospace;
    font-size: 10.5px;
    letter-spacing: 0.08em;
  }
  .grow {
    flex: 1;
  }

  .seg {
    display: flex;
    border: 1px solid var(--border);
    border-radius: 8px;
    overflow: hidden;
  }
  .seg button {
    border: 0;
    border-radius: 0;
    background: transparent;
    padding: 3px 9px;
    font-size: 11px;
    font-family: ui-monospace, Menlo, monospace;
    color: var(--muted);
  }
  .seg button.on {
    background: var(--panel2);
    color: var(--ink);
  }

  .lgbadge {
    color: var(--warn);
    border: 1px solid color-mix(in srgb, var(--warn) 45%, transparent);
    background: color-mix(in srgb, var(--warn) 10%, transparent);
    border-radius: 999px;
    padding: 1px 8px;
    cursor: help;
  }

  .plotwrap {
    position: relative;
    display: flex;
    align-items: stretch;
  }
  .plotarea {
    position: relative;
    flex: 1;
    min-width: 0;
  }
  .rowlabels {
    flex: 0 0 118px;
    display: flex;
    flex-direction: column;
    padding-right: 8px;
  }
  .rowlabel {
    display: flex;
    align-items: center;
    gap: 6px;
    font-size: 11px;
    color: var(--muted);
    min-width: 0;
  }
  .rowlabel i {
    width: 8px;
    height: 8px;
    border-radius: 2px;
    flex-shrink: 0;
  }
  .rl-name {
    flex: 1;
    min-width: 0;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
    color: var(--ink);
  }
  .rl-peak {
    font-variant-numeric: tabular-nums;
    opacity: 0.85;
  }

  svg {
    display: block;
    cursor: crosshair;
    touch-action: none;
    user-select: none;
  }
  .baseline {
    stroke: var(--border);
  }
  .seg-rect {
    stroke: var(--bg);
    stroke-width: 1;
  }
  .bar {
    transition: opacity 0.12s ease;
  }
  .bar.dim {
    opacity: 0.3;
  }
  .bar.hot {
    opacity: 0.85;
  }
  .col-hot {
    fill: color-mix(in srgb, var(--ink) 7%, transparent);
  }
  .tick {
    fill: var(--muted);
    font-family: ui-monospace, Menlo, monospace;
    font-size: 9.5px;
    text-anchor: middle;
    letter-spacing: 0.04em;
  }
  .tick.first {
    text-anchor: start;
  }
  .brush {
    fill: rgba(110, 168, 254, 0.14);
    stroke: var(--accent);
    stroke-width: 1;
  }

  .tip {
    position: absolute;
    top: -6px;
    transform: translate(-50%, -100%);
    background: var(--panel);
    border: 1px solid var(--border);
    border-radius: 10px;
    padding: 8px 10px;
    min-width: 150px;
    box-shadow: 0 10px 26px rgba(0, 0, 0, 0.45);
    pointer-events: none;
    z-index: 8;
  }
  .tiplabel {
    color: var(--ink);
    margin-bottom: 5px;
  }
  .tiprow {
    display: flex;
    align-items: center;
    gap: 7px;
    font-size: 12px;
    color: var(--muted);
    padding: 1px 0;
    font-variant-numeric: tabular-nums;
  }
  .tiprow i,
  .lg i {
    width: 8px;
    height: 8px;
    border-radius: 2px;
    flex-shrink: 0;
    display: inline-block;
  }

  .legend {
    display: flex;
    align-items: center;
    gap: 14px;
    flex-wrap: wrap;
    margin-top: 6px;
  }
  .lg {
    display: inline-flex;
    align-items: center;
    gap: 6px;
    font-size: 11.5px;
    color: var(--muted);
  }
  .hint {
    opacity: 0.7;
  }

  @media (max-width: 900px) {
    .rowlabels {
      flex-basis: 92px;
    }
    .rl-peak {
      display: none;
    }
  }
  @media (prefers-reduced-motion: reduce) {
    .bar {
      transition: none;
    }
  }
</style>
