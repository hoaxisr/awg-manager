// Контекст flat-режима дашборда (класс 2 декомпозиции +page.svelte):
// страница собирает объект с live-геттерами поверх своих $state/$derived
// и передаёт одним пропом в DashboardFlatSection.
import type { TunnelDashboardFlatItem } from '$lib/utils/tunnelDashboardFlat';
import type { SingboxLayoutMode, TunnelRenderMode } from '$lib/constants/singboxLayout';
import type { createReorderDrag } from '$lib/components/sb-router/reorderDrag.svelte';

export interface DashboardTagGroupEntry {
	item: TunnelDashboardFlatItem;
	autoCheck: boolean;
}

export interface DashboardFlatContext {
	// --- derived ---
	readonly DASHBOARD_KIND_LABELS: Record<TunnelDashboardFlatItem['kind'], string>;
	readonly dashboardDndEnabled: boolean;
	readonly dashboardFilterEmpty: boolean;
	readonly dashboardFlatCardMode: boolean;
	readonly dashboardFlatLayout: boolean;
	readonly dashboardGridClass: string;
	readonly dashboardGroupByTags: boolean;
	readonly dashboardRenderItems: TunnelDashboardFlatItem[];
	readonly dashboardSummaryStats: Array<{ value: string; label: string; sub?: string }>;
	readonly dashboardTagGroups: Array<{ tag: string | null; items: DashboardTagGroupEntry[] }>;
	readonly effectiveAwgCardViewMode: 'cards' | 'compact';
	readonly effectiveAwgRenderMode: TunnelRenderMode;
	readonly effectiveSingboxTunnelsEffectiveLayout: SingboxLayoutMode;
	readonly effectiveSingboxTunnelsRenderMode: TunnelRenderMode;
	readonly effectiveSingboxSubscriptionsEffectiveLayout: SingboxLayoutMode;
	readonly effectiveSingboxSubscriptionsRenderMode: TunnelRenderMode;
	readonly showSingboxListOption: boolean;
	readonly showSingboxSections: boolean;
	/** sing-box установлен — без него операции с AWG3 отклоняет бэкенд. */
	readonly awg3Visible: boolean;
	readonly exporting: boolean;
	readonly awgAutoConnectivityNonce: number;
	readonly singboxAutoDelayCheckNonce: number;
	readonly deleteLoading: Record<string, boolean>;
	readonly toggleLoading: Record<string, boolean>;
	readonly liveActives: Record<string, string>;
	readonly flatDrag: ReturnType<typeof createReorderDrag>;
	readonly flatRowEls: Array<HTMLElement | null>;
	// --- state с записью из секции ---
	dashboardSearchQuery: string;
	dashboardTagFilter: string | null;
	flatGridEl: HTMLElement | null;
	// --- обработчики ---
	handleAdoptClick(interfaceName: string): void;
	handleExportAll(): Promise<void>;
	handleGripKeydown(index: number, event: KeyboardEvent): void;
	handleGripPointerDown(index: number, event: PointerEvent): void;
	handleToggleOnOff(id: string): Promise<void>;
	markAsServer(id: string): Promise<void>;
	openAwg3Import(): void;
	openAwgDiagnostics(id: string, name: string, kind?: 'awg' | 'system'): void;
	openDetail(id: string): void;
	openSingboxDetail(tag: string): void;
	openWizard(preselect: 'choose' | 'single' | 'inline' | 'url'): void;
	requestDelete(id: string): void;
	handleLockClick(id: string): void;
	requestSubscriptionDelete(id: string): void;
}
