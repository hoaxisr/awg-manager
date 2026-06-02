import { writable } from 'svelte/store';
import { browser } from '$app/environment';
import { PEER_SORT_DEFAULTS, type PeerSortKey } from '$lib/utils/peerSort';

const storageKey = 'awg-manager-peer-sort';

const VALID_KEYS = new Set<PeerSortKey>(['name', 'traffic', 'ip', 'endpoint', 'online', 'handshake']);

export interface PeerSortState {
	sortBy: PeerSortKey;
	sortAsc: boolean;
}

function defaultState(): PeerSortState {
	return { sortBy: 'name', sortAsc: PEER_SORT_DEFAULTS.name };
}

function getInitial(): PeerSortState {
	if (!browser) return defaultState();
	try {
		const raw = localStorage.getItem(storageKey);
		if (!raw) return defaultState();
		const parsed: unknown = JSON.parse(raw);
		if (!parsed || typeof parsed !== 'object') return defaultState();
		const { sortBy, sortAsc } = parsed as Partial<PeerSortState>;
		if (!sortBy || !VALID_KEYS.has(sortBy)) return defaultState();
		return {
			sortBy,
			sortAsc: typeof sortAsc === 'boolean' ? sortAsc : PEER_SORT_DEFAULTS[sortBy],
		};
	} catch {
		return defaultState();
	}
}

function persist(state: PeerSortState) {
	if (!browser) return;
	try {
		localStorage.setItem(storageKey, JSON.stringify(state));
	} catch {
		// quota / private-mode: silently ignore
	}
}

function createPeerSortStore() {
	const { subscribe, set, update } = writable<PeerSortState>(getInitial());

	return {
		subscribe,
		setSort(sortBy: PeerSortKey, sortAsc: boolean) {
			const next: PeerSortState = { sortBy, sortAsc };
			persist(next);
			set(next);
		},
		setSortBy(key: PeerSortKey) {
			update((s) => {
				if (s.sortBy === key) return s;
				const next: PeerSortState = { sortBy: key, sortAsc: PEER_SORT_DEFAULTS[key] };
				persist(next);
				return next;
			});
		},
		toggleDir() {
			update((s) => {
				const next: PeerSortState = { ...s, sortAsc: !s.sortAsc };
				persist(next);
				return next;
			});
		},
	};
}

export const peerSort = createPeerSortStore();
