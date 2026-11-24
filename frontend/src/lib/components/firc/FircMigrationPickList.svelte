<script lang="ts" module>
  import type { BadgeVariant } from '$lib/components/ui';

  export interface PickItem {
    id: string;
    title: string;
    sub: string;
    badges: { variant: BadgeVariant; text: string }[];
    pill?: string;
  }

  export interface PickFilter {
    label: string;
    run: () => void;
  }
</script>

<script lang="ts">
  // Список выбора туннелей/групп мастера миграции: заголовок со счётчиком,
  // быстрые фильтры и строки-переключатели.
  import { Badge, Button } from '$lib/components/ui';
  import { Check } from 'lucide-svelte';

  interface Props {
    label: string;
    filters: PickFilter[];
    items: PickItem[];
    selected: string[];
    empty: string;
    disabled: boolean;
    ontoggle: (id: string) => void;
  }

  let { label, filters, items, selected, empty, disabled, ontoggle }: Props = $props();
</script>

<div class="wiz-box">
  <div class="sec-head">
    <span class="lbl">{label}</span>
    <div class="filter-btns">
      {#each filters as f (f.label)}
        <Button variant="secondary" size="sm" {disabled} onclick={f.run}>{f.label}</Button>
      {/each}
    </div>
  </div>

  {#if items.length === 0}
    <div class="hint">{empty}</div>
  {:else}
    <div class="item-list">
      {#each items as item (item.id)}
        {@const checked = selected.includes(item.id)}
        <button type="button" class="list-item" class:selected={checked} {disabled} onclick={() => ontoggle(item.id)}>
          <span class="chk" class:checked>
            {#if checked}<Check size={11} strokeWidth={3} />{/if}
          </span>
          <span class="item-main">
            <strong>{item.title}</strong>
            <span class="item-sub mono">{item.sub}</span>
          </span>
          <span class="item-badges">
            {#each item.badges as b (b.text)}
              <Badge variant={b.variant} size="sm">{b.text}</Badge>
            {/each}
            {#if item.pill}
              <span class="mono-pill">{item.pill}</span>
            {/if}
          </span>
        </button>
      {/each}
    </div>
  {/if}
</div>

<style>
  .filter-btns {
    display: flex;
    gap: 4px;
    flex-wrap: wrap;
  }

  .item-list {
    display: flex;
    flex-direction: column;
    gap: 4px;
    max-height: 165px;
    overflow-y: auto;
  }

  .list-item {
    display: flex;
    align-items: center;
    gap: 8px;
    width: 100%;
    text-align: left;
    padding: 6px 8px;
    border-radius: 6px;
    border: 1px solid var(--border);
    background: var(--bg-tertiary);
    color: var(--text-primary);
    font-size: 12px;
    cursor: pointer;
    box-sizing: border-box;
  }

  .list-item.selected {
    background: color-mix(in srgb, var(--color-accent) 10%, var(--bg-tertiary));
    border-color: color-mix(in srgb, var(--color-accent) 32%, var(--border));
  }

  .chk {
    width: 15px;
    height: 15px;
    border-radius: 4px;
    border: 1px solid var(--border);
    background: var(--bg-secondary);
    display: inline-flex;
    align-items: center;
    justify-content: center;
    color: transparent;
    flex-shrink: 0;
  }

  .chk.checked {
    background: color-mix(in srgb, var(--color-accent) 18%, var(--bg-tertiary));
    border-color: var(--color-accent);
    color: var(--color-accent);
  }

  .item-main {
    display: flex;
    flex-direction: column;
    gap: 2px;
    flex: 1 1 auto;
    min-width: 0;
    overflow: hidden;
  }

  .item-main strong {
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }

  .item-sub {
    font-size: 10.5px;
    color: var(--text-secondary);
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }

  .item-badges {
    display: inline-flex;
    align-items: center;
    gap: 5px;
    flex-shrink: 0;
  }

  .mono-pill {
    font-family: var(--font-mono);
    font-size: 10.5px;
    color: var(--text-muted);
    background: var(--bg-secondary);
    padding: 1px 5px;
    border-radius: 4px;
  }
</style>
