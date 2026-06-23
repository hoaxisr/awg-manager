// Stateful mock proxy: sits between Vite and Prism.
// - Holds usageLevel in memory; persists across GET/POST.
// - Forwards all other requests transparently.
// - Optional: simulate /singbox/install failure via env MOCK_SINGBOX_INSTALL_FAIL=1
//   or runtime POST /__mock/singbox-install-fail body {"enabled": true|false}.
// - Overrides /events with deterministic SSE for AWG + sing-box traffic.
// - Injects 8 fake singbox log entries into GET /logs (covers all 6 subgroups
//   and 4 levels). Honors group/subgroup/level filter query params.
// - Sing-box composite proxies (Feature 1): stateful stubs for
//   /singbox/router/proxies/{list,select,test} so the redesigned routing UI
//   can be smoke-tested without a real router. Selections persist in-memory.
// - /monitoring/matrix injects 2 sample t2sX rows so the redesigned monitoring
//   badge can be smoke-tested.
// - GET /connections: optional MOCK_CONNECTIONS_DELAY_MS (default 900) before
//   forwarding to Prism so the diagnostics connections skeleton is visible in dev.
//   After Prism, injects data.tunnels: "" → Direct (count = stats.direct) plus
//   MOCK_AWG_TUNNELS rows with counts split from stats.tunneled so the tunnel chip
//   row matches diagnostics UI (same as a real router).
// - Amnezia Premium CP: POST /amnezia-premium/login | account-info | download-config
//   (paths without /api — Vite rewrite). Stub session + two countries + .conf text.
// - Diagnostics «Окружение: GET /dns-check/client (Test-Phone @ 192.168.1.42,
//   policy Policy1), /system/hydraroute-status, plus existing /routing/*, /tunnels/all,
//   /proxy/*, /singbox/subscriptions.
// Default upstream: http://127.0.0.1:8080 (Prism). Listen: 8081.
/**
 * AWGM development mock proxy.
 *
 * Role:
 * - runs between Vite and Prism;
 * - forwards unknown requests unchanged;
 * - owns stateful fixtures for UI surfaces Prism cannot model well.
 *
 * Design contract:
 * - preserve backend-like response envelopes;
 * - keep all mutable mock state in this process only;
 * - prefer additive mock controls over destructive fixture rewrites;
 * - make every major UI surface smoke-testable without a router.
 *
 * Mock-only control plane:
 * - GET  /__mock/capabilities — fixture map and runtime state;
 * - GET  /__mock/tunnels — normalized tunnel catalog used by mock surfaces;
 * - POST /__mock/reset-runtime (alias /__mock/reset) — reset volatile runtime switches;
 * - POST /__mock/singbox-install-fail {"enabled": true|false}.
 *
 * Default upstream: http://127.0.0.1:8080 (Prism). Listen: 8081.
 */

import http from 'node:http';
import crypto from 'node:crypto';
import { readFile } from 'node:fs/promises';
import { fileURLToPath } from 'node:url';
import { dirname, resolve } from 'node:path';

const __dirname = dirname(fileURLToPath(import.meta.url));
const PRESETS_PATH = resolve(__dirname, 'mock-data/presets-snapshot.json');
const UNIFIED_PRESETS_PATH = resolve(__dirname, '../../internal/presets/defaults.json');
let presetsCache = null;
let unifiedPresetsCache = null;
async function getPresets() {
	if (!presetsCache) {
		presetsCache = JSON.parse(await readFile(PRESETS_PATH, 'utf8'));
	}
	return presetsCache;
}
async function getUnifiedPresets() {
	if (!unifiedPresetsCache) {
		unifiedPresetsCache = JSON.parse(await readFile(UNIFIED_PRESETS_PATH, 'utf8'));
	}
	return unifiedPresetsCache;
}

const UPSTREAM = process.env.UPSTREAM ?? 'http://127.0.0.1:8080';
const PORT = Number(process.env.PORT ?? 8081);
const VALID = new Set(['basic', 'advanced', 'expert']);

const DEFAULT_MOCK_STATE = Object.freeze({
	// Default expert keeps advanced surfaces visible for routing redesign work.
	usageLevel: 'expert',
	singboxLogLevel: 'trace',
	downloadRouteTag: 'direct',
	updateChannel: 'stable',
	updateCheckEnabled: true,
});

const MOCK_CAPABILITY_GROUPS = Object.freeze([
	{
		id: 'core',
		label: 'Settings, updates, downloads',
		endpoints: ['GET /settings/get', 'POST /settings/update', 'GET /download/outbounds'],
	},
	{
		id: 'observability',
		label: 'SSE, logs, monitoring matrix, connections',
		endpoints: ['GET /events', 'GET /logs', 'POST /logs/clear', 'GET /monitoring/matrix', 'GET /connections'],
	},
	{
		id: 'routing',
		label: 'AWG, sing-box router, DNS/rules/rulesets, policies',
		endpoints: [
			'GET /tunnels/all',
			'GET /routing/dns-routes',
			'POST /dns-routes/set-enabled',
			'POST /dns-routes/create',
			'POST /dns-routes/update',
			'POST /dns-routes/delete',
			'GET /singbox/router/status',
			'GET /singbox/router/proxies/list',
		],
	},
	{
		id: 'subscriptions',
		label: 'Sing-box subscriptions and stream details',
		endpoints: ['GET /singbox/subscriptions', 'GET /singbox/subscriptions/get-stream'],
	},
	{
		id: 'diagnostics',
		label: 'DNS check, HydraRoute status, system tunnel tests',
		endpoints: ['GET /dns-check/client', 'GET /system/hydraroute-status', 'GET /system-tunnels/test-*'],
	},
	{
		id: 'mock-control',
		label: 'Runtime controls for local dev scenarios',
		endpoints: [
			'GET /__mock/capabilities',
			'GET /__mock/tunnels',
			'POST /__mock/reset-runtime',
			'POST /__mock/reset',
			'POST /__mock/singbox-install-fail',
			'POST /__mock/download-faults',
			'POST /__mock/keenetic-os',
		],
	},
]);

// In-memory state. It intentionally survives GET/POST calls while the mock
// proxy process is alive so the UI sees realistic settings persistence.
let usageLevel = DEFAULT_MOCK_STATE.usageLevel;
let singboxLogLevel = DEFAULT_MOCK_STATE.singboxLogLevel;
let downloadRouteTag = DEFAULT_MOCK_STATE.downloadRouteTag;
let updateChannel = DEFAULT_MOCK_STATE.updateChannel;
let updateCheckEnabled = DEFAULT_MOCK_STATE.updateCheckEnabled;

// Service-download fault injection: every "download via route" endpoint
// (geo.dat, AWGM update, DNSRoute lists, Amnezia Premium, sing-box binary,
// download-outbounds) randomly returns a realistic failure instead of the
// normal/proxied success, so every UI surface can exercise all outcomes
// (sing-box off, AWG iface down, timeout, network drop, generic). Enabled by
// default in mock dev; disable with MOCK_DOWNLOAD_FAULTS=0 or via
// POST /__mock/download-faults. Probability tunable with MOCK_DOWNLOAD_FAULT_PROB.
function parseProbability(raw, fallback) {
	const n = Number.parseFloat(raw);
	if (Number.isFinite(n) && n >= 0 && n <= 1) return n;
	return fallback;
}
let downloadFaultsEnabled = process.env.MOCK_DOWNLOAD_FAULTS !== '0';
let downloadFaultProbability = parseProbability(process.env.MOCK_DOWNLOAD_FAULT_PROB, 0.4);

function getRuntimeState() {
	return {
		usageLevel,
		singboxLogLevel,
		downloadRouteTag,
		updateChannel,
		updateCheckEnabled,
		singboxInstallShouldFail,
		downloadFaultsEnabled,
		downloadFaultProbability,
		logsCleared: { ...bucketCleared },
		keeneticOS: mockKeeneticProfile.key,
		supportsExtendedASC: mockKeeneticProfile.extended,
	};
}

function resetRuntimeControls() {
	usageLevel = DEFAULT_MOCK_STATE.usageLevel;
	singboxLogLevel = DEFAULT_MOCK_STATE.singboxLogLevel;
	downloadRouteTag = DEFAULT_MOCK_STATE.downloadRouteTag;
	updateChannel = DEFAULT_MOCK_STATE.updateChannel;
	updateCheckEnabled = DEFAULT_MOCK_STATE.updateCheckEnabled;
	singboxInstallShouldFail = process.env.MOCK_SINGBOX_INSTALL_FAIL === '1';
	downloadFaultsEnabled = process.env.MOCK_DOWNLOAD_FAULTS !== '0';
	downloadFaultProbability = parseProbability(process.env.MOCK_DOWNLOAD_FAULT_PROB, 0.4);
	bucketCleared.app = false;
	bucketCleared.singbox = false;
	mockManagedAscByServer = createInitialMockManagedAscByServer();
	mockSystemAscByTunnel = createInitialMockSystemAscByTunnel();
	applyDefaultMockKeeneticProfile();
}
const MOCK_DOWNLOAD_OUTBOUNDS = [
	{
		tag: 'direct',
		kind: 'direct',
		label: 'Direct (WAN)',
		detail: 'без туннеля',
		available: true,
	},
	{
		tag: 'awg-de-frankfurt',
		kind: 'awg',
		label: 'DE Frankfurt',
		detail: 'awg0 · de-fra.demo.example:51820',
		available: true,
	},
	{
		tag: 'awg-nl-amsterdam',
		kind: 'awg',
		label: 'NL Amsterdam',
		detail: 'opkgtun0 · nl-ams.demo.example:51820',
		available: true,
	},
	{
		tag: 'sb-vless-1',
		kind: 'singbox',
		label: 'VLESS EU-1',
		detail: 'eu1.vpn.example:443',
		available: true,
	},
	{
		tag: 'sb-vless-2',
		kind: 'singbox',
		label: 'Trojan NL-2',
		detail: 'nl2.vpn.example:443',
		available: true,
	},
	{
		tag: 'sb-subscription-1',
		kind: 'subscription',
		label: 'Sing subscription RU',
		detail: 'sub-ru.demo.example',
		available: true,
	},
	{
		tag: 'sb-subscription-2',
		kind: 'subscription',
		label: 'AleXRAY (SUB)',
		detail: 'sub-alexray.demo.example',
		available: false,
	},
];
let singboxInstallShouldFail = process.env.MOCK_SINGBOX_INSTALL_FAIL === '1';
let mockProxyInstances = [
	{
		id: 'default',
		name: 'Прокси',
		enabled: true,
		listenAll: true,
		listenInterface: '',
		port: 1099,
		auth: { enabled: false, username: '', password: '' },
		selectedOutbound: 'proxy-01',
	},
	{
		id: 'reserve',
		name: 'Резерв',
		enabled: false,
		listenAll: false,
		listenInterface: 'Home',
		port: 2099,
		auth: { enabled: false, username: '', password: '' },
		selectedOutbound: 'proxy-02',
	},
];
const mockProxyRuntimeByID = {
	default: { alive: true, activeTag: 'proxy-01', defaultTag: 'proxy-01' },
	reserve: { alive: false, activeTag: 'proxy-02', defaultTag: 'proxy-02' },
};

const NOW = () => Date.now();

const MOCK_AWG_TUNNELS = [
	{
		id: 'awg-demo-1',
		name: 'DE Frankfurt',
		type: 'amneziawg',
		status: 'running',
		enabled: true,
		defaultRoute: true,
		resolvedIspInterface: 'PPPoE0',
		resolvedIspInterfaceLabel: 'Домашний провайдер',
		endpoint: 'de-fra.demo.example:51820',
		address: '10.8.0.2/32',
		interfaceName: 'awg0',
		ndmsName: 'Wireguard0',
		rxBytes: 142_320_120,
		txBytes: 38_442_331,
		lastHandshake: new Date(Date.now() - 45_000).toISOString(),
		awgVersion: 'awg2.0',
		mtu: 1420,
		startedAt: new Date(Date.now() - 3_600_000).toISOString(),
		backend: 'kernel',
		connectivityCheck: { method: 'http' },
		pingCheck: { status: 'alive', restartCount: 0, failCount: 0, failThreshold: 3 },
	},
	{
		id: 'awg-demo-2',
		name: 'NL Amsterdam',
		type: 'amneziawg',
		status: 'running',
		enabled: true,
		defaultRoute: true,
		resolvedIspInterface: 'ISP0',
		resolvedIspInterfaceLabel: 'Резервный WAN',
		endpoint: 'nl-ams.demo.example:51820',
		address: '10.9.0.2/32',
		interfaceName: 'OpkgTun0',
		ndmsName: 'Wireguard1',
		rxBytes: 88_201_442,
		txBytes: 22_781_930,
		lastHandshake: new Date(Date.now() - 120_000).toISOString(),
		awgVersion: 'awg1.5',
		mtu: 1380,
		startedAt: new Date(Date.now() - 1_800_000).toISOString(),
		backend: 'nativewg',
		connectivityCheck: { method: 'http' },
		pingCheck: { status: 'recovering', restartCount: 1, failCount: 1, failThreshold: 3 },
	},
	{
		id: 'awg-demo-3',
		name: 'PL Warsaw',
		type: 'amneziawg',
		status: 'running',
		enabled: true,
		defaultRoute: true,
		resolvedIspInterface: 'ISP1',
		resolvedIspInterfaceLabel: 'Мобильный WAN',
		endpoint: 'pl-waw.demo.example:51820',
		address: '10.30.0.2/32',
		interfaceName: 'awg2',
		ndmsName: 'Wireguard2',
		rxBytes: 57_322_404,
		txBytes: 18_640_122,
		lastHandshake: new Date(Date.now() - 20_000).toISOString(),
		awgVersion: 'awg2.0',
		mtu: 1420,
		startedAt: new Date(Date.now() - 4_200_000).toISOString(),
		backend: 'kernel',
		connectivityCheck: { method: 'http' },
		pingCheck: { status: 'alive', restartCount: 0, failCount: 0, failThreshold: 3 },
	},
	{
		id: 'awg-demo-4',
		name: 'SE Stockholm',
		type: 'amneziawg',
		status: 'starting',
		enabled: true,
		defaultRoute: true,
		resolvedIspInterface: 'ISP2',
		resolvedIspInterfaceLabel: 'Резерв LTE',
		endpoint: 'se-sto.demo.example:51820',
		address: '10.40.0.2/32',
		interfaceName: 'awg3',
		ndmsName: 'Wireguard3',
		rxBytes: 0,
		txBytes: 0,
		lastHandshake: '',
		awgVersion: 'awg1.5',
		mtu: 1380,
		startedAt: '',
		backend: 'nativewg',
		connectivityCheck: { method: 'http' },
		pingCheck: { status: 'alive', restartCount: 0, failCount: 0, failThreshold: 3 },
	},
	{
		id: 'awg-demo-5',
		name: 'UK London',
		type: 'amneziawg',
		status: 'broken',
		enabled: true,
		defaultRoute: false,
		resolvedIspInterface: 'ISP0',
		resolvedIspInterfaceLabel: 'Резервный WAN',
		endpoint: 'uk-lon.demo.example:51820',
		address: '10.50.0.2/32, fd00:8::2/128',
		interfaceName: 'awg4',
		ndmsName: 'Wireguard4',
		rxBytes: 1_204_880,
		txBytes: 412_330,
		lastHandshake: new Date(Date.now() - 720_000).toISOString(),
		awgVersion: 'awg2.0',
		mtu: 1420,
		startedAt: new Date(Date.now() - 900_000).toISOString(),
		backend: 'kernel',
		connectivityCheck: { method: 'http' },
		pingCheck: { status: 'failed', restartCount: 3, failCount: 3, failThreshold: 3 },
	},
	{
		id: 'awg-demo-6',
		name: 'CA Toronto',
		type: 'amneziawg',
		status: 'running',
		enabled: true,
		defaultRoute: false,
		resolvedIspInterface: 'ISP3',
		resolvedIspInterfaceLabel: 'Оптика #2',
		endpoint: 'ca-tor.demo.example:51820',
		address: '10.60.0.2/32',
		interfaceName: 'awg5',
		ndmsName: 'Wireguard5',
		rxBytes: 8_102_344,
		txBytes: 2_741_118,
		lastHandshake: new Date(Date.now() - 65_000).toISOString(),
		awgVersion: 'awg2.0',
		mtu: 1388,
		startedAt: new Date(Date.now() - 420_000).toISOString(),
		backend: 'kernel',
		connectivityCheck: { method: 'http' },
		pingCheck: { status: 'alive', restartCount: 0, failCount: 0, failThreshold: 3 },
	},
	// Running + handshake, but HTTP self-check fails → «Нет связи» on the ping chip (like real Fin).
	{
		id: 'awg-demo-fin',
		name: 'Fin',
		type: 'amneziawg',
		status: 'running',
		enabled: true,
		defaultRoute: true,
		resolvedIspInterface: 'ppp0',
		resolvedIspInterfaceLabel: 'Dom.Ru',
		endpoint: '66.234.150.50:9911',
		address: '100.87.200.173/32',
		interfaceName: 'nwg5',
		ndmsName: 'Wireguard5',
		rxBytes: 6636,
		txBytes: 15568,
		lastHandshake: new Date(Date.now() - 90_000).toISOString(),
		awgVersion: 'awg1.5',
		mtu: 1280,
		startedAt: new Date(Date.now() - 120_000).toISOString(),
		backend: 'nativewg',
		connectivityCheck: { method: 'http' },
		pingCheck: { status: 'disabled', restartCount: 0, failCount: 0, failThreshold: 0 },
	},
];

/** AWG tunnels where monitoring self-check is down while status stays running or broken. */
const MOCK_AWG_SELF_CHECK_FAIL = new Set(['awg-demo-fin', 'awg-demo-5']);

const MOCK_SYSTEM_TUNNELS = [
	{
		id: 'Wireguard6',
		interfaceName: 'nwg0',
		description: 'St Petersburg SYS',
		status: 'down',
		connected: true,
		mtu: 1420,
		address: '10.20.0.2',
		mask: '255.255.255.255',
		uptime: 4_200,
		peer: {
			publicKey: mockPubkey(91),
			endpoint: '198.51.100.20:51820',
			via: 'ISP0',
			rxBytes: 42_881_221,
			txBytes: 18_223_554,
			lastHandshake: new Date(Date.now() - 90_000).toISOString(),
			online: true,
		},
	},
	{
		id: 'Wireguard7',
		interfaceName: 'opkgtun0',
		description: 'Moscow SYS',
		status: 'up',
		connected: true,
		mtu: 1420,
		address: '10.20.1.2',
		mask: '255.255.255.255',
		uptime: 18_340,
		peer: {
			publicKey: mockPubkey(77),
			endpoint: '185.10.68.42:51820',
			via: 'PPPoE0',
			rxBytes: 138_442_112,
			txBytes: 54_887_003,
			lastHandshake: new Date(Date.now() - 12_000).toISOString(),
			online: true,
		},
	},
];

/** Merge tunnel summary for GET /connections mocks (Prism omits map entries). */
function enrichConnectionsTunnels(body) {
	if (!body || typeof body !== 'object' || !body.data || typeof body.data !== 'object') return body;
	const data = body.data;
	const stats = data.stats;
	if (!stats || typeof stats !== 'object') return body;

	const direct = Math.max(0, Number(stats.direct) || 0);
	const tunneled = Math.max(0, Number(stats.tunneled) || 0);
	const tunnels = {
		'': {
			name: 'Direct',
			interface: '—',
			count: direct,
		},
	};
	const list = MOCK_AWG_TUNNELS;
	const n = list.length;
	let rem = tunneled;
	for (let i = 0; i < n; i++) {
		const t = list[i];
		const share = i === n - 1 ? rem : Math.floor(tunneled / n);
		if (i < n - 1) rem -= share;
		tunnels[t.id] = {
			name: t.name,
			interface: t.interfaceName ?? '—',
			count: share,
		};
	}
	data.tunnels = tunnels;
	return body;
}

const MOCK_CONNECTIONS_POOL = (() => {
	const items = [];
	const protos = ['tcp', 'udp', 'icmp'];
	const states = ['ESTABLISHED', 'TIME_WAIT', 'SYN_SENT', 'CLOSE_WAIT'];
	const directNames = ['DESKTOP-27QR2B7', 'ROCO-F5', 'My-Phone', 'Work-Laptop'];
	for (let i = 0; i < 42; i++) {
		const tunneled = i < 12;
		const tunnel = tunneled ? getConnectionTunnel(i) : null;
		const tunnelId = tunnel?.id ?? '';
		const tunnelName = tunnel?.name ?? 'Direct';
		const iface = tunnel?.interfaceName ?? 'eth3';
		const proto = protos[i % protos.length];
		const srcHost = directNames[i % directNames.length];
		const srcA = 192;
		const srcB = 168;
		const srcC = i % 2 === 0 ? 1 : 0;
		const srcD = 100 + (i % 60);
		const srcPort = 4000 + i * 37;
		const dstPort = proto === 'icmp' ? 0 : (i % 5 === 0 ? 80 : 443);
		const dstIp = `8.${8 + (i % 10)}.${8 + (i % 20)}.${8 + (i % 40)}`;
		items.push({
			protocol: proto,
			src: `${srcA}.${srcB}.${srcC}.${srcD}`,
			dst: dstIp,
			srcPort,
			dstPort,
			state: states[i % states.length],
			packets: 30 + i * 2,
			bytes: 300 + i * 1111,
			interface: iface,
			tunnelId,
			tunnelName,
			clientMac: `AA:BB:CC:DD:EE:${String((10 + i) % 99).padStart(2, '0')}`,
			clientName: srcHost,
			rules: tunneled
				? [{ listId: `list-${(i % 4) + 1}`, listName: ['YouTube', 'Discord', 'OpenAI', 'GitHub'][i % 4] }]
				: [],
		});
	}
	return items;
})();

function sortConnections(items, sortBy, sortDir) {
	const dir = sortDir === 'desc' ? -1 : 1;
	const cmp = (a, b) => (a > b ? 1 : a < b ? -1 : 0);
	return items.sort((a, b) => {
		switch (sortBy) {
			case 'proto': return cmp(a.protocol, b.protocol) * dir;
			case 'src': return cmp(`${a.src}:${a.srcPort}`, `${b.src}:${b.srcPort}`) * dir;
			case 'dst': return cmp(`${a.dst}:${a.dstPort}`, `${b.dst}:${b.dstPort}`) * dir;
			case 'iface': return cmp(a.interface, b.interface) * dir;
			case 'state': return cmp(a.state, b.state) * dir;
			case 'bytes': return cmp(a.bytes, b.bytes) * dir;
			default: return 0;
		}
	});
}

function buildMockConnectionsResponse(url) {
	const tunnel = (url.searchParams.get('tunnel') || 'all').toLowerCase();
	const protocol = (url.searchParams.get('protocol') || 'all').toLowerCase();
	const search = (url.searchParams.get('search') || '').trim().toLowerCase();
	const offset = Math.max(0, Number(url.searchParams.get('offset') || 0) || 0);
	const limit = Math.max(1, Number(url.searchParams.get('limit') || 50) || 50);
	const sortBy = (url.searchParams.get('sortBy') || '').toLowerCase();
	const sortDir = (url.searchParams.get('sortDir') || 'asc').toLowerCase();

	let filtered = MOCK_CONNECTIONS_POOL.filter((c) => {
		if (tunnel === 'direct' && c.tunnelId !== '') return false;
		if (tunnel !== 'all' && tunnel !== 'direct' && c.tunnelId !== tunnel) return false;
		if (protocol !== 'all' && c.protocol !== protocol) return false;
		if (search) {
			const hay = `${c.src} ${c.dst} ${c.srcPort} ${c.dstPort} ${c.clientName} ${c.state} ${c.interface} ${c.tunnelName}`.toLowerCase();
			if (!hay.includes(search)) return false;
		}
		return true;
	});

	filtered = sortConnections(filtered, sortBy, sortDir);
	const totalFiltered = filtered.length;
	const page = filtered.slice(offset, offset + limit);

	const stats = {
		total: MOCK_CONNECTIONS_POOL.length,
		direct: MOCK_CONNECTIONS_POOL.filter((c) => !c.tunnelId).length,
		tunneled: MOCK_CONNECTIONS_POOL.filter((c) => !!c.tunnelId).length,
		protocols: {
			tcp: MOCK_CONNECTIONS_POOL.filter((c) => c.protocol === 'tcp').length,
			udp: MOCK_CONNECTIONS_POOL.filter((c) => c.protocol === 'udp').length,
			icmp: MOCK_CONNECTIONS_POOL.filter((c) => c.protocol === 'icmp').length,
		},
	};

	const tunnels = buildConnectionsTunnelSummary(stats);

	return {
		success: true,
		data: {
			stats,
			tunnels,
			connections: page,
			pagination: { total: totalFiltered, offset, limit, returned: page.length },
			fetchedAt: new Date().toISOString(),
		},
	};
}

const MOCK_EXTERNAL_TUNNELS = [
	{
		interfaceName: 'Wireguard9',
		tunnelNumber: 9,
		isAWG: true,
		publicKey: mockPubkey(81),
		endpoint: 'ext.demo.example:51820',
		lastHandshake: '2 мин назад',
		rxBytes: 12_200_000,
		txBytes: 3_400_000,
	},
	{
		interfaceName: 'Wireguard10',
		tunnelNumber: 10,
		isAWG: false,
		publicKey: mockPubkey(82),
		endpoint: 'backup.demo.example:51820',
		lastHandshake: '',
		rxBytes: 1_200_000,
		txBytes: 640_000,
	},
];

const MOCK_SINGBOX_TUNNELS = [
	{
		tag: 'Kto-VLESS-kto-po-drova',
		protocol: 'vless',
		server: 'de01.demo.example',
		port: 443,
		security: 'reality',
		transport: 'tcp',
		listenPort: 11011,
		proxyInterface: 't2s0',
		kernelInterface: 'sb0',
		sni: 'cdn.cloudflare.com',
		fingerprint: 'chrome',
		connectivity: { connected: true, latency: 86 },
		running: true,
	},
	{
		tag: 'vless-nl-ws',
		protocol: 'vless',
		server: 'nl02.demo.example',
		port: 443,
		security: 'tls',
		transport: 'grpc',
		listenPort: 11012,
		proxyInterface: 't2s1',
		kernelInterface: 'sb1',
		sni: 'static.example.org',
		fingerprint: 'safari',
		connectivity: { connected: true, latency: 121 },
		running: true,
	},
	{
		tag: 'naive-us-edge',
		protocol: 'naive',
		server: 'us03.demo.example',
		port: 443,
		security: 'tls',
		transport: 'https',
		listenPort: 11013,
		proxyInterface: 't2s2',
		kernelInterface: 'sb2',
		username: 'demo-user',
		sni: 'www.bing.com',
		connectivity: { connected: false, latency: null },
		running: true,
	},
	{
		tag: 'trojan-jp-timeout',
		protocol: 'trojan',
		server: 'jp04.demo.example',
		port: 443,
		security: 'tls',
		transport: 'tcp',
		listenPort: 11014,
		proxyInterface: 't2s3',
		kernelInterface: 'sb3',
		sni: 'www.yahoo.co.jp',
		connectivity: { connected: false, latency: null },
		running: true,
	},
	{
		tag: 'hysteria-sg-off',
		protocol: 'hysteria2',
		server: 'sg05.demo.example',
		port: 443,
		security: 'tls',
		transport: 'udp',
		listenPort: 11015,
		proxyInterface: 't2s4',
		kernelInterface: 'sb4',
		sni: 'www.cloudflare.com',
		connectivity: { connected: false, latency: null },
		running: false,
	},
	{
		tag: 'ss-backup-demo',
		protocol: 'shadowsocks',
		server: 'backup.demo.example',
		port: 8388,
		security: '',
		transport: 'tcp',
		listenPort: 11016,
		proxyInterface: 't2s5',
		kernelInterface: 'sb5',
		connectivity: { connected: true, latency: 94 },
		running: true,
	},
	{
		tag: 'mieru-cn-demo',
		protocol: 'mieru',
		server: 'cn01.demo.example',
		port: 443,
		security: 'none',
		transport: 'tcp',
		listenPort: 11017,
		proxyInterface: 't2s6',
		kernelInterface: 'sb6',
		username: 'demo-user',
		connectivity: { connected: true, latency: 108 },
		running: true,
	},
];

const TRAFFIC_PROFILES = {
	'awg-demo-1': {
		baseRx: 220_000,
		baseTx: 72_000,
		waveRx: 36_000,
		waveTx: 12_000,
		driftRx: 18_000,
		driftTx: 7_000,
		rxStep: 170_000,
		txStep: 54_000,
		jitterRx: 14_000,
		jitterTx: 5_000,
		burstEvery: 0,
		burstRx: 0,
		burstTx: 0,
	},
	'awg-demo-2': {
		baseRx: 110_000,
		baseTx: 38_000,
		waveRx: 72_000,
		waveTx: 24_000,
		driftRx: 22_000,
		driftTx: 8_000,
		rxStep: 105_000,
		txStep: 41_000,
		jitterRx: 22_000,
		jitterTx: 8_000,
		burstEvery: 6,
		burstRx: 210_000,
		burstTx: 66_000,
	},
	'awg-demo-3': {
		baseRx: 24_000,
		baseTx: 8_000,
		waveRx: 10_000,
		waveTx: 3_000,
		driftRx: 5_000,
		driftTx: 2_000,
		rxStep: 12_000,
		txStep: 4_000,
		jitterRx: 2_000,
		jitterTx: 1_000,
		burstEvery: 0,
		burstRx: 0,
		burstTx: 0,
	},
	'Kto-VLESS-kto-po-drova': {
		baseRx: 260_000,
		baseTx: 96_000,
		waveRx: 42_000,
		waveTx: 16_000,
		driftRx: 20_000,
		driftTx: 7_000,
		rxStep: 240_000,
		txStep: 88_000,
		jitterRx: 20_000,
		jitterTx: 8_000,
		burstEvery: 0,
		burstRx: 0,
		burstTx: 0,
	},
	'vless-nl-ws': {
		baseRx: 150_000,
		baseTx: 58_000,
		waveRx: 64_000,
		waveTx: 20_000,
		driftRx: 28_000,
		driftTx: 11_000,
		rxStep: 175_000,
		txStep: 67_000,
		jitterRx: 24_000,
		jitterTx: 10_000,
		burstEvery: 5,
		burstRx: 140_000,
		burstTx: 42_000,
	},
	'naive-us-edge': {
		baseRx: 62_000,
		baseTx: 25_000,
		waveRx: 44_000,
		waveTx: 18_000,
		driftRx: 16_000,
		driftTx: 6_000,
		rxStep: 84_000,
		txStep: 33_000,
		jitterRx: 18_000,
		jitterTx: 7_000,
		burstEvery: 8,
		burstRx: 260_000,
		burstTx: 90_000,
	},
	'trojan-jp-timeout': {
		baseRx: 58_000,
		baseTx: 22_000,
		waveRx: 36_000,
		waveTx: 14_000,
		driftRx: 14_000,
		driftTx: 5_000,
		rxStep: 72_000,
		txStep: 28_000,
		jitterRx: 14_000,
		jitterTx: 6_000,
		burstEvery: 0,
		burstRx: 0,
		burstTx: 0,
	},
	'hysteria-sg-off': {
		baseRx: 8_000,
		baseTx: 3_000,
		waveRx: 2_000,
		waveTx: 1_000,
		driftRx: 1_000,
		driftTx: 500,
		rxStep: 1_000,
		txStep: 400,
		jitterRx: 500,
		jitterTx: 200,
		burstEvery: 0,
		burstRx: 0,
		burstTx: 0,
	},
};

function trafficProfile(id) {
	return TRAFFIC_PROFILES[id] ?? {
		baseRx: 160_000,
		baseTx: 54_000,
		waveRx: 48_000,
		waveTx: 14_000,
		driftRx: 22_000,
		driftTx: 8_000,
		rxStep: 140_000,
		txStep: 42_000,
		jitterRx: 18_000,
		jitterTx: 8_000,
		burstEvery: 0,
		burstRx: 0,
		burstTx: 0,
	};
}

const awgTrafficCounters = new Map(
	MOCK_AWG_TUNNELS
		.filter((t) => t.status === 'running')
		.map((t, i) => [
			t.id,
			{
				eventId: t.ndmsName || t.interfaceName || t.id,
				profileId: t.id,
				rxBytes: t.rxBytes ?? 0,
				txBytes: t.txBytes ?? 0,
				rxStep: trafficProfile(t.id).rxStep,
				txStep: trafficProfile(t.id).txStep,
				tick: i * 2,
				lastHandshake: t.lastHandshake,
				startedAt: t.startedAt,
			},
		]),
);

