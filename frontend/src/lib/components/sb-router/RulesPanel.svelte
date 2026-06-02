<!--
  Источник дизайна: singbox-router/project/screens/MainBeginner.jsx
  (секция "MAIN: Routing rules as cards" + EmptyState для пустого случая)

  Reads singboxRouterStore: три отдельных readable — rules, ruleSets, outbounds
  (store НЕ имеет единого status-объекта, поля плоские).
  Маппит каждое правило через singboxRuleToCard в RuleCardData.
-->

<script lang="ts">
  import { onDestroy, onMount } from 'svelte';
  import { singboxRouter as singboxRouterStore } from '$lib/stores/singboxRouter';
  import { SectionLabel, Button, ConfirmModal } from '$lib/components/ui';
  import { openAddWizard } from './addWizardStore';
  import RuleCard from './RuleCard.svelte';
  import { isSystemRule, singboxRuleToCard } from './adapters';
  import RuleEditModal from '$lib/components/routing/singboxRouter/RuleEditModal.svelte';
  import { computeRuleSetUsage } from '$lib/components/routing/singboxRouter';
  import { api } from '$lib/api/client';
  import { notifications } from '$lib/stores/notifications';
  import { syncTunnelDnsRule } from './emptyStateActions';
  import type { RuleCardData } from './types';

  const rules = singboxRouterStore.rules;
  const ruleSets = singboxRouterStore.ruleSets;
  const outbounds = singboxRouterStore.outbounds;
  const presets = singboxRouterStore.presets;
  const options = singboxRouterStore.options;

  const rowElements = new Map<string, HTMLElement>();

  // Собираем rulesetLabels: tag → tag (у SingboxRouterRuleSet нет поля label,
  // только tag — используем его как отображаемое имя)
  let rulesetLabels: Record<string, string> = $derived.by(() => {
    const labels: Record<string, string> = {};
    for (const rs of $ruleSets) {
      if (rs.tag) labels[rs.tag] = rs.tag;
    }
    return labels;
  });

  let cards: RuleCardData[] = $derived.by(() =>
    $rules.map((r, i) => singboxRuleToCard(r, i, $outbounds, rulesetLabels, $presets, $options)),
  );

  let dragState = $state<null | {
    pointerId: number;
    fromIndex: number;
    cardId: string;
    startY: number;
    grabOffsetY: number;
    rect: DOMRect;
    started: boolean;
    handleEl: HTMLElement;
  }>(null);
  let insertionIndex = $state<number | null>(null);
  let draggingIndex = $state<number | null>(null);
  let dragGhostCard = $state<RuleCardData | null>(null);
  let dragGhostTop = $state(0);
  let dragGhostLeft = $state(0);
  let dragGhostWidth = $state(0);
  let measuredSlots = $state<Array<{ index: number; top: number; bottom: number; mid: number }>>([]);

  const DRAG_THRESHOLD = 7;

  let count = $derived(cards.length);
  function pluralRules(n: number): string {
    if (n === 1) return 'правило';
    if (n >= 2 && n <= 4) return 'правила';
    return 'правил';
  }

  let deleteIndex = $state<number | null>(null);
  let deleteTarget = $state<{ index: number; summary: string } | null>(null);
  let deleteBusy = $state(false);
  let editIndex = $state<number | null>(null);

  onMount(() => {
    void singboxRouterStore.loadAll();
  });

  onDestroy(() => {
    cleanupDrag();
  });

  function safeRuleSummary(card: RuleCardData | undefined, index: number): string {
    const n = String(index + 1).padStart(2, '0');
    if (!card) return `правило #${n}`;
    const target = card.action === 'block' || card.outbound.kind === 'block'
      ? 'Заблокировать'
      : card.outbound.kind === 'direct'
        ? 'Напрямую'
        : card.outbound.label;
    return `правило #${n}: ${card.title} → ${target}`;
  }

  function requestDelete(index: number) {
    const card = cards[index];
    if (!card) {
      notifications.error('Правило уже не найдено');
      deleteIndex = null;
      deleteTarget = null;
      return;
    }
    deleteIndex = index;
    deleteTarget = { index, summary: safeRuleSummary(card, index) };
  }

  function requestEdit(index: number) {
    if (isSystemRule($rules[index])) return;
    cancelDrag();
    editIndex = index;
  }

  async function confirmDelete() {
    if (deleteTarget === null) return;
    const target = deleteTarget;
    deleteBusy = true;
    try {
      await api.singboxRouterDeleteRule(target.index);
      deleteIndex = null;
      deleteTarget = null;
      try {
        await syncTunnelDnsRule();
      } catch (e) {
        notifications.error(`DNS sync: ${e instanceof Error ? e.message : String(e)}`);
      }
      await singboxRouterStore.loadAll();
      notifications.success('Правило удалено');
    } catch (e) {
      notifications.error(`Ошибка: ${e instanceof Error ? e.message : String(e)}`);
    } finally {
      deleteBusy = false;
    }
  }

  const editRuleSetUsage = $derived(
    editIndex === null ? new Map<string, number>() : computeRuleSetUsage($rules, editIndex),
  );

  async function handleEditSave(rule: (typeof $rules)[number]) {
    if (editIndex === null) return;
    try {
      await api.singboxRouterUpdateRule(editIndex, rule);
      try {
        await syncTunnelDnsRule();
      } catch (e) {
        notifications.error(`DNS sync: ${e instanceof Error ? e.message : String(e)}`);
      }
      await singboxRouterStore.loadAll();
      notifications.success('Правило обновлено');
      editIndex = null;
    } catch (e) {
      notifications.error(`Ошибка: ${e instanceof Error ? e.message : String(e)}`);
    }
  }

  function firstUserRuleIndex(): number {
    return cards.findIndex((c) => !c.isSystem);
  }

  function canMoveIndexToTarget(from: number, to: number): boolean {
    if (from === to) return false;
    if (from < 0 || from >= cards.length || to < 0 || to >= cards.length) return false;
    if (cards[from]?.isSystem || cards[to]?.isSystem) return false;
    const firstUser = firstUserRuleIndex();
    if (firstUser === -1) return false;
    return to >= firstUser;
  }

  function rowNode(node: HTMLElement, cardId: string) {
    rowElements.set(cardId, node);
    return {
      destroy() {
        if (rowElements.get(cardId) === node) {
          rowElements.delete(cardId);
        }
      },
    };
  }

  function cleanupDrag() {
    const current = dragState;
    if (current?.started && current.handleEl.hasPointerCapture?.(current.pointerId)) {
      current.handleEl.releasePointerCapture(current.pointerId);
    }
    dragState = null;
    insertionIndex = null;
    draggingIndex = null;
    dragGhostCard = null;
    measuredSlots = [];
    if (typeof document !== 'undefined') {
      document.body.classList.remove('sbr-dragging');
    }
    if (typeof window !== 'undefined') {
      window.removeEventListener('pointermove', onDragPointerMove);
      window.removeEventListener('pointerup', onDragPointerUp);
      window.removeEventListener('pointercancel', cancelDrag);
      window.removeEventListener('keydown', onDragKeyDown);
    }
  }

  function cancelDrag() {
    cleanupDrag();
  }

  function onDragKeyDown(event: KeyboardEvent) {
    if (event.key === 'Escape') {
      event.preventDefault();
      cancelDrag();
    }
  }

  function measureSlots() {
    const state = dragState;
    if (!state) return;
    const items: Array<{ index: number; top: number; bottom: number; mid: number }> = [];
    cards.forEach((card, idx) => {
      if (idx === state.fromIndex) return;
      if (card.isSystem) return;
      const el = rowElements.get(card.id);
      if (!el) return;
      const rect = el.getBoundingClientRect();
      items.push({ index: idx, top: rect.top, bottom: rect.bottom, mid: rect.top + rect.height / 2 });
    });
    items.sort((a, b) => a.top - b.top);
    measuredSlots = items;
  }

  function calculateInsertionIndex(y: number): number {
    if (!dragState) return 0;
    const from = dragState.fromIndex;
    if (!measuredSlots.length) return from;
    if (y < measuredSlots[0].mid) return measuredSlots[0].index;
    for (const slot of measuredSlots) {
      if (y < slot.mid) return slot.index;
    }
    return measuredSlots[measuredSlots.length - 1].index + 1;
  }

  function startDrag(event: PointerEvent) {
    if (!dragState) return;
    dragState.started = true;
    dragState.handleEl.setPointerCapture?.(dragState.pointerId);
    draggingIndex = dragState.fromIndex;
    dragGhostCard = cards[dragState.fromIndex] ?? null;
    dragGhostLeft = dragState.rect.left;
    dragGhostWidth = dragState.rect.width;
    dragGhostTop = event.clientY - dragState.grabOffsetY;
    insertionIndex = dragState.fromIndex;
    measureSlots();
    if (typeof document !== 'undefined') {
      document.body.classList.add('sbr-dragging');
    }
  }

  function onDragPointerMove(event: PointerEvent) {
    if (!dragState) return;
    if (event.pointerId !== dragState.pointerId) return;

    if (!dragState.started) {
      if (Math.abs(event.clientY - dragState.startY) < DRAG_THRESHOLD) return;
      startDrag(event);
    }

    event.preventDefault();
    dragGhostTop = event.clientY - dragState.grabOffsetY;
    const nextInsertion = calculateInsertionIndex(event.clientY);
    const firstMovable = firstUserRuleIndex();
    const normalized = firstMovable >= 0 ? Math.max(firstMovable, nextInsertion) : nextInsertion;
    if (normalized !== insertionIndex) insertionIndex = normalized;
  }

  function normalizeDropTarget(fromIndex: number, targetInsertion: number): number {
    let to = targetInsertion > fromIndex ? targetInsertion - 1 : targetInsertion;
    to = Math.max(0, Math.min(to, cards.length - 1));
    const firstMovable = firstUserRuleIndex();
    if (firstMovable >= 0 && to < firstMovable) to = firstMovable;
    return to;
  }

  async function onDragPointerUp(event: PointerEvent) {
    if (!dragState) return;
    if (event.pointerId !== dragState.pointerId) return;
    const state = dragState;
    const started = state.started;
    const fromIndex = state.fromIndex;
    const targetInsertion = insertionIndex ?? fromIndex;
    cleanupDrag();
    if (!started) return;
    const to = normalizeDropTarget(fromIndex, targetInsertion);
    if (to === fromIndex) return;
    if (!canMoveIndexToTarget(fromIndex, to)) return;
    try {
      await api.singboxRouterMoveRule(fromIndex, to);
      await singboxRouterStore.loadAll();
    } catch (e) {
      notifications.error(`Ошибка перемещения: ${e instanceof Error ? e.message : String(e)}`);
    }
  }

  function handleDragPointerDown(index: number, card: RuleCardData, event: PointerEvent) {
    event.preventDefault();
    event.stopPropagation();
    if (card.isSystem) return;
    if (deleteBusy || editIndex !== null || deleteTarget) return;
    if (event.button !== 0) return;
    const shell = rowElements.get(card.id);
    const handleEl = event.currentTarget as HTMLElement | null;
    if (!shell || !handleEl) return;
    const rect = shell.getBoundingClientRect();

    dragState = {
      pointerId: event.pointerId,
      fromIndex: index,
      cardId: card.id,
      startY: event.clientY,
      grabOffsetY: event.clientY - rect.top,
      rect,
      started: false,
      handleEl,
    };

    window.addEventListener('pointermove', onDragPointerMove);
    window.addEventListener('pointerup', onDragPointerUp);
    window.addEventListener('pointercancel', cancelDrag);
    window.addEventListener('keydown', onDragKeyDown);
  }

  function isDropBefore(index: number): boolean {
    if (!dragState?.started || insertionIndex === null) return false;
    return insertionIndex === index;
  }

  function isDropAtEnd(): boolean {
    if (!dragState?.started || insertionIndex === null) return false;
    return insertionIndex >= cards.length;
  }
