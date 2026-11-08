<script>
  import { createEventDispatcher, onMount } from 'svelte';
  import { api } from '#lib/api.js';

  const dispatch = createEventDispatcher();
  let mode = 'login';
  let email = '';
  let password = '';
  let error = '';
  let busy = false;
  let providers = [];
  // Password signup is offered only where the operator opened it.
  let signup = false;

  // Why an OIDC login came back here (the callback redirects to /?auth_error=).
  const AUTH_ERRORS = {
    expired: 'The login attempt expired or did not match; try again.',
    denied: 'The identity provider refused the login.',
    no_account: 'There is no account for this login. Ask the operator to create one.',
    no_email: 'The identity provider did not supply an email address.',
    email_in_use:
      'An account with this email already exists. Log in with its password, then link the provider on the account page.',
    email_unverified:
      'The identity provider has not verified your email address, so no account can be created from it.',
    failed: 'Login with the identity provider failed; try again.'
  };

  onMount(async () => {
    const code = new URLSearchParams(location.search).get('auth_error');
    if (code) {
      error = AUTH_ERRORS[code] || AUTH_ERRORS.failed;
      history.replaceState(null, '', location.pathname);
    }
    try {
      const opts = await api.authOptions();
      providers = opts.providers || [];
      signup = !!opts.signup;
    } catch {
      providers = [];
    }
  });

  async function submit() {
    error = '';
    busy = true;
    try {
      const user =
        mode === 'login' ? await api.login(email, password) : await api.signup(email, password);
      dispatch('authed', user);
    } catch (e) {
      error = e.message;
    } finally {
      busy = false;
    }
  }
</script>

<div class="wrap">
  <img class="logo" src="/favicon.svg" alt="" width="56" height="56" />
  <div class="kicker" style="text-align:center">Archivist</div>
  <h1>Your personal archive</h1>
  <div class="card">
    {#if signup}
      <div class="tabs">
        <button class:sel={mode === 'login'} on:click={() => (mode = 'login')}>Log in</button>
        <button class:sel={mode === 'signup'} on:click={() => (mode = 'signup')}>Sign up</button>
      </div>
    {/if}
    <label for="auth-email">Email</label>
    <input
      id="auth-email"
      type="email"
      autocomplete="username"
      bind:value={email}
      placeholder="you@example.com"
      on:keydown={(e) => e.key === 'Enter' && submit()}
    />
    <label for="auth-password"
      >Password {#if mode === 'signup'}<span class="muted">(min 8 chars)</span>{/if}</label
    >
    <input
      id="auth-password"
      type="password"
      autocomplete={mode === 'signup' ? 'new-password' : 'current-password'}
      bind:value={password}
      placeholder="••••••••"
      on:keydown={(e) => e.key === 'Enter' && submit()}
    />
    <button class="primary" style="width:100%;margin-top:10px" disabled={busy} on:click={submit}>
      {mode === 'login' ? 'Log in' : 'Create account'}
    </button>
    {#if error}<div class="err">{error}</div>{/if}
    {#if !signup}
      <p class="muted hint">New accounts are created by the operator of this server.</p>
    {/if}
    {#if providers.length}
      <div class="or"><span>or</span></div>
      {#each providers as p (p.id)}
        <a class="btn provider" href={'/api/auth/oidc/' + encodeURIComponent(p.id) + '/start'}>
          Continue with {p.name}
        </a>
      {/each}
    {/if}
  </div>
</div>

<style>
  .wrap {
    max-width: 380px;
    margin: 8vh auto;
    padding: 0 16px;
  }
  .logo {
    display: block;
    margin: 0 auto 10px;
  }
  h1 {
    text-align: center;
    margin: 6px 0 18px;
    font-size: 26px;
    letter-spacing: -0.02em;
  }
  .tabs {
    display: flex;
    gap: 8px;
    margin-bottom: 14px;
  }
  .tabs button {
    flex: 1;
  }
  .tabs button.sel {
    background: var(--accent);
    border-color: var(--accent);
    color: #08111f;
    font-weight: 600;
  }
  label {
    display: block;
    font-size: 12px;
    color: var(--muted);
    margin: 8px 0 4px;
  }
  .hint {
    font-size: 12px;
    margin: 10px 0 0;
    text-align: center;
  }
  .or {
    display: flex;
    align-items: center;
    gap: 10px;
    margin: 14px 0 10px;
    color: var(--muted);
    font-size: 12px;
  }
  .or::before,
  .or::after {
    content: '';
    flex: 1;
    border-top: 1px solid var(--border, #2a3142);
  }
  .provider {
    display: block;
    width: 100%;
    box-sizing: border-box;
    margin-top: 8px;
    padding: 9px 12px;
    text-align: center;
    text-decoration: none;
    border: 1px solid var(--border, #2a3142);
    border-radius: 8px;
    color: inherit;
  }
  .provider:hover {
    border-color: var(--accent);
  }
</style>
