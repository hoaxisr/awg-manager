import { describe, expect, it } from 'vitest';
import type {
	FircGroup,
	FircGroupLive,
	FircInterfaceOption,
	FircInterfaceProbeResult,
	FircProxyGroup,
	FircTunnelProbeRow,
	FircTunnelState,
} from '$lib/types/firc';
import {
	formatProbeLatency,
	friendlyIfaceLabel,
	getEffectiveInterfaceProbe,
	isInternalServerFircInterface,
	isSelectableFircFallbackInterface,
	probeLatencyTone,
	resolveGroupActiveInterface,
	resolveProxyGroupRuntime,
} from './fircFallback';

const IFACES: FircInterfaceOption[] = [
	{ id: 'tunvless0', name: 'tt-rus (tunvless0)', role: 'tunnel', up: true },
	{ id: 'tunvless4', name: 'tt-usa (tunvless4)', role: 'tunnel', up: true },
	{ id: 'tunvless2', name: 'tt-lat (tunvless2)', role: 'tunnel', up: false },
	{ id: 'eth3', name: 'Rostelecom', role: 'wan', up: true },
];

function makeTunnelState(device: string, status: FircTunnelState['status'], id = device): FircTunnelState {
	return {
		id,
		device,
		status,
		since: 0,
		backoffS: 0,
		lastExit: 0,
		uplinkOk: true,
		tcpPort: 0,
		tcpSkipped: [],
		tcpOff: '',
		active: status === 'up' ? ['node-1'] : [],
		groups: [],
		nodes: status === 'up' ? [{ key: 'n1', name: 'node-1', source: 'sub', state: 'active', since: 0 }] : [],
		subscriptions: [],
		rxBps: 0,
		txBps: 0,
		inbounds: [],
	};
}

