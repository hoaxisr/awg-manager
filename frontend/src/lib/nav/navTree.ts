import type { Component } from 'svelte';
import type { UsageLevel, Section } from '$lib/types/usageLevel';
import {
	isSectionVisible,
	isRoutingSubTabVisible,
	type RoutingSubTab,
} from '$lib/types/usageLevel';

export type NavIcon = Component<{ size?: number; strokeWidth?: number; class?: string }>;

export type NavLeaf = {
	kind: 'leaf';
	id: string;
	label: string;
	href: string;
	match: (pathname: string, tab: string | null) => boolean;
	badge?: number;
	section?: Section;
};

export type NavGroup = {
	kind: 'group';
	id: string;
	label: string;
	href: string;
	match: (pathname: string, tab: string | null) => boolean;
	children: NavLeaf[];
};

export type NavSection = {
	kind: 'section';
	id: string;
	label: string;
	href: string;
	icon: 'tunnels' | 'servers' | 'routing' | 'diagnostics' | 'settings';
	match: (pathname: string, tab: string | null) => boolean;
	children: (NavLeaf | NavGroup)[];
};

export type NavTreeCtx = {
	usageLevel: UsageLevel;
	isOS5: boolean;
	singboxInstalled: boolean;
	hydrarouteInstalled: boolean;
	fakeipModeActive: boolean;
	badges?: {
		awg?: number;
		singbox?: number;
		subscriptions?: number;
		awg3?: number;
	};
};

function defaultTabMatch(tab: string | null, id: string): boolean {
	return tab === null || tab === id;
}

function tunnelsSectionMatch(pathname: string): boolean {
	return (
		pathname === '/' ||
		pathname.startsWith('/tunnels') ||
		pathname.startsWith('/system-tunnels') ||
		pathname.startsWith('/freeturn') ||
		pathname.startsWith('/wdtt') ||
		pathname.startsWith('/singbox') ||
		pathname.startsWith('/subscriptions')
	);
}

function awgLeafMatch(pathname: string, tab: string | null): boolean {
	if (pathname.startsWith('/tunnels') || pathname.startsWith('/system-tunnels')) return true;
	if (pathname === '/') return defaultTabMatch(tab, 'awg');
	return false;
}

function singboxTunnelLeafMatch(pathname: string, tab: string | null): boolean {
	if (pathname.startsWith('/singbox')) return true;
	if (pathname === '/') return tab === 'singbox';
	return false;
}

function subscriptionsLeafMatch(pathname: string, tab: string | null): boolean {
	if (pathname.startsWith('/subscriptions')) return true;
	if (pathname === '/') return tab === 'subscriptions';
	return false;
}

function freeturnLeafMatch(pathname: string, tab: string | null): boolean {
	if (pathname.startsWith('/freeturn')) return true;
	if (pathname === '/') return tab === 'freeturn';
	return false;
}

function wdttLeafMatch(pathname: string, tab: string | null): boolean {
	if (pathname.startsWith('/wdtt')) return true;
	if (pathname === '/') return tab === 'wdtt';
	return false;
}

function awg3LeafMatch(pathname: string, tab: string | null): boolean {
	if (pathname === '/') return tab === 'awg3';
	return false;
}

function routingSectionMatch(pathname: string): boolean {
	return pathname.startsWith('/routing');
}

function routingTabMatch(pathname: string, tab: string | null, tabId: string, isDefault = false): boolean {
	if (!routingSectionMatch(pathname)) return false;
	if (isDefault) return defaultTabMatch(tab, tabId);
	return tab === tabId;
}

function diagnosticsSectionMatch(pathname: string): boolean {
	return pathname.startsWith('/diagnostics') || pathname.startsWith('/logs');
}

function diagnosticsTabMatch(pathname: string, tab: string | null, tabId: string, isDefault = false): boolean {
	if (!diagnosticsSectionMatch(pathname)) return false;
	if (isDefault) return defaultTabMatch(tab, tabId);
	return tab === tabId;
}

function routingSubVisible(level: UsageLevel, sub: RoutingSubTab): boolean {
	return isRoutingSubTabVisible(level, sub);
}

export function isNavItemActive(
	item: NavLeaf | NavGroup | NavSection,
	pathname: string,
	tab: string | null,
): boolean {
	if (item.match(pathname, tab)) return true;
	if (item.kind === 'section' || item.kind === 'group') {
		return item.children.some((child) => isNavItemActive(child, pathname, tab));
	}
	return false;
}

