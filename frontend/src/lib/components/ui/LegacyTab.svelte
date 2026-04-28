<script lang="ts">
  import type { Snippet } from 'svelte';
  import { getTabsContext } from './tabs-context';

  interface Props {
    value: string;
    disabled?: boolean;
    children: Snippet;
  }

  let { value, disabled = false, children }: Props = $props();

  const ctx = getTabsContext();
  const isActive = $derived(ctx.active() === value);
</script>

<button
  type="button"
  class="tab"
  class:active={isActive}
  {disabled}
  role="tab"
  aria-selected={isActive}
  onclick={() => ctx.setActive(value)}
>
  {@render children()}
</button>

<style>
  .tab {
    background: transparent;
    border: none;
    cursor: pointer;
    font: inherit;
    color: var(--color-text-secondary);
    text-transform: uppercase;
    letter-spacing: 0.05em;
    font-size: 12px;
    font-weight: 500;
    padding: 0.5rem 0;
    transition: color var(--t-fast) ease;
    position: relative;
  }

  .tab:hover:not(:disabled) {
    color: var(--color-text-primary);
  }

  .tab:disabled {
    opacity: 0.5;
    cursor: not-allowed;
  }

  .tab:focus-visible {
    outline: 2px solid var(--color-accent);
    outline-offset: 2px;
    border-radius: var(--radius-sm);
  }

  /* Underline variant */
  :global(.variant-underline) > .tab.active {
    color: var(--color-text-primary);
  }
  :global(.variant-underline) > .tab.active::after {
    content: '';
    position: absolute;
    left: 0;
    right: 0;
    bottom: -1px;
    height: 2px;
    background: var(--color-accent);
  }

  /* Pill variant */
  :global(.variant-pill) > .tab {
    padding: 0.375rem 0.875rem;
    border-radius: var(--radius-sm);
    text-transform: none;
    letter-spacing: 0;
    font-size: 13px;
  }
  :global(.variant-pill) > .tab.active {
    background: var(--color-bg-tertiary);
    color: var(--color-text-primary);
  }
</style>
