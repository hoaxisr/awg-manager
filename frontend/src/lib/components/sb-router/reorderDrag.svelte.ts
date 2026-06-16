/**
 * Переиспользуемое pointer-drag-ядро для переупорядочивания вертикальных списков.
 *
 * Это ВЕРБАТИМ-порт полировки из RulesPanel.svelte (route.rules): тот же
 * floating ghost-card, тот же раскрывающийся/схлопывающийся скелетон-слот в точке
 * вставки, тот же auto-scroll у краёв, тот же порог + pointer-capture, та же
 * оптимистика-commit + откат. RRulesPanel ОСТАЁТСЯ нетронутым (страница маршрутов
 * не должна регрессировать) — здесь независимая копия того же движка, чтобы DNS-
 * списки выглядели и ощущались ПИКСЕЛЬ-в-пиксель как route.rules.
 *
 * Движок data-agnostic: знает только индексы и DOM-строки (через колбэки), не
 * знает, что внутри карточек. Компонент рендерит ghost/skeleton-разметку и
 * привязывает её к реактивным геттерам контроллера.
 *
 * Геометрические тайминги/easing держим идентичными RulesPanel (см. константы).
 */

import { findScrollContainer } from '$lib/utils/findScrollContainer';

const DRAG_THRESHOLD = 7;
const SCROLL_EDGE = 84;
const SCROLL_MAX_SPEED = 14;
const DROP_SKELETON_DELAY_MS = 680;
const DROP_SLOT_MOTION_MS = 360;
const DROP_LINE_COLLAPSE_MS = 240;
const SLOT_EASE = 'cubic-bezier(0.45, 0.05, 0.55, 0.95)';
const CARD_GAP = 6;

interface ReorderDragOptions {
	/** DOM-строка (shell) по индексу — для измерения геометрии. */
	getRowElement: (index: number) => HTMLElement | null;
	/** Сколько строк в списке (включая виртуальные «фиксированные», напр. final). */
	count: () => number;
	/** Корневой элемент панели — для поиска scroll-контейнера. */
	getPanelEl: () => HTMLElement | null;
	/**
	 * Строка «фиксирована» (нельзя схватить и нельзя стать целью вставки): напр.
	 * системные правила или read-only «final»-строка. По умолчанию ничего.
	 */
	isFixed?: (index: number) => boolean;
	/** Доп. ограничение перемещения from→to сверх isFixed (напр. «не ниже final»). */
	canMove?: (from: number, to: number) => boolean;
	/** Оптимистика + API + откат при ошибке. */
	onCommit: (from: number, to: number) => Promise<void>;
}

export interface ReorderDragController {
	/** Индекс строки-источника во время drag (для тусклой подсветки исходной карточки). */
	readonly draggingIndex: number | null;
	/** Индекс строки-источника, чей слот сейчас под ghost (рисуем как «дырку»). */
	readonly ghostFromIndex: number | null;
	/** Идёт ли активный drag/commit (источник вынут). */
	readonly active: boolean;
	/** Заблокировать грипы на время async-commit. */
	readonly busy: boolean;
	/** Геометрия плавающей ghost-карточки. */
	readonly ghostVisible: boolean;
	readonly ghostTop: number;
	readonly ghostLeft: number;
	readonly ghostWidth: number;

	/** Строка-источник «схлопывается» (анимация выезда). */
	isDragSource: (index: number) => boolean;
	sourceCollapsed: (index: number) => boolean;
	/** Скелетон-слот перед строкой index. */
	showsDropBefore: (index: number) => boolean;
	dropBeforeExpanded: (index: number) => boolean;
	dropBeforeCollapsing: (index: number) => boolean;
	/** Скелетон-слот в самом конце списка. */
	showsDropAtEnd: () => boolean;
	dropEndExpanded: () => boolean;
	dropEndCollapsing: () => boolean;

	/** inline-style для CSS-переменных мотиона на контейнере .cards. */
	cardsMotionStyle: () => string;
	/** inline-style для скелетон-слота/коллапса источника (высота источника). */
	dropIndicatorStyle: () => string;

	/** Навесить на pointerdown ручки-грипа строки. */
	handlePointerDown: (index: number, event: PointerEvent) => void;
	/** Снять все слушатели (onDestroy). */
	destroy: () => void;
}

