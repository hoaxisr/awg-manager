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
});
