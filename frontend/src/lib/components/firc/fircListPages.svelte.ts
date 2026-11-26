/**
 * Страницы правил удалённого списка групп: загрузка страницы с бэкенда,
 * контроль актуальности URL списка и несохранённые правки включения правил.
 */

import { api } from '$lib/api/client';
import { PAGE_SIZE, type GroupListPageState } from '$lib/utils/fircGroups';
import { normalizeFircListUrl } from '$lib/utils/fircOverlap';
import type { FircGroup, FircListRule } from '$lib/types/firc';

export interface FircListPendingGroupEdits {
	groupId: string;
	entries: Array<{ id: string; type?: string; enable?: boolean }>;
}

export interface FircListPagesController {
	/** Загруженная страница по id группы. */
	readonly pages: Record<string, GroupListPageState>;
	/** Есть несохранённые правки хотя бы в одной группе. */
	readonly hasEdits: boolean;
	/** Возвращает страницу только если URL группы совпадает с сохранённым и загруженным. */
	activePage(group: FircGroup): GroupListPageState | undefined;
	/** Проверяет наличие несохранённых правок среди актуальных страниц текущих групп. */
	hasEditsFor(groups: readonly FircGroup[]): boolean;
	/** Возвращает несохранённые правки для актуальных страниц текущих групп. */
	pendingEditsFor(groups: readonly FircGroup[]): FircListPendingGroupEdits[];
	load(group: FircGroup, offset: number, query: string): Promise<void>;
	toggleRule(group: FircGroup, rule: FircListRule, checked: boolean): void;
	/** Сбрасывает кэш страницы одной группы и отменяет её активные запросы. */
	clear(groupId: string): void;
	/** Удаляет из кэша страницы групп, которых больше нет в списке. */
	prune(validGroupIds: ReadonlySet<string>): void;
	/** Полный сброс кэша и обновление сохранённых URL (при загрузке / сбросе черновика). */
	resetForGroups(savedGroups: readonly FircGroup[]): void;
	/** Обновление сохранённых URL и очистка правок / устаревших или незавершённых страниц после сохранения. */
	reconcileAfterSave(savedGroups: readonly FircGroup[]): void;
	/** Обновляет сохранённый URL группы после синхронизации на бэкенде. */
	setPersistedUrl(groupId: string, url: string | undefined | null): void;
	/** Инвалидирует кэш при смене URL списка или догружает страницу при откате к сохранённому URL. */
	handleUrlEdited(group: FircGroup, expanded: boolean, query: string): void;
	/** Правки сохранены или отброшены: страницы остаются, правки — нет. */
	clearEdits(): void;
}

function buildPersistedUrls(savedGroups: readonly FircGroup[]): Record<string, string> {
	const next: Record<string, string> = {};
	for (const g of savedGroups) {
		const norm = normalizeFircListUrl(g.list?.url);
		if (norm) next[g.id] = norm;
	}
	return next;
}

