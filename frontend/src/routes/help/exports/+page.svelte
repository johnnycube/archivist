<script>
  // Public page: readable without a session (it only asks /api/auth/me to decide
  // between the app header and a plain "Log in" bar).
  import { onMount, tick } from 'svelte';
  import { api } from '#lib/api.js';
  import { platform } from '#lib/platform.js';
  import { EXPORT_GUIDES, GUIDES_VERIFIED, UPLOAD_CAP } from '#lib/exportGuides.js';
  import Header from '#lib/components/Header.svelte';
  import RichText from '#lib/components/RichText.svelte';

  let user = null;
  let open = {}; // id → true

  async function showHash() {
    const id = decodeURIComponent(location.hash.slice(1));
    if (!id || !EXPORT_GUIDES.some((g) => g.id === id)) return;
    open[id] = true;
    await tick();
    document.getElementById(id)?.scrollIntoView({ block: 'start' });
  }

  onMount(() => {
    showHash();
    api
      .me()
      .then((u) => (user = u))
      .catch(() => (user = null));
  });

  async function logout() {
    await api.logout();
    user = null;
  }

  const setAll = (v) => (open = Object.fromEntries(EXPORT_GUIDES.map((g) => [g.id, v])));
</script>

<svelte:head><title>How to get your data — Archivist</title></svelte:head>
<svelte:window on:hashchange={showHash} />