export function buildNavTree(ctx: NavTreeCtx): NavSection[] {
	const { usageLevel: level, isOS5, singboxInstalled, hydrarouteInstalled, fakeipModeActive, badges } =
		ctx;

	const tree: NavSection[] = [];

	if (isSectionVisible(level, 'tunnels')) {
		const tunnelChildren: (NavLeaf | NavGroup)[] = [
			{
				kind: 'leaf',
				id: 'awg',
				label: 'AmneziaWG',
				href: '/',
				match: awgLeafMatch,
				badge: badges?.awg,
				section: 'tunnels',
			},
		];

		const singboxGroupChildren: NavLeaf[] = [];
		if (isSectionVisible(level, 'singboxTunnels')) {
			singboxGroupChildren.push({
				kind: 'leaf',
				id: 'singbox',
				label: 'Туннели',
				href: '/?tab=singbox',
				match: singboxTunnelLeafMatch,
				badge: badges?.singbox,
				section: 'singboxTunnels',
			});
			singboxGroupChildren.push({
				kind: 'leaf',
				id: 'subscriptions',
				label: 'Подписки',
				href: '/?tab=subscriptions',
				match: subscriptionsLeafMatch,
				badge: badges?.subscriptions,
				section: 'singboxTunnels',
			});
		}
		if (singboxInstalled) {
			singboxGroupChildren.push({
				kind: 'leaf',
				id: 'awg3',
				label: 'WG endpoints',
				href: '/?tab=awg3',
				match: awg3LeafMatch,
				badge: badges?.awg3,
			});
		}
		if (singboxGroupChildren.length > 0) {
			const firstChild = singboxGroupChildren[0];
			tunnelChildren.push({
				kind: 'group',
				id: 'singbox-tunnels',
				label: 'Sing-box',
				href: firstChild.href,
				match: (_pathname, tab) =>
					singboxGroupChildren.some((child) => child.match(_pathname, tab)),
				children: singboxGroupChildren,
			});
		}

		if (isSectionVisible(level, 'freeturn')) {
			tunnelChildren.push({
				kind: 'leaf',
				id: 'freeturn',
				label: 'FreeTurn',
				href: '/?tab=freeturn',
				match: freeturnLeafMatch,
				section: 'freeturn',
			});
		}
		if (isSectionVisible(level, 'wdtt')) {
			tunnelChildren.push({
				kind: 'leaf',
				id: 'wdtt',
				label: 'WDTT',
				href: '/?tab=wdtt',
				match: wdttLeafMatch,
				section: 'wdtt',
			});
		}

		tree.push({
			kind: 'section',
			id: 'tunnels',
			label: 'Туннели',
			href: '/',
			icon: 'tunnels',
			match: (pathname) => tunnelsSectionMatch(pathname),
			children: tunnelChildren,
		});
	}

	if (isSectionVisible(level, 'servers')) {
		tree.push({
			kind: 'section',
			id: 'servers',
			label: 'Серверы',
			href: '/servers',
			icon: 'servers',
			match: (pathname) => pathname.startsWith('/servers'),
			children: [],
		});
	}

	if (isSectionVisible(level, 'routing')) {
		const routingChildren: (NavLeaf | NavGroup)[] = [];

		if (isOS5 && routingSubVisible(level, 'dnsRoutes')) {
			routingChildren.push({
				kind: 'leaf',
				id: 'routing-dns',
				label: 'NDMS',
				href: '/routing',
				match: (pathname, tab) => routingTabMatch(pathname, tab, 'dns', true),
			});
		}
		if (routingSubVisible(level, 'ipRoutes')) {
			routingChildren.push({
				kind: 'leaf',
				id: 'routing-ip',
				label: 'IP-адреса',
				href: '/routing?tab=ip',
				match: (pathname, tab) => routingTabMatch(pathname, tab, 'ip'),
			});
		}
		if (routingSubVisible(level, 'clientRoutes')) {
			routingChildren.push({
				kind: 'leaf',
				id: 'routing-clientvpn',
				label: 'VPN для устройств',
				href: '/routing?tab=clientvpn',
				match: (pathname, tab) => routingTabMatch(pathname, tab, 'clientvpn'),
			});
		}
		if (routingSubVisible(level, 'accessPolicies')) {
			routingChildren.push({
				kind: 'leaf',
				id: 'routing-policy',
				label: 'Политики доступа',
				href: '/routing?tab=policy',
				match: (pathname, tab) => routingTabMatch(pathname, tab, 'policy'),
			});
		}

		const singboxRouterChildren: NavLeaf[] = [];
		const showTproxy = singboxInstalled && routingSubVisible(level, 'singboxRouter');
		const showFakeip =
			singboxInstalled && (routingSubVisible(level, 'singboxRouter') || fakeipModeActive);

		if (showTproxy) {
			singboxRouterChildren.push({
				kind: 'leaf',
				id: 'singbox',
				label: 'TProxy',
				href: '/routing?tab=singbox',
				match: (pathname, tab) => routingTabMatch(pathname, tab, 'singbox'),
			});
		}
		if (showFakeip) {
			singboxRouterChildren.push({
				kind: 'leaf',
				id: 'fakeip',
				label: 'FakeIP',
				href: '/routing?tab=fakeip',
				match: (pathname, tab) => routingTabMatch(pathname, tab, 'fakeip'),
			});
		}
		if (singboxRouterChildren.length > 0) {
			const firstChild = singboxRouterChildren[0];
			routingChildren.push({
				kind: 'group',
				id: 'singbox-routing',
				label: 'Sing-box',
				href: firstChild.href,
				match: (_pathname, tab) =>
					singboxRouterChildren.some((child) => child.match(_pathname, tab)),
				children: singboxRouterChildren,
			});
		}

		if (hydrarouteInstalled && routingSubVisible(level, 'hrNeo')) {
			routingChildren.push({
				kind: 'leaf',
				id: 'routing-hrneo',
				label: 'HR Neo',
				href: '/routing?tab=hrneo',
				match: (pathname, tab) => routingTabMatch(pathname, tab, 'hrneo'),
			});
		}
		if (
			(hydrarouteInstalled || singboxInstalled) &&
			routingSubVisible(level, 'geoData')
		) {
			routingChildren.push({
				kind: 'leaf',
				id: 'routing-geodata',
				label: 'Гео-данные',
				href: '/routing?tab=geodata',
				match: (pathname, tab) => routingTabMatch(pathname, tab, 'geodata'),
			});
		}

		let routingHref = '/routing';
		if (routingChildren.length > 0) {
			const first = routingChildren[0];
			routingHref = first.kind === 'group' ? first.children[0]?.href ?? '/routing' : first.href;
		}

		tree.push({
			kind: 'section',
			id: 'routing',
			label: 'Маршрутизация',
			href: routingHref,
			icon: 'routing',
			match: (pathname) => routingSectionMatch(pathname),
			children: routingChildren,
		});
	}

	if (isSectionVisible(level, 'diagnostics')) {
		const diagnosticsChildren: NavLeaf[] = [
			{
				kind: 'leaf',
				id: 'diagnostics-logs',
				label: 'Журнал',
				href: '/diagnostics',
				match: (pathname, tab) => diagnosticsTabMatch(pathname, tab, 'logs', true),
			},
			{
				kind: 'leaf',
				id: 'diagnostics-monitoring',
				label: 'Мониторинг',
				href: '/diagnostics?tab=monitoring',
				match: (pathname, tab) => diagnosticsTabMatch(pathname, tab, 'monitoring'),
			},
			{
				kind: 'leaf',
				id: 'diagnostics-connections',
				label: 'Соединения',
				href: '/diagnostics?tab=connections',
				match: (pathname, tab) => diagnosticsTabMatch(pathname, tab, 'connections'),
			},
			{
				kind: 'leaf',
				id: 'diagnostics-checks',
				label: 'Проверки',
				href: '/diagnostics?tab=checks',
				match: (pathname, tab) => diagnosticsTabMatch(pathname, tab, 'checks'),
			},
			{
				kind: 'leaf',
				id: 'diagnostics-about',
				label: 'Окружение',
				href: '/diagnostics?tab=about',
				match: (pathname, tab) => diagnosticsTabMatch(pathname, tab, 'about'),
			},
		];

		if (level === 'expert') {
			diagnosticsChildren.push({
				kind: 'leaf',
				id: 'diagnostics-awgconfig',
				label: 'Конфиг AWG',
				href: '/diagnostics?tab=awgConfig',
				match: (pathname, tab) => diagnosticsTabMatch(pathname, tab, 'awgConfig'),
			});
			diagnosticsChildren.push({
				kind: 'leaf',
				id: 'diagnostics-dns',
				label: 'Сведения о DNS',
				href: '/diagnostics?tab=dns',
				match: (pathname, tab) => diagnosticsTabMatch(pathname, tab, 'dns'),
			});
		}

		tree.push({
			kind: 'section',
			id: 'diagnostics',
			label: 'Инструменты',
			href: '/diagnostics',
			icon: 'diagnostics',
			match: (pathname) => diagnosticsSectionMatch(pathname),
			children: diagnosticsChildren,
		});
	}

	if (isSectionVisible(level, 'settings')) {
		tree.push({
			kind: 'section',
			id: 'settings',
			label: 'Настройки',
			href: '/settings',
			icon: 'settings',
			match: (pathname) => pathname.startsWith('/settings'),
			children: [],
		});
	}

	return tree;
}
