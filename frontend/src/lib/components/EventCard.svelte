<script>
  import { isImage, mediaUrl, externalUrl, attachmentName } from '#lib/media.js';
  import { safeHref } from '#lib/url.js';
  import { createEventDispatcher } from 'svelte';
  import { platform } from '#lib/platform.js';

  export let event;
  export let selected = false;

  const dispatch = createEventDispatcher();

  $: p = platform(event.source_id);
  $: cat = event.category || 'other';
  $: time =
    event.occurred_at && !event.occurred_at.startsWith('0001')
      ? new Date(event.occurred_at).toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' })
      : '';
  $: tgt = event.targets && event.targets.length ? event.targets[0] : null;
  // Stored media streams from the server; an export's own URL is never
  // loaded, only offered as an external link (see media.js).
  let broken = false;
  $: m0 = event.media && event.media.length ? event.media[0] : null;
  $: thumb = m0 && isImage(m0) && !broken ? mediaUrl(event, 0) : '';
  // Everything not shown as the thumbnail, with its index into event.media.
  $: files = (event.media || [])
    .map((m, i) => ({ m, i }))
    .filter(({ m, i }) => !(isImage(m) && mediaUrl(event, i)));
  $: file = files.length ? files[0] : null;
  $: fileHref = file ? mediaUrl(event, file.i) || externalUrl(event, file.i) : '';
</script>

<div
  class="ev"
  class:selected
  style="--src:{p.color}"
  data-eid={event.id}
  on:click={() => dispatch('open', event)}
  on:keydown={(e) =>
    (e.key === 'Enter' || e.key === ' ') &&
    e.target === e.currentTarget &&
    (e.preventDefault(), dispatch('open', event))}
  role="button"
  tabindex="0"
>
  <div class="c">
    <div class="meta">
      <span class="glyph" aria-hidden="true">{p.glyph}</span>
      <span class="src">{p.label}</span>
      {#if time}<span class="mono">{time}</span>{/if}
      <span class="mono dim">{event.type}</span>
      <span class="grow"></span>
      <span class="badge cat-{cat}">{cat}</span>
    </div>
    {#if event.title}<div class="title">{event.title}</div>{/if}
    {#if event.body}<div class="body">{event.body}</div>{/if}
    {#if tgt}
      <div class="tgt">
        → {tgt.relation}
        {#if tgt.target_url}
          {#if safeHref(tgt.target_url)}
            <a
              href={safeHref(tgt.target_url)}
              target="_blank"
              rel="noopener noreferrer"
              on:click|stopPropagation>{tgt.target_url}</a
            >
          {:else}
            <span>{tgt.target_url}</span>
          {/if}
        {:else}
          {tgt.target_external_id || ''}
        {/if}
      </div>
    {/if}
  </div>
  {#if thumb}
    <img class="thumb" src={thumb} alt="" loading="lazy" on:error={() => (broken = true)} />
  {:else if file && fileHref}
    <a
      class="file"
      href={fileHref}
      target={mediaUrl(event, file.i) ? undefined : '_blank'}
      rel={mediaUrl(event, file.i) ? undefined : 'noopener noreferrer'}
      on:click|stopPropagation
      title="{mediaUrl(event, file.i) ? 'Download' : 'Open (external)'} {attachmentName(file.m)}"
    >
      📎 <span class="fname">{attachmentName(file.m)}</span>{#if files.length > 1}
        +{files.length - 1}{/if}
    </a>
  {:else if file}
    <span class="file" title="Not stored with the import">
      📎 <span class="fname">{attachmentName(file.m)}</span>
    </span>
  {/if}
</div>

<style>
  .ev {
    display: flex;
    align-items: flex-start;
    gap: 12px;
    background: var(--panel);
    border: 1px solid var(--border);
    border-left: 3px solid var(--src);
    border-radius: 12px;
    padding: 11px 14px;
    margin-bottom: 8px;
    cursor: pointer;
    transition:
      transform 0.15s ease,
      border-color 0.15s ease,
      box-shadow 0.15s ease;
  }
  .ev:hover {
    border-color: color-mix(in srgb, var(--src) 55%, var(--border));
    transform: translateX(3px);
    box-shadow: 0 8px 24px -16px var(--src);
  }
  .ev.selected,
  .ev:focus-visible {
    outline: none;
    border-color: color-mix(in srgb, var(--src) 70%, var(--border));
    box-shadow: 0 0 0 2px color-mix(in srgb, var(--src) 35%, transparent);
  }
  .c {
    flex: 1;
    min-width: 0;
  }
  .file {
    font-size: 12px;
    color: var(--muted);
    text-decoration: none;
    flex-shrink: 0;
    max-width: 140px;
    display: inline-flex;
    gap: 4px;
    align-items: center;
    border: 1px solid var(--border);
    border-radius: 8px;
    padding: 4px 8px;
  }
  .file:hover {
    color: var(--fg, #e6e9ef);
    border-color: var(--src);
  }
  .fname {
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  .thumb {
    width: 64px;
    height: 64px;
    border-radius: 8px;
    object-fit: cover;
    flex-shrink: 0;
    border: 1px solid var(--border);
  }
  .meta {
    display: flex;
    gap: 8px;
    align-items: center;
    flex-wrap: wrap;
    margin-bottom: 5px;
    min-width: 0;
  }
  .glyph {
    flex-shrink: 0;
    width: 20px;
    height: 20px;
    border-radius: 50%;
    text-align: center;
    font:
      700 10.5px/18px ui-monospace,
      Menlo,
      monospace;
    color: var(--src);
    background: color-mix(in srgb, var(--src) 16%, var(--panel2));
    border: 1px solid color-mix(in srgb, var(--src) 45%, transparent);
  }
  .src {
    color: var(--src);
    font-weight: 600;
    font-size: 12.5px;
  }
  .mono {
    font-family: ui-monospace, Menlo, monospace;
    font-size: 11.5px;
    color: var(--muted);
  }
  .dim {
    opacity: 0.75;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  .grow {
    flex: 1;
  }
  /* a title can be one long unbreakable token (a pasted URL): break it rather
     than let the card push a horizontal scrollbar onto the whole page */
  .title {
    font-weight: 600;
    overflow-wrap: anywhere;
  }
  .body {
    color: var(--muted);
    margin-top: 3px;
    white-space: pre-wrap;
    overflow: hidden;
    display: -webkit-box;
    -webkit-line-clamp: 2;
    -webkit-box-orient: vertical;
  }
  .tgt {
    margin-top: 5px;
    font-size: 12px;
    color: color-mix(in srgb, var(--src) 70%, var(--ink));
    word-break: break-all;
  }
  .tgt a {
    color: inherit;
    text-decoration: none;
  }
  .tgt a:hover {
    text-decoration: underline;
  }
  @media (prefers-reduced-motion: reduce) {
    .ev {
      transition: none;
    }
    .ev:hover {
      transform: none;
    }
  }
</style>
