import { writable } from 'svelte/store';
import { browser } from '$app/environment';
import type { FircGroup } from '$lib/types/firc';

const STORAGE_KEY = 'awgm_firc_groups_draft_v1';

export interface FircGroupsDraft {
	groups: FircGroup[];
	baseSnapshot: string;
	draftedAt: string;
}

function readInitialDraft(): FircGroupsDraft | null {
	if (!browser) return null;
	try {
		const raw = sessionStorage.getItem(STORAGE_KEY);
		if (!raw) return null;
		const parsed = JSON.parse(raw) as FircGroupsDraft;
		if (!parsed || !Array.isArray(parsed.groups) || typeof parsed.baseSnapshot !== 'string') {
			return null;
		}
		return parsed;
	} catch {
		return null;
	}
}

const initial = readInitialDraft();

export const fircGroupsDraft = writable<FircGroupsDraft | null>(initial);
export const fircHasDraft = writable<boolean>(initial !== null);

export function saveFircGroupsDraft(groups: FircGroup[], baseSnapshot: string, existingDraftedAt?: string | null): string {
	const draftedAt = existingDraftedAt || new Date().toISOString();
	const payload: FircGroupsDraft = {
		groups: JSON.parse(JSON.stringify(groups)),
		baseSnapshot,
		draftedAt,
	};
	fircGroupsDraft.set(payload);
	fircHasDraft.set(true);
	if (browser) {
		try {
			sessionStorage.setItem(STORAGE_KEY, JSON.stringify(payload));
		} catch {
			// Ignore quota errors
		}
	}
	return draftedAt;
}

export function loadFircGroupsDraft(): FircGroupsDraft | null {
	return readInitialDraft();
}

export function clearFircGroupsDraft(): void {
	fircGroupsDraft.set(null);
	fircHasDraft.set(false);
	if (browser) {
		try {
			sessionStorage.removeItem(STORAGE_KEY);
		} catch {
			// Ignore storage errors
		}
	}
}
