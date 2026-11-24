/**
 * Страницы правил удалённого списка групп: загрузка страницы с бэкенда и
 * несохранённые правки включения правил (уходят вместе с «Сохранить»).
 */

import { api } from '$lib/api/client';
import { PAGE_SIZE, type GroupListPageState } from '$lib/utils/fircGroups';
import type { FircListRule } from '$lib/types/firc';

export interface FircListPagesController {
	/** Загруженная страница по id группы. */
	readonly pages: Record<string, GroupListPageState>;
	/** Есть несохранённые правки хотя бы в одной группе. */
	readonly hasEdits: boolean;
	load(groupId: string, offset: number, query: string): Promise<void>;
	toggleRule(groupId: string, rule: FircListRule, checked: boolean): void;
	/** Правки сохранены или отброшены: страницы остаются, правки — нет. */
	clearEdits(): void;
}

export function createFircListPages(): FircListPagesController {
	let pages = $state<Record<string, GroupListPageState>>({});
	const hasEdits = $derived(Object.values(pages).some((p) => Object.keys(p.edits).length > 0));

	async function load(groupId: string, offset: number, query: string) {
		const existing = pages[groupId];
		pages = {
			...pages,
			[groupId]: {
				offset,
				total: existing?.total ?? 0,
				matched: existing?.matched ?? 0,
				loading: true,
				rules: existing?.rules ?? [],
				edits: existing?.edits ?? {},
			},
		};
		try {
			const res = await api.fircGetGroupListRules(groupId, offset, PAGE_SIZE, query);
			const edits = pages[groupId]?.edits ?? {};
			const rules = (res.rules ?? []).map((r) => ({
				...r,
				enable: edits[r.id]?.enable !== undefined ? edits[r.id].enable! : r.enable,
				type: edits[r.id]?.type !== undefined ? edits[r.id].type! : r.type,
			}));
			pages = {
				...pages,
				[groupId]: {
					offset: res.offset ?? offset,
					total: res.total ?? 0,
					matched: res.matched ?? 0,
					loading: false,
					rules,
					edits,
				},
			};
		} catch {
			pages = {
				...pages,
				[groupId]: {
					...(pages[groupId] ?? {
						offset: 0,
						total: 0,
						matched: 0,
						rules: [],
						edits: {},
					}),
					loading: false,
				},
			};
		}
	}

	function toggleRule(groupId: string, rule: FircListRule, checked: boolean) {
		const page = pages[groupId];
		if (!page) return;
		const updatedRules = page.rules.map((r) => (r.id === rule.id ? { ...r, enable: checked } : r));
		const updatedEdits = {
			...page.edits,
			[rule.id]: { ...page.edits[rule.id], enable: checked },
		};
		pages = {
			...pages,
			[groupId]: {
				...page,
				rules: updatedRules,
				edits: updatedEdits,
			},
		};
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
		load,
		toggleRule,
		clearEdits,
	};
}
