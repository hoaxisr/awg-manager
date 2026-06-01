<!--
  Источник дизайна: singbox-router/project/parts/RuleCard.jsx (RuleCard)
  Grid: order(28) | main(1fr) | arrow+outbound(auto) | system_badge(auto, опц.)
  В F2 НЕ рендерим: drag handle (F5), edit/menu кнопки (F5).
-->

<script lang="ts">
  import type { RuleCardData } from './types';
  import ServiceTile from './ServiceTile.svelte';
  import MatcherChip from './MatcherChip.svelte';
  import OutboundTile from './OutboundTile.svelte';
  import { Badge } from '$lib/components/ui';
  import { Edit3, GripVertical, Trash2 } from 'lucide-svelte';

  interface Props {
    card: RuleCardData;
    /** 0-based index — отображается как 01/02/... */
    index: number;
    onDelete?: () => void;
    onEdit?: () => void;
    onDragHandlePointerDown?: (event: PointerEvent) => void;
    dragging?: boolean;
  }
  let {
    card,
    index,
    onDelete,
    onEdit,
    onDragHandlePointerDown,
    dragging = false,
  }: Props = $props();

  const MAX_CHIPS = 4;
  let visibleChips = $derived(card.matchers.slice(0, MAX_CHIPS));
  let hiddenCount = $derived(Math.max(0, card.matchers.length - MAX_CHIPS));
  let orderStr = $derived(String(index + 1).padStart(2, '0'));
  let useServiceTile = $derived(!card.isSystem);
  let editTip = $derived(actionTooltip('edit', card, index));
  let deleteTip = $derived(actionTooltip('delete', card, index));

  function outboundLabel(cardData: RuleCardData): string {
    if (cardData.action === 'block' || cardData.outbound.kind === 'block') return 'Заблокировать';
    if (cardData.outbound.kind === 'direct') return 'Напрямую';
    return cardData.outbound.label;
  }

  function ruleActionTarget(cardData: RuleCardData, idx: number): string {
    const n = String(idx + 1).padStart(2, '0');
    return `правило #${n}: ${cardData.title} → ${outboundLabel(cardData)}`;
  }

  function actionTooltip(action: 'edit' | 'delete', cardData: RuleCardData, idx: number): string {
    const prefix = action === 'edit' ? 'Редактировать' : 'Удалить';
    return `${prefix} ${ruleActionTarget(cardData, idx)}`;
  }
</script>

