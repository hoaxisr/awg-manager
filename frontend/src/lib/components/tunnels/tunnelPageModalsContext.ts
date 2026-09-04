// Контекст модалок страницы туннелей (класс 2 декомпозиции +page.svelte).
// Страница собирает объект с live-геттерами поверх своих $state/$derived и
// передаёт одним пропом в TunnelPageModals — чтение геттера в шаблоне
// отслеживает исходную реактивность, запись в сеттеры мутирует состояние
// страницы.
import type {
	TunnelListItem,
	SystemTunnel,
	SingboxTunnel,
	Subscription,
	TunnelReferencedError,
} from '$lib/types';
import type { SubscriptionActiveCardVM } from '$lib/components/subscriptions/subscriptionVMs';

export interface TunnelPageModalsContext {
	// --- derived (только чтение) ---
	readonly awgList: TunnelListItem[];
	readonly systemList: SystemTunnel[];
	readonly singboxTunnelsList: SingboxTunnel[];
	readonly subscriptionsActiveCards: SubscriptionActiveCardVM[];
	readonly subscriptionsListRows: Subscription[];
	readonly liveActives: Record<string, string>;
	readonly pendingSubscriptionLabel: string;
	// --- state (страница владеет; часть пишется модалками) ---
	adoptDialogOpen: boolean;
	adoptError: string;
	adoptLoading: boolean;
	readonly adoptingInterface: string;
	deleteConfirmId: string | null;
	unlockConfirmId: string | null;
	referencedDetails: TunnelReferencedError | null;
	referencedTunnelName: string;
	createModalOpen: boolean;
	readonly wizardPreselect: 'choose' | 'single' | 'inline' | 'url';
	openAwg3Import: () => void;
	readonly awg3Visible: boolean;
	pendingSubscriptionDelete: string | null;
	readonly deletingSubscription: boolean;
	readonly detailId: string | null;
	readonly singboxDetailTag: string | null;
	readonly awgDiagnosticsTarget: { id: string; name: string; kind: 'awg' | 'system' } | null;
	readonly connectivitySettingsTunnel: TunnelListItem | null;
	connectivitySettingsOpen: boolean;
	// --- обработчики страницы ---
	handleAdopt: (data: { content: string; name: string }) => Promise<void>;
	handleDelete: (id: string) => Promise<void>;
	confirmUnlock: () => Promise<void>;
	confirmSubscriptionDelete: () => Promise<void>;
	closeDetail: () => void;
	closeSingboxDetail: () => void;
	closeAwgDiagnostics: () => void;
	closeConnectivitySettings: () => void;
}
