import { writable } from 'svelte/store';
import type { SystemInfo } from '$lib/types';

function createSystemStore() {
	const inner = writable<SystemInfo | null>(null);

	return {
		subscribe: inner.subscribe,
		setSnapshot(data: SystemInfo) {
			inner.set(data);
		},
		applySingboxStatus(installed: boolean, version: string): void {
			inner.update(info => info ? { ...info, singbox: { installed, version } } : info);
		},
	};
}

export const systemInfo = createSystemStore();
