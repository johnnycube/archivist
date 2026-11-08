<script>
  import { isImage, mediaUrl, externalUrl, attachmentName } from '#lib/media.js';
  import { safeHref } from '#lib/url.js';
  import { createEventDispatcher } from 'svelte';
  import { platform } from '#lib/platform.js';
  export let event;
  const dispatch = createEventDispatcher();

  $: cat = event?.category || 'other';
  $: p = platform(event?.source_id);
  let imgBroken = false;
  $: if (event) imgBroken = false; // reset per event
  $: meta = event
    ? {
        source: event.source_id,
        external_id: event.external_id,
        thread: event.thread_external_id,
        parent: event.parent_external_id,
        metadata: event.metadata
      }
    : {};
  $: when =
    event?.occurred_at && !event.occurred_at.startsWith('0001')
      ? new Date(event.occurred_at).toLocaleString()
      : 'undated';
</script>

<svelte:window on:keydown={(e) => e.key === 'Escape' && event && dispatch('close')} />

{#if event}
  <div class="backdrop" on:click|self={() => dispatch('close')} role="presentation">
    <div class="card modal" style="--src:{p.color}">
      <div class="row" style="gap:8px">
        <span class="chip">{p.label}</span>
        <span class="badge cat-{cat}">{cat}</span>
        <span class="type">{event.type}</span>
      </div>
      <h2>{event.title || '(no title)'}</h2>
      <div class="muted">{event.actor ? 'by ' + event.actor + ' · ' : ''}{when}</div>
      {#if event.media && event.media.length}
        {#if isImage(event.media[0]) && mediaUrl(event, 0) && !imgBroken}
          <img
            class="mimg"
            src={mediaUrl(event, 0)}
            alt="attached media"
            on:error={() => (imgBroken = true)}
          />
        {/if}
        <ul class="files">
          {#each event.media as m, i}
            {#if !isImage(m) || i > 0 || !mediaUrl(event, 0) || imgBroken}
              <li>
                {#if mediaUrl(event, i) || externalUrl(event, i)}
                  <a
                    href={mediaUrl(event, i) || externalUrl(event, i)}
                    target="_blank"
                    rel="noopener noreferrer">📎 {attachmentName(m)}</a
                  >{#if !mediaUrl(event, i)}
                    <span class="muted">(external link)</span>{/if}
                {:else}
                  📎 {attachmentName(m)} <span class="muted">(not stored)</span>
                {/if}{#if m.mime}
                  <span class="muted">({m.mime})</span>{/if}
              </li>
            {/if}
          {/each}
        </ul>
      {/if}
      {#if event.body}<div class="body">{event.body}</div>{/if}
      {#if event.targets && event.targets.length}
        <div class="tgt">
          → {event.targets[0].relation}
          {#if event.targets[0].target_url}
            {#if safeHref(event.targets[0].target_url)}
              <a
                href={safeHref(event.targets[0].target_url)}
                target="_blank"
                rel="noopener noreferrer">{event.targets[0].target_url}</a
              >
            {:else}
              <span>{event.targets[0].target_url}</span>
            {/if}
          {:else}
            {event.targets[0].target_external_id || ''}
          {/if}
        </div>
      {/if}
      <pre>{JSON.stringify(meta, null, 2)}</pre>
      <div class="row" style="justify-content:flex-end">
        <button on:click={() => dispatch('close')}>Close</button>
      </div>
    </div>
  </div>
{/if}

<style>
  .backdrop {
    position: fixed;
    inset: 0;
    background: rgba(0, 0, 0, 0.55);
    display: flex;
    align-items: center;
    justify-content: center;
    padding: 20px;
    z-index: 20;
  }
  .modal {
    max-width: 640px;
    width: 100%;
    max-height: 82vh;
    overflow: auto;
    border-top: 3px solid var(--src);
  }
  .chip {
    font-size: 11px;
    font-weight: 600;
    padding: 2px 8px;
    border-radius: 999px;
    color: var(--src);
    border: 1px solid color-mix(in srgb, var(--src) 45%, transparent);
    background: color-mix(in srgb, var(--src) 14%, transparent);
  }
  h2 {
    margin: 8px 0;
    font-size: 18px;
  }
  .body {
    white-space: pre-wrap;
    margin: 12px 0;
  }
  .files {
    list-style: none;
    padding: 0;
    margin: 6px 0 0;
    font-size: 13px;
  }
  .files li {
    margin: 2px 0;
  }
  .files a {
    color: inherit;
  }
  .mimg {
    width: 100%;
    max-height: 280px;
    object-fit: cover;
    border-radius: 10px;
    margin: 10px 0 2px;
    border: 1px solid var(--border);
  }
  .tgt {
    color: var(--accent2);
    word-break: break-all;
    margin-bottom: 8px;
  }
  .tgt a {
    color: inherit;
  }
</style>
