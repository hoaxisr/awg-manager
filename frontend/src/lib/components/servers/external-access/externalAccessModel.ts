import type {
	XrayConfig,
	XrayStatus,
	XHTTPTuning,
	TgWebProxyConfig,
	TgWebProxyStatus,
	CDNDispatcherConfig,
} from '$lib/types';
import type { EgressOption, WizardKind, WizardPlanRequest } from '$lib/types/serverWizard';
import { m, type UiText } from '$lib/i18n';
import {
	formatTelegramCarrierLabel,
	formatTelegramEgressLabel,
	formatXrayEgressLabel,
	getVlessPresetLabel,
	isLegacySocksEgress,
} from './connectionLabels';

export const KNOWN_TLS_PRESETS = [
	'www.google.com',
	'www.microsoft.com',
	'www.apple.com',
	'dl.google.com',
] as const;

export interface ExternalAccessWizardDraft {
	isEditMode: boolean;
	vlessInitiallyEnabled: boolean;
	tgInitiallyEnabled: boolean;
	disableOtherService: boolean;
	existingClientCount: number;
	keepExistingConnectionScheme: boolean;
	confirmDirectBehindNAT: boolean;
	/** Текст для показа: в черновике ($state) лежит отложенным вызовом i18n. */
	connectionSchemeSummary: UiText;
	connectionSchemeWarning: UiText;
	keendnsMode: 'manual' | 'existing' | 'create';
	keendnsLabel: string;
	selectedKind: WizardKind;
	tgScenario: string;
	tgCarrierMode: string;
	connectionWay: 'direct' | 'external_https';
	originConfigured: boolean;
	originType: 'public_address' | 'cloud_address';
	originHost: string;
	originScheme: 'http' | 'https';
	originPort: number;
	deviceType: string;
	clientRemark: string;
	existingClientUuid: string;
	preserveExistingCredentials: boolean;
	selectedMode: string;
	preserveTransport: boolean;
	rawTransport: string;
	rawMode: string;
	rawUplinkMethod: string;
	cdnProfileId: string;
	publicDomain: string;
	useSeparateTgDomain: boolean;
	tgPublicDomain: string;
	path: string;
	publicPort: number;
	listenPort: number;
	tgListenPort: number;
	dispatcherPort: number;
	directHost: string;
	directPort: number;
	tlsPreset: string;
	customTlsDomain: string;
	upstreamDevice: string;
	tgUpstreamDevice: string;
	legacyXrayEgress: boolean;
	legacyTgEgress: boolean;
}

export interface DomainRouteView {
	id: string;
	domain: string;
	description: string;
	routeXray: boolean;
	routeTelegram: boolean;
	enabled: boolean;
	xrayPath?: string;
	tgCarrier?: string;
	source: 'dispatcher' | 'service_only' | 'merged';
	warning?: string;
}

export interface ExternalAccessWebEndpointSummary {
	id: string;
	hostname: string;
	carrierMode: string;
	isCdn: boolean;
	enabled: boolean;
}

export interface ExternalAccessSummaryView {
	configFingerprint: string;
	vless: {
		installed: boolean;
		backend?: string;
		version?: string;
		unavailableReason?: string;
		configured: boolean;
		enabled: boolean;
		running: boolean;
		domains: string[];
		publicPort: number;
		listenPort: number;
		dispatcherPort: number;
		path: string;
		transportLabel: string;
		presetLabel: string;
		egressLabel: string;
		legacyEgress: boolean;
		activeClients: number;
		totalClients: number;
		activeClientCount: number;
		clientCount: number;
		recoveryRequired: boolean;
	};
	telegram: {
		configured: boolean;
		enabled: boolean;
		running: boolean;
		overallState: 'running' | 'partial' | 'stopped';
		scenario: string;
		egressLabel: string;
		legacyEgress: boolean;
		mtprotoOnline: boolean;
		webOnline: boolean;
		directHost: string;
		directPort: number;
		webListenPort: number;
		webEndpoints: ExternalAccessWebEndpointSummary[];
		mtproto: {
			enabled: boolean;
			online: boolean;
			address: string;
			port: number;
			tlsDomain: string;
		};
		web: {
			enabled: boolean;
			online: boolean;
			listenPort: number;
			domains: string[];
			endpointsCount: number;
			carrierLabel: string;
		};
	};
	dispatcher: {
		listenAddr: string;
		port: number;
		xrayPathPrefix: string;
		defaultAction: string;
		routes: DomainRouteView[];
		warnings: string[];
		activeRoutesCount: number;
		totalRoutesCount: number;
	};
}

export interface ExternalAccessInput {
	xrayConfig?: XrayConfig | null;
	xrayStatus?: XrayStatus | null;
	xrayTuning?: XHTTPTuning | null;
	tgConfig?: TgWebProxyConfig | null;
	tgStatus?: TgWebProxyStatus | null;
	dispatcherConfig?: CDNDispatcherConfig | null;
	egressOptions?: EgressOption[];
	realityEnabled?: boolean;
	realityPort?: number;
	realityPublicHost?: string;
}

export const RESERVED_MTPROTO_DIRECT_PORTS = new Set([
	1099, 2222, 2398, 3553, 8084, 8085, 8086, 9007, 9008, 9009, 51820,
]);

export function isValidMtprotoDirectPort(port: number | string): boolean {
	const n = Number(port);
	return Number.isInteger(n) && n >= 1024 && n <= 65535 && !RESERVED_MTPROTO_DIRECT_PORTS.has(n);
}

