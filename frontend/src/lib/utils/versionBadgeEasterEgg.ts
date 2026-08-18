import { get } from 'svelte/store';
import { experimentalSettingsUnlocked } from '$lib/stores/experimentalSettingsUnlocked';
import { poniesUnlocked } from '$lib/stores/poniesUnlocked';
import { settingsUpdateHighlight } from '$lib/stores/settingsUpdateHighlight';
import { notifications } from '$lib/stores/notifications';
import type { UsageLevel } from '$lib/types/usageLevel';

export const VERSION_EASTER_EGG_CLICKS = 10;
export const PONY_EASTER_EGG_CLICKS = 5;
export const VERSION_EASTER_EGG_RESET_MS = 2500;

let clickCount = 0;
let resetTimer: ReturnType<typeof setTimeout> | null = null;

function scheduleReset() {
	if (resetTimer) clearTimeout(resetTimer);
	resetTimer = setTimeout(() => {
		clickCount = 0;
		resetTimer = null;
	}, VERSION_EASTER_EGG_RESET_MS);
}

function remainingClicksMessage(remaining: number): string {
	if (remaining === 1) return 'Осталось кликнуть ещё 1 раз';
	return `Осталось кликнуть ещё ${remaining} ${remaining >= 2 && remaining <= 4 ? 'раза' : 'раз'}`;
}

export function handleVersionBadgeClick(options: {
	usageLevel: UsageLevel;
	hasUpdate: boolean;
	onSettingsPage: boolean;
}): void {
	const { usageLevel, hasUpdate, onSettingsPage } = options;

	if (!onSettingsPage) return;

	clickCount += 1;
	scheduleReset();

	if (usageLevel === 'expert') {
		// 5 Clicks: Unlock Pink Ponies!
		if (clickCount === PONY_EASTER_EGG_CLICKS) {
			poniesUnlocked.unlock();
			notifications.success('🦄✨ Секретный раздел «Страна розовых пони» разблокирован в разделе «Система»!');
		}
	}

	if (hasUpdate) {
		settingsUpdateHighlight.pulse();
		if (typeof window !== 'undefined') {
			window.requestAnimationFrame(() => {
				document.getElementById('awgm-update')?.scrollIntoView({ behavior: 'smooth', block: 'center' });
			});
		}
	}

	if (usageLevel === 'expert') {
		if (clickCount >= 7 && clickCount < VERSION_EASTER_EGG_CLICKS) {
			notifications.info(remainingClicksMessage(VERSION_EASTER_EGG_CLICKS - clickCount));
			return;
		}
		if (clickCount >= VERSION_EASTER_EGG_CLICKS) {
			clickCount = 0;
			if (resetTimer) {
				clearTimeout(resetTimer);
				resetTimer = null;
			}
			experimentalSettingsUnlocked.toggle();
			const unlocked = get(experimentalSettingsUnlocked);
			notifications.success(
				unlocked
					? 'Экспериментальные настройки разблокированы'
					: 'Экспериментальные настройки скрыты',
			);
		}
	}
}

/** Test helper — resets in-memory click counter between tests. */
export function resetVersionBadgeEasterEggForTests(): void {
	clickCount = 0;
	if (resetTimer) {
		clearTimeout(resetTimer);
		resetTimer = null;
	}
}