const singboxTrafficCounters = new Map(
	MOCK_SINGBOX_TUNNELS.map((t, i) => [
		t.tag,
		{
			tag: t.tag,
			download: t.running && t.connectivity?.connected ? 64_000_000 + i * 10_000_000 : 0,
			upload: t.running && t.connectivity?.connected ? 12_000_000 + i * 4_000_000 : 0,
			downloadStep: trafficProfile(t.tag).rxStep,
			uploadStep: trafficProfile(t.tag).txStep,
			profileId: t.tag,
			tick: i * 3,
		},
	]),
);

function isSingboxTunnelTrafficActive(tag) {
	const t = MOCK_SINGBOX_TUNNELS.find((x) => x.tag === tag);
	return !!(t && t.running && t.connectivity?.connected);
}

const MOCK_SHARE_LINK_TYPES = new Set([
	'vless',
	'trojan',
	'shadowsocks',
	'hysteria2',
	'naive',
	'mieru',
]);

/** Share-link scheme for mock encode stub (outbound type → URI scheme). */
function mockShareLinkScheme(type) {
	if (type === 'shadowsocks') return 'ss';
	if (type === 'mieru') return 'mierus';
	return type;
}

function buildAwgSnapshot() {
	return {
		tunnels: MOCK_AWG_TUNNELS.map((t) => {
			const live = awgTrafficCounters.get(t.id);
			return live
				? {
					...t,
					rxBytes: live.rxBytes,
					txBytes: live.txBytes,
					lastHandshake: live.lastHandshake,
					startedAt: live.startedAt,
				}
				: { ...t };
		}),
		external: MOCK_EXTERNAL_TUNNELS.map((t) => ({ ...t })),
		system: MOCK_SYSTEM_TUNNELS.map((t) => ({ ...t, peer: t.peer ? { ...t.peer } : undefined })),
	};
}

// ── Watchdog «Мониторинг» fixtures ──────────────────────────────────────────
// Maps the AWG fixtures (which carry only a coarse pingCheck.status) into the
// rich TunnelPingStatus / PingLogEntry shapes the Monitoring cards join by id.
// Fixtures with status:'disabled' or 'starting' are skipped (no watchdog row).

// Per-tunnel watchdog profile keyed by fixture id. Only listed tunnels become
// watchdog cards. status:'failed' (fixture) → 'recovering' (UI union has none).
const MOCK_PINGCHECK_PROFILES = {
	'awg-demo-1': { status: 'alive', method: 'icmp', target: '8.8.8.8', lastLatency: 42, failCount: 0, restartCount: 0 },
	'awg-demo-2': { status: 'recovering', method: 'http', target: 'https://nl-ams.demo.example', lastLatency: 118, failCount: 2, restartCount: 1 },
	'awg-demo-3': { status: 'alive', method: 'icmp', target: '1.1.1.1', lastLatency: 38, failCount: 0, restartCount: 0 },
	'awg-demo-5': { status: 'recovering', method: 'icmp', target: '9.9.9.9', lastLatency: 95, failCount: 2, restartCount: 2 },
};

function buildPingCheckStatus() {
	const tunnels = [];
	for (const t of MOCK_AWG_TUNNELS) {
		const p = MOCK_PINGCHECK_PROFILES[t.id];
		if (!p) continue;
		tunnels.push({
			tunnelId: t.id,
			tunnelName: t.name,
			enabled: true,
			backend: t.backend === 'nativewg' ? 'nativewg' : 'kernel',
			status: p.status,
			method: p.method,
			lastLatency: p.lastLatency,
			failCount: p.failCount,
			failThreshold: 3,
			restartCount: p.restartCount,
		});
	}
	return { enabled: true, tunnels };
}

// ~12 newest-first entries per watchdog tunnel. Alive: all success, low latency.
// Recovering: mostly success at higher latency, but the 2 newest are timeouts
// with rising failCount + a stateChange marker (the card surfaces the 3 newest).
function buildPingCheckLogs() {
	const entries = [];
	const now = Date.now();
	for (const t of MOCK_AWG_TUNNELS) {
		const p = MOCK_PINGCHECK_PROFILES[t.id];
		if (!p) continue;
		const recovering = p.status === 'recovering';
		// index 0 = newest. interval ~30s, jittered a few seconds.
		for (let i = 0; i < 12; i++) {
			const ts = new Date(now - i * 30_000 - (i % 3) * 1500).toISOString();
			let success = true;
			let latency;
			let error = '';
			let stateChange = '';
			let failCount = 0;
			if (recovering) {
				if (i === 0) {
					success = false; latency = 0; error = 'timeout';
					stateChange = 'recovering'; failCount = 2;
				} else if (i === 1) {
					success = false; latency = 0; error = 'timeout';
					stateChange = 'link_toggle'; failCount = 1;
				} else {
					latency = 110 + ((i * 7) % 16); // 110-125ms
				}
			} else {
				latency = 40 + ((i * 5) % 16); // 40-55ms
			}
			entries.push({
				timestamp: ts,
				tunnelId: t.id,
				tunnelName: t.name,
				success,
				latency,
				error,
				failCount,
				threshold: 3,
				stateChange,
				backend: t.backend === 'nativewg' ? 'nativewg' : 'kernel',
			});
		}
	}
	// Newest-first overall.
	entries.sort((a, b) => Date.parse(b.timestamp) - Date.parse(a.timestamp));
	return entries;
}

// Full AWGTunnel for getTunnel(id) — fixture merged with a complete pingCheck
// config so the card config-line («ICMP → 8.8.8.8 · 30с · порог 3») renders.
function buildSingleTunnel(id) {
	const base = MOCK_AWG_TUNNELS.find((t) => t.id === id);
	if (!base) return null;
	const p = MOCK_PINGCHECK_PROFILES[id];
	return {
		...base,
		pingCheck: {
			enabled: true,
			method: p?.method ?? 'icmp',
			target: p?.target ?? '8.8.8.8',
			interval: 30,
			deadInterval: 120,
			failThreshold: 3,
			minSuccess: 1,
			timeout: 5,
			restart: true,
		},
	};
}

// Per-tunnel base latencies (ms), randomised once on startup so they stay in
// different tiers across the session but drift slightly on every SSE tick.
const AWG_BASE_LATENCY = (() => {
	// Spread tunnels across latency tiers for a useful demo:
	// good (<80ms): awg-demo-1, awg-demo-3
	// warn (80-199ms): awg-demo-2 (recovering)
	// starting: awg-demo-4 (SE Stockholm) → no latency until running
	// bad (≥200ms): awg-demo-6
	// self-check fail: awg-demo-fin → «Нет связи»
	// broken/failed: awg-demo-5 → «Сломан», ping failed
	const ranges = {
		'awg-demo-1': [15, 75],
		'awg-demo-2': [80, 170],
		'awg-demo-3': [20, 79],
		'awg-demo-4': null,
		'awg-demo-5': null,
		'awg-demo-6': [190, 250],
		'awg-demo-fin': null,
	};
	const map = {};
	for (const [id, range] of Object.entries(ranges)) {
		map[id] = range ? range[0] + Math.floor(Math.random() * (range[1] - range[0] + 1)) : null;
	}
	return map;
})();

function buildConnectivityMatrixEvent({ forced = false } = {}) {
	const nowIso = new Date().toISOString();
	const targets = [
		{ id: 'self', name: 'Self-check', host: '', url: '' },
		{ id: 'dns-cf', name: 'Cloudflare DNS', host: '1.1.1.1', url: '' },
		{ id: 'dns-google', name: 'Google DNS', host: '8.8.8.8', url: '' },
		{ id: 'dns-quad9', name: 'Quad9 DNS', host: '9.9.9.9', url: '' },
		{ id: 'cc-gstatic', name: 'connectivitycheck.gstatic.com', host: 'connectivitycheck.gstatic.com', url: '' },
	];

	const cells = [];
	const tunnelEntries = [];
	const profiles = new Map();

	function hashNumber(...parts) {
		let h = 0;
		for (const part of parts) {
			const text = String(part ?? '');
			for (let i = 0; i < text.length; i++) h = ((h << 5) - h + text.charCodeAt(i)) | 0;
		}
		return Math.abs(h);
	}

	function addTunnel(row, profile) {
		tunnelEntries.push(row);
		profiles.set(row.id, profile);
	}

	for (const t of MOCK_AWG_TUNNELS) {
		const base = AWG_BASE_LATENCY[t.id] ?? null;
		const isActive = t.status === 'running' || t.status === 'broken';
		const pingFailed = t.pingCheck?.status === 'failed';
		const selfCheckFail = MOCK_AWG_SELF_CHECK_FAIL.has(t.id);
		addTunnel({
			id: t.id,
			name: t.name,
			ifaceName: t.interfaceName,
			pingcheckTarget: '',
			selfTarget: '',
			selfMethod: 'http',
			source: 'awg',
			backend: t.backend,
			awgVersion: t.awgVersion,
			defaultRoute: !!t.defaultRoute,
		}, {
			base,
			up: base !== null && isActive && !pingFailed && !selfCheckFail,
			blankTargets: new Set(),
			failedTargets: pingFailed || selfCheckFail ? new Set(targets.map((x) => x.id)) : new Set(),
		});
	}

	for (const t of MOCK_SYSTEM_TUNNELS) {
		const id = `sys-${t.id}`;
		const up = t.connected !== false && t.status === 'up';
		addTunnel({
			id,
			name: t.description || t.interfaceName || t.id,
			ifaceName: t.interfaceName,
			pingcheckTarget: '',
			selfTarget: '',
			selfMethod: 'system',
			source: 'system',
		}, {
			base: up ? 35 + (hashNumber(id) % 90) : null,
			up,
			blankTargets: new Set(),
			failedTargets: up ? new Set(['dns-quad9']) : new Set(targets.map((x) => x.id)),
		});
	}

	for (const t of MOCK_SINGBOX_TUNNELS) {
		const connected = !!(t.running && t.connectivity?.connected);
		const base = connected ? (Number(t.connectivity?.latency) || 60 + (hashNumber(t.tag) % 220)) : null;
		addTunnel({
			id: t.tag,
			name: t.tag,
			ifaceName: t.proxyInterface,
			pingcheckTarget: '',
			selfTarget: '',
			selfMethod: 'disabled',
			source: 'singbox',
			singboxTag: t.tag,
			clashDelay: connected ? base : undefined,
			urltestGroup: connected ? 'auto' : '',
			protocol: t.protocol,
			security: t.security,
			transport: t.transport,
		}, {
			base,
			up: connected,
			blankTargets: new Set(['self']),
			failedTargets: connected ? new Set() : new Set(targets.map((x) => x.id)),
		});
	}

	for (const sub of mockSubscriptions) {
		const activeTag = String(sub?.activeMember || '');
		if (!activeTag) continue;
		const member = (sub.members ?? []).find((m) => m.tag === activeTag) ?? null;
		const enabled = sub.enabled !== false && !(sub.lastError && String(sub.lastError).trim() !== '');
		const base = enabled ? 45 + (hashNumber(activeTag) % 160) : null;
		addTunnel({
			id: activeTag,
			name: sub.label || sub.selectorTag || activeTag,
			ifaceName: sub.proxyIndex >= 0 ? `t2s${sub.proxyIndex}` : sub.inboundTag,
			pingcheckTarget: '',
			selfTarget: '',
			selfMethod: 'disabled',
			source: 'singbox',
			singboxTag: activeTag,
			clashDelay: enabled ? base : undefined,
			urltestGroup: sub.mode === 'urltest' ? (sub.selectorTag || 'urltest') : '',
			subscription: true,
			protocol: member?.protocol,
			security: member?.security,
			transport: member?.transport,
		}, {
			base,
			up: enabled,
			blankTargets: new Set(['self']),
			failedTargets: enabled ? new Set() : new Set(targets.map((x) => x.id)),
		});
	}

	for (let ti = 0; ti < targets.length; ti++) {
		const target = targets[ti];
		for (let vi = 0; vi < tunnelEntries.length; vi++) {
			const tunnel = tunnelEntries[vi];
			const profile = profiles.get(tunnel.id) ?? {};
			const blank = profile.blankTargets?.has(target.id);
			const failed = !profile.up || profile.failedTargets?.has(target.id);
			const base = Number(profile.base) || 80;
			const hash = hashNumber(target.id, tunnel.id);
			const drift = Math.round(Math.sin((Date.now() / 1800) + hash) * 9);
			const forceJitter = forced ? Math.floor(Math.random() * 15) - 7 : 0;
			const latency = blank || failed
				? null
				: Math.max(10, Math.min(520, base + (hash % 55) + drift + forceJitter + ti * 6));

			cells.push({
				targetId: target.id,
				tunnelId: tunnel.id,
				latencyMs: latency,
				ok: latency !== null,
				activeForRestart: tunnel.source === 'awg' && tunnel.defaultRoute && target.id === 'dns-google',
				isSelf: target.id === 'self',
				ts: nowIso,
			});
		}
	}

	return {
		targets,
		tunnels: tunnelEntries,
		cells,
		updatedAt: nowIso,
	};
}

function buildAwgTunnelMetaMap() {
	const byId = new Map();
	const byName = new Map();
	const byIface = new Map();
	for (const t of MOCK_AWG_TUNNELS) {
		if (t.id) byId.set(String(t.id).toLowerCase(), t);
		if (t.name) byName.set(String(t.name).toLowerCase(), t);
		if (t.interfaceName) byIface.set(String(t.interfaceName).toLowerCase(), t);
	}
	return { byId, byName, byIface };
}

function enrichMonitoringMatrixAwgTunnels(tunnels) {
	if (!Array.isArray(tunnels) || tunnels.length === 0) return;
	const meta = buildAwgTunnelMetaMap();
	let anyMatched = false;
	for (let i = 0; i < tunnels.length; i++) {
		const row = tunnels[i];
		if (!row || typeof row !== 'object') continue;
		const idKey = row.id ? String(row.id).toLowerCase() : '';
		const nameKey = row.name ? String(row.name).toLowerCase() : '';
		const ifaceKey = row.ifaceName ? String(row.ifaceName).toLowerCase() : '';
		const match =
			(idKey && meta.byId.get(idKey)) ||
			(nameKey && meta.byName.get(nameKey)) ||
			(ifaceKey && meta.byIface.get(ifaceKey));
		if (!match) continue;
		anyMatched = true;
		row.source = 'awg';
		row.backend = match.backend;
		row.awgVersion = match.awgVersion;
		row.defaultRoute = !!match.defaultRoute;
		row.ifaceName = row.ifaceName || match.interfaceName || '';
	}
	// Prism mock often returns a single generic tunnel row; force a representative AWG row.
	if (!anyMatched && tunnels[0] && typeof tunnels[0] === 'object') {
		const first = tunnels[0];
		first.source = 'awg';
		first.backend = 'nativewg';
		first.awgVersion = 'awg2.0';
		first.defaultRoute = true;
	}
}

const TRAFFIC_PERIOD_MS = {
	'5m': 5 * 60_000,
	'10m': 10 * 60_000,
	'30m': 30 * 60_000,
	'1h': 60 * 60_000,
	'3h': 3 * 60 * 60_000,
	'6h': 6 * 60 * 60_000,
	'12h': 12 * 60 * 60_000,
	'24h': 24 * 60 * 60_000,
	'48h': 48 * 60 * 60_000,
};

function buildTrafficPoints(id, period) {
	const durationMs = TRAFFIC_PERIOD_MS[period] ?? TRAFFIC_PERIOD_MS['1h'];
	const count = Math.min(360, Math.max(2, Math.round(durationMs / 10_000)));
	const stepMs = durationMs / Math.max(count - 1, 1);
	const now = NOW();
	const points = [];
	const profile = trafficProfile(id);
	// SSE advances awgTrafficCounters.tick; tie synthetic rates to it so GET /tunnels/traffic
	// drifts with the same mock "session" as tunnel:traffic (otherwise the chart looks frozen).
	const live = awgTrafficCounters.get(id);
	const phaseOffset = live?.tick ?? 0;
	for (let i = count - 1; i >= 0; i--) {
		const tick = count - i + phaseOffset;
		const wave = Math.sin((tick + id.length) / 4.5);
		const drift = Math.cos((tick + id.length) / 8);
		const burst = profile.burstEvery > 0 && tick % profile.burstEvery === 0;
		const pointRx = Math.max(
			18_000,
			Math.round(
				profile.baseRx +
				wave * profile.waveRx +
				drift * profile.driftRx +
				(burst ? profile.burstRx : 0),
			),
		);
		const pointTx = Math.max(
			8_000,
			Math.round(
				profile.baseTx +
				wave * profile.waveTx +
				drift * profile.driftTx +
				(burst ? profile.burstTx : 0),
			),
		);
		points.push({
			t: Math.floor((now - i * stepMs) / 1000),
			rx: pointRx,
			tx: pointTx,
		});
	}
	return points;
}

function buildTrafficResponse(id, period) {
	const points = buildTrafficPoints(id, period);
	const avgRx = points.reduce((sum, p) => sum + p.rx, 0) / points.length;
	const avgTx = points.reduce((sum, p) => sum + p.tx, 0) / points.length;
	const current = points[points.length - 1];
	const peakRate = Math.max(...points.map((p) => Math.max(p.rx, p.tx)));
	return {
		success: true,
		data: {
			points,
			stats: {
				points: points.length,
				peakRate,
				avgRx,
				avgTx,
				currentRx: current?.rx ?? 0,
				currentTx: current?.tx ?? 0,
			},
		},
	};
}

function tickAwgTraffic() {
	const events = [];
	for (const traffic of awgTrafficCounters.values()) {
		const profile = trafficProfile(traffic.profileId);
		traffic.tick += 1;
		const burst = profile.burstEvery > 0 && traffic.tick % profile.burstEvery === 0;
		traffic.rxBytes +=
			traffic.rxStep +
			Math.round(Math.sin(traffic.tick / 3) * profile.waveRx * 0.25) +
			Math.floor(Math.random() * profile.jitterRx) +
			(burst ? profile.burstRx : 0);
		traffic.txBytes +=
			traffic.txStep +
			Math.round(Math.cos(traffic.tick / 4) * profile.waveTx * 0.25) +
			Math.floor(Math.random() * profile.jitterTx) +
			(burst ? profile.burstTx : 0);
		traffic.lastHandshake = new Date(Date.now() - (20_000 + Math.floor(Math.random() * 70_000))).toISOString();
		events.push({
			id: traffic.eventId,
			rxBytes: traffic.rxBytes,
			txBytes: traffic.txBytes,
			lastHandshake: traffic.lastHandshake,
			startedAt: traffic.startedAt,
		});
	}
	return events;
}

function tickSingboxTraffic() {
	return Array.from(singboxTrafficCounters.values(), (traffic) => {
		if (!isSingboxTunnelTrafficActive(traffic.tag)) {
			traffic.download = 0;
			traffic.upload = 0;
			return {
				tag: traffic.tag,
				download: 0,
				upload: 0,
			};
		}
		const profile = trafficProfile(traffic.profileId);
		traffic.tick += 1;
		const burst = profile.burstEvery > 0 && traffic.tick % profile.burstEvery === 0;
		traffic.download +=
			traffic.downloadStep +
			Math.round(Math.sin(traffic.tick / 3.2) * profile.waveRx * 0.35) +
			Math.floor(Math.random() * profile.jitterRx) +
			(burst ? profile.burstRx : 0);
		traffic.upload +=
			traffic.uploadStep +
			Math.round(Math.cos(traffic.tick / 4.1) * profile.waveTx * 0.35) +
			Math.floor(Math.random() * profile.jitterTx) +
			(burst ? profile.burstTx : 0);
		return {
			tag: traffic.tag,
			download: traffic.download,
			upload: traffic.upload,
		};
	});
}

function tickSubscriptionTraffic() {
	const events = [];
	for (const sub of mockSubscriptions) {
		const activeTag = String(sub?.activeMember || '');
		if (!activeTag) continue;

		const hasError = !!(sub?.lastError && String(sub.lastError).trim() !== '');
		const shouldRun = !!sub?.enabled && !hasError;
		const profile = trafficProfile(activeTag);
		const hash = Array.from(activeTag).reduce((acc, ch) => acc + ch.charCodeAt(0), 0);
		const phase = (NOW() / 1500 + hash) % 360;

		if (!shouldRun) {
			events.push({ tag: activeTag, download: 0, upload: 0 });
			continue;
		}

		const baseDown = 36_000_000 + (hash % 11) * 4_000_000;
		const baseUp = 8_000_000 + (hash % 7) * 1_500_000;
		const waveDown = Math.round(Math.sin(phase / 12) * profile.waveRx * 0.4);
		const waveUp = Math.round(Math.cos(phase / 15) * profile.waveTx * 0.4);
		const jitterDown = Math.floor(Math.random() * Math.max(1, Math.floor(profile.jitterRx * 0.8)));
		const jitterUp = Math.floor(Math.random() * Math.max(1, Math.floor(profile.jitterTx * 0.8)));

		events.push({
			tag: activeTag,
			download: Math.max(0, baseDown + waveDown + jitterDown),
			upload: Math.max(0, baseUp + waveUp + jitterUp),
		});
	}
	return events;
}

function buildSingboxTrafficEvent() {
	const merged = [];
	const seen = new Set();
	for (const item of [...tickSingboxTraffic(), ...tickSubscriptionTraffic()]) {
		const tag = String(item?.tag || '');
		if (!tag || seen.has(tag)) continue;
		seen.add(tag);
		merged.push(item);
	}
	return merged;
}

/** Clamp mock latency to 10..600 ms (0 = timeout). */
function mockDelayJitter(base, spread = 70) {
	if (Math.random() < 0.05) return 0;
	const jitter = Math.round((Math.random() - 0.5) * spread);
	return Math.max(10, Math.min(600, base + jitter));
}

function currentSingboxDelays() {
	const tunnelDelays = MOCK_SINGBOX_TUNNELS.map((t, i) => {
		if (t.tag === 'trojan-jp-timeout') {
			return { tag: t.tag, delay: 0 };
		}
		if (t.tag === 'hysteria-sg-off') {
			return { tag: t.tag, delay: 0 };
		}
		const seed = 40 + (i * 113) % 560;
		return {
			tag: t.tag,
			delay: mockDelayJitter(seed, 55),
		};
	});

	// Also emit delay samples for subscription members so subscription cards
	// get realistic "connected / timeout / stopped" states from the same SSE
	// history store used by sing-box cards.
	const subDelays = [];
	for (const sub of mockSubscriptions) {
		const members = Array.isArray(sub.members) ? sub.members : [];
		for (let i = 0; i < members.length; i++) {
			const m = members[i];
			const tag = String(m?.tag || '');
			if (!tag) continue;

			// Disabled subscriptions and errored feeds stay in timeout.
			if (!sub.enabled || (sub.lastError && String(sub.lastError).trim() !== '')) {
				subDelays.push({ tag, delay: 0 });
				continue;
			}

			// Spread 10..600 ms by tag; active member biased lower.
			const isActive = tag === sub.activeMember;
			let acc = 0;
			for (let j = 0; j < tag.length; j++) acc = ((acc << 5) - acc + tag.charCodeAt(j)) | 0;
			const spread = Math.abs(acc) % 591;
			const base = isActive ? 15 + (spread % 140) : 120 + (spread % 481);
			subDelays.push({ tag, delay: mockDelayJitter(base, 45) });
		}
	}

	// De-duplicate by tag: explicit tunnel delay wins over generated sub delay.
	const seen = new Set();
	const merged = [];
	for (const d of [...tunnelDelays, ...subDelays]) {
		if (seen.has(d.tag)) continue;
		seen.add(d.tag);
		merged.push(d);
	}
	return merged;
}

// ── Subscriptions mock state ───────────────────────────────────
// Pre-populated for visual testing — shows non-empty list state, selector with members.
let mockSubscriptions = [
	{
		// Inline server group (wizard «Группа серверов») — no subscription URL.
		id: 'sub-inlinegrp',
		label: 'Neo Inline Group',
		url: '',
		isInline: true,
		headers: [],
		refreshHours: 0,
		lastFetched: new Date(Date.now() - 45 * 60 * 1000).toISOString(),
		lastError: '',
		selectorTag: 'sub-inlinegrp',
		inboundTag: 'sb2',
		listenPort: 11020,
		proxyIndex: 2,
		memberTags: ['sub-inlinegrp-node-a', 'sub-inlinegrp-node-b', 'sub-inlinegrp-node-c'],
		members: [
			{ tag: 'sub-inlinegrp-node-a', label: 'Frankfurt', protocol: 'vless', server: 'de.inline.example', port: 443, transport: 'tcp', security: 'reality' },
			{ tag: 'sub-inlinegrp-node-b', label: 'Amsterdam', protocol: 'vless', server: 'nl.inline.example', port: 443, transport: 'ws', security: 'tls' },
			{ tag: 'sub-inlinegrp-node-c', label: 'Helsinki', protocol: 'trojan', server: 'fi.inline.example', port: 443, transport: 'tcp', security: 'tls' },
		],
		orphanTags: [],
		activeMember: 'sub-inlinegrp-node-a',
		enabled: true,
	},
	{
		id: 'sub-demo0001',
		label: 'Provider Demo',
		url: 'https://demo-provider.example/sub/aaa',
		headers: [
			{ name: 'User-Agent', value: 'Happ/4.6.0/ios/2603181556604' },
			{ name: 'X-Device-OS', value: 'iOS' },
		],
		refreshHours: 24,
		lastFetched: new Date(Date.now() - 3 * 3600 * 1000).toISOString(),
		lastError: '',
		selectorTag: 'sub-demo0001',
		inboundTag: 'sub-demo0001-in',
		listenPort: 11001,
		proxyIndex: 11,
		memberTags: [
			'sub-demo0001-aabbccdd',
			'sub-demo0001-eeff0011',
			'sub-demo0001-22334455',
		],
		members: [
			{ tag: 'sub-demo0001-aabbccdd', protocol: 'vless', server: 'de01.demo.example', port: 443, transport: 'tcp', security: 'reality' },
			{ tag: 'sub-demo0001-eeff0011', protocol: 'vless', server: 'nl02.demo.example', port: 443, transport: 'ws', security: 'tls' },
			{ tag: 'sub-demo0001-22334455', protocol: 'trojan', server: 'fi03.demo.example', port: 443, transport: 'tcp', security: 'tls' },
		],
		orphanTags: [],
		infoItems: [
			{
				id: 'sub-demo0001-banner-days',
				label: '📆 Осталось: 8 дней',
				tag: 'sub-demo0001-banner-days',
				source: 'auto',
			},
			{
				id: 'sub-demo0001-banner-traffic',
				label: '⏳ Трафик: 42.5 GB / 100 GB',
				tag: 'sub-demo0001-banner-traffic',
				source: 'auto',
			},
		],
		rejectedMembers: [
			{
				tag: 'sub-demo0001-invalid-uuid',
				label: 'service-line (bad uuid)',
				protocol: 'vless',
				server: 'localhost',
				port: 80,
				reason: 'invalid uuid format',
			},
			{
				tag: 'sub-demo0001-dead-reality',
				label: 'DE backup (reality без uTLS)',
				protocol: 'vless',
				server: 'de-backup.demo.example',
				port: 443,
				reason: 'reality requires utls block',
			},
		],
		activeMember: 'sub-demo0001-aabbccdd',
		enabled: true,
		mode: 'selector',
	},
	{
		id: 'sub-demo0002',
		label: 'Backup Provider',
		url: 'https://backup.example/sub/bbb',
		headers: [],
		refreshHours: 0,
		lastFetched: new Date(Date.now() - 7 * 24 * 3600 * 1000).toISOString(),
		lastError: 'fetch: HTTP 503',
		selectorTag: 'sub-demo0002',
		inboundTag: 'sub-demo0002-in',
		listenPort: 11002,
		proxyIndex: 12,
		memberTags: ['sub-demo0002-99887766'],
		members: [
			{ tag: 'sub-demo0002-99887766', protocol: 'shadowsocks', server: 'backup.example', port: 8388, transport: 'tcp', security: '' },
		],
		orphanTags: ['sub-demo0002-deadbeef'],
		activeMember: 'sub-demo0002-99887766',
		enabled: true,
	},
	{
		// Large urltest subscription for visual testing — exercises:
		// - urltest mode header (Issue 1)
		// - per-member labels from #fragment (Issue 2): country names, mixed languages
		// - SNI row visibility (Issue from previous PR)
		// - 11 members (above the "10+ feels slow" threshold)
		id: 'sub-bigprov',
		label: 'Big Provider Pro',
		url: 'https://bigprovider.example/sub/xyz',
		headers: [
			{ name: 'User-Agent', value: 'sing-box/1.14.0' },
		],
		refreshHours: 6,
		lastFetched: new Date(Date.now() - 30 * 60 * 1000).toISOString(),
		lastError: '',
		selectorTag: 'sub-bigprov',
		inboundTag: 'sub-bigprov-in',
		listenPort: 11003,
		proxyIndex: 13,
		mode: 'urltest',
		urlTest: {
			url: 'https://www.gstatic.com/generate_204',
			intervalSec: 60,
			toleranceMs: 50,
		},
		memberTags: [
			'sub-bigprov-de01a1b2',
			'sub-bigprov-nl02c3d4',
			'sub-bigprov-fi03e5f6',
			'sub-bigprov-fr04g7h8',
			'sub-bigprov-uk05i9j0',
			'sub-bigprov-us06k1l2',
			'sub-bigprov-jp07m3n4',
			'sub-bigprov-sg08o5p6',
			'sub-bigprov-hk09q7r8',
			'sub-bigprov-ca10s9t0',
			'sub-bigprov-au11u1v2',
			'sub-bigprov-in12w3x4',
		],
		members: [
			{ tag: 'sub-bigprov-de01a1b2', label: '🇩🇪 Germany — Frankfurt', protocol: 'vless',       server: 'de01.bigprov.example',  port: 443,   sni: 'cdn.example.com',     transport: 'tcp', security: 'reality' },
			{ tag: 'sub-bigprov-nl02c3d4', label: '🇳🇱 Netherlands — A\'dam', protocol: 'vless',      server: 'nl02.bigprov.example',  port: 443,   sni: 'static.example.org',  transport: 'ws',  security: 'tls' },
			{ tag: 'sub-bigprov-fi03e5f6', label: '🇫🇮 Finland — Helsinki',  protocol: 'trojan',     server: 'fi03.bigprov.example',  port: 443,   sni: 'cdn.example.com',     transport: 'tcp', security: 'tls' },
			{ tag: 'sub-bigprov-fr04g7h8', label: '🇫🇷 France — Paris',      protocol: 'vless',      server: 'fr04.bigprov.example',  port: 443,                                transport: 'grpc', security: 'reality' },
			{ tag: 'sub-bigprov-uk05i9j0', label: '🇬🇧 UK — London',          protocol: 'vless',      server: 'uk05.bigprov.example',  port: 8443,  sni: 'web.example.net',     transport: 'ws',  security: 'tls' },
			{ tag: 'sub-bigprov-us06k1l2', label: '🇺🇸 USA — Los Angeles',    protocol: 'shadowsocks', server: 'us06.bigprov.example', port: 8388,                              transport: 'tcp', security: '' },
			{ tag: 'sub-bigprov-jp07m3n4', label: '🇯🇵 Japan — Tokyo',        protocol: 'vless',      server: 'jp07.bigprov.example',  port: 443,   sni: 'gstatic.com',         transport: 'tcp', security: 'reality' },
			{ tag: 'sub-bigprov-sg08o5p6', label: '🇸🇬 Singapore',            protocol: 'hysteria2',  server: 'sg08.bigprov.example',  port: 443,   sni: 'sg.example.io',       transport: 'tcp', security: 'tls' },
			{ tag: 'sub-bigprov-hk09q7r8', label: '🇭🇰 Hong Kong',            protocol: 'trojan',     server: 'hk09.bigprov.example',  port: 443,                                transport: 'ws',  security: 'tls' },
			{ tag: 'sub-bigprov-ca10s9t0', label: '🇨🇦 Canada — Toronto',     protocol: 'vless',      server: 'ca10.bigprov.example',  port: 21123, sni: 'ca.example.cloud',    transport: 'tcp', security: 'tls' },
			{ tag: 'sub-bigprov-au11u1v2',                                    protocol: 'naive',      server: 'au11.bigprov.example',  port: 443,   sni: 'au.example.app',      transport: 'tcp', security: 'tls' },
			{ tag: 'sub-bigprov-in12w3x4', label: '🇮🇳 India — Mumbai',       protocol: 'vless',      server: 'in12.bigprov.example',  port: 443,   sni: 'in.example.cloud',    transport: 'tcp', security: 'tls' },
		],
		orphanTags: [],
		activeMember: 'sub-bigprov-de01a1b2',
		enabled: true,
	},
	{
		id: 'sub-latam-mixed',
		label: 'LATAM Mixed',
		url: 'https://latam.example/sub/mixed',
		headers: [],
		refreshHours: 12,
		lastFetched: new Date(Date.now() - 12 * 60 * 1000).toISOString(),
		lastError: '',
		selectorTag: 'sub-latam',
		inboundTag: 'sub-latam-in',
		listenPort: 11004,
		proxyIndex: 14,
		memberTags: [
			'sub-latam-br01aa11',
			'sub-latam-ar02bb22',
			'sub-latam-cl03cc33',
			'sub-latam-mx04dd44',
		],
		members: [
			{ tag: 'sub-latam-br01aa11', label: '🇧🇷 Brazil — São Paulo', protocol: 'vless', server: 'br01.latam.example', port: 443, transport: 'tcp', security: 'reality' },
			{ tag: 'sub-latam-ar02bb22', label: '🇦🇷 Argentina — Buenos Aires', protocol: 'trojan', server: 'ar02.latam.example', port: 443, transport: 'ws', security: 'tls' },
			{ tag: 'sub-latam-cl03cc33', label: '🇨🇱 Chile — Santiago', protocol: 'vless', server: 'cl03.latam.example', port: 8443, transport: 'grpc', security: 'tls' },
			{ tag: 'sub-latam-mx04dd44', label: '🇲🇽 Mexico — Querétaro', protocol: 'hysteria2', server: 'mx04.latam.example', port: 443, transport: 'tcp', security: 'tls' },
		],
		orphanTags: [],
		activeMember: 'sub-latam-br01aa11',
		enabled: true,
	},
	{
		id: 'sub-legacy-off',
		label: 'Legacy Provider (off)',
		url: 'https://legacy.example/sub/old',
		headers: [],
		refreshHours: 24,
		lastFetched: new Date(Date.now() - 3 * 24 * 3600 * 1000).toISOString(),
		lastError: 'fetch: context deadline exceeded',
		selectorTag: 'sub-legacy',
		inboundTag: 'sub-legacy-in',
		listenPort: 11005,
		proxyIndex: 15,
		memberTags: [
			'sub-legacy-dead0001',
			'sub-legacy-dead0002',
		],
		members: [
			{ tag: 'sub-legacy-dead0001', protocol: 'shadowsocks', server: 'legacy01.example', port: 8388, transport: 'tcp', security: '' },
			{ tag: 'sub-legacy-dead0002', protocol: 'trojan', server: 'legacy02.example', port: 443, transport: 'tcp', security: 'tls' },
		],
		orphanTags: ['sub-legacy-staleffff'],
		activeMember: 'sub-legacy-dead0001',
		enabled: false,
	},
	// enabled: false — чекбокс «Включена» снят в настройках подписки.
	{
		id: 'sub-off-eu',
		label: 'EU Nodes',
		url: 'https://eu-nodes.example/sub/off',
		headers: [{ name: 'User-Agent', value: 'sing-box/1.14.0' }],
		refreshHours: 12,
		lastFetched: new Date(Date.now() - 45 * 60 * 1000).toISOString(),
		lastError: '',
		selectorTag: 'sub-off-eu',
		inboundTag: 'sub-off-eu-in',
		listenPort: 11006,
		proxyIndex: 16,
		memberTags: [
			'sub-off-eu-de01',
			'sub-off-eu-pl02',
			'sub-off-eu-se03',
		],
		members: [
			{ tag: 'sub-off-eu-de01', label: '🇩🇪 Germany', protocol: 'vless', server: 'de01.eu-nodes.example', port: 443, transport: 'tcp', security: 'reality' },
			{ tag: 'sub-off-eu-pl02', label: '🇵🇱 Poland', protocol: 'trojan', server: 'pl02.eu-nodes.example', port: 443, transport: 'ws', security: 'tls' },
			{ tag: 'sub-off-eu-se03', label: '🇸🇪 Sweden', protocol: 'vless', server: 'se03.eu-nodes.example', port: 8443, transport: 'grpc', security: 'tls' },
		],
		orphanTags: [],
		activeMember: 'sub-off-eu-de01',
		enabled: false,
	},
	{
		id: 'sub-off-amer',
		label: 'Americas Pool',
		url: 'https://americas.example/sub/off',
		headers: [],
		refreshHours: 6,
		lastFetched: new Date(Date.now() - 2 * 3600 * 1000).toISOString(),
		lastError: '',
		selectorTag: 'sub-off-amer',
		inboundTag: 'sub-off-amer-in',
		listenPort: 11007,
		proxyIndex: 17,
		memberTags: [
			'sub-off-amer-us01',
			'sub-off-amer-ca02',
			'sub-off-amer-mx03',
		],
		members: [
			{ tag: 'sub-off-amer-us01', label: '🇺🇸 USA — NYC', protocol: 'vless', server: 'us01.americas.example', port: 443, transport: 'tcp', security: 'reality' },
			{ tag: 'sub-off-amer-ca02', label: '🇨🇦 Canada — Vancouver', protocol: 'trojan', server: 'ca02.americas.example', port: 443, transport: 'tcp', security: 'tls' },
			{ tag: 'sub-off-amer-mx03', label: '🇲🇽 Mexico — CDMX', protocol: 'hysteria2', server: 'mx03.americas.example', port: 443, transport: 'tcp', security: 'tls' },
		],
		orphanTags: ['sub-off-amer-stale99'],
		activeMember: 'sub-off-amer-us01',
		enabled: false,
	},
	{
		id: 'sub-off-err',
		label: 'Stale Feed',
		url: 'https://stale-feed.example/sub/expired',
		headers: [],
		refreshHours: 24,
		lastFetched: new Date(Date.now() - 5 * 24 * 3600 * 1000).toISOString(),
		lastError: 'fetch: HTTP 402 Payment Required',
		selectorTag: 'sub-off-err',
		inboundTag: 'sub-off-err-in',
		listenPort: 11008,
		proxyIndex: 18,
		memberTags: ['sub-off-err-only01'],
		members: [
			{ tag: 'sub-off-err-only01', protocol: 'shadowsocks', server: 'stale01.example', port: 8388, transport: 'tcp', security: '' },
		],
		orphanTags: [],
		activeMember: 'sub-off-err-only01',
		enabled: false,
	},
];
let mockSubID = mockSubscriptions.length;