export function createFircListPages(): FircListPagesController {
	let pages = $state<Record<string, GroupListPageState>>({});
	let persistedUrls = $state<Record<string, string>>({});
	const requestSeq: Record<string, number> = {};
	const loadedUrlsByGroup: Record<string, string> = {};

	const hasEdits = $derived(Object.values(pages).some((p) => Object.keys(p.edits).length > 0));

	function bumpSeq(groupId: string): number {
		const next = (requestSeq[groupId] ?? 0) + 1;
		requestSeq[groupId] = next;
		return next;
	}

	function activePage(group: FircGroup): GroupListPageState | undefined {
		const page = pages[group.id];
		if (!page) return undefined;
		const currentUrl = normalizeFircListUrl(group.list?.url);
		const persistedUrl = persistedUrls[group.id] ?? '';
		if (!currentUrl || !persistedUrl || currentUrl !== persistedUrl || page.loadedUrl !== currentUrl) {
			return undefined;
		}
		return page;
	}

	function pendingEditsFor(groups: readonly FircGroup[]): FircListPendingGroupEdits[] {
		const out: FircListPendingGroupEdits[] = [];
		for (const g of groups) {
			const page = activePage(g);
			if (!page) continue;
			const entries = Object.entries(page.edits).map(([id, ed]) => ({ id, ...ed }));
			if (entries.length > 0) {
				out.push({ groupId: g.id, entries });
			}
		}
		return out;
	}

	function hasEditsFor(groups: readonly FircGroup[]): boolean {
		return pendingEditsFor(groups).length > 0;
	}

	function clear(groupId: string) {
		bumpSeq(groupId);
		delete loadedUrlsByGroup[groupId];
		if (!(groupId in pages)) return;
		const next = { ...pages };
		delete next[groupId];
		pages = next;
	}

	function prune(validGroupIds: ReadonlySet<string>) {
		let changed = false;
		const next: Record<string, GroupListPageState> = {};
		for (const [id, page] of Object.entries(pages)) {
			if (validGroupIds.has(id)) {
				next[id] = page;
			} else {
				bumpSeq(id);
				delete loadedUrlsByGroup[id];
				changed = true;
			}
		}
		for (const id of Object.keys(requestSeq)) {
			if (!validGroupIds.has(id)) {
				bumpSeq(id);
				delete loadedUrlsByGroup[id];
			}
		}
		if (changed) {
			pages = next;
		}
	}

	function resetForGroups(savedGroups: readonly FircGroup[]) {
		for (const id of new Set([
			...Object.keys(pages),
			...Object.keys(requestSeq),
			...Object.keys(loadedUrlsByGroup),
		])) {
			bumpSeq(id);
			delete loadedUrlsByGroup[id];
		}
		persistedUrls = buildPersistedUrls(savedGroups);
		pages = {};
	}

	function reconcileAfterSave(savedGroups: readonly FircGroup[]) {
		const nextPersisted = buildPersistedUrls(savedGroups);
		const nextPages: Record<string, GroupListPageState> = {};
		for (const id of new Set([
			...Object.keys(pages),
			...Object.keys(requestSeq),
			...Object.keys(loadedUrlsByGroup),
		])) {
			const prev = pages[id];
			const persistedUrl = nextPersisted[id] ?? '';
			if (
				prev &&
				!prev.loading &&
				persistedUrl &&
				prev.loadedUrl === persistedUrl &&
				loadedUrlsByGroup[id] === persistedUrl
			) {
				nextPages[id] = { ...prev, edits: {} };
			} else {
				// Invalidate removed, URL-changed, or mid-flight loading entries so stale responses
				// are ignored and expanded groups re-fetch cleanly.
				bumpSeq(id);
				delete loadedUrlsByGroup[id];
			}
		}
		persistedUrls = nextPersisted;
		pages = nextPages;
	}

	function setPersistedUrl(groupId: string, url: string | undefined | null) {
		const norm = normalizeFircListUrl(url);
		if (norm) {
			persistedUrls = { ...persistedUrls, [groupId]: norm };
		} else if (groupId in persistedUrls) {
			const next = { ...persistedUrls };
			delete next[groupId];
			persistedUrls = next;
		}
	}

	async function load(group: FircGroup, offset: number, query: string) {
		const groupId = group.id;
		const expectedUrl = normalizeFircListUrl(group.list?.url);
		const persistedUrl = persistedUrls[groupId] ?? '';
		if (!expectedUrl || !persistedUrl || expectedUrl !== persistedUrl) {
			clear(groupId);
			return;
		}
		const seq = bumpSeq(groupId);
		const existing = pages[groupId]?.loadedUrl === expectedUrl ? pages[groupId] : undefined;
		const hadLoaded = Boolean(existing && loadedUrlsByGroup[groupId] === expectedUrl);
		const previousOffset = existing?.offset ?? 0;
		pages = {
			...pages,
			[groupId]: {
				offset: hadLoaded ? previousOffset : offset,
				total: existing?.total ?? 0,
				matched: existing?.matched ?? 0,
				loading: true,
				loadedUrl: expectedUrl,
				rules: existing?.rules ?? [],
				edits: existing?.edits ?? {},
			},
		};
		try {
			const res = await api.fircGetGroupListRules(groupId, offset, PAGE_SIZE, query);
			if (requestSeq[groupId] !== seq) return;
			const edits = pages[groupId]?.loadedUrl === expectedUrl ? (pages[groupId]?.edits ?? {}) : {};
			const rules = (res.rules ?? []).map((r) => ({
				...r,
				enable: edits[r.id]?.enable !== undefined ? edits[r.id].enable! : r.enable,
				type: edits[r.id]?.type !== undefined ? edits[r.id].type! : r.type,
			}));
			loadedUrlsByGroup[groupId] = expectedUrl;
			pages = {
				...pages,
				[groupId]: {
					offset: res.offset ?? offset,
					total: res.total ?? 0,
					matched: res.matched ?? 0,
					loading: false,
					loadedUrl: expectedUrl,
					rules,
					edits,
				},
			};
		} catch {
			if (requestSeq[groupId] !== seq) return;
			if (!hadLoaded) {
				const next = { ...pages };
				delete next[groupId];
				pages = next;
				return;
			}
			const cur = pages[groupId];
			if (!cur || cur.loadedUrl !== expectedUrl) return;
			pages = {
				...pages,
				[groupId]: {
					...cur,
					offset: previousOffset,
					loading: false,
				},
			};
		}
	}

	function toggleRule(group: FircGroup, rule: FircListRule, checked: boolean) {
		const page = activePage(group);
		if (!page) return;
		const updatedRules = page.rules.map((r) => (r.id === rule.id ? { ...r, enable: checked } : r));
		const updatedEdits = {
			...page.edits,
			[rule.id]: { ...page.edits[rule.id], enable: checked },
		};
		pages = {
			...pages,
			[group.id]: {
				...page,
				rules: updatedRules,
				edits: updatedEdits,
			},
		};
	}

	function handleUrlEdited(group: FircGroup, expanded: boolean, query: string) {
		const currentUrl = normalizeFircListUrl(group.list?.url);
		const persistedUrl = persistedUrls[group.id] ?? '';
		if (currentUrl && currentUrl === persistedUrl) {
			if (expanded && (!pages[group.id] || pages[group.id].loadedUrl !== currentUrl)) {
				void load(group, 0, query);
			}
			return;
		}
		clear(group.id);
	}

	function clearEdits() {
		const cleanedPages: Record<string, GroupListPageState> = {};
		for (const [k, v] of Object.entries(pages)) {
			cleanedPages[k] = { ...v, edits: {} };
		}
		pages = cleanedPages;
	}

	return {
		get pages() {
			return pages;
		},
		get hasEdits() {
			return hasEdits;
		},
		activePage,
		hasEditsFor,
		pendingEditsFor,
		load,
		toggleRule,
		clear,
		prune,
		resetForGroups,
		reconcileAfterSave,
		setPersistedUrl,
		handleUrlEdited,
		clearEdits,
	};
}
