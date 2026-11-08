<script>
  // Renders guide text: `backtick` spans become <code>, everything else stays
  // plain text (no {@html} — nothing here is ever parsed as markup).
  export let text = '';
  $: parts = text.split('`').map((t, i) => ({ t, code: i % 2 === 1 }));
</script>

{#each parts as p}{#if p.code}<code>{p.t}</code>{:else}{p.t}{/if}{/each}

<style>
  code {
    font:
      12px/1.4 ui-monospace,
      Menlo,
      monospace;
    background: var(--chip);
    border: 1px solid var(--border);
    border-radius: 5px;
    padding: 0 4px;
    overflow-wrap: anywhere;
  }
</style>