export function isTgDirectOnlyDraft(
	draft: Pick<ExternalAccessWizardDraft, 'selectedKind' | 'tgScenario'>
): boolean {
	return draft.selectedKind === 'tgwebproxy' && draft.tgScenario === 'direct_fake_tls';
}

export function draftNeedsWebIngress(
	draft: Pick<ExternalAccessWizardDraft, 'selectedKind' | 'tgScenario'>
): boolean {
	return !isTgDirectOnlyDraft(draft);
}

export function pickDefaultMtprotoDirectHost(opts: {
	directDomain?: string | null;
	whiteIP?: string | null;
}): string {
	const domain = (opts.directDomain || '').trim();
	if (domain) return domain;
	return (opts.whiteIP || '').trim();
}

export function isGrayIPNetwork(opts: {
	hasRealityNetwork?: boolean;
	realityWhiteIP?: boolean | null;
	wanIPPrivate?: boolean | null;
	keendnsEnabled?: boolean;
	keendnsMode?: string | null;
}): boolean {
	if (opts.hasRealityNetwork && opts.realityWhiteIP === true) return false;
	if (opts.hasRealityNetwork && opts.realityWhiteIP === false) return true;
	if (opts.wanIPPrivate === true) return true;
	if (opts.wanIPPrivate === false) return false;
	if (Boolean(opts.keendnsEnabled) && opts.keendnsMode === 'cloud') return true;
	return false;
}

export function doesStep2RequireNATConfirm(opts: {
	isEditMode: boolean;
	keepExistingConnectionScheme: boolean;
	isTgDirectOnly: boolean;
	isRealityOnly: boolean;
	wantXray: boolean;
	realityMode: 'off' | 'also' | 'only';
	originType: 'public_address' | 'cloud_address';
	grayIPDetected: boolean;
	realityWhiteIP: boolean;
}): boolean {
	if (!opts.grayIPDetected) return false;
	if (opts.isTgDirectOnly) return true;
	if (opts.isEditMode && opts.keepExistingConnectionScheme) return false;
	if (opts.isRealityOnly) return true;
	if (opts.originType === 'public_address') return true;
	if (opts.wantXray && opts.realityMode === 'also' && !opts.realityWhiteIP) return true;
	return false;
}

export function isCloudKeenDNSHost(
	host?: string | null,
	baseDomain?: string | null,
	baseIsDirect?: boolean
): boolean {
	const h = (host || '').trim().toLowerCase().replace(/\.$/, '');
	const base = (baseDomain || '').trim().toLowerCase().replace(/\.$/, '');
	if (!h || !base || baseIsDirect) return false;
	return h === base || h.endsWith('.' + base);
}

export function pickDefaultKeenDNSActionMode(opts: {
	keendnsAvailable: boolean;
	hasCompatiblePublications: boolean;
}): 'existing' | 'create' | 'manual' {
	if (opts.hasCompatiblePublications) return 'existing';
	if (opts.keendnsAvailable) return 'create';
	return 'manual';
}

export function resolveEffectiveMtprotoDirectHost(
	draft: Pick<
		ExternalAccessWizardDraft,
		| 'selectedKind'
		| 'tgScenario'
		| 'directHost'
		| 'originHost'
		| 'publicDomain'
		| 'keendnsMode'
		| 'originType'
	>
): string {
	if (isTgDirectOnlyDraft(draft)) {
		return draft.directHost.trim();
	}
	if (draft.selectedKind !== 'xray' && draft.tgScenario !== 'cdn_http') {
		if (draft.directHost.trim()) return draft.directHost.trim();
		if (draft.originType === 'cloud_address' && draft.keendnsMode !== 'manual') {
			return '';
		}
		return draft.originHost.trim() || (draft.publicDomain.split(',')[0] || '').trim();
	}
	return (draft.directHost || draft.originHost).trim();
}

export function formatDomainList(raw?: string | null): string {
	if (!raw) return '';
	return raw
		.split(',')
		.map((d) => d.trim())
		.filter(Boolean)
		.join(', ');
}

function splitDomains(raw?: string | null): string[] {
	if (!raw) return [];
	return raw
		.split(',')
		.map((d) => d.trim().toLowerCase())
		.filter(Boolean);
}

export function computeConfigFingerprint(input: ExternalAccessInput): string {
	const x = input.xrayConfig;
	const t = input.tgConfig;
	const d = input.dispatcherConfig;
	const tu = input.xrayTuning;

	const parts = [
		x
			? `x:${x.enabled}:${x.public_domain}:${x.public_port}:${x.listen_port}:${x.connection_way}:${x.origin_type}:${x.origin_host}:${x.origin_scheme}:${x.origin_port}:${x.path}:${x.transport}:${x.mode}:${x.uplink_method}:${x.outbound_mode}:${x.outbound_interface}:${x.outbound_socks_port}:${(x.clients || []).filter((c) => c.enabled).length}`
			: 'x:none',
		tu ? `tu:${tu.preset}:${tu.uplink_method}:${tu.stream_path_suffix}:${tu.enable_xmux}` : 'tu:none',
		t
			? `t:${t.enabled}:${t.scenario}:${t.direct_host}:${t.direct_port}:${t.tls_domain}:${t.listen_port}:${t.public_hostname}:${t.carrier_mode}:${t.upstream_device}:${(t.endpoints || []).map((e) => `${e.hostname}:${e.enabled}:${e.carrier_mode}`).join(';')}`
			: 't:none',
		d
			? `d:${d.listen_addr}:${d.xray_path_prefix}:${d.default_action}:${(d.routes || []).map((r) => `${r.domain}:${r.enabled}:${r.route_xray}:${r.route_telegram}`).join(';')}`
			: 'd:none',
	];
	return parts.join('|');
}

