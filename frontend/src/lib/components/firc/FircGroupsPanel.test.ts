import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/svelte';
import { m } from '$lib/i18n';
import { reloadFircAll } from '$lib/stores/firc';
import { invalidateResource } from '$lib/stores/storeRegistry';
import type { FircExtConfig, FircGroup } from '$lib/types/firc';

const { api } = vi.hoisted(() => {
	const fns: Record<string, ReturnType<typeof vi.fn>> = {};
	return {
		api: new Proxy(fns, {
			get: (t, key: string) => (t[key] ??= vi.fn(async () => [])),
		}),
	};
});
vi.mock('$lib/api/client', () => ({ api }));

import FircGroupsPanel from './FircGroupsPanel.svelte';

const groups: FircGroup[] = [
	{ id: 'g1', name: 'One', interface: ['eth3', 'tunvless0'], enable: true, rules: [] },
	{ id: 'g2', name: 'Two', interface: ['eth3', 'tunvless0'], enable: true, rules: [] },
];
const ext: FircExtConfig = {
	proxyGroups: [{ id: 'pg', name: 'PG', interfaces: ['eth3', 'tunvless0'], check: { mode: 'url', url: 'https://pg.example/204' } }],
	keenDnsCloud: { enabled: false, interfaces: [] },
	serverRouting: { enabledServers: [] },
	groupLinks: { g1: 'pg' },
};

