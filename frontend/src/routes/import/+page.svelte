<script>
  import { onMount, onDestroy } from 'svelte';
  import { api } from '#lib/api.js';
  import Login from '#lib/components/Login.svelte';
  import Header from '#lib/components/Header.svelte';
  import ImportForm from '#lib/components/ImportForm.svelte';

  let user = null;
  let ready = false;
  let jobs = [];
  let sources = [];
  let timer = null;

  onMount(async () => {
    try {
      user = await api.me();
      await Promise.all([refreshJobs(), loadSources()]);
    } catch {
      user = null;
    } finally {
      ready = true;
    }
  });
  onDestroy(() => clearTimeout(timer));

  async function loadSources() {
    const d = await api.sources();
    sources = d.sources || [];
  }

  async function refreshJobs() {
    const d = await api.imports();
    jobs = d.jobs || [];
    clearTimeout(timer);
    // Keep polling while the background worker is busy.
    if (jobs.some((j) => j.status === 'queued' || j.status === 'running')) {
      timer = setTimeout(refreshJobs, 1200);
    }
  }

  async function onAuthed(e) {
    user = e.detail;
    await Promise.all([refreshJobs(), loadSources()]);
  }
  async function logout() {
    await api.logout();
    user = null;
    jobs = [];
  }

  let actionErr = '';

  async function reprocess(j) {
    actionErr = '';
    try {
      await api.reprocessImport(j.id);
      await refreshJobs();
    } catch (e) {
      actionErr = e.message;
    }
  }

  // The coverage report as an issue wants it: counts, event types and the names
  // of unrecognised files — no content of the archive.
  let copied = '';
  async function copyReport(j) {
    const report = {
      source: j.source,
      status: j.status,
      error: j.error || undefined,
      parts: j.parts,
      coverage: j.coverage
    };
    try {
      await navigator.clipboard.writeText(JSON.stringify(report, null, 2));
      copied = j.id;
      setTimeout(() => (copied = copied === j.id ? '' : copied), 2000);
    } catch (e) {
      actionErr = 'Could not copy: ' + e.message;
    }
  }

  async function cancel(j) {
    if (
      j.status === 'running' &&
      !confirm(
        `Stop this ${j.source} import?\n\nEvents it saved so far are kept; delete the import afterwards to remove them.`
      )
    )
      return;
    actionErr = '';
    try {
      await api.cancelImport(j.id);
      await refreshJobs();
    } catch (e) {
      actionErr = e.message;
    }
  }

  async function remove(j) {
    const what =
      j.status === 'done'
        ? 'This removes the import, its preserved archive, and the events it added. Events also contained in a later import are kept.'
        : 'This removes the import and its uploaded archive.';
    if (!confirm(`Delete this ${j.source} import?\n\n${what}`)) return;
    actionErr = '';
    try {
      await api.deleteImport(j.id);
      await refreshJobs();
    } catch (e) {
      actionErr = e.message;
    }
  }

  const fmtDate = (s) => (s ? new Date(s).toLocaleString() : '');
  const pct = (j) => (j.bytes_total ? Math.floor((j.bytes_done / j.bytes_total) * 100) : 0);
</script>

