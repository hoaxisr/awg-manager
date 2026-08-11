import { createPersistedFlag } from './persisted';

/** Top-of-page KPI / summary strips on tunnels & servers. Default on. */
const store = createPersistedFlag('awg-manager-show-summary', true);

export const showSummary = {
	subscribe: store.subscribe,
	init: store.init,
	setEnabled: store.set,
};
