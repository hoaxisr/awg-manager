import { writable, type Readable } from 'svelte/store';

export type ProxyPanelMode = 'simple' | 'expert';

const STORAGE_KEY = 'awg.proxy-panel.mode';
const VALID: ReadonlyArray<ProxyPanelMode> = ['simple', 'expert'];

function isValid(v: unknown): v is ProxyPanelMode {
	return typeof v === 'string' && (VALID as readonly string[]).includes(v);
}

function readInitialMode(): ProxyPanelMode {
	if (typeof window === 'undefined') return 'simple';
	try {
		const v = window.localStorage.getItem(STORAGE_KEY);
		return isValid(v) ? v : 'simple';
	} catch {
		return 'simple';
	}
}

const store = writable<ProxyPanelMode>(readInitialMode());

/** Режим UI WDTT/FreeTurn: мастер или полная форма. */
export const proxyPanelMode: Readable<ProxyPanelMode> = { subscribe: store.subscribe };

export function setProxyPanelMode(next: ProxyPanelMode): void {
	if (!isValid(next)) return;
	store.set(next);
	if (typeof window === 'undefined') return;
	try {
		window.localStorage.setItem(STORAGE_KEY, next);
	} catch {
		// private mode
	}
}
