<script lang="ts" module>
  import type { Snippet } from 'svelte';
  export type BadgeVariant = 'default' | 'accent' | 'success' | 'error' | 'warning' | 'info' | 'muted';
  export type BadgeSize = 'sm' | 'md';
</script>

<script lang="ts">
  interface Props {
    variant?: BadgeVariant;
    size?: BadgeSize;
    uppercase?: boolean;
    mono?: boolean;
    children: Snippet;
  }

  let {
    variant = 'default',
    size = 'sm',
    uppercase = false,
    mono = false,
    children,
  }: Props = $props();
</script>

<span
  class="badge"
  class:variant-default={variant === 'default'}
  class:variant-accent={variant === 'accent'}
  class:variant-success={variant === 'success'}
  class:variant-error={variant === 'error'}
  class:variant-warning={variant === 'warning'}
  class:variant-info={variant === 'info'}
  class:variant-muted={variant === 'muted'}
  class:size-sm={size === 'sm'}
  class:size-md={size === 'md'}
  class:is-uppercase={uppercase}
  class:is-mono={mono}
>
  {@render children()}
</span>

<style>
  .badge {
    display: inline-flex;
    align-items: center;
    gap: 0.25rem;
    padding: 0.125rem 0.4375rem;
    border-radius: var(--radius-sm);
    border: 1px solid transparent;
    font-weight: 500;
    line-height: 1.4;
    white-space: nowrap;
  }

  .size-sm { font-size: 11px; padding: 0.0625rem 0.375rem; }
  .size-md { font-size: 12px; padding: 0.125rem 0.4375rem; }

  .is-uppercase { text-transform: uppercase; letter-spacing: 0.04em; }
  .is-mono { font-family: var(--font-mono); }

  .variant-default {
    background: var(--color-bg-tertiary);
    color: var(--color-text-secondary);
    border-color: var(--color-border);
  }

  .variant-accent {
    background: var(--color-accent-tint);
    color: var(--color-accent);
    border-color: var(--color-accent-border);
  }

  .variant-success {
    background: var(--color-success-tint);
    color: var(--color-success);
    border-color: var(--color-success-border);
  }

  .variant-error {
    background: var(--color-error-tint);
    color: var(--color-error);
    border-color: var(--color-error-border);
  }

  .variant-warning {
    background: var(--color-warning-tint);
    color: var(--color-warning);
    border-color: var(--color-warning-border);
  }

  .variant-info {
    background: var(--color-info-tint);
    color: var(--color-info);
    border-color: var(--color-info-border);
  }

  .variant-muted {
    background: var(--color-muted-tint);
    color: var(--color-text-muted);
    border-color: var(--color-border);
  }
</style>