/** Align list/get payloads with production SubscriptionDTO (incl. isInline). */
function toMockSubscriptionDTO(sub) {
	const url = sub.url ?? '';
	const isInline = sub.isInline ?? !String(url).trim();
	const mode = sub.mode ?? 'selector';
	const dto = {
		...sub,
		url,
		isInline,
		mode,
		enabled: sub.enabled !== false,
		headers: sub.headers ?? [],
		memberTags: sub.memberTags ?? [],
		members: sub.members ?? [],
		orphanTags: sub.orphanTags ?? [],
		rejectedMembers: sub.rejectedMembers ?? [],
		infoItems: sub.infoItems ?? [],
	};
	if (mode === 'urltest' && sub.urlTest) dto.urlTest = sub.urlTest;
	return dto;
}

function newSub(input) {
	mockSubID++;
	const id = `sub-${mockSubID.toString().padStart(8, '0')}`;
	const shortID = id.slice(0, 8);
	const memberTags = [`sub-${shortID}-aaaa`, `sub-${shortID}-bbbb`];
	const url = input.url ?? (input.inline ? '' : 'https://test');
	const isInline = !!input.inline || !String(url).trim();
	return {
		id,
		label: input.label || 'Test',
		url,
		isInline,
		headers: input.headers || [],
		refreshHours: input.refreshHours || 0,
		lastFetched: new Date().toISOString(),
		lastError: '',
		selectorTag: `sub-${shortID}`,
		inboundTag: `sub-${shortID}-in`,
		listenPort: 11000 + mockSubID,
		proxyIndex: 10 + mockSubID,
		memberTags,
		members: memberTags.map((tag, i) => ({
			tag,
			protocol: i % 2 === 0 ? 'vless' : 'trojan',
			server: `mock-${i + 1}.example`,
			port: 443,
			transport: 'tcp',
			security: 'tls',
		})),
		orphanTags: [],
		rejectedMembers: [],
		infoItems: [],
		activeMember: `sub-${shortID}-aaaa`,
		enabled: input.enabled,
	};
}

const MAX_MOCK_INFO_ITEMS = 4;

function moveMockRejectedToInfo(sub, memberTag) {
	const tag = String(memberTag || '').trim();
	if (!tag) return { error: { status: 400, code: 'MISSING_MEMBER_TAG', message: 'memberTag required' } };
	const rejected = sub.rejectedMembers ?? [];
	const idx = rejected.findIndex((r) => r.tag === tag);
	if (idx < 0) {
		return { error: { status: 404, code: 'REJECTED_NOT_FOUND', message: 'rejected member not found' } };
	}
	const info = [...(sub.infoItems ?? [])];
	if (info.length >= MAX_MOCK_INFO_ITEMS) {
		return {
			error: {
				status: 409,
				code: 'INFO_ITEMS_FULL',
				message: 'subscription: info block is full (max 4 items)',
			},
		};
	}
	const r = rejected[idx];
	const id = tag || `info-${(r.label || 'item').slice(0, 24)}`;
	if (info.some((it) => it.id === id)) {
		sub.rejectedMembers = rejected.filter((_, i) => i !== idx);
		return { ok: true };
	}
	info.push({
		id,
		label: r.label || tag,
		tag: r.tag || tag,
		source: 'auto',
	});
	sub.infoItems = info;
	sub.rejectedMembers = rejected.filter((_, i) => i !== idx);
	return { ok: true };
}

// ── Wizard mock state ──────────────────────────────────────────
let mockEngineRunning = false;
let mockSBPolicyExists = false;

// Sing-box router settings state. Defaults mirror what
// storage.defaultSettings() produces on a fresh install — WANAutoDetect=true
// + WANInterface="" is the only valid auto-mode combo. Mutated by
// POST/PUT /singbox/router/settings so the user's WAN-binding toggle
// in dev-mock survives a page reload within the same session.
let mockSBSettings = {
	enabled: false,
	policyName: '',
	deviceMode: 'policy',
	snifferEnabled: true,
	refreshMode: 'interval',
	refreshIntervalHours: 24,
	wanAutoDetect: true,
	wanInterface: '',
	// fakeip-tun engine settings (user-editable). Defaults mirror
	// DefaultFakeIPTunParams / NormalizeSingboxRouterSettings.
	fakeipStack: 'gvisor',
	fakeipPool4: '10.128.0.0/10',
	fakeipPool6: '3f80::/10',
	fakeipMtu: 1500,
	fakeipSourcePreserve: true,
};

// DHCP pools projected as fakeip "segments" (Task 2.1/2.2). Persistent
// in-memory state so the POST toggle mutates what the GET reads. dnsServer is
// the fakeip-tun DNS (.2 of the 172.18.0.1/30 link) when a pool is in fakeip.
const FAKEIP_TUN_DNS = '172.18.0.2';
// lanDNS is the pool's non-fakeip DNS, kept so a clear can restore it.
let mockFakeIPSegments = [
	// _WEBADMIN covers the policy-device LAN (192.168.1.x) and is IN fakeip so the
	// Устройства-чип renders «fakeip» назначение for devices inside it.
	{ pool: '_WEBADMIN', subnet: '192.168.1.1/24', dnsServer: FAKEIP_TUN_DNS, inFakeip: true, lanDNS: '192.168.1.1' },
	{ pool: '_WEBADMIN_GUEST_AP', subnet: '172.16.1.1/24', dnsServer: '172.16.1.1', inFakeip: false, lanDNS: '172.16.1.1' },
];

// Interfaces a user can bind a direct outbound to (issue #245). Mirrors
// backend ListBindable: all router interfaces minus our own and AWG/WG
// auto-covered. Includes a couple of non-AWG VPNs to exercise the picker.
const mockBindableInterfaces = [
	{ name: 'ipsec0', id: 'IKE0', label: 'IKEv2 office', up: true, priority: 0 },
	{ name: 'ipsec1', id: 'IPSec1', label: 'IPSec branch', up: false, priority: 0 },
	{ name: 'ppp0', id: 'PPPoE0', label: 'Letai (PPPoE)', up: true, priority: 0 },
];
let mockOutbounds = [
	{ type: 'selector', tag: 'manual-eu', outbounds: ['awg-vpn0', 'awg-sys-Wireguard0'], default: 'awg-vpn0', source: 'router' },
	{
		type: 'selector',
		tag: 'sub-demo0001',
		outbounds: ['sub-demo0001-aabbccdd', 'sub-demo0001-eeff0011', 'sub-demo0001-22334455'],
		source: 'subscription',
	},
	{
		type: 'urltest',
		tag: 'sub-bigprov',
		outbounds: [
			'sub-bigprov-de01a1b2',
			'sub-bigprov-nl02c3d4',
			'sub-bigprov-fi03e5f6',
			'sub-bigprov-fr04g7h8',
			'sub-bigprov-uk05i9j0',
			'sub-bigprov-us06k1l2',
			'sub-bigprov-jp07m3n4',
			'sub-bigprov-sg08o5p6',
			'sub-bigprov-hk09q7r8',
			'sub-bigprov-ca10s9t0',
			'sub-bigprov-au11u1v2',
			'sub-bigprov-in12w3x4',
		],
		source: 'subscription',
	},
];

// WAN interfaces returned by GET /singbox/router/wan-interfaces. Mix of
// up/down + types so the dev UI shows real variety. `name` is the kernel
// system-name that gets persisted into wanInterface; `id` is the NDMS
// interface ID (what NDMS shows in its UI); `label` is the human-friendly
// display string. The picker in EngineStatusCard shows all three.
const mockWANInterfaces = [
	{ name: 'ppp0', id: 'PPPoE0', label: 'Letai (PPPoE)', up: true, priority: 700000 },
	{ name: 'eth3', id: 'ISP', label: 'Ethernet ISP', up: false, priority: 600000 },
	{ name: 'usb0', id: 'UsbModem0', label: 'Резервный 4G', up: true, priority: 500000 },
];
let mockBoundDevices = new Set();

// Registry of live /events SSE senders so POST /singbox/router/mode can replay a
// representative singbox-router:transition progress sequence to the FE.
const sseSenders = new Set();
let mockTransitionSeq = 0;

// buildTransitionSequence returns a representative ordered list of
// singbox-router:transition events for a from→to switch (the same shape the Go
// backend's router.TransitionEvent emits). Used as the FE mock fixture.
function buildTransitionSequence(from, to) {
	const id = `switch-${++mockTransitionSeq}`;
	const ev = (step, status, extra = {}) => ({
		transitionId: id, from, to, step: { step, status }, ...extra,
	});
	if (from === to) {
		return [ev('ready', 'done', { done: true, finalState: to, step: { step: 'ready', status: 'done', message: 'already in target state' } })];
	}
	const seq = [ev('start', 'current')];
	if (from !== 'off') {
		seq.push(ev('teardown', 'current'), ev('teardown', 'done'));
	}
	if (to === 'off') {
		seq.push(ev('ready', 'done', { done: true, finalState: 'off' }));
		return seq;
	}
	seq.push(ev('provision', 'current'), ev('readiness', 'done'), ev('ready', 'done', { done: true, finalState: to }));
	return seq;
}
function scrubMockDnsServerStored(server) {
	const next = { ...server };
	const detour = typeof next.detour === 'string' ? next.detour.trim() : '';
	delete next.detour;
	if (detour && detour !== 'direct') {
		next.detour = detour;
	}
	return next;
}

/** Write path — mirrors backend scrubDNSServerDetourForSingbox. */
function sanitizeMockDnsServerForWrite(server) {
	const next = { ...server };
	const detour = typeof next.detour === 'string' ? next.detour.trim() : '';
	delete next.detour;
	if (detour && detour !== 'direct' && server?.tag !== 'dns-direct') {
		next.detour = detour;
	}
	return next;
}

let mockDNSGlobals = { final: 'dns-direct', strategy: 'prefer_ipv4' };

let mockDNSServers = [
	// UI repro: legacy detour on final DNS — human label instead of outbound tag.
	{
		tag: 'dns-direct',
		type: 'udp',
		server: '77.88.8.8',
		server_port: 53,
		detour: 'Нидерланды🇳🇱🔃',
	},
	{
		tag: 'dns-tunnel',
		type: 'udp',
		server: '9.9.9.9',
		server_port: 53,
		detour: 'vless-nl-ws',
	},
	{
		tag: 'wizard-upstream',
		type: 'udp',
		server: '77.8.8.8',
		server_port: 53,
		detour: 'sub-demo0001',
	},
	// issue #214 Sc3 repro — длинный hostname + длинный detour
	// триггерят узкое viewport overflow.
	{
		tag: 'vpn-test',
		type: 'tls',
		server: 'cloudflare-dns.example.com',
		server_port: 853,
		detour: 'fast-vpn',
	},
	{
		tag: 'dns-local-router',
		type: 'udp',
		server: '192.168.0.51',
		server_port: 53,
	},
];
let mockDNSRules = [
	{
		action: 'route',
		rule_set: ['geosite-youtube'],
		server: 'wizard-upstream',
	},
];
let mockDNSRewrites = [
	{ pattern: 'finland10*.discord.media', ips: ['104.25.158.178'] },
	{ pattern: '*.steamcontent.com', ips: ['23.55.171.10'] },
];

// ── fakeip config state ────────────────────────────────────────────────────
// Mirrors the router state above but scoped to /singbox/fakeip/config/*.
// Deliberately minimal: enough for loadAll() to resolve in dev:mock:proxy.

let mockFakeipDNSGlobals = { final: 'fakeip-dns-direct', strategy: 'prefer_ipv4' };

let mockFakeipDNSServers = [
	{ tag: 'fakeip-dns-fakeip', type: 'local' },
	{ tag: 'fakeip-dns-direct', type: 'udp', server: '77.88.8.8', server_port: 53 },
];

let mockFakeipDNSRules = [
	{ action: 'route', query_type: ['A', 'AAAA'], server: 'fakeip-dns-fakeip' },
];

let mockFakeipRules = [
	{ action: 'sniff' },
	{ action: 'hijack-dns', protocol: 'dns' },
	{ ip_is_private: true, outbound: 'direct' },
	{ action: 'route', rule_set: ['fakeip-geosite-youtube'], outbound: 'proxy-eu' },
];

const mockFakeipRuleSets = [
	{ tag: 'fakeip-geosite-youtube', type: 'remote', format: 'binary', url: 'https://cdn.example.com/geosite-youtube.srs', update_interval: '24h', download_detour: 'direct' },
];

let mockFakeipOutbounds = [
	{ type: 'selector', tag: 'proxy-eu', outbounds: ['awg-vpn0', 'awg-sys-Wireguard0'], default: 'awg-vpn0', source: 'router' },
];

/** Built-in NDMS policy names (Policy0..PolicyN), same rule as backend accesspolicy. */
function isStandardPolicyName(name) {
	return /^Policy\d+$/.test(name);
}

const mockPolicyDevices = [
	{ mac: 'aa:aa:aa:aa:aa:01', ip: '192.168.1.42', name: 'Test-Phone',    hostname: 'phone',  active: true, link: 'WiFi', policy: 'Policy1' },
	{ mac: 'aa:aa:aa:aa:aa:02', ip: '192.168.1.43', name: 'Test-Laptop',   hostname: 'laptop', active: true, link: 'WiFi', policy: '' },
	{ mac: 'aa:aa:aa:aa:aa:03', ip: '192.168.1.44', name: 'HR-Client',     hostname: 'hr',     active: true, link: 'WiFi', policy: 'HydraRoute' },
	{ mac: 'aa:aa:aa:aa:aa:04', ip: '192.168.1.45', name: 'Family-TV',     hostname: 'tv',     active: true, link: 'LAN',  policy: 'Policy0' },
	{ mac: 'aa:aa:aa:aa:aa:05', ip: '192.168.1.46', name: 'PS5',           hostname: 'ps5',    active: true, link: 'LAN',  policy: '' },
	{ mac: 'aa:aa:aa:aa:aa:06', ip: '192.168.1.47', name: 'Work-Mac',      hostname: 'work',   active: true, link: 'WiFi', policy: 'HydraRoute' },
	// Вне fakeip-сегмента (другая подсеть) → «прямой» в Устройства-чипе. Offline,
	// чтобы показать офлайн-статус + «—» в соединениях.
	{ mac: 'aa:aa:aa:aa:aa:07', ip: '10.0.5.20',    name: 'NAS-Backup',    hostname: 'nas',    active: false, link: 'LAN',  policy: '' },
];
const mockAccessPolicies = [
	{
		name: 'Policy0',
		description: 'home',
		isStandard: true,
		standalone: false,
		interfaces: [mockPolicyInterfaceRef('awg-demo-1', 0)],
		deviceCount: 1,
	},
	{
		name: 'Policy1',
		description: 'kids',
		isStandard: true,
		standalone: false,
		interfaces: [{ name: 'Direct', label: 'Direct', order: 0 }],
		deviceCount: 1,
	},
	{
		name: 'Policy2',
		description: 'work',
		isStandard: true,
		standalone: true,
		interfaces: [{ name: 'DE vless-tcp-reality', label: 'DE', order: 0 }],
		deviceCount: 0,
	},
	{
		name: 'HydraRoute',
		description: '',
		isStandard: false,
		standalone: false,
		interfaces: [
			mockPolicyInterfaceRef('awg-demo-1', 0),
			mockPolicyInterfaceRef('awg-demo-2', 1),
		],
		deviceCount: 2,
	},
	// Icon gallery — one mock per distinct icon (Policy0–2 + HydraRoute + extended set)
	{ name: 'Policy3', description: 'guest', isStandard: true, standalone: false, interfaces: [], deviceCount: 0 },
	{ name: 'Policy4', description: 'singbox', isStandard: true, standalone: false, interfaces: [], deviceCount: 0 },
	{ name: 'Policy5', description: 'ProxyRU', isStandard: true, standalone: false, interfaces: [], deviceCount: 0 },
	{ name: 'Policy6', description: 'IoT_Xyandex', isStandard: true, standalone: false, interfaces: [], deviceCount: 0 },
	{ name: 'Policy7', description: 'nfqws', isStandard: true, standalone: false, interfaces: [], deviceCount: 0 },
	{ name: 'Policy8', description: 'gaming', isStandard: true, standalone: false, interfaces: [], deviceCount: 0 },
	{ name: 'Policy9', description: 'tv', isStandard: true, standalone: false, interfaces: [], deviceCount: 0 },
	{ name: 'Policy10', description: 'beeline', isStandard: true, standalone: false, interfaces: [], deviceCount: 0 },
	{ name: 'Policy11', description: 'backup', isStandard: true, standalone: false, interfaces: [], deviceCount: 0 },
	{ name: 'Policy12', description: 'test', isStandard: true, standalone: false, interfaces: [], deviceCount: 0 },
	{ name: 'Policy13', description: 'family', isStandard: true, standalone: false, interfaces: [], deviceCount: 0 },
	{ name: 'Policy14', description: 'wifi', isStandard: true, standalone: false, interfaces: [], deviceCount: 0 },
	{ name: 'Policy15', description: '', isStandard: true, standalone: false, interfaces: [], deviceCount: 0 },
	{ name: 'Policy16', description: 'docker', isStandard: true, standalone: false, interfaces: [], deviceCount: 0 },
	{ name: 'Policy17', description: 'North_Korea', isStandard: true, standalone: false, interfaces: [], deviceCount: 0 },
];

/** Диагностика → «Окружение»: LAN-клиент и политика (GET /dns-check/client). */
const mockDnsCheckClientPayload = {
	clientIP: '192.168.1.42',
	hostname: 'Test-Phone',
	checks: [
		{
			id: 'client_policy',
			status: 'ok',
			title: 'Политика доступа клиента',
			message: 'Клиент использует политику: Policy1',
		},
	],
};

/** Диагностика → «Сведения о DNS»: распарсенный /show/dns-proxy
 *  (GET /diagnostics/dns-proxy). displayName уже заполнен — на проде это
 *  делает handler через access-policies; здесь отдаём готовую форму. */
const mockDnsProxyInfo = {
	proxies: [
		{
			name: 'System',
			displayName: 'Системный',
			tcpPort: 53,
			udpPort: 53,
			stat: { totalRequests: 1242, proxyRequestsSent: 318, cacheHitRatio: 0.744, cacheHits: 924, memory: '13.33K' },
			upstreams: [
				{ address: '8.8.8.8', port: 0, encryption: 'DoT', sni: 'dns.google', scope: 'all', rSent: 120, aRcvd: 120, nxRcvd: 4, medResp: '38ms', avgResp: '41ms', rank: 6 },
				{ address: '77.88.8.8', port: 853, encryption: 'DoT', sni: 'common.dot.dns.yandex.net', scope: 'ru', rSent: 64, aRcvd: 64, nxRcvd: 1, medResp: '70ms', avgResp: '78ms', rank: 4 },
				{ address: '9.9.9.9', port: 0, encryption: 'DoT', sni: '', scope: 'all', rSent: 134, aRcvd: 132, nxRcvd: 2, medResp: '52ms', avgResp: '60ms', rank: 5 },
			],
			staticRecords: [
				{ host: 'host1.example.net', type: 'A', value: '203.0.113.10', flag: 1 },
				{ host: 'host1.example.net', type: 'AAAA', value: '2001:db8::1', flag: 1 },
				{ host: 'awgm-dnscheck.test', type: 'A', value: '10.10.10.1', flag: 0 },
				{ host: 'host2.example.com', type: 'A', value: '203.0.113.10', flag: 1 },
				{ host: 'host2.example.com', type: 'AAAA', value: '2001:db8::1', flag: 1 },
				{ host: 'host3.example.ru', type: 'A', value: '203.0.113.10', flag: 1 },
			],
			rebind: { enabled: true, nets: ['10.10.10.1:24', '10.10.20.1:24', '172.16.6.1:24', '255.255.255.255:32'], excludes: ['ru', '*.ru'] },
		},
		{
			name: 'Policy0',
			displayName: 'IoT_VPN',
			tcpPort: 41100,
			udpPort: 41100,
			stat: { totalRequests: 87, proxyRequestsSent: 30, cacheHitRatio: 0.655, cacheHits: 57, memory: '12.75K' },
			upstreams: [
				{ address: '8.8.8.8', port: 0, encryption: 'DoT', sni: 'dns.google', scope: 'all', rSent: 6, aRcvd: 6, nxRcvd: 0, medResp: '44ms', avgResp: '49ms', rank: 5 },
				{ address: '77.88.8.8', port: 853, encryption: 'DoT', sni: 'common.dot.dns.yandex.net', scope: 'ru', rSent: 3, aRcvd: 3, nxRcvd: 0, medResp: '120ms', avgResp: '120ms', rank: 4 },
				{ address: '9.9.9.9', port: 0, encryption: 'DoT', sni: '', scope: 'all', rSent: 21, aRcvd: 21, nxRcvd: 1, medResp: '58ms', avgResp: '63ms', rank: 5 },
			],
			staticRecords: [{ host: 'awgm-dnscheck.test', type: 'A', value: '10.10.10.1', flag: 0 }],
			rebind: { enabled: true, nets: ['10.10.10.1:24'], excludes: ['ru', '*.ru'] },
		},
		{
			name: 'Policy1',
			displayName: 'Netflix',
			tcpPort: 41101,
			udpPort: 41101,
			stat: { totalRequests: 283, proxyRequestsSent: 102, cacheHitRatio: 0.64, cacheHits: 181, memory: '17.25K' },
			upstreams: [
				{ address: '8.8.8.8', port: 0, encryption: 'DoT', sni: 'dns.google', scope: 'all', rSent: 4, aRcvd: 4, nxRcvd: 0, medResp: '144ms', avgResp: '143ms', rank: 4 },
				{ address: '77.88.8.8', port: 853, encryption: 'DoT', sni: 'common.dot.dns.yandex.net', scope: 'ru', rSent: 11, aRcvd: 11, nxRcvd: 0, medResp: '70ms', avgResp: '60ms', rank: 8 },
				{ address: '9.9.9.9', port: 0, encryption: 'DoT', sni: '', scope: 'all', rSent: 87, aRcvd: 87, nxRcvd: 0, medResp: '147ms', avgResp: '109ms', rank: 4 },
			],
			staticRecords: [{ host: 'awgm-dnscheck.test', type: 'A', value: '10.10.10.1', flag: 0 }],
			rebind: { enabled: true, nets: ['10.10.10.1:24'], excludes: ['ru', '*.ru'] },
		},
		{
			name: 'Policy2',
			displayName: 'Policy2',
			tcpPort: 41102,
			udpPort: 41102,
			stat: { totalRequests: 0, proxyRequestsSent: 0, cacheHitRatio: 0, cacheHits: 0, memory: '12.75K' },
			upstreams: [
				{ address: '8.8.8.8', port: 0, encryption: 'DoT', sni: 'dns.google', scope: 'all', rSent: 0, aRcvd: 0, nxRcvd: 0, medResp: '0ms', avgResp: '0ms', rank: 1 },
				{ address: '77.88.8.8', port: 853, encryption: 'DoT', sni: 'common.dot.dns.yandex.net', scope: 'ru', rSent: 0, aRcvd: 0, nxRcvd: 0, medResp: '0ms', avgResp: '0ms', rank: 1 },
				{ address: '9.9.9.9', port: 0, encryption: 'DoT', sni: '', scope: 'all', rSent: 0, aRcvd: 0, nxRcvd: 0, medResp: '0ms', avgResp: '0ms', rank: 1 },
			],
			staticRecords: [],
			rebind: { enabled: false, nets: [], excludes: [] },
		},
	],
};

/** HydraRoute Neo в блоке AWGM (GET /system/hydraroute-status). */
const mockHydraRouteStatus = {
	installed: true,
	running: true,
	version: '2.4.1',
	pid: 2345,
	processState: 'running',
};

