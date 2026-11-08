<script>
  import '../app.css';
  import { EXPORT_GUIDES } from '#lib/exportGuides.js';
</script>

<!-- The footer is ordinary document flow below the page (never fixed/sticky), so
     it cannot cover the timeline, its infinite-scroll sentinel or the modal; on
     short pages the flex column pushes it to the bottom of the viewport. No
     overflow is set on these wrappers — that would break the sticky header. -->
<div class="shell">
  <div class="page"><slot /></div>

  <footer class="foot">
    <a class="how" href="/help/exports">How to get your data</a>
    <nav aria-label="Export guides per service">
      {#each EXPORT_GUIDES as g}<a href="/help/exports#{g.id}">{g.name}</a>{/each}
    </nav>
  </footer>
</div>

<style>
  .shell {
    min-height: 100vh;
    min-height: 100dvh;
    display: flex;
    flex-direction: column;
  }
  .page {
    flex: 1 0 auto;
    min-width: 0;
  }
  .foot {
    flex-shrink: 0;
    margin-top: 28px;
    padding: 12px 18px 16px;
    border-top: 1px solid var(--border);
    color: var(--muted);
    font-size: 12px;
    display: flex;
    flex-wrap: wrap;
    gap: 4px 16px;
    align-items: baseline;
  }
  .how {
    color: var(--ink);
    font-weight: 600;
    text-decoration: none;
    white-space: nowrap;
  }
  .how:hover {
    color: var(--accent);
  }
  nav {
    display: flex;
    flex-wrap: wrap;
    gap: 2px 12px;
    min-width: 0;
  }
  /* A label longer than the viewport (Google Takeout lists its services) may
     wrap inside itself; shorter ones stay on one line. */
  nav a {
    color: var(--muted);
    text-decoration: none;
    white-space: nowrap;
    max-width: 100%;
  }
  @media (max-width: 480px) {
    nav a {
      white-space: normal;
    }
  }
  nav a:hover {
    color: var(--accent);
    text-decoration: underline;
  }
</style>
