import { writable } from 'svelte/store';

const STORAGE_KEY = 'awgm.ponies.unlocked';

function createPoniesStore() {
	const initial = typeof localStorage !== 'undefined' && localStorage.getItem(STORAGE_KEY) === 'true';
	const { subscribe, set, update } = writable<boolean>(initial);

	return {
		subscribe,
		unlock: () => {
			if (typeof localStorage !== 'undefined') {
				localStorage.setItem(STORAGE_KEY, 'true');
			}
			set(true);
		},
		lock: () => {
			if (typeof localStorage !== 'undefined') {
				localStorage.removeItem(STORAGE_KEY);
			}
			set(false);
		},
		toggle: () => {
			update((val) => {
				const next = !val;
				if (typeof localStorage !== 'undefined') {
					if (next) localStorage.setItem(STORAGE_KEY, 'true');
					else localStorage.removeItem(STORAGE_KEY);
				}
				return next;
			});
		},
	};
}

export const poniesUnlocked = createPoniesStore();