const mockRoutingDnsRoutes = [
	{
		id: 'dns-work-vpn',
		name: 'Work VPN',
		domains: ['corp.example.com'],
		manualDomains: ['corp.example.com'],
		routes: [mockAwgRoute('awg-demo-1')],
		enabled: true,
		createdAt: '2026-05-01T10:00:00Z',
		updatedAt: '2026-05-14T20:00:00Z',
	},
	{
		id: 'dns-youtube',
		name: 'YouTube',
		domains: ['youtube.com', 'ytimg.com', 'googlevideo.com'],
		manualDomains: ['youtube.com'],
		routes: [mockAwgRoute('awg-demo-1')],
		enabled: true,
		lastDedupeReport: {
			totalInput: 19,
			totalKept: 11,
			totalRemoved: 8,
			exactDupes: 5,
			wildcardDupes: 3,
			items: [
				{ domain: 'm.youtube.com', reason: 'exact', coveredBy: 'youtube.com', listId: 'manual', listName: 'вручную' },
				{ domain: 'www.youtube.com', reason: 'exact', coveredBy: 'youtube.com', listId: 'manual', listName: 'вручную' },
				{ domain: 'i.ytimg.com', reason: 'wildcard', coveredBy: '*.ytimg.com', listId: 'yt-list', listName: 'YouTube list' },
			],
		},
		createdAt: '2026-05-02T10:00:00Z',
		updatedAt: '2026-05-14T20:00:00Z',
	},
	{
		id: 'dns-discord',
		name: 'Discord',
		domains: ['discord.com', 'discord.gg', 'discordapp.com'],
		manualDomains: ['discord.com'],
		routes: [mockAwgRoute('awg-demo-2')],
		enabled: true,
		lastDedupeReport: {
			totalInput: 52,
			totalKept: 43,
			totalRemoved: 9,
			exactDupes: 6,
			wildcardDupes: 3,
			items: [
				{ domain: 'ptb.discord.com', reason: 'wildcard', coveredBy: '*.discord.com', listId: 'dc-list', listName: 'Discord list' },
				{ domain: 'canary.discord.com', reason: 'wildcard', coveredBy: '*.discord.com', listId: 'dc-list', listName: 'Discord list' },
			],
		},
		createdAt: '2026-05-03T10:00:00Z',
		updatedAt: '2026-05-14T20:00:00Z',
	},
	{
		id: 'dns-telegram',
		name: 'Telegram',
		domains: ['telegram.org', 't.me'],
		manualDomains: ['telegram.org'],
		routes: [mockAwgRoute('awg-demo-1')],
		enabled: false,
		createdAt: '2026-05-04T10:00:00Z',
		updatedAt: '2026-05-14T20:00:00Z',
	},
	{
		id: 'dns-openai',
		name: 'OpenAI',
		domains: ['openai.com', 'chatgpt.com'],
		manualDomains: ['openai.com'],
		routes: [mockAwgRoute('awg-demo-2')],
		enabled: true,
		lastDedupeReport: {
			totalInput: 10,
			totalKept: 7,
			totalRemoved: 3,
			exactDupes: 2,
			wildcardDupes: 1,
			items: [
				{ domain: 'www.openai.com', reason: 'exact', coveredBy: 'openai.com', listId: 'manual', listName: 'вручную' },
			],
		},
		createdAt: '2026-05-05T10:00:00Z',
		updatedAt: '2026-05-14T20:00:00Z',
	},
	{
		id: 'dns-github',
		name: 'GitHub',
		domains: ['github.com', 'githubusercontent.com'],
		manualDomains: ['github.com'],
		routes: [mockAwgRoute('awg-demo-1')],
		enabled: true,
		createdAt: '2026-05-06T10:00:00Z',
		updatedAt: '2026-05-14T20:00:00Z',
	},
	{
		id: 'dns-twitter-x',
		name: 'Twitter/X',
		domains: ['twitter.com', 'x.com', 'twimg.com'],
		manualDomains: ['x.com'],
		routes: [mockAwgRoute('awg-demo-2')],
		enabled: true,
		lastDedupeReport: {
			totalInput: 24,
			totalKept: 23,
			totalRemoved: 1,
			exactDupes: 1,
			wildcardDupes: 0,
			items: [
				{ domain: 'www.x.com', reason: 'exact', coveredBy: 'x.com', listId: 'manual', listName: 'вручную' },
			],
		},
		createdAt: '2026-05-06T12:00:00Z',
		updatedAt: '2026-05-14T20:00:00Z',
	},
	{
		id: 'dns-ads-trackers',
		name: 'Реклама и трекеры',
		domains: ['geosite:category-ads-all', 'doubleclick.net', 'googlesyndication.com', 'adservice.google.com'],
		manualDomains: ['geosite:category-ads-all'],
		routes: [mockAwgRoute('awg-demo-1')],
		enabled: true,
		createdAt: '2026-05-06T13:00:00Z',
		updatedAt: '2026-05-14T20:00:00Z',
	},
	{
		id: 'dns-blocked-rf',
		name: 'Заблокировано в РФ',
		domains: ['geosite:rkn', 'instagram.com', 'facebook.com', 'twitter.com'],
		manualDomains: ['geosite:rkn'],
		routes: [mockAwgRoute('awg-demo-1')],
		enabled: true,
		createdAt: '2026-05-06T13:30:00Z',
		updatedAt: '2026-05-14T20:00:00Z',
	},
	{
		id: 'dns-unavailable-rf',
		name: 'Недоступно из РФ',
		domains: ['geosite:unavailable-in-russia', 'netflix.com', 'spotify.com', 'discord.com'],
		manualDomains: ['geosite:unavailable-in-russia'],
		routes: [mockAwgRoute('awg-demo-2')],
		enabled: true,
		createdAt: '2026-05-06T14:00:00Z',
		updatedAt: '2026-05-14T20:00:00Z',
	},
	// HydraRoute Neo — ids match production (`hr:RuleName`), files are SoT on device.
	{
		id: 'hr:Youtube',
		name: 'Youtube',
		domains: ['youtube.com', 'ytimg.com'],
		manualDomains: ['youtube.com', 'ytimg.com'],
		hrRouteMode: 'policy',
		hrPolicyName: 'HydraRoute',
		routes: [mockAwgRoute('awg-demo-1')],
		enabled: true,
		backend: 'hydraroute',
		createdAt: '2026-05-07T10:00:00Z',
		updatedAt: '2026-05-14T20:00:00Z',
	},
	{
		id: 'hr:Netflix',
		name: 'Netflix',
		domains: ['netflix.com'],
		manualDomains: ['netflix.com'],
		hrRouteMode: 'policy',
		hrPolicyName: 'HydraRoute',
		routes: [mockAwgRoute('awg-demo-2')],
		enabled: false,
		backend: 'hydraroute',
		createdAt: '2026-05-08T10:00:00Z',
		updatedAt: '2026-05-14T20:00:00Z',
	},
	{
		id: 'hr:Discord',
		name: 'Discord',
		domains: ['discord.com', 'discord.gg'],
		manualDomains: ['discord.com', 'discord.gg'],
		hrRouteMode: 'interface',
		routes: [{ interface: 'awg0', tunnelId: 'awg-demo-1', fallback: 'auto' }],
		enabled: true,
		backend: 'hydraroute',
		createdAt: '2026-05-09T10:00:00Z',
		updatedAt: '2026-05-14T20:00:00Z',
	},
	{
		id: 'hr:OpenAI',
		name: 'OpenAI',
		domains: ['openai.com', 'chatgpt.com'],
		manualDomains: ['openai.com', 'chatgpt.com'],
		hrRouteMode: 'policy',
		hrPolicyName: 'HydraRoute',
		routes: [mockAwgRoute('awg-demo-2')],
		enabled: true,
		backend: 'hydraroute',
		createdAt: '2026-05-10T10:00:00Z',
		updatedAt: '2026-05-14T20:00:00Z',
	},
	{
		id: 'hr:Константинопольские',
		name: 'Константинопольские сервисы',
		domains: ['example-long-name.local'],
		manualDomains: ['example-long-name.local'],
		hrRouteMode: 'policy',
		hrPolicyName: 'HydraRoute',
		routes: [mockAwgRoute('awg-demo-1')],
		enabled: true,
		backend: 'hydraroute',
		createdAt: '2026-05-11T10:00:00Z',
		updatedAt: '2026-05-14T20:00:00Z',
	},
	{
		id: 'hr:Telegram',
		name: 'Telegram',
		domains: ['telegram.org', 't.me'],
		subnets: ['91.108.4.0/22'],
		manualDomains: ['telegram.org', 't.me', '91.108.4.0/22'],
		hrRouteMode: 'interface',
		routes: [{ interface: 'OpkgTun0', tunnelId: 'awg-demo-2', fallback: 'auto' }],
		enabled: true,
		backend: 'hydraroute',
		createdAt: '2026-05-12T10:00:00Z',
		updatedAt: '2026-05-14T20:00:00Z',
	},
	{
		id: 'hr:GitHub',
		name: 'GitHub',
		domains: ['github.com'],
		manualDomains: ['github.com'],
		hrRouteMode: 'policy',
		hrPolicyName: 'HydraRoute',
		routes: [mockAwgRoute('awg-demo-1')],
		enabled: true,
		backend: 'hydraroute',
		createdAt: '2026-05-13T10:00:00Z',
		updatedAt: '2026-05-14T20:00:00Z',
	},
	{
		id: 'hr:Twitch',
		name: 'Twitch',
		domains: ['twitch.tv'],
		manualDomains: ['twitch.tv'],
		hrRouteMode: 'interface',
		routes: [{ interface: 'OpkgTun0', tunnelId: 'awg-demo-2', fallback: 'auto' }],
		enabled: true,
		backend: 'hydraroute',
		createdAt: '2026-05-14T10:00:00Z',
		updatedAt: '2026-05-14T20:00:00Z',
	},
];

const mockStaticRoutes = [
	{
		id: 'ip-office-subnets',
		name: 'Office subnets',
		tunnelID: 'awg-demo-1',
		subnets: ['10.10.0.0/16', '10.10.12.0/24'],
		fallback: '',
		enabled: true,
		createdAt: '2026-05-01T09:00:00Z',
		updatedAt: '2026-05-14T20:00:00Z',
	},
	{
		id: 'ip-dev-lab',
		name: 'Dev Lab',
		tunnelID: 'awg-demo-2',
		subnets: ['10.20.0.0/16', '10.21.0.0/16', '10.20.24.0/24'],
		fallback: '',
		enabled: true,
		createdAt: '2026-05-02T09:00:00Z',
		updatedAt: '2026-05-14T20:00:00Z',
	},
	{
		id: 'ip-nas-backup',
		name: 'NAS Backup',
		tunnelID: 'awg-demo-1',
		subnets: ['172.16.50.0/24', '172.16.51.0/24'],
		fallback: 'reject',
		enabled: false,
		createdAt: '2026-05-03T09:00:00Z',
		updatedAt: '2026-05-14T20:00:00Z',
	},
	{
		id: 'ip-iot-zone',
		name: 'IoT Zone',
		tunnelID: 'awg-demo-2',
		subnets: ['192.168.50.0/24', '192.168.50.128/25'],
		fallback: '',
		enabled: true,
		createdAt: '2026-05-04T09:00:00Z',
		updatedAt: '2026-05-14T20:00:00Z',
	},
	{
		id: 'ip-streaming-cdn',
		name: 'Streaming CDN',
		tunnelID: 'awg-demo-1',
		subnets: ['45.67.0.0/16', '45.67.18.0/24', '45.67.18.0/24'],
		fallback: 'bypass',
		enabled: true,
		createdAt: '2026-05-05T09:00:00Z',
		updatedAt: '2026-05-14T20:00:00Z',
	},
	{
		id: 'ip-gaming-latency',
		name: 'Gaming Low-Latency',
		tunnelID: 'awg-demo-2',
		subnets: ['31.13.64.0/18', '157.240.0.0/16'],
		fallback: 'drop',
		enabled: true,
		createdAt: '2026-05-06T09:00:00Z',
		updatedAt: '2026-05-14T20:00:00Z',
	},
	{
		id: 'ip-partner-extranet',
		name: 'Partner Extranet',
		tunnelID: 'direct',
		subnets: ['203.0.113.0/24', '198.51.100.0/24'],
		fallback: 'bypass',
		enabled: false,
		createdAt: '2026-05-07T09:00:00Z',
		updatedAt: '2026-05-14T20:00:00Z',
	},
	{
		id: 'ip-legacy-overlap',
		name: 'Legacy overlap',
		tunnelID: 'awg-demo-1',
		subnets: ['10.10.0.0/16', '10.10.100.0/24', '10.10.101.0/24'],
		fallback: '',
		enabled: true,
		createdAt: '2026-05-08T09:00:00Z',
		updatedAt: '2026-05-14T20:00:00Z',
	},
];

const mockClientRoutes = [
	{
		id: 'client-my-phone',
		clientIp: '192.168.1.100',
		clientHostname: 'My-Phone',
		tunnelId: 'awg-demo-1',
		fallback: 'drop',
		enabled: true,
	},
	{
		id: 'client-work-laptop',
		clientIp: '192.168.1.110',
		clientHostname: 'Work-Laptop',
		tunnelId: 'awg-demo-2',
		fallback: 'bypass',
		enabled: true,
	},
	{
		id: 'client-tv-box',
		clientIp: '192.168.1.120',
		clientHostname: 'TV-Box',
		tunnelId: 'awg-demo-1',
		fallback: 'drop',
		enabled: true,
	},
	{
		id: 'client-guest-tablet',
		clientIp: '192.168.1.130',
		clientHostname: 'Guest-Tablet',
		tunnelId: 'awg-demo-2',
		fallback: 'bypass',
		enabled: false,
	},
];

// Built on demand in handlers so policy mutations and tunnel status changes
// stay visible without restarting the mock proxy.
const LEGACY_POLICY_INTERFACE_FIXTURES = [
	// Preserve explicit offline legacy edge case that is not part of tunnel catalogs.
	{ name: 'NL vless-grpc', label: 'NL', up: false, source: 'legacy-fixture' },
];

const mockSingboxRules = [
	{ action: 'sniff' },
	{ action: 'hijack-dns', protocol: 'dns' },
	// system bypass — render as BYPASS chip; long matcher summary that
	// triggers issue #214 narrow-viewport wrap problem.
	{ ip_is_private: true, outbound: 'direct' },
	{ action: 'route', domain_suffix: ['youtube.com', 'ytimg.com'], outbound: 'sub-demo0001' },
	{ action: 'route', rule_set: ['geosite-openai'], outbound: 'sub-demo0001' },
	{ action: 'route', rule_set: ['inline-neo-demo'], outbound: 'sub-demo0001' },
	{ action: 'route', rule_set: ['geosite-google'], outbound: 'sub-demo0001' },
	{ action: 'route', rule_set: ['geosite-discord'], outbound: 'manual-eu' },
	{ action: 'route', domain_suffix: ['netflix.com'], outbound: 'Kto-VLESS-kto-po-drova' },
	{ action: 'route', domain_suffix: ['spotify.com'], outbound: 'awg-vpn0' },
	// Персональная привязка устройства (Устройства-чип): source_ip_cidr →
	// outbound. Work-Mac (192.168.1.47) → manual-eu, рендерит «персональный».
	{ action: 'route', source_ip_cidr: ['192.168.1.47/32'], outbound: 'manual-eu' },
	{ action: 'route', rule_set: ['geosite-github'], outbound: 'sub-bigprov' },
	{ action: 'route', rule_set: ['local-ads-block'], outbound: 'direct' },
	{ action: 'route', domain_suffix: ['github.com'], outbound: 'direct' },
	{ action: 'reject', domain: ['vkvideo.ru', 'long-host.example.com'], rule_set: ['geosite-category-ads-all', 'geosite-youtube'] },
];

const mockSingboxRuleSets = [
	{ tag: 'geosite-cn', type: 'remote', format: 'binary', url: 'https://cdn.example.com/geosite-cn.srs', update_interval: '24h', download_detour: 'direct' },
	{ tag: 'geosite-youtube', type: 'remote', format: 'binary', url: 'https://cdn.example.com/geosite-youtube.srs', update_interval: '24h', download_detour: 'direct' },
	{ tag: 'geosite-openai', type: 'remote', format: 'binary', url: 'https://cdn.example.com/geosite-openai.srs', update_interval: '24h', download_detour: 'direct' },
	{ tag: 'geosite-discord', type: 'remote', format: 'binary', url: 'https://cdn.example.com/geosite-discord.srs', update_interval: '24h', download_detour: 'direct' },
	{ tag: 'geosite-github', type: 'remote', format: 'binary', url: 'https://cdn.example.com/geosite-github.srs', update_interval: '24h', download_detour: 'direct' },
	{ tag: 'geoip-ru', type: 'remote', format: 'binary', url: 'https://cdn.example.com/geoip-ru.srs', update_interval: '24h', download_detour: 'direct' },
	{
		tag: 'geosite-google',
		type: 'remote',
		format: 'binary',
		url: 'http://127.0.0.1:8081/api/singbox/router/rulesets/dat-srs?kind=geosite&tag=GOOGLE&tag=GEMINI',
		update_interval: '24h',
	},
	{
		tag: 'geoip-ru-dat',
		type: 'remote',
		format: 'binary',
		url: 'http://127.0.0.1:8081/api/singbox/router/rulesets/dat-srs?kind=geoip&tag=RU',
		update_interval: '24h',
	},
	{
		tag: 'local-ads-block',
		type: 'local',
		format: 'binary',
		path: '/opt/etc/sing-box/rule-sets/ads.srs',
	},
	{
		tag: 'inline-neo-demo',
		type: 'inline',
		rules: [
			{
				domain: ['claude.ai'],
				domain_suffix: [
					'chatgpt.com',
					'openai.com',
					'gemini.google.com',
					'xn--h1aaemethbj4a4h.xn--p1ai',
					'.perplexity.ai',
					'deepseek.com',
				],
				domain_keyword: ['youtube'],
			},
			{ ip_cidr: ['1.1.1.1/32', '8.8.8.0/24'] },
		],
	},
];

const mockSingboxPresets = [
	{ id: 'preset-youtube', name: 'YouTube', category: 'media', iconSlug: 'youtube', ruleSets: [{ tag: 'geosite-youtube', url: 'https://cdn.example.com/geosite-youtube.srs' }], rules: [{ ruleSetRef: 'geosite-youtube', actionTarget: 'tunnel' }] },
	{ id: 'preset-discord', name: 'Discord', category: 'social', iconSlug: 'discord', ruleSets: [{ tag: 'geosite-discord', url: 'https://cdn.example.com/geosite-discord.srs' }], rules: [{ ruleSetRef: 'geosite-discord', actionTarget: 'tunnel' }] },
	{ id: 'preset-openai', name: 'OpenAI', category: 'ai', iconSlug: 'openai', ruleSets: [{ tag: 'geosite-openai', url: 'https://cdn.example.com/geosite-openai.srs' }], rules: [{ ruleSetRef: 'geosite-openai', actionTarget: 'tunnel' }] },
	{ id: 'preset-github', name: 'GitHub', category: 'developer', iconSlug: 'github', ruleSets: [{ tag: 'geosite-github', url: 'https://cdn.example.com/geosite-github.srs' }], rules: [{ ruleSetRef: 'geosite-github', actionTarget: 'direct' }] },
	{ id: 'preset-telegram', name: 'Telegram', category: 'social', iconSlug: 'telegram', ruleSets: [{ tag: 'geosite-telegram', url: 'https://cdn.example.com/geosite-telegram.srs' }], rules: [{ ruleSetRef: 'geosite-telegram', actionTarget: 'tunnel' }] },
	{ id: 'preset-twitter', name: 'Twitter/X', category: 'social', iconSlug: 'x', ruleSets: [{ tag: 'geosite-twitter', url: 'https://cdn.example.com/geosite-twitter.srs' }], rules: [{ ruleSetRef: 'geosite-twitter', actionTarget: 'tunnel' }] },
	{ id: 'preset-netflix', name: 'Netflix', category: 'media', iconSlug: 'netflix', ruleSets: [{ tag: 'geosite-netflix', url: 'https://cdn.example.com/geosite-netflix.srs' }], rules: [{ ruleSetRef: 'geosite-netflix', actionTarget: 'tunnel' }] },
	{ id: 'preset-gemini', name: 'Google Gemini', category: 'ai', iconSlug: 'gemini', ruleSets: [{ tag: 'geosite-gemini', url: 'https://cdn.example.com/geosite-gemini.srs' }], rules: [{ ruleSetRef: 'geosite-gemini', actionTarget: 'tunnel' }] },
	{ id: 'preset-anthropic', name: 'Anthropic', category: 'ai', iconSlug: 'anthropic', ruleSets: [{ tag: 'geosite-anthropic', url: 'https://cdn.example.com/geosite-anthropic.srs' }], rules: [{ ruleSetRef: 'geosite-anthropic', actionTarget: 'tunnel' }] },
];
const FAKE_INSTALL_STDERR = `Collected errors:
 * verify_pkg_installable: Only have 12 KB available on filesystem /opt, pkg sing-box needs 18432
 * opkg_install_cmd: Cannot install package sing-box.
opkg_install_cmd: failed.
exit code 255`;

// ── Managed WG server fixture (peer sort UI test) ──────────────
// Mirrors a realistic 11-client server. IPs are deliberately not in
// the storage order, and span 10.0.0.2..10.0.0.13 so lexicographic
// vs numeric sort diverge ("10.0.0.10" < "10.0.0.2" as strings).
const MANAGED_PEERS_FIXTURE = [
	{ ip: '10.0.0.5',  name: 'Macbook Pro' },
	{ ip: '10.0.0.7',  name: 'iPhone',          rx: 118213447, tx: 6973850, handshakeMinAgo: 2, endpoint: '192.168.2.101:51515' },
	{ ip: '10.0.0.8',  name: 'iPad' },
	{ ip: '10.0.0.9',  name: 'Office laptop' },
	{ ip: '10.0.0.10', name: 'NUC' },
	{ ip: '10.0.0.11', name: 'TV box' },
	{ ip: '10.0.0.12', name: 'Reserve phone',   rx: 9326 },
	{ ip: '10.0.0.2',  name: 'Server one' },
	{ ip: '10.0.0.3',  name: 'Server two' },
	{ ip: '10.0.0.4',  name: 'Server three' },
	{ ip: '10.0.0.13', name: 'Guest device' },
];

const SYSTEM_SERVER_PEERS_FIXTURE = [
	{ ip: '10.0.1.2', name: 'Phone 15 Pro', endpoint: '95.64.154.50:50412', rx: 182_440_112, tx: 34_207_991, handshakeMinAgo: 1, enabled: true },
	{ ip: '10.0.1.3', name: 'MacBook-Pro-13', endpoint: '178.176.80.12:54822', rx: 96_882_301, tx: 22_914_004, handshakeMinAgo: 3, enabled: true },
	{ ip: '10.0.1.4', name: 'Windows-Workstation', endpoint: '0.0.0.0:0', rx: 0, tx: 0, enabled: true },
	{ ip: '10.0.1.5', name: 'Android-Tablet', endpoint: '188.17.9.22:61234', rx: 24_331_554, tx: 7_220_118, handshakeMinAgo: 14, enabled: true },
	{ ip: '10.0.1.6', name: 'Smart TV LG', endpoint: '100.64.2.8:53001', rx: 512_441_223, tx: 61_441_002, handshakeMinAgo: 120, enabled: true },
	{ ip: '10.0.1.7', name: 'Synology NAS', endpoint: '0.0.0.0:0', rx: 1_522_112, tx: 211_334, enabled: true },
	{ ip: '10.0.1.8', name: 'PS5', endpoint: '85.113.41.10:41999', rx: 72_556_801, tx: 11_772_900, handshakeMinAgo: 40, enabled: true },
	{ ip: '10.0.1.9', name: 'MikroTik-Lab', endpoint: '203.0.113.18:51820', rx: 8_211_402, tx: 4_115_721, handshakeMinAgo: 9, enabled: true },
	{ ip: '10.0.1.10', name: 'Guest-iPad', endpoint: '0.0.0.0:0', rx: 0, tx: 0, enabled: false },
	{ ip: '10.0.1.11', name: 'Home-Assistant', endpoint: '192.0.2.77:55000', rx: 17_030_221, tx: 2_002_144, handshakeMinAgo: 300, enabled: true },
];

function mockPubkey(i) {
	// 43 chars + '=' → 44, matches real WG pubkey length so any UI truncation behaves realistically.
	return `MOCK${String(i).padStart(2, '0')}${'A'.repeat(37)}=`;
}

function mockZeroASC() {
	return {
		jc: 0,
		jmin: 0,
		jmax: 0,
		s1: 0,
		s2: 0,
		h1: '',
		h2: '',
		h3: '',
		h4: '',
	};
}

function mockFilledASC() {
	return {
		jc: 3,
		jmin: 77,
		jmax: 266,
		s1: 18,
		s2: 29,
		h1: '103994526',
		h2: '1201929360',
		h3: '2403636727',
		h4: '3602647725',
		s3: 12,
		s4: 9,
		i1: 'sig1',
		i2: 'sig2',
		i3: 'sig3',
		i4: 'sig4',
		i5: 'sig5',
	};
}

/** Wireguard0 — второй managed-сервер в rail («AWGM ASC»). */
const MOCK_ASC_SERVER_ID = 'Wireguard0';

function createInitialMockManagedAscByServer() {
	return {
		[MOCK_ASC_SERVER_ID]: mockFilledASC(),
		Wireguard1: mockZeroASC(),
	};
}

let mockManagedAscByServer = createInitialMockManagedAscByServer();

const MOCK_KEENETIC_PROFILES = {
	'5.0': {
		key: '5.0',
		extended: false,
		keeneticOS: '5.0',
		firmwareVersion: '5.0.11 (5.00.C.11.0-0)',
		firmwareRelease: '5.0.11 (5.00.C.11.0-0)',
		isOS5: true,
	},
	'5.1': {
		key: '5.1',
		extended: true,
		keeneticOS: '5.1',
		firmwareVersion: '5.1.0 (5.01.A.0-0)',
		firmwareRelease: '5.1.0 (5.01.A.0-0)',
		isOS5: true,
	},
};

const DEFAULT_MOCK_KEENETIC_OS = '5.1';

function resolveMockKeeneticProfileKey() {
	const forced = process.env.MOCK_KEENETIC_OS?.trim();
	if (forced === '5.0' || forced === '5.1') return forced;
	return DEFAULT_MOCK_KEENETIC_OS;
}

function setMockKeeneticProfileKey(key) {
	mockKeeneticProfile = { ...MOCK_KEENETIC_PROFILES[key] };
	return mockKeeneticProfile;
}

function applyDefaultMockKeeneticProfile() {
	const profile = setMockKeeneticProfileKey(resolveMockKeeneticProfileKey());
	console.log(
		`[mock-proxy] KeeneticOS ${profile.key} — supportsExtendedASC=${profile.extended}`,
	);
	return profile;
}

/** Current mock firmware profile; switch via POST /__mock/keenetic-os or mock reset. */
let mockKeeneticProfile = { ...MOCK_KEENETIC_PROFILES[DEFAULT_MOCK_KEENETIC_OS] };
applyDefaultMockKeeneticProfile();

function applyMockKeeneticProfile(data) {
	data.keeneticOS = mockKeeneticProfile.keeneticOS;
	data.firmwareVersion = mockKeeneticProfile.firmwareVersion;
	data.isOS5 = mockKeeneticProfile.isOS5;
	data.supportsExtendedASC = mockKeeneticProfile.extended;
	data.supportsHRanges = mockKeeneticProfile.extended;
	if (data.routerDetails && typeof data.routerDetails === 'object') {
		data.routerDetails.firmwareRelease = mockKeeneticProfile.firmwareRelease;
	}
}

function shapeASCForMockProfile(params) {
	if (!params || typeof params !== 'object') return mockZeroASC();
	if (mockKeeneticProfile.extended) return { ...params };
	return {
		jc: params.jc ?? 0,
		jmin: params.jmin ?? 0,
		jmax: params.jmax ?? 0,
		s1: params.s1 ?? 0,
		s2: params.s2 ?? 0,
		h1: params.h1 ?? '',
		h2: params.h2 ?? '',
		h3: params.h3 ?? '',
		h4: params.h4 ?? '',
	};
}

function createInitialMockSystemAscByTunnel() {
	return {
		Wireguard6: mockFilledASC(),
		Wireguard7: mockZeroASC(),
	};
}

let mockSystemAscByTunnel = createInitialMockSystemAscByTunnel();

function mockManagedServer() {
	return {
		interfaceName: 'Wireguard1',
		description: 'Default WG',
		address: '10.0.0.1',
		mask: '255.255.255.0',
		listenPort: 51821,
		endpoint: '203.0.113.42:51821',
		dns: '8.8.8.8',
		mtu: 1420,
		natEnabled: true,
		policy: 'Policy0',
		peers: MANAGED_PEERS_FIXTURE.map((p, i) => ({
			publicKey: mockPubkey(i + 1),
			privateKey: '',
			presharedKey: '',
			description: p.name,
			tunnelIP: `${p.ip}/32`,
			dns: '',
			enabled: true,
		})),
	};
}

function mockManagedSystemServer() {
	return {
		interfaceName: 'Wireguard0',
		description: 'AWGM ASC',
		address: '10.0.1.1',
		mask: '255.255.255.0',
		listenPort: 51820,
		endpoint: '',
		dns: '',
		mtu: 1420,
		natEnabled: true,
		policy: 'none',
		privateKey: 'AgICAgICAgICAgICAgICAgICAgICAgICAgICAgICAgI=',
		peers: SYSTEM_SERVER_PEERS_FIXTURE.map((p, i) => ({
			publicKey: mockPubkey(50 + i),
			privateKey: '',
			presharedKey: '',
			description: p.name,
			tunnelIP: `${p.ip}/32`,
			dns: '',
			enabled: p.enabled !== false,
		})),
	};
}

function mockManagedStats() {
	const now = Date.now();
	return {
		status: 'up',
		peers: MANAGED_PEERS_FIXTURE.map((p, i) => {
			const online = p.handshakeMinAgo !== undefined;
			return {
				publicKey: mockPubkey(i + 1),
				endpoint: p.endpoint ?? '0.0.0.0:0',
				rxBytes: p.rx ?? 0,
				txBytes: p.tx ?? 0,
				lastHandshake: online ? new Date(now - p.handshakeMinAgo * 60_000).toISOString() : '',
				online,
			};
		}),
	};
}

function mockManagedSystemStats() {
	return {
		status: 'up',
		peers: mockSystemServerPeers(),
	};
}

function buildMockServersAllData() {
	return {
		servers: mockSystemServers(),
		managed: [mockManagedServer(), mockManagedSystemServer()],
		managedStats: {
			Wireguard1: mockManagedStats(),
			Wireguard0: mockManagedSystemStats(),
		},
	};
}

/** @type {Record<string, { natMode: 'full' | 'internet-only' | 'none', policy: string, endpoint?: string }>} */
const mockSystemServerSettings = {
	Wireguard0: { natMode: 'full', policy: 'none', endpoint: '' },
	Wireguard9: { natMode: 'none', policy: 'Policy0', endpoint: '' },
};

/** @type {Map<string, { privateKey: string, presharedKey: string, description: string, tunnelIP: string }>} */
const mockSystemPeerSecrets = new Map();
mockSystemPeerSecrets.set(mockPubkey(50), {
	privateKey: mockPubkey(150),
	presharedKey: mockPubkey(250),
	description: 'Phone',
	tunnelIP: '10.0.14.2/32',
});

function mockSystemServerPeers() {
	const now = Date.now();
	return SYSTEM_SERVER_PEERS_FIXTURE.map((p, i) => {
		const online = p.handshakeMinAgo !== undefined;
		return {
			publicKey: mockPubkey(50 + i),
			description: p.name,
			endpoint: p.endpoint ?? '0.0.0.0:0',
			allowedIPs: [`${p.ip}/32`],
			rxBytes: p.rx ?? 0,
			txBytes: p.tx ?? 0,
			lastHandshake: online ? new Date(now - p.handshakeMinAgo * 60_000).toISOString() : '',
			online,
			enabled: p.enabled !== false,
		};
	});
}

function mockSystemServers() {
	const builtinPeers = mockSystemServerPeers().map((p, i) => ({
		...p,
		confAvailable: i === 0 || mockSystemPeerSecrets.has(p.publicKey),
	}));
	const wg0 = mockSystemServerSettings.Wireguard0 ?? { natMode: 'full', policy: 'none' };
	const wg9 = mockSystemServerSettings.Wireguard9 ?? { natMode: 'none', policy: 'none' };
	return [
		{
			id: 'Wireguard0',
			interfaceName: 'nwg0',
			description: 'Wireguard VPN Server',
			builtIn: true,
			status: 'up',
			connected: true,
			mtu: 1420,
			address: '10.0.14.88',
			mask: '255.255.255.0',
			publicKey: mockPubkey(41),
			listenPort: 8443,
			natEnabled: wg0.natMode === 'full',
			natMode: wg0.natMode,
			policy: wg0.policy,
			endpoint: wg0.endpoint ?? '',
			keenDnsDomain: 'demo.keenetic.pro',
			peers: builtinPeers,
		},
		{
			id: 'Wireguard9',
			interfaceName: 'nwg9',
			description: 'Branch Office WG',
			status: 'down',
			connected: false,
			mtu: 1420,
			address: '10.9.0.1',
			mask: '255.255.255.0',
			publicKey: mockPubkey(42),
			listenPort: 53199,
			natEnabled: wg9.natMode === 'full',
			natMode: wg9.natMode,
			policy: wg9.policy,
			peers: [
				{
					publicKey: mockPubkey(60),
					description: 'Branch-Router',
					endpoint: '0.0.0.0:0',
					allowedIPs: ['10.9.0.2/32'],
					rxBytes: 0,
					txBytes: 0,
					lastHandshake: '',
					online: false,
					enabled: true,
					confAvailable: false,
				},
			],
		},
	];
}

async function fetchJSON(path, init) {
	const base = String(UPSTREAM || '').trim().replace(/\/+$/, '');
	const suffix = String(path || '').trim();
	const normalizedPath = suffix.startsWith('/') ? suffix : `/${suffix}`;
	const r = await fetch(`${base}${normalizedPath}`, init);
	const text = await r.text();
	try {
		return { status: r.status, body: JSON.parse(text) };
	} catch {
		return { status: r.status, body: text };
	}
}

function send(res, status, body, contentType = 'application/json') {
	res.writeHead(status, { 'Content-Type': contentType });
	res.end(typeof body === 'string' ? body : JSON.stringify(body));
}

function sendData(res, data, status = 200) {
	send(res, status, { success: true, data });
}

function sendInvalidRequest(res, error, status = 400) {
	send(res, status, {
		success: false,
		error: { code: 'INVALID_REQUEST', message: String(error) },
	});
}

function wait(ms) {
	return new Promise((resolveWait) => setTimeout(resolveWait, ms));
}

// Endpoints that perform a "service download" through the configurable route.
// `style: 'envelope'` → backend error shape {error, message, code} @ 400.
// `style: 'updateInfo'` → /system/update/check returns 200 with data.error.
const DOWNLOAD_FAULT_ROUTES = [
	{ method: 'POST', path: '/hydraroute/geo-files/update', style: 'envelope', code: 'GEO_UPDATE_ERROR' },
	{ method: 'POST', path: '/hydraroute/geo-files/add', style: 'envelope', code: 'GEO_DOWNLOAD_ERROR' },
	{ method: 'POST', path: '/dns-routes/refresh', style: 'envelope', code: 'DNS_ROUTE_REFRESH_ERROR' },
	{ method: 'POST', path: '/amnezia-premium/login', style: 'envelope', code: 'AMNEZIA_CP_NETWORK' },
	{ method: 'POST', path: '/amnezia-premium/account-info', style: 'envelope', code: 'AMNEZIA_CP_NETWORK' },
	{ method: 'POST', path: '/amnezia-premium/download-config', style: 'envelope', code: 'AMNEZIA_CP_NETWORK' },
	{ method: 'POST', path: '/singbox/install', style: 'envelope', code: 'SINGBOX_INSTALL_ERROR' },
	{ method: 'POST', path: '/singbox/update', style: 'envelope', code: 'SINGBOX_UPDATE_ERROR' },
	// NB: /download/outbounds is route *discovery*, not a download — never fault
	// it, otherwise the route dropdown randomly empties.
	{ method: 'GET', path: '/system/update/check', style: 'updateInfo' },
];