{#if !ready}
  <div class="center muted">Loading…</div>
{:else if !user}
  <Login on:authed={onAuthed} />
{:else}
  <Header {user} on:logout={logout} />

  <main>
    <div class="grid">
      <section>
        <ImportForm on:queued={refreshJobs} on:imported={refreshJobs} />

        <div class="card">
          <h3>Supported sources</h3>
          <ul class="srcs">
            {#each sources as s}
              <li>
                <strong>{s.display_name}</strong> <span class="type">{s.id}</span> — {s.event_types
                  .length} event types
              </li>
            {/each}
          </ul>
          <p class="muted hint">
            Request your archive from each service, then upload the .zip here unchanged. Anything
            the importer can't map is kept as <span class="type">&lt;source&gt;.unknown</span> — nothing
            is dropped. Re-importing the same archive is idempotent: events you already archived are refreshed
            in place, never duplicated. Each import reports how many events were new versus already archived.
          </p>
        </div>
      </section>

      <section>
        <div class="card">
          <h3>Recent imports</h3>
          {#if actionErr}<div class="err">{actionErr}</div>{/if}
          {#if !jobs.length}
            <div class="muted">No imports yet.</div>
          {:else}
            <table>
              <thead>
                <tr
                  ><th>When</th><th>Source</th><th>Label</th><th>Status</th><th>Events</th><th
                  ></th></tr
                >
              </thead>
              <tbody>
                {#each jobs as j (j.id)}
                  <tr>
                    <td class="muted">{fmtDate(j.created_at)}</td>
                    <td
                      >{j.source}{#if j.parts > 1}<div class="muted detail">
                          {j.parts} parts
                        </div>{/if}</td
                    >
                    <td class="muted">{j.label}</td>
                    <td>
                      <span class="badge st-{j.status}">{j.status}</span>
                      {#if j.error}<div class="err">{j.error}</div>{/if}
                    </td>
                    <td>
                      {#if j.status === 'done'}
                        {j.saved.toLocaleString()}
                        {#if j.coverage}
                          <div class="muted detail">
                            {j.coverage.new || 0} new · {j.coverage.reimported || 0} already archived{#if j.coverage.superseded}
                              · {j.coverage.superseded} superseded removed{/if}
                            · {j.coverage.mapped || 0} mapped, {j.coverage.generic || 0} generic
                          </div>
                        {/if}
                      {:else if j.status === 'running'}
                        {j.saved.toLocaleString()}
                        {#if j.cancel_requested}<div class="muted detail">Stopping…</div>{/if}
                        <div class="bar"><div style="width:{pct(j)}%"></div></div>
                        <div class="muted detail">{pct(j)}% of archive read</div>
                      {:else}
                        <span class="muted">—</span>
                      {/if}
                    </td>
                    <td class="actions">
                      {#if j.coverage || j.error}
                        <button
                          title="Copy the coverage report (counts and file names, no content) for an issue"
                          on:click={() => copyReport(j)}
                          >{copied === j.id ? 'Copied' : 'Copy report'}</button
                        >
                      {/if}
                      {#if j.status === 'done' || j.status === 'error'}
                        <button
                          title="Re-derive this import's events from the preserved archive"
                          on:click={() => reprocess(j)}>Reprocess</button
                        >
                      {/if}
                      {#if (j.status === 'queued' || j.status === 'running') && !j.cancel_requested}
                        <button title="Stop this import" on:click={() => cancel(j)}>Cancel</button>
                      {/if}
                      {#if j.status !== 'running'}
                        <button class="del" on:click={() => remove(j)}>Delete</button>
                      {/if}
                    </td>
                  </tr>
                {/each}
              </tbody>
            </table>
          {/if}
        </div>
      </section>
    </div>
  </main>
{/if}

<style>
  .center {
    text-align: center;
    margin-top: 20vh;
  }
  main {
    padding: 18px;
    max-width: 1100px;
    margin: 0 auto;
  }
  .grid {
    display: grid;
    grid-template-columns: 340px 1fr;
    gap: 18px;
    align-items: start;
  }
  @media (max-width: 900px) {
    .grid {
      grid-template-columns: 1fr;
    }
  }
  h3 {
    font-size: 13px;
    text-transform: uppercase;
    letter-spacing: 0.04em;
    color: var(--muted);
    margin: 0 0 8px;
  }
  .srcs {
    margin: 0;
    padding-left: 18px;
  }
  .srcs li {
    margin: 4px 0;
  }
  .hint {
    font-size: 13px;
    margin: 10px 0 0;
  }
  table {
    width: 100%;
    border-collapse: collapse;
    font-size: 14px;
  }
  th {
    text-align: left;
    color: var(--muted);
    font-size: 12px;
    text-transform: uppercase;
    letter-spacing: 0.04em;
    padding: 6px 8px;
    border-bottom: 1px solid var(--border);
  }
  td {
    padding: 8px;
    border-bottom: 1px solid var(--border);
    vertical-align: top;
  }
  .detail {
    font-size: 12px;
    margin-top: 2px;
  }
  tbody tr:last-child td {
    border-bottom: none;
  }
  .bar {
    height: 5px;
    border-radius: 999px;
    background: var(--chip);
    margin-top: 5px;
    overflow: hidden;
    min-width: 90px;
  }
  .bar div {
    height: 100%;
    background: var(--accent);
    border-radius: 999px;
    transition: width 0.4s;
  }
  .actions {
    white-space: nowrap;
    text-align: right;
  }
  .actions button {
    font-size: 12px;
    padding: 3px 9px;
    margin-left: 4px;
  }
  .actions .del:hover {
    border-color: var(--danger);
    color: var(--danger);
  }
  .st-done {
    color: var(--accent2);
  }
  .st-error {
    color: var(--danger);
  }
  .st-running,
  .st-queued {
    color: var(--warn);
  }
</style>
