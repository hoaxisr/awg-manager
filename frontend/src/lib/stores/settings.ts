// Minimal global cache for Settings.
// Loaded once from /+layout.svelte after authentication; updated by:
//   1. The settings page after any of its save flows (write-through)
//   2. The SSE handler for resource:invalidated{resource:"settings"}
//      which calls reloadSettings() (see Task 9)
// Pages can keep loading settings their own way — this store coexists
// with that pattern.

import { writable, get } from 'svelte/store';
import type { Settings } from '$lib/types';
import { api } from '$lib/api/client';

export const settings = writable<Settings | null>(null);

export function setSettings(s: Settings) {
	settings.set(s);
}

export async function reloadSettings(): Promise<Settings | null> {
	try {
		const s = await api.getSettings();
		settings.set(s);
		return s;
	} catch {
		return get(settings);
	}
}
