<script>
  import { createEventDispatcher } from 'svelte';
  import { platform } from '#lib/platform.js';

  export let facets = { sources: [], categories: [], types: [] };
  export let filters; // {sources:Set, categories:Set, types:Set, q, from, to, hideConsumption}
  // Effective archive range: shown (muted) in the date inputs while no explicit
  // date filter is set; editing an input turns it into a real filter.
  export let archFrom = '';
  export let archTo = '';
  // View preference (bound): collapse runs of repetitive events in the timeline.
  export let groupRepeats = true;
  // Types the loaded page classified as consumption (muted counts below).
  export let consumptionTypes = new Set();

  const dispatch = createEventDispatcher();

  // Known high-volume types, so their counts are muted even before one of
  // them appears in the loaded page.
  const KNOWN_CONSUMPTION = new Set([
    'spotify.play',
    'spotify.search',
    'google.youtube.watch',
    'google.youtube.search',
    'tiktok.watch',
    'google.chrome.visit',
    'google.location.point'
  ]);

  function setDate(field, value) {
    filters[field] = value;
    filters = filters;
    dispatch('change');
  }

  function toggle(kind, value) {
    const set = filters[kind];
    set.has(value) ? set.delete(value) : set.add(value);
    // asking for consumption explicitly overrides "hide consumption"
    if (kind === 'categories' && value === 'consumption' && set.has(value))
      filters.hideConsumption = false;
    filters = filters; // trigger Svelte reactivity on Set mutation
    dispatch('change');
  }

  function toggleHide() {
    filters.hideConsumption = !filters.hideConsumption;
    if (filters.hideConsumption) filters.categories.delete('consumption');
    filters = filters;
    dispatch('change');
  }

  // Consumption counts are shown muted; a value that dwarfs the rest of its
  // facet (≥ 10× everything else combined) is flagged as high-volume. Nothing
  // is hidden — the counts stay exact.
  const isConsumption = (key, v) =>
    (key === 'categories' && v === 'consumption') ||
    (key === 'types' && (consumptionTypes.has(v) || KNOWN_CONSUMPTION.has(v)));
  function dominates(list, f) {
    if (!list || list.length < 2) return false;
    const rest = list.reduce((a, x) => a + (x === f ? 0 : x.count), 0);
    return rest > 0 && f.count >= 10 * rest;
  }
  const HV_NOUN = { sources: 'source', categories: 'category', types: 'type' };
</script>

<aside>
  <div class="card">
    <h3>Search</h3>
    <input placeholder="full-text…" bind:value={filters.q} on:input={() => dispatch('change')} />
    <div class="row" style="margin-top:8px">
      <div class="datecol">
        <label for="f-from">From</label>
        <input
          id="f-from"
          type="date"
          class:implicit={!filters.from}
          value={filters.from || archFrom}
          on:change={(e) => setDate('from', e.target.value)}
        />
      </div>
      <div class="datecol">
        <label for="f-to">To</label>
        <input
          id="f-to"
          type="date"
          class:implicit={!filters.to}
          value={filters.to || archTo}
          on:change={(e) => setDate('to', e.target.value)}
        />
      </div>
    </div>
  </div>

  <div class="card">
    <h3>View</h3>
    <label class="check">
      <input type="checkbox" bind:checked={groupRepeats} on:change={() => dispatch('pref')} />
      <span>Group repetitive events</span>
    </label>
    <div class="hint">Runs of plays, watches and other repeats fold into one card.</div>
    <button
      class="chip"
      class:on={filters.hideConsumption}
      aria-pressed={filters.hideConsumption}
      on:click={toggleHide}
      title="Leave out consumption events (plays, watches, visits) from the timeline and the chart"
    >
      {filters.hideConsumption ? '✓ ' : ''}Hide consumption
    </button>
  </div>

  {#each [['categories', 'Category'], ['sources', 'Source'], ['types', 'Type']] as [key, title]}
    <div class="card">
      <h3>{title}</h3>
      {#if facets[key] && facets[key].length}
        <div class="facets">
          {#each facets[key] as f}
            {@const hv = dominates(facets[key], f)}
            <div
              class="facet"
              class:active={filters[key].has(f.value)}
              class:hv
              title={hv ? `${f.value} — high-volume ${HV_NOUN[key]}` : f.value}
              on:click={() => toggle(key, f.value)}
              on:keydown={(e) => e.key === 'Enter' && toggle(key, f.value)}
              role="button"
              tabindex="0"
            >
              <span class="v">
                {#if key === 'sources'}<i class="dot" style="background:{platform(f.value).color}"
                  ></i>{/if}{f.value}
              </span><span class="c" class:cons={isConsumption(key, f.value) || hv}
                >{f.count.toLocaleString()}{#if hv}<span
                    class="hvmark"
                    aria-label="high-volume {HV_NOUN[key]}">▲</span
                  >{/if}</span
              >
            </div>
          {/each}
        </div>
      {:else}<div class="muted" style="font-size:12px">—</div>{/if}
    </div>
  {/each}

  <button style="width:100%" on:click={() => dispatch('clear')}>Clear filters</button>
</aside>

<style>
  aside {
    padding: 16px;
    min-width: 0;
  }
  h3 {
    font-size: 13px;
    text-transform: uppercase;
    letter-spacing: 0.04em;
    color: var(--muted);
    margin: 0 0 8px;
  }
  label {
    display: block;
    font-size: 12px;
    color: var(--muted);
    margin: 8px 0 4px;
  }
  .datecol {
    flex: 1;
    min-width: 0;
  }
  input.implicit {
    color: var(--muted);
  }
  .check {
    display: flex;
    align-items: center;
    gap: 8px;
    margin: 0;
    font-size: 13.5px;
    color: var(--ink);
    cursor: pointer;
  }
  .check input {
    width: auto;
    margin: 0;
    accent-color: var(--accent);
  }
  .hint {
    font-size: 11.5px;
    color: var(--muted);
    margin: 4px 0 10px 24px;
  }
  .chip {
    font-size: 12px;
    padding: 5px 11px;
    border-radius: 999px;
  }
  .chip.on {
    background: var(--accent);
    border-color: var(--accent);
    color: #08111f;
    font-weight: 600;
  }
  .facets {
    max-height: 264px;
    overflow-y: auto;
    overflow-x: hidden;
    margin: 0 -4px;
    padding: 0 4px;
  }
  .facet {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: 8px;
    padding: 5px 8px;
    border-radius: 8px;
    cursor: pointer;
    min-width: 0;
  }
  .facet .v {
    flex: 1;
    min-width: 0;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  .facet:hover {
    background: var(--panel2);
  }
  .facet.active {
    background: var(--accent);
    color: #08111f;
  }
  .facet .c {
    font-size: 12px;
    opacity: 0.8;
    flex-shrink: 0;
    font-variant-numeric: tabular-nums;
  }
  .facet .c.cons {
    opacity: 0.5;
  }
  .facet.active .c.cons {
    opacity: 0.7;
  }
  .hvmark {
    font-size: 8px;
    margin-left: 4px;
    vertical-align: 1px;
  }
  .dot {
    display: inline-block;
    width: 8px;
    height: 8px;
    border-radius: 50%;
    margin-right: 7px;
    vertical-align: baseline;
  }
</style>
