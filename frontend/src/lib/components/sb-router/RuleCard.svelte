<!--
  Источник дизайна: singbox-router/project/parts/RuleCard.jsx (RuleCard)
  Grid: order(28) | main(1fr) | arrow+outbound(auto) | system_badge(auto, опц.)
  В F2 НЕ рендерим: drag handle (F5), edit/menu кнопки (F5).
-->

<script lang="ts">
  import type { MatcherChip as MatcherChipData, RuleCardData } from './types';
  import ServiceTile from './ServiceTile.svelte';
  import MatcherChip from './MatcherChip.svelte';
  import RuleOutboundAction from './RuleOutboundAction.svelte';
  import { Badge } from '$lib/components/ui';
  import { Edit3, GripVertical, Lock, Trash2 } from 'lucide-svelte';
  import { outboundDisplayTitle } from './outboundLabelFormat';
  import { COMPLEX_RULE_EDIT_MESSAGE } from './simpleRule';

  interface Props {
    card: RuleCardData;
    /** 0-based index — отображается как 01/02/... */
    index: number;
    onDelete?: () => void;
    onEdit?: () => void;
    onRulesetClick?: (tag: string) => void;
    onTextMatchersClick?: () => void;
    onInlineListClick?: () => void;
    knownRulesetTags?: Set<string>;
    onDragHandlePointerDown?: (event: PointerEvent) => void;
    dragging?: boolean;
    dragDisabled?: boolean;
  }
  let {
    card,
    index,
    onDelete,
    onEdit,
    onRulesetClick,
    onTextMatchersClick,
    onInlineListClick,
    knownRulesetTags,
    onDragHandlePointerDown,
    dragging = false,
    dragDisabled = false,
  }: Props = $props();

  const MAX_CHIPS = 4;
  let visibleChips = $derived(card.matchers.slice(0, MAX_CHIPS));
  let hiddenCount = $derived(Math.max(0, card.matchers.length - MAX_CHIPS));
  let orderStr = $derived(String(index).padStart(2, '0'));
  let useServiceTile = $derived(!card.isSystem);
  let editTip = $derived(actionTooltip('edit', card, index));
  let deleteTip = $derived(actionTooltip('delete', card, index));
  let editDisabled = $derived(!card.isSystem && !card.simplicity.simple);
  let editDisabledTip = $derived(editDisabled ? COMPLEX_RULE_EDIT_MESSAGE : editTip);

  const isCompositeOutbound = $derived(
    (card.outbound.kind === 'composite' || card.outbound.kind === 'subscription')
      && !!card.outbound.activeMemberLabel,
  );

  function outboundLabel(cardData: RuleCardData): string {
    if (cardData.action === 'block' || cardData.outbound.kind === 'block') return 'Заблокировать';
    if (cardData.outbound.kind === 'direct') return 'Напрямую';
    if (
      (cardData.outbound.kind === 'composite' || cardData.outbound.kind === 'subscription')
      && cardData.outbound.activeMemberLabel
    ) {
      return `${outboundDisplayTitle(cardData.outbound)} → ${cardData.outbound.activeMemberLabel}`;
    }
    return outboundDisplayTitle(cardData.outbound);
  }

  function ruleActionTarget(cardData: RuleCardData, idx: number): string {
    const n = String(idx + 1).padStart(2, '0');
    return `правило #${n}: ${cardData.title} → ${outboundLabel(cardData)}`;
  }

  function actionTooltip(action: 'edit' | 'delete', cardData: RuleCardData, idx: number): string {
    const prefix = action === 'edit' ? 'Редактировать' : 'Удалить';
    return `${prefix} ${ruleActionTarget(cardData, idx)}`;
  }

  function chipOnclick(chip: MatcherChipData): (() => void) | undefined {
    const sim = card.simplicity;
    // Развёрнутые домены/IP custom-N — тот же редактор набора, что и по бейджу inline.
    if (sim.simple && sim.kind === 'inline-set') {
      if ((chip.kind === 'domain' || chip.kind === 'ip') && onInlineListClick) {
        return onInlineListClick;
      }
    }
    if (sim.simple && sim.kind === 'inline-text') {
      if ((chip.kind === 'domain' || chip.kind === 'ip') && onTextMatchersClick) {
        return onTextMatchersClick;
      }
    }
    if (chip.kind === 'ruleset' && chip.rulesetTag && onRulesetClick && knownRulesetTags?.has(chip.rulesetTag)) {
      return () => onRulesetClick(chip.rulesetTag!);
    }
    return undefined;
  }

  function chipTitle(chip: MatcherChipData): string | undefined {
    const sim = card.simplicity;
    if (sim.simple && sim.kind === 'inline-set' && onInlineListClick) {
      if (chip.kind === 'domain' || chip.kind === 'ip') {
        return 'Редактировать набор';
      }
    }
    if (sim.simple && sim.kind === 'inline-text' && onTextMatchersClick) {
      if (chip.kind === 'domain' || chip.kind === 'ip') {
        return 'Редактировать домены и адреса';
      }
    }
    if (chip.kind === 'ruleset' && chip.rulesetTag && onRulesetClick && knownRulesetTags?.has(chip.rulesetTag)) {
      return `Редактировать набор «${chip.label}»`;
    }
    return undefined;
  }