describe('FircGroupsPanel', () => {
	beforeEach(() => {
		localStorage.clear();
		sessionStorage.clear();
		localStorage.setItem('awgm.firc.groupView', 'list');
		api.fircGetGroups.mockReset();
		api.fircGetGroups.mockImplementation(async () => structuredClone(groups));
		api.fircGetGroupsState.mockImplementation(async () => ({ groups: {} }));
		api.fircGetExt.mockReset();
		api.fircGetExt.mockImplementation(async () => structuredClone(ext));
		api.fircSaveExt.mockReset();
		api.fircSaveGroups.mockReset();
		api.fircSaveGroups.mockImplementation(async (g: FircGroup[]) => ({ groups: g }));
	});
	afterEach(() => {
		cleanup();
	});

	async function moveDownAndSave() {
		render(FircGroupsPanel);
		const down = await screen.findAllByTitle(m.firc_groups_move_down());
		await fireEvent.click(down[0]);
		await fireEvent.click((await screen.findAllByRole('button', { name: m.firc_groups_save_changes() }))[0]);
		await waitFor(() => expect(api.fircSaveGroups).toHaveBeenCalledTimes(1));
		return api.fircSaveGroups.mock.calls[0];
	}

	it('sends group links beside the groups', async () => {
		const [payload, , links] = await moveDownAndSave();
		expect(payload.map((g: FircGroup) => g.id)).toEqual(['g2', 'g1']);
		expect(JSON.stringify(payload)).not.toContain('proxyGroupId');
		expect(links).toEqual({ g1: 'pg' });

		// После сохранения связь остаётся в панели: второе сохранение её не теряет.
		await fireEvent.click((await screen.findAllByTitle(m.firc_groups_move_down()))[0]);
		await fireEvent.click((await screen.findAllByRole('button', { name: m.firc_groups_save_changes() }))[0]);
		await waitFor(() => expect(api.fircSaveGroups).toHaveBeenCalledTimes(2));
		expect(api.fircSaveGroups.mock.calls[1][2]).toEqual({ g1: 'pg' });
	});

	it('links every group of a bulk proxy group pick', async () => {
		render(FircGroupsPanel);
		await fireEvent.click(await screen.findByRole('button', { name: m.firc_bulk_select_groups_btn() }));
		await fireEvent.click(screen.getByText(m.firc_bulk_select_all({ count: 2 })));
		await fireEvent.change(screen.getByRole('combobox', { name: m.firc_bulk_groups_change_iface() }), {
			target: { value: 'pg:pg' },
		});
		await fireEvent.click((await screen.findAllByRole('button', { name: m.firc_groups_save_changes() }))[0]);
		await waitFor(() => expect(api.fircSaveGroups).toHaveBeenCalledTimes(1));
		const [payload, , links] = api.fircSaveGroups.mock.calls[0];
		expect(links).toEqual({ g1: 'pg', g2: 'pg' });
		expect(payload.map((g: FircGroup) => g.check?.url)).toEqual(['https://pg.example/204', 'https://pg.example/204']);
	});

	it('leaves links to the server when the ext config could not be read', async () => {
		api.fircGetExt.mockImplementation(async () => {
			throw new Error('down');
		});
		const [, , links] = await moveDownAndSave();
		expect(links).toBeUndefined();
	});

	it('allows picking a Fallback proxy group when creating a new routing group', async () => {
		api.fircGetInterfaces.mockImplementation(async () => [
			{ id: 'eth3', name: 'WAN', role: 'wan' },
			{ id: 'tunvless0', name: 'DE', role: 'tunnel' },
		]);
		render(FircGroupsPanel);
		await screen.findByText('One');

		await fireEvent.click(screen.getByRole('button', { name: m.firc_groups_add_group() }));
		const nameInput = screen.getByLabelText(m.firc_groups_form_name());
		const ifaceSelect = screen.getByLabelText(m.firc_groups_form_iface());
		await fireEvent.input(nameInput, { target: { value: 'NewPG' } });
		await fireEvent.change(ifaceSelect, { target: { value: 'pg:pg' } });

		expect(
			screen.getByText(m.firc_group_check_managed_by_pg({ name: 'PG' })),
		).toBeTruthy();

		await fireEvent.click(screen.getByRole('button', { name: m.firc_groups_modal_create_btn() }));
		await fireEvent.click((await screen.findAllByRole('button', { name: m.firc_groups_save_changes() }))[0]);
		await waitFor(() => expect(api.fircSaveGroups).toHaveBeenCalledTimes(1));

		const [payload, , links] = api.fircSaveGroups.mock.calls[0];
		const created = (payload as FircGroup[]).find((g) => g.name === 'NewPG');
		expect(created).toBeDefined();
		expect(created?.interface).toEqual(['eth3', 'tunvless0']);
		expect(created?.check).toEqual({ mode: 'url', url: 'https://pg.example/204' });
		expect(links[created!.id]).toBe('pg');
	});

	it('updates linked draft groups when tunnel order in a Fallback proxy group changes', async () => {
		let currentGroups = structuredClone(groups);
		let currentExt = structuredClone(ext);
		api.fircGetGroups.mockImplementation(async () => structuredClone(currentGroups));
		api.fircGetExt.mockImplementation(async () => structuredClone(currentExt));
		api.fircGetInterfaces.mockImplementation(async () => [
			{ id: 'eth3', name: 'WAN', role: 'wan' },
			{ id: 'tunvless0', name: 'DE', role: 'tunnel' },
		]);
		api.fircSaveExt.mockImplementation(async (nextExt: FircExtConfig) => {
			const plain = JSON.parse(JSON.stringify(nextExt)) as FircExtConfig;
			currentExt = { ...plain, groupLinks: currentExt.groupLinks };
			// Имитация обновления связанной группы g1 на бэкенде при смене порядка в PG.
			const updatedPg = currentExt.proxyGroups.find((p) => p.id === 'pg');
			if (updatedPg) {
				currentGroups = currentGroups.map((g) =>
					g.id === 'g1' ? { ...g, interface: [...updatedPg.interfaces] } : g,
				);
			}
			return structuredClone(currentExt);
		});

		render(FircGroupsPanel);
		await screen.findByText('One');

		// Создаём несохранённую группу на ту же прокси-группу (черновик становится dirty).
		await fireEvent.click(screen.getByRole('button', { name: m.firc_groups_add_group() }));
		await fireEvent.input(screen.getByLabelText(m.firc_groups_form_name()), { target: { value: 'DraftPG' } });
		await fireEvent.change(screen.getByLabelText(m.firc_groups_form_iface()), { target: { value: 'pg:pg' } });
		await fireEvent.click(screen.getByRole('button', { name: m.firc_groups_modal_create_btn() }));

		// Меняем порядок туннелей в прокси-группе и сохраняем её.
		await fireEvent.click(screen.getByTitle(m.firc_fallback_configure_chain()));
		const moveDownBtns = await screen.findAllByTitle(m.firc_groups_pg_move_down());
		await fireEvent.click(moveDownBtns[0]);
		await fireEvent.click(screen.getByRole('button', { name: m.common_save() }));

		await waitFor(() => expect(api.fircSaveExt).toHaveBeenCalledTimes(1));
		await waitFor(() => expect(screen.queryByRole('button', { name: m.common_save() })).toBeNull());
		await screen.findByText('DraftPG');

		// Сохраняем черновик правил: и g1, и новая DraftPG должны иметь новый порядок и остаться привязаны к pg.
		const saveBtn = (await screen.findAllByRole('button', { name: m.firc_groups_save_changes() }))[0] as HTMLButtonElement;
		await waitFor(() => expect(saveBtn.disabled).toBe(false));
		await fireEvent.click(saveBtn);
		await waitFor(() => expect(api.fircSaveGroups).toHaveBeenCalledTimes(1));

		const [payload, , links] = api.fircSaveGroups.mock.calls[0];
		const savedG1 = (payload as FircGroup[]).find((g) => g.id === 'g1');
		const savedDraft = (payload as FircGroup[]).find((g) => g.name === 'DraftPG');
		expect(savedG1?.interface).toEqual(['tunvless0', 'eth3']);
		expect(savedDraft?.interface).toEqual(['tunvless0', 'eth3']);
		expect(links.g1).toBe('pg');
		expect(links[savedDraft!.id]).toBe('pg');
	});

	it('detaches a PG-linked group when switched to a single interface in settings modal', async () => {
		api.fircGetInterfaces.mockImplementation(async () => [
			{ id: 'eth3', name: 'WAN', role: 'wan' },
			{ id: 'tunvless0', name: 'DE', role: 'tunnel' },
		]);
		render(FircGroupsPanel);
		await screen.findByText('One');

		const settingsBtns = screen.getAllByTitle(m.firc_groups_settings_tip());
		await fireEvent.click(settingsBtns[0]);
		const ifaceSelect = screen.getByLabelText<HTMLSelectElement>(m.firc_groups_form_iface());
		expect(ifaceSelect.value).toBe('pg:pg');
		expect(screen.getByText(m.firc_group_check_managed_by_pg({ name: 'PG' }))).toBeTruthy();

		// Переключаем на одиночный eth3 — плашка прокси-группы исчезает, правило отвязывается.
		await fireEvent.change(ifaceSelect, { target: { value: 'eth3' } });
		expect(screen.queryByText(m.firc_group_check_managed_by_pg({ name: 'PG' }))).toBeNull();

		await fireEvent.click(screen.getByRole('button', { name: m.common_apply() }));
		await fireEvent.click((await screen.findAllByRole('button', { name: m.firc_groups_save_changes() }))[0]);
		await waitFor(() => expect(api.fircSaveGroups).toHaveBeenCalledTimes(1));

		const [payload, , links] = api.fircSaveGroups.mock.calls[0];
		const savedG1 = (payload as FircGroup[]).find((g) => g.id === 'g1');
		expect(savedG1?.interface).toBe('eth3');
		expect(links.g1).toBeUndefined();
	});

	it('auto-enables url check for checkless Fallback PG and blocks submit if picked PG disappears', async () => {
		let extState: FircExtConfig = {
			...structuredClone(ext),
			proxyGroups: [
				...structuredClone(ext.proxyGroups),
				{ id: 'pg_free', name: 'FreePG', interfaces: ['eth3', 'tunvless0'] },
			],
		};
		let resolveRefresh: ((v: FircExtConfig) => void) | null = null;
		let extCalls = 0;
		api.fircGetExt.mockImplementation(() => {
			extCalls++;
			if (extCalls === 1) return Promise.resolve(structuredClone(extState));
			return new Promise<FircExtConfig>((resolve) => {
				resolveRefresh = resolve;
			});
		});
		api.fircGetInterfaces.mockImplementation(async () => [
			{ id: 'eth3', name: 'WAN', role: 'wan' },
			{ id: 'tunvless0', name: 'DE', role: 'tunnel' },
		]);

		render(FircGroupsPanel);
		await screen.findByText('One');

		await fireEvent.click(screen.getByRole('button', { name: m.firc_groups_add_group() }));
		const ifaceSelect = screen.getByLabelText<HTMLSelectElement>(m.firc_groups_form_iface());
		await fireEvent.change(ifaceSelect, { target: { value: 'pg:pg_free' } });

		// Прокси-группа без проверки автоматически включает режим URL в форме.
		const checkSelect = screen.getByLabelText<HTMLSelectElement>(m.firc_group_check_label());
		expect(checkSelect.value).toBe('url');

		// Прокси-группа исчезает во время открытой модалки — появляется явная ошибка и блокируется создание.
		extState = { ...extState, proxyGroups: extState.proxyGroups.filter((p) => p.id !== 'pg_free') };
		resolveRefresh!(structuredClone(extState));
		const alert = await screen.findByRole('alert');
		expect(alert.textContent).toContain(m.firc_groups_pg_not_found());
		const createBtn = screen.getByRole<HTMLButtonElement>('button', {
			name: m.firc_groups_modal_create_btn(),
		});
		expect(createBtn.disabled).toBe(true);
	});

	it('preserves draft proxyGroupId when loadAll resolves after a concurrent refreshProxyGroups', async () => {
		sessionStorage.setItem(
			'awgm_firc_groups_draft_v1',
			JSON.stringify({
				groups: [
					{ id: 'g1', name: 'One edited', interface: ['eth3', 'tunvless0'], enable: true, rules: [], proxyGroupId: 'pg' },
					{ id: 'g2', name: 'Two', interface: ['eth3', 'tunvless0'], enable: true, rules: [] },
				],
				baseSnapshot: '[]',
				draftedAt: '2026-11-26T00:00:00.000Z',
			}),
		);
		let resolveInitialExt: ((v: FircExtConfig) => void) | null = null;
		let resolveConcurrentExt: ((v: FircExtConfig) => void) | null = null;
		let extCalls = 0;
		api.fircGetExt.mockImplementation(() => {
			extCalls++;
			if (extCalls === 1) {
				return new Promise<FircExtConfig>((resolve) => {
					resolveInitialExt = resolve;
				});
			}
			return new Promise<FircExtConfig>((resolve) => {
				resolveConcurrentExt = resolve;
			});
		});

		render(FircGroupsPanel);
		// Открытие модалки во время первичной загрузки инкрементирует proxyGroupRefresh.
		await fireEvent.click(screen.getByRole('button', { name: m.firc_groups_add_group() }));
		// loadAll завершается первым, пока extConfig ещё пустой — proxyGroupId в черновике не должен стереться.
		resolveInitialExt!(structuredClone(ext));
		await screen.findByText('One edited');
		// Затем завершается конкурентный refreshProxyGroups.
		resolveConcurrentExt!(structuredClone(ext));

		await fireEvent.click(screen.getByRole('button', { name: m.common_cancel() }));
		await fireEvent.click((await screen.findAllByRole('button', { name: m.firc_groups_save_changes() }))[0]);
		await waitFor(() => expect(api.fircSaveGroups).toHaveBeenCalledTimes(1));

		const [, , links] = api.fircSaveGroups.mock.calls[0];
		expect(links.g1).toBe('pg');
	});

	it('syncs mounted dirty draft when ext updates externally via resource invalidation', async () => {
		let currentExt = structuredClone(ext);
		api.fircGetExt.mockImplementation(async () => structuredClone(currentExt));
		api.fircGetInterfaces.mockImplementation(async () => [
			{ id: 'eth3', name: 'WAN', role: 'wan' },
			{ id: 'tunvless0', name: 'DE', role: 'tunnel' },
		]);

		render(FircGroupsPanel);
		await screen.findByText('One');

		// Создаём несохранённое правило на pg (панель становится dirty и остаётся смонтированной).
		await fireEvent.click(screen.getByRole('button', { name: m.firc_groups_add_group() }));
		await fireEvent.input(screen.getByLabelText(m.firc_groups_form_name()), { target: { value: 'DrawerDraft' } });
		await fireEvent.change(screen.getByLabelText(m.firc_groups_form_iface()), { target: { value: 'pg:pg' } });
		await fireEvent.click(screen.getByRole('button', { name: m.firc_groups_modal_create_btn() }));

		// Имитируем сохранение нового порядка туннелей из боковой панели настроек (FircSettingsPanel).
		currentExt = {
			...currentExt,
			proxyGroups: [{ ...currentExt.proxyGroups[0], interfaces: ['tunvless0', 'eth3'] }],
		};
		invalidateResource('firc', 'ext-updated');
		await waitFor(() =>
			expect(sessionStorage.getItem('awgm_firc_groups_draft_v1')).toContain('["tunvless0","eth3"]'),
		);

		// Сохраняем черновик — обе связанные группы уходят с новым порядком, не откатывая сервер.
		await fireEvent.click((await screen.findAllByRole('button', { name: m.firc_groups_save_changes() }))[0]);
		await waitFor(() => expect(api.fircSaveGroups).toHaveBeenCalledTimes(1));

		const [payload, , links] = api.fircSaveGroups.mock.calls[0];
		const savedG1 = (payload as FircGroup[]).find((g) => g.id === 'g1');
		const savedDraft = (payload as FircGroup[]).find((g) => g.name === 'DrawerDraft');
		expect(savedG1?.interface).toEqual(['tunvless0', 'eth3']);
		expect(savedDraft?.interface).toEqual(['tunvless0', 'eth3']);
		expect(links.g1).toBe('pg');
		expect(links[savedDraft!.id]).toBe('pg');
	});

	it('hydrates server groupLinks when fircGetExt fails on loadAll and succeeds later in refreshProxyGroups', async () => {
		let extCalls = 0;
		api.fircGetExt.mockImplementation(async () => {
			extCalls++;
			if (extCalls === 1) throw new Error('temporary ext failure');
			return structuredClone(ext);
		});
		api.fircGetInterfaces.mockImplementation(async () => [
			{ id: 'eth3', name: 'WAN', role: 'wan' },
			{ id: 'tunvless0', name: 'DE', role: 'tunnel' },
		]);

		render(FircGroupsPanel);
		await screen.findByText('One');

		// Открытие модалки создания вызывает refreshProxyGroups(true), который успешно подгружает ext.
		await fireEvent.click(screen.getByRole('button', { name: m.firc_groups_add_group() }));
		await waitFor(() => expect(extCalls).toBeGreaterThanOrEqual(2));
		await fireEvent.input(screen.getByLabelText(m.firc_groups_form_name()), { target: { value: 'AddedLater' } });
		await fireEvent.click(screen.getByRole('button', { name: m.firc_groups_modal_create_btn() }));

		await fireEvent.click((await screen.findAllByRole('button', { name: m.firc_groups_save_changes() }))[0]);
		await waitFor(() => expect(api.fircSaveGroups).toHaveBeenCalledTimes(1));

		const [, , links] = api.fircSaveGroups.mock.calls[0];
		// Существующая связь g1 -> pg не должна быть стёрта.
		expect(links.g1).toBe('pg');
	});

	it('keeps baseline synced on external PG reorder so Discard does not revert to stale chain', async () => {
		let currentExt = structuredClone(ext);
		api.fircGetExt.mockImplementation(async () => structuredClone(currentExt));
		api.fircGetInterfaces.mockImplementation(async () => [
			{ id: 'eth3', name: 'WAN', role: 'wan' },
			{ id: 'tunvless0', name: 'DE', role: 'tunnel' },
		]);

		render(FircGroupsPanel);
		await screen.findByText('One');

		// Делаем несвязанную правку (переставляем группы), затем внешне меняем порядок в pg.
		const down = await screen.findAllByTitle(m.firc_groups_move_down());
		await fireEvent.click(down[0]);
		currentExt = {
			...currentExt,
			proxyGroups: [{ ...currentExt.proxyGroups[0], interfaces: ['tunvless0', 'eth3'] }],
		};
		invalidateResource('firc', 'ext-updated');
		await waitFor(() =>
			expect(sessionStorage.getItem('awgm_firc_groups_draft_v1')).toContain('["tunvless0","eth3"]'),
		);

		// Сброс черновика должен оставить g1 на новом порядке (не откатывать к старому baseline).
		await fireEvent.click(screen.getByRole('button', { name: m.firc_groups_reset() }));
		await fireEvent.click(screen.getByRole('button', { name: m.firc_groups_discard_confirm() }));

		// Новое сохранение после сброса (переставим обратно) должно отправить g1 с новым порядком.
		const downAfter = await screen.findAllByTitle(m.firc_groups_move_down());
		await fireEvent.click(downAfter[0]);
		await fireEvent.click((await screen.findAllByRole('button', { name: m.firc_groups_save_changes() }))[0]);
		await waitFor(() => expect(api.fircSaveGroups).toHaveBeenCalledTimes(1));

		const [payload, , links] = api.fircSaveGroups.mock.calls[0];
		const savedG1 = (payload as FircGroup[]).find((g) => g.id === 'g1');
		expect(savedG1?.interface).toEqual(['tunvless0', 'eth3']);
		expect(links.g1).toBe('pg');
	});

	it('preserves modal creation submitted while a silent firc-invalidation reload is in flight', async () => {
		let groupsCalls = 0;
		let resolveReloadGroups: ((v: FircGroup[]) => void) | null = null;
		api.fircGetGroups.mockImplementation(() => {
			groupsCalls++;
			if (groupsCalls === 1) return Promise.resolve(structuredClone(groups));
			return new Promise<FircGroup[]>((resolve) => {
				resolveReloadGroups = resolve;
			});
		});
		api.fircGetInterfaces.mockImplementation(async () => [
			{ id: 'eth3', name: 'WAN', role: 'wan' },
			{ id: 'tunvless0', name: 'DE', role: 'tunnel' },
		]);

		render(FircGroupsPanel);
		await screen.findByText('One');

		// Открываем модалку создания на чистой панели, пока в фоне стартует silent reload по инвалидации firc.
		await fireEvent.click(screen.getByRole('button', { name: m.firc_groups_add_group() }));
		invalidateResource('firc', 'ext-updated');
		await waitFor(() => expect(groupsCalls).toBeGreaterThanOrEqual(2));

		// Пользователь создаёт правило ДО завершения фоновой перезагрузки.
		await fireEvent.input(screen.getByLabelText(m.firc_groups_form_name()), { target: { value: 'CreatedMidReload' } });
		await fireEvent.change(screen.getByLabelText(m.firc_groups_form_iface()), { target: { value: 'pg:pg' } });
		await fireEvent.click(screen.getByRole('button', { name: m.firc_groups_modal_create_btn() }));

		// Фоновая перезагрузка завершается со старым списком с сервера — новое правило не должно пропасть.
		resolveReloadGroups!(structuredClone(groups));
		await screen.findByText('CreatedMidReload');

		const saveBtn = (await screen.findAllByRole('button', { name: m.firc_groups_save_changes() }))[0] as HTMLButtonElement;
		await waitFor(() => expect(saveBtn.disabled).toBe(false));
		await fireEvent.click(saveBtn);
		await waitFor(() => expect(api.fircSaveGroups).toHaveBeenCalledTimes(1));

		const [payload, , links] = api.fircSaveGroups.mock.calls[0];
		const created = (payload as FircGroup[]).find((g) => g.name === 'CreatedMidReload');
		expect(created).toBeDefined();
		expect(links[created!.id]).toBe('pg');
	});

	it('uses fresher extConfig from concurrent refreshProxyGroups when loadAll ext fetch fails', async () => {
		let resolveInitialGroups: ((v: FircGroup[]) => void) | null = null;
		let extCalls = 0;
		api.fircGetGroups.mockImplementation(
			() =>
				new Promise<FircGroup[]>((resolve) => {
					resolveInitialGroups = resolve;
				}),
		);
		api.fircGetExt.mockImplementation(() => {
			extCalls++;
			if (extCalls === 1) return Promise.reject(new Error('loadAll ext failed'));
			return Promise.resolve(structuredClone(ext));
		});
		api.fircGetInterfaces.mockImplementation(async () => [
			{ id: 'eth3', name: 'WAN', role: 'wan' },
			{ id: 'tunvless0', name: 'DE', role: 'tunnel' },
		]);

		render(FircGroupsPanel);
		// Во время первичного loadAll открываем модалку: refreshProxyGroups успевает успешно загрузить ext раньше loadAll.
		await fireEvent.click(screen.getByRole('button', { name: m.firc_groups_add_group() }));
		await waitFor(() => expect(extCalls).toBeGreaterThanOrEqual(2));

		resolveInitialGroups!(structuredClone(groups));
		await screen.findByText('One');

		await fireEvent.input(screen.getByLabelText(m.firc_groups_form_name()), { target: { value: 'ConcurrentPG' } });
		await fireEvent.change(screen.getByLabelText(m.firc_groups_form_iface()), { target: { value: 'pg:pg' } });
		await fireEvent.click(screen.getByRole('button', { name: m.firc_groups_modal_create_btn() }));

		await fireEvent.click((await screen.findAllByRole('button', { name: m.firc_groups_save_changes() }))[0]);
		await waitFor(() => expect(api.fircSaveGroups).toHaveBeenCalledTimes(1));

		const [payload, , links] = api.fircSaveGroups.mock.calls[0];
		const created = (payload as FircGroup[]).find((g) => g.name === 'ConcurrentPG');
		expect(created).toBeDefined();
		expect(links.g1).toBe('pg');
		expect(links[created!.id]).toBe('pg');
	});

	it('hydrates server groupLinks when user edits during silent loadAll after initial fircGetExt failure', async () => {
		let groupsCalls = 0;
		let extCalls = 0;
		let resolveReloadGroups: ((v: FircGroup[]) => void) | null = null;
		const reorderedExt: FircExtConfig = {
			...structuredClone(ext),
			proxyGroups: [{ ...structuredClone(ext.proxyGroups[0]), interfaces: ['tunvless0', 'eth3'] }],
		};
		const reorderedGroups: FircGroup[] = structuredClone(groups).map((g) =>
			g.id === 'g1' ? { ...g, interface: ['tunvless0', 'eth3'] } : g,
		);
		api.fircGetGroups.mockImplementation(() => {
			groupsCalls++;
			if (groupsCalls === 1) return Promise.resolve(structuredClone(groups));
			return new Promise<FircGroup[]>((resolve) => {
				resolveReloadGroups = resolve;
			});
		});
		api.fircGetExt.mockImplementation(() => {
			extCalls++;
			if (extCalls === 1) return Promise.reject(new Error('initial ext failure'));
			return Promise.resolve(structuredClone(reorderedExt));
		});
		api.fircGetInterfaces.mockImplementation(async () => [
			{ id: 'eth3', name: 'WAN', role: 'wan' },
			{ id: 'tunvless0', name: 'DE', role: 'tunnel' },
		]);

		render(FircGroupsPanel);
		await screen.findByText('One');

		// Фоновая перезагрузка стартует, пока панель чистая и baselineLinksLoaded === false.
		invalidateResource('firc', 'ext-updated');
		await waitFor(() => expect(groupsCalls).toBeGreaterThanOrEqual(2));

		// Пользователь делает локальную правку (переставляет группы), пока loadAll(true) в полёте.
		const down = await screen.findAllByTitle(m.firc_groups_move_down());
		await fireEvent.click(down[0]);

		// loadAll(true) завершается с валидными groupLinks и обновлённым порядком pg.
		resolveReloadGroups!(reorderedGroups);
		const saveBtn = (await screen.findAllByRole('button', { name: m.firc_groups_save_changes() }))[0] as HTMLButtonElement;
		await waitFor(() => expect(saveBtn.disabled).toBe(false));
		await fireEvent.click(saveBtn);
		await waitFor(() => expect(api.fircSaveGroups).toHaveBeenCalledTimes(1));

		const [payload, , links] = api.fircSaveGroups.mock.calls[0];
		const savedG1 = (payload as FircGroup[]).find((g) => g.id === 'g1');
		expect(savedG1?.interface).toEqual(['tunvless0', 'eth3']);
		expect(links.g1).toBe('pg');
	});

	it('seeds extConfig when initial loadAll resolves before a concurrent refreshProxyGroups that later fails', async () => {
		let resolveInitialExt: ((v: FircExtConfig) => void) | null = null;
		let rejectConcurrentExt: ((e: Error) => void) | null = null;
		let extCalls = 0;
		api.fircGetExt.mockImplementation(() => {
			extCalls++;
			if (extCalls === 1) {
				return new Promise<FircExtConfig>((resolve) => {
					resolveInitialExt = resolve;
				});
			}
			return new Promise<FircExtConfig>((_, reject) => {
				rejectConcurrentExt = reject;
			});
		});
		api.fircGetInterfaces.mockImplementation(async () => [
			{ id: 'eth3', name: 'WAN', role: 'wan' },
			{ id: 'tunvless0', name: 'DE', role: 'tunnel' },
		]);

		render(FircGroupsPanel);
		await fireEvent.click(screen.getByRole('button', { name: m.firc_groups_add_group() }));
		await waitFor(() => expect(extCalls).toBe(2));

		// Первичный loadAll завершается первым, а конкурентный refreshProxyGroups падает.
		resolveInitialExt!(structuredClone(ext));
		await screen.findByText('One');
		rejectConcurrentExt!(new Error('refresh failed'));

		// extConfig должен остаться заполненным: прокси-группа доступна в селекторе, а g1 сохраняет связь.
		await fireEvent.input(screen.getByLabelText(m.firc_groups_form_name()), { target: { value: 'SeededPG' } });
		await fireEvent.change(screen.getByLabelText(m.firc_groups_form_iface()), { target: { value: 'pg:pg' } });
		await fireEvent.click(screen.getByRole('button', { name: m.firc_groups_modal_create_btn() }));

		await fireEvent.click((await screen.findAllByRole('button', { name: m.firc_groups_save_changes() }))[0]);
		await waitFor(() => expect(api.fircSaveGroups).toHaveBeenCalledTimes(1));

		const [payload, , links] = api.fircSaveGroups.mock.calls[0];
		const created = (payload as FircGroup[]).find((g) => g.name === 'SeededPG');
		expect(created?.interface).toEqual(['eth3', 'tunvless0']);
		expect(links.g1).toBe('pg');
		expect(links[created!.id]).toBe('pg');
	});

	it('syncs baseline and groups when concurrent refreshProxyGroups returns a reordered PG after initial loadAll', async () => {
		let resolveInitialExt: ((v: FircExtConfig) => void) | null = null;
		let resolveConcurrentExt: ((v: FircExtConfig) => void) | null = null;
		let extCalls = 0;
		api.fircGetExt.mockImplementation(() => {
			extCalls++;
			if (extCalls === 1) {
				return new Promise<FircExtConfig>((resolve) => {
					resolveInitialExt = resolve;
				});
			}
			return new Promise<FircExtConfig>((resolve) => {
				resolveConcurrentExt = resolve;
			});
		});
		api.fircGetInterfaces.mockImplementation(async () => [
			{ id: 'eth3', name: 'WAN', role: 'wan' },
			{ id: 'tunvless0', name: 'DE', role: 'tunnel' },
		]);

		render(FircGroupsPanel);
		await fireEvent.click(screen.getByRole('button', { name: m.firc_groups_add_group() }));
		await waitFor(() => expect(extCalls).toBe(2));

		resolveInitialExt!(structuredClone(ext));
		await screen.findByText('One');
		await fireEvent.click(screen.getByRole('button', { name: m.common_cancel() }));

		// Конкурентный refreshProxyGroups приходит с новым порядком туннелей — синхронизирует и groups, и baseline.
		const reorderedExt: FircExtConfig = {
			...structuredClone(ext),
			proxyGroups: [{ ...structuredClone(ext.proxyGroups[0]), interfaces: ['tunvless0', 'eth3'] }],
		};
		resolveConcurrentExt!(reorderedExt);
		await waitFor(() => expect(screen.queryByRole('button', { name: m.firc_groups_save_changes() })).toBeNull());

		const down = await screen.findAllByTitle(m.firc_groups_move_down());
		await fireEvent.click(down[0]);
		await fireEvent.click((await screen.findAllByRole('button', { name: m.firc_groups_save_changes() }))[0]);
		await waitFor(() => expect(api.fircSaveGroups).toHaveBeenCalledTimes(1));

		const [payload, , links] = api.fircSaveGroups.mock.calls[0];
		const savedG1 = (payload as FircGroup[]).find((g) => g.id === 'g1');
		expect(savedG1?.interface).toEqual(['tunvless0', 'eth3']);
		expect(links.g1).toBe('pg');
	});

	it('keeps authoritative extConfig after saveProxyGroupsModal even if fircGetExt would fail', async () => {
		let currentGroups = structuredClone(groups);
		let currentExt = structuredClone(ext);
		let failExt = false;
		api.fircGetGroups.mockImplementation(async () => structuredClone(currentGroups));
		api.fircGetExt.mockImplementation(async () => {
			if (failExt) throw new Error('transient ext outage');
			return structuredClone(currentExt);
		});
		api.fircGetInterfaces.mockImplementation(async () => [
			{ id: 'eth3', name: 'WAN', role: 'wan' },
			{ id: 'tunvless0', name: 'DE', role: 'tunnel' },
		]);
		api.fircSaveExt.mockImplementation(async (nextExt: FircExtConfig) => {
			const plain = JSON.parse(JSON.stringify(nextExt)) as FircExtConfig;
			currentExt = { ...plain, groupLinks: currentExt.groupLinks };
			const updatedPg = currentExt.proxyGroups.find((p) => p.id === 'pg');
			if (updatedPg) {
				currentGroups = currentGroups.map((g) =>
					g.id === 'g1' ? { ...g, interface: [...updatedPg.interfaces] } : g,
				);
			}
			failExt = true;
			return structuredClone(currentExt);
		});

		render(FircGroupsPanel);
		await screen.findByText('One');

		// Создаём несохранённое правило на pg.
		await fireEvent.click(screen.getByRole('button', { name: m.firc_groups_add_group() }));
		await fireEvent.input(screen.getByLabelText(m.firc_groups_form_name()), { target: { value: 'KnownExtDraft' } });
		await fireEvent.change(screen.getByLabelText(m.firc_groups_form_iface()), { target: { value: 'pg:pg' } });
		await fireEvent.click(screen.getByRole('button', { name: m.firc_groups_modal_create_btn() }));

		// Сохраняем новый порядок PG — fircGetExt после fircSaveExt уже недоступен, но loadAll получает knownExt.
		await fireEvent.click(screen.getByTitle(m.firc_fallback_configure_chain()));
		const moveDownBtns = await screen.findAllByTitle(m.firc_groups_pg_move_down());
		await fireEvent.click(moveDownBtns[0]);
		const extCallsBeforeSave = api.fircGetExt.mock.calls.length;
		await fireEvent.click(screen.getByRole('button', { name: m.common_save() }));

		await waitFor(() => expect(api.fircSaveExt).toHaveBeenCalledTimes(1));
		await waitFor(() => expect(screen.queryByRole('button', { name: m.common_save() })).toBeNull());
		expect(api.fircGetExt.mock.calls.length).toBe(extCallsBeforeSave);

		const saveBtn = (await screen.findAllByRole('button', { name: m.firc_groups_save_changes() }))[0] as HTMLButtonElement;
		await waitFor(() => expect(saveBtn.disabled).toBe(false));
		await fireEvent.click(saveBtn);
		await waitFor(() => expect(api.fircSaveGroups).toHaveBeenCalledTimes(1));

		const [payload, , links] = api.fircSaveGroups.mock.calls[0];
		const savedG1 = (payload as FircGroup[]).find((g) => g.id === 'g1');
		const savedDraft = (payload as FircGroup[]).find((g) => g.name === 'KnownExtDraft');
		expect(savedG1?.interface).toEqual(['tunvless0', 'eth3']);
		expect(savedDraft?.interface).toEqual(['tunvless0', 'eth3']);
		expect(links.g1).toBe('pg');
		expect(links[savedDraft!.id]).toBe('pg');
	});

	it('applies fresh loadedExt on subsequent loadAll(true) when concurrent refreshProxyGroups fails later', async () => {
		const reorderedExt: FircExtConfig = {
			...structuredClone(ext),
			proxyGroups: [{ ...structuredClone(ext.proxyGroups[0]), interfaces: ['tunvless0', 'eth3'] }],
		};
		const reorderedGroups: FircGroup[] = structuredClone(groups).map((g) =>
			g.id === 'g1' ? { ...g, interface: ['tunvless0', 'eth3'] } : g,
		);
		let groupsCalls = 0;
		let extCalls = 0;
		let resolveReloadExt: ((v: FircExtConfig) => void) | null = null;
		let rejectModalExt: ((e: Error) => void) | null = null;
		api.fircGetGroups.mockImplementation(() => {
			groupsCalls++;
			if (groupsCalls === 1) return Promise.resolve(structuredClone(groups));
			return Promise.resolve(structuredClone(reorderedGroups));
		});
		api.fircGetExt.mockImplementation(() => {
			extCalls++;
			if (extCalls === 1) return Promise.resolve(structuredClone(ext));
			if (extCalls === 2) {
				return new Promise<FircExtConfig>((resolve) => {
					resolveReloadExt = resolve;
				});
			}
			return new Promise<FircExtConfig>((_, reject) => {
				rejectModalExt = reject;
			});
		});
		api.fircGetInterfaces.mockImplementation(async () => [
			{ id: 'eth3', name: 'WAN', role: 'wan' },
			{ id: 'tunvless0', name: 'DE', role: 'tunnel' },
		]);

		render(FircGroupsPanel);
		await screen.findByText('One');

		// Фоновый loadAll(true) (extReq = 2) стартует после внешнего реордера PG.
		invalidateResource('firc', 'ext-updated');
		await waitFor(() => expect(extCalls).toBe(2));

		// Пока loadAll(true) в полёте, открытие модалки запускает refreshProxyGroups (request = 3).
		await fireEvent.click(screen.getByRole('button', { name: m.firc_groups_add_group() }));
		await waitFor(() => expect(extCalls).toBe(3));

		// loadAll(true) завершается первым со свежим extConfig, а более поздний refreshProxyGroups падает.
		resolveReloadExt!(structuredClone(reorderedExt));
		await waitFor(() => expect(groupsCalls).toBe(2));
		rejectModalExt!(new Error('refresh failed'));

		// Свежий extConfig должен сохраниться: новая группа на pg:pg получает новый порядок.
		await fireEvent.input(screen.getByLabelText(m.firc_groups_form_name()), { target: { value: 'FreshAfterRace' } });
		await fireEvent.change(screen.getByLabelText(m.firc_groups_form_iface()), { target: { value: 'pg:pg' } });
		await fireEvent.click(screen.getByRole('button', { name: m.firc_groups_modal_create_btn() }));

		const saveBtn = (await screen.findAllByRole('button', { name: m.firc_groups_save_changes() }))[0] as HTMLButtonElement;
		await waitFor(() => expect(saveBtn.disabled).toBe(false));
		await fireEvent.click(saveBtn);
		await waitFor(() => expect(api.fircSaveGroups).toHaveBeenCalledTimes(1));

		const [payload, , links] = api.fircSaveGroups.mock.calls[0];
		const created = (payload as FircGroup[]).find((g) => g.name === 'FreshAfterRace');
		expect(created?.interface).toEqual(['tunvless0', 'eth3']);
		expect(links.g1).toBe('pg');
		expect(links[created!.id]).toBe('pg');
	});

	it('fetches fresh server groups on first refreshProxyGroups after initial fircGetExt failure so dirty draft syncs PG reorder', async () => {
		const reorderedExt: FircExtConfig = {
			...structuredClone(ext),
			proxyGroups: [{ ...structuredClone(ext.proxyGroups[0]), interfaces: ['tunvless0', 'eth3'] }],
		};
		const reorderedGroups: FircGroup[] = structuredClone(groups).map((g) =>
			g.id === 'g1' ? { ...g, interface: ['tunvless0', 'eth3'] } : g,
		);
		let currentGroups = structuredClone(groups);
		let extCalls = 0;
		api.fircGetGroups.mockImplementation(async () => structuredClone(currentGroups));
		api.fircGetExt.mockImplementation(() => {
			extCalls++;
			if (extCalls === 1) return Promise.reject(new Error('initial ext outage'));
			return Promise.resolve(structuredClone(reorderedExt));
		});
		api.fircGetInterfaces.mockImplementation(async () => [
			{ id: 'eth3', name: 'WAN', role: 'wan' },
			{ id: 'tunvless0', name: 'DE', role: 'tunnel' },
		]);

		render(FircGroupsPanel);
		await screen.findByText('One');

		// Делаем панель dirty до первого успешного чтения ext.
		const down = await screen.findAllByTitle(m.firc_groups_move_down());
		await fireEvent.click(down[0]);

		// Сервер переупорядочивает pg и g1; инвалидация вызывает refreshProxyGroups(true).
		currentGroups = reorderedGroups;
		invalidateResource('firc', 'ext-updated');
		await waitFor(() =>
			expect(sessionStorage.getItem('awgm_firc_groups_draft_v1')).toContain('["tunvless0","eth3"]'),
		);

		await fireEvent.click((await screen.findAllByRole('button', { name: m.firc_groups_save_changes() }))[0]);
		await waitFor(() => expect(api.fircSaveGroups).toHaveBeenCalledTimes(1));

		const [payload, , links] = api.fircSaveGroups.mock.calls[0];
		const savedG1 = (payload as FircGroup[]).find((g) => g.id === 'g1');
		expect(savedG1?.interface).toEqual(['tunvless0', 'eth3']);
		expect(links.g1).toBe('pg');
	});

	it('does not corrupt open edit modal target when external firc invalidation inserts a group at index 0', async () => {
		let currentGroups = structuredClone(groups);
		api.fircGetGroups.mockImplementation(async () => structuredClone(currentGroups));
		api.fircGetInterfaces.mockImplementation(async () => [
			{ id: 'eth3', name: 'WAN', role: 'wan' },
			{ id: 'tunvless0', name: 'DE', role: 'tunnel' },
		]);

		render(FircGroupsPanel);
		await screen.findByText('One');

		// Открываем редактирование g2 (индекс 1) на чистой панели.
		const settingsBtns = screen.getAllByTitle(m.firc_groups_settings_tip());
		await fireEvent.click(settingsBtns[1]);

		// Внешнее событие добавляет группу в начало списка; при открытой модалке редактирования
		// onResourceInvalidated не должен сдвигать индексы через loadAll(true).
		currentGroups = [
			{ id: 'c100d002', name: 'Global', interface: 'eth3', enable: true, rules: [] },
			...structuredClone(groups),
		];
		const groupsCallsBefore = api.fircGetGroups.mock.calls.length;
		const extCallsBefore = api.fircGetExt.mock.calls.length;
		invalidateResource('firc', 'ext-updated');
		await waitFor(() => expect(api.fircGetExt.mock.calls.length).toBeGreaterThan(extCallsBefore));
		expect(api.fircGetGroups.mock.calls.length).toBe(groupsCallsBefore);

		await fireEvent.input(screen.getByLabelText(m.firc_groups_form_name()), { target: { value: 'TwoRenamed' } });
		await fireEvent.click(screen.getByRole('button', { name: m.common_apply() }));
		await fireEvent.click((await screen.findAllByRole('button', { name: m.firc_groups_save_changes() }))[0]);
		await waitFor(() => expect(api.fircSaveGroups).toHaveBeenCalledTimes(1));

		const [payload] = api.fircSaveGroups.mock.calls[0];
		expect((payload as FircGroup[]).find((g) => g.id === 'g1')?.name).toBe('One');
		expect((payload as FircGroup[]).find((g) => g.id === 'g2')?.name).toBe('TwoRenamed');
	});

	it('preserves groupLinks when background loadAll(true) hits a transient fircGetExt failure', async () => {
		let extCalls = 0;
		api.fircGetExt.mockImplementation(() => {
			extCalls++;
			if (extCalls === 1) return Promise.resolve(structuredClone(ext));
			return Promise.reject(new Error('transient ext failure'));
		});
		api.fircGetInterfaces.mockImplementation(async () => [
			{ id: 'eth3', name: 'WAN', role: 'wan' },
			{ id: 'tunvless0', name: 'DE', role: 'tunnel' },
		]);

		render(FircGroupsPanel);
		await screen.findByText('One');

		// Фоновый loadAll(true) получает groups, но fircGetExt временно падает — связи и baselineLinksLoaded не сбрасываются.
		invalidateResource('firc', 'ext-updated');
		await waitFor(() => expect(extCalls).toBe(2));

		await fireEvent.click(screen.getByRole('button', { name: m.firc_groups_add_group() }));
		await fireEvent.input(screen.getByLabelText(m.firc_groups_form_name()), { target: { value: 'AfterTransientFail' } });
		await fireEvent.change(screen.getByLabelText(m.firc_groups_form_iface()), { target: { value: 'pg:pg' } });
		await fireEvent.click(screen.getByRole('button', { name: m.firc_groups_modal_create_btn() }));

		const saveBtn = (await screen.findAllByRole('button', { name: m.firc_groups_save_changes() }))[0] as HTMLButtonElement;
		await waitFor(() => expect(saveBtn.disabled).toBe(false));
		await fireEvent.click(saveBtn);
		await waitFor(() => expect(api.fircSaveGroups).toHaveBeenCalledTimes(1));

		const [payload, , links] = api.fircSaveGroups.mock.calls[0];
		const created = (payload as FircGroup[]).find((g) => g.name === 'AfterTransientFail');
		expect(created?.interface).toEqual(['eth3', 'tunvless0']);
		expect(links.g1).toBe('pg');
		expect(links[created!.id]).toBe('pg');
	});

	it('does not advance extConfig while saveAllChanges is in flight so a failed save still syncs PG reorder on retry', async () => {
		const reorderedExt: FircExtConfig = {
			...structuredClone(ext),
			proxyGroups: [{ ...structuredClone(ext.proxyGroups[0]), interfaces: ['tunvless0', 'eth3'] }],
		};
		let extCalls = 0;
		let resolveRefreshWhileSaving: ((v: FircExtConfig) => void) | null = null;
		let rejectFirstSave: ((e: Error) => void) | null = null;
		let saveCalls = 0;
		api.fircGetExt.mockImplementation(() => {
			extCalls++;
			if (extCalls === 1) return Promise.resolve(structuredClone(ext));
			if (extCalls === 2) {
				return new Promise<FircExtConfig>((resolve) => {
					resolveRefreshWhileSaving = resolve;
				});
			}
			return Promise.resolve(structuredClone(reorderedExt));
		});
		api.fircSaveGroups.mockImplementation((g: FircGroup[]) => {
			saveCalls++;
			if (saveCalls === 1) {
				return new Promise((_, reject) => {
					rejectFirstSave = reject;
				});
			}
			return Promise.resolve({ groups: g });
		});

		render(FircGroupsPanel);
		await screen.findByText('One');

		// Делаем правку и запускаем фоновый refreshProxyGroups до нажатия «Сохранить».
		const down = await screen.findAllByTitle(m.firc_groups_move_down());
		await fireEvent.click(down[0]);
		invalidateResource('firc', 'ext-updated');
		await waitFor(() => expect(extCalls).toBe(2));

		// Нажимаем «Сохранить» — пока saveAllChanges в полёте, refreshProxyGroups #2 завершается с новым порядком,
		// но не должен сдвигать extConfig/extAppliedReq без синхронизации groups.
		await fireEvent.click((await screen.findAllByRole('button', { name: m.firc_groups_save_changes() }))[0]);
		await waitFor(() => expect(saveCalls).toBe(1));
		resolveRefreshWhileSaving!(structuredClone(reorderedExt));
		await Promise.resolve();

		// Первое сохранение падает; следующее обновление ext должно увидеть diff и перенести новый порядок в g1.
		rejectFirstSave!(new Error('save failed'));
		const saveBtn = (await screen.findAllByRole('button', { name: m.firc_groups_save_changes() }))[0] as HTMLButtonElement;
		await waitFor(() => expect(saveBtn.disabled).toBe(false));

		invalidateResource('firc', 'ext-updated');
		await waitFor(() =>
			expect(sessionStorage.getItem('awgm_firc_groups_draft_v1')).toContain('["tunvless0","eth3"]'),
		);

		await fireEvent.click(saveBtn);
		await waitFor(() => expect(api.fircSaveGroups).toHaveBeenCalledTimes(2));

		const [payload, , links] = api.fircSaveGroups.mock.calls[1];
		const savedG1 = (payload as FircGroup[]).find((g) => g.id === 'g1');
		expect(savedG1?.interface).toEqual(['tunvless0', 'eth3']);
		expect(links.g1).toBe('pg');
	});

	it('syncs reordered PG chain on first refreshProxyGroups even when modalOrderChanged keeps edit modal indices intact', async () => {
		const reorderedExt: FircExtConfig = {
			...structuredClone(ext),
			proxyGroups: [{ ...structuredClone(ext.proxyGroups[0]), interfaces: ['tunvless0', 'eth3'] }],
		};
		let currentGroups = structuredClone(groups);
		let extCalls = 0;
		api.fircGetGroups.mockImplementation(async () => structuredClone(currentGroups));
		api.fircGetExt.mockImplementation(() => {
			extCalls++;
			if (extCalls === 1) return Promise.reject(new Error('initial ext failure'));
			return Promise.resolve(structuredClone(reorderedExt));
		});
		api.fircGetInterfaces.mockImplementation(async () => [
			{ id: 'eth3', name: 'WAN', role: 'wan' },
			{ id: 'tunvless0', name: 'DE', role: 'tunnel' },
		]);

		render(FircGroupsPanel);
		await screen.findByText('One');

		// На сервере одновременно появилась группа Global в индексе 0 и переупорядочилась pg (и g1).
		currentGroups = [
			{ id: 'c100d002', name: 'Global', interface: 'eth3', enable: true, rules: [] },
			...structuredClone(groups).map((g) => (g.id === 'g1' ? { ...g, interface: ['tunvless0', 'eth3'] } : g)),
		];

		// Открываем редактирование g2 (индекс 1): refreshProxyGroups(true) попадает в modalOrderChanged,
		// но всё равно должен синхронизировать g1 на новый порядок без сдвига индекса g2.
		const settingsBtns = screen.getAllByTitle(m.firc_groups_settings_tip());
		await fireEvent.click(settingsBtns[1]);
		await waitFor(() => expect(extCalls).toBe(2));

		await fireEvent.input(screen.getByLabelText(m.firc_groups_form_name()), { target: { value: 'TwoKeptIdx' } });
		await fireEvent.click(screen.getByRole('button', { name: m.common_apply() }));
		await fireEvent.click((await screen.findAllByRole('button', { name: m.firc_groups_save_changes() }))[0]);
		await waitFor(() => expect(api.fircSaveGroups).toHaveBeenCalledTimes(1));

		const [payload, , links] = api.fircSaveGroups.mock.calls[0];
		const savedG1 = (payload as FircGroup[]).find((g) => g.id === 'g1');
		const savedG2 = (payload as FircGroup[]).find((g) => g.id === 'g2');
		expect(savedG1?.name).toBe('One');
		expect(savedG1?.interface).toEqual(['tunvless0', 'eth3']);
		expect(savedG2?.name).toBe('TwoKeptIdx');
		expect(links.g1).toBe('pg');
	});

	it('upgrades open edit modal from raw primary iface to pg:<id> when initial link hydration arrives', async () => {
		const reorderedExt: FircExtConfig = {
			...structuredClone(ext),
			proxyGroups: [{ ...structuredClone(ext.proxyGroups[0]), interfaces: ['tunvless0', 'eth3'] }],
		};
		let currentGroups = structuredClone(groups);
		let extCalls = 0;
		api.fircGetGroups.mockImplementation(async () => structuredClone(currentGroups));
		api.fircGetExt.mockImplementation(() => {
			extCalls++;
			if (extCalls === 1) return Promise.reject(new Error('initial ext failure'));
			return Promise.resolve(structuredClone(reorderedExt));
		});
		api.fircGetInterfaces.mockImplementation(async () => [
			{ id: 'eth3', name: 'WAN', role: 'wan' },
			{ id: 'tunvless0', name: 'DE', role: 'tunnel' },
		]);

		render(FircGroupsPanel);
		await screen.findByText('One');

		// На сервере pg и g1 уже имеют новый порядок; открываем настройки g1 до прихода extConfig.
		currentGroups = structuredClone(groups).map((g) =>
			g.id === 'g1' ? { ...g, interface: ['tunvless0', 'eth3'] } : g,
		);
		const settingsBtns = screen.getAllByTitle(m.firc_groups_settings_tip());
		await fireEvent.click(settingsBtns[0]);

		// После первичной гидратации селектор в открытой модалке переключается на pg:pg,
		// и простое переименование g1 сохраняет новый порядок и связь с pg.
		const ifaceSelect = screen.getByLabelText<HTMLSelectElement>(m.firc_groups_form_iface());
		await waitFor(() => expect(ifaceSelect.value).toBe('pg:pg'));

		await fireEvent.input(screen.getByLabelText(m.firc_groups_form_name()), { target: { value: 'OneRenamed' } });
		await fireEvent.click(screen.getByRole('button', { name: m.common_apply() }));
		await fireEvent.click((await screen.findAllByRole('button', { name: m.firc_groups_save_changes() }))[0]);
		await waitFor(() => expect(api.fircSaveGroups).toHaveBeenCalledTimes(1));

		const [payload, , links] = api.fircSaveGroups.mock.calls[0];
		const savedG1 = (payload as FircGroup[]).find((g) => g.id === 'g1');
		expect(savedG1?.name).toBe('OneRenamed');
		expect(savedG1?.interface).toEqual(['tunvless0', 'eth3']);
		expect(links.g1).toBe('pg');
	});

	it('refreshes proxy groups on fircSyncNonce even when panel is dirty with no edit modal open', async () => {
		let currentExt = structuredClone(ext);
		api.fircGetExt.mockImplementation(async () => structuredClone(currentExt));

		render(FircGroupsPanel);
		await screen.findByText('One');

		const down = await screen.findAllByTitle(m.firc_groups_move_down());
		await fireEvent.click(down[0]);

		currentExt = {
			...currentExt,
			proxyGroups: [{ ...currentExt.proxyGroups[0], interfaces: ['tunvless0', 'eth3'] }],
		};
		await reloadFircAll();
		await waitFor(() =>
			expect(sessionStorage.getItem('awgm_firc_groups_draft_v1')).toContain('["tunvless0","eth3"]'),
		);

		await fireEvent.click((await screen.findAllByRole('button', { name: m.firc_groups_save_changes() }))[0]);
		await waitFor(() => expect(api.fircSaveGroups).toHaveBeenCalledTimes(1));

		const [payload, , links] = api.fircSaveGroups.mock.calls[0];
		const savedG1 = (payload as FircGroup[]).find((g) => g.id === 'g1');
		expect(savedG1?.interface).toEqual(['tunvless0', 'eth3']);
		expect(links.g1).toBe('pg');
	});
});