// One realistic message per humanized failure kind (see downloadError.ts):
// sing-box off, AWG interface down, timeout, network drop, generic.
const DOWNLOAD_FAULT_MESSAGES = [
	'outbound "sb-subscription-1" is unavailable: sing-box is not running',
	'outbound "awg-de-frankfurt" interface "awg0" is not present',
	'download via DE Frankfurt: request failed: context deadline exceeded (timed out)',
	'download via Direct (WAN): request failed: http get: EOF',
	'remote returned HTTP 500: internal server error',
];

// Randomly short-circuit a service-download endpoint with a failure so the UI
// can run through all outcomes. Returns true when a fault was written (caller
// must stop), false to continue with the normal/proxied success path.
function maybeInjectDownloadFault(req, res, path) {
	if (!downloadFaultsEnabled) return false;
	const route = DOWNLOAD_FAULT_ROUTES.find((r) => r.method === req.method && r.path === path);
	if (!route) return false;
	if (Math.random() >= downloadFaultProbability) return false;

	// Drain any request body so keep-alive sockets don't stall.
	if (req.method !== 'GET' && req.method !== 'HEAD') req.resume();

	const message = DOWNLOAD_FAULT_MESSAGES[Math.floor(Math.random() * DOWNLOAD_FAULT_MESSAGES.length)];
	if (route.style === 'updateInfo') {
		send(res, 200, {
			success: true,
			data: {
				currentVersion: 'dev',
				latestVersion: '',
				available: false,
				channel: updateChannel,
				error: message,
			},
		});
	} else {
		send(res, 400, { error: true, message, code: route.code });
	}
	console.log(`[mock-proxy] injected download fault on ${req.method} ${path}: ${message}`);
	return true;
}

function readRequestText(req) {
	return new Promise((resolveRead, rejectRead) => {
		let raw = '';
		req.on('data', (chunk) => (raw += chunk));
		req.on('end', () => resolveRead(raw));
		req.on('error', rejectRead);
	});
}

async function readJsonBody(req, fallback = {}) {
	const raw = await readRequestText(req);
	return JSON.parse(raw || JSON.stringify(fallback));
}

function buildMockCapabilities() {
	return {
		name: 'awg-manager mock-proxy',
		upstream: UPSTREAM,
		port: PORT,
		state: getRuntimeState(),
		capabilities: MOCK_CAPABILITY_GROUPS,
		fixtures: {
			tunnelCatalog: buildMockTunnelCatalog().length,
			downloadOutbounds: buildDownloadOutbounds().length,
			awgTunnels: MOCK_AWG_TUNNELS.length,
			systemTunnels: MOCK_SYSTEM_TUNNELS.length,
			singboxTunnels: MOCK_SINGBOX_TUNNELS.length,
			subscriptions: mockSubscriptions.length,
			proxyInstances: mockProxyInstances.length,
		},
	};
}


function awgTunnelByID(id) {
	return MOCK_AWG_TUNNELS.find((t) => t.id === id) ?? null;
}

function awgRouteInterface(id) {
	return awgTunnelByID(id)?.interfaceName ?? id;
}

function mockAwgRoute(id, fallback = 'auto') {
	return { interface: awgRouteInterface(id), tunnelId: id, fallback };
}

function findMockDnsRoute(id) {
	const idx = mockRoutingDnsRoutes.findIndex((r) => r.id === id);
	if (idx < 0) return null;
	return { idx, route: mockRoutingDnsRoutes[idx] };
}

function touchMockDnsRoute(route) {
	route.updatedAt = new Date().toISOString();
}

function mergeMockDnsRoute(route, patch) {
	if (patch.name != null) route.name = patch.name;
	if (patch.domains != null) route.domains = patch.domains;
	if (patch.subnets != null) route.subnets = patch.subnets;
	if (patch.manualDomains != null) route.manualDomains = patch.manualDomains;
	if (patch.routes != null) route.routes = patch.routes;
	if (patch.enabled != null) route.enabled = patch.enabled !== false;
	if (patch.backend != null) route.backend = patch.backend;
	if (patch.hrRouteMode != null) route.hrRouteMode = patch.hrRouteMode;
	if (patch.hrPolicyName != null) route.hrPolicyName = patch.hrPolicyName;
	if (patch.hrPolicyInterfaces != null) route.hrPolicyInterfaces = patch.hrPolicyInterfaces;
	if (patch.iconUrl != null) route.iconUrl = patch.iconUrl;
	touchMockDnsRoute(route);
	return route;
}

function mockPolicyInterfaceRef(id, order = 0) {
	const tunnel = awgTunnelByID(id);
	if (!tunnel) return { name: id, label: id, order };
	return { name: tunnel.interfaceName, label: tunnel.name || tunnel.interfaceName, order };
}

function getConnectionTunnel(index) {
	const candidates = MOCK_AWG_TUNNELS.filter((t) => t.enabled !== false && t.status === 'running');
	if (candidates.length === 0) return MOCK_AWG_TUNNELS[0] ?? null;
	return candidates[index % candidates.length];
}

function buildMockTunnelCatalog() {
	const direct = {
		id: 'direct',
		kind: 'direct',
		name: 'Direct',
		label: 'Direct (WAN)',
		iface: 'eth3',
		status: 'up',
		available: true,
	};

	const awg = MOCK_AWG_TUNNELS.map((t) => ({
		id: t.id,
		tag: t.id,
		kind: 'awg',
		name: t.name,
		label: t.name,
		iface: t.interfaceName,
		ndmsName: t.ndmsName,
		endpoint: t.endpoint,
		status: t.status,
		running: t.status === 'running',
		available: t.enabled !== false && t.status === 'running',
		defaultRoute: !!t.defaultRoute,
		source: 'MOCK_AWG_TUNNELS',
	}));

	const system = MOCK_SYSTEM_TUNNELS.map((t) => ({
		id: t.id,
		tag: t.id,
		kind: 'system-wg',
		name: t.description || t.id,
		label: t.description || t.interfaceName || t.id,
		iface: t.interfaceName,
		endpoint: t.peer?.endpoint ?? '',
		status: t.status,
		running: t.status === 'up',
		available: t.connected !== false && t.status === 'up',
		source: 'MOCK_SYSTEM_TUNNELS',
	}));

	const singbox = MOCK_SINGBOX_TUNNELS.map((t) => ({
		id: t.tag,
		tag: t.tag,
		kind: 'singbox',
		protocol: t.protocol,
		name: t.tag,
		label: t.tag,
		iface: t.proxyInterface,
		endpoint: `${t.server}:${t.port}`,
		status: t.running ? 'running' : 'stopped',
		running: !!t.running,
		available: !!t.running && t.connectivity?.connected !== false,
		source: 'MOCK_SINGBOX_TUNNELS',
	}));

	return [direct, ...awg, ...system, ...singbox];
}

function buildDownloadOutbounds() {
	const out = [];
	const seen = new Set();
	const add = (outbound) => {
		if (!outbound?.tag || seen.has(outbound.tag)) return;
		seen.add(outbound.tag);
		out.push({ ...outbound });
	};

	for (const outbound of MOCK_DOWNLOAD_OUTBOUNDS) {
		add(outbound);
	}

	add({
		tag: 'direct',
		kind: 'direct',
		label: 'Direct (WAN)',
		detail: 'без туннеля',
		available: true,
	});

	for (const t of MOCK_AWG_TUNNELS) {
		add({
			tag: t.id,
			kind: 'awg',
			label: t.name,
			// detail = kernel iface (backend bind_interface), not endpoint metadata
			detail: t.interfaceName ?? '',
			available: t.enabled !== false && !!t.interfaceName,
		});
	}

	for (const t of MOCK_SINGBOX_TUNNELS) {
		add({
			tag: t.tag,
			kind: 'singbox',
			label: t.tag,
			detail: `${t.protocol?.toUpperCase() ?? 'TUNNEL'} · ${t.server}:${t.port}`,
			available: !!t.running && t.listenPort > 0,
		});
	}

	return out;
}

function buildConnectionsTunnelSummary(stats) {
	const directCount = Math.max(0, Number(stats?.direct) || 0);
	const tunnels = {
		'': { name: 'Direct', interface: 'eth3', count: directCount },
	};

	for (const tunnel of MOCK_AWG_TUNNELS) {
		tunnels[tunnel.id] = {
			name: tunnel.name,
			interface: tunnel.interfaceName ?? '—',
			count: MOCK_CONNECTIONS_POOL.filter((c) => c.tunnelId === tunnel.id).length,
		};
	}

	return tunnels;
}

function buildMockPolicyInterfaces() {
	const seen = new Set();
	const add = (items, item) => {
		if (!item.name || seen.has(item.name)) return;
		seen.add(item.name);
		items.push(item);
	};

	const items = [];
	add(items, { name: 'Direct', label: 'Direct', up: true });

	for (const iface of LEGACY_POLICY_INTERFACE_FIXTURES) {
		add(items, { ...iface });
	}

	for (const tunnel of MOCK_AWG_TUNNELS) {
		add(items, {
			name: tunnel.interfaceName,
			label: tunnel.name,
			up: tunnel.enabled !== false && tunnel.status === 'running',
		});
	}

	for (const tunnel of MOCK_SYSTEM_TUNNELS) {
		add(items, {
			name: tunnel.interfaceName,
			label: tunnel.description || tunnel.interfaceName,
			up: tunnel.connected !== false && tunnel.status === 'up',
		});
	}

	for (const tunnel of MOCK_SINGBOX_TUNNELS) {
		add(items, {
			name: tunnel.proxyInterface,
			label: tunnel.tag,
			up: !!tunnel.running,
		});
	}

	for (const policy of mockAccessPolicies) {
		for (const [index, iface] of (policy.interfaces ?? []).entries()) {
			add(items, {
				name: iface.name,
				label: iface.label || iface.name,
				up: true,
				source: `policy:${policy.name}`,
				order: iface.order ?? index,
			});
		}
	}

	return items;
}

function buildMockRoutingTunnels() {
	const awg = MOCK_AWG_TUNNELS.map((t) => ({
		id: t.id,
		name: t.name,
		iface: t.interfaceName,
		type: 'managed',
		status: t.status === 'running' ? 'up' : t.status,
		available: t.enabled !== false && t.status === 'running',
	}));

	const system = MOCK_SYSTEM_TUNNELS.map((t) => ({
		id: t.id,
		name: t.description || t.id,
		iface: t.interfaceName,
		type: 'system',
		status: t.status,
		available: t.connected !== false && t.status === 'up',
	}));

	return [
		...awg,
		...system,
		{ id: 'direct', name: 'Direct', iface: 'eth3', type: 'wan', status: 'up', available: true },
	];
}

// ── Logs catalog (mock) ────────────────────────────────────────
// Two independent buckets matching the backend split. Each bucket is a
// rotating ring of pre-baked entries; the proxy injects fresh timestamps
// on every call so the UI always sees recent activity. Pagination is
// supported via ?offset/?limit; the static catalogs are large enough to
// exercise the "Загрузить ещё" button.

const APP_LOG_TEMPLATES = [
	// tunnel
	{ group: 'tunnel',  subgroup: 'lifecycle',   level: 'info',  action: 'create',   target: 'awg0',     message: 'Tunnel created' },
	{ group: 'tunnel',  subgroup: 'lifecycle',   level: 'info',  action: 'start',    target: 'awg0',     message: 'Tunnel started' },
	{ group: 'tunnel',  subgroup: 'lifecycle',   level: 'warn',  action: 'start',    target: 'awg1',     message: 'wg-quick: handshake timeout, retrying' },
	{ group: 'tunnel',  subgroup: 'lifecycle',   level: 'error', action: 'start',    target: 'awg2',     message: 'Failed to bring up interface: address conflict' },
	{ group: 'tunnel',  subgroup: 'ops',         level: 'info',  action: 'add-route',target: '10.0.0.0/8', message: 'Route added via awg0' },
	{ group: 'tunnel',  subgroup: 'state',       level: 'info',  action: 'transition', target: 'awg0',  message: 'state running → stopped' },
	{ group: 'tunnel',  subgroup: 'firewall',    level: 'info',  action: 'rule-add', target: 'awg0',     message: 'iptables FORWARD rule installed' },
	{ group: 'tunnel',  subgroup: 'pingcheck',   level: 'warn',  action: 'fail',     target: 'awg0',     message: 'ping 8.8.8.8 timeout (3/3 lost)' },
	{ group: 'tunnel',  subgroup: 'connectivity',level: 'debug', action: 'await-handshake', target: 'awg0', message: 'tunnel went running, waiting for handshake' },
	{ group: 'tunnel',  subgroup: 'test',        level: 'info',  action: 'connectivity', target: 'awg0', message: 'TCP 1.1.1.1:443 reachable in 23ms' },
	// routing
	{ group: 'routing', subgroup: 'dns-route',   level: 'info',  action: 'apply',    target: 'youtube', message: 'List "youtube" applied (124 domains)' },
	{ group: 'routing', subgroup: 'dns-route',   level: 'warn',  action: 'failover', target: 'youtube', message: 'Switched youtube → backup tunnel awg1' },
	{ group: 'routing', subgroup: 'static-route',level: 'info',  action: 'add',      target: '8.8.8.8/32', message: 'static route via awg0' },
	{ group: 'routing', subgroup: 'access-policy',level: 'info', action: 'create',   target: 'AwgPolicy0', message: 'Policy created with 2 interfaces' },
	{ group: 'routing', subgroup: 'client-route',level: 'info',  action: 'apply',    target: '192.168.1.50', message: 'Per-client route to awg0 installed' },
	{ group: 'routing', subgroup: 'singbox-router',level:'info', action: 'reload',   target: '',        message: 'sing-box router config reloaded (12 rules)' },
	{ group: 'routing', subgroup: 'deviceproxy', level: 'info',  action: 'enable',   target: 'vless-1', message: 'Device proxy enabled on :1099 via vless-1' },
	{ group: 'routing', subgroup: 'deviceproxy', level: 'info',  action: 'change-outbound', target: 'vless-2', message: 'Device proxy outbound switched to vless-2' },
	{ group: 'routing', subgroup: 'hrneo',       level: 'info',  action: 'restart',  target: '',        message: 'neo restarted' },
	{ group: 'routing', subgroup: 'hrneo',       level: 'info',  action: 'sync-geo', target: '',        message: 'sync geo files: 3 geoip + 5 geosite' },
	{ group: 'routing', subgroup: 'hrneo',       level: 'warn',  action: 'download', target: 'https://example.com/geoip.dat', message: 'geoip: read tcp: i/o timeout' },
	{ group: 'routing', subgroup: 'catalog',     level: 'warn',  action: 'snapshot-section', target: 'staticRoutes', message: 'failed to load static routes section' },
	// server
	{ group: 'server',  subgroup: 'managed',     level: 'info',  action: 'create',   target: 'wg-server-1', message: 'Managed WG server provisioned' },
	{ group: 'server',  subgroup: 'managed',     level: 'warn',  action: 'sync',     target: 'wg-server-1', message: 'Peer count drift: storage=12 ndms=11' },
	// system
	{ group: 'system',  subgroup: 'boot',        level: 'info',  action: 'startup',  target: '',        message: 'awg-manager v2.9.9.3 started' },
	{ group: 'system',  subgroup: 'auth',        level: 'info',  action: 'login',    target: 'admin',   message: 'Login successful' },
	{ group: 'system',  subgroup: 'auth',        level: 'warn',  action: 'login',    target: '',        message: 'Login failed: bad credentials (192.168.1.50)' },
	{ group: 'system',  subgroup: 'settings',    level: 'info',  action: 'logging',  target: '',        message: 'Logging enabled' },
	{ group: 'system',  subgroup: 'update',      level: 'info',  action: 'check',    target: '',        message: 'Update check: latest=2.9.9.3, current=2.9.9.3' },
	{ group: 'system',  subgroup: 'wan',         level: 'info',  action: 'detect',   target: 'ppp0',    message: 'WAN interface ppp0 detected' },
	{ group: 'system',  subgroup: 'system-tunnels', level:'info', action: 'sync',    target: 'Wireguard0', message: 'System NWG tunnel imported' },
	{ group: 'system',  subgroup: 'dnscheck',    level: 'info',  action: 'start',    target: '192.168.1.50', message: 'DNS check started' },
	{ group: 'system',  subgroup: 'dnscheck',    level: 'info',  action: 'complete', target: '192.168.1.50', message: 'DNS check completed: all checks passed' },
	{ group: 'system',  subgroup: 'connections', level: 'warn',  action: 'read-conntrack', target: '', message: 'open /proc/net/nf_conntrack: permission denied' },
	{ group: 'system',  subgroup: 'traffic',     level: 'warn',  action: 'read-counters', target: 'awg0', message: 'sysfs awg0: stat file disappeared' },
	{ group: 'system',  subgroup: 'diagnostics', level: 'info',  action: 'run',      target: '',        message: 'Diagnostics complete in 1832ms (14 tests)' },
	{ group: 'system',  subgroup: 'cleanup',     level: 'info',  action: 'sweep',    target: '',        message: 'startup sweep: removed 2 orphan rules' },
	{ group: 'system',  subgroup: 'rci',         level: 'debug', action: 'GET',      target: '/show/interface', message: '200 in 12ms' },
	{ group: 'system',  subgroup: 'rci',         level: 'debug', action: 'POST',     target: '/',       message: '200 in 45ms' },
	{ group: 'system',  subgroup: 'ndms',        level: 'debug', action: 'GET',      target: '/show/ip/route', message: '200 in 8ms' },
	{ group: 'system',  subgroup: 'ndms',        level: 'debug', action: 'POST',     target: '/',       message: '200 in 67ms' },
];

const SINGBOX_LOG_TEMPLATES = [
	{ group: 'singbox', subgroup: 'process',  level: 'info',  action: 'stdout', target: '', message: 'sing-box version 1.9.3 starting' },
	{ group: 'singbox', subgroup: 'process',  level: 'error', action: 'stderr', target: '', message: 'FATAL: failed to bind tproxy: address already in use' },
	{ group: 'singbox', subgroup: 'process',  level: 'warn',  action: 'stderr', target: '', message: 'WARN: deprecated config field "auto_detect_interface"' },
	{ group: 'singbox', subgroup: 'runtime',  level: 'info',  action: 'run',    target: 'sing-box',  message: 'started 8 inbounds, 6 outbounds' },
	{ group: 'singbox', subgroup: 'runtime',  level: 'info',  action: 'clash',  target: '',          message: '[Connection] tcp 192.168.1.50:54321 -> example.com:443' },
	{ group: 'singbox', subgroup: 'inbound',  level: 'info',  action: 'run',    target: 'tun-in',    message: '[TPROXY] mark=0x1 fwmark applied' },
	{ group: 'singbox', subgroup: 'inbound',  level: 'full',  action: 'run',    target: 'mixed-in',  message: 'mixed: accepted connection from 192.168.1.10' },
	{ group: 'singbox', subgroup: 'outbound', level: 'info',  action: 'run',    target: 'veesp',     message: 'outbound connection to www.gstatic.com:443' },
	{ group: 'singbox', subgroup: 'outbound', level: 'info',  action: 'run',    target: 'veesp',     message: 'outbound connection to youtube.com:443' },
	{ group: 'singbox', subgroup: 'outbound', level: 'info',  action: 'run',    target: 'prague',    message: 'outbound connection to api.example.com:443' },
	{ group: 'singbox', subgroup: 'outbound', level: 'warn',  action: 'run',    target: 'prague',    message: 'connect to upstream: i/o timeout' },
	{ group: 'singbox', subgroup: 'dns',      level: 'debug', action: 'run',    target: 'dns',       message: '[DNS] resolve example.com via 1.1.1.1' },
	{ group: 'singbox', subgroup: 'dns',      level: 'info',  action: 'run',    target: 'dns',       message: '[DNS] cache miss for telegram.org (TTL=300)' },
	{ group: 'singbox', subgroup: 'router',   level: 'full',  action: 'run',    target: 'router',    message: '[Router] match rule "geo:RU" -> outbound: direct' },
	{ group: 'singbox', subgroup: 'router',   level: 'info',  action: 'run',    target: 'router',    message: '[Router] reload: 12 rules, 3 rule-sets' },
];

function expandTemplates(templates, copies, jitterMs) {
	const out = [];
	const nowMs = Date.now();
	for (let c = 0; c < copies; c++) {
		for (let i = 0; i < templates.length; i++) {
			const t = templates[i];
			const offset = (c * templates.length + i) * jitterMs;
			out.push({
				...t,
				timestamp: new Date(nowMs - offset).toISOString(),
			});
		}
	}
	// newest-first
	return out.sort((a, b) => new Date(b.timestamp).getTime() - new Date(a.timestamp).getTime());
}

function buildFakeAppEntries() {
	// 4 copies × ~40 templates = ~160 entries — enough for 2 pages of 200? not quite,
	// bump to 6 so pagination button appears on common page sizes.
	return expandTemplates(APP_LOG_TEMPLATES, 6, 5_000);
}

function buildFakeSingboxEntries() {
	// Sing-box is noisier — 30 copies × 15 templates = 450 entries.
	return expandTemplates(SINGBOX_LOG_TEMPLATES, 30, 1_500);
}

const KNOWN_SUBGROUPS_MOCK = {
	tunnel: ['lifecycle', 'ops', 'state', 'firewall', 'pingcheck', 'connectivity', 'test', 'signature'],
	routing: ['dns-route', 'static-route', 'access-policy', 'client-route', 'singbox-router', 'deviceproxy', 'hrneo', 'catalog', 'awg-outbounds'],
	server: ['managed'],
	system: ['boot', 'auth', 'settings', 'update', 'wan', 'system-tunnels', 'cleanup', 'dnscheck', 'connections', 'traffic', 'diagnostics', 'rci', 'ndms'],
	singbox: ['process', 'inbound', 'outbound', 'dns', 'router', 'runtime'],
};

const BUCKET_CAPACITY_MOCK = 5000;

// In-memory clear state per bucket — Clear button hides everything
// until next refresh of the static catalog.
const bucketCleared = { app: false, singbox: false };

function applyFilters(entries, qs) {
	let out = entries;
	const group = qs.get('group');
	if (group) out = out.filter((e) => e.group === group);
	const sub = qs.get('subgroup');
	if (sub) out = out.filter((e) => e.subgroup === sub);
	const lvl = qs.get('level');
	if (lvl) {
		const levelOrder = ['error', 'warn', 'info', 'full', 'debug'];
		const idx = levelOrder.indexOf(lvl);
		if (idx >= 0) {
			const allowed = new Set(levelOrder.slice(0, idx + 1));
			// ERROR and WARN always visible regardless of configured level.
			allowed.add('error');
			allowed.add('warn');
			out = out.filter((e) => allowed.has(e.level));
		}
	}
	return out;
}

function paginate(entries, qs) {
	const limit = Math.max(1, Math.min(10000, Number(qs.get('limit')) || 200));
	const offset = Math.max(0, Number(qs.get('offset')) || 0);
	const total = entries.length;
	const slice = entries.slice(offset, offset + limit);
	return { slice, total, limit, offset };
}

// ── Sing-box composite proxies (Feature 1) ─────────────────────
// Stateful across calls: selecting a member persists in this map
// so the UI's optimistic update is reflected on the next /list poll.
const mockProxies = {
	'veesp-fast': {
		type: 'selector',
		now: 'vless-1',
		all: ['vless-1', 'vless-2', 'vless-3'],
	},
	'auto': {
		type: 'urltest',
		now: 'vless-2',
		all: ['vless-1', 'vless-2', 'vless-3', 'vless-4'],
	},
	'sub-demo0001': {
		type: 'selector',
		now: 'sub-demo0001-aabbccdd',
		all: ['sub-demo0001-aabbccdd', 'sub-demo0001-eeff0011', 'sub-demo0001-22334455'],
	},
	'manual-eu': {
		type: 'selector',
		now: 'awg-vpn0',
		all: ['awg-vpn0', 'awg-sys-Wireguard0'],
	},
	'sub-bigprov': {
		type: 'urltest',
		now: 'sub-bigprov-de01a1b2',
		all: [
			'sub-bigprov-de01a1b2',
			'sub-bigprov-nl02c3d4',
			'sub-bigprov-fi03e5f6',
			'sub-bigprov-fr04g7h8',
			'sub-bigprov-uk05i9j0',
			'sub-bigprov-us06k1l2',
			'sub-bigprov-jp07m3n4',
			'sub-bigprov-sg08o5p6',
			'sub-bigprov-hk09q7r8',
			'sub-bigprov-ca10s9t0',
			'sub-bigprov-au11u1v2',
			'sub-bigprov-in12w3x4',
		],
	},
};
const mockProxyDelays = {
	'vless-1': 48,
	'vless-2': 135,
	'vless-3': 285,
	'vless-4': 520,
};
function randomizeDelays() {
	for (const k of Object.keys(mockProxyDelays)) {
		mockProxyDelays[k] = mockDelayJitter(mockProxyDelays[k]);
	}
}

const MOCK_AMNEZIA_PREMIUM_SID = 'mock-v_sid-amnezia-premium-dev';
const MOCK_AMNEZIA_PREMIUM_COUNTRIES = [
	{ server_country_code: 'ru', server_country_name: 'Russia (mock)' },
	{ server_country_code: 'nl', server_country_name: 'Netherlands (mock)' },
	{ server_country_code: 'ee', server_country_name: 'Estonia (mock stale)' },
];

function buildMockAmneziaPremiumConf(countryCode) {
	const cc = String(countryCode || 'xx').toLowerCase();
	return `# Amnezia Premium mock — country ${cc}
[Interface]
PrivateKey = YNaK+9G1J6K1G3K1G3K1G3K1G3K1G3K1G3K1G2E=
Address = 10.77.0.2/32
DNS = 1.1.1.1

[Peer]
PublicKey = RT7K+9G1J6K1G3K1G3K1G3K1G3K1G3K1G3K1G2E=
Endpoint = mock-premium-${cc}.example:51820
AllowedIPs = 0.0.0.0/0, ::/0
PersistentKeepalive = 25
`;
}