</script>

<div class="card-wrap" title={card.isSystem ? card.tooltip : undefined}>
<div class="card" class:is-system={card.isSystem} class:dragging>
  <!-- Order number -->
  <div class="order">{orderStr}</div>

  <div class="drag-slot">
    {#if !card.isSystem}
      <button
        type="button"
        class="drag-handle"
        class:is-busy={dragDisabled}
        aria-disabled={dragDisabled}
        aria-label={`Перетащить правило #${orderStr}`}
        title={dragDisabled ? 'Подождите, правило перемещается…' : `Перетащить правило #${orderStr}`}
        onpointerdown={dragDisabled ? undefined : onDragHandlePointerDown}
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
          <Lock size={16} aria-hidden={true} />
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
          <MatcherChip
            kind={chip.kind}
            label={chip.label}
            mono={chip.mono}
            rulesetType={chip.rulesetType}
            onclick={chipOnclick(chip)}
            title={chipTitle(chip)}
          />
        {/each}
        {#if hiddenCount > 0}
          <span class="more">+{hiddenCount} ещё</span>
        {/if}
      </div>
    {/if}
  </div>

  <div class="trail">
    <div class="action" class:composite={isCompositeOutbound}>
      <RuleOutboundAction outbound={card.outbound} />
    </div>

    {#if card.isSystem}
      <div class="right-slot">
        <Badge variant="muted" size="sm">система</Badge>
      </div>
    {:else if onDelete || onEdit}
      <div class="right-slot">
        {#if onEdit}
          <span class="action-tip" data-tip={editDisabledTip}>
            <button
              type="button"
              class="route-action-btn"
              class:is-disabled={editDisabled}
              onclick={editDisabled ? undefined : onEdit}
              disabled={editDisabled}
              aria-label={editDisabledTip}
              title={editDisabledTip}
            >
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
</div>

<style>
  .card-wrap {
    position: relative;
    min-width: 0;
  }
  .card {
    display: grid;
    grid-template-columns: 28px 28px minmax(0, 1fr) auto;
    gap: 12px;
    align-items: center;
    padding: 10px 14px;
    background: var(--bg-secondary);
    border: 1px solid var(--border);
    border-radius: var(--radius);
    transition: border-color var(--t-fast);
    min-width: 0;
  }
  .card:hover { border-color: var(--border-hover); }
  .card.is-system {
    opacity: 0.6;
  }
  .card.is-system:hover {
    border-color: var(--border);
  }
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
    opacity: 1;
    transition:
      color 0.28s ease,
      opacity 0.28s ease;
  }
  .drag-handle:hover {
    color: var(--text-primary);
  }
  .drag-handle:active { cursor: grabbing; }
  .drag-handle.is-busy {
    cursor: wait;
    opacity: 0.42;
    pointer-events: none;
  }
  .drag-handle.is-busy:hover {
    color: var(--text-muted);
  }
  .handle-disabled {
    width: 20px;
    height: 20px;
    flex-shrink: 0;
  }
  .main {
    display: flex;
    flex-wrap: wrap;
    align-items: center;
    gap: 8px 12px;
    min-width: 0;
    overflow: hidden;
  }

  .main :global(.service-tile) {
    min-width: 0;
    max-width: 100%;
    overflow: hidden;
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
    flex: 0 1 auto;
    max-width: 100%;
    overflow: hidden;
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

  .trail {
    display: flex;
    align-items: center;
    gap: 8px;
    min-width: 0;
    max-width: 100%;
    justify-content: flex-end;
  }

  .action {
    display: flex;
    align-items: center;
    gap: 10px;
    flex-shrink: 1;
    min-width: 0;
    max-width: 100%;
  }
  .action.composite {
    flex-shrink: 1;
    min-width: 0;
    max-width: 100%;
  }

  @media (min-width: 769px) {
    .action.composite {
      max-width: min(40vw, 100%);
    }
  }
  .action :global(.tile),
  .action :global(.tone-chip) {
    min-width: 0;
    max-width: 11rem;
  }
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
  .route-action-btn.is-disabled {
    opacity: 0.35;
    cursor: not-allowed;
  }
  .route-action-btn.is-disabled:hover {
    color: var(--text-muted);
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

    .trail {
      display: contents;
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
      max-width: 100%;
      display: flex;
      align-items: center;
      gap: 8px;
      border-top: 1px dashed var(--border);
      padding-top: 8px;
      margin-top: 2px;
      justify-content: flex-start;
    }

    .action.composite {
      width: 100%;
      max-width: 100%;
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
