<script lang="ts" module>
  import type { Snippet } from 'svelte';
  export type TabsVariant = 'underline' | 'pill';
</script>

<script lang="ts">
  import { setTabsContext } from './tabs-context';

  interface Props {
    value: string;
    onChange?: (value: string) => void;
    variant?: TabsVariant;
    children: Snippet;
  }

  let {
    value = $bindable(),
    onChange,
    variant = 'underline',
    children,
  }: Props = $props();

  setTabsContext({
    active: () => value,
    setActive: (v) => {
      value = v;
      onChange?.(v);
    },
  });
</script>

<div
  class="tabs"
  class:variant-underline={variant === 'underline'}
  class:variant-pill={variant === 'pill'}
  role="tablist"
>
  {@render children()}
</div>

<style>
  .tabs {
    display: flex;
    align-items: center;
  }

  .variant-underline {
    gap: 1.5rem;
    border-bottom: 1px solid var(--color-border);
  }

  .variant-pill {
    gap: 0.25rem;
    padding: 0.25rem;
    background: var(--color-bg-secondary);
    border: 1px solid var(--color-border);
    border-radius: var(--radius-sm);
    width: fit-content;
  }
</style>