const server = http.createServer(async (req, res) => {
	const url = new URL(req.url, `http://${req.headers.host}`);
	const path = url.pathname;

	if (req.method === 'GET' && path === '/__mock/capabilities') {
		sendData(res, buildMockCapabilities());
		return;
	}

	if (req.method === 'GET' && path === '/__mock/tunnels') {
		sendData(res, buildMockTunnelCatalog());
		return;
	}

	if (req.method === 'POST' && (path === '/__mock/reset-runtime' || path === '/__mock/reset')) {
		await readRequestText(req);
		resetRuntimeControls();
		sendData(res, { reset: true, scope: 'runtime', state: getRuntimeState() });
		console.log('[mock-proxy] volatile mock runtime state reset');
		return;
	}

	// Toggle / tune the random service-download fault injector at runtime.
	// Body (optional JSON): { enabled?: boolean, probability?: 0..1 }.
	if (req.method === 'POST' && path === '/__mock/download-faults') {
		const text = await readRequestText(req);
		try {
			const body = text ? JSON.parse(text) : {};
			if (typeof body.enabled === 'boolean') downloadFaultsEnabled = body.enabled;
			if (body.probability !== undefined) {
				downloadFaultProbability = parseProbability(body.probability, downloadFaultProbability);
			}
		} catch {
			/* ignore malformed body, just report current state */
		}
		sendData(res, {
			downloadFaultsEnabled,
			downloadFaultProbability,
		});
		console.log(`[mock-proxy] download faults: enabled=${downloadFaultsEnabled} p=${downloadFaultProbability}`);
		return;
	}

	if (req.method === 'POST' && path === '/__mock/keenetic-os') {
		const text = await readRequestText(req);
		try {
			const body = text ? JSON.parse(text) : {};
			if (body.version === '5.0' || body.version === '5.1') {
				setMockKeeneticProfileKey(body.version);
			} else {
				applyDefaultMockKeeneticProfile();
			}
		} catch {
			applyDefaultMockKeeneticProfile();
		}
		sendData(res, {
			keeneticOS: mockKeeneticProfile.key,
			supportsExtendedASC: mockKeeneticProfile.extended,
			firmwareVersion: mockKeeneticProfile.firmwareVersion,
		});
		console.log(
			`[mock-proxy] keenetic-os: ${mockKeeneticProfile.key} (supportsExtendedASC=${mockKeeneticProfile.extended})`,
		);
		return;
	}

	if (maybeInjectDownloadFault(req, res, path)) return;

	if (req.method === 'GET' && path === '/system/info') {
		fetchJSON('/system/info').then(({ status, body }) => {
			if (body && typeof body === 'object' && body.data && typeof body.data === 'object') {
				const data = body.data;
				const details = (data.routerDetails && typeof data.routerDetails === 'object')
					? data.routerDetails
					: {};

				const titleRaw = String(details.firmwareTitle ?? '');

				// Keep router title as model only; [Port] is rendered separately in UI
				// from details.portedBuild and must not be duplicated in the string.
				let firmwareTitle = titleRaw.replace(/\s*\[Port\]\s*/gi, '').trim();
				if (!firmwareTitle || firmwareTitle === '—') {
					firmwareTitle = 'CMCC RAX3000M (KN-3812)';
				}

				data.routerDetails = {
					...details,
					firmwareTitle,
					firmwareRelease: mockKeeneticProfile.firmwareRelease,
					portedBuild: details.portedBuild ?? true,
					modelDisplay: details.modelDisplay || 'CMCC RAX3000M (KN-3812)',
					region: details.region || 'EA',
				};
				applyMockKeeneticProfile(data);
			}
			send(res, status, body);
		});
		return;
	}

	if (req.method === 'GET' && path === '/settings/get') {
		fetchJSON('/settings/get').then(({ status, body }) => {
			if (body && typeof body === 'object' && body.data) {
				body.data.usageLevel = usageLevel;
				if (!body.data.logging || typeof body.data.logging !== 'object') {
					body.data.logging = {};
				}
				body.data.logging.singboxLogLevel = singboxLogLevel;
				if (!body.data.download || typeof body.data.download !== 'object') {
					body.data.download = {};
				}
				body.data.download.routeTag = downloadRouteTag;
				if (!body.data.updates || typeof body.data.updates !== 'object') {
					body.data.updates = { checkEnabled: true, channel: 'stable' };
				}
				body.data.updates.channel = updateChannel;
				body.data.updates.checkEnabled = updateCheckEnabled;
			}
			send(res, status, body);
		});
		return;
	}

	if (req.method === 'GET' && path === '/download/outbounds') {
		send(res, 200, {
			success: true,
			data: buildDownloadOutbounds(),
		});
		return;
	}

	if (req.method === 'GET' && path === '/hydraroute/geo-files') {
		const ago = (min) => new Date(Date.now() - min * 60_000).toISOString();
		send(res, 200, {
			success: true,
			data: [
				// Managed by AWGM (external !== true) — updatable through the route.
				{
					type: 'geoip',
					path: '/opt/etc/HydraRoute/geoip_GA.dat',
					url: 'https://raw.githubusercontent.com/Ground-Zerro/Geo-Aggregator/main/geodat/geoip_GA.dat',
					size: 1_245_184,
					tagCount: 312,
					updated: ago(180),
				},
				{
					type: 'geosite',
					path: '/opt/etc/HydraRoute/geosite_GA.dat',
					url: 'https://raw.githubusercontent.com/Ground-Zerro/Geo-Aggregator/main/geodat/geosite_GA.dat',
					size: 4_980_736,
					tagCount: 868,
					updated: ago(180),
				},
				// Extra non-external geoip (managed by AWGM).
				{
					type: 'geoip',
					path: '/opt/etc/HydraRoute/geoip_antifilter.dat',
					url: 'https://cdn.example.com/geodat/geoip_antifilter.dat',
					size: 786_432,
					tagCount: 154,
					updated: ago(45),
				},
				// External — discovered in hrneo.conf, not managed by AWGM.
				{
					type: 'geosite',
					path: '/opt/etc/hrneo/geosite.db',
					url: 'https://cdn.example.com/geosite.db',
					size: 3_145_728,
					tagCount: 420,
					updated: ago(1440),
					external: true,
				},
			],
		});
		return;
	}

	if (req.method === 'POST' && path === '/settings/update') {
		let raw = '';
		req.on('data', (c) => (raw += c));
		req.on('end', async () => {
			try {
				const payload = JSON.parse(raw);
				if (typeof payload.usageLevel === 'string') {
					if (!VALID.has(payload.usageLevel)) {
						send(res, 400, {
							success: false,
							error: 'invalid usageLevel',
							code: 'INVALID_USAGE_LEVEL',
						});
						return;
					}
					usageLevel = payload.usageLevel;
				}
				if (
					payload &&
					typeof payload === 'object' &&
					payload.logging &&
					typeof payload.logging === 'object' &&
					typeof payload.logging.singboxLogLevel === 'string'
				) {
					singboxLogLevel = payload.logging.singboxLogLevel;
				}
				if (
					payload &&
					typeof payload === 'object' &&
					payload.download &&
					typeof payload.download === 'object' &&
					typeof payload.download.routeTag === 'string'
				) {
					downloadRouteTag = payload.download.routeTag;
				}
				if (
					payload &&
					typeof payload === 'object' &&
					payload.updates &&
					typeof payload.updates === 'object'
				) {
					if (payload.updates.channel === 'stable' || payload.updates.channel === 'develop') {
						updateChannel = payload.updates.channel;
					}
					if (typeof payload.updates.checkEnabled === 'boolean') {
						updateCheckEnabled = payload.updates.checkEnabled;
					}
				}
				const { status, body } = await fetchJSON('/settings/get');
				if (body && typeof body === 'object' && body.data) {
					body.data.usageLevel = usageLevel;
					if (!body.data.logging || typeof body.data.logging !== 'object') {
						body.data.logging = {};
					}
					body.data.logging.singboxLogLevel = singboxLogLevel;
					if (!body.data.download || typeof body.data.download !== 'object') {
						body.data.download = {};
					}
					body.data.download.routeTag = downloadRouteTag;
					if (!body.data.updates || typeof body.data.updates !== 'object') {
						body.data.updates = { checkEnabled: true, channel: 'stable' };
					}
					body.data.updates.channel = updateChannel;
					body.data.updates.checkEnabled = updateCheckEnabled;
				}
				send(res, status, body);
				console.log(
					`[mock-proxy] usageLevel → ${usageLevel}, singboxLogLevel → ${singboxLogLevel}, downloadRouteTag → ${downloadRouteTag}, updateChannel → ${updateChannel}`,
				);
			} catch (e) {
				send(res, 500, { success: false, error: String(e) });
			}
		});
		return;
	}

	if (req.method === 'POST' && path === '/amnezia-premium/login') {
		let raw = '';
		req.on('data', (c) => (raw += c));
		req.on('end', () => {
			try {
				const payload = JSON.parse(raw || '{}');
				const key = String(payload.vpnKey ?? '').trim();
				if (!key) {
					send(res, 400, {
						error: true,
						message: 'vpnKey обязателен',
						code: 'MISSING_VPN_KEY',
					});
					return;
				}
				// Локальный стаб: реальный cp.amnezia.org может вернуть 422 на неверный ключ —
				// здесь принимаем любой непустой ключ (не только vpn://), чтобы UI на :5173
				// не блокировать разработку тестовой строкой.
				console.log('[mock-proxy] amnezia-premium/login ok (stub sid)');
				send(res, 200, { success: true, data: { sid: MOCK_AMNEZIA_PREMIUM_SID } });
			} catch {
				send(res, 400, { error: true, message: 'invalid JSON', code: 'INVALID_JSON' });
			}
		});
		return;
	}

	if (req.method === 'POST' && path === '/amnezia-premium/account-info') {
		let raw = '';
		req.on('data', (c) => (raw += c));
		req.on('end', () => {
			try {
				const payload = JSON.parse(raw || '{}');
				const sid = String(payload.sid ?? '').trim();
				if (sid !== MOCK_AMNEZIA_PREMIUM_SID) {
					send(res, 401, {
						error: true,
						message: 'Сессия Amnezia Premium недействительна (mock)',
						code: 'AMNEZIA_CP_ERROR',
					});
					return;
				}
				send(res, 200, {
					success: true,
					data: {
						http_status: 200,
						available_countries: MOCK_AMNEZIA_PREMIUM_COUNTRIES,
						issued_configs: [
							{
								server_country_code: 'nl',
								server_country_name: 'Netherlands (mock issued)',
								worker_last_updated: '2026-02-03T13:49:07.090912Z',
								last_downloaded: new Date().toISOString(),
								source_type: 'country_config',
								os_version: 'Web',
								installation_uuid: '00000000-0000-4000-8000-000000000001',
							},
							{
								server_country_code: 'ee',
								server_country_name: 'Estonia (mock stale)',
								worker_last_updated: '2026-04-30T17:34:17.821424Z',
								last_downloaded: '2026-04-23T16:07:43.367914Z',
								source_type: 'country_config',
								os_version: 'Web',
								installation_uuid: '00000000-0000-4000-8000-000000000002',
							},
						],
						subscription_status: 'active',
						vpn_key: 'vpn://mock',
					},
				});
			} catch {
				send(res, 400, { error: true, message: 'invalid JSON', code: 'INVALID_JSON' });
			}
		});
		return;
	}

	if (req.method === 'POST' && path === '/amnezia-premium/download-config') {
		let raw = '';
		req.on('data', (c) => (raw += c));
		req.on('end', () => {
			try {
				const payload = JSON.parse(raw || '{}');
				const sid = String(payload.sid ?? '').trim();
				const countryCode = String(payload.countryCode ?? '').trim().toLowerCase();
				if (sid !== MOCK_AMNEZIA_PREMIUM_SID) {
					send(res, 401, {
						error: true,
						message: 'Сессия Amnezia Premium недействительна (mock)',
						code: 'AMNEZIA_CP_ERROR',
					});
					return;
				}
				if (!countryCode) {
					send(res, 400, {
						error: true,
						message: 'sid и countryCode обязательны',
						code: 'MISSING_FIELDS',
					});
					return;
				}
				console.log(`[mock-proxy] amnezia-premium/download-config ${countryCode}`);
				send(res, 200, {
					success: true,
					data: { config: buildMockAmneziaPremiumConf(countryCode) },
				});
			} catch {
				send(res, 400, { error: true, message: 'invalid JSON', code: 'INVALID_JSON' });
			}
		});
		return;
	}

	if (req.method === 'GET' && path === '/tunnels/all') {
		send(res, 200, { success: true, data: buildAwgSnapshot() });
		return;
	}

	if (req.method === 'GET' && path === '/pingcheck/status') {
		send(res, 200, { success: true, data: buildPingCheckStatus() });
		return;
	}

	if (req.method === 'GET' && path === '/pingcheck/logs') {
		const id = url.searchParams.get('tunnelId') ?? '';
		const logs = buildPingCheckLogs();
		send(res, 200, { success: true, data: id ? logs.filter((e) => e.tunnelId === id) : logs });
		return;
	}

	if (req.method === 'GET' && path === '/tunnels/get') {
		const id = url.searchParams.get('id') ?? '';
		const tunnel = buildSingleTunnel(id);
		if (!tunnel) {
			send(res, 404, { success: false, error: { code: 'NOT_FOUND', message: `tunnel ${id} not found` } });
			return;
		}
		send(res, 200, { success: true, data: tunnel });
		return;
	}

	if (req.method === 'GET' && path === '/tunnels/traffic') {
		const id = url.searchParams.get('id') ?? '';
		const requestedPeriod = url.searchParams.get('period') ?? '1h';
		const period = Object.prototype.hasOwnProperty.call(TRAFFIC_PERIOD_MS, requestedPeriod) ? requestedPeriod : '1h';
		send(res, 200, buildTrafficResponse(id || 'awg-demo-1', period));
		return;
	}

	if (req.method === 'GET' && path === '/connections') {
		const raw = process.env.MOCK_CONNECTIONS_DELAY_MS;
		const defaultMs = 900;
		let delayMs = defaultMs;
		if (raw !== undefined && raw !== '') {
			const n = Number(raw);
			delayMs = Number.isFinite(n) && n >= 0 ? n : defaultMs;
		}
		if (delayMs > 0) {
			await wait(delayMs);
		}
		send(res, 200, buildMockConnectionsResponse(url));
		return;
	}

	if (req.method === 'GET' && path === '/events') {
		res.writeHead(200, {
			'Content-Type': 'text/event-stream',
			'Cache-Control': 'no-cache',
			'Connection': 'keep-alive',
			'X-Accel-Buffering': 'no',
		});

		const sendEvent = (event, data) => {
			res.write(`event: ${event}\n`);
			res.write(`data: ${JSON.stringify(data)}\n\n`);
		};

		sendEvent('connected', { ok: true });
		// Register this connection so POST /singbox/router/mode can push the
		// representative singbox-router:transition progress sequence.
		sseSenders.add(sendEvent);
		for (const event of tickAwgTraffic()) {
			sendEvent('tunnel:traffic', event);
		}
		sendEvent('singbox:traffic', buildSingboxTrafficEvent());
		for (const delay of currentSingboxDelays()) {
			sendEvent('singbox:delay', delay);
		}
		// Emit initial connectivity matrix so tunnel cards show latency immediately.
		sendEvent('monitoring:matrix-update', buildConnectivityMatrixEvent());

		let connectivityTick = 0;
		const interval = setInterval(() => {
			for (const event of tickAwgTraffic()) {
				sendEvent('tunnel:traffic', event);
			}
			sendEvent('singbox:traffic', buildSingboxTrafficEvent());
			for (const delay of currentSingboxDelays()) {
				sendEvent('singbox:delay', delay);
			}
			// Refresh connectivity every ~4.5s (every 3rd 1500ms tick).
			connectivityTick++;
			if (connectivityTick % 3 === 0) {
				sendEvent('monitoring:matrix-update', buildConnectivityMatrixEvent());
			}
		}, 1500);

		const cleanup = () => {
			clearInterval(interval);
			sseSenders.delete(sendEvent);
		};
		req.on('close', cleanup);
		req.on('error', cleanup);
		res.on('close', cleanup);
		return;
	}

	if (req.method === 'GET' && path === '/logs') {
		const bucket = url.searchParams.get('bucket') === 'singbox' ? 'singbox' : 'app';
		const all = bucketCleared[bucket]
			? []
			: bucket === 'singbox' ? buildFakeSingboxEntries() : buildFakeAppEntries();
		const filtered = applyFilters(all, url.searchParams);
		const { slice, total } = paginate(filtered, url.searchParams);
		const oldest = all.length > 0 ? all[all.length - 1].timestamp : undefined;
		send(res, 200, {
			data: {
				enabled: true,
				logs: slice,
				total,
				bucket,
				bufferSize: all.length,
				bufferCapacity: BUCKET_CAPACITY_MOCK,
				oldestTimestamp: oldest,
			},
			success: true,
		});
		return;
	}

	if (req.method === 'POST' && path === '/logs/clear') {
		const raw = url.searchParams.get('bucket');
		if (raw !== 'app' && raw !== 'singbox') {
			send(res, 400, { success: false, error: 'invalid bucket', code: 'INVALID_BUCKET' });
			return;
		}
		bucketCleared[raw] = true;
		console.log(`[mock-proxy] /logs/clear bucket=${raw}`);
		send(res, 200, { success: true, data: { cleared: true, bucket: raw } });
		return;
	}

	if (req.method === 'GET' && path === '/logs/subgroups') {
		const group = url.searchParams.get('group') ?? '';
		if (!group) {
			send(res, 400, { success: false, error: 'group required', code: 'MISSING_GROUP' });
			return;
		}
		const subs = KNOWN_SUBGROUPS_MOCK[group] ?? [];
		send(res, 200, { success: true, data: { group, subgroups: subs } });
		return;
	}

	if (req.method === 'GET' && path === '/singbox/connections/clients') {
		send(res, 200, {
			success: true,
			data: {
				clientsByIP: {
					'192.168.1.5': 'Anyas-iPhone',
					'192.168.1.7': 'macbook',
					'192.168.1.9': 'android-tablet',
				},
			},
		});
		return;
	}

	if (req.method === 'GET' && path === '/monitoring/matrix') {
		const forced = url.searchParams.get('force') === '1' || url.searchParams.get('force') === 'true';
		send(res, 200, { success: true, data: buildConnectivityMatrixEvent({ forced }) });
		return;
	}

	if (req.method === 'GET' && path === '/singbox/router/proxies/list') {
		randomizeDelays();
		const groups = Object.entries(mockProxies).map(([tag, g]) => ({
			tag,
			type: g.type,
			now: g.now,
			members: g.all.map((memberTag) => ({
				tag: memberTag,
				type: 'vless',
				lastDelay: mockProxyDelays[memberTag] ?? 0,
			})),
		}));
		send(res, 200, { success: true, data: { groups } });
		return;
	}

	if (req.method === 'POST' && path === '/singbox/router/proxies/select') {
		let raw = '';
		req.on('data', (c) => (raw += c));
		req.on('end', () => {
			try {
				const payload = JSON.parse(raw || '{}');
				const group = typeof payload.group === 'string' ? payload.group : '';
				const member = typeof payload.member === 'string' ? payload.member : '';
				const g = mockProxies[group];
				if (!g) {
					send(res, 404, {
						success: false,
						error: { code: 'PROXY_GROUP_NOT_FOUND', message: `group ${group} not found` },
					});
					return;
				}
				if (g.type !== 'selector') {
					send(res, 400, {
						success: false,
						error: { code: 'PROXY_GROUP_NOT_SELECTABLE', message: `group ${group} is ${g.type}, not selector` },
					});
					return;
				}
				if (!g.all.includes(member)) {
					send(res, 400, {
						success: false,
						error: { code: 'PROXY_MEMBER_NOT_FOUND', message: `member ${member} not in group ${group}` },
					});
					return;
				}
				g.now = member;
				send(res, 200, { success: true, data: {} });
				console.log(`[mock-proxy] proxies.select ${group} → ${member}`);
			} catch (e) {
				send(res, 400, {
					success: false,
					error: { code: 'INVALID_REQUEST', message: String(e) },
				});
			}
		});
		return;
	}

	if (req.method === 'POST' && path === '/singbox/router/proxies/test') {
		let raw = '';
		req.on('data', (c) => (raw += c));
		req.on('end', () => {
			try {
				const payload = JSON.parse(raw || '{}');
				const group = typeof payload.group === 'string' ? payload.group : '';
				const g = mockProxies[group];
				if (g) {
					// Known proxy group — use the randomised delay table.
					randomizeDelays();
					const delays = {};
					for (const memberTag of g.all) {
						delays[memberTag] = mockProxyDelays[memberTag] ?? 0;
					}
					send(res, 200, { success: true, data: { delays } });
					console.log(`[mock-proxy] proxies.test ${group} → ${JSON.stringify(delays)}`);
					return;
				}
				// Subscription selector group — synthesize per-tag delays via hash.
				const sub = mockSubscriptions.find((s) => s.selectorTag === group);
				if (!sub) {
					send(res, 404, {
						success: false,
						error: { code: 'PROXY_GROUP_NOT_FOUND', message: `group ${group} not found` },
					});
					return;
				}
				const delays = {};
				for (const tag of sub.memberTags) {
					// Stable hash of the tag → base latency 30..400 ms, plus jitter.
					let h = 0;
					for (let i = 0; i < tag.length; i++) h = ((h << 5) - h + tag.charCodeAt(i)) | 0;
					const base = Math.abs(h) % 370 + 30;
					const jitter = Math.floor(Math.random() * 40) - 20;
					delays[tag] = Math.max(1, base + jitter);
				}
				send(res, 200, { success: true, data: { delays } });
				console.log(`[mock-proxy] proxies.test sub ${group} → ${JSON.stringify(delays)}`);
			} catch (e) {
				send(res, 400, {
					success: false,
					error: { code: 'INVALID_REQUEST', message: String(e) },
				});
			}
		});
		return;
	}

	if (req.method === 'GET' && path === '/singbox/config-preview') {
		const merged = {
			log: { level: 'trace', timestamp: true },
			dns: {
				servers: [
					{ tag: 'cf', address: '1.1.1.1', detour: 'direct' },
					{ tag: 'local', address: '192.168.1.1', detour: 'direct' },
				],
				rules: [{ outbound: 'any', server: 'local' }],
				final: 'cf',
			},
			inbounds: [
				{ tag: 'tproxy-in', type: 'tproxy', listen: '::', listen_port: 51272, sniff: true },
			],
			outbounds: [
				{ tag: 'direct', type: 'direct' },
				{ tag: 'block', type: 'block' },
				{ tag: 'awg-tunnel-1', type: 'wireguard', server: '203.0.113.7', server_port: 51820 },
			],
			route: {
				rules: [
					{ action: 'sniff' },
					{ protocol: 'dns', action: 'hijack-dns' },
					{ rule_set: ['geoip-ru'], outbound: 'direct' },
					{ rule_set: ['geosite-tracker'], outbound: 'block' },
				],
				rule_set: [
					{ tag: 'geoip-ru', type: 'remote', format: 'binary', url: 'https://example/ru.srs' },
					{ tag: 'geosite-tracker', type: 'remote', format: 'binary', url: 'https://example/tracker.srs' },
				],
				final: 'awg-tunnel-1',
				auto_detect_interface: true,
			},
			experimental: {
				clash_api: { external_controller: '127.0.0.1:9090', external_ui: 'ui' },
				cache_file: { enabled: true, path: '/opt/etc/sing-box/cache.db' },
			},
		};
		send(res, 200, {
			success: true,
			data: { json: JSON.stringify(merged, null, 2) },
		});
		return;
	}

	if (req.method === 'POST' && path === '/singbox/install') {
		if (singboxInstallShouldFail) {
			send(res, 500, {
				success: false,
				error: FAKE_INSTALL_STDERR,
				code: 'SINGBOX_INSTALL_ERROR',
			});
			console.log('[mock-proxy] simulated /singbox/install failure');
			return;
		}
		// Falls through to the generic pass-through below.
	}

	// When the install-fail flag is on, also report sing-box as not-installed
	// so the Settings UI shows the "Установить" button (and clicking it hits
	// the /singbox/install override above with a 500 + fake stderr).
	if (req.method === 'GET' && path === '/singbox/status' && singboxInstallShouldFail) {
		fetchJSON(req.url).then(({ status, body }) => {
			if (body && typeof body === 'object' && body.data) {
				body.data.installed = false;
				body.data.running = false;
				body.data.pid = 0;
			}
			send(res, status, body);
		});
		return;
	}

	if (req.method === 'GET' && path === '/singbox/status') {
		send(res, 200, {
			success: true,
			data: {
				installed: true,
				version: '1.13.11',
				running: true,
				pid: 4242,
				tunnelCount: MOCK_SINGBOX_TUNNELS.length,
				proxyComponent: true,
				features: ['with_gvisor', 'with_quic', 'with_naive_outbound'],
			},
		});
		return;
	}

	if (req.method === 'GET' && path === '/system/hydraroute-status') {
		send(res, 200, { success: true, data: mockHydraRouteStatus });
		return;
	}

	// === Device Proxy mock overrides ===
	if (req.method === 'GET' && path === '/proxy/instances') {
		send(res, 200, { success: true, data: mockProxyInstances });
		return;
	}

	if (req.method === 'GET' && path === '/proxy/config') {
		const primary = mockProxyInstances.find((i) => i.id === 'default') ?? mockProxyInstances[0];
		send(res, 200, { success: true, data: {
			enabled: !!primary?.enabled,
			listenAll: !!primary?.listenAll,
			listenInterface: primary?.listenInterface ?? '',
			port: Number(primary?.port ?? 1099),
			auth: primary?.auth ?? { enabled: false, username: '', password: '' },
			selectedOutbound: primary?.selectedOutbound ?? 'proxy-01',
		}});
		return;
	}

	if (req.method === 'GET' && path === '/proxy/runtime') {
		send(res, 200, { success: true, data: mockProxyRuntimeByID.default });
		return;
	}

	if (req.method === 'GET' && path === '/proxy/instance/runtime') {
		const id = new URL(req.url, 'http://x').searchParams.get('id') || 'default';
		send(res, 200, {
			success: true,
			data: mockProxyRuntimeByID[id] ?? { alive: false, activeTag: '', defaultTag: '' },
		});
		return;
	}

	if (req.method === 'GET' && path === '/proxy/instance') {
		const id = new URL(req.url, 'http://x').searchParams.get('id') || 'default';
		const found = mockProxyInstances.find((i) => i.id === id);
		if (!found) {
			send(res, 404, { success: false, error: { code: 'NOT_FOUND', message: 'proxy instance not found' } });
			return;
		}
		send(res, 200, { success: true, data: found });
		return;
	}

	if (req.method === 'DELETE' && path === '/proxy/instance') {
		const id = new URL(req.url, 'http://x').searchParams.get('id');
		if (!id) {
			send(res, 400, { success: false, error: { code: 'MISSING_ID', message: 'missing id' } });
			return;
		}
		const idx = mockProxyInstances.findIndex((i) => i.id === id);
		if (idx === -1) {
			send(res, 404, { success: false, error: { code: 'NOT_FOUND', message: 'proxy instance not found' } });
			return;
		}
		mockProxyInstances.splice(idx, 1);
		delete mockProxyRuntimeByID[id];
		send(res, 200, { success: true, data: { deleted: true, applied: true } });
		return;
	}

	if (req.method === 'PUT' && path === '/proxy/instance') {
		let raw = '';
		req.on('data', (c) => (raw += c));
		req.on('end', () => {
			try {
				const body = JSON.parse(raw || '{}');
				const id = String(body.id || '');
				if (!id) {
					send(res, 400, { success: false, error: { code: 'MISSING_ID', message: 'instance id is empty' } });
					return;
				}
				const idx = mockProxyInstances.findIndex((i) => i.id === id);
				const next = {
					id,
					name: String(body.name || id),
					enabled: !!body.enabled,
					listenAll: !!body.listenAll,
					listenInterface: String(body.listenInterface || ''),
					port: Number(body.port ?? 1099),
					auth: body.auth ?? { enabled: false, username: '', password: '' },
					selectedOutbound: String(body.selectedOutbound || 'direct'),
				};
				if (idx === -1) {
					mockProxyInstances.push(next);
				} else {
					mockProxyInstances[idx] = next;
				}
				if (!mockProxyRuntimeByID[id]) {
					mockProxyRuntimeByID[id] = {
						alive: !!next.enabled,
						activeTag: next.selectedOutbound,
						defaultTag: next.selectedOutbound,
					};
				}
				send(res, 200, { success: true, data: next });
			} catch (e) {
				send(res, 400, { success: false, error: { code: 'INVALID_REQUEST', message: String(e) } });
			}
		});
		return;
	}

	if (req.method === 'POST' && path === '/proxy/instances/apply') {
		send(res, 200, { success: true, data: { applied: true } });
		return;
	}

	if (req.method === 'GET' && path === '/proxy/outbounds') {
		send(res, 200, {
			success: true,
			data: [
				{ tag: 'direct', kind: 'direct', label: 'Direct', detail: 'Без VPN' },
				{ tag: 'proxy-01', kind: 'singbox', label: 'DE vless-tcp-reality', detail: 't2s0 · 11000' },
				{ tag: 'proxy-02', kind: 'singbox', label: 'NL vless-grpc', detail: 't2s1 · 11001' },
			],
		});
		return;
	}

	if (req.method === 'GET' && path === '/proxy/listen-choices') {
		send(res, 200, {
			success: true,
			data: {
				lanIP: '192.168.1.1',
				bridges: [
					{ id: 'Home', label: 'Home', ip: '192.168.1.1' },
					{ id: 'Guest', label: 'Guest', ip: '192.168.77.1' },
				],
				singboxRunning: true,
			},
		});
		return;
	}

	if (req.method === 'POST' && path === '/proxy/instance/runtime/select') {
		let raw = '';
		req.on('data', (c) => (raw += c));
		req.on('end', () => {
			try {
				const id = new URL(req.url, 'http://x').searchParams.get('id') || 'default';
				const body = JSON.parse(raw || '{}');
				const tag = String(body.tag || '');
				if (!mockProxyRuntimeByID[id]) {
					mockProxyRuntimeByID[id] = { alive: false, activeTag: tag, defaultTag: tag };
				} else {
					mockProxyRuntimeByID[id].activeTag = tag;
					mockProxyRuntimeByID[id].defaultTag = tag;
				}
				const inst = mockProxyInstances.find((x) => x.id === id);
				if (inst) inst.selectedOutbound = tag;
				send(res, 200, { success: true, data: { active: tag } });
			} catch (e) {
				send(res, 400, { success: false, error: { code: 'INVALID_REQUEST', message: String(e) } });
			}
		});
		return;
	}

	if (req.method === 'POST' && path === '/singbox/tunnels/share-link') {
		const body = await readJsonBody(req);
		if (!body?.outbound || typeof body.outbound !== 'object') {
			send(res, 400, { error: true, message: 'outbound required', code: 'BAD_REQUEST' });
			return;
		}
		const type = body.outbound.type;
		if (!MOCK_SHARE_LINK_TYPES.has(type)) {
			send(res, 400, {
				error: true,
				message: `unsupported outbound type: ${type ?? 'unknown'}`,
				code: 'ENCODE_UNSUPPORTED',
			});
			return;
		}
		send(res, 200, {
			success: true,
			data: { link: `${mockShareLinkScheme(type)}://EXAMPLE_ENCODE` },
		});
		return;
	}

	if (req.method === 'GET' && path === '/singbox/tunnels') {
		send(res, 200, {
			success: true,
			data: MOCK_SINGBOX_TUNNELS.map((t) => ({ ...t })),
		});
		return;
	}

	if (req.method === 'POST' && path === '/__mock/singbox-install-fail') {
		try {
			const body = await readJsonBody(req);
			singboxInstallShouldFail = !!body.enabled;
			send(res, 200, { ok: true, singboxInstallShouldFail });
			console.log(`[mock-proxy] singboxInstallShouldFail → ${singboxInstallShouldFail}`);
		} catch (e) {
			send(res, 400, { error: String(e) });
		}
		return;
	}

	// === Wizard mock overrides ===

	if (req.method === 'GET' && path === '/presets') {
		const presets = await getUnifiedPresets();
		send(res, 200, { success: true, data: { presets } });
		return;
	}

	if (req.method === 'GET' && path === '/singbox/router/presets/list') {
		send(res, 200, { success: true, data: mockSingboxPresets });
		return;
	}

	if (req.method === 'GET' && path === '/dns-check/client') {
		send(res, 200, { success: true, data: mockDnsCheckClientPayload });
		return;
	}

	if (req.method === 'GET' && path === '/diagnostics/dns-proxy') {
		send(res, 200, { success: true, data: mockDnsProxyInfo });
		return;
	}

	if (req.method === 'POST' && path === '/dns-check/start') {
		send(res, 200, { success: true, data: mockDnsCheckClientPayload });
		return;
	}

	if (req.method === 'GET' && path === '/routing/policy-devices') {
		send(res, 200, { success: true, data: mockPolicyDevices });
		return;
	}

	if (req.method === 'GET' && path === '/routing/dns-routes') {
		send(res, 200, { success: true, data: mockRoutingDnsRoutes });
		return;
	}

	if (req.method === 'GET' && path === '/dns-routes/list') {
		sendData(res, mockRoutingDnsRoutes);
		return;
	}

	if (req.method === 'POST' && path === '/dns-routes/set-enabled') {
		try {
			const id = new URL(req.url, 'http://local').searchParams.get('id');
			const body = await readJsonBody(req);
			const found = findMockDnsRoute(id);
			if (!found) {
				send(res, 400, {
					success: false,
					error: { code: 'DNS_ROUTE_SET_ENABLED_ERROR', message: `dns route list ${JSON.stringify(id)} not found` },
				});
				return;
			}
			found.route.enabled = body.enabled !== false;
			touchMockDnsRoute(found.route);
			sendData(res, mockRoutingDnsRoutes);
		} catch (e) {
			sendInvalidRequest(res, e);
		}
		return;
	}

	if (req.method === 'POST' && path === '/dns-routes/update') {
		try {
			const id = new URL(req.url, 'http://local').searchParams.get('id');
			const body = await readJsonBody(req);
			const found = findMockDnsRoute(id);
			if (!found) {
				send(res, 400, {
					success: false,
					error: { code: 'DNS_ROUTE_UPDATE_ERROR', message: `dns route list ${JSON.stringify(id)} not found` },
				});
				return;
			}
			const updated = mergeMockDnsRoute(found.route, body);
			sendData(res, updated);
		} catch (e) {
			sendInvalidRequest(res, e);
		}
		return;
	}

	if (req.method === 'POST' && path === '/dns-routes/delete') {
		try {
			const id = new URL(req.url, 'http://local').searchParams.get('id');
			const found = findMockDnsRoute(id);
			if (!found) {
				send(res, 400, {
					success: false,
					error: { code: 'DNS_ROUTE_DELETE_ERROR', message: `dns route list ${JSON.stringify(id)} not found` },
				});
				return;
			}
			mockRoutingDnsRoutes.splice(found.idx, 1);
			sendData(res, mockRoutingDnsRoutes);
		} catch (e) {
			sendInvalidRequest(res, e);
		}
		return;
	}

	if (req.method === 'POST' && path === '/dns-routes/create') {
		try {
			const body = await readJsonBody(req);
			const name = String(body.name || '').trim();
			if (!name) {
				sendInvalidRequest(res, 'name is required');
				return;
			}
			const isHR = body.backend === 'hydraroute' || body.hrRouteMode;
			const id = isHR ? `hr:${name}` : `dns-${name.toLowerCase().replace(/\s+/g, '-')}`;
			if (findMockDnsRoute(id)) {
				send(res, 400, {
					success: false,
					error: { code: 'DNS_ROUTE_CREATE_ERROR', message: `dns route list ${JSON.stringify(id)} already exists` },
				});
				return;
			}
			const now = new Date().toISOString();
			const created = {
				id,
				name,
				domains: body.domains ?? body.manualDomains ?? [],
				subnets: body.subnets ?? [],
				manualDomains: body.manualDomains ?? body.domains ?? [],
				routes: body.routes ?? [],
				enabled: body.enabled !== false,
				backend: isHR ? 'hydraroute' : body.backend,
				hrRouteMode: body.hrRouteMode,
				hrPolicyName: body.hrPolicyName,
				hrPolicyInterfaces: body.hrPolicyInterfaces,
				iconUrl: body.iconUrl,
				createdAt: now,
				updatedAt: now,
			};
			mockRoutingDnsRoutes.push(created);
			sendData(res, created);
		} catch (e) {
			sendInvalidRequest(res, e);
		}
		return;
	}

	if (req.method === 'GET' && path === '/routing/static-routes') {
		send(res, 200, { success: true, data: mockStaticRoutes });
		return;
	}

	if (req.method === 'GET' && path === '/routing/client-routes') {
		send(res, 200, { success: true, data: mockClientRoutes });
		return;
	}

	if (req.method === 'GET' && path === '/routing/access-policies') {
		send(res, 200, { success: true, data: mockAccessPolicies });
		return;
	}

	if (req.method === 'GET' && path === '/managed-servers/policies') {
		const data = mockAccessPolicies
			.filter((p) => p.isStandard !== false && isStandardPolicyName(p.name))
			.map((p) => ({ id: p.name, description: p.description }));
		send(res, 200, { success: true, data });
		return;
	}

	const managedAscMatch = path.match(/^\/managed-servers\/([^/]+)\/asc$/);
	if (managedAscMatch) {
		const serverId = decodeURIComponent(managedAscMatch[1]);
		if (req.method === 'GET') {
			const params = mockManagedAscByServer[serverId] ?? mockZeroASC();
			send(res, 200, { success: true, data: shapeASCForMockProfile(params) });
			return;
		}
		if (req.method === 'PUT') {
			let raw = '';
			req.on('data', (c) => (raw += c));
			req.on('end', () => {
				try {
					const body = JSON.parse(raw || '{}');
					mockManagedAscByServer[serverId] = { ...mockZeroASC(), ...body };
					send(res, 200, { success: true, data: buildMockServersAllData() });
				} catch (e) {
					send(res, 400, { success: false, error: { code: 'INVALID_REQUEST', message: String(e) } });
				}
			});
			return;
		}
	}

	if (req.method === 'GET' && path === '/routing/policy-interfaces') {
		send(res, 200, { success: true, data: buildMockPolicyInterfaces() });
		return;
	}

	if (req.method === 'GET' && path === '/routing/tunnels') {
		send(res, 200, { success: true, data: buildMockRoutingTunnels() });
		return;
	}

	if (req.method === 'GET' && path === '/singbox/router/rules/list') {
		send(res, 200, { success: true, data: mockSingboxRules });
		return;
	}

	if (req.method === 'GET' && path === '/singbox/router/rulesets/list') {
		send(res, 200, { success: true, data: mockSingboxRuleSets });
		return;
	}

	if (req.method === 'GET' && path === '/singbox/router/policies') {
		const policies = mockSBPolicyExists ? [{ name: 'SBRouter', description: 'wizard' }] : [];
		send(res, 200, { success: true, data: policies });
		return;
	}

	if (req.method === 'POST' && path === '/singbox/router/policies') {
		mockSBPolicyExists = true;
		send(res, 200, { success: true, data: { name: 'SBRouter', description: 'wizard' } });
		return;
	}

	if (req.method === 'GET' && path === '/singbox/router/wan-interfaces') {
		send(res, 200, { success: true, data: mockWANInterfaces });
		return;
	}

	if (req.method === 'GET' && path === '/singbox/router/bindable-interfaces') {
		send(res, 200, { success: true, data: mockBindableInterfaces });
		return;
	}

	if (req.method === 'GET' && path === '/singbox/fakeip/segments') {
		// Ground-truth segment state — mutated by the POST toggle below.
		// Strip the internal lanDNS bookkeeping field from the DTO. The
		// envelope also carries the read-only fakeip-tun gateway addresses
		// (Inbounds tun-in card) — same DefaultFakeIPTunParams as the backend.
		send(res, 200, {
			success: true,
			data: {
				tunAddr4: '172.18.0.1/30',
				tunAddr6: 'fdfe:dcba:9876::1/126',
				tunDns: FAKEIP_TUN_DNS,
				segments: mockFakeIPSegments.map(({ lanDNS, ...seg }) => seg),
			},
		});
		return;
	}

	if (req.method === 'POST' && path === '/singbox/fakeip/segments') {
		// Toggle one pool's DNS delivery (Task 2.2). Mutate the same in-memory
		// state the GET reads so a refetch reflects the change. Set → advertise
		// the fakeip-tun DNS (.2); clear → fall back to the pool's LAN DNS.
		let raw = '';
		req.on('data', (c) => (raw += c));
		req.on('end', () => {
			let payload;
			try {
				payload = JSON.parse(raw || '{}');
			} catch {
				send(res, 400, { success: false, error: 'invalid request body' });
				return;
			}
			const pool = payload.pool;
			if (!pool) {
				send(res, 400, { success: false, error: 'pool is required' });
				return;
			}
			const seg = mockFakeIPSegments.find((s) => s.pool === pool);
			if (!seg) {
				send(res, 500, { success: false, error: `set DNS delivery for pool ${pool}: not found` });
				return;
			}
			seg.inFakeip = !!payload.inFakeip;
			seg.dnsServer = seg.inFakeip ? FAKEIP_TUN_DNS : seg.lanDNS;
			send(res, 200, { success: true, data: { pool, inFakeip: seg.inFakeip } });
		});
		return;
	}

	if (req.method === 'GET' && path === '/singbox/router/settings') {
		send(res, 200, { success: true, data: mockSBSettings });
		return;
	}

	if ((req.method === 'POST' || req.method === 'PUT') && path === '/singbox/router/settings') {
		let raw = '';
		req.on('data', (c) => (raw += c));
		req.on('end', () => {
			try {
				const payload = JSON.parse(raw || '{}');
				// Mirror backend's ValidateSingboxRouterSettings — reject the
				// two contradictory discriminator combos. Keeps the dev UI
				// honest: a toggle bug that would produce {auto:true, iface:"x"}
				// surfaces here instead of silently saving an invalid state.
				if (payload.wanAutoDetect === true && payload.wanInterface) {
					send(res, 400, {
						success: false,
						error: {
							code: 'INVALID_REQUEST',
							message: `wanAutoDetect=true requires wanInterface to be empty (got "${payload.wanInterface}")`,
						},
					});
					return;
				}
				if (payload.wanAutoDetect === false && !payload.wanInterface) {
					send(res, 400, {
						success: false,
						error: {
							code: 'INVALID_REQUEST',
							message: 'wanAutoDetect=false requires wanInterface to be set to a kernel interface name',
						},
					});
					return;
				}
				mockSBSettings = { ...mockSBSettings, ...payload };
				send(res, 200, { success: true, data: { ok: true } });
			} catch (e) {
				send(res, 400, { success: false, error: { code: 'INVALID_REQUEST', message: String(e) } });
			}
		});
		return;
	}

	if (req.method === 'POST' && path === '/access-policies/assign') {
		let raw = '';
		req.on('data', (c) => (raw += c));
		req.on('end', () => {
			try {
				const payload = JSON.parse(raw || '{}');
				const mac = payload.mac;
				const policy = payload.policy ?? '';
				if (policy && !isStandardPolicyName(policy)) {
					send(res, 400, {
						success: false,
						error: {
							code: 'INVALID_REQUEST',
							message: `policy "${policy}" is managed by HydraRoute Neo and cannot be modified here`,
						},
					});
					return;
				}
				if (mac) {
					mockBoundDevices.add(mac);
					const dev = mockPolicyDevices.find((d) => d.mac === mac);
					if (dev) dev.policy = policy;
					const pol = mockAccessPolicies.find((p) => p.name === policy);
					if (pol) pol.deviceCount = mockPolicyDevices.filter((d) => d.policy === policy).length;
				}
				send(res, 200, { success: true, data: {} });
			} catch (e) {
				send(res, 400, { success: false, error: { code: 'INVALID_REQUEST', message: String(e) } });
			}
		});
		return;
	}

	if (req.method === 'DELETE' && path === '/access-policies/assign') {
		const mac = url.searchParams.get('mac');
		if (mac) {
			const dev = mockPolicyDevices.find((d) => d.mac === mac);
			const prev = dev?.policy ?? '';
			if (dev) dev.policy = '';
			if (prev) {
				const pol = mockAccessPolicies.find((p) => p.name === prev);
				if (pol) pol.deviceCount = mockPolicyDevices.filter((d) => d.policy === prev).length;
			}
		}
		send(res, 200, { success: true, data: {} });
		return;
	}

	if (req.method === 'DELETE' && path === '/access-policies/delete') {
		const name = url.searchParams.get('name') ?? '';
		if (!isStandardPolicyName(name)) {
			send(res, 400, {
				success: false,
				error: {
					code: 'INVALID_REQUEST',
					message: `policy "${name}" is managed by HydraRoute Neo and cannot be modified here`,
				},
			});
			return;
		}
		const idx = mockAccessPolicies.findIndex((p) => p.name === name);
		if (idx >= 0) mockAccessPolicies.splice(idx, 1);
		for (const dev of mockPolicyDevices) {
			if (dev.policy === name) dev.policy = '';
		}
		send(res, 200, { success: true, data: {} });
		return;
	}

	if (req.method === 'GET' && path === '/singbox/router/dns/servers/list') {
		send(res, 200, { success: true, data: mockDNSServers.map(scrubMockDnsServerStored) });
		return;
	}

	if (req.method === 'POST' && path === '/singbox/router/dns/servers/add') {
		let raw = '';
		req.on('data', (c) => (raw += c));
		req.on('end', () => {
			try {
				const payload = sanitizeMockDnsServerForWrite(JSON.parse(raw || '{}'));
				mockDNSServers.push(payload);
				send(res, 200, { success: true, data: payload });
			} catch (e) {
				send(res, 400, { success: false, error: { code: 'INVALID_REQUEST', message: String(e) } });
			}
		});
		return;
	}

	if (req.method === 'POST' && path === '/singbox/router/dns/servers/update') {
		let raw = '';
		req.on('data', (c) => (raw += c));
		req.on('end', () => {
			try {
				const { tag, server } = JSON.parse(raw || '{}');
				const idx = mockDNSServers.findIndex((s) => s.tag === tag);
				if (idx === -1) {
					send(res, 404, { success: false, error: { code: 'NOT_FOUND', message: 'dns server not found' } });
					return;
				}
				mockDNSServers[idx] = sanitizeMockDnsServerForWrite(server);
				send(res, 200, { success: true, data: { ok: true } });
			} catch (e) {
				send(res, 400, { success: false, error: { code: 'INVALID_REQUEST', message: String(e) } });
			}
		});
		return;
	}

	if (req.method === 'POST' && path === '/singbox/router/dns/servers/delete') {
		let raw = '';
		req.on('data', (c) => (raw += c));
		req.on('end', () => {
			try {
				const { tag } = JSON.parse(raw || '{}');
				const idx = mockDNSServers.findIndex((s) => s.tag === tag);
				if (idx === -1) {
					send(res, 404, { success: false, error: { code: 'NOT_FOUND', message: 'dns server not found' } });
					return;
				}
				const refs = [];
				for (let i = 0; i < mockDNSRules.length; i++) {
					if (mockDNSRules[i]?.server === tag) refs.push(`rule[${i}]`);
				}
				for (const s of mockDNSServers) {
					if (s.tag !== tag && s.domain_resolver?.server === tag) {
						refs.push(`server[${s.tag}].domain_resolver`);
					}
				}
				if (refs.length > 0) {
					send(res, 409, {
						success: false,
						error: { code: 'CONFLICT', message: `dns server "${tag}" referenced by ${refs.join(', ')}` },
					});
					return;
				}
				mockDNSServers.splice(idx, 1);
				send(res, 200, { success: true, data: { ok: true } });
			} catch (e) {
				send(res, 400, { success: false, error: { code: 'INVALID_REQUEST', message: String(e) } });
			}
		});
		return;
	}

	if (req.method === 'POST' && path === '/singbox/router/dns/servers/move') {
		let raw = '';
		req.on('data', (c) => (raw += c));
		req.on('end', () => {
			try {
				const { from, to } = JSON.parse(raw || '{}');
				if (from < 0 || from >= mockDNSServers.length || to < 0 || to >= mockDNSServers.length) {
					send(res, 404, { success: false, error: { code: 'NOT_FOUND', message: 'dns server not found' } });
					return;
				}
				const [moved] = mockDNSServers.splice(from, 1);
				mockDNSServers.splice(to, 0, moved);
				send(res, 200, { success: true, data: { ok: true } });
			} catch (e) {
				send(res, 400, { success: false, error: { code: 'INVALID_REQUEST', message: String(e) } });
			}
		});
		return;
	}

	if (req.method === 'POST' && path === '/singbox/router/presets/apply') {
		// simulate latency for visible "Применяем" log
		await wait(200);
		send(res, 200, { success: true, data: {} });
		return;
	}

	// Step-1 DNS-branch inspector (fakeip). Returns a representative fakeip
	// classification with a pool so the inspector modal renders Step 1 on
	// dev:mock. domain==="8.8.8.8" (or any IP) → "real" so the alternate
	// verdict path is also reachable; *.lan → "local".
	if (req.method === 'POST' && path === '/singbox/router/inspect-dns') {
		let raw = '';
		req.on('data', (c) => (raw += c));
		req.on('end', () => {
			let domain = 'discord.com';
			let queryType = '';
			try {
				const p = JSON.parse(raw || '{}');
				if (p.domain) domain = String(p.domain);
				queryType = p.queryType || '';
			} catch {}
			const isIP = /^[0-9]{1,3}(\.[0-9]{1,3}){3}$/.test(domain);
			const isLan = /\.lan$/i.test(domain);
			let classification = 'fakeip';
			let server = 'fakeip';
			let pool = '198.18.0.0/15, fc00::/18';
			let matchedRule = 1;
			if (isIP) {
				classification = 'real';
				server = 'dns-real';
				pool = '';
				matchedRule = -1;
			} else if (isLan) {
				classification = 'local';
				server = 'dns-local';
				pool = '';
				matchedRule = 0;
			}
			const matches = [
				{
					index: 0,
					matched: isLan,
					server: isLan ? 'dns-local' : 'fakeip',
					conditions: [`domain: [${isLan ? domain : 'router.lan'}]`],
					reason: isLan ? 'совпало по: домен' : 'нет совпадения',
				},
				{
					index: 1,
					matched: !isIP && !isLan,
					server: 'fakeip',
					conditions: ['query_type: [A, AAAA]', `domain_suffix: [${domain}]`],
					reason: !isIP && !isLan ? 'совпало по: домен, query_type' : 'нет совпадения',
				},
			];
			send(res, 200, {
				success: true,
				data: {
					input: domain,
					inputType: isIP ? 'ip' : 'domain',
					matches,
					matchedRule,
					server,
					classification,
					pool,
					final: isIP ? 'dns-real' : 'fakeip',
					note: queryType === 'HTTPS' ? 'демо: query_type HTTPS не сматчил fakeip-правило' : '',
				},
			});
		});
		return;
	}

	if (req.method === 'GET' && path === '/singbox/router/dns/rules/list') {
		send(res, 200, { success: true, data: mockDNSRules });
		return;
	}

	if (req.method === 'POST' && path === '/singbox/router/dns/rules/add') {
		let raw = '';
		req.on('data', (c) => (raw += c));
		req.on('end', () => {
			try {
				const payload = JSON.parse(raw || '{}');
				mockDNSRules.push(payload);
				send(res, 200, { success: true, data: payload });
			} catch (e) {
				send(res, 400, { success: false, error: { code: 'INVALID_REQUEST', message: String(e) } });
			}
		});
		return;
	}

	if (req.method === 'POST' && path === '/singbox/router/dns/rules/update') {
		let raw = '';
		req.on('data', (c) => (raw += c));
		req.on('end', () => {
			try {
				const { index, rule } = JSON.parse(raw || '{}');
				if (index < 0 || index >= mockDNSRules.length) {
					send(res, 404, { success: false, error: { code: 'NOT_FOUND', message: 'dns rule not found' } });
					return;
				}
				mockDNSRules[index] = rule;
				send(res, 200, { success: true, data: { ok: true } });
			} catch (e) {
				send(res, 400, { success: false, error: { code: 'INVALID_REQUEST', message: String(e) } });
			}
		});
		return;
	}

	if (req.method === 'POST' && path === '/singbox/router/dns/rules/delete') {
		let raw = '';
		req.on('data', (c) => (raw += c));
		req.on('end', () => {
			try {
				const { index } = JSON.parse(raw || '{}');
				if (index < 0 || index >= mockDNSRules.length) {
					send(res, 404, { success: false, error: { code: 'NOT_FOUND', message: 'dns rule not found' } });
					return;
				}
				mockDNSRules.splice(index, 1);
				send(res, 200, { success: true, data: { ok: true } });
			} catch (e) {
				send(res, 400, { success: false, error: { code: 'INVALID_REQUEST', message: String(e) } });
			}
		});
		return;
	}

	if (req.method === 'POST' && path === '/singbox/router/dns/rules/move') {
		let raw = '';
		req.on('data', (c) => (raw += c));
		req.on('end', () => {
			try {
				const { from, to } = JSON.parse(raw || '{}');
				if (from < 0 || from >= mockDNSRules.length || to < 0 || to >= mockDNSRules.length) {
					send(res, 404, { success: false, error: { code: 'NOT_FOUND', message: 'dns rule not found' } });
					return;
				}
				const [moved] = mockDNSRules.splice(from, 1);
				mockDNSRules.splice(to, 0, moved);
				send(res, 200, { success: true, data: { ok: true } });
			} catch (e) {
				send(res, 400, { success: false, error: { code: 'INVALID_REQUEST', message: String(e) } });
			}
		});
		return;
	}

	if (req.method === 'GET' && path === '/singbox/router/dns/rewrites/list') {
		send(res, 200, { success: true, data: mockDNSRewrites });
		return;
	}

	if (req.method === 'POST' && path === '/singbox/router/dns/rewrites/add') {
		let raw = '';
		req.on('data', (c) => (raw += c));
		req.on('end', () => {
			try {
				const payload = JSON.parse(raw || '{}');
				mockDNSRewrites.push(payload);
				send(res, 200, { success: true, data: payload });
			} catch (e) {
				send(res, 400, { success: false, error: { code: 'INVALID_REQUEST', message: String(e) } });
			}
		});
		return;
	}

	if (req.method === 'POST' && path === '/singbox/router/dns/rewrites/update') {
		let raw = '';
		req.on('data', (c) => (raw += c));
		req.on('end', () => {
			try {
				const { index, rewrite } = JSON.parse(raw || '{}');
				if (index < 0 || index >= mockDNSRewrites.length) {
					send(res, 404, { success: false, error: { code: 'NOT_FOUND', message: 'rewrite not found' } });
					return;
				}
				mockDNSRewrites[index] = rewrite;
				send(res, 200, { success: true, data: rewrite });
			} catch (e) {
				send(res, 400, { success: false, error: { code: 'INVALID_REQUEST', message: String(e) } });
			}
		});
		return;
	}

	if (req.method === 'POST' && path === '/singbox/router/dns/rewrites/delete') {
		let raw = '';
		req.on('data', (c) => (raw += c));
		req.on('end', () => {
			try {
				const { index } = JSON.parse(raw || '{}');
				if (index < 0 || index >= mockDNSRewrites.length) {
					send(res, 404, { success: false, error: { code: 'NOT_FOUND', message: 'rewrite not found' } });
					return;
				}
				mockDNSRewrites.splice(index, 1);
				send(res, 200, { success: true, data: { ok: true } });
			} catch (e) {
				send(res, 400, { success: false, error: { code: 'INVALID_REQUEST', message: String(e) } });
			}
		});
		return;
	}

	if (req.method === 'POST' && path === '/singbox/router/dns/rewrites/move') {
		let raw = '';
		req.on('data', (c) => (raw += c));
		req.on('end', () => {
			try {
				const { from, to } = JSON.parse(raw || '{}');
				if (from < 0 || from >= mockDNSRewrites.length || to < 0 || to >= mockDNSRewrites.length) {
					send(res, 404, { success: false, error: { code: 'NOT_FOUND', message: 'rewrite not found' } });
					return;
				}
				const [moved] = mockDNSRewrites.splice(from, 1);
				mockDNSRewrites.splice(to, 0, moved);
				send(res, 200, { success: true, data: { ok: true } });
			} catch (e) {
				send(res, 400, { success: false, error: { code: 'INVALID_REQUEST', message: String(e) } });
			}
		});
		return;
	}

	if (req.method === 'GET' && path === '/singbox/router/dns/globals') {
		send(res, 200, { success: true, data: mockDNSGlobals });
		return;
	}

	if (req.method === 'PUT' && path === '/singbox/router/dns/globals') {
		let raw = '';
		req.on('data', (c) => (raw += c));
		req.on('end', () => {
			try {
				const payload = JSON.parse(raw || '{}');
				mockDNSGlobals = {
					final: payload.final ?? mockDNSGlobals.final,
					strategy: payload.strategy ?? mockDNSGlobals.strategy,
				};
				send(res, 200, { success: true, data: { ok: true } });
			} catch (e) {
				send(res, 400, { success: false, error: { code: 'INVALID_REQUEST', message: String(e) } });
			}
		});
		return;
	}

	if (req.method === 'POST' && path === '/singbox/router/enable') {
		mockEngineRunning = true;
		send(res, 200, { success: true, data: {} });
		return;
	}

	// POST /singbox/router/mode — orchestrated routing-mode switch. Replays a
	// representative singbox-router:transition progress sequence to all live SSE
	// subscribers (the UI progress screen), then updates the mock router state.
	if (req.method === 'POST' && path === '/singbox/router/mode') {
		const payload = await readJsonBody(req);
		const to = payload && payload.mode;
		if (to !== 'off' && to !== 'tproxy' && to !== 'fakeip-tun') {
			send(res, 400, { success: false, error: 'invalid routing mode (want off|tproxy|fakeip-tun)', code: 'INVALID_MODE' });
			return;
		}
		const from = mockEngineRunning ? (mockSBSettings.routingMode || 'tproxy') : 'off';
		const seq = buildTransitionSequence(from, to);
		// Stagger the push so the FE progress screen animates step-by-step.
		seq.forEach((evt, i) => {
			setTimeout(() => {
				for (const sendEvent of sseSenders) sendEvent('singbox-router:transition', evt);
			}, i * 400);
		});
		mockEngineRunning = to !== 'off';
		mockSBSettings = { ...mockSBSettings, routingMode: to === 'off' ? mockSBSettings.routingMode : to, enabled: to !== 'off' };
		send(res, 200, { success: true, data: { ok: true } });
		return;
	}

	if (req.method === 'GET' && path === '/singbox/router/status') {
		const routingMode = mockSBSettings.routingMode || 'tproxy';
		send(res, 200, {
			success: true,
			data: {
				enabled: mockEngineRunning,
				installed: true,
				running: mockEngineRunning,
				version: '1.13.11',
				configValid: true,
				netfilterAvailable: true,
				policyName: mockSBPolicyExists ? 'SBRouter' : '',
				deviceMode: mockSBSettings.deviceMode || 'policy',
				ruleCount: mockSingboxRules.length,
				ruleSetCount: mockSingboxRuleSets.length,
				// fakeip-tun status fields (backend serializes all omitempty).
				routingMode,
				// fakeipEgressUp: global egress-health (Task 25). Default true. Flip to
				// false here to demo the SegmentsDelivery «доставка DNS придержана» banner.
				...(routingMode === 'fakeip-tun' ? { sourcePreserved: true, fakeipSourcePreserve: true, fakeipIface: 'opkgtun0', fakeipEgressUp: true } : {}),
			},
		});
		return;
	}

	if (req.method === 'GET' && path === '/singbox/awg-outbounds/tags') {
		send(res, 200, {
			success: true,
			data: [
				{ tag: 'awg-vpn0',             label: 'DE Frankfurt', kind: 'managed', iface: 'awg0' },
				{ tag: 'awg-sys-Wireguard0',   label: 'NL Amsterdam', kind: 'system',  iface: 'nwg0' },
				{ tag: 'awg-sys-Wireguard1',   label: 'FI Helsinki',  kind: 'system',  iface: 'nwg1' },
			],
		});
		return;
	}

	if (req.method === 'GET' && path === '/singbox/router/outbounds/list') {
		send(res, 200, { success: true, data: mockOutbounds });
		return;
	}

	if (req.method === 'POST' && path === '/singbox/router/outbounds/add') {
		let raw = '';
		req.on('data', (c) => (raw += c));
		req.on('end', () => {
			try {
				const o = JSON.parse(raw || '{}');
				if (mockOutbounds.some((x) => x.tag === o.tag)) {
					send(res, 400, { success: false, error: { code: 'CONFLICT', message: `tag ${o.tag} exists` } });
					return;
				}
				mockOutbounds.push({ ...o, source: 'router' });
				send(res, 200, { success: true, data: { ok: true } });
			} catch (e) {
				send(res, 400, { success: false, error: { code: 'INVALID_REQUEST', message: String(e) } });
			}
		});
		return;
	}

	if (req.method === 'POST' && path === '/singbox/router/outbounds/update') {
		let raw = '';
		req.on('data', (c) => (raw += c));
		req.on('end', () => {
			try {
				const { tag, outbound } = JSON.parse(raw || '{}');
				const idx = mockOutbounds.findIndex((x) => x.tag === tag);
				if (idx < 0) {
					send(res, 404, { success: false, error: { code: 'NOT_FOUND', message: `tag ${tag} not found` } });
					return;
				}
				mockOutbounds[idx] = { ...outbound, source: 'router' };
				send(res, 200, { success: true, data: { ok: true } });
			} catch (e) {
				send(res, 400, { success: false, error: { code: 'INVALID_REQUEST', message: String(e) } });
			}
		});
		return;
	}

	if (req.method === 'POST' && path === '/singbox/router/outbounds/delete') {
		let raw = '';
		req.on('data', (c) => (raw += c));
		req.on('end', () => {
			try {
				const { tag } = JSON.parse(raw || '{}');
				mockOutbounds = mockOutbounds.filter((x) => x.tag !== tag);
				send(res, 200, { success: true, data: { ok: true } });
			} catch (e) {
				send(res, 400, { success: false, error: { code: 'INVALID_REQUEST', message: String(e) } });
			}
		});
		return;
	}

	// === Subscriptions mock overrides ===

	if (req.method === 'GET' && path === '/singbox/subscriptions') {
		send(res, 200, {
			success: true,
			data: mockSubscriptions.map(toMockSubscriptionDTO),
		});
		return;
	}

	// Mock subscription "external" URL — return a Clash YAML body.
	// Used by the mock-Create code path: when CreateInput.URL points at this,
	// the mock backend pretends the upstream provider returned this YAML.
	if (req.method === 'GET' && path === '/__mock__/clash-subscription.yaml') {
		res.writeHead(200, {
			'Content-Type': 'application/x-yaml',
			'Access-Control-Allow-Origin': '*',
		});
		res.end(`proxies:
  - name: "🇺🇸 LA-1 (mock)"
    type: vless
    server: la1.mock.local
    port: 443
    uuid: 3a3b1c2e-9999-4321-aaaa-1234567890ab
    tls: true
    servername: la1.mock.local
    network: ws
    ws-opts:
      path: /v
      headers:
        Host: la1.mock.local
  - name: "🇩🇪 FRA-1 (mock)"
    type: vless
    server: fra1.mock.local
    port: 443
    uuid: 4a4b1c2e-9999-4321-aaaa-1234567890ab
    tls: true
    servername: fra1.mock.local
  - name: "🇯🇵 TYO-1 (mock)"
    type: trojan
    server: tyo1.mock.local
    port: 443
    password: trpass
    sni: tyo1.mock.local
`);
		return;
	}

	if (req.method === 'POST' && path === '/singbox/subscriptions/create') {
		let raw = '';
		req.on('data', (c) => (raw += c));
		req.on('end', () => {
			try {
				const body = JSON.parse(raw || '{}');
				const sub = newSub(body);
				if (String(body.url || '').endsWith('/__mock__/clash-subscription.yaml')) {
					const shortID = sub.id.slice(0, 8);
					const tags = [`sub-${shortID}-c001`, `sub-${shortID}-c002`, `sub-${shortID}-c003`];
					sub.memberTags = tags;
					sub.members = [
						{ tag: tags[0], label: '🇺🇸 LA-1 (mock)', protocol: 'vless', server: 'la1.mock.local', port: 443, transport: 'ws', security: 'tls' },
						{ tag: tags[1], label: '🇩🇪 FRA-1 (mock)', protocol: 'vless', server: 'fra1.mock.local', port: 443, transport: 'tcp', security: 'tls' },
						{ tag: tags[2], label: '🇯🇵 TYO-1 (mock)', protocol: 'trojan', server: 'tyo1.mock.local', port: 443, transport: 'tcp', security: 'tls' },
					];
					sub.activeMember = '';
					sub.orphanTags = [];
				}
				mockSubscriptions.push(sub);
				send(res, 200, { success: true, data: toMockSubscriptionDTO(sub) });
			} catch (e) {
				send(res, 400, { success: false, error: { code: 'INVALID_REQUEST', message: String(e) } });
			}
		});
		return;
	}

	if (req.method === 'GET' && path === '/singbox/subscriptions/get') {
		const id = new URL(req.url, 'http://x').searchParams.get('id');
		const sub = mockSubscriptions.find((s) => s.id === id);
		if (!sub) {
			send(res, 404, { success: false, error: { code: 'NOT_FOUND', message: 'no such id' } });
			return;
		}
		send(res, 200, { success: true, data: toMockSubscriptionDTO(sub) });
		return;
	}

	// SSE stream — meta + member×N + done with 200ms delay between members
	// for visible UX progress in dev. Production backend has no artificial delay.
	if (req.method === 'GET' && path === '/singbox/subscriptions/get-stream') {
		const id = new URL(req.url, 'http://x').searchParams.get('id');
		const sub = mockSubscriptions.find((s) => s.id === id);
		if (!sub) {
			send(res, 404, { success: false, error: { code: 'NOT_FOUND', message: 'subscription not found' } });
			return;
		}
		res.writeHead(200, {
			'Content-Type': 'text/event-stream',
			'Cache-Control': 'no-cache',
			'Connection': 'keep-alive',
			'X-Accel-Buffering': 'no',
		});

		const meta = {
			id: sub.id,
			label: sub.label,
			url: sub.url,
			isInline: !sub.url,
			headers: sub.headers ?? [],
			refreshHours: sub.refreshHours ?? 0,
			lastFetched: sub.lastFetched ?? '',
			lastError: sub.lastError ?? '',
			selectorTag: sub.selectorTag,
			inboundTag: sub.inboundTag,
			listenPort: sub.listenPort,
			proxyIndex: sub.proxyIndex,
			enabled: sub.enabled,
			mode: sub.mode ?? 'selector',
			urlTest: sub.urlTest,
			total: (sub.members ?? []).length,
			rejectedMembers: sub.rejectedMembers ?? [],
			infoItems: sub.infoItems ?? [],
		};
		res.write(`event: meta\ndata: ${JSON.stringify(meta)}\n\n`);

		const members = sub.members ?? [];
		let i = 0;
		const tick = () => {
			if (i >= members.length) {
				const done = {
					orphanTags: sub.orphanTags ?? [],
					rejectedMembers: sub.rejectedMembers ?? [],
					infoItems: sub.infoItems ?? [],
					activeMember: sub.activeMember ?? '',
				};
				res.write(`event: done\ndata: ${JSON.stringify(done)}\n\n`);
				res.end();
				return;
			}
			res.write(`event: member\ndata: ${JSON.stringify({ index: i, member: members[i] })}\n\n`);
			i += 1;
			setTimeout(tick, 200);
		};
		setTimeout(tick, 0);
		return;
	}

	if (req.method === 'POST' && path === '/singbox/subscriptions/refresh') {
		const id = new URL(req.url, 'http://x').searchParams.get('id');
		const sub = mockSubscriptions.find((s) => s.id === id);
		if (sub) sub.lastFetched = new Date().toISOString();
		send(res, 200, {
			success: true,
			data: {
				when: new Date().toISOString(),
				added: 0,
				updated: 2,
				orphaned: 0,
				skippedVmess: 1,
				skippedOther: 0,
			},
		});
		return;
	}

	if (req.method === 'DELETE' && path === '/singbox/subscriptions/delete') {
		const id = new URL(req.url, 'http://x').searchParams.get('id');
		mockSubscriptions = mockSubscriptions.filter((s) => s.id !== id);
		send(res, 200, { success: true, data: { ok: true } });
		return;
	}

	if (req.method === 'PUT' && path === '/singbox/subscriptions/update') {
		let raw = '';
		req.on('data', (c) => (raw += c));
		req.on('end', () => {
			try {
				const body = JSON.parse(raw || '{}');
				const id = new URL(req.url, 'http://x').searchParams.get('id');
				const sub = mockSubscriptions.find((s) => s.id === id);
				if (!sub) {
					send(res, 404, { success: false, error: { code: 'NOT_FOUND', message: 'no such id' } });
					return;
				}
				Object.assign(sub, body);
				send(res, 200, { success: true, data: toMockSubscriptionDTO(sub) });
			} catch (e) {
				send(res, 400, { success: false, error: { code: 'INVALID_REQUEST', message: String(e) } });
			}
		});
		return;
	}

	// Live "active now" — for urltest mode, simulate auto-switching by rotating
	// through members based on time. For selector, return persisted activeMember.
	if (req.method === 'GET' && path === '/singbox/subscriptions/active-now') {
		const id = new URL(req.url, 'http://x').searchParams.get('id');
		const sub = mockSubscriptions.find((s) => s.id === id);
		if (!sub) {
			send(res, 404, { success: false, error: { code: 'NOT_FOUND', message: 'subscription not found' } });
			return;
		}
		let now = sub.activeMember || '';
		if (sub.mode === 'urltest' && sub.memberTags && sub.memberTags.length > 0) {
			// Rotate every 15 seconds — visible auto-switching for testing.
			const idx = Math.floor(Date.now() / 15000) % sub.memberTags.length;
			now = sub.memberTags[idx];
		}
		send(res, 200, { success: true, data: { now } });
		return;
	}

	if (req.method === 'POST' && path === '/singbox/subscriptions/rejected/to-info') {
		let raw = '';
		req.on('data', (c) => (raw += c));
		req.on('end', () => {
			try {
				const body = JSON.parse(raw || '{}');
				const id = new URL(req.url, 'http://x').searchParams.get('id');
				const sub = mockSubscriptions.find((s) => s.id === id);
				if (!sub) {
					send(res, 404, { success: false, error: { code: 'NOT_FOUND', message: 'no such id' } });
					return;
				}
				const result = moveMockRejectedToInfo(sub, body.memberTag);
				if (result.error) {
					send(res, result.error.status, {
						success: false,
						error: { code: result.error.code, message: result.error.message },
					});
					return;
				}
				send(res, 200, { success: true, data: toMockSubscriptionDTO(sub) });
			} catch (e) {
				send(res, 400, { success: false, error: { code: 'INVALID_REQUEST', message: String(e) } });
			}
		});
		return;
	}

	if (req.method === 'POST' && path === '/singbox/subscriptions/info/remove') {
		let raw = '';
		req.on('data', (c) => (raw += c));
		req.on('end', () => {
			try {
				const body = JSON.parse(raw || '{}');
				const id = new URL(req.url, 'http://x').searchParams.get('id');
				const sub = mockSubscriptions.find((s) => s.id === id);
				if (!sub) {
					send(res, 404, { success: false, error: { code: 'NOT_FOUND', message: 'no such id' } });
					return;
				}
				const itemId = String(body.itemId || '').trim();
				const items = sub.infoItems ?? [];
				const idx = items.findIndex((it) => it.id === itemId);
				if (idx < 0) {
					send(res, 404, {
						success: false,
						error: { code: 'INFO_ITEM_NOT_FOUND', message: 'info item not found' },
					});
					return;
				}
				const removed = items[idx];
				const removedId = removed.id;
				sub.infoItems = items.filter((_, i) => i !== idx);
				const dismissed = new Set(sub.dismissedInfoIds ?? []);
				if (removedId) dismissed.add(removedId);
				sub.dismissedInfoIds = [...dismissed];
				const rejected = sub.rejectedMembers ?? [];
				const r = {
					tag: removed.tag || '',
					label: removed.label || removedId,
					reason: 'убрано из информации провайдера',
				};
				const key = r.tag ? `tag:${r.tag}` : `label:${r.label}`;
				if (!rejected.some((x) => (x.tag ? `tag:${x.tag}` : `label:${x.label}`) === key)) {
					rejected.push(r);
				}
				sub.rejectedMembers = rejected;
				send(res, 200, { success: true, data: toMockSubscriptionDTO(sub) });
			} catch (e) {
				send(res, 400, { success: false, error: { code: 'INVALID_REQUEST', message: String(e) } });
			}
		});
		return;
	}

	if (req.method === 'POST' && path === '/singbox/subscriptions/active-member') {
		let raw = '';
		req.on('data', (c) => (raw += c));
		req.on('end', () => {
			try {
				const body = JSON.parse(raw || '{}');
				const id = new URL(req.url, 'http://x').searchParams.get('id');
				const sub = mockSubscriptions.find((s) => s.id === id);
				if (sub) sub.activeMember = body.memberTag;
				send(res, 200, { success: true, data: { ok: true } });
			} catch (e) {
				send(res, 400, { success: false, error: { code: 'INVALID_REQUEST', message: String(e) } });
			}
		});
		return;
	}

	// ── Managed WG server with 11 peers (exercises peer sort UI) ──
	// IPs are intentionally not in monotonic order so that "По IP" can be
	// visually distinguished from "in storage order" and from a naive
	// lexicographic sort (which would put 10.0.0.10 before 10.0.0.2).
	const serverNatMatch = path.match(/^\/servers\/([^/]+)\/nat$/);
	if (serverNatMatch && req.method === 'POST') {
		const serverId = decodeURIComponent(serverNatMatch[1]);
		let raw = '';
		req.on('data', (c) => (raw += c));
		req.on('end', () => {
			try {
				const body = JSON.parse(raw || '{}');
				const mode = body.mode ?? (body.enabled ? 'full' : 'none');
				if (!['full', 'internet-only', 'none'].includes(mode)) {
					sendInvalidRequest(res, 'invalid NAT mode');
					return;
				}
				if (!mockSystemServerSettings[serverId]) {
					mockSystemServerSettings[serverId] = { natMode: 'none', policy: 'none' };
				}
				mockSystemServerSettings[serverId].natMode = mode;
				sendData(res, buildMockServersAllData());
			} catch (e) {
				sendInvalidRequest(res, String(e));
			}
		});
		return;
	}

	const serverEndpointMatch = path.match(/^\/servers\/([^/]+)\/endpoint$/);
	if (serverEndpointMatch && req.method === 'POST') {
		const serverId = decodeURIComponent(serverEndpointMatch[1]);
		let raw = '';
		req.on('data', (c) => (raw += c));
		req.on('end', () => {
			try {
				const body = JSON.parse(raw || '{}');
				const endpoint = typeof body.endpoint === 'string' ? body.endpoint.trim() : '';
				if (!mockSystemServerSettings[serverId]) {
					mockSystemServerSettings[serverId] = { natMode: 'none', policy: 'none', endpoint: '' };
				}
				mockSystemServerSettings[serverId].endpoint = endpoint;
				sendData(res, buildMockServersAllData());
			} catch (e) {
				sendInvalidRequest(res, String(e));
			}
		});
		return;
	}

	const serverPolicyMatch = path.match(/^\/servers\/([^/]+)\/policy$/);
	if (serverPolicyMatch && req.method === 'POST') {
		const serverId = decodeURIComponent(serverPolicyMatch[1]);
		let raw = '';
		req.on('data', (c) => (raw += c));
		req.on('end', () => {
			try {
				const body = JSON.parse(raw || '{}');
				const policy = body.policy ?? 'none';
				if (!mockSystemServerSettings[serverId]) {
					mockSystemServerSettings[serverId] = { natMode: 'none', policy: 'none' };
				}
				mockSystemServerSettings[serverId].policy = policy;
				sendData(res, buildMockServersAllData());
			} catch (e) {
				sendInvalidRequest(res, String(e));
			}
		});
		return;
	}

	const serverPeerMatch = path.match(/^\/servers\/([^/]+)\/peers(?:\/([^/]+)(?:\/(toggle|conf))?)?$/);
	if (serverPeerMatch) {
		const serverId = decodeURIComponent(serverPeerMatch[1]);
		const pubkey = serverPeerMatch[2] ? decodeURIComponent(serverPeerMatch[2]) : '';
		const leaf = serverPeerMatch[3] ?? '';
		const servers = mockSystemServers();
		const server = servers.find((s) => s.id === serverId);
		if (!server) {
			send(res, 404, { success: false, error: { code: 'NOT_FOUND', message: 'server not found' } });
			return;
		}

		if (req.method === 'POST' && !pubkey) {
			let raw = '';
			req.on('data', (c) => (raw += c));
			req.on('end', () => {
				try {
					const body = JSON.parse(raw || '{}');
					const newKey = mockPubkey(90 + server.peers.length);
					mockSystemPeerSecrets.set(newKey, {
						privateKey: mockPubkey(190 + server.peers.length),
						presharedKey: mockPubkey(290 + server.peers.length),
						description: body.description || 'New peer',
						tunnelIP: body.tunnelIP || '10.0.0.99/32',
					});
					sendData(res, buildMockServersAllData());
				} catch (e) {
					sendInvalidRequest(res, String(e));
				}
			});
			return;
		}

		if (pubkey && leaf === 'toggle' && req.method === 'POST') {
			sendData(res, buildMockServersAllData());
			return;
		}

		if (pubkey && leaf === 'conf' && req.method === 'GET') {
			if (!mockSystemPeerSecrets.has(pubkey)) {
				send(res, 400, { success: false, error: { code: 'CONF_UNAVAILABLE', message: 'ключ недоступен' } });
				return;
			}
			sendData(res, { conf: '[Interface]\nPrivateKey = MOCK\n\n[Peer]\nPublicKey = MOCK\n' });
			return;
		}

		if (pubkey && !leaf && req.method === 'DELETE') {
			mockSystemPeerSecrets.delete(pubkey);
			sendData(res, buildMockServersAllData());
			return;
		}

		if (pubkey && !leaf && req.method === 'PUT') {
			sendData(res, buildMockServersAllData());
			return;
		}
	}

	if (req.method === 'GET' && path === '/servers/all') {
		fetchJSON('/servers/all').then(({ status, body }) => {
			if (body && typeof body === 'object' && body.data && typeof body.data === 'object') {
				Object.assign(body.data, buildMockServersAllData());
			}
			send(res, status, body);
		});
		return;
	}

	if (req.method === 'GET' && path === '/servers/marked') {
		sendData(res, ['Wireguard9']);
		return;
	}

	// ── System Tunnels — endpoints not in swagger ──────────────────────────────

	if (req.method === 'GET' && path === '/system-tunnels/get') {
		const name = url.searchParams.get('name');
		const tunnel = MOCK_SYSTEM_TUNNELS.find((t) => t.id === name);
		if (tunnel) {
			send(res, 200, { success: true, data: { ...tunnel, peer: tunnel.peer ? { ...tunnel.peer } : undefined } });
		} else {
			send(res, 404, { success: false, error: 'system tunnel not found' });
		}
		return;
	}

	if (path === '/system-tunnels/asc') {
		const name = url.searchParams.get('name');
		if (!name) {
			send(res, 400, { success: false, error: { code: 'INVALID_REQUEST', message: 'name is required' } });
			return;
		}
		if (req.method === 'GET') {
			const params = mockSystemAscByTunnel[name] ?? mockZeroASC();
			send(res, 200, { success: true, data: shapeASCForMockProfile(params) });
			return;
		}
		if (req.method === 'PUT') {
			let raw = '';
			req.on('data', (c) => (raw += c));
			req.on('end', () => {
				try {
					const body = JSON.parse(raw || '{}');
					mockSystemAscByTunnel[name] = { ...mockZeroASC(), ...body };
					send(res, 200, { success: true, data: null });
				} catch (e) {
					send(res, 400, { success: false, error: { code: 'INVALID_REQUEST', message: String(e) } });
				}
			});
			return;
		}
	}

	if (req.method === 'GET' && path === '/test/connectivity') {
		const id = url.searchParams.get('id') ?? '';
		const tunnel = MOCK_AWG_TUNNELS.find((t) => t.id === id);
		if (!tunnel) {
			send(res, 404, { success: false, error: 'tunnel not found', code: 'NOT_FOUND' });
			return;
		}
		if (tunnel.status !== 'running' && tunnel.status !== 'broken') {
			send(res, 200, {
				success: true,
				data: { connected: false, reason: 'tunnel not running' },
			});
			return;
		}
		if ((tunnel.connectivityCheck?.method ?? 'http') === 'disabled') {
			send(res, 200, {
				success: true,
				data: { connected: true, reason: 'check disabled' },
			});
			return;
		}
		if (MOCK_AWG_SELF_CHECK_FAIL.has(id)) {
			send(res, 200, {
				success: true,
				data: {
					connected: false,
					latency: null,
					reason: 'HTTP connectivity check failed (mock Fin)',
				},
			});
			return;
		}
		const base = AWG_BASE_LATENCY[id];
		const jitter = Math.floor(Math.random() * 25) - 12;
		const latency =
			base !== null ? Math.max(10, Math.min(300, base + jitter)) : 42 + Math.floor(Math.random() * 40);
		send(res, 200, {
			success: true,
			data: { connected: true, latency, httpCode: 204 },
		});
		return;
	}

	if (req.method === 'GET' && path === '/system-tunnels/test-connectivity') {
		const name = url.searchParams.get('name');
		const tunnel = MOCK_SYSTEM_TUNNELS.find((t) => t.id === name);
		const up = tunnel?.status === 'up';
		setTimeout(() => {
			send(res, 200, {
				success: true,
				data: up
					? { connected: true, latency: 38 + Math.floor(Math.random() * 20) }
					: { connected: false, reason: 'interface down' },
			});
		}, 800);
		return;
	}

	if (req.method === 'GET' && path === '/system-tunnels/test-ip') {
		const name = url.searchParams.get('name');
		const tunnel = MOCK_SYSTEM_TUNNELS.find((t) => t.id === name);
		const up = tunnel?.status === 'up';
		setTimeout(() => {
			send(res, 200, {
				success: true,
				data: up
					? { directIp: '185.10.68.1', vpnIp: tunnel?.address?.split('/')[0] ?? '10.20.1.2', endpointIp: tunnel?.peer?.endpoint?.split(':')[0] ?? '', ipChanged: true }
					: { directIp: '85.174.10.1', vpnIp: '', endpointIp: '', ipChanged: false },
			});
		}, 1200);
		return;
	}

	if (req.method === 'GET' && path === '/system-tunnels/test-speed') {
		const name = url.searchParams.get('name');
		const direction = url.searchParams.get('direction') ?? 'download';
		const tunnel = MOCK_SYSTEM_TUNNELS.find((t) => t.id === name);
		const up = tunnel?.status === 'up';

		res.writeHead(200, {
			'Content-Type': 'text/event-stream',
			'Cache-Control': 'no-cache',
			Connection: 'keep-alive',
		});

		if (!up) {
			res.write(`event: error\ndata: "interface down"\n\n`);
			res.end();
			return;
		}

		const baseBw = direction === 'download' ? 12_000_000 : 4_000_000;
		let sent = 0;
		let second = 0;
		const totalSeconds = 5;
		const iv = setInterval(() => {
			second++;
			const bw = baseBw + Math.floor((Math.random() - 0.5) * 2_000_000);
			const bytes = Math.floor(bw);
			sent += bytes;
			res.write(`event: interval\ndata: ${JSON.stringify({ second, bandwidth: bw })}\n\n`);
			if (second >= totalSeconds) {
				clearInterval(iv);
				res.write(`event: result\ndata: ${JSON.stringify({ server: url.searchParams.get('server') ?? 'speed.demo.example', direction, bandwidth: baseBw, bytes: sent, duration: totalSeconds })}\n\n`);
				res.end();
			}
		}, 1000);

		const cleanup = () => clearInterval(iv);
		req.on('close', cleanup);
		req.on('error', cleanup);
		return;
	}

	// ── fakeip config CRUD ────────────────────────────────────────────────────

	if (req.method === 'GET' && path === '/singbox/fakeip/config/dns/servers/list') {
		send(res, 200, { success: true, data: mockFakeipDNSServers.map(scrubMockDnsServerStored) });
		return;
	}

	if (req.method === 'POST' && path === '/singbox/fakeip/config/dns/servers/add') {
		let raw = '';
		req.on('data', (c) => (raw += c));
		req.on('end', () => {
			try {
				const payload = sanitizeMockDnsServerForWrite(JSON.parse(raw || '{}'));
				mockFakeipDNSServers.push(payload);
				send(res, 200, { success: true, data: payload });
			} catch (e) {
				send(res, 400, { success: false, error: { code: 'INVALID_REQUEST', message: String(e) } });
			}
		});
		return;
	}

	if (req.method === 'POST' && path === '/singbox/fakeip/config/dns/servers/update') {
		let raw = '';
		req.on('data', (c) => (raw += c));
		req.on('end', () => {
			try {
				const { tag, server } = JSON.parse(raw || '{}');
				const idx = mockFakeipDNSServers.findIndex((s) => s.tag === tag);
				if (idx === -1) {
					send(res, 404, { success: false, error: { code: 'NOT_FOUND', message: 'dns server not found' } });
					return;
				}
				mockFakeipDNSServers[idx] = sanitizeMockDnsServerForWrite(server);
				send(res, 200, { success: true, data: { ok: true } });
			} catch (e) {
				send(res, 400, { success: false, error: { code: 'INVALID_REQUEST', message: String(e) } });
			}
		});
		return;
	}

	if (req.method === 'POST' && path === '/singbox/fakeip/config/dns/servers/delete') {
		let raw = '';
		req.on('data', (c) => (raw += c));
		req.on('end', () => {
			try {
				const { tag } = JSON.parse(raw || '{}');
				const idx = mockFakeipDNSServers.findIndex((s) => s.tag === tag);
				if (idx === -1) {
					send(res, 404, { success: false, error: { code: 'NOT_FOUND', message: 'dns server not found' } });
					return;
				}
				mockFakeipDNSServers.splice(idx, 1);
				send(res, 200, { success: true, data: { ok: true } });
			} catch (e) {
				send(res, 400, { success: false, error: { code: 'INVALID_REQUEST', message: String(e) } });
			}
		});
		return;
	}

	if (req.method === 'POST' && path === '/singbox/fakeip/config/dns/servers/move') {
		let raw = '';
		req.on('data', (c) => (raw += c));
		req.on('end', () => {
			try {
				const { from, to } = JSON.parse(raw || '{}');
				if (from < 0 || from >= mockFakeipDNSServers.length || to < 0 || to >= mockFakeipDNSServers.length) {
					send(res, 404, { success: false, error: { code: 'NOT_FOUND', message: 'dns server not found' } });
					return;
				}
				const [moved] = mockFakeipDNSServers.splice(from, 1);
				mockFakeipDNSServers.splice(to, 0, moved);
				send(res, 200, { success: true, data: { ok: true } });
			} catch (e) {
				send(res, 400, { success: false, error: { code: 'INVALID_REQUEST', message: String(e) } });
			}
		});
		return;
	}

	if (req.method === 'GET' && path === '/singbox/fakeip/config/dns/rules/list') {
		send(res, 200, { success: true, data: mockFakeipDNSRules });
		return;
	}

	if (req.method === 'POST' && path === '/singbox/fakeip/config/dns/rules/add') {
		let raw = '';
		req.on('data', (c) => (raw += c));
		req.on('end', () => {
			try {
				const payload = JSON.parse(raw || '{}');
				mockFakeipDNSRules.push(payload);
				send(res, 200, { success: true, data: payload });
			} catch (e) {
				send(res, 400, { success: false, error: { code: 'INVALID_REQUEST', message: String(e) } });
			}
		});
		return;
	}

	if (req.method === 'POST' && path === '/singbox/fakeip/config/dns/rules/update') {
		let raw = '';
		req.on('data', (c) => (raw += c));
		req.on('end', () => {
			try {
				const { index, rule } = JSON.parse(raw || '{}');
				if (index < 0 || index >= mockFakeipDNSRules.length) {
					send(res, 404, { success: false, error: { code: 'NOT_FOUND', message: 'dns rule not found' } });
					return;
				}
				mockFakeipDNSRules[index] = rule;
				send(res, 200, { success: true, data: { ok: true } });
			} catch (e) {
				send(res, 400, { success: false, error: { code: 'INVALID_REQUEST', message: String(e) } });
			}
		});
		return;
	}

	if (req.method === 'POST' && path === '/singbox/fakeip/config/dns/rules/delete') {
		let raw = '';
		req.on('data', (c) => (raw += c));
		req.on('end', () => {
			try {
				const { index } = JSON.parse(raw || '{}');
				if (index < 0 || index >= mockFakeipDNSRules.length) {
					send(res, 404, { success: false, error: { code: 'NOT_FOUND', message: 'dns rule not found' } });
					return;
				}
				mockFakeipDNSRules.splice(index, 1);
				send(res, 200, { success: true, data: { ok: true } });
			} catch (e) {
				send(res, 400, { success: false, error: { code: 'INVALID_REQUEST', message: String(e) } });
			}
		});
		return;
	}

	if (req.method === 'POST' && path === '/singbox/fakeip/config/dns/rules/move') {
		let raw = '';
		req.on('data', (c) => (raw += c));
		req.on('end', () => {
			try {
				const { from, to } = JSON.parse(raw || '{}');
				if (from < 0 || from >= mockFakeipDNSRules.length || to < 0 || to >= mockFakeipDNSRules.length) {
					send(res, 404, { success: false, error: { code: 'NOT_FOUND', message: 'dns rule not found' } });
					return;
				}
				const [moved] = mockFakeipDNSRules.splice(from, 1);
				mockFakeipDNSRules.splice(to, 0, moved);
				send(res, 200, { success: true, data: { ok: true } });
			} catch (e) {
				send(res, 400, { success: false, error: { code: 'INVALID_REQUEST', message: String(e) } });
			}
		});
		return;
	}

	if (req.method === 'GET' && path === '/singbox/fakeip/config/dns/globals') {
		send(res, 200, { success: true, data: mockFakeipDNSGlobals });
		return;
	}

	if (req.method === 'PUT' && path === '/singbox/fakeip/config/dns/globals') {
		let raw = '';
		req.on('data', (c) => (raw += c));
		req.on('end', () => {
			try {
				const payload = JSON.parse(raw || '{}');
				mockFakeipDNSGlobals = {
					final: payload.final ?? mockFakeipDNSGlobals.final,
					strategy: payload.strategy ?? mockFakeipDNSGlobals.strategy,
				};
				send(res, 200, { success: true, data: { ok: true } });
			} catch (e) {
				send(res, 400, { success: false, error: { code: 'INVALID_REQUEST', message: String(e) } });
			}
		});
		return;
	}

	if (req.method === 'GET' && path === '/singbox/fakeip/config/rules/list') {
		send(res, 200, { success: true, data: mockFakeipRules });
		return;
	}

	if (req.method === 'POST' && path === '/singbox/fakeip/config/rules/add') {
		let raw = '';
		req.on('data', (c) => (raw += c));
		req.on('end', () => {
			try {
				const payload = JSON.parse(raw || '{}');
				mockFakeipRules.push(payload);
				send(res, 200, { success: true, data: payload });
			} catch (e) {
				send(res, 400, { success: false, error: { code: 'INVALID_REQUEST', message: String(e) } });
			}
		});
		return;
	}

	if (req.method === 'POST' && path === '/singbox/fakeip/config/rules/update') {
		let raw = '';
		req.on('data', (c) => (raw += c));
		req.on('end', () => {
			try {
				const { index, rule } = JSON.parse(raw || '{}');
				if (index < 0 || index >= mockFakeipRules.length) {
					send(res, 404, { success: false, error: { code: 'NOT_FOUND', message: 'rule not found' } });
					return;
				}
				mockFakeipRules[index] = rule;
				send(res, 200, { success: true, data: { ok: true } });
			} catch (e) {
				send(res, 400, { success: false, error: { code: 'INVALID_REQUEST', message: String(e) } });
			}
		});
		return;
	}

	if (req.method === 'POST' && path === '/singbox/fakeip/config/rules/delete') {
		let raw = '';
		req.on('data', (c) => (raw += c));
		req.on('end', () => {
			try {
				const { index } = JSON.parse(raw || '{}');
				if (index < 0 || index >= mockFakeipRules.length) {
					send(res, 404, { success: false, error: { code: 'NOT_FOUND', message: 'rule not found' } });
					return;
				}
				mockFakeipRules.splice(index, 1);
				send(res, 200, { success: true, data: { ok: true } });
			} catch (e) {
				send(res, 400, { success: false, error: { code: 'INVALID_REQUEST', message: String(e) } });
			}
		});
		return;
	}

	if (req.method === 'POST' && path === '/singbox/fakeip/config/rules/move') {
		let raw = '';
		req.on('data', (c) => (raw += c));
		req.on('end', () => {
			try {
				const { from, to } = JSON.parse(raw || '{}');
				if (from < 0 || from >= mockFakeipRules.length || to < 0 || to >= mockFakeipRules.length) {
					send(res, 404, { success: false, error: { code: 'NOT_FOUND', message: 'rule not found' } });
					return;
				}
				const [moved] = mockFakeipRules.splice(from, 1);
				mockFakeipRules.splice(to, 0, moved);
				send(res, 200, { success: true, data: { ok: true } });
			} catch (e) {
				send(res, 400, { success: false, error: { code: 'INVALID_REQUEST', message: String(e) } });
			}
		});
		return;
	}

	if (req.method === 'POST' && path === '/singbox/fakeip/config/route/final') {
		let raw = '';
		req.on('data', (c) => (raw += c));
		req.on('end', () => {
			try {
				// route final is a separate config key on the backend; acknowledge only.
				JSON.parse(raw || '{}');
				send(res, 200, { success: true, data: { ok: true } });
			} catch (e) {
				send(res, 400, { success: false, error: { code: 'INVALID_REQUEST', message: String(e) } });
			}
		});
		return;
	}

	if (req.method === 'GET' && path === '/singbox/fakeip/config/rulesets/list') {
		send(res, 200, { success: true, data: mockFakeipRuleSets });
		return;
	}

	if (req.method === 'POST' && path === '/singbox/fakeip/config/rulesets/add') {
		let raw = '';
		req.on('data', (c) => (raw += c));
		req.on('end', () => {
			try {
				const rs = JSON.parse(raw || '{}');
				if (mockFakeipRuleSets.some((x) => x.tag === rs.tag)) {
					send(res, 400, { success: false, error: { code: 'CONFLICT', message: `tag ${rs.tag} exists` } });
					return;
				}
				mockFakeipRuleSets.push(rs);
				send(res, 200, { success: true, data: { ok: true } });
			} catch (e) {
				send(res, 400, { success: false, error: { code: 'INVALID_REQUEST', message: String(e) } });
			}
		});
		return;
	}

	if (req.method === 'POST' && path === '/singbox/fakeip/config/rulesets/update') {
		let raw = '';
		req.on('data', (c) => (raw += c));
		req.on('end', () => {
			try {
				const { tag, ruleSet } = JSON.parse(raw || '{}');
				const idx = mockFakeipRuleSets.findIndex((x) => x.tag === tag);
				if (idx < 0) {
					send(res, 404, { success: false, error: { code: 'NOT_FOUND', message: `tag ${tag} not found` } });
					return;
				}
				mockFakeipRuleSets[idx] = ruleSet;
				send(res, 200, { success: true, data: { ok: true } });
			} catch (e) {
				send(res, 400, { success: false, error: { code: 'INVALID_REQUEST', message: String(e) } });
			}
		});
		return;
	}

	if (req.method === 'POST' && path === '/singbox/fakeip/config/rulesets/delete') {
		let raw = '';
		req.on('data', (c) => (raw += c));
		req.on('end', () => {
			try {
				const { tag } = JSON.parse(raw || '{}');
				const idx = mockFakeipRuleSets.findIndex((x) => x.tag === tag);
				if (idx < 0) {
					send(res, 404, { success: false, error: { code: 'NOT_FOUND', message: `tag ${tag} not found` } });
					return;
				}
				mockFakeipRuleSets.splice(idx, 1);
				send(res, 200, { success: true, data: { ok: true } });
			} catch (e) {
				send(res, 400, { success: false, error: { code: 'INVALID_REQUEST', message: String(e) } });
			}
		});
		return;
	}

	if (req.method === 'GET' && path === '/singbox/fakeip/config/outbounds/list') {
		send(res, 200, { success: true, data: mockFakeipOutbounds });
		return;
	}

	if (req.method === 'POST' && path === '/singbox/fakeip/config/outbounds/add') {
		let raw = '';
		req.on('data', (c) => (raw += c));
		req.on('end', () => {
			try {
				const o = JSON.parse(raw || '{}');
				if (mockFakeipOutbounds.some((x) => x.tag === o.tag)) {
					send(res, 400, { success: false, error: { code: 'CONFLICT', message: `tag ${o.tag} exists` } });
					return;
				}
				mockFakeipOutbounds.push({ ...o, source: 'router' });
				send(res, 200, { success: true, data: { ok: true } });
			} catch (e) {
				send(res, 400, { success: false, error: { code: 'INVALID_REQUEST', message: String(e) } });
			}
		});
		return;
	}

	if (req.method === 'POST' && path === '/singbox/fakeip/config/outbounds/update') {
		let raw = '';
		req.on('data', (c) => (raw += c));
		req.on('end', () => {
			try {
				const { tag, outbound } = JSON.parse(raw || '{}');
				const idx = mockFakeipOutbounds.findIndex((x) => x.tag === tag);
				if (idx < 0) {
					send(res, 404, { success: false, error: { code: 'NOT_FOUND', message: `tag ${tag} not found` } });
					return;
				}
				mockFakeipOutbounds[idx] = { ...outbound, source: 'router' };
				send(res, 200, { success: true, data: { ok: true } });
			} catch (e) {
				send(res, 400, { success: false, error: { code: 'INVALID_REQUEST', message: String(e) } });
			}
		});
		return;
	}

	if (req.method === 'POST' && path === '/singbox/fakeip/config/outbounds/delete') {
		let raw = '';
		req.on('data', (c) => (raw += c));
		req.on('end', () => {
			try {
				const { tag } = JSON.parse(raw || '{}');
				mockFakeipOutbounds = mockFakeipOutbounds.filter((x) => x.tag !== tag);
				send(res, 200, { success: true, data: { ok: true } });
			} catch (e) {
				send(res, 400, { success: false, error: { code: 'INVALID_REQUEST', message: String(e) } });
			}
		});
		return;
	}

	// ── end fakeip config CRUD ─────────────────────────────────────────────────

	// Pass-through for everything else (including /events SSE).
	const upstream = new URL(UPSTREAM);
	const proxyReq = http.request(
		{
			hostname: upstream.hostname,
			port: upstream.port,
			path: req.url,
			method: req.method,
			headers: { ...req.headers, host: upstream.host },
		},
		(proxyRes) => {
			res.writeHead(proxyRes.statusCode ?? 502, proxyRes.headers);
			proxyRes.pipe(res);
		},
	);
	proxyReq.on('error', (e) => {
		if (!res.headersSent) {
			send(res, 502, { error: String(e) });
		} else {
			res.end();
		}
	});
	req.pipe(proxyReq);
});

