/**
 * saveStatus — polling store for GET /api/ndms/save-status (cold tier, 30s).
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
		pollInterval: 30_000,
	},
);

registerStore('saveStatus', saveStatus);
