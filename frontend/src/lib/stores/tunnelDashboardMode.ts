// Dashboard mode for the tunnels page (issue #142): an OPT-IN alternative to
// the AWG / Sing-box / Subscriptions tabs that shows every tunnel kind on one
// screen. Three persisted knobs:
//   - tunnelDashboardMode   — the master switch (default OFF → tabs, the
//     pre-existing UI stays byte-identical until the user opts in)
//   - tunnelDashboardLayout — 'sections' (collapsible per-kind groups) or
//     'flat' (one merged list, kind→name order)
//   - tunnelDashboardView   — card density for the dashboard, independent
//     from the per-tab view modes so switching modes never clobbers them.
//     Default 'dense': a fresh dashboard shows full AWG cards with traffic
//     graphs in both layouts (issue: flat layout looked degraded).
import type { SingboxLayoutMode } from '$lib/constants/singboxLayout';
import { parseSingboxLayoutMode } from '$lib/constants/singboxLayout';
import { createPersistedFlag, createPersistedStore } from './persisted';

export type TunnelDashboardLayout = 'flat' | 'sections';

const modeStore = createPersistedFlag('awg-manager-tunnel-dashboard-mode', false);

export const tunnelDashboardMode = {
	subscribe: modeStore.subscribe,
	init: modeStore.init,
	setEnabled: modeStore.set,
};

const layoutStore = createPersistedStore<TunnelDashboardLayout>(
	'awg-manager-tunnel-dashboard-layout',
	{
		defaultValue: 'sections',
		deserialize: (raw) => (raw === 'flat' || raw === 'sections' ? raw : 'sections'),
		serialize: (layout) => layout,
	},
);

export const tunnelDashboardLayout = {
	subscribe: layoutStore.subscribe,
	init: layoutStore.init,
	setLayout: layoutStore.set,
};

const viewStore = createPersistedStore<SingboxLayoutMode>('awg-manager-tunnel-dashboard-view', {
	defaultValue: 'dense',
	deserialize: (raw) => parseSingboxLayoutMode(raw) ?? 'dense',
	serialize: (mode) => mode,
});

export const tunnelDashboardView = {
	subscribe: viewStore.subscribe,
	init: viewStore.init,
	setViewMode: viewStore.set,
};
