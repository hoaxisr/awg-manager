import { browser } from '$app/environment';
import { writable } from 'svelte/store';
import type { UsageLevel } from '$lib/types/usageLevel';

const storageKey = 'awg-manager-compact-layout';

function readStored(): boolean {
	if (!browser) return false;
	try {
		return localStorage.getItem(storageKey) === 'true';
	} catch {
		return false;
	}
}

function writeStored(enabled: boolean): void {
	if (!browser) return;
	try {
		localStorage.setItem(storageKey, enabled ? 'true' : 'false');
	} catch {
		/* ignore quota / private mode */
	}
}

function createCompactLayoutStore() {
	const { subscribe, set } = writable<boolean>(readStored());

	return {
		subscribe,
		init() {
			set(readStored());
		},
		setEnabled(enabled: boolean) {
			set(enabled);
			writeStored(enabled);
		},
	};
}

export const compactLayout = createCompactLayoutStore();

/** Базовый режим — всегда; расширенный/продвинутый — по выбору пользователя. */
export function isCompactLayoutActive(level: UsageLevel, userEnabled: boolean): boolean {
	return level === 'basic' || userEnabled;
}
