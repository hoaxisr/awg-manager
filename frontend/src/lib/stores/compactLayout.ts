import { createPersistedFlag } from './persisted';

const store = createPersistedFlag('awg-manager-compact-layout', false);

export const compactLayout = {
	subscribe: store.subscribe,
	init: store.init,
	setEnabled: store.set,
};
