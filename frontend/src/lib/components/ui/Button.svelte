<script lang="ts" module>
  import type { Snippet } from 'svelte';
  export type ButtonVariant = 'primary' | 'secondary' | 'ghost' | 'danger' | 'success' | 'outline-danger' | 'outline-primary';
  export type ButtonSize = 'sm' | 'md';
</script>

<script lang="ts">
  interface Props {
    variant?: ButtonVariant;
    size?: ButtonSize;
    type?: 'button' | 'submit' | 'reset';
    disabled?: boolean;
    loading?: boolean;
    fullWidth?: boolean;
    href?: string;
    target?: string;
    rel?: string;
    iconBefore?: Snippet;
    iconAfter?: Snippet;
    onclick?: (e: MouseEvent) => void;
    title?: string;
    children: Snippet;
  }

  let {
    variant = 'secondary',
    size = 'sm',
    type = 'button',
    disabled = false,
    loading = false,
    fullWidth = false,
    href,
    target,
    rel,
    iconBefore,
    iconAfter,
    onclick,
    title,
    children,
  }: Props = $props();

  const isDisabled = $derived(disabled || loading);
</script>

{#if href}
  <a
    class="btn"
    class:has-icon-before={!!iconBefore}
    class:has-icon-after={!!iconAfter}
    class:variant-primary={variant === 'primary'}
    class:variant-secondary={variant === 'secondary'}
    class:variant-ghost={variant === 'ghost'}
    class:variant-danger={variant === 'danger'}
    class:variant-success={variant === 'success'}
    class:variant-outline-danger={variant === 'outline-danger'}
    class:variant-outline-primary={variant === 'outline-primary'}
    class:size-sm={size === 'sm'}
    class:size-md={size === 'md'}
    class:full-width={fullWidth}
    class:is-disabled={isDisabled}
    {href}
    {target}
    {rel}
    {title}
    aria-disabled={isDisabled}
    aria-busy={loading}
    role="button"
    tabindex={isDisabled ? -1 : 0}
  >
    {#if loading}
      <span class="spinner" aria-hidden="true"></span>
    {:else if iconBefore}
      <span class="icon icon-before" aria-hidden="true">{@render iconBefore()}</span>
    {/if}
    <span class="btn-label">{@render children()}</span>
    {#if iconAfter && !loading}
      <span class="icon icon-after" aria-hidden="true">{@render iconAfter()}</span>
    {/if}
  </a>
{:else}
  <button
    class="btn"
    class:has-icon-before={!!iconBefore}
    class:has-icon-after={!!iconAfter}
    class:variant-primary={variant === 'primary'}
    class:variant-secondary={variant === 'secondary'}
    class:variant-ghost={variant === 'ghost'}
    class:variant-danger={variant === 'danger'}
    class:variant-success={variant === 'success'}
    class:variant-outline-danger={variant === 'outline-danger'}
    class:variant-outline-primary={variant === 'outline-primary'}
    class:size-sm={size === 'sm'}
    class:size-md={size === 'md'}
    class:full-width={fullWidth}
    {type}
    disabled={isDisabled}
    aria-busy={loading}
    {onclick}
    {title}
  >
    {#if loading}
      <span class="spinner" aria-hidden="true"></span>
    {:else if iconBefore}
      <span class="icon icon-before" aria-hidden="true">{@render iconBefore()}</span>
    {/if}
    <span class="btn-label">{@render children()}</span>
    {#if iconAfter && !loading}
      <span class="icon icon-after" aria-hidden="true">{@render iconAfter()}</span>
    {/if}
  </button>
{/if}

<style>
  .btn {
    display: inline-flex;
    align-items: center;
    justify-content: center;
    gap: 0.4375rem;
    box-sizing: border-box;
    font-family: inherit;
    font-weight: 500;
    line-height: 1;
    border-radius: var(--radius-sm);
    border: 1px solid transparent;
    cursor: pointer;
    transition: background var(--t-fast) ease, color var(--t-fast) ease,
                border-color var(--t-fast) ease, filter var(--t-fast) ease;
    text-decoration: none;
    user-select: none;
    white-space: nowrap;
    vertical-align: middle;
  }

  button.btn {
    margin: 0;
    appearance: none;
  }

  .btn .btn-label {
    display: inline-flex;
    justify-content: center;
    align-items: center;
    line-height: 1;
  }

  .btn .icon {
    display: inline-flex;
    align-items: center;
    justify-content: center;
    flex-shrink: 0;
    pointer-events: none;
  }

  .btn.full-width {
    width: 100%;
    justify-content: center;
    text-align: center;
  }

  .btn.full-width .btn-label {
    justify-content: center;
    text-align: center;
  }

  .btn.full-width:not(:has(.icon-before)):not(:has(.icon-after)):not(:has(.spinner)) .btn-label {
    flex: 1;
  }

  .btn.full-width:has(.spinner) {
    justify-content: center;
  }
  .btn:disabled, .btn.is-disabled { opacity: 0.5; cursor: not-allowed; }
  .btn:focus-visible { outline: 2px solid var(--color-accent); outline-offset: 2px; }

  .size-sm {
    height: 28px;
    min-height: 28px;
    max-height: 28px;
    padding-block: 0;
    padding-inline: 0.625rem;
    font-size: 12px;
  }
  .size-md {
    height: 32px;
    min-height: 32px;
    max-height: 32px;
    padding-block: 0;
    padding-inline: 0.875rem;
    font-size: 13px;
  }

  .variant-primary {
    background: var(--color-accent);
    color: var(--color-accent-contrast, #ffffff);
  }
  .variant-primary:hover:not(:disabled):not(.is-disabled) {
    background: var(--color-accent-hover);
  }

  .variant-secondary {
    background: transparent;
    color: var(--color-text-primary);
    border-color: var(--color-border);
  }
  .variant-secondary:hover:not(:disabled):not(.is-disabled) {
    background: var(--color-bg-hover);
    border-color: var(--color-border-hover);
  }

  .variant-ghost {
    background: transparent;
    color: var(--color-text-secondary);
  }
  .variant-ghost:hover:not(:disabled):not(.is-disabled) {
    background: var(--color-bg-hover);
    color: var(--color-text-primary);
  }

  .variant-danger {
    background: var(--color-error);
    color: var(--color-error-contrast, #ffffff);
  }
  .variant-danger:hover:not(:disabled):not(.is-disabled) {
    filter: brightness(1.1);
  }

  .variant-success {
    background: var(--color-success);
    color: var(--color-success-contrast, #ffffff);
  }
  .variant-success:hover:not(:disabled):not(.is-disabled) {
    filter: brightness(1.1);
  }

  .variant-outline-danger {
    background: transparent;
    color: var(--color-error);
    border-color: var(--color-error-border);
  }
  .variant-outline-danger:hover:not(:disabled):not(.is-disabled) {
    background: var(--color-error-tint);
    border-color: var(--color-error);
  }

  .variant-outline-primary {
    background: transparent;
    color: var(--color-accent);
    border-color: var(--color-accent-border);
  }
  .variant-outline-primary:hover:not(:disabled):not(.is-disabled) {
    background: var(--color-accent-tint);
    border-color: var(--color-accent);
  }

  /* Keep the ring circular in tight flex rows (e.g. diagnostics group headers). */
  .btn:has(.spinner):not(.full-width) {
    flex-shrink: 0;
    min-width: max-content;
  }

  .spinner {
    width: 12px;
    height: 12px;
    flex-shrink: 0;
    border: 2px solid currentColor;
    border-top-color: transparent;
    border-radius: 50%;
    animation: spin 0.6s linear infinite;
  }

  @keyframes spin {
    to { transform: rotate(360deg); }
  }

  :global(.btn > svg),
  :global(.btn .icon > svg) {
    width: 14px;
    height: 14px;
    flex-shrink: 0;
  }
</style>
