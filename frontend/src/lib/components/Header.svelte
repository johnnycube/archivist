<script>
  import { createEventDispatcher } from 'svelte';
  import { page } from '$app/state';

  export let user;
  const dispatch = createEventDispatcher();
</script>

<header class="top">
  <span class="brand">
    <img src="/favicon.svg" alt="" width="22" height="22" />
    <span class="kicker">Archivist</span>
  </span>
  <nav>
    <a href="/" class:active={page.url.pathname === '/'}>Timeline</a>
    <a href="/import" class:active={page.url.pathname.startsWith('/import')}>Import</a>
    <a href="/account" class:active={page.url.pathname.startsWith('/account')}>Account</a>
    <a href="/help/exports" class:active={page.url.pathname.startsWith('/help')}>Help</a>
  </nav>
  <span style="flex:1"></span>
  <span class="muted email">{user.email}</span>
  <button on:click={() => dispatch('logout')}>Log out</button>
</header>

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
  }
  nav {
    display: flex;
    gap: 4px;
  }
  nav a {
    color: var(--muted);
    text-decoration: none;
    font-weight: 600;
    padding: 5px 11px;
    border-radius: 8px;
  }
  nav a:hover {
    color: var(--ink);
    background: var(--panel2);
  }
  nav a.active {
    color: var(--ink);
    background: var(--panel2);
  }
  .email {
    min-width: 0;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
    max-width: 40vw;
  }
  /* Stay on ONE row at phone widths: the timeline's sticky rail/pill offsets
     assume the header's single-row height. */
  @media (max-width: 600px) {
    .top {
      gap: 6px;
      padding: 12px 10px;
    }
    .brand,
    .email {
      display: none;
    }
    nav {
      gap: 0;
    }
    nav a {
      padding: 5px 7px;
      font-size: 13.5px;
    }
    .top button {
      padding: 5px 9px;
      font-size: 13px;
      white-space: nowrap;
    }
  }
</style>
