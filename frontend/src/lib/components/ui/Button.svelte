<script lang="ts" module>
  import type { Snippet } from 'svelte';
  export type ButtonVariant = 'primary' | 'secondary' | 'ghost' | 'danger' | 'success';
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
    children,
  }: Props = $props();

  const isDisabled = $derived(disabled || loading);
</script>

{#if href}
  <a
    class="btn"
    class:variant-primary={variant === 'primary'}
    class:variant-secondary={variant === 'secondary'}
    class:variant-ghost={variant === 'ghost'}
    class:variant-danger={variant === 'danger'}
    class:variant-success={variant === 'success'}
    class:size-sm={size === 'sm'}
    class:size-md={size === 'md'}
    class:full-width={fullWidth}
    class:is-disabled={isDisabled}
    {href}
    {target}
    {rel}
    aria-disabled={isDisabled}
    aria-busy={loading}
    role="button"
    tabindex={isDisabled ? -1 : 0}
  >
    {#if loading}<span class="spinner" aria-hidden="true"></span>{:else if iconBefore}{@render iconBefore()}{/if}
    <span class="label">{@render children()}</span>
    {#if iconAfter && !loading}{@render iconAfter()}{/if}
  </a>
{:else}
  <button
    class="btn"
    class:variant-primary={variant === 'primary'}
    class:variant-secondary={variant === 'secondary'}
    class:variant-ghost={variant === 'ghost'}
    class:variant-danger={variant === 'danger'}
    class:variant-success={variant === 'success'}
    class:size-sm={size === 'sm'}
    class:size-md={size === 'md'}
    class:full-width={fullWidth}
    {type}
    disabled={isDisabled}
    aria-busy={loading}
    {onclick}
  >
    {#if loading}<span class="spinner" aria-hidden="true"></span>{:else if iconBefore}{@render iconBefore()}{/if}
    <span class="label">{@render children()}</span>
    {#if iconAfter && !loading}{@render iconAfter()}{/if}
  </button>
{/if}

<style>
  .btn {
    display: inline-flex;
    align-items: center;
    justify-content: center;
    gap: 0.4375rem;
    font-family: inherit;
    font-weight: 500;
    border-radius: var(--radius-sm);
    border: 1px solid transparent;
    cursor: pointer;
    transition: background var(--t-fast) ease, color var(--t-fast) ease,
                border-color var(--t-fast) ease, filter var(--t-fast) ease;
    text-decoration: none;
    user-select: none;
    white-space: nowrap;
  }

  .btn.full-width { width: 100%; }
  .btn:disabled, .btn.is-disabled { opacity: 0.5; cursor: not-allowed; }
  .btn:focus-visible { outline: 2px solid var(--color-accent); outline-offset: 2px; }

  .size-sm {
    height: 28px;
    padding: 0 0.625rem;
    font-size: 12px;
  }
  .size-md {
    height: 32px;
    padding: 0 0.875rem;
    font-size: 13px;
  }

  .variant-primary {
    background: var(--color-accent);
    color: white;
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
    color: white;
  }
  .variant-danger:hover:not(:disabled):not(.is-disabled) {
    filter: brightness(1.1);
  }

  .variant-success {
    background: var(--color-success);
    color: white;
  }
  .variant-success:hover:not(:disabled):not(.is-disabled) {
    filter: brightness(1.1);
  }

  .spinner {
    width: 12px;
    height: 12px;
    border: 2px solid currentColor;
    border-top-color: transparent;
    border-radius: 50%;
    animation: spin 0.6s linear infinite;
  }

  @keyframes spin {
    to { transform: rotate(360deg); }
  }

  :global(.btn > svg) {
    width: 14px;
    height: 14px;
    flex-shrink: 0;
  }
</style>
