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
		address: '10.8.0.2/32, fd00:8::2/128',
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
		interfaceName: 'awg1',
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
];

const MOCK_SYSTEM_TUNNELS = [
	{
		id: 'Wireguard3',
		interfaceName: 'nwg0',
		description: 'US New York (system)',
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
		tag: 'vless-de-main',
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
	// Process up + TUN present, but outbound unreachable (Clash delay = timeout).
	{
		tag: 'vless-blackhole',
		protocol: 'vless',
		server: 'blackhole.demo.example',
		port: 443,
		security: 'reality',
		transport: 'tcp',
		listenPort: 11014,
		proxyInterface: 't2s3',
		kernelInterface: 'sb3',
		sni: 'cdn.cloudflare.com',
		fingerprint: 'chrome',
		connectivity: { connected: false, latency: null },
		running: true,
	},
	// sing-box / kernel iface down — card shows "stopped", not a latency timeout.
	{
		tag: 'hysteria-stopped',
		protocol: 'hysteria2',
		server: 'jp-stale.demo.example',
		port: 8443,
		security: 'tls',
		transport: 'tcp',
		listenPort: 11015,
		proxyInterface: 't2s4',
		kernelInterface: 'sb4',
		sni: 'jp-stale.demo.example',
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
	'vless-de-main': {
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
	'vless-blackhole': {
		baseRx: 8_000,
		baseTx: 2_000,
		waveRx: 4_000,
		waveTx: 1_000,
		driftRx: 2_000,
		driftTx: 500,
		rxStep: 6_000,
		txStep: 2_000,
		jitterRx: 1_000,
		jitterTx: 400,
		burstEvery: 0,
		burstRx: 0,
		burstTx: 0,
	},
	'hysteria-stopped': {
		baseRx: 0,
		baseTx: 0,
		waveRx: 0,
		waveTx: 0,
		driftRx: 0,
		driftTx: 0,
		rxStep: 0,
		txStep: 0,
		jitterRx: 0,
		jitterTx: 0,
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
			download: 64_000_000 + i * 10_000_000,
			upload: 12_000_000 + i * 4_000_000,
			downloadStep: trafficProfile(t.tag).rxStep,
			uploadStep: trafficProfile(t.tag).txStep,
			profileId: t.tag,
			tick: i * 3,
		},
	]),
);

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

/** Member tags that always report delay timeout in mocks (matches proxies.test + SSE). */
const MOCK_SUB_DEAD_MEMBER_TAGS = new Set([
	'sub-demo0001-deadrelay',
	'sub-bigprov-rudead01',
]);

function delayMsForSubscriptionMemberTag(tag) {
	if (MOCK_SUB_DEAD_MEMBER_TAGS.has(tag)) return 0;
	let h = 0;
	for (let i = 0; i < tag.length; i++) h = ((h << 5) - h + tag.charCodeAt(i)) | 0;
	const base = Math.abs(h) % 370 + 30;
	const jitter = Math.floor(Math.random() * 40) - 20;
	return Math.max(1, base + jitter);
}

/** Samples for SSE `singbox:delay`: managed tunnels + every subscription member tag. */
function buildSingboxDelayEventBatch() {
	const out = [];
	MOCK_SINGBOX_TUNNELS.forEach((t, i) => {
		let delay;
		if (t.running === false) delay = 0;
		else if (!t.connectivity?.connected) delay = 0;
		else delay = 70 + i * 35 + Math.floor(Math.random() * 45);
		out.push({ tag: t.tag, delay });
	});
	for (const sub of mockSubscriptions) {
		for (const tag of sub.memberTags) {
			out.push({ tag, delay: delayMsForSubscriptionMemberTag(tag) });
		}
	}
	return out;
}

// ── Subscriptions mock state ───────────────────────────────────
// Pre-populated for visual testing — shows non-empty list state, selector with members.
let mockSubscriptions = [
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
			'sub-demo0001-deadrelay',
		],
		members: [
			{ tag: 'sub-demo0001-aabbccdd', protocol: 'vless', server: 'de01.demo.example', port: 443, transport: 'tcp', security: 'reality' },
			{ tag: 'sub-demo0001-eeff0011', protocol: 'vless', server: 'nl02.demo.example', port: 443, transport: 'ws', security: 'tls' },
			{ tag: 'sub-demo0001-22334455', protocol: 'trojan', server: 'fi03.demo.example', port: 443, transport: 'tcp', security: 'tls' },
			{ tag: 'sub-demo0001-deadrelay', label: 'DE — мёртвый relay', protocol: 'vless', server: '0.0.0.1', port: 443, transport: 'tcp', security: 'reality' },
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
		// - 12 members (above the "10+ feels slow" threshold)
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
			'sub-bigprov-rudead01',
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
			{ tag: 'sub-bigprov-rudead01', label: '🇷🇺 RU — offline',          protocol: 'vless',      server: '10.255.255.1',        port: 443,   sni: 'dead.bigprov.example', transport: 'tcp', security: 'tls' },
		],
		orphanTags: [],
		activeMember: 'sub-bigprov-de01a1b2',
		enabled: true,
	},
	{
		id: 'sub-off-legacy',
		label: 'Legacy EU (выключена)',
		url: 'https://legacy.example/sub/old',
		headers: [],
		refreshHours: 0,
		lastFetched: new Date(Date.now() - 90 * 24 * 3600 * 1000).toISOString(),
		lastError: '',
		selectorTag: 'sub-off-legacy',
		inboundTag: 'sub-off-legacy-in',
		listenPort: 11004,
		proxyIndex: 14,
		memberTags: ['sub-off-legacy-m01'],
		members: [
			{ tag: 'sub-off-legacy-m01', protocol: 'vless', server: 'legacy.example', port: 443, transport: 'tcp', security: 'tls' },
		],
		orphanTags: [],
		activeMember: 'sub-off-legacy-m01',
		enabled: false,
	},
	{
		id: 'sub-off-trial',
		label: 'Пробный период (выключена)',
		url: 'https://trial.example/sub/expired',
		headers: [],
		refreshHours: 12,
		lastFetched: new Date(Date.now() - 14 * 24 * 3600 * 1000).toISOString(),
		lastError: 'subscription expired',
		selectorTag: 'sub-off-trial',
		inboundTag: 'sub-off-trial-in',
		listenPort: 11005,
		proxyIndex: 15,
		memberTags: ['sub-off-trial-m01', 'sub-off-trial-m02'],
		members: [
			{ tag: 'sub-off-trial-m01', label: 'Node A', protocol: 'trojan', server: 'trial.example', port: 443, transport: 'ws', security: 'tls' },
			{ tag: 'sub-off-trial-m02', label: 'Node B', protocol: 'vless', server: 'trial2.example', port: 443, transport: 'tcp', security: 'reality' },
		],
		orphanTags: [],
		activeMember: 'sub-off-trial-m01',
		enabled: false,
	},
];
let mockSubID = 2;