export function createReorderDrag(opts: ReorderDragOptions): ReorderDragController {
	const isFixed = (i: number) => opts.isFixed?.(i) ?? false;

	let dragState = $state<null | {
		pointerId: number;
		fromIndex: number;
		startY: number;
		grabOffsetY: number;
		rect: DOMRect;
		started: boolean;
		handleEl: HTMLElement;
	}>(null);
	let insertionIndex = $state<number | null>(null);
	let draggingIndex = $state<number | null>(null);
	let ghostActive = $state(false);
	let dragGhostTop = $state(0);
	let dragGhostLeft = $state(0);
	let dragGhostWidth = $state(0);
	let measuredSlots = $state<Array<{ index: number; top: number; bottom: number; mid: number }>>([]);
	let dropAt = $state<number | 'end' | null>(null);
	let dropExpanded = $state(false);
	let collapsingDropAt = $state<number | 'end' | null>(null);
	let collapsingWasExpanded = $state(false);
	let collapsePhaseActive = $state(false);
	let sourceExitCollapsed = $state(false);
	let hasMovedFromSource = $state(false);
	let dropCommitPending = $state(false);
	let moveInFlight = $state(false);

	let scrollContainer: HTMLElement | null = null;
	let lastPointerY = 0;
	let autoScrollRaf: number | null = null;
	let dropSkeletonTimer: ReturnType<typeof setTimeout> | null = null;
	let collapseDropTimer: ReturnType<typeof setTimeout> | null = null;
	let dropCommitTimer: ReturnType<typeof setTimeout> | null = null;

	function firstMovableIndex(): number {
		const n = opts.count();
		for (let i = 0; i < n; i++) {
			if (!isFixed(i)) return i;
		}
		return -1;
	}

	function canMoveIndexToTarget(from: number, to: number): boolean {
		const n = opts.count();
		if (from === to) return false;
		if (from < 0 || from >= n || to < 0 || to >= n) return false;
		if (isFixed(from) || isFixed(to)) return false;
		const firstUser = firstMovableIndex();
		if (firstUser === -1) return false;
		if (to < firstUser) return false;
		if (opts.canMove && !opts.canMove(from, to)) return false;
		return true;
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

	function clearCollapseDropTimer() {
		if (collapseDropTimer !== null) {
			clearTimeout(collapseDropTimer);
			collapseDropTimer = null;
		}
	}

	function clearDropCommitTimer() {
		if (dropCommitTimer !== null) {
			clearTimeout(dropCommitTimer);
			dropCommitTimer = null;
		}
	}

	function resolvesToSourceIndex(targetInsertion: number): boolean {
		if (!dragState) return false;
		return normalizeDropTarget(dragState.fromIndex, targetInsertion) === dragState.fromIndex;
	}

	function sourceDropVisualAt(fromIndex: number): number | 'end' {
		return fromIndex < opts.count() - 1 ? fromIndex + 1 : 'end';
	}

	function targetDropAt(idx: number | null): number | 'end' | null {
		if (idx === null || !dragState?.started) return null;
		const from = dragState.fromIndex;
		if (resolvesToSourceIndex(idx)) {
			if (!hasMovedFromSource) return null;
			return sourceDropVisualAt(from);
		}
		if (idx >= opts.count()) return 'end';
		return idx;
	}

	function scheduleDropSkeleton() {
		const target = targetDropAt(insertionIndex);
		if (!dragState?.started || target === null || target !== dropAt) {
			dropExpanded = false;
			clearDropSkeletonTimer();
			return;
		}
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
		const firstMovable = firstMovableIndex();
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

	async function commitDrop(fromIndex: number, to: number) {
		moveInFlight = true;
		cleanupDrag();
		try {
			await opts.onCommit(fromIndex, to);
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
		ghostActive = false;
		if (typeof document !== 'undefined') {
			document.body.classList.remove('reorder-dragging');
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
		ghostActive = false;
		measuredSlots = [];
		if (typeof document !== 'undefined') {
			document.body.classList.remove('reorder-dragging');
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
		const n = opts.count();
		const items: Array<{ index: number; top: number; bottom: number; mid: number }> = [];
		for (let idx = 0; idx < n; idx++) {
			if (idx === state.fromIndex) continue;
			if (isFixed(idx)) continue;
			const el = opts.getRowElement(idx);
			if (!el) continue;
			const rect = el.getBoundingClientRect();
			items.push({ index: idx, top: rect.top, bottom: rect.bottom, mid: rect.top + rect.height / 2 });
		}
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
		ghostActive = true;
		dragGhostLeft = dragState.rect.left;
		dragGhostWidth = dragState.rect.width;
		dragGhostTop = event.clientY - dragState.grabOffsetY;
		scrollContainer = findScrollContainer(opts.getPanelEl());
		beginSourceExit();
		hasMovedFromSource = false;
		setInsertionIndex(null);
		measureSlots();
		if (typeof document !== 'undefined') {
			document.body.classList.add('reorder-dragging');
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
		to = Math.max(0, Math.min(to, opts.count() - 1));
		const firstMovable = firstMovableIndex();
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

	function handlePointerDown(index: number, event: PointerEvent) {
		event.preventDefault();
		event.stopPropagation();
		if (moveInFlight || dropCommitPending) return;
		if (isFixed(index)) return;
		if (event.button !== 0) return;
		const shell = opts.getRowElement(index);
		const handleEl = event.currentTarget as HTMLElement | null;
		if (!shell || !handleEl) return;
		const rect = shell.getBoundingClientRect();

		dragState = {
			pointerId: event.pointerId,
			fromIndex: index,
			startY: event.clientY,
			grabOffsetY: event.clientY - rect.top,
			rect,
			started: false,
			handleEl,
		};

		if (typeof window !== 'undefined') {
			window.addEventListener('pointermove', onDragPointerMove);
			window.addEventListener('pointerup', onDragPointerUp);
			window.addEventListener('pointercancel', cancelDrag);
			window.addEventListener('keydown', onDragKeyDown);
		}
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

	return {
		get draggingIndex() {
			return draggingIndex;
		},
		get ghostFromIndex() {
			return ghostActive && dragState ? dragState.fromIndex : null;
		},
		get active() {
			return isDragActive();
		},
		get busy() {
			return moveInFlight || dropCommitPending;
		},
		get ghostVisible() {
			return ghostActive && !!dragState?.started;
		},
		get ghostTop() {
			return dragGhostTop;
		},
		get ghostLeft() {
			return dragGhostLeft;
		},
		get ghostWidth() {
			return dragGhostWidth;
		},
		isDragSource,
		sourceCollapsed: (index: number) => isDragSource(index) && sourceExitCollapsed,
		showsDropBefore,
		dropBeforeExpanded,
		dropBeforeCollapsing,
		showsDropAtEnd,
		dropEndExpanded,
		dropEndCollapsing,
		cardsMotionStyle,
		dropIndicatorStyle,
		handlePointerDown,
		destroy: cleanupDrag,
	};
}
