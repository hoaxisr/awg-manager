<script lang="ts" module>
  import type { Snippet } from 'svelte';
  export type IconButtonVariant = 'default' | 'danger' | 'warm';
  export type IconButtonSize = 'sm' | 'md';
</script>

<script lang="ts">
  interface Props {
    variant?: IconButtonVariant;
    size?: IconButtonSize;
    disabled?: boolean;
    ariaLabel: string;
    href?: string;
    onclick?: (e: MouseEvent) => void;
    children: Snippet;
  }

  let {
    variant = 'default',
    size = 'sm',
    disabled = false,
    ariaLabel,
    href,
    onclick,
    children,
  }: Props = $props();
</script>

{#if href}
  <a
    class="icon-btn"
    class:variant-default={variant === 'default'}
    class:variant-danger={variant === 'danger'}
    class:variant-warm={variant === 'warm'}
    class:size-sm={size === 'sm'}
    class:size-md={size === 'md'}
    class:is-disabled={disabled}
    {href}
    aria-label={ariaLabel}
    aria-disabled={disabled}
    tabindex={disabled ? -1 : 0}
  >
    {@render children()}
  </a>
{:else}
  <button
    type="button"
    class="icon-btn"
    class:variant-default={variant === 'default'}
    class:variant-danger={variant === 'danger'}
    class:variant-warm={variant === 'warm'}
    class:size-sm={size === 'sm'}
    class:size-md={size === 'md'}
    {disabled}
    aria-label={ariaLabel}
    {onclick}
  >
    {@render children()}
  </button>
{/if}

<style>
  .icon-btn {
    display: inline-flex;
    align-items: center;
    justify-content: center;
    background: transparent;
    border: 1px solid transparent;
    border-radius: var(--radius-sm);
    color: var(--color-text-muted);
    cursor: pointer;
    transition: background var(--t-fast) ease, color var(--t-fast) ease;
    text-decoration: none;
  }

  .icon-btn:disabled, .icon-btn.is-disabled {
    opacity: 0.5;
    cursor: not-allowed;
  }
  .icon-btn:focus-visible {
    outline: 2px solid var(--color-accent);
    outline-offset: 2px;
  }

  .size-sm { width: 28px; height: 28px; }
  .size-md { width: 32px; height: 32px; }

  .variant-default:hover:not(:disabled):not(.is-disabled) {
    background: var(--color-bg-hover);
    color: var(--color-accent);
  }

  .variant-danger:hover:not(:disabled):not(.is-disabled) {
    background: rgba(247, 118, 142, 0.1);
    color: var(--color-error);
  }

  .variant-warm:hover:not(:disabled):not(.is-disabled) {
    background: rgba(247, 118, 168, 0.1);
    color: #f7a8ce;
  }
  :global(html.light) .variant-warm:hover:not(:disabled):not(.is-disabled),
  :global([data-theme="light"]) .variant-warm:hover:not(:disabled):not(.is-disabled) {
    color: #c2185b;
  }

  :global(.icon-btn > svg) {
    width: 16px;
    height: 16px;
    flex-shrink: 0;
  }
</style>
