// Контекст вкладки AWG страницы туннелей (класс 2 декомпозиции
// +page.svelte). Страница собирает объект с live-геттерами поверх своих
// $state/$derived и передаёт одним пропом в AwgTunnelsTabSection — чтение
// геттера в шаблоне секции отслеживает исходную реактивность, запись в
// сеттеры мутирует состояние страницы.
import type { TunnelListItem, SystemTunnel, ExternalTunnel, SystemInfo } from '$lib/types';
import type { AwgTunnelViewMode } from '$lib/components/ui/layoutViewToggle';
import type { AwgTunnelSortKey } from '$lib/stores/tunnelTableSort';
import type { TunnelRenderMode } from '$lib/constants/singboxLayout';

export type EndpointScope = 'managed' | 'system' | 'external';

export interface AwgTabContext {
	// --- derived (только чтение) ---
	readonly awgList: TunnelListItem[];
	readonly systemList: SystemTunnel[];
	readonly visibleSystemList: SystemTunnel[];
	readonly externalList: ExternalTunnel[];
	readonly sortedFilteredAwgList: TunnelListItem[];
	readonly sortedFilteredSystemList: SystemTunnel[];
	readonly sortedFilteredExternalList: ExternalTunnel[];
	readonly awgConnectivityMap: Map<string, { connected: boolean; latency: number | null }>;
	readonly statusLine: string;
	readonly sysInfo: SystemInfo | null;
	readonly awgSummaryActive: number;
	readonly awgSummaryPeak: { rate: number; name: string };
	readonly awgSummaryRx: number;
	readonly awgSummaryTx: number;
	readonly awgSummaryTotal: number;
	readonly awgTrafficLeader: { bytes: number; name: string };
	readonly nativewgHint: string;
	readonly dashboardOn: boolean;
	readonly dashboardSectionsLayout: boolean;
	readonly dashboardNothingAtAll: boolean;
	readonly awgSearchEmpty: boolean;
	readonly awgSourceRowCount: number;
	readonly showAwgViewModeSwitch: boolean;
	readonly effectiveAwgCardViewMode: 'cards' | 'compact';
	readonly effectiveAwgEffectiveViewMode: AwgTunnelViewMode;
	readonly effectiveAwgRenderMode: TunnelRenderMode;
	// --- state (страница владеет; часть пишется секцией) ---
	adoptDialogOpen: boolean;
	adoptingInterface: string;
	awgListSearchQuery: string;
	awgViewMode: AwgTunnelViewMode;
	selectedBackend: 'nativewg' | 'kernel';
	fileInput: HTMLInputElement | undefined;
	readonly awgAutoConnectivityNonce: number;
	readonly deleteLoading: Record<string, boolean>;
	readonly dragOver: boolean;
	readonly exporting: boolean;
	readonly importing: boolean;
	readonly pingChecking: Record<string, boolean>;
	readonly toggleLoading: Record<string, boolean>;
	// --- обработчики ---
	endpointHost(endpoint?: string | null): string;
	endpointPort(endpoint?: string | null): string;
	endpointVisible(scope: EndpointScope, id: string): boolean;
	toggleEndpointVisible(scope: EndpointScope, id: string): void;
	externalStatusLabel(tunnel: ExternalTunnel): string;
	externalStatusVariant(tunnel: ExternalTunnel): 'success' | 'muted';
	systemStatusLabel(tunnel: SystemTunnel): string;
	systemStatusVariant(tunnel: SystemTunnel): 'success' | 'muted';
	isManagedTunnelOn(tunnel: TunnelListItem): boolean;
	managedRouteMeta(tunnel: TunnelListItem): string;
	showManagedPing(
		tunnel: TunnelListItem,
		connectivity: { connected: boolean; latency: number | null } | undefined,
	): boolean;
	latestRate(id: string): { rx: number; tx: number };
	sparklineSeries(id: string): { rx: number[]; tx: number[] };
	handleAdoptClick(interfaceName: string): void;
	handleAwgSortChange(key: AwgTunnelSortKey): void;
	handleDragLeave(): void;
	handleDragOver(event: DragEvent): void;
	handleDrop(event: DragEvent): void;
	handleFileSelect(event: Event): void;
	openAwgDiagnostics(id: string, name: string, kind?: 'awg' | 'system'): void;
	openConnectivitySettings(tunnel: TunnelListItem): void;
	openDetail(id: string): void;
	requestDelete(id: string): void;
	handleLockClick(id: string): void;
	markAsServer(id: string): Promise<void>;
	handleToggleOnOff(id: string): Promise<void>;
	checkPing(id: string): Promise<void>;
	handleExportAll(): Promise<void>;
}
