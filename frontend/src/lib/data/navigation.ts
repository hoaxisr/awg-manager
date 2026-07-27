import { Server, Waypoints, Globe, Activity, Wrench, Settings } from 'lucide-svelte';

/**
 * lucide-svelte v1 экспортирует классовые компоненты (SvelteComponentTyped) —
 * тип иконки в проекте = typeof <Иконка> (прецедент: lib/utils/policy-icon.ts).
 */
export type NavIcon = typeof Server;

export type NavGroupId = 'awg' | 'sb' | 'router' | 'services';

export interface NavItem {
	id: string;
	label: string;
	href: string;
	match: (url: URL) => boolean;
}

export interface NavGroup {
	kind: 'group';
	id: NavGroupId;
	label: string;
	icon: NavIcon;
	items: NavItem[];
}

export interface NavLink extends NavItem {
	kind: 'link';
	icon: NavIcon;
}

export type NavEntry = NavGroup | NavLink;

const tab = (url: URL) => url.searchParams.get('tab');
const isPath = (url: URL, ...prefixes: string[]) =>
	prefixes.some((p) => url.pathname === p || url.pathname.startsWith(p + '/'));

// Фаза 1: hrefs указывают на СУЩЕСТВУЮЩИЕ адреса (вкладки главной и /routing).
// В фазе 2, при расщеплении контейнеров, hrefs и match меняются на финальные
// (/awg/tunnels, /sb/*, /router/*, /services/*) — см. спеку, раздел 3.
export const NAV_TREE: NavEntry[] = [
	{
		kind: 'group',
		id: 'awg',
		label: 'AmneziaWG',
		icon: Server,
		items: [
			{
				id: 'awg-tunnels',
				label: 'Туннели',
				href: '/',
				match: (url) =>
					(url.pathname === '/' && (tab(url) === null || tab(url) === 'awg')) ||
					isPath(url, '/tunnels', '/system-tunnels'),
			},
			{
				id: 'awg-servers',
				label: 'Серверы',
				href: '/awg/servers',
				match: (url) => isPath(url, '/awg/servers'),
			},
		],
	},
	{
		kind: 'group',
		id: 'sb',
		label: 'Sing-box',
		icon: Waypoints,
		items: [
			{
				id: 'sb-tunnels',
				label: 'Туннели',
				href: '/?tab=singbox',
				match: (url) => (url.pathname === '/' && tab(url) === 'singbox') || isPath(url, '/singbox'),
			},
			{
				// Принцип IA: все sing-box inbound/outbound/endpoint — подпункты
				// группы Sing-box. AWG3 — endpoint sing-box, поэтому здесь.
				id: 'sb-awg3',
				label: 'AWG3',
				href: '/?tab=awg3',
				match: (url) => url.pathname === '/' && tab(url) === 'awg3',
			},
			{
				id: 'sb-subs',
				label: 'Подписки',
				href: '/?tab=subscriptions',
				match: (url) =>
					(url.pathname === '/' && tab(url) === 'subscriptions') || isPath(url, '/subscriptions'),
			},
			{
				id: 'sb-routing',
				label: 'Маршрутизация',
				href: '/routing?tab=singbox',
				match: (url) =>
					isPath(url, '/routing') &&
					(tab(url) === 'singbox' || tab(url) === 'fakeip' || tab(url) === null),
			},
			{
				id: 'sb-geodata',
				label: 'Гео-данные',
				href: '/routing?tab=geodata',
				match: (url) => isPath(url, '/routing') && tab(url) === 'geodata',
			},
		],
	},
	{
		kind: 'group',
		id: 'router',
		label: 'Роутер',
		icon: Globe,
		items: [
			{
				id: 'router-ndms',
				label: 'NDMS',
				href: '/routing?tab=dns',
				match: (url) => isPath(url, '/routing') && tab(url) === 'dns',
			},
			{
				id: 'router-ip',
				label: 'IP-адреса',
				href: '/routing?tab=ip',
				match: (url) => isPath(url, '/routing') && tab(url) === 'ip',
			},
			{
				id: 'router-device-vpn',
				label: 'VPN для устройств',
				href: '/routing?tab=clientvpn',
				match: (url) => isPath(url, '/routing') && tab(url) === 'clientvpn',
			},
			{
				id: 'router-policies',
				label: 'Политики доступа',
				href: '/routing?tab=policy',
				match: (url) => isPath(url, '/routing') && tab(url) === 'policy',
			},
		],
	},
	{
		kind: 'group',
		id: 'services',
		label: 'Сервисы',
		icon: Activity,
		items: [
			{
				id: 'svc-freeturn',
				label: 'FreeTurn',
				href: '/?tab=freeturn',
				match: (url) =>
					(url.pathname === '/' && tab(url) === 'freeturn') || isPath(url, '/freeturn'),
			},
			{
				id: 'svc-wdtt',
				label: 'WDTT',
				href: '/?tab=wdtt',
				match: (url) => (url.pathname === '/' && tab(url) === 'wdtt') || isPath(url, '/wdtt'),
			},
			{
				id: 'svc-hrneo',
				label: 'HR Neo',
				href: '/routing?tab=hrneo',
				match: (url) => isPath(url, '/routing') && tab(url) === 'hrneo',
			},
		],
	},
	{
		kind: 'link',
		id: 'tools',
		label: 'Инструменты',
		icon: Wrench,
		href: '/tools',
		match: (url) => isPath(url, '/tools'),
	},
	{
		kind: 'link',
		id: 'settings',
		label: 'Настройки',
		icon: Settings,
		href: '/settings',
		match: (url) => isPath(url, '/settings'),
	},
];

/** Разделы вне дерева навигации, но с крошкой в шапке. */
const EXTRA_LABELS: Record<string, string> = {
	'/terminal': 'Терминал',
	'/api-docs': 'API-справка',
};

export function activeItem(url: URL): { group: NavGroup | null; item: NavItem | NavLink } | null {
	for (const entry of NAV_TREE) {
		if (entry.kind === 'link') {
			if (entry.match(url)) return { group: null, item: entry };
			continue;
		}
		for (const item of entry.items) {
			if (item.match(url)) return { group: entry, item };
		}
	}
	return null;
}

export function breadcrumbFor(url: URL): { group: string | null; label: string } | null {
	const active = activeItem(url);
	if (active) return { group: active.group?.label ?? null, label: active.item.label };
	const extra = Object.entries(EXTRA_LABELS).find(([p]) => isPath(url, p));
	return extra ? { group: null, label: extra[1] } : null;
}
