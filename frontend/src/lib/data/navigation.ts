import {
	LayoutDashboard,
	Layers,
	Server,
	Waypoints,
	Globe,
	Activity,
	ScrollText,
	Gauge,
	Network,
	Stethoscope,
	Settings,
} from 'lucide-svelte';

/**
 * lucide-svelte v1 экспортирует классовые компоненты (SvelteComponentTyped) —
 * тип иконки в проекте = typeof <Иконка> (прецедент: lib/utils/policy-icon.ts).
 */
export type NavIcon = typeof Server;

export type NavGroupId = 'awg' | 'sb' | 'router' | 'services';

/**
 * Источник значения бейджа, а не само значение: этот модуль статический, а
 * счётчики и режим живут в сторах. Маппинг источник → стор — в
 * `stores/navBadges.ts`, рисует бейдж сайдбар.
 *
 * Источник заводится только под реально существующие данные. Счётчика DNS в
 * Status DTO движка нет, поэтому и источника `dns` здесь нет.
 */
export type NavBadgeSource = 'mode' | 'groups' | 'rules' | 'rule-sets' | 'connections';

export interface NavItem {
	id: string;
	label: string;
	href: string;
	match: (url: URL) => boolean;
	badge?: NavBadgeSource;
}

/**
 * Тихая подпись-разделитель внутри группы — типографская группировка, а не
 * уровень вложенности: у разделителя нет ни `href`, ни матчера, поэтому
 * активным его не сделать в принципе, а не «матчер вернул false».
 */
export interface NavSeparator {
	kind: 'separator';
	id: string;
	label: string;
}

export type NavGroupEntry = NavItem | NavSeparator;

export interface NavGroup {
	kind: 'group';
	id: NavGroupId;
	label: string;
	icon: NavIcon;
	items: NavGroupEntry[];
}

export interface NavLink extends NavItem {
	kind: 'link';
	icon: NavIcon;
}

export type NavEntry = NavGroup | NavLink;

export const isSeparator = (entry: NavGroupEntry): entry is NavSeparator =>
	'kind' in entry && entry.kind === 'separator';

/** Кликабельные пункты группы без разделителей. */
export const groupItems = (group: NavGroup): NavItem[] =>
	group.items.filter((entry): entry is NavItem => !isSeparator(entry));

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
		// Состав и порядок — таблица §2 спеки 5D. Страницы `/sb/routing` больше
		// нет: подэтап 5D1 разобрал её на отдельные маршруты, старый адрес
		// раздаёт закладки редиректом и пунктом меню быть перестал.
		items: [
			{
				id: 'sb-engine',
				label: 'Движок',
				href: '/sb/engine',
				// Единственное место, где текущий режим захвата виден с любой
				// страницы группы.
				badge: 'mode',
				match: (url) => isPath(url, '/sb/engine'),
			},
			{ kind: 'separator', id: 'sb-sep-outbounds', label: 'outbounds' },
			{
				id: 'sb-tunnels',
				label: 'Туннели',
				href: '/sb/tunnels',
				match: (url) => isPath(url, '/sb/tunnels'),
			},
			{
				// Принцип IA: все sing-box inbound/outbound/endpoint — подпункты
				// группы Sing-box. Маршрут остаётся /sb/awg3: у пользователей
				// есть закладки, переименование затрагивает только надпись.
				id: 'sb-awg3',
				label: 'Endpoints',
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
				id: 'sb-groups',
				label: 'Группы',
				href: '/sb/groups',
				badge: 'groups',
				match: (url) => isPath(url, '/sb/groups'),
			},
			{ kind: 'separator', id: 'sb-sep-rules', label: 'правила' },
			{
				id: 'sb-wizard',
				label: 'Мастер',
				href: '/sb/wizard',
				match: (url) => isPath(url, '/sb/wizard'),
			},
			{
				id: 'sb-rules',
				label: 'Маршруты',
				href: '/sb/rules',
				badge: 'rules',
				match: (url) => isPath(url, '/sb/rules'),
			},
			{
				id: 'sb-rule-sets',
				label: 'Rule sets',
				href: '/sb/rule-sets',
				badge: 'rule-sets',
				match: (url) => isPath(url, '/sb/rule-sets'),
			},
			{
				// Счётчика DNS в Status DTO движка нет — бейджа тоже нет.
				id: 'sb-dns',
				label: 'DNS',
				href: '/sb/dns',
				match: (url) => isPath(url, '/sb/dns'),
			},
			{ kind: 'separator', id: 'sb-sep-watch', label: 'наблюдение' },
			{
				id: 'sb-inbounds',
				label: 'Inbounds',
				href: '/sb/inbounds',
				match: (url) => isPath(url, '/sb/inbounds'),
			},
			{
				// «движка» в подписи обязательно: в свёрнутой группе голое
				// «Соединения» неотличимо от conntrack-пункта верхнего уровня.
				id: 'sb-connections',
				label: 'Соединения движка',
				href: '/sb/connections',
				badge: 'connections',
				match: (url) => isPath(url, '/sb/connections'),
			},
			{
				id: 'sb-logs',
				label: 'Журнал движка',
				href: '/sb/logs',
				match: (url) => isPath(url, '/sb/logs'),
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
		id: 'logs',
		label: 'Журнал',
		icon: ScrollText,
		href: '/logs',
		match: (url) => isPath(url, '/logs'),
	},
	{
		kind: 'link',
		id: 'monitoring',
		label: 'Мониторинг',
		icon: Gauge,
		href: '/monitoring',
		match: (url) => isPath(url, '/monitoring'),
	},
	{
		// Соединения роутера (conntrack). Соединения движка sing-box — своя
		// сущность и живут в группе Sing-box.
		kind: 'link',
		id: 'connections',
		label: 'Соединения',
		icon: Network,
		href: '/connections',
		match: (url) => isPath(url, '/connections'),
	},
	{
		kind: 'link',
		id: 'diagnostics',
		label: 'Диагностика',
		icon: Stethoscope,
		href: '/diagnostics',
		// ?tab= выбирает вкладку внутри страницы, на подсветку не влияет.
		match: (url) => isPath(url, '/diagnostics'),
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
		for (const item of groupItems(entry)) {
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
