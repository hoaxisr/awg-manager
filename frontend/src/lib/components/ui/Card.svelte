<script lang="ts" module>
  import type { Snippet } from 'svelte';
  export type CardVariant = 'default' | 'nested';
  export type CardPadding = 'none' | 'sm' | 'md' | 'lg';
</script>

<script lang="ts">
  interface Props {
    variant?: CardVariant;
    padding?: CardPadding;
    header?: Snippet;
    footer?: Snippet;
    children: Snippet;
  }

  let {
    variant = 'default',
    padding = 'md',
    header,
    footer,
    children,
  }: Props = $props();
</script>

<div
  class="card"
  class:variant-default={variant === 'default'}
  class:variant-nested={variant === 'nested'}
  class:pad-none={padding === 'none'}
  class:pad-sm={padding === 'sm'}
  class:pad-md={padding === 'md'}
  class:pad-lg={padding === 'lg'}
>
  {#if header}
    <div class="card-header">{@render header()}</div>
  {/if}
  <div class="card-body">{@render children()}</div>
  {#if footer}
    <div class="card-footer">{@render footer()}</div>
  {/if}
</div>

<style>
  .card {
    border: 1px solid var(--color-border);
    border-radius: var(--radius);
    overflow: hidden;
    width: 100%;
    min-width: 0;
    box-sizing: border-box;
  }

  .card-body, .card-header, .card-footer {
    min-width: 0;
  }

  .variant-default {
    background: var(--color-bg-secondary);
    box-shadow: var(--shadow);
  }

  .variant-nested {
    background: var(--color-bg-tertiary);
    box-shadow: none;
  }

  .card-header {
    padding: 0.75rem 1rem;
    border-bottom: 1px solid var(--color-border);
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: 0.5rem;
  }

  .card-footer {
    padding: 0.75rem 1rem;
    border-top: 1px solid var(--color-border);
  }

  .pad-none .card-body { padding: 0; }
  .pad-sm .card-body { padding: 0.625rem; }
  .pad-md .card-body { padding: 1rem; }
  .pad-lg .card-body { padding: 1.5rem; }
</style>