export function buildExternalAccessSummary(input: ExternalAccessInput): ExternalAccessSummaryView {
	const xCfg = input.xrayConfig ?? null;
	const xSt = input.xrayStatus ?? null;
	const xTun = input.xrayTuning ?? null;
	const tCfg = input.tgConfig ?? null;
	const tSt = input.tgStatus ?? null;
	const dCfg = input.dispatcherConfig ?? null;
	const egressOpts = input.egressOptions ?? [];

	const tunnelInterfaces = egressOpts.filter(
		(o) => o.kind === 'interface' && o.iface && o.iface !== 'direct'
	);

	// 1. VLESS (XHTTP)
	const vlessDomains = splitDomains(xCfg?.public_domain);
	const basePath = xCfg?.path || '/cdn-bridge/';
	const suffix = xTun?.stream_path_suffix?.trim();
	const effectivePath =
		suffix && !basePath.endsWith(suffix)
			? `${basePath.replace(/\/+$/, '')}/${suffix}`
			: basePath;

	const uplinkMethod = xTun?.uplink_method || xCfg?.uplink_method || 'POST';
	const transportLabel =
		xCfg?.mode === 'finiteproto' || xCfg?.transport === 'finiteproto'
			? 'FiniteProto v2'
			: `XHTTP (${uplinkMethod})`;

	const clients = xSt?.clients ?? xCfg?.clients ?? [];
	const activeClients = clients.filter((c) => c.enabled).length;
	const vlessMode =
		xCfg?.outbound_mode ||
		((xCfg?.outbound_socks_port ?? 0) > 0
			? 'socks'
			: xCfg?.outbound_interface
			? 'interface'
			: 'direct');

	// 2. Telegram (MTProto + Web)
	const tgScenario = tCfg?.scenario || 'dual';
	const mtprotoEnabled =
		Boolean(tCfg?.enabled ?? tSt?.running) &&
		(tgScenario === 'direct_fake_tls' || tgScenario === 'dual');
	const webEnabled =
		Boolean(tCfg?.enabled ?? tSt?.running) &&
		(tgScenario === 'cdn_http' || tgScenario === 'dual');

	const mtprotoOnline = Boolean(tSt?.direct_online);
	const webOnline = Boolean(tSt?.webproxy_online);

	let overallState: 'running' | 'partial' | 'stopped' = 'stopped';
	if (tCfg && tCfg.enabled === false) {
		overallState = tSt?.running || mtprotoOnline || webOnline ? 'partial' : 'stopped';
	} else if (tSt?.running || mtprotoOnline || webOnline) {
		if (tgScenario === 'dual') {
			if (mtprotoOnline && webOnline) overallState = 'running';
			else if (mtprotoOnline || webOnline) overallState = 'partial';
			else overallState = 'stopped';
		} else if (tgScenario === 'direct_fake_tls') {
			overallState = mtprotoOnline ? 'running' : 'stopped';
		} else if (tgScenario === 'cdn_http') {
			overallState = webOnline ? 'running' : 'stopped';
		} else {
			overallState = tSt?.running ? 'running' : 'stopped';
		}
	}

	const tgWebActive = tgScenario !== 'direct_fake_tls';
	const tgEndpoints = tgWebActive
		? (tCfg?.endpoints ?? []).filter((e) => e.hostname && e.hostname.trim() !== '')
		: [];
	const tgWebDomains: string[] = [];
	const tgCarrierByDomain = new Map<string, string>();
	const webEndpoints: ExternalAccessWebEndpointSummary[] = [];

	if (tgWebActive) {
		for (const ep of tgEndpoints) {
			const host = ep.hostname.trim().toLowerCase();
			if (!tgWebDomains.includes(host)) {
				tgWebDomains.push(host);
			}
			if (ep.carrier_mode) {
				tgCarrierByDomain.set(host, ep.carrier_mode);
			}
			webEndpoints.push({
				id: ep.id || host,
				hostname: ep.hostname.trim(),
				carrierMode: formatTelegramCarrierLabel(ep.carrier_mode || tCfg?.carrier_mode),
				isCdn: Boolean(ep.is_cdn),
				enabled: ep.enabled !== false,
			});
		}
		for (const d of splitDomains(tCfg?.public_hostname || tSt?.public_host)) {
			if (!tgWebDomains.includes(d)) {
				tgWebDomains.push(d);
			}
			if (webEndpoints.length === 0) {
				webEndpoints.push({
					id: d,
					hostname: d,
					carrierMode: formatTelegramCarrierLabel(tCfg?.carrier_mode || tSt?.carrier_mode),
					isCdn: tgScenario === 'cdn_http' || tgScenario === 'dual',
					enabled: true,
				});
			}
		}
	}

	const primaryCarrier =
		tgEndpoints[0]?.carrier_mode || tCfg?.carrier_mode || tSt?.carrier_mode || 'https-lanes';

	// 3. Lossless Domain & Dispatcher Route Aggregation
	const routeMap = new Map<string, DomainRouteView>();
	const explicitDispatcherRoutes = dCfg?.routes ?? [];
	const hasExplicitTable = explicitDispatcherRoutes.length > 0;
	const warnings: string[] = [];

	for (const r of explicitDispatcherRoutes) {
		const dom = (r.domain || '').trim().toLowerCase();
		if (!dom) continue;
		routeMap.set(dom, {
			id: r.id || dom,
			domain: dom,
			description: r.description || '',
			routeXray: Boolean(r.route_xray),
			routeTelegram: Boolean(r.route_telegram),
			enabled: Boolean(r.enabled),
			xrayPath: r.route_xray ? effectivePath : undefined,
			tgCarrier: r.route_telegram
				? formatTelegramCarrierLabel(tgCarrierByDomain.get(dom) || primaryCarrier)
				: undefined,
			source: 'dispatcher',
		});
	}

	for (const dom of vlessDomains) {
		const existing = routeMap.get(dom);
		if (existing) {
			existing.source = 'merged';
			existing.xrayPath = effectivePath;
			if (!existing.routeXray || !existing.enabled) {
				const w = m.servers_ext_model_vless_route_off({ domain: dom });
				existing.warning = w;
				warnings.push(w);
			}
		} else {
			const w = hasExplicitTable
				? m.servers_ext_model_vless_route_missing({ domain: dom })
				: undefined;
			if (w) warnings.push(w);
			routeMap.set(dom, {
				id: `vless-${dom}`,
				domain: dom,
				description: m.servers_ext_model_vless_desc(),
				routeXray: true,
				routeTelegram: tgWebDomains.includes(dom),
				enabled: !hasExplicitTable,
				xrayPath: effectivePath,
				tgCarrier: tgWebDomains.includes(dom)
					? formatTelegramCarrierLabel(tgCarrierByDomain.get(dom) || primaryCarrier)
					: undefined,
				source: 'service_only',
				warning: w,
			});
		}
	}

	for (const dom of tgWebDomains) {
		const existing = routeMap.get(dom);
		if (existing) {
			existing.source = 'merged';
			existing.tgCarrier = formatTelegramCarrierLabel(tgCarrierByDomain.get(dom) || primaryCarrier);
			if (!existing.routeTelegram || !existing.enabled) {
				const w = m.servers_ext_model_tg_route_off({ domain: dom });
				existing.warning = w;
				warnings.push(w);
			}
		} else {
			const w = hasExplicitTable
				? m.servers_ext_model_tg_route_missing({ domain: dom })
				: undefined;
			if (w) warnings.push(w);
			routeMap.set(dom, {
				id: `tg-${dom}`,
				domain: dom,
				description: m.servers_ext_model_tg_desc(),
				routeXray: false,
				routeTelegram: true,
				enabled: !hasExplicitTable,
				tgCarrier: formatTelegramCarrierLabel(tgCarrierByDomain.get(dom) || primaryCarrier),
				source: 'service_only',
				warning: w,
			});
		}
	}

	const routes = Array.from(routeMap.values());
	const activeRoutesCount = routes.filter((r) => r.enabled).length;

	const directHost = (tCfg?.direct_host || tSt?.direct_host || '').trim();
	const directPort = tCfg?.direct_port || tSt?.direct_port || 8443;
	const webListenPort = tCfg?.listen_port || tSt?.port || 8085;

	return {
		configFingerprint: computeConfigFingerprint(input),
		vless: {
			installed: xSt ? Boolean(xSt.installed) : true,
			backend: xSt?.backend,
			version: xSt?.version,
			unavailableReason: xSt?.unavailable_reason,
			configured: Boolean(xCfg?.enabled || vlessDomains.length > 0 || clients.length > 0),
			enabled: Boolean(xCfg?.enabled ?? xSt?.running),
			running: Boolean(xSt?.running),
			domains: vlessDomains,
			publicPort: xCfg?.public_port || 443,
			listenPort: xCfg?.listen_port || xSt?.port || 9008,
			dispatcherPort: xCfg?.dispatcher_port || 9009,
			path: effectivePath,
			transportLabel,
			presetLabel: getVlessPresetLabel(xTun?.preset),
			egressLabel: formatXrayEgressLabel(xCfg, tunnelInterfaces),
			legacyEgress: isLegacySocksEgress(vlessMode, xCfg?.outbound_socks_port),
			activeClients,
			totalClients: clients.length,
			activeClientCount: activeClients,
			clientCount: clients.length,
			recoveryRequired: Boolean(xSt?.recovery_required),
		},
		telegram: {
			configured: Boolean(
				tCfg?.enabled || tSt?.running || directHost !== '' || tgWebDomains.length > 0
			),
			enabled: Boolean(tCfg?.enabled ?? tSt?.running),
			running: Boolean(tSt?.running),
			overallState,
			scenario: tgScenario,
			egressLabel: formatTelegramEgressLabel(tCfg?.upstream_device),
			legacyEgress: isLegacySocksEgress(tCfg?.upstream_device),
			mtprotoOnline,
			webOnline,
			directHost,
			directPort,
			webListenPort,
			webEndpoints,
			mtproto: {
				enabled: mtprotoEnabled,
				online: mtprotoOnline,
				address: directHost ? `${directHost}:${directPort}` : '',
				port: directPort,
				tlsDomain: tCfg?.tls_domain || tSt?.tls_domain || '',
			},
			web: {
				enabled: webEnabled,
				online: webOnline,
				listenPort: webListenPort,
				domains: tgWebDomains,
				endpointsCount: tgEndpoints.length || (tgWebDomains.length > 0 ? 1 : 0),
				carrierLabel: formatTelegramCarrierLabel(primaryCarrier),
			},
		},
		dispatcher: {
			listenAddr: dCfg?.listen_addr || ':9009',
			port: xCfg?.dispatcher_port || 9009,
			xrayPathPrefix: dCfg?.xray_path_prefix || '/cdn-bridge',
			defaultAction: dCfg?.default_action || '404',
			routes,
			warnings,
			activeRoutesCount,
			totalRoutesCount: routes.length,
		},
	};
}

