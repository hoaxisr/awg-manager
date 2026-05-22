import { get } from 'svelte/store';
import { api } from '$lib/api/client';
import { auth } from '$lib/stores/auth';
import { reloadSettings, settings, usageLevel } from '$lib/stores/settings';
import { singboxStatus } from '$lib/stores/singbox';
import { systemInfo } from '$lib/stores/system';
import { theme } from '$lib/stores/theme';
import type {
	AccessPolicy,
	DeviceProxyConfig,
	DeviceProxyRuntime,
	DnsCheckStartResponse,
	HydraRouteStatus,
	PolicyDevice,
	Subscription,
} from '$lib/types';
import {
	awgmServicesRows,
	browserSnapshotRows,
	buildAwgmServicesSnapshot,
	buildPolicyNameLookup,
	buildRouterClientContext,
	collectBrowserSnapshot,
	routerClientRows,
	routerStaticRows,
	type AboutInfoRow,
	type AwgmServicesSnapshot,
	type BrowserSnapshot,
	type RouterClientContext,
} from '$lib/utils/about-device';
import {
	isRoutingSubTabVisible,
	isSectionVisible,
	type UsageLevel,
} from '$lib/types/usageLevel';

export interface DiagnosticsEnvironmentSection {
	title: string;
	rows: AboutInfoRow[];
}

export interface DiagnosticsEnvironmentSnapshot {
	generatedAt: string;
	source: 'frontend';
	partial: boolean;
	privacy: {
		sanitized: boolean;
		rules: string[];
	};
	sections: DiagnosticsEnvironmentSection[];
	raw: {
		browser?: BrowserSnapshot;
		routerClient?: RouterClientContext | null;
		awgm?: AwgmServicesSnapshot | null;
	};
	errors: Array<{ scope: string; message: string }>;
}

type AwgmCounts = {
	hydra?: HydraRouteStatus | null;
	hydraLoaded?: boolean;
	deviceProxy?: DeviceProxyConfig | null;
	deviceProxyRuntime?: DeviceProxyRuntime | null;
	dnsRoutesTotal?: number;
	dnsRoutesEnabled?: number;
	dnsRoutesLoaded?: boolean;
	awgRunning?: number;
	awgTotal?: number;
	awgCountsLoaded?: boolean;
	subscriptionsEnabled?: number;
	subscriptionsTotal?: number;
	subscriptionsLoaded?: boolean;
};

const CLIENT_MAC_PLACEHOLDER = 'MAC-1';
const CLIENT_HOST_PLACEHOLDER = 'CLIENT-HOST-1';

function formatOffset(offsetMinutes: number): string {
	const sign = offsetMinutes >= 0 ? '+' : '-';
	const abs = Math.abs(offsetMinutes);
	const hh = String(Math.floor(abs / 60)).padStart(2, '0');
	const mm = String(abs % 60).padStart(2, '0');
	return `${sign}${hh}:${mm}`;
}

function nowWithOffset(offsetMinutes?: number): string {
	if (offsetMinutes === undefined || offsetMinutes === null || !Number.isFinite(offsetMinutes)) {
		const d = new Date();
		const localOffsetMinutes = -d.getTimezoneOffset();
		return nowWithOffset(localOffsetMinutes);
	}
	const d = new Date();
	const shifted = new Date(d.getTime() + offsetMinutes * 60_000);
	const pad = (n: number) => String(n).padStart(2, '0');
	return `${shifted.getUTCFullYear()}-${pad(shifted.getUTCMonth() + 1)}-${pad(shifted.getUTCDate())}T${pad(shifted.getUTCHours())}:${pad(shifted.getUTCMinutes())}:${pad(shifted.getUTCSeconds())}.${String(shifted.getUTCMilliseconds()).padStart(3, '0')}${formatOffset(offsetMinutes)}`;
}

function sanitizeRouterClientContext(ctx: RouterClientContext | null): RouterClientContext | null {
	if (!ctx) return ctx;
	return {
		...ctx,
		hostname: ctx.hostname ? CLIENT_HOST_PLACEHOLDER : ctx.hostname,
		device: ctx.device
			? {
				...ctx.device,
				mac: ctx.device.mac ? CLIENT_MAC_PLACEHOLDER : ctx.device.mac,
				name: ctx.device.name ? CLIENT_HOST_PLACEHOLDER : ctx.device.name,
				hostname: ctx.device.hostname ? CLIENT_HOST_PLACEHOLDER : ctx.device.hostname,
			}
			: ctx.device,
	};
}

