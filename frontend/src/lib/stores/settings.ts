// Minimal global cache for Settings + a derived UsageLevel store.
// Loaded once from /+layout.svelte after authentication; updated by:
//   1. UsageLevelCard after a successful api.updateSettings() call
//   2. The settings page after any of its save flows (write-through)
//   3. The SSE handler for resource:invalidated{resource:"settings"}
//      which calls reloadSettings() (see Task 9)
// Pages that don't care about UsageLevel can keep loading settings their
// own way — this store coexists with that pattern.

import { writable, derived, get } from 'svelte/store';
import type { Settings } from '$lib/types';
import { type UsageLevel } from '$lib/types/usageLevel';
import { api } from '$lib/api/client';

export const settings = writable<Settings | null>(null);

/** Настройки не загрузились ни разу: экраны, которые ждут их ради видимости
 *  вкладок, перестают ждать и откатываются на доступное. */
export const settingsUnavailable = writable(false);

// Safe fallback: when the store is empty we return 'advanced' so the UI
// doesn't flicker by hiding things it should show.
export const usageLevel = derived(
	settings,
	($s): UsageLevel => ($s?.usageLevel ?? 'advanced'),
);

export function setSettings(s: Settings) {
	settings.set(s);
}

export async function reloadSettings(): Promise<Settings | null> {
	try {
		const s = await api.getSettings();
		settings.set(s);
		settingsUnavailable.set(false);
		return s;
	} catch {
		if (get(settings) === null) settingsUnavailable.set(true);
		return get(settings);
	}
}
