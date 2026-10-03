<script lang="ts">
  import type { ProxyGroup } from '$lib/types/sbRouter';
  import { Edit3, Trash2 } from 'lucide-svelte';
  import { Button } from '$lib/components/ui';

  interface Props {
    groups: ProxyGroup[];
    onEdit: (name: string) => void;
    onDelete?: (name: string) => void;
  }

  let {
    groups,
    onEdit,
    onDelete,
  }: Props = $props();

  function toneFor(type: string): 'success' | 'accent' | 'info' | 'muted' | 'error' {
    return 'accent';
  }
</script>

<div class="list">
  {#each groups as g (g.name)}
    <div class="row">
      <span class="dot" data-tone={toneFor(g.type)}></span>
      <button
        type="button"
        class="meta-btn"
        onclick={() => onEdit(g.name)}
      >
        <div class="meta">
          <div class="tag">{g.name}</div>
          <div class="sub">Тип: {g.type} · Участников: {g.proxies.length}</div>
        </div>
      </button>

      <div class="actions">
        <button type="button" class="action-btn" onclick={() => onEdit(g.name)} title="Редактировать">
          <Edit3 size={14} aria-hidden="true" />
        </button>
        {#if onDelete}
          <button type="button" class="action-btn text-error-500" onclick={() => onDelete(g.name)} title="Удалить">
            <Trash2 size={14} aria-hidden="true" />
          </button>
        {/if}
      </div>
    </div>
  {:else}
    <div class="empty">Нет настроенных proxy-групп</div>
  {/each}
</div>

<style>
  .list {
    display: flex;
    flex-direction: column;
  }
  .row {
    transition: background-color 0.15s ease;
    display: grid;
    grid-template-columns: 6px minmax(0, 1fr) auto auto;
    align-items: center;
    gap: 10px;
    padding: 8px 14px;
    border-bottom: 1px solid rgba(255, 255, 255, 0.04);
  }
  @media (hover: hover) and (pointer: fine) {
    .row:hover {
      background: color-mix(in srgb, var(--bg-hover) 70%, transparent);
    }
  }
  .dot {
    width: 6px;
    height: 6px;
    border-radius: 50%;
    background: var(--text-muted);
    flex-shrink: 0;
  }
  .dot[data-tone="accent"] { background: var(--accent); }

  .meta-btn {
    min-width: 0;
    padding: 0;
    border: 0;
    background: transparent;
    color: inherit;
    font: inherit;
    text-align: left;
    cursor: pointer;
  }
  .meta {
    flex: 1;
    min-width: 0;
  }
  .tag {
    font-family: var(--font-mono);
    font-size: 12px;
    font-weight: 600;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  .sub {
    font-size: 11px;
    color: var(--text-muted);
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }

  .actions {
    display: inline-flex;
    align-items: center;
    gap: 4px;
  }
  .action-btn {
    display: inline-flex;
    align-items: center;
    justify-content: center;
    width: 24px;
    height: 24px;
    background: transparent;
    border: none;
    color: var(--text-muted);
    cursor: pointer;
    border-radius: 4px;
    transition: all 0.2s;
  }
  .action-btn:hover {
    background: rgba(255, 255, 255, 0.1);
    color: var(--text-primary);
  }
  .action-btn.text-error-500:hover {
    color: var(--color-error, #dc2626);
    background: rgba(220, 38, 38, 0.1);
  }

  .empty {
    padding: 14px;
    color: var(--text-muted);
    text-align: center;
    font-size: 12px;
  }
</style>