function newSub(input) {
	mockSubID++;
	const id = `sub-${mockSubID.toString().padStart(8, '0')}`;
	const shortID = id.slice(0, 8);
	const memberTags = [`sub-${shortID}-aaaa`, `sub-${shortID}-bbbb`];
	return {
		id,
		label: input.label || 'Test',
		url: input.url || 'https://test',
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
let mockBoundDevices = new Set();
let mockDNSServers = [];
let mockDNSRules = [];
const mockPolicyDevices = [
	{ mac: 'aa:aa:aa:aa:aa:01', ip: '192.168.1.42', name: 'Test-Phone',    hostname: 'phone',  active: true, link: 'WiFi', policy: '' },
	{ mac: 'aa:aa:aa:aa:aa:02', ip: '192.168.1.43', name: 'Test-Laptop',   hostname: 'laptop', active: true, link: 'WiFi', policy: '' },
	{ mac: 'aa:aa:aa:aa:aa:03', ip: '192.168.1.44', name: 'BoundElsewhere', hostname: 'other', active: true, link: 'WiFi', policy: 'OtherPolicy' },
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
		policy: 'default',
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

async function fetchJSON(path, init) {
	const r = await fetch(`${UPSTREAM}${path}`, init);
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
		all: ['vless-1', 'vless-dead', 'vless-2', 'vless-3'],
	},
	'auto': {
		type: 'urltest',
		now: 'vless-2',
		all: ['vless-1', 'vless-2', 'vless-3', 'vless-4'],
	},
};
const mockProxyDelays = {
	'vless-1': 45,
	'vless-dead': 0,
	'vless-2': 78,
	'vless-3': 180,
	'vless-4': 320,
};
const PROXY_DELAY_ALWAYS_DEAD = new Set(['vless-dead']);
function randomizeDelays() {
	for (const k of Object.keys(mockProxyDelays)) {
		if (PROXY_DELAY_ALWAYS_DEAD.has(k)) {
			mockProxyDelays[k] = 0;
			continue;
		}
		const base = mockProxyDelays[k];
		if (Math.random() < 0.05) {
			mockProxyDelays[k] = 0; // 5% timeout
		} else {
			mockProxyDelays[k] = Math.max(10, base + Math.round((Math.random() - 0.5) * 40));
		}
	}
}

const server = http.createServer(async (req, res) => {
	const url = new URL(req.url, `http://${req.headers.host}`);
	const path = url.pathname;

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
		try {
			const { status, body } = await fetchJSON(`${path}${url.search}`);
			if (status === 200 && body && typeof body === 'object') {
				enrichConnectionsTunnels(body);
			}
			send(res, status, body);
		} catch (e) {
			send(res, 502, { success: false, error: String(e) });
		}
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
		sendEvent('singbox:traffic', tickSingboxTraffic());
		for (const delay of buildSingboxDelayEventBatch()) {
			sendEvent('singbox:delay', delay);
		}

		const interval = setInterval(() => {
			for (const event of tickAwgTraffic()) {
				sendEvent('tunnel:traffic', event);
			}
			sendEvent('singbox:traffic', tickSingboxTraffic());
			for (const delay of buildSingboxDelayEventBatch()) {
				sendEvent('singbox:delay', delay);
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
						// no urltest data — UI should NOT show the badge
					},
				];
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
					delays[tag] = delayMsForSubscriptionMemberTag(tag);
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

	if (req.method === 'GET' && path === '/singbox/tunnels') {
		send(res, 200, {
			success: true,
			data: MOCK_SINGBOX_TUNNELS.map((t) => ({ ...t })),
		});
		return;
	}

	if (req.method === 'POST' && path === '/singbox/tunnels/delay-check') {
		const tag = new URL(req.url, 'http://x').searchParams.get('tag') ?? '';
		if (!tag) {
			send(res, 400, { success: false, error: 'tag required' });
			return;
		}
		const tunnel = MOCK_SINGBOX_TUNNELS.find((t) => t.tag === tag);
		let delay = 0;
		if (tunnel) {
			if (tunnel.running === false) delay = 0;
			else if (!tunnel.connectivity?.connected) delay = 0;
			else {
				const i = MOCK_SINGBOX_TUNNELS.indexOf(tunnel);
				delay = 70 + i * 35 + Math.floor(Math.random() * 45);
			}
		} else {
			const knownMember = mockSubscriptions.some((s) => s.memberTags.includes(tag));
			if (!knownMember) {
				send(res, 404, { success: false, error: { code: 'NOT_FOUND', message: `unknown tag ${tag}` } });
				return;
			}
			delay = delayMsForSubscriptionMemberTag(tag);
		}
		send(res, 200, { success: true, data: { tag, delay } });
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
		const data = await getPresets();
		send(res, 200, { success: true, data: data.data });
		return;
	}

	if (req.method === 'GET' && path === '/routing/policy-devices') {
		send(res, 200, { success: true, data: mockPolicyDevices });
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

	if (req.method === 'POST' && path === '/access-policies/assign') {
		let raw = '';
		req.on('data', (c) => (raw += c));
		req.on('end', () => {
			try {
				const payload = JSON.parse(raw || '{}');
				const mac = payload.mac;
				if (mac) {
					mockBoundDevices.add(mac);
					const dev = mockPolicyDevices.find((d) => d.mac === mac);
					if (dev) dev.policy = payload.policy ?? 'SBRouter';
				}
				send(res, 200, { success: true, data: {} });
			} catch (e) {
				send(res, 400, { success: false, error: { code: 'INVALID_REQUEST', message: String(e) } });
			}
		});
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
		send(res, 200, { success: true, data: mockSubscriptions });
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
				send(res, 200, { success: true, data: sub });
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
		send(res, 200, { success: true, data: sub });
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
				send(res, 200, { success: true, data: sub });
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
				body.data.managed = [mockManagedServer()];
				body.data.managedStats = { Wireguard1: mockManagedStats() };
			}
			send(res, status, body);
		});
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
