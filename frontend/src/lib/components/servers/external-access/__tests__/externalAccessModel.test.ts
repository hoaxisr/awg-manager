import { describe, it, expect } from 'vitest';
import {
	EXTERNAL_ACCESS_LABELS,
	VLESS_PRESET_LABELS,
	TELEGRAM_WEB_PRESETS,
	formatTelegramCarrierLabel,
	formatXrayEgressLabel,
	formatTelegramEgressLabel,
	formatShortEgressSummary,
	isLegacySocksEgress,
} from '../connectionLabels';
import {
	buildExternalAccessSummary,
	computeConfigFingerprint,
	computeEffectiveVerifyKind,
	createInitialWizardDraft,
	draftNeedsWebIngress,
	isCloudKeenDNSHost,
	isDraftEgressValid,
	isGrayIPNetwork,
	isTgDirectOnlyDraft,
	isValidMtprotoDirectPort,
	pickDefaultKeenDNSActionMode,
	pickDefaultMtprotoDirectHost,
	resolveEffectiveMtprotoDirectHost,
	shouldKeepExistingSchemeAfterStep1Change,
	buildPlanRequestFromDraft,
	validateKeenDNSLabelClient,
} from '../externalAccessModel';
import type {
	XrayConfig,
	XrayStatus,
	TgWebProxyConfig,
	TgWebProxyStatus,
	CDNDispatcherConfig,
} from '$lib/types';
import { uiText } from '$lib/i18n';

describe('connectionLabels', () => {
	it('formats Telegram carrier mode accurately without misleading WebSocket labels', () => {
		expect(formatTelegramCarrierLabel('https-lanes')).toBe('HTTPS-потоки (https-lanes)');
		expect(formatTelegramCarrierLabel('websocket')).toBe('WebSocket');
		expect(formatTelegramCarrierLabel('websocket-lanes')).toBe('Параллельные WebSocket (websocket-lanes)');
		expect(formatTelegramCarrierLabel('https')).toBe('HTTPS POST');
	});

	it('detects legacy SOCKS :1099 egress and formats FIRC / tunnel / direct labels', () => {
		expect(isLegacySocksEgress('socks', 1099)).toBe(true);
		expect(isLegacySocksEgress('', 1099)).toBe(true);
		expect(isLegacySocksEgress('router', 0)).toBe(false);
		expect(isLegacySocksEgress('direct', 0)).toBe(false);

		expect(formatXrayEgressLabel({ outboundMode: 'router' })).toBe(
			EXTERNAL_ACCESS_LABELS.egressFirc,
		);
		expect(formatXrayEgressLabel({ outboundMode: 'socks', outboundSocksPort: 1099 })).toContain(
			EXTERNAL_ACCESS_LABELS.egressLegacyWarning,
		);
		expect(
			formatXrayEgressLabel({
				outboundMode: 'interface',
				outboundInterface: 'nwg1',
				tunnelName: 'AWG Main (nwg1)',
			}),
		).toBe('Туннель: AWG Main (nwg1)');
		expect(formatTelegramEgressLabel('direct')).toBe(EXTERNAL_ACCESS_LABELS.egressDirect);
		expect(formatTelegramEgressLabel('nwg1')).toBe('Интерфейс: nwg1');
	});

	it('does not expose Mihomo, Xray-core, or marketing promises in canonical labels', () => {
		const serialized = JSON.stringify({
			EXTERNAL_ACCESS_LABELS,
			VLESS_PRESET_LABELS,
			TELEGRAM_WEB_PRESETS,
		});
		expect(serialized).not.toMatch(/Mihomo/i);
		expect(serialized).not.toMatch(/Xray-core/i);
		expect(serialized).not.toMatch(/Sing-box/i);
		expect(serialized).not.toMatch(/Рекомендуется для РФ/i);
	});
});

