/**
 * Модалка «Новая группа / Настройки группы»: открытие, поля формы, каталог
 * списков для подсказок и предпросмотр списка по URL. Применение формы к
 * группам делает панель.
 */

import { api } from '$lib/api/client';
import { emptyGroupForm, type GroupForm } from '$lib/utils/fircGroups';
import type { FircCatalog, FircListPreviewRes } from '$lib/types/firc';

export interface FircGroupFormController {
	readonly open: boolean;
	readonly mode: 'create' | 'edit';
	/** Индекс редактируемой группы; null — создание. */
	readonly index: number | null;
	form: GroupForm;
	readonly catalog: FircCatalog | null;
	readonly previewLoading: boolean;
	readonly previewData: FircListPreviewRes | null;
	readonly previewError: string | null;
	openWith(mode: 'create' | 'edit', index: number | null, form: GroupForm): void;
	close(): void;
	ensureCatalog(): Promise<void>;
	preview(): Promise<void>;
	/** Выбор сервиса в каталоге: имя (если пусто) и URL его списка. */
	applyCatalogPick(name: string, url: string): void;
}

export function createFircGroupForm(): FircGroupFormController {
	let open = $state(false);
	let mode = $state<'create' | 'edit'>('create');
	let index = $state<number | null>(null);
	let form = $state<GroupForm>(emptyGroupForm(''));
	let catalog = $state<FircCatalog | null>(null);
	let catalogLoading = false;
	let previewLoading = $state(false);
	let previewData = $state<FircListPreviewRes | null>(null);
	let previewError = $state<string | null>(null);

	async function ensureCatalog() {
		if ((catalog?.sources?.length ?? 0) > 0 || catalogLoading) return;
		catalogLoading = true;
		try {
			catalog = await api.fircGetListCatalog();
		} catch {
			// catalog optional
		} finally {
			catalogLoading = false;
		}
	}

	return {
		get open() {
			return open;
		},
		get mode() {
			return mode;
		},
		get index() {
			return index;
		},
		get form() {
			return form;
		},
		set form(v: GroupForm) {
			form = v;
		},
		get catalog() {
			return catalog;
		},
		get previewLoading() {
			return previewLoading;
		},
		get previewData() {
			return previewData;
		},
		get previewError() {
			return previewError;
		},
		openWith(nextMode, nextIndex, nextForm) {
			mode = nextMode;
			index = nextIndex;
			form = nextForm;
			previewData = null;
			previewError = null;
			open = true;
			void ensureCatalog();
		},
		close() {
			open = false;
		},
		ensureCatalog,
		async preview() {
			if (!form.listUrl.trim()) return;
			previewLoading = true;
			previewError = null;
			try {
				previewData = await api.fircPreviewGroupList(form.listUrl.trim());
			} catch (e) {
				previewError = (e as Error).message;
				previewData = null;
			} finally {
				previewLoading = false;
			}
		},
		applyCatalogPick(name, url) {
			if (!form.name.trim()) {
				form.name = name;
			}
			if (url) {
				form.listEnabled = true;
				form.listUrl = url;
				previewData = null;
				previewError = null;
			}
		},
	};
}
