// singboxMemory — sing-box process RSS (bytes) from the Clash /connections
// WebSocket `memory` field, pushed via the `singbox:memory` SSE event.
// Fed by +layout onSingboxMemory handler. Value is 0 before the first push.

import { writable } from 'svelte/store';

export const singboxMemory = writable<number>(0);

export function applySingboxMemory(data: { memory: number }): void {
	singboxMemory.set(data.memory ?? 0);
}
