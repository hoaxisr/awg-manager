/**
 * saveStatus — polling store for GET /api/ndms/save-status.
 *
 * Показывает индикатор в шапке (layout/SaveStatusLed.svelte). До 17.09 стор
 * не импортировал НИКТО: registerStore не выполнялся, и событие координатора
 * уходило в никуда — индикатора в панели не было вовсе (F358). Сторож на этот
 * случай — в SaveStatusLed.test.ts.
 *
 * Mirrors the former save:status SSE event one-for-one. SaveCoordinator
 * now publishes a `resource:invalidated` hint with Resource="saveStatus"
 * on every state transition — the store registry calls `.invalidate()`
 * which triggers an immediate refetch while a subscriber is active.
 *
 * Subscribe from any header/footer component that renders the save
 * indicator and read `$saveStatus.data?.state`.
 */
import { createPollingStore, type PollingStore } from './polling';
import { registerStore } from './storeRegistry';

export interface SaveStatus {
	/** "idle" | "pending" | "saving" | "error" | "failed". */
	state: string;
	lastError?: string;
	lastSaveAt?: string;
	pendingCount: number;
}

async function fetchSaveStatus(): Promise<SaveStatus> {
	const res = await fetch('/api/ndms/save-status');
	if (!res.ok) throw new Error(`saveStatus ${res.status}`);
	const body = await res.json();
	return body.data as SaveStatus;
}

export const saveStatus: PollingStore<SaveStatus> = createPollingStore<SaveStatus>(
	fetchSaveStatus,
	{
		staleTime: 30_000,
		// Наш координатор публикует каждый свой переход, но несохранённые правки
		// конфигурации, сделанные из РОДНОЙ веб-морды роутера, идут мимо него.
		pollInterval: 60_000,
	},
);

registerStore('saveStatus', saveStatus);
