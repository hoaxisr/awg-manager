<script lang="ts" module>
  import type { Snippet } from 'svelte';
  export type BadgeVariant = 'default' | 'accent' | 'purple' | 'success' | 'error' | 'warning' | 'info' | 'muted' | 'dotted';
  export type BadgeSize = 'xs' | 'sm' | 'md';
</script>

<script lang="ts">
  interface Props {
    variant?: BadgeVariant;
    size?: BadgeSize;
    uppercase?: boolean;
    mono?: boolean;
    /** Fully rounded ends (like VersionBadge / AWG Kernel). */
    pill?: boolean;
    /** Optional native tooltip; rendered as the span's title attribute. */
    title?: string;
    /** Tighter horizontal padding (e.g. +N overflow chips). */
    compact?: boolean;
    children: Snippet;
  }

  let {
    variant = 'default',
    size = 'sm',
    uppercase = false,
    mono = false,
    pill = false,
    title,
    compact = false,
    children,
  }: Props = $props();
</script>

<span
  class="badge"
  class:variant-default={variant === 'default'}
  class:variant-accent={variant === 'accent'}
  class:variant-purple={variant === 'purple'}
  class:variant-success={variant === 'success'}
  class:variant-error={variant === 'error'}
  class:variant-warning={variant === 'warning'}
  class:variant-info={variant === 'info'}
  class:variant-muted={variant === 'muted'}
  class:variant-dotted={variant === 'dotted'}
  class:size-xs={size === 'xs'}
  class:size-sm={size === 'sm'}
  class:size-md={size === 'md'}
  class:is-uppercase={uppercase}
  class:is-mono={mono}
  class:is-pill={pill}
  class:is-compact={compact}
  {title}
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

  .size-xs { font-size: 10px; padding: 2px 6px; line-height: 1.2; border-radius: 3px; }
  .size-xs.is-compact { padding: 2px 2px; min-width: 0; }
  .size-sm { font-size: 11px; padding: 0.0625rem 0.375rem; }
  .size-md { font-size: 12px; padding: 0.125rem 0.4375rem; }

  .is-pill {
    border-radius: var(--radius-pill);
  }

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

  .variant-purple {
    background: color-mix(in srgb, var(--hue-violet) 14%, transparent);
    color: var(--hue-violet);
    border-color: color-mix(in srgb, var(--hue-violet) 36%, transparent);
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

  .variant-dotted {
    background: transparent;
    color: var(--color-text-muted);
    border-style: dotted;
    border-color: color-mix(in srgb, var(--color-text-muted) 40%, transparent);
    opacity: 0.68;
    cursor: pointer;
  }
</style>