export function computeEffectiveVerifyKind(summary: ExternalAccessSummaryView): WizardKind {
	const vlessActive = summary.vless.enabled || summary.vless.configured;
	const tgActive = summary.telegram.enabled || summary.telegram.configured;
	if (vlessActive && !tgActive) {
		return 'xray';
	}
	if (tgActive && !vlessActive) {
		return 'tgwebproxy';
	}
	return 'both';
}

export function createInitialWizardDraft(
	input: ExternalAccessInput,
	preferredKind?: WizardKind
): ExternalAccessWizardDraft {
	const xCfg = input.xrayConfig ?? null;
	const xSt = input.xrayStatus ?? null;
	const xTun = input.xrayTuning ?? null;
	const tCfg = input.tgConfig ?? null;
	const tSt = input.tgStatus ?? null;
	const dCfg = input.dispatcherConfig ?? null;

	const vlessDomains = splitDomains(xCfg?.public_domain);
	const xClients = xCfg?.clients?.length ? xCfg.clients : (xSt?.clients ?? []);
	const vlessConfigured = Boolean(
		xCfg?.enabled || vlessDomains.length > 0 || xClients.length > 0 || input.realityEnabled
	);

	const tgScenarioRaw = tCfg?.scenario || 'dual';
	const tgEndpoints =
		tgScenarioRaw === 'direct_fake_tls'
			? []
			: (tCfg?.endpoints ?? []).filter(
					(e) => e.hostname && e.hostname.trim() !== ''
				);
	const tgDomains: string[] = [];
	if (tgScenarioRaw !== 'direct_fake_tls') {
		for (const ep of tgEndpoints) {
			const host = ep.hostname.trim().toLowerCase();
			if (!tgDomains.includes(host)) tgDomains.push(host);
		}
		for (const d of splitDomains(tCfg?.public_hostname || tSt?.public_host)) {
			if (!tgDomains.includes(d)) tgDomains.push(d);
		}
	}
	const directHostRaw = (tCfg?.direct_host || tSt?.direct_host || '').trim();
	const tgConfigured = Boolean(
		tCfg?.enabled || tSt?.running || directHostRaw !== '' || tgDomains.length > 0
	);

	const isEditMode = vlessConfigured || tgConfigured;

	let selectedKind: WizardKind = 'both';
	if (isEditMode) {
		if (vlessConfigured && tgConfigured) selectedKind = 'both';
		else if (vlessConfigured) selectedKind = 'xray';
		else if (tgConfigured) selectedKind = 'tgwebproxy';
	} else if (preferredKind) {
		selectedKind = preferredKind;
	}

	if (!isEditMode) {
		return {
			isEditMode: false,
			vlessInitiallyEnabled: false,
			tgInitiallyEnabled: false,
			disableOtherService: false,
			existingClientCount: 0,
			keepExistingConnectionScheme: false,
			confirmDirectBehindNAT: false,
			connectionSchemeSummary: '',
			connectionSchemeWarning: '',
			keendnsMode: 'manual',
			keendnsLabel: 'connect',
			selectedKind,
			tgScenario: 'dual',
			tgCarrierMode: 'https-lanes',
			connectionWay: 'external_https',
			originConfigured: false,
			originType: 'cloud_address',
			originHost: '',
			originScheme: 'http',
			originPort: 9009,
			deviceType: 'phone',
			clientRemark: m.servers_ext_default_device(),
			existingClientUuid: '',
			preserveExistingCredentials: false,
			selectedMode: 'xhttp_get',
			preserveTransport: false,
			rawTransport: 'xhttp',
			rawMode: 'packet-up',
			rawUplinkMethod: 'POST',
			cdnProfileId: 'cdn_get',
			publicDomain: '',
			useSeparateTgDomain: false,
			tgPublicDomain: '',
			path: '/xhttp-bridge',
			publicPort: 443,
			listenPort: 9008,
			tgListenPort: 8085,
			dispatcherPort: 9009,
			directHost: '',
			directPort: 8443,
			tlsPreset: 'www.google.com',
			customTlsDomain: '',
			upstreamDevice: 'direct',
			tgUpstreamDevice: 'direct',
			legacyXrayEgress: false,
			legacyTgEgress: false,
		};
	}

	const tgScenario = tCfg?.scenario || 'dual';
	const tgCarrierMode =
		tgEndpoints[0]?.carrier_mode || tCfg?.carrier_mode || tSt?.carrier_mode || 'https-lanes';

	const vlessDomainStr = formatDomainList(xCfg?.public_domain);
	const vlessRealityOnly = Boolean(vlessConfigured && !vlessDomainStr && input.realityEnabled);
	const tgDomainStr =
		tgScenario === 'direct_fake_tls'
			? ''
			: tgDomains.length > 0
				? tgDomains.join(', ')
				: formatDomainList(tCfg?.public_hostname || tSt?.public_host);

	const publicDomain = vlessDomainStr || tgDomainStr;
	const useSeparateTgDomain = Boolean(
		vlessDomainStr &&
			tgDomainStr &&
			vlessDomainStr.toLowerCase() !== tgDomainStr.toLowerCase()
	);
	const tgPublicDomain = useSeparateTgDomain ? tgDomainStr : '';

	const path = xCfg?.path || dCfg?.xray_path_prefix || '/xhttp-bridge';
	const publicPort = xCfg?.public_port || 443;
	const listenPort = xCfg?.listen_port || xSt?.port || 9008;
	const tgListenPort = tCfg?.listen_port || tSt?.port || 8085;
	const dispatcherPort = xCfg?.dispatcher_port || 9009;
	const directHost = directHostRaw;
	const directPort = tCfg?.direct_port || tSt?.direct_port || 8443;

	const isDirectVless = Boolean(
		xCfg && xCfg.public_port && xCfg.public_port !== 443 && xCfg.public_port === xCfg.listen_port
	);
	const connectionWay: 'direct' | 'external_https' =
		xCfg?.connection_way === 'direct' || xCfg?.connection_way === 'external_https'
			? xCfg.connection_way
			: isDirectVless || (!vlessConfigured && tgScenario === 'direct_fake_tls')
				? 'direct'
				: 'external_https';

	const savedOriginHost = formatDomainList(xCfg?.origin_host);
	const originConfigured = Boolean(savedOriginHost);
	const originHost = savedOriginHost;
	const originScheme: 'http' | 'https' = xCfg?.origin_scheme === 'https' ? 'https' : 'http';
	const originPort =
		xCfg?.origin_port && xCfg.origin_port > 0 ? xCfg.origin_port : dispatcherPort;
	const originType: 'public_address' | 'cloud_address' =
		xCfg?.origin_type === 'public_address' || xCfg?.origin_type === 'cloud_address'
			? xCfg.origin_type
			: (!vlessConfigured && tgScenario === 'direct_fake_tls') ||
				  (originHost && /^\d+\.\d+\.\d+\.\d+$/.test(originHost))
				? 'public_address'
				: 'cloud_address';

	const rawTls = (tCfg?.tls_domain || tSt?.tls_domain || '').trim() || 'www.google.com';
	const isKnownTls = (KNOWN_TLS_PRESETS as readonly string[]).includes(rawTls);
	const tlsPreset = isKnownTls ? rawTls : '__custom__';
	const customTlsDomain = isKnownTls ? '' : rawTls;

	const firstClient = xClients.find((c) => c.enabled) || xClients[0];
	const clientRemark = firstClient?.remark || m.servers_ext_default_device();
	const existingClientUuid = firstClient?.id || '';

	const rawTransport = xCfg?.transport || 'xhttp';
	const rawMode = xCfg?.mode || 'packet-up';
	const rawUplinkMethod = xCfg?.uplink_method || xTun?.uplink_method || 'POST';
	const isSupportedTransport = rawTransport === 'xhttp' || rawTransport === 'finiteproto';
	const preserveTransport = Boolean(vlessConfigured && xCfg?.transport && isSupportedTransport);

	const selectedMode = 'xhttp_get';

	const vlessOutMode =
		xCfg?.outbound_mode ||
		((xCfg?.outbound_socks_port ?? 0) > 0
			? 'socks'
			: xCfg?.outbound_interface
				? 'interface'
				: 'direct');

	let upstreamDevice = 'direct';
	let legacyXrayEgress = false;
	if (vlessOutMode === 'router' || vlessOutMode === 'firc:router') {
		upstreamDevice = 'firc:router';
	} else if (vlessOutMode === 'interface' && xCfg?.outbound_interface) {
		upstreamDevice = xCfg.outbound_interface;
	} else if (isLegacySocksEgress(vlessOutMode, xCfg?.outbound_socks_port)) {
		upstreamDevice = `mihomo:${xCfg?.outbound_socks_port || 1099}`;
		legacyXrayEgress = true;
	}

	const rawTgOut = (tCfg?.upstream_device || '').trim();
	let tgUpstreamDevice = 'direct';
	let legacyTgEgress = false;
	if (!rawTgOut || rawTgOut === 'direct') {
		tgUpstreamDevice = 'direct';
	} else if (isLegacySocksEgress(rawTgOut)) {
		tgUpstreamDevice = rawTgOut;
		legacyTgEgress = true;
	} else {
		tgUpstreamDevice = rawTgOut;
	}

	if (selectedKind === 'tgwebproxy') {
		upstreamDevice = tgUpstreamDevice;
	}

	// Части схемы — отложенными вызовами: черновик живёт в $state мастера,
	// и готовая строка застыла бы на языке момента открытия.
	const schemeParts: (() => string)[] = [];
	if (selectedKind === 'tgwebproxy' && tgScenario === 'direct_fake_tls') {
		schemeParts.push(() => m.servers_ext_wiz_s2_tg_direct_title());
		if (directHost) {
			const direct = `${directHost}:${directPort}`;
			schemeParts.push(() => m.servers_ext_model_scheme_mtproto_addr({ addr: direct }));
		}
	} else if (selectedKind === 'xray' && vlessRealityOnly) {
		schemeParts.push(() => m.servers_ext_wiz_s2_reality_title());
		const rHost = (input.realityPublicHost || '').trim();
		if (rHost) {
			const rAddr = `${rHost}:${input.realityPort || 2053}`;
			schemeParts.push(() => m.servers_ext_model_scheme_phone_addr({ addr: rAddr }));
		}
	} else {
		if (connectionWay === 'direct') {
			schemeParts.push(() => m.servers_ext_model_scheme_router_domain());
		} else {
			schemeParts.push(() => m.servers_ext_model_scheme_own_domain());
		}
		if (publicDomain) {
			schemeParts.push(() => m.servers_ext_model_scheme_phone_addr({ addr: publicDomain }));
		}
		if (originHost) {
			const origin = `${originScheme}://${originHost}:${originPort}`;
			schemeParts.push(() => m.servers_ext_model_scheme_router_addr({ addr: origin }));
		} else if (directHost) {
			const direct = `${directHost}:${directPort}`;
			schemeParts.push(() => m.servers_ext_model_scheme_mtproto_addr({ addr: direct }));
		}
	}

	const schemeWarnings: (() => string)[] = [];
	if (vlessConfigured && !vlessDomainStr && !vlessRealityOnly) {
		schemeWarnings.push(() => m.servers_ext_model_warn_no_vless_domain());
	}
	if (connectionWay === 'external_https' && !originHost && vlessConfigured && !vlessRealityOnly) {
		schemeWarnings.push(() => m.servers_ext_model_warn_no_origin());
	}
	if (rawTransport && !isSupportedTransport) {
		schemeWarnings.push(() => m.servers_ext_model_warn_legacy_transport({ transport: rawTransport }));
	}

	return {
		isEditMode: true,
		vlessInitiallyEnabled: vlessConfigured,
		tgInitiallyEnabled: tgConfigured,
		disableOtherService: false,
		existingClientCount: xClients.length,
		keepExistingConnectionScheme: true,
		confirmDirectBehindNAT: false,
		connectionSchemeSummary: () => schemeParts.map((t) => t()).join(' · '),
		connectionSchemeWarning: schemeWarnings.length ? () => schemeWarnings.map((t) => t()).join('. ') : '',
		keendnsMode: 'manual',
		keendnsLabel: 'connect',
		selectedKind,
		tgScenario,
		tgCarrierMode,
		connectionWay,
		originConfigured,
		originType,
		originHost,
		originScheme,
		originPort,
		deviceType: 'phone',
		clientRemark,
		existingClientUuid,
		preserveExistingCredentials: true,
		selectedMode,
		preserveTransport,
		rawTransport,
		rawMode,
		rawUplinkMethod,
		cdnProfileId: 'cdn_get',
		publicDomain,
		useSeparateTgDomain,
		tgPublicDomain,
		path,
		publicPort,
		listenPort,
		tgListenPort,
		dispatcherPort,
		directHost,
		directPort,
		tlsPreset,
		customTlsDomain,
		upstreamDevice,
		tgUpstreamDevice,
		legacyXrayEgress,
		legacyTgEgress,
	};
}

