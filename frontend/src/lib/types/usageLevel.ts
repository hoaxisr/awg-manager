// Single source of truth for which sections / sub-tabs are visible at each
// usage level. Imported by AppHeader, +layout.svelte (route guard),
// routing/+page.svelte (sub-tabs), UsageLevelCard, and WelcomeBanner.

export type UsageLevel = 'basic' | 'advanced' | 'expert';

export const USAGE_LEVELS: UsageLevel[] = ['basic', 'advanced', 'expert'];

export const USAGE_LEVEL_LABELS: Record<UsageLevel, string> = {
	basic: 'Базовый',
	advanced: 'Расширенный',
	expert: 'Продвинутый',
};

export type Section =
	| 'tunnels'
	| 'systemTunnels'
	| 'singboxTunnels'
	| 'servers'
	| 'subscriptions'
	| 'routing'
	| 'monitoring'
	| 'diagnostics'
	| 'settings'
	| 'terminal';

export type RoutingSubTab =
	| 'accessPolicies'
	| 'clientRoutes'
	| 'dnsRoutes'
	| 'ipRoutes'
	| 'hrNeo'
	| 'geoData'
	| 'singboxRouter';

const SECTION_MIN_LEVEL: Record<Section, UsageLevel> = {
	tunnels: 'basic',
	systemTunnels: 'basic',
	diagnostics: 'basic',
	settings: 'basic',
	routing: 'basic',
	singboxTunnels: 'advanced',
	servers: 'advanced',
	subscriptions: 'advanced',
	monitoring: 'advanced',
	terminal: 'advanced',
};

const ROUTING_SUBTAB_MIN_LEVEL: Record<RoutingSubTab, UsageLevel> = {
	accessPolicies: 'advanced',
	clientRoutes: 'basic',
	dnsRoutes: 'basic',
	ipRoutes: 'advanced',
	hrNeo: 'expert',
	geoData: 'expert',
	singboxRouter: 'expert',
};

const LEVEL_RANK: Record<UsageLevel, number> = { basic: 0, advanced: 1, expert: 2 };

/** Блок «Внешний вид» / цветовая схема — не ниже «Расширенного». */
export const APPEARANCE_SETTINGS_MIN_LEVEL: UsageLevel = 'advanced';

/** Переключатель stable/develop — только на «Продвинутом». */
export const UPDATE_CHANNEL_MIN_LEVEL: UsageLevel = 'expert';

/** Подробности маршрута служебных загрузок: "через Direct (WAN)", route labels и transport hints. */
export const DOWNLOAD_ROUTE_DETAILS_MIN_LEVEL: UsageLevel = 'expert';

export function isUsageLevelAtLeast(level: UsageLevel, minimum: UsageLevel): boolean {
	return LEVEL_RANK[level] >= LEVEL_RANK[minimum];
}

export function isAppearanceSettingsVisible(level: UsageLevel): boolean {
	return isUsageLevelAtLeast(level, APPEARANCE_SETTINGS_MIN_LEVEL);
}

export function isUpdateChannelSwitchVisible(level: UsageLevel): boolean {
	return isUsageLevelAtLeast(level, UPDATE_CHANNEL_MIN_LEVEL);
}

export function areDownloadRouteDetailsVisible(level: UsageLevel): boolean {
	return isUsageLevelAtLeast(level, DOWNLOAD_ROUTE_DETAILS_MIN_LEVEL);
}

export function isSectionVisible(level: UsageLevel, section: Section): boolean {
	return LEVEL_RANK[level] >= LEVEL_RANK[SECTION_MIN_LEVEL[section]];
}

export function isRoutingSubTabVisible(level: UsageLevel, tab: RoutingSubTab): boolean {
	return LEVEL_RANK[level] >= LEVEL_RANK[ROUTING_SUBTAB_MIN_LEVEL[tab]];
}

// Map a URL pathname to its Section. null = unknown path; the route guard
// must skip those (404 / dev pages remain accessible).
export function pathToSection(pathname: string): Section | null {
	if (pathname === '/' || pathname.startsWith('/tunnels')) return 'tunnels';
	if (pathname.startsWith('/system-tunnels')) return 'systemTunnels';
	if (pathname.startsWith('/singbox')) return 'singboxTunnels';
	if (pathname.startsWith('/servers')) return 'servers';
	if (pathname.startsWith('/subscriptions')) return 'subscriptions';
	if (pathname.startsWith('/routing')) return 'routing';
	if (
		pathname.startsWith('/monitoring') ||
		pathname.startsWith('/pingcheck') ||
		pathname.startsWith('/connections')
	)
		return 'monitoring';
	if (pathname.startsWith('/diagnostics') || pathname.startsWith('/logs')) return 'diagnostics';
	if (pathname.startsWith('/settings')) return 'settings';
	if (pathname.startsWith('/terminal')) return 'terminal';
	return null;
}

export const SECTION_LABELS: Record<Section, string> = {
	tunnels: 'Туннели',
	systemTunnels: 'Системные туннели',
	singboxTunnels: 'Sing-box',
	servers: 'Серверы',
	subscriptions: 'Подписки',
	routing: 'Маршрутизация',
	monitoring: 'Мониторинг',
	diagnostics: 'Диагностика',
	settings: 'Настройки',
	terminal: 'Терминал',
};