<div class="card-wrap">
<div class="card" class:is-system={card.isSystem} class:dragging>
  <!-- Order number -->
  <div class="order">{orderStr}</div>

  <div class="drag-slot">
    {#if !card.isSystem}
      <button
        type="button"
        class="drag-handle"
        aria-label={`Перетащить правило #${orderStr}`}
        title={`Перетащить правило #${orderStr}`}
        onpointerdown={onDragHandlePointerDown}
      >
        <GripVertical size={16} />
      </button>
    {:else}
      <div class="handle-disabled" aria-hidden="true"></div>
    {/if}
  </div>

  <!-- Service tile or generic icon-tile + matchers -->
  <div class="main">
    {#if useServiceTile}
      <ServiceTile serviceKey={card.serviceKey} name={card.title} sub={card.subtitle} />
    {:else}
      <!-- System rule: Lock icon -->
      <div class="generic-tile">
        <div class="icon-box is-system">
          <svg viewBox="0 0 24 24" width="16" height="16" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">
            <rect x="3" y="11" width="18" height="11" rx="2" />
            <path d="M7 11V7a5 5 0 0 1 10 0v4" />
          </svg>
        </div>
        <div class="text">
          <div class="title">{card.title}</div>
          {#if card.subtitle}<div class="subtitle">{card.subtitle}</div>{/if}
        </div>
      </div>
    {/if}

    {#if !card.isSystem && visibleChips.length > 0}
      <div class="chips">
        {#each visibleChips as chip}
          <MatcherChip kind={chip.kind} label={chip.label} mono={chip.mono} />
        {/each}
        {#if hiddenCount > 0}
          <span class="more">+{hiddenCount} ещё</span>
        {/if}
      </div>
    {/if}
  </div>

  <!-- Arrow + outbound tile -->
  <div class="action">
    <svg class="arrow" viewBox="0 0 24 24" width="14" height="14" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">
      <line x1="5" y1="12" x2="19" y2="12" />
      <polyline points="12 5 19 12 12 19" />
    </svg>
    <OutboundTile outbound={card.outbound} />
  </div>

  <!-- System badge -->
  {#if card.isSystem}
    <div class="right-slot">
      <Badge variant="muted" size="sm">система</Badge>
    </div>
  {:else if onDelete || onEdit}
    <div class="right-slot">
      {#if onEdit}
        <span class="action-tip" data-tip={editTip}>
          <button type="button" class="route-action-btn" onclick={onEdit} aria-label={editTip} title={editTip}>
            <Edit3 size={15} />
          </button>
        </span>
      {/if}
      <span class="action-tip" data-tip={deleteTip}>
        <button type="button" class="route-action-btn danger" onclick={onDelete} aria-label={deleteTip} title={deleteTip}>
          <Trash2 size={15} />
        </button>
      </span>
    </div>
  {/if}
</div>
</div>

<style>
  .card-wrap {
    position: relative;
  }
  .card {
    display: grid;
    grid-template-columns: 28px 28px minmax(0, 1fr) auto auto;
    gap: 12px;
    align-items: center;
    padding: 10px 14px;
    background: var(--bg-secondary);
    border: 1px solid var(--border);
    border-radius: var(--radius);
    transition: border-color var(--t-fast);
  }
  .card:hover { border-color: var(--border-hover); }
  .card.dragging {
    border-color: color-mix(in srgb, var(--accent) 55%, var(--border));
    box-shadow: 0 4px 14px rgba(0, 0, 0, 0.24);
    opacity: 0.82;
    transform: translateY(-1px);
  }
  .order {
    font-family: var(--font-mono);
    font-size: 12px;
    font-weight: 600;
    color: var(--text-secondary);
    text-align: center;
  }
  .is-system .order { color: var(--text-muted); }

  .drag-slot {
    display: flex;
    flex-direction: column;
    align-items: center;
    gap: 2px;
  }
  .drag-handle {
    background: transparent;
    border: none;
    color: var(--text-muted);
    padding: 2px;
    cursor: grab;
    display: inline-flex;
    align-items: center;
    justify-content: center;
    touch-action: none;
    border-radius: 4px;
    transition: color 0.15s;
  }
  .drag-handle:hover {
    color: var(--text-primary);
  }
  .drag-handle:active { cursor: grabbing; }
  .handle-disabled {
    width: 20px;
    height: 20px;
    flex-shrink: 0;
  }
  .main {
    display: flex;
    align-items: center;
    gap: 12px;
    min-width: 0;
  }

  .generic-tile {
    display: flex;
    align-items: center;
    gap: 10px;
    min-width: 0;
  }
  .icon-box {
    width: 32px;
    height: 32px;
    border-radius: 8px;
    background: var(--accent-soft);
    color: var(--accent);
    display: flex;
    align-items: center;
    justify-content: center;
    flex-shrink: 0;
  }
  .icon-box.is-system {
    background: rgba(255, 255, 255, 0.04);
    color: var(--text-muted);
  }
  .text { min-width: 0; line-height: 1.2; }
  .title {
    font-weight: 600;
    font-size: 14px;
    color: var(--text-primary);
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  .is-system .title { color: var(--text-secondary); }
  .subtitle {
    font-size: 11px;
    color: var(--text-muted);
    margin-top: 2px;
  }

  .chips {
    display: flex;
    gap: 4px;
    flex-wrap: wrap;
    min-width: 0;
  }
  .more {
    display: inline-flex;
    align-items: center;
    padding: 2px 7px;
    border-radius: 4px;
    background: transparent;
    border: 1px dashed var(--border);
    color: var(--text-muted);
    font-size: 10px;
    line-height: 1.4;
  }

  .action {
    display: flex;
    align-items: center;
    gap: 10px;
  }
  .arrow { color: var(--text-muted); }

  .right-slot {
    display: flex;
    gap: 4px;
    flex-shrink: 0;
    overflow: visible;
    position: relative;
  }
  .action-tip {
    position: relative;
    display: inline-flex;
  }
  .action-tip:hover::after,
  .action-tip:focus-within::after {
    content: attr(data-tip);
    position: absolute;
    right: 0;
    bottom: calc(100% + 8px);
    width: max-content;
    max-width: 320px;
    white-space: normal;
    font-size: 11px;
    line-height: 1.35;
    color: var(--text-primary);
    background: color-mix(in srgb, var(--bg-tertiary) 90%, var(--bg-secondary));
    border: 1px solid var(--border);
    border-radius: var(--radius-sm);
    box-shadow: 0 6px 16px rgba(0, 0, 0, 0.3);
    padding: 6px 8px;
    z-index: 10;
    pointer-events: none;
  }

  .action :global(.badge) {
    max-width: 100%;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }

  /* ── Mobile ── */
  @media (max-width: 768px) {
    .card {
      grid-template-columns: 28px minmax(0, 1fr) auto;
      grid-template-areas:
        "drag main right"
        "order main right"
        "action action action";
      align-items: start;
      gap: 8px 10px;
      padding: 10px 12px;
    }

    .drag-slot {
      grid-area: drag;
      width: 28px;
      min-width: 28px;
      height: 28px;
      display: flex;
      align-items: center;
      justify-content: center;
    }

    .drag-handle {
      width: 28px;
      height: 28px;
      padding: 0;
      border-radius: 8px;
    }

    .order {
      grid-area: order;
      width: 28px;
      text-align: center;
      padding-top: 0;
      font-size: 11px;
      line-height: 1;
    }

    .main {
      grid-area: main;
      min-width: 0;
      display: flex;
      flex-direction: column;
      align-items: stretch;
      gap: 8px;
      flex-wrap: nowrap;
    }

    .main :global(.service-tile),
    .generic-tile {
      min-width: 0;
    }

    .chips {
      width: 100%;
      display: flex;
      flex-wrap: wrap;
      gap: 5px;
      min-width: 0;
    }

    .right-slot {
      grid-area: right;
      align-self: start;
      display: flex;
      gap: 6px;
      padding-top: 0;
    }

    .action {
      grid-area: action;
      min-width: 0;
      display: flex;
      align-items: center;
      gap: 8px;
      border-top: 1px dashed var(--border);
      padding-top: 8px;
      margin-top: 2px;
    }

    .card.is-system {
      grid-template-columns: 28px minmax(0, 1fr) auto;
      grid-template-areas:
        "order main right"
        "action action action";
    }

    .card.is-system .drag-slot {
      display: none;
    }
  }
</style>