export function isDraftEgressValid(
	draft: ExternalAccessWizardDraft,
	egressOptions: EgressOption[] = []
): {
	valid: boolean;
	reason?: string;
} {
	const checkOptionAvailability = (id: string, serviceLabel: string): string | undefined => {
		if (!id || id === 'direct' || egressOptions.length === 0) {
			return undefined;
		}
		const found = egressOptions.find((o) => o.id === id || o.iface === id);
		if (found && !found.available) {
			// У правил роутера туннеля нет: причину называет сервер.
			if (id === 'firc:router' && found.unavailable_reason) return found.unavailable_reason;
			return m.servers_ext_model_egress_unavailable({ service: serviceLabel, name: found.name });
		}
		return undefined;
	};

	if (draft.selectedKind === 'xray') {
		if (!draft.upstreamDevice || isLegacySocksEgress(draft.upstreamDevice)) {
			return {
				valid: false,
				reason: m.servers_ext_model_egress_legacy_xray(),
			};
		}
		const unavail = checkOptionAvailability(draft.upstreamDevice, 'VLESS');
		if (unavail) {
			return { valid: false, reason: unavail };
		}
		return { valid: true };
	}

	if (draft.selectedKind === 'tgwebproxy') {
		const dev = draft.tgUpstreamDevice || draft.upstreamDevice;
		if (!dev || isLegacySocksEgress(dev)) {
			return {
				valid: false,
				reason: m.servers_ext_model_egress_legacy_tg(),
			};
		}
		if (dev === 'firc:router') {
			return {
				valid: false,
				reason: m.servers_ext_model_egress_tg_iface_only(),
			};
		}
		const unavail = checkOptionAvailability(dev, 'Telegram');
		if (unavail) {
			return { valid: false, reason: unavail };
		}
		return { valid: true };
	}

	// selectedKind === 'both'
	if (!draft.upstreamDevice || isLegacySocksEgress(draft.upstreamDevice)) {
		return {
			valid: false,
			reason: m.servers_ext_model_egress_legacy_both_xray(),
		};
	}
	const xrayUnavail = checkOptionAvailability(draft.upstreamDevice, 'VLESS');
	if (xrayUnavail) {
		return { valid: false, reason: xrayUnavail };
	}
	if (!draft.tgUpstreamDevice || isLegacySocksEgress(draft.tgUpstreamDevice)) {
		return {
			valid: false,
			reason: m.servers_ext_model_egress_tg_required(),
		};
	}
	if (draft.tgUpstreamDevice === 'firc:router') {
		return {
			valid: false,
			reason: m.servers_ext_model_egress_tg_no_firc(),
		};
	}
	const tgUnavail = checkOptionAvailability(draft.tgUpstreamDevice, 'Telegram');
	if (tgUnavail) {
		return { valid: false, reason: tgUnavail };
	}
	return { valid: true };
}