function sanitizeClientRows(rows: AboutInfoRow[]): AboutInfoRow[] {
	return rows.map((row) => {
		if (row.label === 'MAC') {
			return { ...row, value: CLIENT_MAC_PLACEHOLDER, title: undefined };
		}
		if (row.label === 'Hostname' || row.label === 'Имя в NDMS') {
			return { ...row, value: CLIENT_HOST_PLACEHOLDER, title: undefined };
		}
		return row;
	});
}

async function capture<T>(
	scope: string,
	fn: () => Promise<T>,
	fallback: T,
	errors: Array<{ scope: string; message: string }>,
	markPartial: () => void,
): Promise<T> {
	try {
		return await fn();
	} catch (e) {
		markPartial();
		errors.push({ scope, message: e instanceof Error ? e.message : String(e) });
		return fallback;
	}
}

async function fetchAccessPolicies(level: UsageLevel): Promise<AccessPolicy[]> {
	if (!isSectionVisible(level, 'routing')) return [];
	const res = await fetch('/api/routing/access-policies');
	if (!res.ok) {
		throw new Error(`access-policies ${res.status}`);
	}
	const body = await res.json();
	return (body.data ?? []) as AccessPolicy[];
}

async function fetchClientContextOrThrow(): Promise<DnsCheckStartResponse> {
	try {
		const out = await api.getDnsCheckClient();
		if (!out) throw new Error('dns-check-client empty response');
		return out;
	} catch (firstError) {
		try {
			const out = await api.startDnsCheck();
			if (!out) throw new Error('dns-check-start empty response');
			return out;
		} catch (secondError) {
			const firstMsg = firstError instanceof Error ? firstError.message : String(firstError);
			const secondMsg = secondError instanceof Error ? secondError.message : String(secondError);
			throw new Error(`dns check unavailable: ${firstMsg}; fallback failed: ${secondMsg}`);
		}
	}
}

