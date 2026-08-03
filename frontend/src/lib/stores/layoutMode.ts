import type { UsageLevel } from '$lib/types/usageLevel';
import { browser } from '$app/environment';
import { writable } from 'svelte/store';

export type LayoutMode = 'sidebar' | 'classic' | 'compact';

export const LAYOUT_MODE_LABELS: Record<LayoutMode, string> = {
	sidebar: 'Сайдбар (new)',
	classic: 'Классическая',
	compact: 'Компактная',
};

const STORAGE_KEY = 'awg-manager-layout-mode';
const LEGACY_SIDEBAR_KEY = 'awg-manager-sidebar-nav';
const LEGACY_COMPACT_KEY = 'awg-manager-compact-layout';

function isValidMode(value: string): value is LayoutMode {
	return value === 'sidebar' || value === 'classic' || value === 'compact';
}

function readMigrated(): LayoutMode {
	if (!browser) return 'classic';
	try {
		const raw = localStorage.getItem(STORAGE_KEY);
		if (raw !== null && isValidMode(raw)) return raw;

		const sidebar = localStorage.getItem(LEGACY_SIDEBAR_KEY);
		if (sidebar === '1' || sidebar === 'true') return 'sidebar';

		const compact = localStorage.getItem(LEGACY_COMPACT_KEY);
		if (compact === '1' || compact === 'true') return 'compact';

		return 'classic';
	} catch {
		return 'classic';
	}
}

function persist(mode: LayoutMode): void {
	if (!browser) return;
	try {
		localStorage.setItem(STORAGE_KEY, mode);
	} catch {
		/* private mode */
	}
}

const { subscribe, set } = writable<LayoutMode>(readMigrated());

export const layoutMode = {
	subscribe,
	setMode(mode: LayoutMode) {
		persist(mode);
		set(mode);
	},
	init() {
		set(readMigrated());
	},
};

/** In basic mode layout is always compact; sidebar is unavailable. */
export function resolveLayoutMode(level: UsageLevel, mode: LayoutMode): LayoutMode {
	if (level === 'basic') return 'compact';
	return mode;
}

export function isSidebarNavActive(level: UsageLevel, mode: LayoutMode): boolean {
	return resolveLayoutMode(level, mode) === 'sidebar';
}

export function isCompactLayoutActive(level: UsageLevel, mode: LayoutMode): boolean {
	return resolveLayoutMode(level, mode) === 'compact';
}
