import { describe, expect, it, vi } from 'vitest';
import { get } from 'svelte/store';

const getSettings = vi.hoisted(() => vi.fn());
vi.mock('$lib/api/client', () => ({ api: { getSettings } }));

import { reloadSettings, settings, settingsUnavailable } from './settings';

describe('settingsUnavailable', () => {
	it('is raised only while settings never loaded and cleared by a later success', async () => {
		getSettings.mockRejectedValueOnce(new Error('offline'));
		await reloadSettings();
		expect(get(settingsUnavailable)).toBe(true);

		getSettings.mockResolvedValueOnce({ usageLevel: 'expert' });
		await reloadSettings();
		expect(get(settingsUnavailable)).toBe(false);

		getSettings.mockRejectedValueOnce(new Error('offline'));
		await reloadSettings();
		expect(get(settingsUnavailable)).toBe(false);
		expect(get(settings)?.usageLevel).toBe('expert');
	});
});