server.listen(PORT, '127.0.0.1', () => {
	console.log(`mock-proxy on http://127.0.0.1:${PORT} → ${UPSTREAM} (usageLevel=${usageLevel})`);
	console.log('[mock-proxy] controls: GET /__mock/capabilities, GET /__mock/tunnels, POST /__mock/reset-runtime, POST /__mock/singbox-install-fail, POST /__mock/download-faults, POST /__mock/keenetic-os');
	console.log(`[mock-proxy] keenetic-os: ${mockKeeneticProfile.key} (supportsExtendedASC=${mockKeeneticProfile.extended}; default: 5.1, force: MOCK_KEENETIC_OS=5.0|5.1, switch: POST /__mock/keenetic-os)`);
	console.log(`[mock-proxy] download faults: enabled=${downloadFaultsEnabled} p=${downloadFaultProbability} (disable: MOCK_DOWNLOAD_FAULTS=0)`);
});

function wsAccept(key) {
	const GUID = '258EAFA5-E914-47DA-95CA-C5AB0DC85B11';
	return crypto.createHash('sha1').update(key + GUID).digest('base64');
}

function encodeWSFrame(payload) {
	const data = Buffer.from(payload, 'utf8');
	const len = data.length;
	let header;
	if (len < 126) {
		header = Buffer.from([0x81, len]);
	} else if (len < 65536) {
		header = Buffer.alloc(4);
		header[0] = 0x81; header[1] = 126;
		header.writeUInt16BE(len, 2);
	} else {
		header = Buffer.alloc(10);
		header[0] = 0x81; header[1] = 127;
		header.writeBigUInt64BE(BigInt(len), 2);
	}
	return Buffer.concat([header, data]);
}

