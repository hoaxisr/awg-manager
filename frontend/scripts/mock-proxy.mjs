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

import http from 'node:http';
import crypto from 'node:crypto';
import { readFile } from 'node:fs/promises';
import { fileURLToPath } from 'node:url';
import { dirname, resolve } from 'node:path';

const __dirname = dirname(fileURLToPath(import.meta.url));
const PRESETS_PATH = resolve(__dirname, 'mock-data/presets-snapshot.json');
let presetsCache = null;
async function getPresets() {
	if (!presetsCache) {
		presetsCache = JSON.parse(await readFile(PRESETS_PATH, 'utf8'));
	}
	return presetsCache;
}

const UPSTREAM = process.env.UPSTREAM ?? 'http://127.0.0.1:8080';
const PORT = Number(process.env.PORT ?? 8081);
const VALID = new Set(['basic', 'advanced', 'expert']);

// In-memory state. Default 'expert' so all advanced surfaces (singbox
// router, rule sets, device proxy, etc.) are visible by default — the
// realistic case for development against the redesigned routing page.
let usageLevel = 'expert';
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
		defaultRoute: false,
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
		defaultRoute: false,
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
		defaultRoute: false,
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
		status: 'stopped',
		enabled: false,
		defaultRoute: false,
		resolvedIspInterface: 'ISP0',
		resolvedIspInterfaceLabel: 'Резервный WAN',
		endpoint: 'uk-lon.demo.example:51820',
		address: '10.50.0.2/32, fd00:8::2/128',
		interfaceName: 'awg4',
		ndmsName: 'Wireguard4',
		rxBytes: 0,
		txBytes: 0,
		lastHandshake: '',
		awgVersion: 'awg2.0',
		mtu: 1420,
		startedAt: '',
		backend: 'kernel',
		connectivityCheck: { method: 'http' },
		pingCheck: { status: 'failed', restartCount: 0, failCount: 3, failThreshold: 3 },
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

/** AWG tunnels where monitoring self-check is down while status stays running. */
const MOCK_AWG_SELF_CHECK_FAIL = new Set(['awg-demo-fin']);

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
		const tunnelId = tunneled ? (i % 2 === 0 ? 'awg-demo-1' : 'awg-demo-2') : '';
		const tunnelName = tunneled ? (i % 2 === 0 ? 'DE Frankfurt' : 'NL Amsterdam') : 'Direct';
		const iface = tunneled ? 'My VPN' : 'eth3';
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

	const tunnels = {
		'': { name: 'Direct', interface: 'eth3', count: stats.direct },
		'awg-demo-1': { name: 'DE Frankfurt', interface: 'My VPN', count: MOCK_CONNECTIONS_POOL.filter((c) => c.tunnelId === 'awg-demo-1').length },
		'awg-demo-2': { name: 'NL Amsterdam', interface: 'My VPN', count: MOCK_CONNECTIONS_POOL.filter((c) => c.tunnelId === 'awg-demo-2').length },
	};

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