describe('externalAccessModel', () => {
	it('preserves multi-domain VLESS, multi-endpoint Telegram, and dispatcher routes losslessly', () => {
		const xrayConfig: XrayConfig = {
			enabled: true,
			listen_address: '127.0.0.1',
			listen_port: 9008,
			public_domain: 'edge1.example.test, edge2.example.test',
			public_port: 443,
			path: '/cdn-bridge/',
			transport: 'xhttp',
			mode: 'packet-up',
			uplink_method: 'POST',
			dispatcher_port: 9009,
			outbound_mode: 'router',
			outbound_interface: '',
			outbound_socks_port: 0,
			xmux_max_connections: 0,
			clients: [
				{ id: '00000000-0000-4000-8000-000000000001', remark: 'Phone', enabled: true, created_at: '2026-01-01T00:00:00Z' },
				{ id: '00000000-0000-4000-8000-000000000002', remark: 'Laptop', enabled: false, created_at: '2026-01-01T00:00:00Z' },
			],
		};
		const xrayStatus: XrayStatus = {
			installed: true,
			running: true,
			port: 9008,
			clients: xrayConfig.clients,
		};

		const tgConfig: TgWebProxyConfig = {
			enabled: true,
			scenario: 'dual',
			listen_port: 8085,
			admin_port: 8086,
			public_hostname: 'tg1.example.test',
			direct_host: 'direct.example.test',
			direct_port: 8443,
			tls_domain: 'gateway.icloud.com',
			carrier_mode: 'https-lanes',
			upstream_device: 'nwg1',
			secret: '00112233445566778899aabbccddeeff',
			backend: '127.0.0.1:2398',
			endpoints: [
				{
					id: 'ep-1',
					hostname: 'tg1.example.test',
					listen_port: 8084,
					admin_port: 8086,
					description: 'CDN Edge',
					preset: 'cdn_keendns',
					carrier_mode: 'https-lanes',
					is_cdn: true,
					enabled: true,
				},
				{
					id: 'ep-2',
					hostname: 'tg2.example.test',
					listen_port: 18084,
					admin_port: 18086,
					description: 'Cloud Direct',
					preset: 'keendns',
					carrier_mode: 'websocket',
					is_cdn: false,
					enabled: true,
				},
			],
		};

		// Simulate MTProto online, but Web proxy offline -> must report 'partial'
		const tgStatus = {
			installed: true,
			running: true,
			port: 8085,
			direct_online: true,
			webproxy_online: false,
			raw_online: true,
			upstream_status: 'ok',
		} as unknown as TgWebProxyStatus;

		const dispatcherConfig: CDNDispatcherConfig = {
			enabled: true,
			listen_addr: ':9009',
			xray_target: 'http://127.0.0.1:9008',
			tg_target: 'http://127.0.0.1:8085',
			xray_path_prefix: '/cdn-bridge',
			default_action: '404',
			routes: [
				{
					id: 'r1',
					domain: 'edge1.example.test',
					description: 'Primary VLESS',
					route_xray: true,
					route_telegram: false,
					enabled: true,
				},
			],
		};

		const summary = buildExternalAccessSummary({
			xrayConfig,
			xrayStatus,
			xrayTuning: null,
			tgConfig,
			tgStatus,
			dispatcherConfig,
		});

		expect(summary.vless.domains).toEqual(['edge1.example.test', 'edge2.example.test']);
		expect(summary.vless.egressLabel).toBe(EXTERNAL_ACCESS_LABELS.egressFirc);
		expect(summary.vless.legacyEgress).toBe(false);
		expect(summary.vless.activeClientCount).toBe(1);
		expect(summary.vless.clientCount).toBe(2);

		expect(summary.telegram.overallState).toBe('partial');
		expect(summary.telegram.mtprotoOnline).toBe(true);
		expect(summary.telegram.webOnline).toBe(false);
		expect(summary.telegram.webEndpoints).toHaveLength(2);
		expect(summary.telegram.webEndpoints[0].carrierMode).toBe('HTTPS-потоки (https-lanes)');
		expect(summary.telegram.webEndpoints[1].carrierMode).toBe('WebSocket');

		// edge2.example.test is missing from dispatcher.routes -> should produce a warning
		expect(summary.dispatcher.warnings.some((w) => w.includes('edge2.example.test'))).toBe(true);
	});

	it('produces different configFingerprint when domain, port, or egress changes', () => {
		const baseFp = computeConfigFingerprint({
			xrayConfig: {
				enabled: true,
				listen_address: '127.0.0.1',
				listen_port: 9008,
				public_domain: 'a.example.test',
				public_port: 443,
				path: '/cdn-bridge/',
				transport: 'xhttp',
				mode: 'packet-up',
				uplink_method: 'GET',
				dispatcher_port: 9009,
				outbound_mode: 'direct',
				outbound_interface: '',
				outbound_socks_port: 0,
				xmux_max_connections: 0,
				clients: [],
			},
			xrayStatus: null,
			xrayTuning: null,
			tgConfig: null,
			tgStatus: null,
			dispatcherConfig: null,
		});

		const changedFp = computeConfigFingerprint({
			xrayConfig: {
				enabled: true,
				listen_address: '127.0.0.1',
				listen_port: 9008,
				public_domain: 'a.example.test',
				public_port: 443,
				path: '/cdn-bridge/',
				transport: 'xhttp',
				mode: 'packet-up',
				uplink_method: 'GET',
				dispatcher_port: 9009,
				outbound_mode: 'router',
				outbound_interface: '',
				outbound_socks_port: 0,
				xmux_max_connections: 0,
				clients: [],
			},
			xrayStatus: null,
			xrayTuning: null,
			tgConfig: null,
			tgStatus: null,
			dispatcherConfig: null,
		});

		expect(baseFp).not.toBe(changedFp);
	});

	it('creates neutral defaults in create mode and lossless draft in edit mode (open/close zero writes, change-one-field preservation)', () => {
		// 1. Create mode defaults
		const freshDraft = createInitialWizardDraft({});
		expect(freshDraft.isEditMode).toBe(false);
		expect(freshDraft.clientRemark).toBe('Основное устройство');
		expect(freshDraft.connectionWay).toBe('external_https');
		expect(freshDraft.originType).toBe('cloud_address');
		expect(freshDraft.originPort).toBe(9009);
		expect(freshDraft.path).toBe('/xhttp-bridge');
		expect(freshDraft.preserveExistingCredentials).toBe(false);

		// 2. Edit mode with custom path, custom stream-up transport, custom TLS domain, and existing client UUID
		const existingXray: XrayConfig = {
			enabled: true,
			listen_address: '127.0.0.1',
			listen_port: 19008,
			public_domain: 'vless1.example.test, vless2.example.test',
			public_port: 443,
			connection_way: 'external_https',
			origin_type: 'cloud_address',
			origin_host: 'origin.keenetic.example',
			origin_scheme: 'https',
			origin_port: 9443,
			path: '/custom-secret-path',
			transport: 'xhttp',
			mode: 'stream-up',
			uplink_method: 'POST',
			dispatcher_port: 9009,
			outbound_mode: 'direct',
			outbound_interface: '',
			outbound_socks_port: 0,
			xmux_max_connections: 0,
			clients: [
				{
					id: '11111111-2222-4333-8444-555555555555',
					remark: 'Мой ноутбук',
					enabled: true,
					created_at: '2026-01-01T00:00:00Z',
				},
			],
		};
		const existingTg: TgWebProxyConfig = {
			enabled: true,
			scenario: 'dual',
			listen_port: 18085,
			admin_port: 18086,
			public_hostname: 'tg.example.test',
			direct_host: 'router.keenetic.example',
			direct_port: 18443,
			tls_domain: 'custom-sni.example.org',
			carrier_mode: 'websocket',
			upstream_device: 'direct',
			secret: '00112233445566778899aabbccddeeff',
			backend: '127.0.0.1:2398',
		};

		const editDraft = createInitialWizardDraft({
			xrayConfig: existingXray,
			tgConfig: existingTg,
		});

		expect(editDraft.isEditMode).toBe(true);
		expect(editDraft.selectedKind).toBe('both');
		expect(editDraft.publicDomain).toBe('vless1.example.test, vless2.example.test');
		expect(editDraft.useSeparateTgDomain).toBe(true);
		expect(editDraft.tgPublicDomain).toBe('tg.example.test');
		expect(editDraft.path).toBe('/custom-secret-path');
		expect(editDraft.listenPort).toBe(19008);
		expect(editDraft.tgListenPort).toBe(18085);
		expect(editDraft.directHost).toBe('router.keenetic.example');
		expect(editDraft.originConfigured).toBe(true);
		expect(editDraft.originHost).toBe('origin.keenetic.example');
		expect(editDraft.originScheme).toBe('https');
		expect(editDraft.originPort).toBe(9443);
		expect(editDraft.directPort).toBe(18443);
		expect(editDraft.tlsPreset).toBe('__custom__');
		expect(editDraft.customTlsDomain).toBe('custom-sni.example.org');
		expect(editDraft.tgCarrierMode).toBe('websocket');
		expect(editDraft.clientRemark).toBe('Мой ноутбук');
		expect(editDraft.existingClientUuid).toBe('11111111-2222-4333-8444-555555555555');
		expect(editDraft.preserveExistingCredentials).toBe(true);
		expect(editDraft.preserveTransport).toBe(true);
		expect(editDraft.rawTransport).toBe('xhttp');
		expect(editDraft.rawMode).toBe('stream-up');
		expect(editDraft.rawUplinkMethod).toBe('POST');

		// Change ONLY one field (egress) -> custom fields, origin metadata & credentials remain intact in plan request
		editDraft.upstreamDevice = 'firc:router';
		editDraft.tgUpstreamDevice = 'nwg0';

		const req = buildPlanRequestFromDraft(editDraft);
		expect(req.upstream_device).toBe('firc:router');
		expect(req.tg_upstream_device).toBe('nwg0');
		expect(req.path).toBe('/custom-secret-path');
		expect(req.origin_host).toBe('origin.keenetic.example');
		expect(req.origin_scheme).toBe('https');
		expect(req.origin_port).toBe(9443);
		expect(req.preserve_transport).toBe(true);
		expect(req.raw_transport).toBe('xhttp');
		expect(req.raw_mode).toBe('stream-up');
		expect(req.raw_uplink_method).toBe('POST');
		expect(req.preserve_existing_credentials).toBe(true);
		expect(req.existing_client_uuid).toBe('11111111-2222-4333-8444-555555555555');
		expect(req.tls_domain).toBe('custom-sni.example.org');
		expect(req.carrier_mode).toBe('websocket');

		// 3. When origin_host was NOT saved in xrayConfig, do NOT fabricate it from Telegram direct_host
		const legacyWithoutOrigin = { ...existingXray, origin_host: undefined, origin_scheme: undefined, origin_port: undefined };
		const draftNoOrigin = createInitialWizardDraft({
			xrayConfig: legacyWithoutOrigin,
			tgConfig: existingTg,
		});
		expect(draftNoOrigin.originConfigured).toBe(false);
		expect(draftNoOrigin.originHost).toBe('');
		expect(draftNoOrigin.directHost).toBe('router.keenetic.example');
		expect(buildPlanRequestFromDraft(draftNoOrigin).origin_host).toBeUndefined();
	});

	it('does NOT silently replace legacy socks/mihomo:1099 egress with direct on open and enforces explicit Telegram egress for both+firc', () => {
		const legacyXray: XrayConfig = {
			enabled: true,
			listen_address: '127.0.0.1',
			listen_port: 9008,
			public_domain: 'edge.example.test',
			public_port: 443,
			path: '/xhttp-bridge',
			transport: 'xhttp',
			mode: 'packet-up',
			uplink_method: 'POST',
			dispatcher_port: 9009,
			outbound_mode: 'socks',
			outbound_interface: '',
			outbound_socks_port: 1099,
			xmux_max_connections: 0,
			clients: [],
		};

		const draft = createInitialWizardDraft({ xrayConfig: legacyXray });
		expect(draft.upstreamDevice).toBe('mihomo:1099');
		expect(draft.legacyXrayEgress).toBe(true);
		expect(isDraftEgressValid(draft).valid).toBe(false);

		// Switching to 'both' + 'firc:router' requires explicit non-FIRC tgUpstreamDevice
		draft.selectedKind = 'both';
		draft.upstreamDevice = 'firc:router';
		draft.tgUpstreamDevice = 'firc:router';
		expect(isDraftEgressValid(draft).valid).toBe(false);

		draft.tgUpstreamDevice = 'direct';
		expect(isDraftEgressValid(draft).valid).toBe(true);
	});

	it('computes effective verify kind based on enabled/configured services', () => {
		const xrayOnlySummary = buildExternalAccessSummary({
			xrayConfig: {
				enabled: true,
				listen_address: '127.0.0.1',
				listen_port: 9008,
				public_domain: 'vless.example.test',
				public_port: 443,
				path: '/xhttp-bridge',
				transport: 'xhttp',
				mode: 'packet-up',
				uplink_method: 'POST',
				dispatcher_port: 9009,
				outbound_mode: 'direct',
				outbound_interface: '',
				outbound_socks_port: 0,
				xmux_max_connections: 0,
				clients: [],
			},
			tgConfig: null,
		});
		expect(computeEffectiveVerifyKind(xrayOnlySummary)).toBe('xray');
	});

	it('formats short egress summaries for VLESS and Telegram cards without kernel/port jargon', () => {
		expect(formatShortEgressSummary('direct')).toBe('Интернет: напрямую');
		expect(formatShortEgressSummary('firc:router')).toBe('Интернет: по правилам роутера');
		expect(formatShortEgressSummary('mihomo:1099')).toBe(
			`Интернет: ${EXTERNAL_ACCESS_LABELS.egressLegacyWarning}`,
		);
		expect(
			formatShortEgressSummary(
				formatXrayEgressLabel({
					outboundMode: 'interface',
					outboundInterface: 'nwg0',
					tunnelName: 'Mullvad DE (nwg0)',
				}),
			),
		).toBe('Интернет: через Mullvad DE');
	});

	it('validates KeenDNS 4th-level labels on the client with Russian explanations', () => {
		expect(validateKeenDNSLabelClient('connect')).toEqual({ valid: true, normalized: 'connect' });
		expect(validateKeenDNSLabelClient('my-home-vless-01')).toEqual({
			valid: true,
			normalized: 'my-home-vless-01',
		});
		expect(validateKeenDNSLabelClient('')).toMatchObject({ valid: false });
		expect(validateKeenDNSLabelClient('connect.sub')).toMatchObject({
			valid: false,
			error: expect.stringContaining('без точек'),
		});
		expect(validateKeenDNSLabelClient('-bad-start')).toMatchObject({ valid: false });
		expect(validateKeenDNSLabelClient('admin')).toMatchObject({
			valid: false,
			error: expect.stringContaining('зарезервировано'),
		});
		expect(validateKeenDNSLabelClient('a'.repeat(64))).toMatchObject({
			valid: false,
			error: expect.stringContaining('63 символов'),
		});
	});

	it('preserves connection scheme in edit mode, detects unavailable current tunnel, and serializes keendns + disable_other_service', () => {
		const xrayConfig: XrayConfig = {
			enabled: true,
			listen_address: '127.0.0.1',
			listen_port: 9008,
			public_domain: 'connect.example.keenetic.link',
			public_port: 443,
			connection_way: 'direct',
			origin_type: 'cloud_address',
			path: '/xhttp-bridge',
			transport: 'xhttp',
			mode: 'packet-up',
			uplink_method: 'POST',
			dispatcher_port: 9009,
			outbound_mode: 'interface',
			outbound_interface: 'nwg9',
			outbound_socks_port: 0,
			xmux_max_connections: 0,
			clients: [
				{ id: '00000000-0000-4000-8000-000000000010', remark: 'Phone 1', enabled: true, created_at: '2026-01-01T00:00:00Z' },
				{ id: '00000000-0000-4000-8000-000000000011', remark: 'Tablet 2', enabled: true, created_at: '2026-01-01T00:00:00Z' },
			],
		};
		const tgConfig: TgWebProxyConfig = {
			enabled: true,
			scenario: 'dual',
			listen_port: 8085,
			admin_port: 8086,
			public_hostname: 'connect.example.keenetic.link',
			direct_host: 'connect.example.keenetic.link',
			direct_port: 8443,
			tls_domain: 'www.google.com',
			carrier_mode: 'websocket',
			upstream_device: 'direct',
			secret: '00112233445566778899aabbccddeeff',
			backend: '127.0.0.1:2398',
		};

		const draft = createInitialWizardDraft({ xrayConfig, tgConfig });
		expect(draft.isEditMode).toBe(true);
		expect(draft.keepExistingConnectionScheme).toBe(true);
		expect(draft.existingClientCount).toBe(2);
		expect(draft.vlessInitiallyEnabled).toBe(true);
		expect(draft.tgInitiallyEnabled).toBe(true);
		expect(draft.disableOtherService).toBe(false);

		// Unavailable current tunnel must block Next/Plan with a clear warning
		const validity = isDraftEgressValid(draft, [
			{
				id: 'nwg9',
				kind: 'interface',
				owner: 'keenetic',
				name: 'Удалённый туннель',
				iface: 'nwg9',
				available: false,
				state: 'unavailable',
				unavailable_reason: 'Туннель остановлен или удалён. Выберите рабочий выход или запустите туннель.',
				current: true,
				supports_tcp: true,
				supports_udp: true,
				generation: 1,
			},
		]);
		expect(validity.valid).toBe(false);
		expect(validity.reason).toContain('Удалённый туннель');

		// «По правилам роутера» без FIRC сервер отвергнет — черновик тоже.
		draft.upstreamDevice = 'firc:router';
		const router = (available: boolean) => isDraftEgressValid(draft, [
			{
				id: 'firc:router',
				kind: 'router_outbound',
				owner: 'firc',
				name: 'По правилам роутера',
				iface: 'router',
				available,
				unavailable_reason: available ? undefined : 'Правила FIRC неактивны',
				supports_tcp: true,
				supports_udp: true,
				generation: 1,
			},
		]).reason ?? '';
		expect(router(false)).toBe('Правила FIRC неактивны');
		expect(router(true)).not.toContain('FIRC неактивны');

		// Switch to KeenDNS create mode and disable Telegram
		draft.selectedKind = 'xray';
		draft.disableOtherService = true;
		draft.keendnsMode = 'create';
		draft.keendnsLabel = 'connect';
		draft.upstreamDevice = 'direct';

		const req = buildPlanRequestFromDraft(draft);
		expect(req.disable_other_service).toBe(true);
		expect(req.keendns_mode).toBe('create');
		expect(req.keendns_label).toBe('connect');
	});

	it('surfaces C daemon (awgm-xhttpd) unavailability and never preserves legacy ws transport', () => {
		const summary = buildExternalAccessSummary({
			xrayConfig: {
				enabled: true,
				listen_address: '127.0.0.1',
				listen_port: 9008,
				public_domain: 'vless.example.test',
				public_port: 443,
				path: '/xhttp-bridge',
				transport: 'ws',
				mode: 'packet-up',
				uplink_method: 'POST',
				dispatcher_port: 9009,
				outbound_mode: 'direct',
				outbound_interface: '',
				outbound_socks_port: 0,
				xmux_max_connections: 0,
				clients: [],
			},
			xrayStatus: {
				installed: false,
				running: false,
				backend: 'awgm-xhttpd',
				binary_path: '/opt/bin/awgm-xhttpd',
				unavailable_reason: 'awgm-xhttpd binary missing at /opt/bin/awgm-xhttpd',
			},
		});
		expect(summary.vless.installed).toBe(false);
		expect(summary.vless.backend).toBe('awgm-xhttpd');
		expect(summary.vless.unavailableReason).toContain('awgm-xhttpd');
		expect(summary.vless.transportLabel).toBe('XHTTP (POST)');

		const draft = createInitialWizardDraft({
			xrayConfig: {
				enabled: true,
				listen_address: '127.0.0.1',
				listen_port: 9008,
				public_domain: 'vless.example.test',
				public_port: 443,
				path: '/xhttp-bridge',
				transport: 'ws',
				mode: 'packet-up',
				uplink_method: 'POST',
				dispatcher_port: 9009,
				outbound_mode: 'direct',
				outbound_interface: '',
				outbound_socks_port: 0,
				xmux_max_connections: 0,
				clients: [],
			},
		});
		expect(draft.selectedMode).toBe('xhttp_get');
		expect(draft.preserveTransport).toBe(false);
		expect(uiText(draft.connectionSchemeWarning)).toContain('C-ядром');
	});

	it('treats embedded tgwebproxy runtime as unconfigured before user setup and serializes XHTTP/CDN contract fields', () => {
		const embeddedStoppedStatus = {
			installed: true,
			installed_telemt: true,
			installed_tproxy: true,
			running: false,
			port: 8085,
			direct_online: false,
			webproxy_online: false,
			backend_online: false,
			public_host: '',
			direct_host: '',
		} as unknown as TgWebProxyStatus;

		const summary = buildExternalAccessSummary({ tgStatus: embeddedStoppedStatus });
		expect(summary.telegram.configured).toBe(false);
		expect(computeEffectiveVerifyKind(summary)).toBe('both');

		const draft = createInitialWizardDraft({ tgStatus: embeddedStoppedStatus }, 'tgwebproxy');
		expect(draft.isEditMode).toBe(false);
		expect(draft.keepExistingConnectionScheme).toBe(false);
		expect(draft.tgInitiallyEnabled).toBe(false);
		expect(draft.selectedKind).toBe('tgwebproxy');

		// 1. Direct / KeenDNS connection way must emit mode='xhttp_get' and cdn_profile_id='direct'
		draft.selectedKind = 'both';
		draft.connectionWay = 'direct';
		draft.keendnsMode = 'create';
		draft.keendnsLabel = 'connect';
		draft.publicDomain = 'connect.example.netcraze.club';
		draft.tgScenario = 'dual';
		const directReq = buildPlanRequestFromDraft(draft);
		expect(directReq.mode).toBe('xhttp_get');
		expect(directReq.cdn_profile_id).toBe('direct');
		expect(directReq.carrier_mode).toBe('websocket');

		// 2. External HTTPS + direct_fake_tls: tgwebproxy-only forces 'direct',
		//    while 'both' keeps VLESS's 'cdn_get' (backend forTelegram() normalizes Telegram to 'direct')
		draft.connectionWay = 'external_https';
		draft.tgScenario = 'direct_fake_tls';
		expect(buildPlanRequestFromDraft({ ...draft, selectedKind: 'tgwebproxy' }).cdn_profile_id).toBe(
			'direct'
		);
		expect(buildPlanRequestFromDraft(draft).cdn_profile_id).toBe('cdn_get');

		// 3. External HTTPS + dual keeps cdn_get and xhttp_get
		draft.tgScenario = 'dual';
		const extReq = buildPlanRequestFromDraft(draft);
		expect(extReq.mode).toBe('xhttp_get');
		expect(extReq.cdn_profile_id).toBe('cdn_get');
	});

	it('sends tgListenPort (8085) instead of xray listenPort (9008) as listen_port when selectedKind is tgwebproxy', () => {
		const tgOnly = createInitialWizardDraft({});
		tgOnly.selectedKind = 'tgwebproxy';
		tgOnly.tgScenario = 'cdn_http';
		tgOnly.publicDomain = 'connect.example.netcraze.club';
		const tgReq = buildPlanRequestFromDraft(tgOnly);
		expect(tgReq.kind).toBe('tgwebproxy');
		expect(tgReq.listen_port).toBe(8085);
		expect(tgReq.tg_listen_port).toBe(8085);

		const xrayOnly = createInitialWizardDraft({});
		xrayOnly.selectedKind = 'xray';
		const xrayReq = buildPlanRequestFromDraft(xrayOnly);
		expect(xrayReq.listen_port).toBe(9008);

		const both = createInitialWizardDraft({});
		both.selectedKind = 'both';
		const bothReq = buildPlanRequestFromDraft(both);
		expect(bothReq.listen_port).toBe(9008);
		expect(bothReq.tg_listen_port).toBe(8085);
	});

	it('never reports running overallState when tgConfig.enabled is false', () => {
		const summary = buildExternalAccessSummary({
			tgConfig: {
				enabled: false,
				scenario: 'cdn_http',
				listen_port: 8085,
				admin_port: 8086,
				public_hostname: 'tg.example.test',
				direct_host: 'tg.example.test',
				direct_port: 8443,
				tls_domain: 'www.google.com',
				carrier_mode: 'https-lanes',
				upstream_device: 'direct',
				secret: '',
				backend: '127.0.0.1:2398',
			},
			tgStatus: {
				installed: true,
				running: true,
				port: 8085,
				direct_online: false,
				webproxy_online: true,
			} as unknown as TgWebProxyStatus,
		});
		expect(summary.telegram.overallState).toBe('partial');

		const stoppedSummary = buildExternalAccessSummary({
			tgConfig: {
				enabled: false,
				scenario: 'cdn_http',
				listen_port: 8085,
				admin_port: 8086,
				public_hostname: 'tg.example.test',
				direct_host: 'tg.example.test',
				direct_port: 8443,
				tls_domain: 'www.google.com',
				carrier_mode: 'https-lanes',
				upstream_device: 'direct',
				secret: '',
				backend: '127.0.0.1:2398',
			},
			tgStatus: {
				installed: true,
				running: false,
				port: 8085,
				direct_online: false,
				webproxy_online: false,
			} as unknown as TgWebProxyStatus,
		});
		expect(stoppedSummary.telegram.overallState).toBe('stopped');
	});

	it('disables KeenDNS and web-only fields for MTProto-only (direct_fake_tls) while keeping them for web proxy / VLESS', () => {
		expect(isTgDirectOnlyDraft({ selectedKind: 'tgwebproxy', tgScenario: 'direct_fake_tls' })).toBe(true);
		expect(draftNeedsWebIngress({ selectedKind: 'tgwebproxy', tgScenario: 'direct_fake_tls' })).toBe(false);

		expect(isTgDirectOnlyDraft({ selectedKind: 'tgwebproxy', tgScenario: 'cdn_http' })).toBe(false);
		expect(draftNeedsWebIngress({ selectedKind: 'tgwebproxy', tgScenario: 'cdn_http' })).toBe(true);

		expect(isTgDirectOnlyDraft({ selectedKind: 'tgwebproxy', tgScenario: 'dual' })).toBe(false);
		expect(draftNeedsWebIngress({ selectedKind: 'tgwebproxy', tgScenario: 'dual' })).toBe(true);

		expect(isTgDirectOnlyDraft({ selectedKind: 'both', tgScenario: 'direct_fake_tls' })).toBe(false);
		expect(draftNeedsWebIngress({ selectedKind: 'both', tgScenario: 'direct_fake_tls' })).toBe(true);

		const tgOnlyDraft = createInitialWizardDraft({
			tgConfig: {
				enabled: true,
				scenario: 'direct_fake_tls',
				listen_port: 8085,
				admin_port: 8086,
				public_hostname: 'stale.keenetic.link',
				direct_host: '203.0.113.10',
				direct_port: 8443,
				tls_domain: 'www.google.com',
				carrier_mode: 'https-lanes',
				upstream_device: 'direct',
				secret: '00112233445566778899aabbccddeeff',
				backend: '127.0.0.1:2398',
			},
		});
		expect(tgOnlyDraft.selectedKind).toBe('tgwebproxy');
		expect(tgOnlyDraft.connectionWay).toBe('direct');
		expect(tgOnlyDraft.originType).toBe('public_address');
		expect(uiText(tgOnlyDraft.connectionSchemeSummary)).toContain('белом IP');

		// Even if stale KeenDNS/HTTP values sit in draft, MTProto-only request must strip them
		tgOnlyDraft.keendnsMode = 'create';
		tgOnlyDraft.keendnsLabel = 'connect';
		tgOnlyDraft.originHost = 'stale.keenetic.link';
		tgOnlyDraft.publicDomain = 'stale.keenetic.link';
		tgOnlyDraft.connectionWay = 'external_https';
		tgOnlyDraft.originType = 'cloud_address';

		const mtprotoReq = buildPlanRequestFromDraft(tgOnlyDraft);
		expect(mtprotoReq.keendns_mode).toBeUndefined();
		expect(mtprotoReq.keendns_label).toBeUndefined();
		expect(mtprotoReq.public_domain).toBe('');
		expect(mtprotoReq.origin_host).toBeUndefined();
		expect(mtprotoReq.origin_scheme).toBeUndefined();
		expect(mtprotoReq.origin_port).toBeUndefined();
		expect(mtprotoReq.connection_way).toBe('direct');
		expect(mtprotoReq.origin_type).toBe('public_address');
		expect(mtprotoReq.direct_host).toBe('203.0.113.10');
		expect(mtprotoReq.direct_port).toBe(8443);

		// Switching to dual (web proxy + MTProto) preserves KeenDNS and HTTP fields
		tgOnlyDraft.tgScenario = 'dual';
		const dualReq = buildPlanRequestFromDraft(tgOnlyDraft);
		expect(dualReq.keendns_mode).toBe('create');
		expect(dualReq.keendns_label).toBe('connect');
		expect(dualReq.public_domain).toBe('stale.keenetic.link');
		expect(dualReq.origin_host).toBe('stale.keenetic.link');

		// 3rd-level direct KeenDNS domain is preferred over raw white IP when available
		expect(
			pickDefaultMtprotoDirectHost({
				directDomain: '  myrouter.keenetic.pro  ',
				whiteIP: '203.0.113.10',
			})
		).toBe('myrouter.keenetic.pro');
		expect(pickDefaultMtprotoDirectHost({ directDomain: '', whiteIP: ' 203.0.113.10 ' })).toBe(
			'203.0.113.10'
		);
		expect(pickDefaultMtprotoDirectHost({})).toBe('');
	});

	it('aligns dual/both direct_host fallback, filters stale direct_fake_tls :9009 warnings, and drops sticky scheme on scenario switch', () => {
		// 1. dual with empty directHost & originHost falls back to first public domain
		const dualDraft = createInitialWizardDraft({});
		dualDraft.selectedKind = 'tgwebproxy';
		dualDraft.tgScenario = 'dual';
		dualDraft.publicDomain = 'edge.example.test, alt.example.test';
		dualDraft.originHost = '';
		dualDraft.directHost = '';
		expect(buildPlanRequestFromDraft(dualDraft).direct_host).toBe('edge.example.test');

		// 2. both + direct_fake_tls with empty directHost & originHost falls back to first public domain
		const bothDirectDraft = createInitialWizardDraft({});
		bothDirectDraft.selectedKind = 'both';
		bothDirectDraft.tgScenario = 'direct_fake_tls';
		bothDirectDraft.publicDomain = 'vless.example.test, backup.example.test';
		bothDirectDraft.originHost = '';
		bothDirectDraft.directHost = '';
		expect(buildPlanRequestFromDraft(bothDirectDraft).direct_host).toBe('vless.example.test');

		// 3. direct_fake_tls with leftover tCfg.endpoints omits stale :9009 warnings and webEndpoints
		const directSummary = buildExternalAccessSummary({
			tgConfig: {
				enabled: true,
				scenario: 'direct_fake_tls',
				listen_port: 8085,
				admin_port: 8086,
				public_hostname: 'stale.example.test',
				direct_host: '203.0.113.10',
				direct_port: 8443,
				tls_domain: 'www.google.com',
				carrier_mode: 'https-lanes',
				upstream_device: 'direct',
				secret: '00112233445566778899aabbccddeeff',
				backend: '127.0.0.1:2398',
				endpoints: [
					{
						id: 'ep-1',
						hostname: 'stale.example.test',
						listen_port: 8084,
						admin_port: 8086,
						is_cdn: true,
						enabled: true,
					},
				],
			},
			dispatcherConfig: {
				enabled: true,
				listen_addr: ':9009',
				xray_target: 'http://127.0.0.1:9008',
				tg_target: 'http://127.0.0.1:8085',
				xray_path_prefix: '/xhttp-bridge',
				default_action: '404',
				routes: [
					{
						id: 'r-1',
						domain: 'other.example.test',
						description: 'Other',
						route_xray: true,
						route_telegram: false,
						enabled: true,
					},
				],
			},
		});
		expect(directSummary.telegram.webEndpoints).toEqual([]);
		expect(directSummary.dispatcher.warnings).toEqual([]);

		// 4. Switching out of direct_fake_tls edit mode clears sticky keepExistingConnectionScheme
		expect(
			shouldKeepExistingSchemeAfterStep1Change(true, {
				selectedKind: 'tgwebproxy',
				tgScenario: 'direct_fake_tls',
				keepExistingConnectionScheme: true,
			})
		).toBe(true);
		for (const nextDraft of [
			{ selectedKind: 'tgwebproxy' as const, tgScenario: 'dual' },
			{ selectedKind: 'tgwebproxy' as const, tgScenario: 'cdn_http' },
			{ selectedKind: 'both' as const, tgScenario: 'direct_fake_tls' },
			{ selectedKind: 'xray' as const, tgScenario: 'direct_fake_tls' },
		]) {
			expect(
				shouldKeepExistingSchemeAfterStep1Change(true, {
					...nextDraft,
					keepExistingConnectionScheme: true,
				})
			).toBe(false);
		}
		expect(
			shouldKeepExistingSchemeAfterStep1Change(false, {
				selectedKind: 'tgwebproxy',
				tgScenario: 'cdn_http',
				keepExistingConnectionScheme: true,
			})
		).toBe(true);
	});

	it('detects gray IP / cloud KeenDNS hosts, picks smart KeenDNS default mode, and avoids cloud 4th-level MTProto fallback', () => {
		// 1. isGrayIPNetwork
		expect(
			isGrayIPNetwork({
				hasRealityNetwork: true,
				realityWhiteIP: true,
				wanIPPrivate: true,
				keendnsEnabled: true,
				keendnsMode: 'cloud',
			})
		).toBe(false);
		expect(
			isGrayIPNetwork({
				hasRealityNetwork: true,
				realityWhiteIP: false,
			})
		).toBe(true);
		expect(isGrayIPNetwork({ wanIPPrivate: true })).toBe(true);
		expect(isGrayIPNetwork({ keendnsEnabled: true, keendnsMode: 'cloud' })).toBe(true);
		expect(isGrayIPNetwork({})).toBe(false);

		// 2. isCloudKeenDNSHost
		expect(isCloudKeenDNSHost('dacha.example.keenetic.pro', 'dacha.example.keenetic.pro', false)).toBe(
			true
		);
		expect(
			isCloudKeenDNSHost('  Connect.DACHA.EXAMPLE.KEENETIC.PRO. ', 'dacha.example.keenetic.pro', false)
		).toBe(true);
		expect(isCloudKeenDNSHost('dacha.example.keenetic.pro', 'dacha.example.keenetic.pro', true)).toBe(
			false
		);
		expect(isCloudKeenDNSHost('203.0.113.10', 'dacha.example.keenetic.pro', false)).toBe(false);
		expect(isCloudKeenDNSHost('', 'dacha.example.keenetic.pro', false)).toBe(false);

		// 3. pickDefaultKeenDNSActionMode priority: existing > create > manual
		expect(
			pickDefaultKeenDNSActionMode({ keendnsAvailable: true, hasCompatiblePublications: true })
		).toBe('existing');
		expect(
			pickDefaultKeenDNSActionMode({ keendnsAvailable: true, hasCompatiblePublications: false })
		).toBe('create');
		expect(
			pickDefaultKeenDNSActionMode({ keendnsAvailable: false, hasCompatiblePublications: false })
		).toBe('manual');

		// 4. resolveEffectiveMtprotoDirectHost & buildPlanRequestFromDraft under cloud KeenDNS
		const cloudDual = createInitialWizardDraft({});
		cloudDual.selectedKind = 'tgwebproxy';
		cloudDual.tgScenario = 'dual';
		cloudDual.originType = 'cloud_address';
		cloudDual.keendnsMode = 'create';
		cloudDual.publicDomain = 'connect.dacha.example.keenetic.pro';
		cloudDual.originHost = 'connect.dacha.example.keenetic.pro';
		cloudDual.directHost = '';
		expect(resolveEffectiveMtprotoDirectHost(cloudDual)).toBe('');
		expect(buildPlanRequestFromDraft(cloudDual).direct_host).toBe('');

		// Explicit directHost overrides even in cloud mode; keendnsMode=manual keeps legacy fallback
		cloudDual.directHost = '203.0.113.10';
		expect(resolveEffectiveMtprotoDirectHost(cloudDual)).toBe('203.0.113.10');
		expect(buildPlanRequestFromDraft(cloudDual).direct_host).toBe('203.0.113.10');

		cloudDual.directHost = '';
		cloudDual.keendnsMode = 'manual';
		const freshDraft = createInitialWizardDraft({});
		expect(freshDraft.confirmDirectBehindNAT).toBe(false);
		expect(resolveEffectiveMtprotoDirectHost(cloudDual)).toBe(
			'connect.dacha.example.keenetic.pro'
		);
	});

	it('validates MTProto direct ports, overrides cloud flag when WAN is public, keeps Reality-only draft summary clean, and isolates direct_fake_tls CDN profile on xray/both', () => {
		// 1. wanIPPrivate=false overrides keendnsMode='cloud'
		expect(
			isGrayIPNetwork({
				wanIPPrivate: false,
				keendnsEnabled: true,
				keendnsMode: 'cloud',
			})
		).toBe(false);

		// 2. isValidMtprotoDirectPort rejects reserved/out-of-range ports and accepts valid ones
		for (const okPort of [1024, 8443, 9443, 65535]) {
			expect(isValidMtprotoDirectPort(okPort)).toBe(true);
		}
		for (const badPort of [0, 22, 443, 1023, 1099, 2222, 2398, 3553, 8084, 8085, 8086, 9007, 9008, 9009, 51820, 65536, 'abc', 8443.5]) {
			expect(isValidMtprotoDirectPort(badPort)).toBe(false);
		}

		// 3. createInitialWizardDraft handles Reality-only VLESS cleanly
		const realityDraft = createInitialWizardDraft(
			{
				xrayConfig: {
					enabled: true,
					clients: [{ id: 'c1', remark: 'Phone', enabled: true }],
				} as unknown as XrayConfig,
				realityEnabled: true,
				realityPort: 2053,
				realityPublicHost: '203.0.113.10',
			},
			'xray'
		);
		expect(realityDraft.isEditMode).toBe(true);
		expect(realityDraft.selectedKind).toBe('xray');
		const summaryText =
			typeof realityDraft.connectionSchemeSummary === 'function'
				? realityDraft.connectionSchemeSummary()
				: realityDraft.connectionSchemeSummary;
		const warnText =
			typeof realityDraft.connectionSchemeWarning === 'function'
				? realityDraft.connectionSchemeWarning()
				: realityDraft.connectionSchemeWarning;
		expect(summaryText).toContain('203.0.113.10:2053');
		expect(warnText).toBe('');

		// 4. buildPlanRequestFromDraft on both + direct_fake_tls + external_https keeps VLESS cdn_profile_id
		const bothDraft = createInitialWizardDraft({});
		bothDraft.selectedKind = 'both';
		bothDraft.tgScenario = 'direct_fake_tls';
		bothDraft.connectionWay = 'external_https';
		bothDraft.cdnProfileId = 'cdn_get';
		expect(buildPlanRequestFromDraft(bothDraft).cdn_profile_id).toBe('cdn_get');
	});
});
