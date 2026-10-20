import { describe, it, expect, vi } from 'vitest';
import { render, screen, fireEvent, waitFor } from '@testing-library/svelte';
import UpdateSection from './UpdateSection.svelte';
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

describe('UpdateSection', () => {
	// F684: шлётся только изменённое поле, а не снимок настроек — устаревший
	// снимок другой вкладки откатил бы чужие изменения (например, apiKey).
	it('переключатель статистики шлёт только updates.statsEnabled', async () => {
		vi.mocked(api.updateSettings).mockResolvedValue(settings);
		render(UpdateSection, { settings, updateInfo: null });
		const row = screen.getByText('Анонимная статистика').parentElement?.parentElement;
		await fireEvent.click(row?.querySelector('input[type="checkbox"]') as HTMLInputElement);
		await waitFor(() => expect(api.updateSettings).toHaveBeenCalledTimes(1));
		expect(vi.mocked(api.updateSettings).mock.calls[0][0]).toEqual({ updates: { statsEnabled: false } });
	});
});
