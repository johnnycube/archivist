<script>
  import { createEventDispatcher } from 'svelte';
  import { api } from '#lib/api.js';
  import { exportGuide } from '#lib/exportGuides.js';

  const dispatch = createEventDispatcher();

  let sources = [];
  $: current = sources.find((s) => s.id === impSource);
  let impSource = 'reddit';
  let impLabel = 'default';
  let files = [];
  let pct = null; // 0..100 while uploading/importing, null = indeterminate
  let impMsg = '';
  let impErr = '';
  let busy = false;
  $: guide = exportGuide(impSource);

  api.sources().then((d) => {
    sources = d.sources || [];
    if (sources.length) {
      impSource = sources.some((s) => s.id === 'reddit') ? 'reddit' : sources[0].id;
    }
  });

  const sleep = (ms) => new Promise((r) => setTimeout(r, ms));

  async function doImport() {
    impErr = '';
    impMsg = '';
    if (!files.length) {
      impErr = 'Choose a .zip first';
      return;
    }
    busy = true;
    try {
      impMsg = 'Uploading…';
      pct = 0;
      // Returns as soon as the upload is staged (job queued).
      const job = await api.importZips(impSource, impLabel, files, (done, total) => {
        pct = total ? (done / total) * 100 : null;
        impMsg = `Uploading… ${Math.floor(pct)}%`;
      });
      dispatch('queued');
      let j = job;
      // Poll the background worker until the job finishes.
      while (j.status === 'queued' || j.status === 'running') {
        if (j.status === 'running' && j.bytes_total) {
          pct = (j.bytes_done / j.bytes_total) * 100;
          impMsg = `Importing… ${Math.floor(pct)}% · ${j.saved.toLocaleString()} events`;
        } else {
          pct = null;
          impMsg = j.status === 'running' ? 'Importing…' : 'Queued…';
        }
        await sleep(600);
        j = await api.importStatus(job.id);
      }
      if (j.status === 'done') {
        const c = j.coverage || {};
        if (j.saved > 0 && !c.new) {
          impMsg = `Already archived — all ${j.saved} events matched an earlier import and were refreshed.`;
        } else {
          impMsg = `Imported ${j.saved} events · ${c.new || 0} new, ${c.reimported || 0} already archived`;
        }
        dispatch('imported');
      } else {
        impErr = j.error || 'import failed';
        dispatch('imported');
      }
    } catch (e) {
      impErr = e.message;
    } finally {
      busy = false;
      pct = null;
    }
  }
</script>

<div class="card">
  <h3>Import an export</h3>
  <label for="imp-source">Source</label>
  <select id="imp-source" bind:value={impSource}>
    {#each sources as s}<option value={s.id}
        >{s.display_name} ({s.id}){s.maturity === 'verified' ? '' : ' — experimental'}</option
      >{/each}
  </select>
  {#if current && current.maturity !== 'verified'}
    <div class="hint muted experimental">
      Experimental: this importer was written from the documented export format and tested with
      synthetic exports only. Whatever it does not recognise is still kept (as
      <code>{current.id}.unknown</code>), and the archive is preserved, so a later fix reaches your
      data by reprocessing. Please
      <a href="https://github.com/johnnycube/archivist/issues/new?template=export.yml"
        >report what it gets wrong</a
      >.
    </div>
  {/if}
  {#if guide}
    <div class="hint muted">
      {guide.hint}
      <a href="/help/exports#{guide.id}">How to request this export →</a>
    </div>
  {/if}
  <label for="imp-label">Connection label</label>
  <input id="imp-label" bind:value={impLabel} />
  <label for="imp-file">Archive (.zip) — select all parts of a split export</label>
  <input
    id="imp-file"
    type="file"
    accept=".zip"
    multiple
    on:change={(e) => (files = [...e.target.files])}
  />
  {#if files.length > 1}<div class="muted parts">
      {files.length} parts, imported as one archive
    </div>{/if}
  <button class="primary" style="width:100%;margin-top:10px" disabled={busy} on:click={doImport}
    >Import</button
  >
  {#if busy}
    <div class="bar" class:indeterminate={pct === null}>
      <div style="width:{pct === null ? 100 : pct}%"></div>
    </div>
  {/if}
  {#if impMsg}<div class="ok">{impMsg}</div>{/if}
  {#if impErr}<div class="err">{impErr}</div>{/if}
</div>

<style>
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
  .hint {
    font-size: 12px;
    margin-top: 5px;
    line-height: 1.4;
  }
  .hint a {
    white-space: nowrap;
  }
  .parts {
    font-size: 12px;
    margin-top: 4px;
  }
  .bar {
    height: 6px;
    border-radius: 999px;
    background: var(--chip);
    margin-top: 10px;
    overflow: hidden;
  }
  .bar div {
    height: 100%;
    background: var(--accent);
    border-radius: 999px;
    transition: width 0.3s;
  }
  .bar.indeterminate div {
    opacity: 0.45;
    animation: pulse 1.2s ease-in-out infinite;
  }
  @keyframes pulse {
    50% {
      opacity: 0.15;
    }
  }
</style>
