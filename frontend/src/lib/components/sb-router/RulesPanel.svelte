<!--
  Источник дизайна: singbox-router/project/screens/MainBeginner.jsx
  (секция "MAIN: Routing rules as cards" + EmptyState для пустого случая)

  Reads singboxRouterStore: три отдельных readable — rules, ruleSets, outbounds
  (store НЕ имеет единого status-объекта, поля плоские).
  Маппит каждое правило через singboxRuleToCard в RuleCardData.
-->

<script lang="ts">
  import { onDestroy, onMount } from 'svelte';
  import { get } from 'svelte/store';
  import { singboxRouter as singboxRouterStore } from '$lib/stores/singboxRouter';
  import { SectionLabel, Button, ConfirmModal } from '$lib/components/ui';
  import { openAddWizard, openEditWizard } from './addWizardStore';
  import RuleCard from './RuleCard.svelte';
  import { isSystemRule, singboxRuleToCard } from './adapters';
  import { classifyRuleSimplicity } from './simpleRule';
  import { prefillWizardFromRule } from './ruleWizardPrefill';
  import { setTemplateSelection } from './templatesStore';
  import { presetCatalog } from '$lib/stores/presets';
  import { subscriptionsStore } from '$lib/stores/subscriptions';
  import { singboxProxies } from '$lib/stores/singboxProxies';
  import { singboxTunnels } from '$lib/stores/singbox';
  import RuleEditModal from '$lib/components/routing/singboxRouter/RuleEditModal.svelte';
  import RuleSetAddModal from '$lib/components/routing/singboxRouter/RuleSetAddModal.svelte';
  import type { SingboxRouterRule, SingboxRouterRuleSet } from '$lib/types';
  import { api } from '$lib/api/client';
  import { notifications } from '$lib/stores/notifications';
  import { syncTunnelDnsRule } from './emptyStateActions';
  import { pluralize, RULE_WORDS } from '$lib/utils/pluralize';
  import { displayRuleSetTag } from '$lib/utils/singboxInlineRules';
  import { findScrollContainer } from '$lib/utils/findScrollContainer';
  import type { RuleCardData } from './types';

  const rules = singboxRouterStore.rules;
  const ruleUiKeys = singboxRouterStore.ruleUiKeys;
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
      if (rs.tag) labels[rs.tag] = displayRuleSetTag(rs.tag);
    }
    return labels;
  });

  const knownRulesetTags = $derived(new Set($ruleSets.map((rs) => rs.tag).filter(Boolean)));

  let cards: RuleCardData[] = $derived.by(() =>
    $rules.map((r, i) =>
      singboxRuleToCard(
        r,
        i,
        $outbounds,
        rulesetLabels,
        $presets,
        $options,
        $presetCatalog,
        $ruleSets,
        $subscriptionsStore.data,
        $singboxProxies.data ?? [],
        $singboxTunnels.data ?? [],
        $ruleUiKeys[i],
      ),
    ),
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
  let panelEl = $state<HTMLElement | null>(null);
  let dropAt = $state<number | 'end' | null>(null);
  let dropExpanded = $state(false);
  let collapsingDropAt = $state<number | 'end' | null>(null);
  let collapsingWasExpanded = $state(false);
  let collapsePhaseActive = $state(false);

  let scrollContainer: HTMLElement | null = null;
  let lastPointerY = 0;
  let autoScrollRaf: number | null = null;
  let dropSkeletonTimer: ReturnType<typeof setTimeout> | null = null;
  let collapseDropTimer: ReturnType<typeof setTimeout> | null = null;
  let sourceExitCollapsed = $state(false);
  let hasMovedFromSource = $state(false);
  let dropCommitPending = $state(false);
  let dropCommitTimer: ReturnType<typeof setTimeout> | null = null;
  let moveInFlight = $state(false);

  const DRAG_THRESHOLD = 7;
  const SCROLL_EDGE = 84;
  const SCROLL_MAX_SPEED = 14;
  const DROP_SKELETON_DELAY_MS = 680;
  const DROP_SLOT_MOTION_MS = 360;
  const DROP_LINE_COLLAPSE_MS = 240;
  const SLOT_EASE = 'cubic-bezier(0.45, 0.05, 0.55, 0.95)';
  const CARD_GAP = 6;

  let count = $derived(cards.length);

  let deleteIndex = $state<number | null>(null);
  let deleteTarget = $state<{ index: number; summary: string } | null>(null);
  let deleteBusy = $state(false);
  let textMatchersEditIndex = $state<number | null>(null);
  let rsEditTag = $state<string | null>(null);

  const rsEditTarget = $derived<SingboxRouterRuleSet | undefined>(
    rsEditTag !== null ? $ruleSets.find((rs) => rs.tag === rsEditTag) : undefined,
  );

  onMount(() => {
    void singboxRouterStore.loadAll();
  });

  onDestroy(() => {
    cleanupDrag();
  });

  function safeRuleSummary(card: RuleCardData | undefined, index: number): string {
    const n = String(index).padStart(2, '0');
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
    const rule = $rules[index];
    if (!rule || isSystemRule(rule)) return;
    const info = classifyRuleSimplicity(rule, $ruleSets);
    if (!info.simple) return;
    cancelDrag();
    const prefill = prefillWizardFromRule(rule, $presets, $ruleSets, $outbounds);
    if (!prefill.editMode) return;
    setTemplateSelection(prefill.templateIds);
    openEditWizard(index, {
      editMode: prefill.editMode,
      rulesList: prefill.rulesList,
      outboundCategory: prefill.outboundCategory,
      tunnelTags: prefill.tunnelTags,
      existingInlineRuleSetTag: prefill.existingInlineRuleSetTag,
      wasInlineText: prefill.wasInlineText,
    });
  }

  function requestTextMatchersEdit(index: number) {
    const rule = $rules[index];
    if (!rule) return;
    const info = classifyRuleSimplicity(rule, $ruleSets);
    if (!info.simple || info.kind !== 'inline-text') return;
    cancelDrag();
    textMatchersEditIndex = index;
  }

  function requestInlineListEdit(index: number) {
    const rule = $rules[index];
    if (!rule) return;
    const info = classifyRuleSimplicity(rule, $ruleSets);
    if (!info.simple || info.kind !== 'inline-set' || !info.inlineRuleSetTag) return;
    requestRulesetEdit(info.inlineRuleSetTag);
  }

  function requestRulesetEdit(tag: string) {
    cancelDrag();
    // Чип несёт display-тег (без -srs); если inline-база удалена, а компаньон
    // ещё не вычищен, в $ruleSets остался только тег с суффиксом — ищем по нему.
    const rs = $ruleSets.find((r) => r.tag === tag)
      ?? $ruleSets.find((r) => !!r.tag && displayRuleSetTag(r.tag) === tag);
    if (!rs) {
      notifications.error(`Набор «${tag}» не найден в конфигурации`);
      return;
    }
    rsEditTag = rs.tag;
  }

  async function handleRsEditSave(rs: SingboxRouterRuleSet) {
    if (rsEditTag === null) return;
    try {
      await api.singboxRouterUpdateRuleSet(rsEditTag, rs);
      await singboxRouterStore.loadAll();
      notifications.success('Набор обновлён');
      rsEditTag = null;
    } catch (e) {
      notifications.error(`Ошибка: ${e instanceof Error ? e.message : String(e)}`);
    }
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

  async function handleTextMatchersSave(rule: (typeof $rules)[number]) {
    if (textMatchersEditIndex === null) return;
    try {
      await api.singboxRouterUpdateRule(textMatchersEditIndex, rule);
      await singboxRouterStore.loadAll();
      notifications.success('Адреса обновлены');
      textMatchersEditIndex = null;
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

  function canScrollWindow(): boolean {
    if (typeof document === 'undefined' || typeof window === 'undefined') return false;
    return document.documentElement.scrollHeight > window.innerHeight + 1;
  }

  function isInScrollEdge(y: number): boolean {
    if (scrollContainer) {
      const rect = scrollContainer.getBoundingClientRect();
      const distTop = y - rect.top;
      const distBottom = rect.bottom - y;
      if (distTop >= 0 && distTop < SCROLL_EDGE) return true;
      if (distBottom >= 0 && distBottom < SCROLL_EDGE) return true;
      return false;
    }
    if (!canScrollWindow()) return false;
    return y < SCROLL_EDGE || y > window.innerHeight - SCROLL_EDGE;
  }

  function stopAutoScroll() {
    if (autoScrollRaf !== null) {
      cancelAnimationFrame(autoScrollRaf);
      autoScrollRaf = null;
    }
  }

  function clearDropSkeletonTimer() {
    if (dropSkeletonTimer !== null) {
      clearTimeout(dropSkeletonTimer);
      dropSkeletonTimer = null;
    }
  }

  function resolvesToSourceIndex(targetInsertion: number): boolean {
    if (!dragState) return false;
    return normalizeDropTarget(dragState.fromIndex, targetInsertion) === dragState.fromIndex;
  }

  function clearCollapseDropTimer() {
    if (collapseDropTimer !== null) {
      clearTimeout(collapseDropTimer);
      collapseDropTimer = null;
    }
  }

  function sourceDropVisualAt(fromIndex: number): number | 'end' {
    return fromIndex < cards.length - 1 ? fromIndex + 1 : 'end';
  }

  function targetDropAt(idx: number | null): number | 'end' | null {
    if (idx === null || !dragState?.started) return null;

    const from = dragState.fromIndex;

    if (resolvesToSourceIndex(idx)) {
      if (!hasMovedFromSource) return null;
      return sourceDropVisualAt(from);
    }

    if (idx >= cards.length) return 'end';
    return idx;
  }

  function scheduleDropSkeleton() {
    const target = targetDropAt(insertionIndex);
    if (!dragState?.started || target === null || target !== dropAt) {
      dropExpanded = false;
      clearDropSkeletonTimer();
      return;
    }
    // Цель не менялась: раскрытый скелетон не схлопываем (раскрытие сдвигает
    // геометрию → insertionIndex меняется → безусловный сброс рушил только что
    // показанный скелетон), идущий таймер не перезапускаем.
    if (dropExpanded || dropSkeletonTimer !== null) return;
    dropSkeletonTimer = setTimeout(() => {
      if (dragState?.started && targetDropAt(insertionIndex) === dropAt && dropAt !== null) {
        dropExpanded = true;
        requestAnimationFrame(() => {
          if (!dragState?.started) return;
          measureSlots();
          applyInsertionAtPointer(lastPointerY);
        });
      }
      dropSkeletonTimer = null;
    }, DROP_SKELETON_DELAY_MS);
  }

  function reconcileDropDisplay() {
    const next = targetDropAt(insertionIndex);
    if (next === dropAt) {
      if (next !== null) scheduleDropSkeleton();
      return;
    }

    const prev = dropAt;
    const wasExpanded = dropExpanded && prev !== null;

    clearDropSkeletonTimer();
    dropExpanded = false;

    if (prev !== null && prev !== next) {
      collapsingDropAt = prev;
      collapsingWasExpanded = wasExpanded;
      collapsePhaseActive = false;
      clearCollapseDropTimer();
      requestAnimationFrame(() => {
        if (collapsingDropAt !== prev) return;
        collapsePhaseActive = true;
        const collapseMs = wasExpanded ? DROP_SLOT_MOTION_MS : DROP_LINE_COLLAPSE_MS;
        collapseDropTimer = setTimeout(() => {
          collapsingDropAt = null;
          collapsingWasExpanded = false;
          collapsePhaseActive = false;
          collapseDropTimer = null;
        }, collapseMs);
      });
    }

    dropAt = next;
    if (next !== null) scheduleDropSkeleton();
  }

  function setInsertionIndex(next: number | null) {
    if (insertionIndex === next) return;
    insertionIndex = next;
    reconcileDropDisplay();
  }

  function applyInsertionAtPointer(y: number) {
    const nextInsertion = calculateInsertionIndex(y);
    const firstMovable = firstUserRuleIndex();
    const normalized = firstMovable >= 0 ? Math.max(firstMovable, nextInsertion) : nextInsertion;

    if (resolvesToSourceIndex(normalized)) {
      if (!hasMovedFromSource) {
        setInsertionIndex(null);
        return;
      }
    } else {
      hasMovedFromSource = true;
    }

    setInsertionIndex(normalized);
  }

  function tickAutoScroll() {
    autoScrollRaf = null;
    if (!dragState?.started) return;

    const y = lastPointerY;
    let scrolled = false;

    if (scrollContainer) {
      const rect = scrollContainer.getBoundingClientRect();
      const distTop = y - rect.top;
      const distBottom = rect.bottom - y;
      if (distTop >= 0 && distTop < SCROLL_EDGE) {
        scrollContainer.scrollTop -= SCROLL_MAX_SPEED * (1 - distTop / SCROLL_EDGE);
        scrolled = true;
      } else if (distBottom >= 0 && distBottom < SCROLL_EDGE) {
        scrollContainer.scrollTop += SCROLL_MAX_SPEED * (1 - distBottom / SCROLL_EDGE);
        scrolled = true;
      }
    } else if (canScrollWindow()) {
      if (y < SCROLL_EDGE) {
        window.scrollBy(0, -SCROLL_MAX_SPEED * (1 - y / SCROLL_EDGE));
        scrolled = true;
      } else if (y > window.innerHeight - SCROLL_EDGE) {
        window.scrollBy(0, SCROLL_MAX_SPEED * (1 - (window.innerHeight - y) / SCROLL_EDGE));
        scrolled = true;
      }
    }

    if (scrolled) {
      measureSlots();
      applyInsertionAtPointer(y);
    }

    if (isInScrollEdge(y)) {
      autoScrollRaf = requestAnimationFrame(tickAutoScroll);
    }
  }

  function updateAutoScroll(y: number) {
    lastPointerY = y;
    if (!dragState?.started) return;
    if (isInScrollEdge(y)) {
      if (autoScrollRaf === null) {
        autoScrollRaf = requestAnimationFrame(tickAutoScroll);
      }
    } else {
      stopAutoScroll();
    }
  }

  function beginSourceExit() {
    sourceExitCollapsed = false;
    requestAnimationFrame(() => {
      if (!dragState?.started) return;
      sourceExitCollapsed = true;
    });
  }

  function clearDropCommitTimer() {
    if (dropCommitTimer !== null) {
      clearTimeout(dropCommitTimer);
      dropCommitTimer = null;
    }
  }

  function reorderRules(all: SingboxRouterRule[], from: number, to: number): SingboxRouterRule[] {
    if (from === to) return all;
    const next = all.slice();
    const [moved] = next.splice(from, 1);
    next.splice(to, 0, moved);
    return next;
  }

  async function commitDrop(fromIndex: number, to: number) {
    moveInFlight = true;
    const snapshot = get(rules);
    singboxRouterStore.applyRules(reorderRules(snapshot, fromIndex, to));
    cleanupDrag();
    try {
      await api.singboxRouterMoveRule(fromIndex, to);
      await singboxRouterStore.loadAll();
    } catch (e) {
      singboxRouterStore.applyRules(snapshot);
      notifications.error(`Ошибка перемещения: ${e instanceof Error ? e.message : String(e)}`);
    } finally {
      moveInFlight = false;
    }
  }

  function detachDragInteraction() {
    const current = dragState;
    if (current?.started && current.handleEl.hasPointerCapture?.(current.pointerId)) {
      current.handleEl.releasePointerCapture(current.pointerId);
    }
    stopAutoScroll();
    clearDropSkeletonTimer();
    dragGhostCard = null;
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

  function cleanupDrag() {
    const current = dragState;
    if (current?.started && current.handleEl.hasPointerCapture?.(current.pointerId)) {
      current.handleEl.releasePointerCapture(current.pointerId);
    }
    stopAutoScroll();
    clearDropSkeletonTimer();
    clearCollapseDropTimer();
    clearDropCommitTimer();
    dropCommitPending = false;
    sourceExitCollapsed = false;
    hasMovedFromSource = false;
    dropAt = null;
    dropExpanded = false;
    collapsingDropAt = null;
    collapsingWasExpanded = false;
    collapsePhaseActive = false;
    scrollContainer = null;
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
    scrollContainer = findScrollContainer(panelEl);
    beginSourceExit();
    hasMovedFromSource = false;
    setInsertionIndex(null);
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
    measureSlots();
    applyInsertionAtPointer(event.clientY);
    updateAutoScroll(event.clientY);
  }

  function normalizeDropTarget(fromIndex: number, targetInsertion: number): number {
    let to = targetInsertion > fromIndex ? targetInsertion - 1 : targetInsertion;
    to = Math.max(0, Math.min(to, cards.length - 1));
    const firstMovable = firstUserRuleIndex();
    if (firstMovable >= 0 && to < firstMovable) to = firstMovable;
    return to;
  }

  async function onDragPointerUp(event: PointerEvent) {
    if (!dragState || dropCommitPending) return;
    if (event.pointerId !== dragState.pointerId) return;

    const state = dragState;
    const started = state.started;
    const fromIndex = state.fromIndex;
    const targetInsertion = insertionIndex ?? fromIndex;
    const to = normalizeDropTarget(fromIndex, targetInsertion);

    if (!started) {
      cleanupDrag();
      return;
    }

    if (to === fromIndex || !canMoveIndexToTarget(fromIndex, to)) {
      cleanupDrag();
      return;
    }

    if (dropAt !== null && !dropExpanded) {
      clearDropCommitTimer();
      clearDropSkeletonTimer();
      dropCommitPending = true;
      detachDragInteraction();
      requestAnimationFrame(() => {
        if (!dropCommitPending) return;
        dropExpanded = true;
        dropCommitTimer = setTimeout(async () => {
          dropCommitTimer = null;
          dropCommitPending = false;
          await commitDrop(fromIndex, to);
        }, DROP_SLOT_MOTION_MS);
      });
      return;
    }

    await commitDrop(fromIndex, to);
  }

  function handleDragPointerDown(index: number, card: RuleCardData, event: PointerEvent) {
    event.preventDefault();
    event.stopPropagation();
    if (moveInFlight || dropCommitPending) return;
    if (card.isSystem) return;
    if (deleteBusy || textMatchersEditIndex !== null || rsEditTag !== null || deleteTarget) return;
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

  function showsDropBefore(index: number): boolean {
    return dropAt === index || collapsingDropAt === index;
  }

  function showsDropAtEnd(): boolean {
    return dropAt === 'end' || collapsingDropAt === 'end';
  }

  function dropBeforeExpanded(index: number): boolean {
    if (collapsingDropAt === index) return collapsingWasExpanded;
    return dropAt === index && dropExpanded;
  }

  function dropBeforeCollapsing(index: number): boolean {
    return collapsingDropAt === index && collapsePhaseActive;
  }

  function dropEndExpanded(): boolean {
    if (collapsingDropAt === 'end') return collapsingWasExpanded;
    return dropAt === 'end' && dropExpanded;
  }

  function dropEndCollapsing(): boolean {
    return collapsingDropAt === 'end' && collapsePhaseActive;
  }

  function isDragSource(index: number): boolean {
    return draggingIndex === index && (!!dragState?.started || dropCommitPending);
  }

  function isDragActive(): boolean {
    return !!dragState?.started || dropCommitPending;
  }

  function cardsMotionStyle(): string {
    return [
      `--card-gap:${CARD_GAP}px`,
      `--drop-slot-motion-ms:${DROP_SLOT_MOTION_MS}ms`,
      `--drop-line-collapse-ms:${DROP_LINE_COLLAPSE_MS}ms`,
      `--slot-ease:${SLOT_EASE}`,
    ].join(';');
  }

  function dropIndicatorStyle(): string {
    const height = dragState?.rect.height ?? 0;
    return `--drop-height:${height}px;--card-gap:${CARD_GAP}px`;
  }
</script>

<section class="rules-panel" bind:this={panelEl}>
  <header class="panel-header">
    <div class="title-group">
      <h2 class="title">Что и куда отправлять</h2>
      <p class="sub">Правила применяются сверху вниз. Срабатывает первое подходящее.</p>
    </div>
    <div class="header-right">
      <div class="counter">
        {pluralize(count, RULE_WORDS)}
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
    <div class="cards" class:is-dragging={isDragActive()} style={cardsMotionStyle()} role="list">
      {#each cards as card, i (card.id)}
        <div
          data-rule-index={i}
          role="listitem"
          class="card-shell"
          class:drag-source-exiting={isDragSource(i)}
          class:drag-source-collapsed={isDragSource(i) && sourceExitCollapsed}
          style={isDragSource(i) ? dropIndicatorStyle() : undefined}
          use:rowNode={card.id}
        >
          {#if showsDropBefore(i)}
            <div
              class="drop-indicator"
              class:expanded={dropBeforeExpanded(i)}
              class:collapsing={dropBeforeCollapsing(i)}
              style={dropIndicatorStyle()}
            ></div>
          {/if}

          <RuleCard
            {card}
            index={i}
            dragging={draggingIndex === i}
            dragDisabled={moveInFlight || dropCommitPending}
            onEdit={() => requestEdit(i)}
            onTextMatchersClick={() => requestTextMatchersEdit(i)}
            onInlineListClick={() => requestInlineListEdit(i)}
            onRulesetClick={requestRulesetEdit}
            {knownRulesetTags}
            onDelete={() => requestDelete(i)}
            onDragHandlePointerDown={(e) => handleDragPointerDown(i, card, e)}
          />
        </div>
      {/each}
      {#if showsDropAtEnd()}
        <div
          class="drop-indicator drop-indicator-end"
          class:expanded={dropEndExpanded()}
          class:collapsing={dropEndCollapsing()}
          style={dropIndicatorStyle()}
        ></div>
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

{#if textMatchersEditIndex !== null && $rules[textMatchersEditIndex]}
  <RuleEditModal
    rule={$rules[textMatchersEditIndex]}
    outboundOptions={$options}
    availableRuleSets={$ruleSets}
    matchersOnly
    onClose={() => (textMatchersEditIndex = null)}
    onSave={handleTextMatchersSave}
  />
{/if}

{#if rsEditTag !== null && rsEditTarget}
  <RuleSetAddModal
    ruleSet={rsEditTarget}
    outboundOptions={$options}
    onClose={() => (rsEditTag = null)}
    onSave={handleRsEditSave}
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
    gap: var(--card-gap, 6px);
    min-width: 0;
  }
  .cards.is-dragging {
    user-select: none;
  }
  .card-shell {
    position: relative;
  }
  .card-shell.drag-source-exiting {
    overflow: hidden;
    height: var(--drop-height);
    opacity: 1;
    transition:
      height var(--drop-slot-motion-ms, 360ms) var(--slot-ease, cubic-bezier(0.45, 0.05, 0.55, 0.95)),
      opacity var(--drop-slot-motion-ms, 360ms) var(--slot-ease, cubic-bezier(0.45, 0.05, 0.55, 0.95)),
      margin var(--drop-slot-motion-ms, 360ms) var(--slot-ease, cubic-bezier(0.45, 0.05, 0.55, 0.95));
  }
  .card-shell.drag-source-exiting.drag-source-collapsed {
    height: 0;
    max-height: 0;
    opacity: 0;
    margin-bottom: calc(-1 * var(--card-gap, 6px));
  }
  .drop-indicator {
    box-sizing: border-box;
    overflow: hidden;
    border: 1px solid transparent;
    border-radius: 999px;
    background: var(--accent);
    box-shadow: 0 0 10px color-mix(in srgb, var(--accent) 45%, transparent);
    opacity: 1;
    pointer-events: none;
    transition:
      height var(--drop-slot-motion-ms, 360ms) var(--slot-ease, cubic-bezier(0.45, 0.05, 0.55, 0.95)),
      margin var(--drop-slot-motion-ms, 360ms) var(--slot-ease, cubic-bezier(0.45, 0.05, 0.55, 0.95)),
      border-radius calc(var(--drop-slot-motion-ms, 360ms) * 0.85) var(--slot-ease, cubic-bezier(0.45, 0.05, 0.55, 0.95)),
      background calc(var(--drop-slot-motion-ms, 360ms) * 0.85) var(--slot-ease, cubic-bezier(0.45, 0.05, 0.55, 0.95)),
      box-shadow calc(var(--drop-slot-motion-ms, 360ms) * 0.85) var(--slot-ease, cubic-bezier(0.45, 0.05, 0.55, 0.95)),
      border-color calc(var(--drop-slot-motion-ms, 360ms) * 0.85) var(--slot-ease, cubic-bezier(0.45, 0.05, 0.55, 0.95)),
      opacity calc(var(--drop-slot-motion-ms, 360ms) * 0.85) var(--slot-ease, cubic-bezier(0.45, 0.05, 0.55, 0.95));
  }
  .drop-indicator:not(.expanded):not(.collapsing) {
    position: absolute;
    top: calc(-1 * var(--card-gap, 6px) / 2 - 1px);
    left: 0;
    right: 0;
    height: 2px;
    margin: 0;
    z-index: 2;
  }
  .drop-indicator.expanded:not(.collapsing) {
    position: static;
    top: auto;
    height: var(--drop-height);
    margin: 0 0 var(--card-gap, 6px);
    border-radius: var(--radius);
    background: color-mix(in srgb, var(--accent) 6%, transparent);
    border-color: var(--accent-line, var(--accent));
    border-style: dashed;
    box-shadow: none;
  }
  .drop-indicator.collapsing {
    margin: 0 !important;
    opacity: 0;
    border-color: transparent;
    background: transparent;
    box-shadow: none;
  }
  .drop-indicator.collapsing.expanded {
    position: static;
    height: 0 !important;
    transition:
      height var(--drop-slot-motion-ms, 360ms) var(--slot-ease, cubic-bezier(0.45, 0.05, 0.55, 0.95)),
      margin var(--drop-slot-motion-ms, 360ms) var(--slot-ease, cubic-bezier(0.45, 0.05, 0.55, 0.95)),
      border-radius calc(var(--drop-slot-motion-ms, 360ms) * 0.85) var(--slot-ease, cubic-bezier(0.45, 0.05, 0.55, 0.95)),
      background calc(var(--drop-slot-motion-ms, 360ms) * 0.85) var(--slot-ease, cubic-bezier(0.45, 0.05, 0.55, 0.95)),
      box-shadow calc(var(--drop-slot-motion-ms, 360ms) * 0.85) var(--slot-ease, cubic-bezier(0.45, 0.05, 0.55, 0.95)),
      border-color calc(var(--drop-slot-motion-ms, 360ms) * 0.85) var(--slot-ease, cubic-bezier(0.45, 0.05, 0.55, 0.95)),
      opacity var(--drop-slot-motion-ms, 360ms) var(--slot-ease, cubic-bezier(0.45, 0.05, 0.55, 0.95));
  }
  /* Тонкая линия: держим top/height, иначе при .collapsing теряется :not(.collapsing)
     и absolute-блок прыгает к верху card-shell → «точка» ниже щели между карточками. */
  .drop-indicator.collapsing:not(.expanded) {
    position: absolute;
    top: calc(-1 * var(--card-gap, 6px) / 2 - 1px);
    left: 0;
    right: 0;
    height: 2px !important;
    z-index: 2;
    transition:
      opacity var(--drop-line-collapse-ms, 240ms) var(--slot-ease, cubic-bezier(0.45, 0.05, 0.55, 0.95)),
      box-shadow calc(var(--drop-line-collapse-ms, 240ms) * 0.85) var(--slot-ease, cubic-bezier(0.45, 0.05, 0.55, 0.95)),
      background calc(var(--drop-line-collapse-ms, 240ms) * 0.85) var(--slot-ease, cubic-bezier(0.45, 0.05, 0.55, 0.95)),
      border-color calc(var(--drop-line-collapse-ms, 240ms) * 0.85) var(--slot-ease, cubic-bezier(0.45, 0.05, 0.55, 0.95));
  }
  .drop-indicator-end.collapsing:not(.expanded) {
    position: relative;
    top: auto;
    left: auto;
    right: auto;
    height: 2px !important;
    margin: calc(-1 * var(--card-gap, 6px) / 2 - 1px) 0 0 !important;
  }
  .drop-indicator-end:not(.expanded):not(.collapsing) {
    position: relative;
    top: auto;
    height: 2px;
    margin: calc(-1 * var(--card-gap, 6px) / 2 - 1px) 0 0;
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