const RESERVED_KEENDNS_LABELS = new Set([
	'admin',
	'auth',
	'cli',
	'control',
	'keendns',
	'login',
	'my',
	'ndms',
	'proxy',
	'rci',
	'router',
	'setup',
	'system',
	'web',
	'webcli',
	'www',
]);

export function validateKeenDNSLabelClient(rawLabel: string): {
	valid: boolean;
	normalized: string;
	error?: string;
} {
	const clean = (rawLabel || '').trim().toLowerCase();
	if (!clean) {
		return { valid: false, normalized: '', error: m.servers_ext_model_kd_empty() };
	}
	if (clean.includes('.')) {
		return {
			valid: false,
			normalized: clean,
			error: m.servers_ext_model_kd_no_dots(),
		};
	}
	if (clean.length > 63) {
		return { valid: false, normalized: clean, error: m.servers_ext_model_kd_too_long() };
	}
	if (!/^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$/.test(clean)) {
		return {
			valid: false,
			normalized: clean,
			error: m.servers_ext_model_kd_charset(),
		};
	}
	if (RESERVED_KEENDNS_LABELS.has(clean)) {
		return {
			valid: false,
			normalized: clean,
			error: m.servers_ext_model_kd_reserved({ name: clean }),
		};
	}
	return { valid: true, normalized: clean };
}

