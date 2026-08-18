import { beforeEach, describe, expect, it, vi } from 'vitest';
import { get } from 'svelte/store';
import { experimentalSettingsUnlocked } from '$lib/stores/experimentalSettingsUnlocked';
import {
	handleVersionBadgeClick,
	resetVersionBadgeEasterEggForTests,
	VERSION_EASTER_EGG_CLICKS,
} from './versionBadgeEasterEgg';

vi.mock('$lib/stores/notifications', () => ({
	notifications: {
		info: vi.fn(),
		success: vi.fn(),
	},
}));

vi.mock('$lib/stores/settingsUpdateHighlight', () => ({
	settingsUpdateHighlight: { pulse: vi.fn() },
}));

import { notifications } from '$lib/stores/notifications';
import { settingsUpdateHighlight } from '$lib/stores/settingsUpdateHighlight';

describe('handleVersionBadgeClick', () => {
	beforeEach(() => {
		resetVersionBadgeEasterEggForTests();
		experimentalSettingsUnlocked.set(false);
		vi.clearAllMocks();
	});

	it('ignores clicks outside settings', () => {
		handleVersionBadgeClick({ usageLevel: 'expert', hasUpdate: true, onSettingsPage: false });
		expect(settingsUpdateHighlight.pulse).not.toHaveBeenCalled();
		expect(notifications.info).not.toHaveBeenCalled();
	});

	it('highlights update block on settings when update is available', () => {
		handleVersionBadgeClick({ usageLevel: 'basic', hasUpdate: true, onSettingsPage: true });
		expect(settingsUpdateHighlight.pulse).toHaveBeenCalledOnce();
	});

	it('shows countdown from the 7th click in expert mode', () => {
		for (let i = 0; i < 6; i += 1) {
			handleVersionBadgeClick({ usageLevel: 'expert', hasUpdate: false, onSettingsPage: true });
		}
		expect(notifications.info).not.toHaveBeenCalled();

		handleVersionBadgeClick({ usageLevel: 'expert', hasUpdate: false, onSettingsPage: true });
		expect(notifications.info).toHaveBeenCalledWith('Осталось кликнуть ещё 3 раза');
	});

	it('unlocks experimental settings on the 10th click', () => {
		for (let i = 0; i < VERSION_EASTER_EGG_CLICKS; i += 1) {
			handleVersionBadgeClick({ usageLevel: 'expert', hasUpdate: false, onSettingsPage: true });
		}
		expect(get(experimentalSettingsUnlocked)).toBe(true);
		expect(notifications.success).toHaveBeenCalledWith('Экспериментальные настройки разблокированы');
	});

	it('unlocks pink ponies on the 5th click for expert mode', () => {
		for (let i = 0; i < 5; i += 1) {
			handleVersionBadgeClick({ usageLevel: 'expert', hasUpdate: false, onSettingsPage: true });
		}
		expect(notifications.success).toHaveBeenCalledWith('🦄✨ Секретный раздел «Страна розовых пони» разблокирован в разделе «Система»!');
	});

	it('does not unlock pink ponies for basic mode', () => {
		for (let i = 0; i < 5; i += 1) {
			handleVersionBadgeClick({ usageLevel: 'basic', hasUpdate: false, onSettingsPage: true });
		}
		expect(notifications.success).not.toHaveBeenCalled();
	});
});