// Per-tunnel base latencies (ms), randomised once on startup so they stay in
// different tiers across the session but drift slightly on every SSE tick.
const AWG_BASE_LATENCY = (() => {
	// Spread tunnels across latency tiers for a useful demo:
	// good (<80ms): awg-demo-1, awg-demo-3
	// warn (80-199ms): awg-demo-2 (recovering)
	// starting: awg-demo-4 (SE Stockholm) → no latency until running
	// bad (≥200ms): awg-demo-6
	// self-check fail: awg-demo-fin → «Нет связи»
	// stopped/failed: awg-demo-5 → no latency
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

function buildConnectivityMatrixEvent() {
	const nowIso = new Date().toISOString();
	const selfTarget = { id: 'self', name: 'Self-check', host: '', url: '' };

	const cells = [];
	const tunnelEntries = [];

	for (const t of MOCK_AWG_TUNNELS) {
		const base = AWG_BASE_LATENCY[t.id] ?? null;
		const isActive = t.status === 'running' || t.status === 'broken';
		const pingFailed = t.pingCheck?.status === 'failed';
		const selfCheckFail = MOCK_AWG_SELF_CHECK_FAIL.has(t.id);

		// Add small jitter (±12ms) so the value visibly fluctuates
		const jitter = Math.floor(Math.random() * 25) - 12;
		const latency = base !== null && isActive && !pingFailed && !selfCheckFail
			? Math.max(10, Math.min(300, base + jitter))
			: null;

		cells.push({
			targetId: selfTarget.id,
			tunnelId: t.id,
			latencyMs: latency,
			ok: isActive && !pingFailed && !selfCheckFail,
			activeForRestart: false,
			isSelf: true,
			ts: nowIso,
		});

		tunnelEntries.push({
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
		});
	}

	return {
		targets: [selfTarget],
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
		activeMember: 'sub-demo0001-aabbccdd',
		enabled: true,
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
		activeMember: `sub-${shortID}-aaaa`,
		enabled: input.enabled,
	};
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
	refreshMode: 'interval',
	refreshIntervalHours: 24,
	wanAutoDetect: true,
	wanInterface: '',
};

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
let mockDNSServers = [
	{
		tag: 'wizard-upstream',
		type: 'udp',
		server: '77.8.8.8',
		server_port: 53,
		detour: 'sub-demo0001',
	},
];
let mockDNSRules = [
	{
		action: 'route',
		rule_set: ['geosite-youtube'],
		server: 'wizard-upstream',
	},
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
];
const mockAccessPolicies = [
	{
		name: 'Policy0',
		description: 'home',
		isStandard: true,
		standalone: false,
		interfaces: [{ name: 'My VPN', label: 'My VPN', order: 0 }],
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
		standalone: false,
		interfaces: [{ name: 'DE vless-tcp-reality', label: 'DE', order: 0 }],
		deviceCount: 0,
	},
	{
		name: 'HydraRoute',
		description: '',
		isStandard: false,
		standalone: false,
		interfaces: [
			{ name: 'NetcrazeHy2', label: 'NetcrazeHy2', order: 0 },
			{ name: 'amnezia_for_awg_fornex', label: 'amnezia_for_awg_fornex', order: 1 },
		],
		deviceCount: 2,
	},
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

/** HydraRoute Neo в блоке AWGM (GET /system/hydraroute-status). */
const mockHydraRouteStatus = {
	installed: true,
	running: true,
	version: '2.4.1',
};

const mockRoutingDnsRoutes = [
	{
		id: 'dns-work-vpn',
		name: 'Work VPN',
		domains: ['corp.example.com'],
		manualDomains: ['corp.example.com'],
		routes: [{ interface: 'nwg0', tunnelId: 'awg-demo-1', fallback: 'auto' }],
		enabled: true,
		createdAt: '2026-05-01T10:00:00Z',
		updatedAt: '2026-05-14T20:00:00Z',
	},
	{
		id: 'dns-youtube',
		name: 'YouTube',
		domains: ['youtube.com', 'ytimg.com', 'googlevideo.com'],
		manualDomains: ['youtube.com'],
		routes: [{ interface: 'nwg0', tunnelId: 'awg-demo-1', fallback: 'auto' }],
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
		routes: [{ interface: 'nwg1', tunnelId: 'awg-demo-2', fallback: 'auto' }],
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
		routes: [{ interface: 'nwg0', tunnelId: 'awg-demo-1', fallback: 'auto' }],
		enabled: false,
		createdAt: '2026-05-04T10:00:00Z',
		updatedAt: '2026-05-14T20:00:00Z',
	},
	{
		id: 'dns-openai',
		name: 'OpenAI',
		domains: ['openai.com', 'chatgpt.com'],
		manualDomains: ['openai.com'],
		routes: [{ interface: 'nwg1', tunnelId: 'awg-demo-2', fallback: 'auto' }],
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
		routes: [{ interface: 'nwg0', tunnelId: 'awg-demo-1', fallback: 'auto' }],
		enabled: true,
		createdAt: '2026-05-06T10:00:00Z',
		updatedAt: '2026-05-14T20:00:00Z',
	},
	{
		id: 'dns-twitter-x',
		name: 'Twitter/X',
		domains: ['twitter.com', 'x.com', 'twimg.com'],
		manualDomains: ['x.com'],
		routes: [{ interface: 'nwg1', tunnelId: 'awg-demo-2', fallback: 'auto' }],
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
		id: 'hrneo-geo-youtube',
		name: 'HR GEO YouTube',
		domains: ['youtube.com'],
		manualDomains: ['youtube.com'],
		hrRouteMode: 'policy',
		hrPolicyName: 'HydraRoute',
		routes: [{ interface: 'nwg0', tunnelId: 'awg-demo-1', fallback: 'auto' }],
		enabled: true,
		backend: 'hydraroute',
		createdAt: '2026-05-07T10:00:00Z',
		updatedAt: '2026-05-14T20:00:00Z',
	},
	{
		id: 'hrneo-geo-discord',
		name: 'HR GEO Discord',
		domains: ['discord.com'],
		manualDomains: ['discord.com'],
		hrRouteMode: 'policy',
		hrPolicyName: 'HydraRoute',
		routes: [{ interface: 'nwg1', tunnelId: 'awg-demo-2', fallback: 'auto' }],
		enabled: true,
		backend: 'hydraroute',
		createdAt: '2026-05-08T10:00:00Z',
		updatedAt: '2026-05-14T20:00:00Z',
	},
	{
		id: 'hrneo-geo-openai',
		name: 'HR GEO OpenAI',
		domains: ['openai.com'],
		manualDomains: ['openai.com'],
		hrRouteMode: 'policy',
		hrPolicyName: 'HydraRoute',
		routes: [{ interface: 'nwg1', tunnelId: 'awg-demo-2', fallback: 'auto' }],
		enabled: true,
		backend: 'hydraroute',
		createdAt: '2026-05-09T10:00:00Z',
		updatedAt: '2026-05-14T20:00:00Z',
	},
	{
		id: 'hrneo-geo-github',
		name: 'HR GEO GitHub',
		domains: ['github.com'],
		manualDomains: ['github.com'],
		hrRouteMode: 'policy',
		hrPolicyName: 'HydraRoute',
		routes: [{ interface: 'nwg0', tunnelId: 'awg-demo-1', fallback: 'auto' }],
		enabled: true,
		backend: 'hydraroute',
		createdAt: '2026-05-10T10:00:00Z',
		updatedAt: '2026-05-14T20:00:00Z',
	},
	{
		id: 'hrneo-geo-twitch',
		name: 'HR GEO Twitch',
		domains: ['twitch.tv'],
		manualDomains: ['twitch.tv'],
		hrRouteMode: 'policy',
		hrPolicyName: 'HydraRoute',
		routes: [{ interface: 'nwg0', tunnelId: 'awg-demo-1', fallback: 'auto' }],
		enabled: true,
		backend: 'hydraroute',
		createdAt: '2026-05-11T10:00:00Z',
		updatedAt: '2026-05-14T20:00:00Z',
	},
	{
		id: 'hrneo-geo-google',
		name: 'HR GEO Google',
		domains: ['google.com'],
		manualDomains: ['google.com'],
		hrRouteMode: 'policy',
		hrPolicyName: 'HydraRoute',
		routes: [{ interface: 'nwg0', tunnelId: 'awg-demo-1', fallback: 'auto' }],
		enabled: true,
		backend: 'hydraroute',
		createdAt: '2026-05-12T10:00:00Z',
		updatedAt: '2026-05-14T20:00:00Z',
	},
	{
		id: 'hrneo-geo-telegram',
		name: 'HR GEO Telegram',
		domains: ['telegram.org'],
		manualDomains: ['telegram.org'],
		hrRouteMode: 'policy',
		hrPolicyName: 'HydraRoute',
		routes: [{ interface: 'nwg1', tunnelId: 'awg-demo-2', fallback: 'auto' }],
		enabled: true,
		backend: 'hydraroute',
		createdAt: '2026-05-13T10:00:00Z',
		updatedAt: '2026-05-14T20:00:00Z',
	},
	{
		id: 'hrneo-geo-netflix',
		name: 'HR GEO Netflix',
		domains: ['netflix.com'],
		manualDomains: ['netflix.com'],
		hrRouteMode: 'policy',
		hrPolicyName: 'HydraRoute',
		routes: [{ interface: 'nwg1', tunnelId: 'awg-demo-2', fallback: 'auto' }],
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

const mockPolicyInterfaces = [
	{ name: 'Direct', label: 'Direct', up: true },
	{ name: 'My VPN', label: 'My VPN', up: true },
	{ name: 'NetcrazeHy2', label: 'NetcrazeHy2', up: true },
	{ name: 'amnezia_for_awg_fornex', label: 'amnezia_for_awg_fornex', up: true },
	{ name: 'NL vless-grpc', label: 'NL', up: false },
];

const mockRoutingTunnels = [
	{ id: 'awg-demo-1', name: 'tun_abc123', iface: 'nwg0', type: 'managed', status: 'up', available: true },
	{ id: 'awg-demo-2', name: 'tun_def456', iface: 'nwg1', type: 'managed', status: 'up', available: true },
	{ id: 'direct', name: 'Direct', iface: 'eth3', type: 'wan', status: 'up', available: true },
];

const mockSingboxRules = [
	{ action: 'sniff' },
	{ action: 'hijack-dns', protocol: 'dns' },
	{ action: 'route', domain_suffix: ['youtube.com', 'ytimg.com'], outbound: 'sub-demo0001' },
	{ action: 'route', rule_set: ['geosite-openai'], outbound: 'sub-demo0001' },
	{ action: 'route', domain_suffix: ['github.com'], outbound: 'direct' },
	{ action: 'reject', domain_suffix: ['ads.example'] },
];

const mockSingboxRuleSets = [
	{ tag: 'geosite-cn', type: 'remote', format: 'binary', url: 'https://cdn.example.com/geosite-cn.srs', update_interval: '24h', download_detour: 'direct' },
	{ tag: 'geosite-youtube', type: 'remote', format: 'binary', url: 'https://cdn.example.com/geosite-youtube.srs', update_interval: '24h', download_detour: 'direct' },
	{ tag: 'geosite-openai', type: 'remote', format: 'binary', url: 'https://cdn.example.com/geosite-openai.srs', update_interval: '24h', download_detour: 'direct' },
	{ tag: 'geosite-discord', type: 'remote', format: 'binary', url: 'https://cdn.example.com/geosite-discord.srs', update_interval: '24h', download_detour: 'direct' },
	{ tag: 'geosite-github', type: 'remote', format: 'binary', url: 'https://cdn.example.com/geosite-github.srs', update_interval: '24h', download_detour: 'direct' },
	{ tag: 'geoip-ru', type: 'remote', format: 'binary', url: 'https://cdn.example.com/geoip-ru.srs', update_interval: '24h', download_detour: 'direct' },
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

function mockManagedServer() {
	return {
		interfaceName: 'Wireguard1',
		description: 'Mock home server',
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
		description: 'Wireguard VPN Server',
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
	return [
		{
			id: 'Wireguard0',
			interfaceName: 'Wireguard0',
			description: 'Wireguard VPN Server',
			status: 'up',
			connected: true,
			mtu: 1420,
			address: '10.0.1.1',
			mask: '255.255.255.0',
			publicKey: mockPubkey(41),
			listenPort: 51820,
			peers: mockSystemServerPeers(),
		},
		{
			id: 'Wireguard9',
			interfaceName: 'Wireguard9',
			description: 'Branch Office WG',
			status: 'down',
			connected: false,
			mtu: 1420,
			address: '10.9.0.1',
			mask: '255.255.255.0',
			publicKey: mockPubkey(42),
			listenPort: 53199,
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

	if (req.method === 'GET' && path === '/system/info') {
		fetchJSON('/system/info').then(({ status, body }) => {
			if (body && typeof body === 'object' && body.data && typeof body.data === 'object') {
				const data = body.data;
				const details = (data.routerDetails && typeof data.routerDetails === 'object')
					? data.routerDetails
					: {};

				const titleRaw = String(details.firmwareTitle ?? '');
				const releaseRaw = String(details.firmwareRelease ?? '');
				const versionRaw = String(data.firmwareVersion ?? data.keeneticOS ?? '');

				// Keep router title as model only; [Port] is rendered separately in UI
				// from details.portedBuild and must not be duplicated in the string.
				let firmwareTitle = titleRaw.replace(/\s*\[Port\]\s*/gi, '').trim();
				if (!firmwareTitle || firmwareTitle === '—') {
					firmwareTitle = 'CMCC RAX3000M (KN-3812)';
				}

				// Extract a clean Keenetic release string if upstream example is polluted
				// with model/title fragments.
				const releaseMatch = `${releaseRaw} ${versionRaw}`.match(/\d+\.\d+\.\d+\s*\([^)]+\)/);
				const firmwareRelease = releaseMatch ? releaseMatch[0].trim() : '5.0.11 (5.00.C.11.0-0)';

				data.routerDetails = {
					...details,
					firmwareTitle,
					firmwareRelease,
					portedBuild: details.portedBuild ?? true,
					modelDisplay: details.modelDisplay || 'CMCC RAX3000M (KN-3812)',
					region: details.region || 'EA',
				};
			}
			send(res, status, body);
		});
		return;
	}

	if (req.method === 'GET' && path === '/settings/get') {
		fetchJSON('/settings/get').then(({ status, body }) => {
			if (body && typeof body === 'object' && body.data) {
				body.data.usageLevel = usageLevel;
			}
			send(res, status, body);
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
				const { status, body } = await fetchJSON('/settings/get');
				if (body && typeof body === 'object' && body.data) {
					body.data.usageLevel = usageLevel;
				}
				send(res, status, body);
				console.log(`[mock-proxy] usageLevel → ${usageLevel}`);
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
								last_downloaded: new Date().toISOString(),
								source_type: 'country_config',
								os_version: 'Web',
								installation_uuid: '00000000-0000-4000-8000-000000000001',
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
			await new Promise((r) => setTimeout(r, delayMs));
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

		const cleanup = () => clearInterval(interval);
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
		const forced = (req.url ?? '').includes('force=1');
		fetchJSON('/monitoring/matrix').then(({ status, body }) => {
			if (body && typeof body === 'object' && body.data) {
				const data = body.data;
				enrichMonitoringMatrixAwgTunnels(data.tunnels);
				// On force=1 jitter the synthetic delay so the user sees the badge change.
				const veespDelay = forced ? 40 + Math.floor(Math.random() * 240) : 78;
				data.tunnels = [
					...(data.tunnels ?? []),
					{
						id: 'veesp',
						name: 'veesp',
						ifaceName: 't2s0',
						pingcheckTarget: '',
						selfTarget: '',
						selfMethod: 'disabled',
						source: 'singbox',
						singboxTag: 'veesp',
						clashDelay: veespDelay,
						urltestGroup: 'auto',
						subscription: true,
						protocol: 'vless',
						security: 'reality',
						transport: 'tcp',
					},
					{
						id: 'prague',
						name: 'prague',
						ifaceName: 't2s1',
						pingcheckTarget: '',
						selfTarget: '',
						selfMethod: 'disabled',
						source: 'singbox',
						singboxTag: 'prague',
						protocol: 'vless',
						security: 'tls',
						transport: 'grpc',
						// no urltest data — UI should NOT show the badge
					},
				];
				const targets = [
					{ id: 'dns-cf', name: 'Cloudflare DNS', host: '1.1.1.1', url: '' },
					{ id: 'dns-google', name: 'Google DNS', host: '8.8.8.8', url: '' },
					{ id: 'dns-quad9', name: 'Quad9 DNS', host: '9.9.9.9', url: '' },
					{ id: 'cc-gstatic', name: 'connectivitycheck.gstatic.com', host: 'connectivitycheck.gstatic.com', url: '' },
				];
				data.targets = targets;

				const primaryTunnelId = (data.tunnels ?? [])[0]?.id ?? '';
				const nowIso = new Date().toISOString();
				const baseLatencies = [84, 77, 79, 118];
				const jitter = forced ? Math.floor(Math.random() * 9) - 4 : 0;

				const cells = [];
				for (let ti = 0; ti < targets.length; ti++) {
					const target = targets[ti];
					for (const tun of data.tunnels ?? []) {
						if (tun.id === primaryTunnelId) {
							cells.push({
								targetId: target.id,
								tunnelId: tun.id,
								latencyMs: Math.max(15, baseLatencies[ti] + jitter),
								ok: true,
								activeForRestart: ti === 1,
								isSelf: false,
								ts: nowIso,
							});
						} else {
							cells.push({
								targetId: target.id,
								tunnelId: tun.id,
								latencyMs: null,
								ok: false,
								activeForRestart: false,
								isSelf: false,
								ts: nowIso,
							});
						}
					}
				}
				data.cells = cells;
				if (forced) data.updatedAt = new Date().toISOString();
			}
			send(res, status, body);
		});
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

	if (req.method === 'GET' && path === '/singbox/tunnels') {
		send(res, 200, {
			success: true,
			data: MOCK_SINGBOX_TUNNELS.map((t) => ({ ...t })),
		});
		return;
	}

	if (req.method === 'POST' && path === '/__mock/singbox-install-fail') {
		let raw = '';
		req.on('data', (c) => (raw += c));
		req.on('end', () => {
			try {
				const body = JSON.parse(raw);
				singboxInstallShouldFail = !!body.enabled;
				send(res, 200, { ok: true, singboxInstallShouldFail });
				console.log(`[mock-proxy] singboxInstallShouldFail → ${singboxInstallShouldFail}`);
			} catch (e) {
				send(res, 400, { error: String(e) });
			}
		});
		return;
	}

	// === Wizard mock overrides ===

	if (req.method === 'GET' && path === '/singbox/router/presets/list') {
		send(res, 200, { success: true, data: mockSingboxPresets });
		return;
	}

	if (req.method === 'GET' && path === '/dns-check/client') {
		send(res, 200, { success: true, data: mockDnsCheckClientPayload });
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

	if (req.method === 'GET' && path === '/routing/policy-interfaces') {
		send(res, 200, { success: true, data: mockPolicyInterfaces });
		return;
	}

	if (req.method === 'GET' && path === '/routing/tunnels') {
		send(res, 200, { success: true, data: mockRoutingTunnels });
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
		send(res, 200, { success: true, data: mockDNSServers });
		return;
	}

	if (req.method === 'POST' && path === '/singbox/router/dns/servers/add') {
		let raw = '';
		req.on('data', (c) => (raw += c));
		req.on('end', () => {
			try {
				const payload = JSON.parse(raw || '{}');
				mockDNSServers.push(payload);
				send(res, 200, { success: true, data: payload });
			} catch (e) {
				send(res, 400, { success: false, error: { code: 'INVALID_REQUEST', message: String(e) } });
			}
		});
		return;
	}

	if (req.method === 'POST' && path === '/singbox/router/presets/apply') {
		// simulate latency for visible "Применяем" log
		await new Promise((r) => setTimeout(r, 200));
		send(res, 200, { success: true, data: {} });
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

	if (req.method === 'POST' && path === '/singbox/router/enable') {
		mockEngineRunning = true;
		send(res, 200, { success: true, data: {} });
		return;
	}

	if (req.method === 'GET' && path === '/singbox/router/status') {
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
			},
		});
		return;
	}

	if (req.method === 'GET' && path === '/singbox/awg-outbounds/tags') {
		send(res, 200, {
			success: true,
			data: [
				{ tag: 'awg-vpn0',             label: 'DE Frankfurt', kind: 'managed', iface: 't2s0' },
				{ tag: 'awg-sys-Wireguard0',   label: 'NL Amsterdam', kind: 'system',  iface: 'nwg0' },
				{ tag: 'awg-sys-Wireguard1',   label: 'FI Helsinki',  kind: 'system',  iface: 'nwg1' },
			],
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
		};
		res.write(`event: meta\ndata: ${JSON.stringify(meta)}\n\n`);

		const members = sub.members ?? [];
		let i = 0;
		const tick = () => {
			if (i >= members.length) {
				const done = {
					orphanTags: sub.orphanTags ?? [],
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
	if (req.method === 'GET' && path === '/servers/all') {
		fetchJSON('/servers/all').then(({ status, body }) => {
			if (body && typeof body === 'object' && body.data && typeof body.data === 'object') {
				body.data.servers = mockSystemServers();
				body.data.managed = [mockManagedServer(), mockManagedSystemServer()];
				body.data.managedStats = {
					Wireguard1: mockManagedStats(),
					Wireguard0: mockManagedSystemStats(),
				};
			}
			send(res, status, body);
		});
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
	const sources = ['192.168.1.5', '192.168.1.7', '192.168.1.9', '192.168.1.42'];
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