export async function collectDiagnosticsEnvironmentSnapshot(): Promise<DiagnosticsEnvironmentSnapshot> {
	const errors: Array<{ scope: string; message: string }> = [];
	let partial = false;
	const markPartial = () => { partial = true; };

	const refreshResults = await Promise.allSettled([
		systemInfo.refetch(),
		singboxStatus.refetch(),
		reloadSettings(),
	]);
	for (const [idx, result] of refreshResults.entries()) {
		if (result.status === 'rejected') {
			markPartial();
			const scope = idx === 0 ? 'refresh.systemInfo' : idx === 1 ? 'refresh.singboxStatus' : 'refresh.settings';
			errors.push({
				scope,
				message: result.reason instanceof Error ? result.reason.message : String(result.reason),
			});
		}
	}

	const level = get(usageLevel);

	const sys = get(systemInfo).data ?? null;
	const routerOffset = sys?.routerTimezoneOffsetMinutes;

	const browser = collectBrowserSnapshot();
	let routerClient: RouterClientContext | null = null;
	let awgm: AwgmServicesSnapshot | null = null;

	const counts: AwgmCounts = {};

	const dns = await capture('routerClient.dns', fetchClientContextOrThrow, null, errors, markPartial);
	const policies = await capture('routerClient.policies', () => fetchAccessPolicies(level), [] as AccessPolicy[], errors, markPartial);
	const devices = isRoutingSubTabVisible(level, 'clientRoutes')
		? await capture('routerClient.devices', () => api.listPolicyDevices(), null as PolicyDevice[] | null, errors, markPartial)
		: null;
	routerClient = buildRouterClientContext(dns, devices, buildPolicyNameLookup(policies));

	const snap = await capture('awgTunnels', () => api.getTunnelsAll(), null, errors, markPartial);
	if (snap?.tunnels) {
		const list = snap.tunnels ?? [];
		counts.awgTotal = list.length;
		counts.awgRunning = list.filter((t) => t.status === 'running').length;
		counts.awgCountsLoaded = true;
	}

	if (isRoutingSubTabVisible(level, 'dnsRoutes')) {
		await capture(
			'dnsRoutes',
			async () => {
				const res = await fetch('/api/routing/dns-routes');
				if (!res.ok) {
					throw new Error(`dns-routes ${res.status}`);
				}
				const body = await res.json();
				const lists = (body.data ?? []) as { enabled?: boolean }[];
				counts.dnsRoutesTotal = lists.length;
				counts.dnsRoutesEnabled = lists.filter((l) => l.enabled).length;
				counts.dnsRoutesLoaded = true;
				return true;
			},
			false,
			errors,
			markPartial,
		);
	}

	if (isRoutingSubTabVisible(level, 'clientRoutes')) {
		const [cfg, rt] = await Promise.all([
			capture('deviceProxy.config', () => api.getDeviceProxyConfig(), null as DeviceProxyConfig | null, errors, markPartial),
			capture('deviceProxy.runtime', () => api.getDeviceProxyRuntime(), null as DeviceProxyRuntime | null, errors, markPartial),
		]);
		counts.deviceProxy = cfg;
		counts.deviceProxyRuntime = rt;
	}

	if (isRoutingSubTabVisible(level, 'hrNeo')) {
		counts.hydra = await capture('hydraRoute', () => api.getHydraRouteStatus(), null as HydraRouteStatus | null, errors, markPartial);
		counts.hydraLoaded = true;
	}

	if (level !== 'basic') {
		const subs = await capture('subscriptions', () => api.listSubscriptions(), [] as Subscription[], errors, markPartial);
		counts.subscriptionsTotal = subs.length;
		counts.subscriptionsEnabled = subs.filter((s) => s.enabled).length;
		counts.subscriptionsLoaded = true;
	}

	awgm = await capture(
		'awgm',
		async () =>
			buildAwgmServicesSnapshot({
				level,
				theme: get(theme),
				settings: get(settings),
				authDisabled: get(auth).authDisabled,
				authenticated: get(auth).authenticated,
				login: get(auth).login,
				singbox: get(singboxStatus).data,
				hydra: counts.hydra ?? null,
				hydraLoaded: counts.hydraLoaded ?? false,
				showHydra: isRoutingSubTabVisible(level, 'hrNeo'),
				deviceProxy: counts.deviceProxy ?? null,
				deviceProxyRuntime: counts.deviceProxyRuntime ?? null,
				dnsRoutesTotal: counts.dnsRoutesTotal ?? 0,
				dnsRoutesEnabled: counts.dnsRoutesEnabled ?? 0,
				dnsRoutesLoaded: counts.dnsRoutesLoaded ?? false,
				showDnsRoutes: isRoutingSubTabVisible(level, 'dnsRoutes'),
				awgRunning: counts.awgRunning ?? 0,
				awgTotal: counts.awgTotal ?? 0,
				awgCountsLoaded: counts.awgCountsLoaded ?? false,
				subscriptionsEnabled: counts.subscriptionsEnabled ?? 0,
				subscriptionsTotal: counts.subscriptionsTotal ?? 0,
				subscriptionsLoaded: counts.subscriptionsLoaded ?? false,
			}),
		null as AwgmServicesSnapshot | null,
		errors,
		markPartial,
	);

	const routerRows = sys ? routerStaticRows(sys, level) : [{ label: 'Статус', value: 'Не загружено' }];
	const browserRows = browserSnapshotRows(browser);
	const sanitizedRouterClient = sanitizeRouterClientContext(routerClient);
	const clientRows = sanitizeClientRows(routerClientRows(sanitizedRouterClient));
	const awgmRows = awgm ? awgmServicesRows(awgm) : [{ label: 'Статус', value: 'Не загружено' }];

	return {
		generatedAt: nowWithOffset(routerOffset),
		source: 'frontend',
		partial,
		privacy: {
			sanitized: true,
			rules: ['client-mac', 'client-hostname'],
		},
		sections: [
			{ title: 'Роутер', rows: routerRows },
			{ title: 'Браузер', rows: browserRows },
			{ title: 'Клиент в сети роутера', rows: clientRows },
			{ title: 'AWGM', rows: awgmRows },
		],
		raw: {
			browser,
			routerClient: sanitizedRouterClient,
			awgm,
		},
		errors,
	};
}
