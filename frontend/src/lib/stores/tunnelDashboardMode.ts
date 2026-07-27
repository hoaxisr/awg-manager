// Настройки сводной страницы туннелей /tunnels (issue #142). Персистентные
// ручки:
//   - tunnelDashboardLayout — 'sections' (per-kind groups) или 'flat'; после
//     расщепления навигации v3 роль «разделов» выполняет сайдбар, страница
//     значение не читает (переключатель снят) — стор оставлен под фазу 3
//   - tunnelDashboardView   — плотность карточек. Default 'dense': свежая
//     страница показывает полные AWG-карточки с графиками трафика.
import type { SingboxLayoutMode } from '$lib/constants/singboxLayout';
import { parseSingboxLayoutMode } from '$lib/constants/singboxLayout';
import { createPersistedStore } from './persisted';

export type TunnelDashboardLayout = 'flat' | 'sections';

export const TUNNEL_DASHBOARD_LAYOUT_LABELS: Record<TunnelDashboardLayout, string> = {
	flat: 'Сплошной',
	sections: 'Разделы',
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
