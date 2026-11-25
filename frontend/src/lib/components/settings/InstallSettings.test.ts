import { describe, it, expect, vi } from 'vitest';
import { render, screen, fireEvent, waitFor } from '@testing-library/svelte';
import InstallSettings from './InstallSettings.svelte';
import { api } from '$lib/api/client';
import type { Settings } from '$lib/types';

vi.mock('$lib/api/client', () => ({ api: { updateSettings: vi.fn() } }));
vi.mock('$lib/stores/notifications', () => ({ notifications: { success: vi.fn(), error: vi.fn() } }));
vi.mock('$lib/stores/settings', () => ({ setSettings: vi.fn() }));

const settings = {
	authEnabled: true,
	apiKey: 'old-key',
	usageLevel: 'expert',
	updates: { checkEnabled: true, channel: 'stable', autoInstallEnabled: false, autoInstallIntervalDays: 7, autoInstallTime: '05:00', statsEnabled: true },
} as unknown as Settings;

describe('InstallSettings', () => {
	// F684: шлётся только изменённое поле, а не снимок настроек — устаревший
	// снимок другой вкладки откатил бы чужие изменения (например, apiKey).
	it('переключатель статистики шлёт только updates.statsEnabled', async () => {
		vi.mocked(api.updateSettings).mockResolvedValue(settings);
		render(InstallSettings, { settings, updateInfo: null });
		const row = screen.getByText('Анонимная статистика').parentElement?.parentElement;
		await fireEvent.click(row?.querySelector('input[type="checkbox"]') as HTMLInputElement);
		await waitFor(() => expect(api.updateSettings).toHaveBeenCalledTimes(1));
		expect(vi.mocked(api.updateSettings).mock.calls[0][0]).toEqual({ updates: { statsEnabled: false } });
	});

	// Черновик расписания: правка видна, пока её не сохранят, и сам собой
	// исчезает, когда сохранённое значение с ней сошлось.
	it('расписание: правка показывает «Сохранить», сохранение её убирает', async () => {
		const on = { ...settings, updates: { ...settings.updates, autoInstallEnabled: true } } as Settings;
		vi.mocked(api.updateSettings).mockReset();
		vi.mocked(api.updateSettings).mockResolvedValue({ ...on, updates: { ...on.updates, autoInstallIntervalDays: 3 } });
		const { rerender } = render(InstallSettings, { props: { settings: on, updateInfo: null } });
		expect(screen.queryByRole('button', { name: 'Сохранить' })).toBeNull();
		await fireEvent.input(document.getElementById('autoInstallIntervalDays')!, { target: { value: '3' } });
		await fireEvent.click(screen.getByRole('button', { name: 'Сохранить' }));
		expect(api.updateSettings).toHaveBeenCalledWith({ updates: { autoInstallIntervalDays: 3, autoInstallTime: '05:00' } });
		await rerender({ settings: { ...on, updates: { ...on.updates, autoInstallIntervalDays: 3 } }, updateInfo: null });
		await waitFor(() => expect(screen.queryByRole('button', { name: 'Сохранить' })).toBeNull());
	});
});
