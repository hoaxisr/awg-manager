// Последние SSE-события WARP-сканера (`warpscout:scan`, `warpscout:findjunk`, `warpscout:watchdog`).
// Пишет +layout, читает вкладка сканера. Значение до первого события — null;
// после него в сторе лежит последнее событие, возможно давнее: подписчик
// берёт только то, что пришло после его подписки.

import { writable } from 'svelte/store';
import type { WarpFindJunkStatus, WarpScanStatus, WarpWatchStatus } from '$lib/types/warpscout';

export const warpScanPush = writable<WarpScanStatus | null>(null);
export const warpFindJunkPush = writable<WarpFindJunkStatus | null>(null);
export const warpWatchPush = writable<WarpWatchStatus | null>(null);
