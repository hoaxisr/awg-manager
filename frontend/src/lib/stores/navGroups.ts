import { createPersistedStore } from './persisted';
import type { NavGroupId } from '$lib/data/navigation';

type GroupState = Record<NavGroupId, boolean>;

const DEFAULT: GroupState = { awg: true, sb: true, router: true, services: true };

const store = createPersistedStore<GroupState>('awg-manager-nav-groups', {
	defaultValue: DEFAULT,
	serialize: JSON.stringify,
	deserialize: (raw) => {
		try {
			const parsed: unknown = JSON.parse(raw);
			return parsed && typeof parsed === 'object'
				? { ...DEFAULT, ...(parsed as Partial<GroupState>) }
				: DEFAULT;
		} catch {
			return DEFAULT;
		}
	},
});

export const navGroups = {
	subscribe: store.subscribe,
	init: store.init,
	toggle(id: NavGroupId) {
		store.update((s) => ({ ...s, [id]: !s[id] }));
	},
};