describe('FircGroupsPanel: правки приёмки #1308', () => {
	beforeEach(() => {
		localStorage.clear();
		sessionStorage.clear();
		localStorage.setItem('awgm.firc.groupView', 'list');
		api.fircGetGroupsState.mockImplementation(async () => ({ groups: {} }));
		api.fircSaveGroups.mockReset();
		api.fircSaveGroups.mockImplementation(async (g: FircGroup[]) => ({ groups: g }));
		api.fircGetInterfaces.mockImplementation(async () => [
			{ id: 'eth3', name: 'WAN', role: 'wan' },
			{ id: 'tunvless0', name: 'DE', role: 'tunnel' },
		]);
	});
	afterEach(() => cleanup());

	it('после отказа сохранения прокси-группы перечитывает сервер и не возвращает старую цепочку', async () => {
		let currentGroups = structuredClone(groups);
		let currentExt = structuredClone(ext);
		api.fircGetGroups.mockReset();
		api.fircGetGroups.mockImplementation(async () => structuredClone(currentGroups));
		api.fircGetExt.mockReset();
		api.fircGetExt.mockImplementation(async () => structuredClone(currentExt));
		api.fircSaveExt.mockReset();
		// Бэкенд применил новую цепочку и обновил связанную g1, но упал на позднем шаге.
		api.fircSaveExt.mockImplementation(async (nextExt: FircExtConfig) => {
			const plain = JSON.parse(JSON.stringify(nextExt)) as FircExtConfig;
			currentExt = { ...plain, groupLinks: currentExt.groupLinks };
			const pg = currentExt.proxyGroups.find((p) => p.id === 'pg')!;
			currentGroups = currentGroups.map((g) => (g.id === 'g1' ? { ...g, interface: [...pg.interfaces] } : g));
			throw new Error('reconcile netfilter');
		});

		render(FircGroupsPanel);
		await screen.findByText('One');
		await fireEvent.click(screen.getByTitle(m.firc_fallback_configure_chain()));
		await fireEvent.click((await screen.findAllByTitle(m.firc_groups_pg_move_down()))[0]);
		await fireEvent.click(screen.getByRole('button', { name: m.common_save() }));
		await waitFor(() => expect(api.fircSaveExt).toHaveBeenCalledTimes(1));
		await fireEvent.click(screen.getByRole('button', { name: m.common_cancel() }));

		await fireEvent.click((await screen.findAllByTitle(m.firc_groups_move_down()))[0]);
		const saveBtn = (await screen.findAllByRole('button', { name: m.firc_groups_save_changes() }))[0] as HTMLButtonElement;
		await waitFor(() => expect(saveBtn.disabled).toBe(false));
		await fireEvent.click(saveBtn);
		await waitFor(() => expect(api.fircSaveGroups).toHaveBeenCalledTimes(1));
		const [payload] = api.fircSaveGroups.mock.calls[0];
		const g1 = (payload as FircGroup[]).find((g) => g.id === 'g1');
		expect(g1?.interface).toEqual(['tunvless0', 'eth3']);
	});

	it('окно спецгруппы не предлагает прокси-группы', async () => {
		const withGlobal: FircGroup[] = [
			...structuredClone(groups),
			{ id: 'c100d002', name: 'Global', interface: ['eth3', 'tunvless0'], enable: true, rules: [] },
		];
		api.fircGetGroups.mockReset();
		api.fircGetGroups.mockImplementation(async () => structuredClone(withGlobal));
		api.fircGetExt.mockReset();
		api.fircGetExt.mockImplementation(async () => structuredClone(ext));

		render(FircGroupsPanel);
		await screen.findByText('Global');
		const settings = screen.getAllByTitle(m.firc_groups_settings_tip());
		await fireEvent.click(settings[settings.length - 1]);
		const select = screen.getByLabelText<HTMLSelectElement>(m.firc_groups_form_iface());
		expect(Array.from(select.options).map((o) => o.value)).not.toContain('pg:pg');
	});
});