export function shouldKeepExistingSchemeAfterStep1Change(
	initiallyTgDirectOnly: boolean,
	draft: Pick<
		ExternalAccessWizardDraft,
		'selectedKind' | 'tgScenario' | 'keepExistingConnectionScheme'
	>
): boolean {
	if (!draft.keepExistingConnectionScheme) {
		return false;
	}
	if (initiallyTgDirectOnly && !isTgDirectOnlyDraft(draft)) {
		return false;
	}
	return true;
}

export function buildPlanRequestFromDraft(
	draft: ExternalAccessWizardDraft
): WizardPlanRequest {
	const tgDirectOnly = isTgDirectOnlyDraft(draft);
	const effectiveTlsDomain =
		draft.tlsPreset === '__custom__' ? draft.customTlsDomain.trim() : draft.tlsPreset;
	const effectiveOriginHost = tgDirectOnly ? '' : draft.originHost.trim();
	const effectiveDirectHost = resolveEffectiveMtprotoDirectHost(draft);
	const effectiveMode =
		draft.selectedMode === 'xhttp_get' || draft.preserveTransport
			? draft.selectedMode
			: 'xhttp_get';
	const effectiveCdnProfileId =
		tgDirectOnly || draft.connectionWay === 'direct'
			? 'direct'
			: draft.cdnProfileId || 'cdn_get';
	const effectiveCarrierMode =
		draft.selectedKind === 'xray'
			? undefined
			: effectiveCdnProfileId === 'direct' || effectiveCdnProfileId === 'cdn_ws'
				? 'websocket'
				: draft.tgCarrierMode;

	return {
		kind: draft.selectedKind,
		scenario: draft.selectedKind === 'xray' ? undefined : draft.tgScenario,
		carrier_mode: effectiveCarrierMode,
		device_type: draft.deviceType,
		mode: effectiveMode,
		connection_way: tgDirectOnly ? 'direct' : draft.connectionWay,
		origin_type: tgDirectOnly ? 'public_address' : draft.originType,
		origin_host: effectiveOriginHost || undefined,
		origin_scheme: tgDirectOnly ? undefined : draft.originScheme,
		origin_port: tgDirectOnly ? undefined : draft.originPort,
		public_domain: tgDirectOnly ? '' : draft.publicDomain.trim(),
		tg_public_domain:
			!tgDirectOnly && draft.selectedKind === 'both' && draft.useSeparateTgDomain
				? draft.tgPublicDomain.trim()
				: undefined,
		public_port: draft.publicPort,
		path: draft.path.trim(),
		dispatcher_port: draft.dispatcherPort,
		listen_port:
			draft.selectedKind === 'tgwebproxy' ? draft.tgListenPort : draft.listenPort,
		tg_listen_port: draft.tgListenPort,
		direct_host: effectiveDirectHost,
		direct_port: draft.directPort,
		tls_domain: effectiveTlsDomain,
		upstream_device:
			draft.selectedKind === 'tgwebproxy'
				? draft.tgUpstreamDevice || draft.upstreamDevice
				: draft.upstreamDevice,
		tg_upstream_device:
			draft.selectedKind === 'both' ? draft.tgUpstreamDevice : undefined,
		client_remark: draft.clientRemark.trim() || m.servers_ext_default_device(),
		cdn_profile_id: effectiveCdnProfileId,
		preserve_transport: draft.preserveTransport || undefined,
		raw_transport: draft.preserveTransport ? draft.rawTransport : undefined,
		raw_mode: draft.preserveTransport ? draft.rawMode : undefined,
		raw_uplink_method: draft.preserveTransport ? draft.rawUplinkMethod : undefined,
		preserve_existing_credentials: draft.preserveExistingCredentials || undefined,
		existing_client_uuid:
			draft.preserveExistingCredentials && draft.existingClientUuid
				? draft.existingClientUuid
				: undefined,
		keendns_mode:
			!tgDirectOnly && draft.keendnsMode && draft.keendnsMode !== 'manual'
				? draft.keendnsMode
				: undefined,
		keendns_label:
			!tgDirectOnly && draft.keendnsMode && draft.keendnsMode !== 'manual'
				? draft.keendnsLabel.trim().toLowerCase()
				: undefined,
		disable_other_service: draft.disableOtherService || undefined,
	};
}
