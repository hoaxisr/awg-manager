import { writable } from 'svelte/store';
import type { SystemInfo } from '$lib/types';

function createSystemStore() {
	const { subscribe, set } = writable<SystemInfo | null>(null);

	return {
		subscribe,
		setSnapshot(data: SystemInfo) {
			set(data);
		},
	};
}

export const systemInfo = createSystemStore();