{#if user}
  <Header {user} on:logout={logout} />
{:else}
  <header class="top">
    <a class="kicker brand" href="/">
      <img src="/favicon.svg" alt="" width="22" height="22" />Archivist
    </a>
    <span style="flex:1"></span>
    <a href="/">Log in</a>
  </header>
{/if}

<main>
  <h1>How to get your data</h1>
  <p class="lead muted">
    Every service below must hand you a copy of your data on request. Ask for it as described,
    <strong>pick the options in the yellow boxes</strong> — a wrong format usually means waiting
    days for a second export — then upload the ZIP on the <a href="/import">Import</a> page.
  </p>

  <div class="card general">
    <div class="kicker">Uploading, for every service</div>
    <ul>
      <li>
        Upload the ZIP <strong>unchanged</strong> — do not unpack or re-pack it (Telegram is the exception:
        you zip its folder yourself).
      </li>
      <li>
        <strong>Split exports</strong> (Google Takeout, Facebook, Instagram, Snapchat, X): select
        <em>all</em> parts in the one file dialog; they are imported as one archive.
      </li>
      <li>
        One upload may be at most <strong>{UPLOAD_CAP}</strong> by default, all parts together (the
        server operator can raise <code>MAX_UPLOAD_MB</code>).
      </li>
      <li>
        Whatever Archivist does not recognise is kept as <code>&lt;source&gt;.unknown</code>;
        re-importing never duplicates events.
      </li>
    </ul>
  </div>

  <div class="tools">
    <span class="muted small"
      >Steps checked against the vendors' help pages as of {GUIDES_VERIFIED}. Menus change — when in
      doubt, follow the linked official article.</span
    >
    <span style="flex:1"></span>
    <button class="small" on:click={() => setAll(true)}>Expand all</button>
    <button class="small" on:click={() => setAll(false)}>Collapse all</button>
  </div>

  {#each EXPORT_GUIDES as g (g.id)}
    {@const p = platform(g.id)}
    <details id={g.id} class="card guide" style="--src:{p.color}" bind:open={open[g.id]}>
      <summary>
        <span class="glyph" aria-hidden="true">{p.glyph}</span>
        <span class="name">{g.name}</span>
        <span class="sum muted">{g.format}</span>
        <span class="wait badge">{g.wait}</span>
      </summary>

      <div class="body">
        <div class="links">
          {#if g.requestUrl}
            <a class="ext primary" href={g.requestUrl} target="_blank" rel="noopener noreferrer"
              >Open the request page ↗</a
            >
          {/if}
          <a class="ext" href={g.helpUrl} target="_blank" rel="noopener noreferrer"
            >Official help article ↗</a
          >
        </div>

        <h3>Where to request it</h3>
        <p class="where"><RichText text={g.where} /></p>
        {#each g.steps as block}
          {#if block.title}<h4>{block.title}</h4>{/if}
          <ol>
            {#each block.items as it}<li><RichText text={it} /></li>{/each}
          </ol>
        {/each}

        <div class="callout" role="note">
          <div class="kicker warn">Choose these options</div>
          <ul>
            {#each g.choose as c}<li><RichText text={c} /></li>{/each}
          </ul>
        </div>

        <h3>How long it takes, how it arrives</h3>
        <p><RichText text={g.delivery} /></p>

        <h3>Upload to Archivist</h3>
        <ul>
          {#each g.upload as u}<li><RichText text={u} /></li>{/each}
        </ul>

        <h3>What you get</h3>
        <p class="types">
          {#each g.events as e}<span class="type chip">{g.id}.{e}</span>{' '}{/each}
        </p>
      </div>
    </details>
  {/each}
</main>

<style>
  .top {
    display: flex;
    align-items: center;
    gap: 14px;
    padding: 12px 18px;
    border-bottom: 1px solid var(--border);
    position: sticky;
    top: 0;
    background: var(--bg);
    z-index: 5;
  }
  .brand {
    display: flex;
    align-items: center;
    gap: 8px;
    text-decoration: none;
  }
  main {
    max-width: 860px;
    margin: 0 auto;
    padding: 18px 16px 8px;
  }
  h1 {
    font-size: 24px;
    letter-spacing: -0.02em;
    margin: 6px 0 8px;
  }
  .lead {
    margin: 0 0 16px;
    max-width: 70ch;
  }
  .lead strong {
    color: var(--warn);
    font-weight: 600;
  }
  .general ul {
    margin: 6px 0 0;
    padding-left: 18px;
  }
  .general li,
  .body li {
    margin: 4px 0;
  }
  code {
    font:
      12px/1.4 ui-monospace,
      Menlo,
      monospace;
    background: var(--chip);
    border: 1px solid var(--border);
    border-radius: 5px;
    padding: 0 4px;
  }
  .tools {
    display: flex;
    gap: 8px;
    align-items: center;
    flex-wrap: wrap;
    margin: 14px 0 10px;
  }
  .small {
    font-size: 12px;
  }
  button.small {
    padding: 3px 9px;
  }

  .guide {
    padding: 0;
    border-left: 3px solid var(--src);
    scroll-margin-top: 78px;
  }
  summary {
    display: flex;
    align-items: center;
    gap: 10px;
    padding: 12px 16px;
    cursor: pointer;
    list-style: none;
    flex-wrap: wrap;
  }
  summary::-webkit-details-marker {
    display: none;
  }
  summary::after {
    content: '▸';
    color: var(--muted);
    margin-left: 2px;
  }
  details[open] > summary::after {
    content: '▾';
  }
  summary:hover .name {
    color: var(--src);
  }
  summary:focus-visible {
    outline: 2px solid var(--accent);
    outline-offset: -2px;
    border-radius: 12px;
  }
  .glyph {
    flex-shrink: 0;
    width: 24px;
    height: 24px;
    border-radius: 50%;
    text-align: center;
    font:
      700 12px/22px ui-monospace,
      Menlo,
      monospace;
    color: var(--src);
    background: color-mix(in srgb, var(--src) 16%, var(--panel2));
    border: 1px solid color-mix(in srgb, var(--src) 45%, transparent);
  }
  .name {
    font-weight: 600;
    min-width: 120px;
  }
  .sum {
    flex: 1 1 220px;
    min-width: 0;
    font-size: 13px;
  }
  .wait {
    color: var(--muted);
    white-space: normal;
  }

  .body {
    padding: 2px 16px 14px;
    border-top: 1px solid var(--border);
    overflow-wrap: anywhere;
  }
  .links {
    display: flex;
    gap: 8px;
    flex-wrap: wrap;
    margin: 12px 0 2px;
  }
  .ext {
    font-size: 13px;
    text-decoration: none;
    padding: 5px 11px;
    border-radius: 9px;
    border: 1px solid var(--border);
    background: var(--panel2);
    color: var(--ink);
  }
  .ext:hover {
    border-color: var(--accent);
  }
  .ext.primary {
    border-color: color-mix(in srgb, var(--src) 60%, var(--border));
  }
  h3 {
    font-size: 12px;
    text-transform: uppercase;
    letter-spacing: 0.04em;
    color: var(--muted);
    margin: 16px 0 4px;
  }
  h4 {
    font-size: 13px;
    margin: 10px 0 2px;
  }
  p {
    margin: 4px 0;
  }
  .where {
    font-weight: 600;
  }
  ol,
  ul {
    margin: 4px 0;
    padding-left: 20px;
  }
  .callout {
    margin: 14px 0 4px;
    padding: 10px 14px;
    border-radius: 10px;
    border: 1px solid color-mix(in srgb, var(--warn) 45%, var(--border));
    background: color-mix(in srgb, var(--warn) 8%, var(--panel));
  }
  .kicker.warn {
    color: var(--warn);
  }
  .types {
    line-height: 2;
  }
  .chip {
    background: var(--chip);
    border: 1px solid var(--border);
    border-radius: 999px;
    padding: 2px 8px;
    white-space: nowrap;
  }

  @media (max-width: 520px) {
    main {
      padding: 14px 12px 8px;
    }
    .sum {
      flex-basis: 100%;
      order: 3;
    }
    .chip {
      white-space: normal;
    }
  }
</style>
