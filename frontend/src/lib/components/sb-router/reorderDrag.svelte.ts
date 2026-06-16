/**
 * Переиспользуемое pointer-drag-ядро для переупорядочивания списков.
 *
 * Подход взят из RulesPanel.svelte (route.rules): порог сдвига → pointer-capture
 * → midpoint-вставка по измеренным строкам → drop-индикатор → оптимистичный
 * reorder + async-commit + откат при ошибке. Это самодостаточная копия того же
 * подхода (RulesPanel НЕ трогаем — его ghost/skeleton-анимации слишком сплетены
 * с его DOM, чтобы извлечь без риска для рабочей страницы). Здесь — минимальный,
 * но честный вариант: захват ручки, порог, тонкая линия-индикатор, grab-курсор,
 * оптимистичный apply с откатом.
 *
 * Использование (в компоненте):
 *   const drag = createReorderDrag({
 *     getRowElement: (i) => rowEls[i] ?? null,
 *     canMove: (from, to) => to < items.length,   // напр. final-строка не движется
 *     onCommit: async (from, to) => {              // оптимистика + API + revert
 *       const snap = get(store);
 *       applyX(reorder(snap, from, to));
 *       try { await api.moveX(from, to); await loadAll(); }
 *       catch (e) { applyX(snap); notifications.error(...); }
 *     },
 *   });
 *   ...
 *   <span onpointerdown={(e) => drag.handlePointerDown(i, e)}>grip</span>
 *   {#if drag.dropBefore(i)}<div class="drop-line"></div>{/if}
 */

const DRAG_THRESHOLD = 7;

interface ReorderDragOptions {
	/** Вернуть DOM-строку по индексу (для измерения середины). */
	getRowElement: (index: number) => HTMLElement | null;
	/** Сколько строк сейчас в списке. */
	count: () => number;
	/** Можно ли переместить from→to (напр. read-only «final» строку нельзя). */
	canMove?: (from: number, to: number) => boolean;
	/** Совершить перемещение: оптимистика + API + откат при ошибке. */
	onCommit: (from: number, to: number) => Promise<void>;
}

export interface ReorderDragController {
	/** Индекс перетаскиваемой строки (null — нет drag). */
	readonly draggingIndex: number | null;
	/** true, пока идёт async-commit (грипы дизейблятся). */
	readonly busy: boolean;
	/** Показывать тонкую линию-индикатор ПЕРЕД строкой index. */
	dropBefore: (index: number) => boolean;
	/** Показывать линию-индикатор в самом конце списка. */
	dropAtEnd: () => boolean;
	/** Навесить на pointerdown ручки-грипа. */
	handlePointerDown: (index: number, event: PointerEvent) => void;
}

export function createReorderDrag(opts: ReorderDragOptions): ReorderDragController {
	let draggingIndex = $state<number | null>(null);
	let busy = $state(false);
	// Куда вставить: индекс строки, перед которой ляжет элемент, либо 'end'.
	let insertion = $state<number | 'end' | null>(null);

	let pointerId = -1;
	let startY = 0;
	let started = false;
	let fromIndex = -1;
	let handleEl: HTMLElement | null = null;
	let slots: Array<{ index: number; mid: number }> = [];

	function measureSlots() {
		const n = opts.count();
		const items: Array<{ index: number; mid: number }> = [];
		for (let i = 0; i < n; i++) {
			if (i === fromIndex) continue;
			const el = opts.getRowElement(i);
			if (!el) continue;
			const r = el.getBoundingClientRect();
			items.push({ index: i, mid: r.top + r.height / 2 });
		}
		items.sort((a, b) => a.mid - b.mid);
		slots = items;
	}

	/** Индекс вставки (0..count) по координате указателя. */
	function insertionIndexAt(y: number): number {
		if (!slots.length) return fromIndex;
		for (const slot of slots) {
			if (y < slot.mid) return slot.index;
		}
		return slots[slots.length - 1].index + 1;
	}

	/** Нормализованная вставка → display-цель (число или 'end'), учитывая canMove. */
	function displayTarget(insertIdx: number): number | 'end' | null {
		const n = opts.count();
		const to = insertIdx > fromIndex ? insertIdx - 1 : insertIdx;
		if (to === fromIndex) return null;
		if (opts.canMove && !opts.canMove(fromIndex, to)) return null;
		return insertIdx >= n ? 'end' : insertIdx;
	}

	function applyAtPointer(y: number) {
		const idx = insertionIndexAt(y);
		insertion = displayTarget(idx);
	}

	function startDrag() {
		started = true;
		draggingIndex = fromIndex;
		handleEl?.setPointerCapture?.(pointerId);
		measureSlots();
		if (typeof document !== 'undefined') document.body.classList.add('reorder-dragging');
	}

	function onPointerMove(event: PointerEvent) {
		if (event.pointerId !== pointerId) return;
		if (!started) {
			if (Math.abs(event.clientY - startY) < DRAG_THRESHOLD) return;
			startDrag();
		}
		event.preventDefault();
		measureSlots();
		applyAtPointer(event.clientY);
	}

	function teardown() {
		if (handleEl?.hasPointerCapture?.(pointerId)) handleEl.releasePointerCapture(pointerId);
		if (typeof window !== 'undefined') {
			window.removeEventListener('pointermove', onPointerMove);
			window.removeEventListener('pointerup', onPointerUp);
			window.removeEventListener('pointercancel', onCancel);
			window.removeEventListener('keydown', onKeyDown);
		}
		if (typeof document !== 'undefined') document.body.classList.remove('reorder-dragging');
		started = false;
		draggingIndex = null;
		insertion = null;
		slots = [];
		handleEl = null;
		pointerId = -1;
		fromIndex = -1;
	}

	function onCancel() {
		teardown();
	}

	function onKeyDown(event: KeyboardEvent) {
		if (event.key === 'Escape') {
			event.preventDefault();
			teardown();
		}
	}

	async function onPointerUp(event: PointerEvent) {
		if (event.pointerId !== pointerId) return;
		const wasStarted = started;
		const from = fromIndex;
		const insertIdx = insertion === 'end' ? opts.count() : insertion;
		teardown();
		if (!wasStarted || insertIdx === null) return;
		const to = insertIdx > from ? insertIdx - 1 : insertIdx;
		if (to === from) return;
		if (opts.canMove && !opts.canMove(from, to)) return;
		busy = true;
		try {
			await opts.onCommit(from, to);
		} finally {
			busy = false;
		}
	}

	function handlePointerDown(index: number, event: PointerEvent) {
		if (busy || draggingIndex !== null) return;
		if (event.button !== 0) return;
		event.preventDefault();
		event.stopPropagation();
		pointerId = event.pointerId;
		fromIndex = index;
		startY = event.clientY;
		started = false;
		handleEl = event.currentTarget as HTMLElement | null;
		if (typeof window !== 'undefined') {
			window.addEventListener('pointermove', onPointerMove);
			window.addEventListener('pointerup', onPointerUp);
			window.addEventListener('pointercancel', onCancel);
			window.addEventListener('keydown', onKeyDown);
		}
	}

	return {
		get draggingIndex() {
			return draggingIndex;
		},
		get busy() {
			return busy;
		},
		dropBefore: (index: number) => started && insertion === index,
		dropAtEnd: () => started && insertion === 'end',
		handlePointerDown,
	};
}
