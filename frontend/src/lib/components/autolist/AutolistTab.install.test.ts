import { cleanup, fireEvent, render, screen } from '@testing-library/svelte';
import { afterEach, describe, expect, it, vi } from 'vitest';
import AutolistTab from './AutolistTab.svelte';
import { api } from '$lib/api/client';
import type { AutolistStatus } from '$lib/types';

vi.stubGlobal('ResizeObserver', class {
	observe() {}
	disconnect() {}
});

const notInstalled: AutolistStatus = { running: false, installed: false, enabled: false, candidates: [], history: [] };
const running: AutolistStatus = { running: true, installed: true, enabled: true, candidates: [], history: [] };

describe('AutolistTab install', () => {
	afterEach(() => {
		cleanup();
		vi.restoreAllMocks();
	});

	it('offers install when the package is absent and calls the install API', async () => {
		const status = vi.spyOn(api, 'getAutolistStatus').mockResolvedValue(notInstalled);
		const install = vi.spyOn(api, 'installAutolist').mockImplementation(async () => {
			status.mockResolvedValue(running);
			return running;
		});

		render(AutolistTab);
		await screen.findByText('Сателлит не установлен');
		await fireEvent.click(screen.getByRole('button', { name: 'Установить' }));

		expect(install).toHaveBeenCalledOnce();
		await screen.findByText('Сателлит работает');
	});
});