</script>

<section class="rules-panel">
  <header class="panel-header">
    <div class="title-group">
      <h2 class="title">Что и куда отправлять</h2>
      <p class="sub">Правила применяются сверху вниз. Срабатывает первое подходящее.</p>
    </div>
    <div class="header-right">
      <div class="counter">
        {count} {pluralRules(count)}
      </div>
      <Button variant="secondary" size="sm" onclick={() => openAddWizard()}>
        + Правило
      </Button>
    </div>
  </header>

  {#if count === 0}
    <div class="empty">
      <SectionLabel>Пока нет правил</SectionLabel>
      <p class="empty-text">
        Создайте правило через <strong>Эксперт-режим</strong> → подвкладка «Rules».
        В будущих версиях здесь появится мастер создания.
      </p>
    </div>
  {:else}
    <div class="cards" class:is-dragging={dragState?.started} role="list">
      {#each cards as card, i (card.id)}
        <div
          data-rule-index={i}
          role="listitem"
          class="card-shell"
          class:drag-source={draggingIndex === i}
          use:rowNode={card.id}
        >
          {#if isDropBefore(i)}
            <div class="insert-line"></div>
          {/if}

          {#if draggingIndex === i && dragState?.started}
            <div class="drag-placeholder" style={`height:${dragState.rect.height}px`}></div>
          {:else}
            <RuleCard
              {card}
              index={i}
              dragging={draggingIndex === i}
              onEdit={() => requestEdit(i)}
              onDelete={() => requestDelete(i)}
              onDragHandlePointerDown={(e) => handleDragPointerDown(i, card, e)}
            />
          {/if}
        </div>
      {/each}
      {#if isDropAtEnd()}
        <div class="insert-line insert-line-end"></div>
      {/if}
    </div>
  {/if}
</section>

<ConfirmModal
  open={deleteIndex !== null}
  title="Удалить правило"
  message={deleteTarget ? `Удалить ${deleteTarget.summary}?` : ''}
  busy={deleteBusy}
  onConfirm={confirmDelete}
  onClose={() => { if (!deleteBusy) { deleteIndex = null; deleteTarget = null; } }}
/>

{#if editIndex !== null && $rules[editIndex]}
  <RuleEditModal
    rule={$rules[editIndex]}
    outboundOptions={$options}
    availableRuleSets={$ruleSets}
    ruleSetUsage={editRuleSetUsage}
    onClose={() => (editIndex = null)}
    onSave={handleEditSave}
  />
{/if}

{#if dragGhostCard && dragState?.started}
  <div
    class="drag-ghost"
    style={`top:${dragGhostTop}px;left:${dragGhostLeft}px;width:${dragGhostWidth}px;`}
  >
    <RuleCard card={dragGhostCard} index={dragState.fromIndex} dragging />
  </div>
{/if}

<style>
  .rules-panel {
    display: flex;
    flex-direction: column;
    gap: var(--sp-4);
    margin-top: var(--sp-4);
  }

  .panel-header {
    display: flex;
    align-items: flex-end;
    justify-content: space-between;
    gap: var(--sp-4);
  }

  .title-group {
    min-width: 0;
    flex: 1;
  }

  .title {
    margin: 0;
    font-size: var(--fs-h4);
    font-weight: 600;
    color: var(--text-primary);
    line-height: var(--lh-tight);
  }

  .sub {
    margin: 4px 0 0;
    font-size: var(--fs-sm);
    color: var(--text-muted);
    line-height: var(--lh-body);
  }

  .header-right {
    display: flex;
    align-items: center;
    gap: var(--sp-3);
    flex-shrink: 0;
  }

  .counter {
    font-size: var(--fs-sm);
    color: var(--text-muted);
    flex-shrink: 0;
  }

  .cards {
    display: flex;
    flex-direction: column;
    gap: 6px;
    min-width: 0;
  }
  .cards.is-dragging {
    user-select: none;
  }
  .card-shell {
    position: relative;
  }
  .insert-line {
    height: 2px;
    border-radius: 999px;
    background: var(--accent);
    box-shadow: 0 0 10px color-mix(in srgb, var(--accent) 45%, transparent);
    margin: 2px 0;
  }
  .insert-line-end {
    margin-top: 2px;
  }
  .drag-placeholder {
    border: 1px dashed var(--accent-line, var(--accent));
    border-radius: var(--radius);
    background: color-mix(in srgb, var(--accent) 6%, transparent);
  }
  .drag-ghost {
    position: fixed;
    z-index: 10000;
    pointer-events: none;
    transform: none;
    opacity: 0.96;
    filter: drop-shadow(0 14px 24px rgba(0,0,0,.35));
  }
  :global(body.sbr-dragging) {
    user-select: none;
    cursor: grabbing;
  }

  .empty {
    padding: var(--sp-5) var(--sp-4);
    background: var(--bg-secondary);
    border: 1px dashed var(--border);
    border-radius: var(--radius);
    text-align: center;
  }

  .empty-text {
    margin: var(--sp-3) auto 0;
    max-width: 480px;
    font-size: var(--fs-sm);
    color: var(--text-secondary);
    line-height: var(--lh-body);
  }
</style>
