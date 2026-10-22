import { browser } from '$app/environment';
import { createPersistedFlag } from './persisted';

const store = createPersistedFlag('awg-manager-country-flag-emojis', true);

function applyFlagEmojis(enabled: boolean) {
	if (!browser) return;
	if (enabled) {
		document.documentElement.classList.add('flag-emojis-enabled');
	} else {
		document.documentElement.classList.remove('flag-emojis-enabled');
	}
}

/** User preference: display country flag emojis (Twemoji polyfill font for Windows and all platforms). */
export const countryFlagEmojis = {
	subscribe: store.subscribe,
	init() {
		store.init();
		if (browser) {
			let current = true;
			const unsub = store.subscribe((val) => {
				current = val;
			});
			unsub();
			applyFlagEmojis(current);
		}
	},
	setEnabled(value: boolean) {
		store.set(value);
		applyFlagEmojis(value);
	},
};

if (browser) {
	store.subscribe((val) => {
		applyFlagEmojis(val);
	});
}
