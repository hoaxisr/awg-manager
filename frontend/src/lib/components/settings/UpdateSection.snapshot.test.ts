import { describe, it, expect, vi } from 'vitest';
import { render, screen, fireEvent, waitFor } from '@testing-library/svelte';
import UpdateSection from './UpdateSection.svelte';
import { api } from '$lib/api/client';
import type { Settings } from '$lib/types';

vi.mock('$lib/api/client', () => ({
	api: { updateSettings: vi.fn() }
}));
vi.mock('$lib/stores/notifications', () => ({
	notifications: { success: vi.fn(), error: vi.fn(), warning: vi.fn(), info: vi.fn() }
}));

function settingsWith(snapshotDisabled?: boolean): Settings {
	return {
		usageLevel: 'basic',
		updates: {
			checkEnabled: false,
			channel: 'stable',
			autoInstallEnabled: false,
			autoInstallIntervalDays: 7,
			autoInstallTime: '05:00',
			statsEnabled: true,
			snapshotDisabled
		}
	} as unknown as Settings;
}

describe('UpdateSection: снимок перед обновлением', () => {
	it('включён по умолчанию; выключение сохраняет snapshotDisabled=true', async () => {
		const initial = settingsWith(undefined);
		vi.mocked(api.updateSettings).mockImplementation(async (s) => s as Settings);
		render(UpdateSection, { props: { updateInfo: null, settings: initial } });

		const toggle = screen.getByRole<HTMLInputElement>('checkbox', { name: 'Снимок настроек перед обновлением' });
		expect(toggle.checked).toBe(true);

		await fireEvent.input(toggle, { target: { checked: false } });
		await waitFor(() => expect(api.updateSettings).toHaveBeenCalled());
		const sent = vi.mocked(api.updateSettings).mock.calls[0][0] as Settings;
		expect(sent.updates.snapshotDisabled).toBe(true);
		expect(sent.updates.statsEnabled).toBe(true);
	});
});
