import { describe, expect, it, vi } from 'vitest';
import { render, screen, fireEvent } from '@testing-library/svelte';
import { api } from '$lib/api/client';
import type { HydraRouteConfig } from '$lib/types';
import HrNeoSettingsView from './HrNeoSettingsView.svelte';

vi.mock('$lib/api/client', () => ({
	api: { getHydraRouteConfig: vi.fn(), updateHydraRouteConfig: vi.fn() },
}));

const cfg: HydraRouteConfig = {
	autoStart: false,
	clearIPSet: false,
	cidr: true,
	ipsetEnableTimeout: false,
	ipsetTimeout: 300,
	ipsetMaxElem: 65536,
	directRouteEnabled: false,
	globalRouting: false,
	conntrackFlush: false,
	log: 'info',
	logFile: '',
	geoIPFiles: [],
	geoSiteFiles: [],
	policyOrder: ['A', 'B'],
};

describe('HrNeoSettingsView', () => {
	// Снимок вкладки старее файла: порядок политик могли поменять после
	// открытия. Сохранение шлёт только изменённое поле (F728).
	it('sends only the changed field', async () => {
		vi.mocked(api.getHydraRouteConfig).mockResolvedValue(cfg);
		vi.mocked(api.updateHydraRouteConfig).mockResolvedValue({ ...cfg, autoStart: true });
		const { container } = render(HrNeoSettingsView, { props: { embedded: true } });
		await vi.waitFor(() => expect(container.querySelector('input[type="checkbox"]')).toBeTruthy());

		await fireEvent.input(container.querySelector('input[type="checkbox"]') as HTMLInputElement, {
			target: { checked: true },
		});
		await fireEvent.click(await screen.findByRole('button', { name: /Сохранить|Save/ }));

		expect(api.updateHydraRouteConfig).toHaveBeenCalledWith({ autoStart: true });
	});

	// Правка во время сохранения не стирается ответом и уходит следующим
	// сохранением; отправленное при ошибке возвращается в очередь.
	it('keeps an edit made while saving and resends on failure', async () => {
		vi.mocked(api.getHydraRouteConfig).mockResolvedValue(cfg);
		let resolveFirst: (v: HydraRouteConfig) => void = () => {};
		vi.mocked(api.updateHydraRouteConfig)
			.mockReset()
			.mockImplementationOnce(() => new Promise((r) => (resolveFirst = r)))
			.mockRejectedValueOnce(new Error('boom'))
			.mockResolvedValue(cfg);
		const { container } = render(HrNeoSettingsView, { props: { embedded: true } });
		await vi.waitFor(() => expect(container.querySelectorAll('input[type="checkbox"]').length).toBeGreaterThan(1));
		const boxes = () => container.querySelectorAll<HTMLInputElement>('input[type="checkbox"]');
		const save = async () => fireEvent.click(await screen.findByRole('button', { name: /Сохранить|Save/ }));

		await fireEvent.input(boxes()[0], { target: { checked: true } });
		await save();
		await fireEvent.input(boxes()[1], { target: { checked: true } });
		resolveFirst({ ...cfg, autoStart: true });
		await vi.waitFor(() => expect(boxes()[1].checked).toBe(true));

		await save();
		expect(api.updateHydraRouteConfig).toHaveBeenLastCalledWith({ clearIPSet: true });
		await save();
		expect(api.updateHydraRouteConfig).toHaveBeenLastCalledWith({ clearIPSet: true });
	});
});