function makeMockSnapshot() {
	const hosts = ['youtube.com', 'discord.com', 'github.com', 'cloudflare.com', 'mozilla.org'];
	// Включаем IP реальных policy-devices, чтобы Устройства-чип показал живой
	// per-device счётчик соединений (по metadata.sourceIP).
	const sources = ['192.168.1.42', '192.168.1.43', '192.168.1.47', '192.168.1.5'];
	const outbounds = ['vless-1', 'urltest:auto', 'DIRECT'];
	const rules = ['DOMAIN-SUFFIX', 'RULE-SET', 'GEOIP'];
	const networks = ['tcp', 'tcp', 'tcp', 'udp'];
	const conns = Array.from({ length: 6 + Math.floor(Math.random() * 4) }, (_, i) => {
		const out = outbounds[i % outbounds.length];
		return {
			id: `mock-${i}-${Date.now()}`,
			metadata: {
				network: networks[i % networks.length],
				type: 'Tun',
				sourceIP: sources[i % sources.length],
				sourcePort: String(50000 + i * 13),
				destinationIP: `142.250.${i}.${10 + i}`,
				destinationPort: '443',
				host: hosts[i % hosts.length],
			},
			upload: 1024 * (50 + Math.floor(Math.random() * 5000)),
			download: 1024 * (200 + Math.floor(Math.random() * 50000)),
			start: new Date(Date.now() - (60 + i * 30) * 1000).toISOString(),
			chains: [out],
			rule: rules[i % rules.length],
			rulePayload: hosts[i % hosts.length],
		};
	});
	// Keep one stable public-IP source entry so search/filter demos
	// can produce a visible subset and show bulk actions in mock mode.
	conns[0] = {
		id: `mock-public-${Date.now()}`,
		metadata: {
			network: 'udp',
			type: 'Tun',
			sourceIP: '95.64.154.50',
			sourcePort: '500',
			destinationIP: '95.64.154.50',
			destinationPort: '500',
			host: '',
		},
		upload: 2310,
		download: 0,
		start: new Date(Date.now() - 27 * 1000).toISOString(),
		chains: ['DIRECT'],
		rule: 'final',
		rulePayload: '',
	};
	return {
		downloadTotal: conns.reduce((s, c) => s + c.download, 0),
		uploadTotal: conns.reduce((s, c) => s + c.upload, 0),
		connections: conns,
	};
}

server.on('upgrade', (req, socket) => {
	if (req.url !== '/singbox/clash/connections') {
		socket.destroy();
		return;
	}
	const key = req.headers['sec-websocket-key'];
	if (!key) { socket.destroy(); return; }
	socket.write(
		'HTTP/1.1 101 Switching Protocols\r\n' +
		'Upgrade: websocket\r\n' +
		'Connection: Upgrade\r\n' +
		`Sec-WebSocket-Accept: ${wsAccept(key)}\r\n\r\n`,
	);
	const interval = setInterval(() => {
		try {
			socket.write(encodeWSFrame(JSON.stringify(makeMockSnapshot())));
		} catch {
			clearInterval(interval);
		}
	}, 1500);
	socket.on('close', () => clearInterval(interval));
	socket.on('error', () => clearInterval(interval));
	// Initial frame so the UI flips to "Live" immediately.
	try { socket.write(encodeWSFrame(JSON.stringify(makeMockSnapshot()))); } catch { /* ignore */ }
});
