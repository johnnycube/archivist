<script>
  import { onMount } from 'svelte';
  import { api } from '#lib/api.js';
  import Login from '#lib/components/Login.svelte';
  import Header from '#lib/components/Header.svelte';

  let user = null;
  let ready = false;

  let cur = '';
  let next = '';
  let next2 = '';
  let pwMsg = '';
  let pwErr = '';
  let pwBusy = false;

  let providers = [];
  let linkMsg = '';
  let linkErr = '';
  let linkBusy = '';

  // Why linking a provider came back here (/account?link_error=<code>).
  const LINK_ERRORS = {
    expired: 'The link attempt expired or did not match; try again.',
    denied: 'The identity provider refused the request.',
    identity_taken: 'That login already belongs to another account.',
    failed: 'Linking failed; try again.'
  };

  let delPw = '';
  let delConfirm = '';
  let delErr = '';
  let delBusy = false;

  onMount(async () => {
    const params = new URLSearchParams(location.search);
    const linked = params.get('linked');
    const failed = params.get('link_error');
    if (linked || failed) {
      history.replaceState(null, '', location.pathname);
    }
    try {
      user = await api.me();
    } catch {
      user = null;
    } finally {
      ready = true;
    }
    try {
      providers = (await api.oidcProviders()).providers || [];
    } catch {
      providers = [];
    }
    if (linked) {
      const p = providers.find((x) => x.id === linked);
      linkMsg = 'Linked ' + (p ? p.name : linked) + '. You can now log in through it.';
    } else if (failed) {
      linkErr = LINK_ERRORS[failed] || LINK_ERRORS.failed;
    }
  });

  async function authed() {
    try {
      user = await api.me();
    } catch {
      user = null;
    }
  }

  async function link(id) {
    linkMsg = '';
    linkErr = '';
    linkBusy = id;
    try {
      const { url } = await api.linkProvider(id);
      window.location.assign(url);
    } catch (e) {
      linkErr = e.message;
      linkBusy = '';
    }
  }

  async function changePassword() {
    pwMsg = '';
    pwErr = '';
    if (next !== next2) {
      pwErr = 'New passwords do not match';
      return;
    }
    pwBusy = true;
    try {
      await api.changePassword(cur, next);
      pwMsg = 'Password changed. Other devices were signed out.';
      cur = next = next2 = '';
      user = { ...user, has_password: true };
    } catch (e) {
      pwErr = e.message;
    } finally {
      pwBusy = false;
    }
  }

  async function deleteAccount() {
    delErr = '';
    if (delConfirm !== 'delete my archive') {
      delErr = 'Type the confirmation phrase exactly';
      return;
    }
    delBusy = true;
    try {
      await api.deleteAccount(delPw);
      window.location.href = '/';
    } catch (e) {
      delErr = e.message;
      delBusy = false;
    }
  }

  async function logout() {
    await api.logout();
    user = null;
  }
</script>

{#if !ready}
  <div class="center muted">Loading…</div>
{:else if !user}
  <Login on:authed={authed} />
{:else}
  <Header {user} on:logout={logout} />

  <main>
    <div class="card">
      <h3>Signed in as</h3>
      <div>{user.email}</div>
    </div>

    <div class="card">
      <h3>{user.has_password ? 'Change password' : 'Set a password'}</h3>
      <p class="muted note">
        {user.has_password
          ? 'Changing your password signs out every other session.'
          : 'This account logs in through an identity provider. A password lets you log in without it; setting one signs out every other session.'}
      </p>
      {#if user.has_password}
        <label for="pw-cur">Current password</label>
        <input id="pw-cur" type="password" bind:value={cur} autocomplete="current-password" />
      {/if}
      <label for="pw-new">New password (min. 8 characters)</label>
      <input id="pw-new" type="password" bind:value={next} autocomplete="new-password" />
      <label for="pw-new2">Repeat new password</label>
      <input id="pw-new2" type="password" bind:value={next2} autocomplete="new-password" />
      <button
        class="primary"
        style="margin-top:10px"
        disabled={pwBusy || (user.has_password && !cur) || !next}
        on:click={changePassword}
      >
        Change password
      </button>
      {#if pwMsg}<div class="ok">{pwMsg}</div>{/if}
      {#if pwErr}<div class="err">{pwErr}</div>{/if}
    </div>

    {#if providers.length}
      <div class="card">
        <h3>Linked logins</h3>
        <p class="muted note">
          Log in through an identity provider as well as with your password. Linking takes you to
          the provider and back.
        </p>
        {#each providers as p (p.id)}
          <div class="provider">
            <span>{p.name}</span>
            {#if (user.linked_providers || []).includes(p.id)}
              <span class="muted">Linked</span>
            {:else}
              <button disabled={!!linkBusy} on:click={() => link(p.id)}>
                {linkBusy === p.id ? 'Opening…' : 'Link'}
              </button>
            {/if}
          </div>
        {/each}
        {#if linkMsg}<div class="ok">{linkMsg}</div>{/if}
        {#if linkErr}<div class="err">{linkErr}</div>{/if}
      </div>
    {/if}

    <div class="card danger">
      <h3>Delete account</h3>
      <p class="muted note">
        Deletes your account and your entire archive — every imported event, media file, and import
        record. There is no undo.
      </p>
      {#if user.has_password}
        <label for="del-pw">Password</label>
        <input id="del-pw" type="password" bind:value={delPw} autocomplete="current-password" />
      {/if}
      <label for="del-confirm">Type <strong>delete my archive</strong> to confirm</label>
      <input id="del-confirm" bind:value={delConfirm} placeholder="delete my archive" />
      <button
        class="del"
        disabled={delBusy || (user.has_password && !delPw)}
        on:click={deleteAccount}>Delete my account and all data</button
      >
      {#if delErr}<div class="err">{delErr}</div>{/if}
    </div>
  </main>
{/if}

<style>
  .center {
    text-align: center;
    margin-top: 20vh;
  }
  main {
    max-width: 520px;
    margin: 0 auto;
    padding: 18px;
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
  .note {
    font-size: 13px;
    margin: 0 0 8px;
  }
  .provider {
    display: flex;
    justify-content: space-between;
    align-items: center;
    padding: 6px 0;
  }
  .card.danger {
    border-color: color-mix(in srgb, var(--danger) 40%, var(--border));
  }
  .del {
    margin-top: 10px;
    width: 100%;
    border-color: var(--danger);
    color: var(--danger);
  }
  .del:hover {
    background: color-mix(in srgb, var(--danger) 15%, var(--panel2));
  }
</style>
