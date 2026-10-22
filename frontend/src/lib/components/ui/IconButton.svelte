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
    loading?: boolean;
    ariaLabel: string;
    title?: string;
    href?: string;
    onclick?: (e: MouseEvent) => void;
    children: Snippet;
  }

  let {
    variant = 'default',
    size = 'sm',
    disabled = false,
    loading = false,
    ariaLabel,
    title,
    href,
    onclick,
    children,
  }: Props = $props();

  let unavailable = $derived(disabled || loading);
</script>

{#if href}
  <a
    class="icon-btn"
    class:variant-default={variant === 'default'}
    class:variant-danger={variant === 'danger'}
    class:variant-warm={variant === 'warm'}
    class:size-sm={size === 'sm'}
    class:size-md={size === 'md'}
    class:is-disabled={unavailable}
    href={unavailable ? undefined : href}
    {title}
    aria-label={ariaLabel}
    aria-disabled={unavailable}
    tabindex={unavailable ? -1 : 0}
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
    disabled={unavailable}
    aria-label={ariaLabel}
    aria-busy={loading ? 'true' : undefined}
    {onclick}
    {title}
  >
    {#if loading}
      <span class="loader" aria-hidden="true"></span>
    {:else}
      {@render children()}
    {/if}
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

  /* Не общий .spinner из app.css: тот 16px в цветах акцента, а внутри
     иконочной кнопки спиннер заменяет иконку — её размер и currentColor.
     Ключевые кадры общие (spin из app.css). */
  .loader {
    width: 14px;
    height: 14px;
    border: 2px solid currentColor;
    border-right-color: transparent;
    border-radius: 50%;
  }

  @media (prefers-reduced-motion: no-preference) {
    .loader { animation: spin 0.8s linear infinite; }
  }
</style>
