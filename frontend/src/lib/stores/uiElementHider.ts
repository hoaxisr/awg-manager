import { writable } from 'svelte/store';
import { isSelectorHideProtected } from '$lib/utils/uiElementHiderGuard';

const ENABLED_KEY = 'awg-manager-ui-element-hider-enabled';
const RULES_KEY = 'awg-manager-ui-element-hider-rules';

export type HiddenElementRule = {
	id: string;
	selector: string;
	label: string;
	/** pathname, где правило создано */
	path: string;
	createdAt: number;
};

function readEnabled(): boolean {
	if (typeof localStorage === 'undefined') return false;
	return localStorage.getItem(ENABLED_KEY) === '1';
}

function persistEnabled(value: boolean): void {
	if (typeof localStorage === 'undefined') return;
	localStorage.setItem(ENABLED_KEY, value ? '1' : '0');
}

function readRules(): HiddenElementRule[] {
	if (typeof localStorage === 'undefined') return [];
	try {
		const raw = localStorage.getItem(RULES_KEY);
		if (!raw) return [];
		const parsed: unknown = JSON.parse(raw);
		if (!Array.isArray(parsed)) return [];
		return parsed.filter(isHiddenElementRule).filter((r) => !isSelectorHideProtected(r.selector));
	} catch {
		return [];
	}
}

function isHiddenElementRule(v: unknown): v is HiddenElementRule {
	if (!v || typeof v !== 'object') return false;
	const o = v as Record<string, unknown>;
	return (
		typeof o.id === 'string' &&
		typeof o.selector === 'string' &&
		typeof o.label === 'string' &&
		typeof o.path === 'string' &&
		typeof o.createdAt === 'number'
	);
}

// crypto.randomUUID недоступен вне secure context (роутер по plain HTTP).
function newRuleId(): string {
	if (typeof crypto !== 'undefined' && typeof crypto.randomUUID === 'function') {
		return crypto.randomUUID();
	}
	return `rule-${Date.now().toString(36)}-${Math.random().toString(36).slice(2, 10)}`;
}

function persistRules(rules: HiddenElementRule[]): void {
	if (typeof localStorage === 'undefined') return;
	try {
		localStorage.setItem(RULES_KEY, JSON.stringify(rules));
	} catch {
		// quota / private mode
	}
}

const enabledStore = writable<boolean>(readEnabled());
const pickerActiveStore = writable<boolean>(false);
const rulesStore = writable<HiddenElementRule[]>(readRules());

export const uiElementHiderEnabled = {
	subscribe: enabledStore.subscribe,
	set(value: boolean) {
		persistEnabled(value);
		enabledStore.set(value);
		if (!value) pickerActiveStore.set(false);
	},
};

export const uiElementHiderPickerActive = {
	subscribe: pickerActiveStore.subscribe,
	set: pickerActiveStore.set,
	toggle() {
		pickerActiveStore.update((v) => !v);
	},
};

export const uiElementHiderRules = {
	subscribe: rulesStore.subscribe,
	add(rule: Omit<HiddenElementRule, 'id' | 'createdAt'>) {
		if (isSelectorHideProtected(rule.selector)) return;
		rulesStore.update((prev) => {
			if (prev.some((r) => r.selector === rule.selector && r.path === rule.path)) {
				return prev;
			}
			const next: HiddenElementRule[] = [
				...prev,
				{
					...rule,
					id: newRuleId(),
					createdAt: Date.now(),
				},
			];
			persistRules(next);
			return next;
		});
	},
	remove(id: string) {
		rulesStore.update((prev) => {
			const next = prev.filter((r) => r.id !== id);
			persistRules(next);
			return next;
		});
	},
	clear() {
		persistRules([]);
		rulesStore.set([]);
	},
};

export function rulesForPath(all: HiddenElementRule[], pathname: string): HiddenElementRule[] {
	return all.filter((r) => r.path === pathname);
}

export function buildHideStyles(activeRules: HiddenElementRule[]): string {
	const safe = activeRules.filter((r) => !isSelectorHideProtected(r.selector));
	if (safe.length === 0) return '';
	const block = safe.map((r) => `${r.selector}`).join(',\n');
	return `${block} { display: none !important; }`;
}
