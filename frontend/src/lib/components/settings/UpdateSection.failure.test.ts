import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import '@testing-library/jest-dom/vitest';
import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/svelte';
import { m } from '$lib/i18n';
import type { UpdateInfo } from '$lib/types';

const { getBootStatusMock, applyUpdateMock, checkUpdateMock } = vi.hoisted(() => ({
	getBootStatusMock: vi.fn(),
	applyUpdateMock: vi.fn(),
	checkUpdateMock: vi.fn(),
}));

vi.mock('$lib/api/client', () => ({
	api: {
		getBootStatus: getBootStatusMock,
		applyUpdate: applyUpdateMock,
		checkUpdate: checkUpdateMock,
	},
}));

import UpdateSection from './UpdateSection.svelte';

const available: UpdateInfo = {
	available: true,
	currentVersion: '2.20.6',
	latestVersion: '2.21.0',
	checkedAt: '2026-11-27T10:00:00Z',
	checking: false,
};

// F933: opkg отказал до остановки старой версии — демон прежний, UI показывает
// причину вместо «сервер не ответил» через две минуты.
describe('UpdateSection: обновление не установилось', () => {
	beforeEach(() => {
		vi.resetAllMocks();
		getBootStatusMock.mockResolvedValue({ initializing: false, remainingSeconds: 0, phase: 'ready', instanceId: 'same' });
		applyUpdateMock.mockResolvedValue({ status: 'upgrading' });
		checkUpdateMock.mockResolvedValue({
			...available,
			upgradeError: "wget: bad address 'bin.entware.net'\nupgrade FAILED: opkg rc=255",
			upgradeErrorHint: 'dns',
			upgradeErrorAt: '2026-11-27T10:01:00Z',
		});
	});
	afterEach(cleanup);

	it('показывает причину и подсказку, ожидание прекращается', async () => {
		render(UpdateSection, { props: { updateInfo: { ...available } } });
		await fireEvent.click(screen.getByRole('button', { name: m.common_update() }));
		const dialog = await screen.findByRole('dialog');
		const confirm = Array.from(dialog.querySelectorAll('button')).find((b) => b.textContent?.trim() === m.common_update());
		await fireEvent.click(confirm!);

		await waitFor(() => expect(screen.getAllByText(m.settings_update_failed()).length).toBeGreaterThan(0), { timeout: 5000 });
		expect(screen.getByText(m.settings_update_failed_hint_dns())).toBeInTheDocument();
		expect(screen.getByText(/bad address 'bin\.entware\.net'/)).toBeInTheDocument();
		expect(screen.queryByText(m.settings_update_in_progress())).not.toBeInTheDocument();
		expect(checkUpdateMock).toHaveBeenCalledWith(false);
	});
});
