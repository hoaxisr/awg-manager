import {
	LayoutDashboard,
	Layers,
	Server,
	Waypoints,
	Globe,
	Activity,
	Wrench,
	Settings,
} from 'lucide-svelte';

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

const isPath = (url: URL, ...prefixes: string[]) =>
	prefixes.some((p) => url.pathname === p || url.pathname.startsWith(p + '/'));

// Фаза 2 завершена: контейнеры с вкладками расщеплены, каждый пункт ведёт
// на свой маршрут (/awg/*, /sb/*, /router/*, /services/*) и подсвечивается
// по пути — параметры поверхности (?edit=, ?view=, ?mode=) на это не влияют.
export const NAV_TREE: NavEntry[] = [
	{
		// Обзор — статусная сводка на корне. Матчер точный: префиксный '/'
		// совпал бы с любым путём и подсветил бы весь список сразу.
		kind: 'link',
		id: 'overview',
		label: 'Обзор',
		icon: LayoutDashboard,
		href: '/',
		match: (url) => url.pathname === '/',
	},
	{
		// Сводная страница всех видов туннелей.
		kind: 'link',
		id: 'tunnels-all',
		label: 'Все туннели',
		icon: Layers,
		href: '/tunnels',
		match: (url) => isPath(url, '/tunnels'),
	},
	{
		kind: 'group',
		id: 'awg',
		label: 'AmneziaWG',
		icon: Server,
		items: [
			{
				id: 'awg-tunnels',
				label: 'Туннели',
				href: '/awg/tunnels',
				// Префикс покрывает и детальные: /awg/tunnels/{id},
				// /awg/tunnels/new, /awg/tunnels/system/{name}.
				match: (url) => isPath(url, '/awg/tunnels'),
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
				href: '/sb/tunnels',
				match: (url) => isPath(url, '/sb/tunnels'),
			},
			{
				// Принцип IA: все sing-box inbound/outbound/endpoint — подпункты
				// группы Sing-box. AWG3 — endpoint sing-box, поэтому здесь.
				id: 'sb-awg3',
				label: 'AWG3',
				href: '/sb/awg3',
				match: (url) => isPath(url, '/sb/awg3'),
			},
			{
				id: 'sb-subs',
				label: 'Подписки',
				href: '/sb/subscriptions',
				match: (url) => isPath(url, '/sb/subscriptions'),
			},
			{
				id: 'sb-routing',
				label: 'Маршрутизация',
				href: '/sb/routing',
				// Поверхность (TProxy/FakeIP) выбирается локальным ?view=,
				// на подсветку раздела он не влияет — matcher по пути.
				match: (url) => isPath(url, '/sb/routing'),
			},
			{
				id: 'sb-geodata',
				label: 'Гео-данные',
				href: '/sb/geodata',
				match: (url) => isPath(url, '/sb/geodata'),
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
				href: '/router/ndms',
				// ?edit= из поиска — параметр поверхности, matcher по пути.
				match: (url) => isPath(url, '/router/ndms'),
			},
			{
				id: 'router-ip',
				label: 'IP-адреса',
				href: '/router/ip',
				match: (url) => isPath(url, '/router/ip'),
			},
			{
				id: 'router-device-vpn',
				label: 'VPN для устройств',
				href: '/router/device-vpn',
				match: (url) => isPath(url, '/router/device-vpn'),
			},
			{
				id: 'router-policies',
				label: 'Политики доступа',
				href: '/router/policies',
				match: (url) => isPath(url, '/router/policies'),
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
				href: '/services/freeturn',
				match: (url) => isPath(url, '/services/freeturn'),
			},
			{
				id: 'svc-wdtt',
				label: 'WDTT',
				href: '/services/wdtt',
				match: (url) => isPath(url, '/services/wdtt'),
			},
			{
				id: 'svc-hrneo',
				label: 'HR Neo',
				href: '/services/hrneo',
				match: (url) => isPath(url, '/services/hrneo'),
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