describe('fircFallback', () => {
	it('formats friendly interface labels for tunnels and WAN', () => {
		expect(friendlyIfaceLabel('tunvless4', IFACES)).toBe('tt-usa');
		expect(friendlyIfaceLabel('eth3', IFACES)).toBe('Direct (eth3)');
		expect(friendlyIfaceLabel('unknown0', IFACES)).toBe('unknown0');
	});

	it('resolves primary active vs failover to backup vs all dead on a group', () => {
		const group: Pick<FircGroup, 'interface'> = {
			interface: ['tunvless0', 'tunvless4', 'eth3'],
		};

		const primaryLive: FircGroupLive = {
			live: true,
			active: 'tunvless0',
			interfaces: [
				{ name: 'tunvless0', state: 'alive', since: 1, error: '' },
				{ name: 'tunvless4', state: 'alive', since: 1, error: '' },
			],
		};
		expect(resolveGroupActiveInterface(group, primaryLive, {}, [], IFACES)).toEqual({
			active: 'tunvless0',
			isBackup: false,
			allDead: false,
		});

		const backupLive: FircGroupLive = {
			live: true,
			active: 'tunvless4',
			interfaces: [
				{ name: 'tunvless0', state: 'dead', since: 1, error: 'no traffic' },
				{ name: 'tunvless4', state: 'alive', since: 1, error: '' },
			],
		};
		expect(resolveGroupActiveInterface(group, backupLive, {}, [], IFACES)).toEqual({
			active: 'tunvless4',
			isBackup: true,
			allDead: false,
		});

		const deadLive: FircGroupLive = {
			live: false,
			active: '',
			interfaces: [
				{ name: 'tunvless0', state: 'dead', since: 1, error: 'down' },
				{ name: 'tunvless4', state: 'dead', since: 1, error: 'down' },
				{ name: 'eth3', state: 'dead', since: 1, error: 'down' },
			],
		};
		expect(resolveGroupActiveInterface(group, deadLive, {}, [], IFACES)).toEqual({
			active: null,
			isBackup: false,
			allDead: true,
		});
	});

	it('resolves proxy group runtime from matched FIRC group and standalone fallback health', () => {
		const pg: FircProxyGroup = {
			id: 'pg1',
			name: 'AI Chain',
			interfaces: ['tunvless0', 'tunvless4', 'eth3'],
		};

		const groups: FircGroup[] = [
			{
				id: 'g1',
				name: 'OpenAI',
				interface: ['tunvless0', 'tunvless4', 'eth3'],
				enable: true,
				rules: [],
			},
		];

		const liveMap: Record<string, FircGroupLive> = {
			g1: {
				live: true,
				active: 'tunvless4',
				interfaces: [
					{ name: 'tunvless0', state: 'dead', since: 10, error: 'the tunnel carries no traffic' },
					{ name: 'tunvless4', state: 'alive', since: 10, error: '' },
					{ name: 'eth3', state: 'alive', since: 10, error: '' },
				],
			},
		};

		const probes: Record<string, FircInterfaceProbeResult> = {
			tunvless0: { interface: 'tunvless0', ok: false, latencyMs: 0, error: 'timeout' },
			tunvless4: { interface: 'tunvless4', ok: true, latencyMs: 142 },
		};

		const matched = resolveProxyGroupRuntime(pg, groups, liveMap, [], IFACES, probes, {});
		expect(matched.usedByCount).toBe(1);
		expect(matched.activeIface).toBe('tunvless4');
		expect(matched.isOnBackup).toBe(true);
		expect(matched.allDead).toBe(false);
		expect(matched.members.map((m) => [m.id, m.isActive, m.state, m.error])).toEqual([
			['tunvless0', false, 'dead', 'the tunnel carries no traffic'],
			['tunvless4', true, 'alive', ''],
			['eth3', false, 'alive', ''],
		]);

		// Standalone (no matching FIRC group) resolves from tunnel states & interfaces in priority order
		const standalone = resolveProxyGroupRuntime(
			pg,
			[],
			{},
			[makeTunnelState('tunvless0', 'no_node', 't0'), makeTunnelState('tunvless4', 'up', 't4')],
			IFACES,
			{},
			{},
		);
		expect(standalone.usedByCount).toBe(0);
		expect(standalone.activeIface).toBe('tunvless4');
		expect(standalone.isOnBackup).toBe(true);
	});

	it('treats live:false with all configured interfaces dead as allDead even when active field lingers', () => {
		const group: Pick<FircGroup, 'interface'> = {
			interface: ['eth3', 'tunvless4'],
		};
		const blockedLive: FircGroupLive = {
			live: false,
			active: 'eth3',
			interfaces: [
				{ name: 'eth3', state: 'dead', since: 10, error: 'timeout' },
				{ name: 'tunvless4', state: 'dead', since: 10, error: 'down' },
			],
		};
		expect(resolveGroupActiveInterface(group, blockedLive, {}, [], IFACES)).toEqual({
			active: null,
			isBackup: false,
			allDead: true,
		});
	});

	it('prefers per-group checked/failed-over group over unchecked group sharing same chain and flags divergence', () => {
		const pg: FircProxyGroup = {
			id: 'pg_keen',
			name: 'KeenDNS Fallback + Direct',
			interfaces: ['eth3', 'tunvless4'],
		};
		const groups: FircGroup[] = [
			{
				id: 'c100d001',
				name: 'KeenDNS / Облако Keenetic',
				interface: ['eth3', 'tunvless4'],
				enable: true,
				check: { mode: 'none' },
				rules: [],
			},
			{
				id: 'c100d002',
				name: 'Global',
				interface: ['eth3', 'tunvless4'],
				enable: true,
				check: { mode: 'url' },
				rules: [],
			},
		];
		const liveMap: Record<string, FircGroupLive> = {
			c100d001: {
				live: true,
				active: 'eth3',
				interfaces: [
					{ name: 'eth3', state: 'alive', since: 10, error: '' },
					{ name: 'tunvless4', state: 'alive', since: 10, error: '' },
				],
			},
			c100d002: {
				live: true,
				active: 'tunvless4',
				interfaces: [
					{ name: 'eth3', state: 'dead', since: 20, error: 'timeout' },
					{ name: 'tunvless4', state: 'alive', since: 20, error: '' },
				],
			},
		};
		const rt = resolveProxyGroupRuntime(pg, groups, liveMap, [], IFACES, {}, {});
		expect(rt.activeIface).toBe('tunvless4');
		expect(rt.isOnBackup).toBe(true);
		expect(rt.hasDivergentActive).toBe(true);
		expect(rt.divergentSummary).toBe('KeenDNS / Облако Keenetic -> Direct (eth3); Global -> tt-usa');
	});

	it('prefers explicit interface probe over tunnel node probe fallback', () => {
		const tunnelsState = [makeTunnelState('tunvless4', 'up', 'tun_4')];
		const tunnelProbes: Record<string, Record<string, FircTunnelProbeRow>> = {
			tun_4: {
				n1: { key: 'n1', ok: true, why: '', handshakeMs: 85, firstByteMs: 165 },
			},
		};

		const fromTunnel = getEffectiveInterfaceProbe('tunvless4', {}, tunnelProbes, tunnelsState);
		expect(fromTunnel).toEqual({ interface: 'tunvless4', ok: true, latencyMs: 165 });

		const explicitProbes: Record<string, FircInterfaceProbeResult> = {
			tunvless4: { interface: 'tunvless4', ok: true, latencyMs: 98 },
		};
		const fromExplicit = getEffectiveInterfaceProbe('tunvless4', explicitProbes, tunnelProbes, tunnelsState);
		expect(fromExplicit).toEqual({ interface: 'tunvless4', ok: true, latencyMs: 98 });
	});

	it('classifies latency tones and formats labels consistently', () => {
		expect(probeLatencyTone(null)).toBe('none');
		expect(probeLatencyTone({ interface: 'a', ok: true, latencyMs: 120 })).toBe('good');
		expect(probeLatencyTone({ interface: 'a', ok: true, latencyMs: 280 })).toBe('medium');
		expect(probeLatencyTone({ interface: 'a', ok: true, latencyMs: 450 })).toBe('bad');
		expect(probeLatencyTone({ interface: 'a', ok: false, latencyMs: 0 })).toBe('bad');

		expect(formatProbeLatency(null)).toBe('—');
		expect(formatProbeLatency({ interface: 'a', ok: false, latencyMs: 0, error: 'timeout' })).toBe('таймаут');
		expect(formatProbeLatency({ interface: 'a', ok: true, latencyMs: 118.4 })).toBe('118 мс');
	});

	it('includes wdtt client tunnels while hiding only internal server interfaces', () => {
		expect(
			isInternalServerFircInterface({
				id: 'opkgtun18',
				name: 'AWGM WDTT Raw Client: Дом',
				ndmsName: 'OpkgTun18',
			}),
		).toBe(false);
		expect(
			isInternalServerFircInterface({
				id: 'opkgtun17',
				name: 'AWGM WDTT Raw Client',
			}),
		).toBe(false);
		expect(
			isInternalServerFircInterface({
				id: 'opkgtun19',
				name: 'AWGM WDTT client',
			}),
		).toBe(false);
		expect(
			isInternalServerFircInterface({
				id: 'opkgtun17',
				name: 'Дача WDTT',
			}),
		).toBe(false);
		expect(
			isInternalServerFircInterface({
				id: 'wdttraw0',
				name: 'WDTT DE',
			}),
		).toBe(false);

		expect(
			isInternalServerFircInterface({
				id: 'opkgtun17',
				name: 'AWGM WDTT',
			}),
		).toBe(true);
		expect(
			isInternalServerFircInterface({
				id: 'opkgtun19',
				ndmsName: 'AWGM WDTT Raw',
			}),
		).toBe(true);
		expect(
			isInternalServerFircInterface({
				id: 'opkgtun20',
				name: 'AWGM WDTT Server',
			}),
		).toBe(true);
		expect(
			isInternalServerFircInterface({
				id: 'nwg2',
				name: 'AWGM',
			}),
		).toBe(true);
		expect(
			isInternalServerFircInterface({
				id: 'nwg0',
				name: 'WireGuard VPN Server',
			}),
		).toBe(true);

		expect(
			isSelectableFircFallbackInterface({
				id: 'opkgtun18',
				name: 'AWGM WDTT Raw Client: Дом',
				role: 'tunnel',
			}),
		).toBe(true);
		expect(
			isSelectableFircFallbackInterface({
				id: 'opkgtun19',
				name: 'AWGM WDTT Raw Client: Дом',
				role: 'tunnel',
			}),
		).toBe(true);
		expect(
			isSelectableFircFallbackInterface({
				id: 'wdttraw0',
				name: 'wdttraw0',
				role: 'tunnel',
			}),
		).toBe(true);
		expect(
			isSelectableFircFallbackInterface({
				id: 't2s0',
				name: 'Sing-box',
				role: 'tunnel',
			}),
		).toBe(false);
		expect(
			isSelectableFircFallbackInterface({
				id: 'eth3',
				name: 'Broadband',
				role: 'wan',
			}),
		).toBe(true);
		expect(
			isSelectableFircFallbackInterface({
				id: 'opkgtun17',
				name: 'AWGM WDTT',
				role: 'tunnel',
			}),
		).toBe(false);
		expect(
			isSelectableFircFallbackInterface({
				id: 'opkgtun10',
				name: 'Дача FT',
				role: 'tunnel',
			}),
		).toBe(true);
		expect(
			isSelectableFircFallbackInterface({
				id: 'tunvless2',
				name: 'LAT',
				role: 'tunnel',
			}),
		).toBe(true);
	});
});
