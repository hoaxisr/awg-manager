import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import '@testing-library/jest-dom/vitest';
import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/svelte';
import { get } from 'svelte/store';
import { m } from '$lib/i18n';

const {
	fircGetTunnels,
	fircGetTunnelsState,
	fircGetGroups,
	fircGetStatus,
	fircGetSettings,
	fircGetMigrationState,
	fircPreviewFromSingbox,
	fircListSnapshots,
	fircMigrateFromSingbox,
	fircProbeTunnel,
	fircProbeInterfaces,
	fircSaveTunnels,
	fircPreviewTunnel,
} = vi.hoisted(() => {
	if (typeof window !== 'undefined' && typeof window.matchMedia !== 'function') {
		Object.defineProperty(window, 'matchMedia', {
			writable: true,
			value: (query: string) => ({
				matches: false,
				media: query,
				onchange: null,
				addListener: () => {},
				removeListener: () => {},
				addEventListener: () => {},
				removeEventListener: () => {},
				dispatchEvent: () => false,
			}),
		});
	}
	if (typeof globalThis.ResizeObserver === 'undefined') {
		globalThis.ResizeObserver = class {
			observe() {}
			unobserve() {}
			disconnect() {}
		};
	}
	return {
		fircGetTunnels: vi.fn(),
		fircGetTunnelsState: vi.fn(),
		fircGetGroups: vi.fn(),
		fircGetStatus: vi.fn(),
		fircGetSettings: vi.fn(),
		fircGetMigrationState: vi.fn(),
		fircPreviewFromSingbox: vi.fn(),
		fircListSnapshots: vi.fn(),
		fircMigrateFromSingbox: vi.fn(),
		fircProbeTunnel: vi.fn(),
		fircProbeInterfaces: vi.fn(),
		fircSaveTunnels: vi.fn(),
		fircPreviewTunnel: vi.fn(),
	};
});

vi.mock('$lib/api/client', () => ({
	api: {
		fircGetTunnels,
		fircGetTunnelsState,
		fircGetGroups,
		fircGetStatus,
		fircGetSettings,
		fircGetMigrationState,
		fircPreviewFromSingbox,
		fircListSnapshots,
		fircMigrateFromSingbox,
		singboxListTunnels: vi.fn().mockResolvedValue([]),
		singboxGetStatus: vi.fn().mockResolvedValue({ installed: true, running: false }),
		getSingboxDelay: vi.fn().mockResolvedValue({ delay: 0 }),
		getTraffic: vi.fn().mockResolvedValue({ points: [], stats: {} }),
		fircProbeTunnel,
		fircProbeInterfaces,
		fircSaveTunnels,
		fircPreviewTunnel,
		fircRestartTunnel: vi.fn().mockResolvedValue({ ok: true }),
		fircRefreshTunnel: vi.fn().mockResolvedValue({ ok: true }),
		listSubscriptionHeaderProfiles: vi.fn().mockResolvedValue([]),
		getHappKeysInfo: vi.fn().mockResolvedValue({ configured: false, count: 0 }),
	},
}));

vi.mock('$lib/stores/notifications', () => ({
	notifications: { success: vi.fn(), error: vi.fn(), warning: vi.fn(), info: vi.fn() },
}));

import FircTunnelsTabSection from './FircTunnelsTabSection.svelte';
import FircTunnelEditModal from './FircTunnelEditModal.svelte';
import SingboxTunnelCard from '$lib/components/singbox/SingboxTunnelCard.svelte';
import SingboxTunnelsTabSection from '$lib/components/singbox/SingboxTunnelsTabSection.svelte';
import { notifications } from '$lib/stores/notifications';
import { settings } from '$lib/stores/settings';
import {
	fircStatus,
	fircTunnels,
	fircTunnelsState,
	fircGroups,
	fircEditTunnelId,
	fircEditTunnelSubTab,
	fircProbeResults,
	fircInterfaceProbeResults,
} from '$lib/stores/firc';
import { singboxStatus, singboxTunnels, singboxDelayHistory } from '$lib/stores/singbox';
import { singboxRouter } from '$lib/stores/singboxRouter';
import {
	closeFircMigrationModal,
	fircMigrationModalOpen,
	fircMigrationModalTab,
	notifyFircMigrationApplied,
} from '$lib/stores/fircMigrationModal';
import type { SingboxTunnel } from '$lib/types';

beforeEach(() => {
	vi.clearAllMocks();
	closeFircMigrationModal();
	settings.set(null);
	singboxRouter.setSettings({
		routingEngine: 'firc',
		enabled: false,
		policyName: '',
		snifferEnabled: true,
		wanAutoDetect: true,
	});
	singboxDelayHistory.set(new Map());
	singboxTunnels.applyMutationResponse([]);
	singboxStatus.applyMutationResponse({ installed: true, running: false } as never);
	fircTunnels.set([]);
	fircTunnelsState.set([]);
	fircGroups.set([]);
	fircEditTunnelId.set(null);
	fircEditTunnelSubTab.set('nodes');
	fircProbeResults.set({});
	fircInterfaceProbeResults.set({});
	fircProbeTunnel.mockResolvedValue([]);
	fircProbeInterfaces.mockResolvedValue({ url: '', checkedAt: 0, results: {} });
	fircSaveTunnels.mockResolvedValue({ tunnels: [] });
	fircPreviewTunnel.mockResolvedValue({
		nodes: [],
		filteredNodes: [],
		matched: 0,
		total: 0,
		subscriptions: [],
	});
	const runningStatus = {
		fircdInstalled: true,
		tunvlessInstalled: true,
		running: true,
		selected: true,
		socketPath: '/opt/var/run/fircd.sock',
		dnsProxyEndpoint: '127.0.0.1:3553',
		inboundPort: 1099,
		tunnelsCount: 1,
		groupsCount: 0,
	};
	fircStatus.set(runningStatus);
	fircGetStatus.mockResolvedValue(runningStatus);
	fircGetSettings.mockResolvedValue({});
	fircGetGroups.mockResolvedValue([]);
	fircGetTunnels.mockResolvedValue([
		{
			id: 'lat',
			device: 'tunvless0',
			description: 'LAT Tunnel',
			enable: true,
			active: 1,
			by: 'connection',
			sources: [{ id: '11223344', kind: 'link', name: 'LAT', link: 'vless://u@1.2.3.4:443#LAT' }],
			inbounds: [{ port: 1099, listen: 'br0', node: '11223344:LAT' }],
		},
	]);
	fircGetTunnelsState.mockResolvedValue([
		{
			id: 'lat',
			device: 'tunvless0',
			status: 'up',
			active: ['LAT'],
			nodes: [{ key: '11223344:LAT', name: 'LAT', state: 'active' }],
			inbounds: [{ port: 1099, state: 'no-node', why: 'node missing' }],
			groups: [],
			rxBps: 0,
			txBps: 0,
			since: 0,
			backoffS: 0,
			lastExit: 0,
			uplinkOk: true,
			tcpPort: 41000,
			tcpSkipped: [],
			tcpOff: '',
			subscriptions: [],
		},
	]);
});

afterEach(cleanup);

describe('FIRC tunnels UI integration', () => {
	it('separates engine, daemon, active node, and inbound :1099 no-node status clearly', async () => {
		render(FircTunnelsTabSection, { props: { dashboardMode: false, showTrustTunnels: false } });

		const strip = await screen.findByTestId('firc-runtime-strip');
		await waitFor(() => {
			expect(strip).toHaveTextContent(m.firc_engine_label());
			expect(strip).toHaveTextContent('FIRC');
			expect(strip).toHaveTextContent(m.firc_daemon_running());
			expect(strip).toHaveTextContent('LAT');
			expect(strip).toHaveTextContent(':1099');
			expect(strip).toHaveTextContent(m.firc_inbound_no_node());
		});

		fircGetTunnelsState.mockResolvedValueOnce([
			{
				id: 'lat',
				device: 'tunvless0',
				status: 'up',
				active: ['LAT'],
				nodes: [{ key: '11223344:LAT', name: 'LAT', state: 'active' }],
				inbounds: [{ port: 1099, state: 'up' }],
				groups: [],
				rxBps: 0,
				txBps: 0,
				since: 0,
				backoffS: 0,
				lastExit: 0,
				uplinkOk: true,
				tcpPort: 41000,
				tcpSkipped: [],
				tcpOff: '',
				subscriptions: [],
			},
		]);
		const beforeCalls = fircGetTunnels.mock.calls.length;
		notifyFircMigrationApplied();

		await waitFor(() => {
			expect(fircGetTunnels.mock.calls.length).toBeGreaterThan(beforeCalls);
			expect(screen.getByTestId('firc-runtime-strip')).toHaveTextContent(m.firc_inbound_up());
		});
	});

	it('opens migration wizard from FIRC tunnels tab button', async () => {
		render(FircTunnelsTabSection, { props: { dashboardMode: false, showTrustTunnels: false } });
		const btn = await screen.findByRole('button', { name: m.firc_migration_btn() });
		await fireEvent.click(btn);
		expect(get(fircMigrationModalOpen)).toBe(true);
		expect(get(fircMigrationModalTab)).toBe('forward');
	});

	it('marks saved sing-box configs as Saved rather than Running when FIRC owns routing', async () => {
		const sampleTunnel: SingboxTunnel = {
			tag: 'lat-sb',
			protocol: 'vless',
			server: '1.2.3.4',
			port: 443,
			security: 'reality',
			transport: 'tcp',
			listenPort: 0,
			proxyInterface: 'via sing-box',
			connectivity: { connected: false, latency: null },
			running: false,
		};
		render(SingboxTunnelCard, {
			props: { tunnel: sampleTunnel, layout: 'compact', renderMode: 'compact' },
		});
		expect(screen.getByText(m.singbox_saved_pill())).toBeInTheDocument();
		expect(screen.queryByText(m.singbox_running_pill())).not.toBeInTheDocument();

		cleanup();
		render(SingboxTunnelsTabSection, {
			props: {
				dashboardOn: false,
				dashboardSingboxTunnels: [],
				singboxTunnelsList: [sampleTunnel],
				sortedFilteredSingboxTunnels: [sampleTunnel],
				singboxTunnelListStats: {
					count: 1,
					running: 0,
					stopped: 1,
					down: 0,
					up: 0,
					avgDelayMs: null,
					leaderBytes: 0,
					leaderName: '—',
				},
				singboxTunnelsSourceRowCount: 1,
				singboxTunnelsSearchEmpty: false,
				singboxAutoDelayCheckNonce: 0,
				showSingboxGridListToggle: true,
				effectiveSingboxTunnelsEffectiveLayout: 'compact',
				effectiveSingboxTunnelsRenderMode: 'compact',
				subscriptionsActiveCards: [],
				singboxTunnelsSearchQuery: '',
				singboxTunnelsLayoutMode: 'compact',
				handleSingboxTunnelSortChange: vi.fn(),
				openSingboxDetail: vi.fn(),
				openWizard: vi.fn(),
			},
		});
		expect(screen.getByText(m.singbox_saved_banner())).toBeInTheDocument();
		const migBtn = screen.getByRole('button', { name: m.singbox_saved_migrate_btn() });
		await fireEvent.click(migBtn);
		expect(get(fircMigrationModalOpen)).toBe(true);
		expect(get(fircMigrationModalTab)).toBe('reverse');
	});

	it('renders FircResourceCard UI parity with TrustTunnel/Hysteria2 badges, StatStrip, Test modal, and Edit modal', async () => {
		fircGetTunnels.mockResolvedValue([
			{
				id: 'fin',
				device: 'tunvless2',
				description: 'FIN (Финляндия) (Premium)',
				enable: true,
				active: 1,
				by: 'connection',
				sources: [{ id: 's1', kind: 'link', name: 'FIN', link: 'tt://host.example:443?d=fin' }],
				inbounds: [],
			},
			{
				id: 'hy',
				device: 'tunvless3',
				description: 'HY2 Fast',
				enable: true,
				active: 1,
				by: 'connection',
				sources: [{ id: 's2', kind: 'link', name: 'HY2', link: 'hysteria2://pass@1.2.3.4:443#HY2' }],
				inbounds: [],
			},
		]);
		fircGetTunnelsState.mockResolvedValue([
			{
				id: 'fin',
				device: 'tunvless2',
				status: 'up',
				active: ['FIN'],
				nodes: [{ key: 's1:FIN', name: 'FIN', state: 'active' }],
				inbounds: [],
				groups: [],
				rxBps: 0,
				txBps: 0,
				since: 0,
				backoffS: 0,
				lastExit: 0,
				uplinkOk: true,
				tcpPort: 41002,
				tcpSkipped: [],
				tcpOff: '',
				subscriptions: [],
			},
			{
				id: 'hy',
				device: 'tunvless3',
				status: 'up',
				active: ['HY2'],
				nodes: [{ key: 's2:HY2', name: 'HY2', state: 'active' }],
				inbounds: [],
				groups: [],
				rxBps: 0,
				txBps: 0,
				since: 0,
				backoffS: 0,
				lastExit: 0,
				uplinkOk: true,
				tcpPort: 41003,
				tcpSkipped: [],
				tcpOff: '',
				subscriptions: [],
			},
		]);

		render(FircTunnelsTabSection, { props: { dashboardMode: false, showTrustTunnels: false } });

		expect(await screen.findByText('FIN (Финляндия) (Premium)')).toBeInTheDocument();
		expect(screen.getByText('TrustTunnel')).toBeInTheDocument();
		expect(screen.getByText('TLS')).toBeInTheDocument();
		expect(screen.getByText('Hysteria2')).toBeInTheDocument();
		expect(screen.getByText('QUIC')).toBeInTheDocument();
		expect(screen.getAllByText('FIRC').length).toBeGreaterThanOrEqual(2);
		expect(screen.getByText('2/2')).toBeInTheDocument();
		expect(screen.getByText(m.firc_tabs_traffic_while_viewing())).toBeInTheDocument();
		expect(screen.getByText(m.singbox_tabs_avg_delay())).toBeInTheDocument();
		expect(screen.getByText(m.tunnels_awg_stat_leader())).toBeInTheDocument();

		const testBtn = screen.getByRole('button', {
			name: m.firc_resource_test_title({ name: 'FIN (Финляндия) (Premium)' }),
		});
		await fireEvent.click(testBtn);
		expect(
			await screen.findByText(m.tunnel_test_title({ prefix: 'FIRC', name: 'FIN (Финляндия) (Premium)' })),
		).toBeInTheDocument();

		const editBtns = screen.getAllByText(m.common_edit());
		await fireEvent.click(editBtns[0]);
		expect(
			await screen.findByText(m.firc_tunnel_edit_title({ name: 'FIN (Финляндия) (Premium)', device: 'tunvless2' })),
		).toBeInTheDocument();
	});

	it('probes running tunnels in parallel via fircProbeInterfaces and updates latency & StatStrip', async () => {
		const manyNodes = Array.from({ length: 47 }, (_, idx) => ({
			key: `sub:${idx}`,
			name: `Node ${idx + 1}`,
			state: idx === 0 ? 'active' : 'reserve',
		}));
		fircGetTunnels.mockResolvedValue([
			{
				id: 'vox',
				device: 'tunvless4',
				description: 'VOX',
				enable: true,
				active: 1,
				by: 'connection',
				sources: [{ id: 's-vox', kind: 'subscription', name: 'VOX', url: 'https://example.invalid/sub' }],
				inbounds: [],
			},
			{
				id: 'fp',
				device: 'tunvless5',
				description: 'Dom FiniteProto',
				enable: true,
				active: 1,
				by: 'connection',
				sources: [{ id: 's-fp', kind: 'link', name: 'FP', link: 'vless://u@1.2.3.4:443?type=finiteproto#FP' }],
				inbounds: [],
			},
			{
				id: 'stopped_tun',
				device: 'tunvless6',
				description: 'Stopped Tunnel',
				enable: true,
				active: 1,
				by: 'connection',
				sources: [{ id: 's-stop', kind: 'link', name: 'Stop', link: 'vless://u@1.2.3.5:443#Stop' }],
				inbounds: [],
			},
		]);
		fircGetTunnelsState.mockResolvedValue([
			{
				id: 'vox',
				device: 'tunvless4',
				status: 'up',
				active: ['Node 1'],
				nodes: manyNodes,
				inbounds: [],
				groups: [],
				rxBps: 0,
				txBps: 0,
				since: 0,
				backoffS: 0,
				lastExit: 0,
				uplinkOk: true,
				tcpPort: 41004,
				tcpSkipped: [],
				tcpOff: '',
				subscriptions: [],
			},
			{
				id: 'fp',
				device: 'tunvless5',
				status: 'up',
				active: ['FP'],
				nodes: [{ key: 's-fp:FP', name: 'FP', state: 'active' }],
				inbounds: [],
				groups: [],
				rxBps: 0,
				txBps: 0,
				since: 0,
				backoffS: 0,
				lastExit: 0,
				uplinkOk: true,
				tcpPort: 41005,
				tcpSkipped: [],
				tcpOff: '',
				subscriptions: [],
			},
			{
				id: 'stopped_tun',
				device: 'tunvless6',
				status: 'no_node',
				active: [],
				nodes: [{ key: 's-stop:Stop', name: 'Stop', state: 'down' }],
				inbounds: [],
				groups: [],
				rxBps: 0,
				txBps: 0,
				since: 0,
				backoffS: 0,
				lastExit: 0,
				uplinkOk: true,
				tcpPort: 41006,
				tcpSkipped: [],
				tcpOff: '',
				subscriptions: [],
			},
		]);

		fircProbeInterfaces.mockImplementation(async (ifaces: string[]) => {
			const dev = ifaces[0];
			if (dev === 'tunvless4') {
				return {
					url: 'http://www.gstatic.com/generate_204',
					checkedAt: 1700000001,
					results: { tunvless4: { interface: 'tunvless4', ok: true, latencyMs: 140 } },
				};
			}
			if (dev === 'tunvless5') {
				return {
					url: 'http://www.gstatic.com/generate_204',
					checkedAt: 1700000002,
					results: { tunvless5: { interface: 'tunvless5', ok: true, latencyMs: 220 } },
				};
			}
			return { url: '', checkedAt: 0, results: {} };
		});

		fircProbeTunnel.mockResolvedValue([
			{ key: 's-stop:Stop', ok: true, why: '', handshakeMs: 95, firstByteMs: 185 },
		]);

		render(FircTunnelsTabSection, { props: { dashboardMode: false, showTrustTunnels: false } });

		await waitFor(() => {
			expect(fircProbeInterfaces).toHaveBeenCalledWith(['tunvless4']);
			expect(fircProbeInterfaces).toHaveBeenCalledWith(['tunvless5']);
		});
		expect(fircProbeInterfaces).not.toHaveBeenCalledWith(['tunvless6']);
		expect(fircProbeTunnel).not.toHaveBeenCalled();

		expect(await screen.findByText('140ms')).toBeInTheDocument();
		expect(await screen.findByText('220ms')).toBeInTheDocument();
		expect(await screen.findByText('180 ms')).toBeInTheDocument();

		const pingBtns = screen.getAllByTitle(m.ui_ping_refresh_delay());
		await fireEvent.click(pingBtns[pingBtns.length - 1]);

		await waitFor(() => {
			expect(fircProbeTunnel).toHaveBeenCalledWith(
				expect.objectContaining({ id: 'stopped_tun' }),
			);
		});
	});

	it('вкладка при стороннем firc — только пояснение: ни туннелей, ни создания, ни мастера', async () => {
		const foreignStatus = { ...get(fircStatus)!, running: false, selected: false, foreign: { package: 'firc' } };
		fircStatus.set(foreignStatus);
		fircGetStatus.mockResolvedValue(foreignStatus);
		render(FircTunnelsTabSection);
		expect(await screen.findByText(m.firc_foreign_text())).toBeInTheDocument();
		expect(screen.queryAllByRole('button')).toHaveLength(0);
		// Порт интерфейса неизвестен — ссылки нет.
		expect(screen.queryByRole('link')).not.toBeInTheDocument();
	});

	it('проверка без единого узла не выдаётся за завершённую', async () => {
		fircTunnels.set([
			{
				id: 'tun-empty', device: 'tunvless7', description: 'Empty', enable: true, uplink: { kind: 'auto' },
				sources: [], active: 1, by: 'connection', interval: 30, silence: 20, filter: '', order: [], exclude: [], advanced: {},
			} as never,
		]);
		fircProbeTunnel.mockResolvedValue([]);
		fircEditTunnelId.set('tun-empty');
		render(FircTunnelEditModal);
		await fireEvent.click(await screen.findByRole('button', { name: m.firc_tunnel_edit_probe_all() }));
		await waitFor(() =>
			expect(notifications.info).toHaveBeenCalledWith(m.firc_tunnel_edit_probe_empty({ device: 'tunvless7' })),
		);
		expect(notifications.success).not.toHaveBeenCalled();
	});

	it('shows edit modal probe progress, single-node probe, and sorts by total latency', async () => {
		fircTunnels.set([
			{
				id: 'tun-edit',
				device: 'tunvless4',
				description: 'Sub Pool',
				enable: true,
				uplink: { kind: 'auto' },
				sources: [{ id: 'src1', kind: 'url', url: 'https://sub.example/list' }],
				active: 1,
				by: 'connection',
				interval: 30,
				silence: 20,
				filter: '',
				order: ['src1:Alpha', 'src1:Beta'],
				exclude: [],
				advanced: {},
			} as never,
		]);
		fircTunnelsState.set([
			{
				id: 'tun-edit',
				device: 'tunvless4',
				status: 'up',
				active: ['Alpha'],
				nodes: [
					{ key: 'src1:Alpha', name: 'Alpha', source: 'sub', state: 'active', since: 0 },
					{ key: 'src1:Beta', name: 'Beta', source: 'sub', state: 'reserve', since: 0 },
				],
				inbounds: [],
				groups: [],
				rxBps: 0,
				txBps: 0,
				since: 0,
				backoffS: 0,
				lastExit: 0,
				uplinkOk: true,
				tcpPort: 41004,
				tcpSkipped: [],
				tcpOff: '',
				subscriptions: [],
			},
		]);

		let releaseProbe!: (rows: Array<Record<string, unknown>>) => void;
		const deferredProbe = new Promise<Array<Record<string, unknown>>>((resolve) => {
			releaseProbe = resolve;
		});

		fircProbeTunnel.mockImplementation(
			async (
				payload: { id?: string; keys?: string[] },
				opts?: { onProgress?: (rows: Array<Record<string, unknown>>, total: number) => void },
			) => {
				if (payload.keys && payload.keys.length > 0) {
					return [
						{ key: 'src1:Alpha', ok: true, why: '', handshakeMs: 12, firstByteMs: 18 },
					];
				}
				opts?.onProgress?.(
					[{ key: 'src1:Alpha', ok: true, why: '', handshakeMs: 200, firstByteMs: 10 }],
					2,
				);
				return deferredProbe;
			},
		);

		fircEditTunnelId.set('tun-edit');
		fircEditTunnelSubTab.set('nodes');
		render(FircTunnelEditModal);

		const probeAllBtn = await screen.findByRole('button', { name: m.firc_tunnel_edit_probe_all() });
		await fireEvent.click(probeAllBtn);

		await waitFor(() => {
			expect(screen.getByRole('button', { name: `${m.firc_tunnel_edit_probe_all()} (1/2)` })).toBeInTheDocument();
			expect(screen.getByText(m.firc_tunnel_edit_ms({ ms: 210 }))).toBeInTheDocument();
		});

		releaseProbe([
			{ key: 'src1:Alpha', ok: true, why: '', handshakeMs: 200, firstByteMs: 10 },
			{ key: 'src1:Beta', ok: true, why: '', handshakeMs: 20, firstByteMs: 40 },
		]);

		expect(await screen.findByText(m.firc_tunnel_edit_ms({ ms: 60 }))).toBeInTheDocument();
		expect(screen.getByText(m.firc_probe_breakdown({ connect: '20', reply: '40' }))).toBeInTheDocument();
		expect(screen.getByText(m.firc_probe_breakdown({ connect: '200', reply: '10' }))).toBeInTheDocument();

		await fireEvent.click(screen.getByRole('button', { name: m.firc_tunnel_edit_sort_by_ping() }));

		await waitFor(() => {
			const rows = Array.from(document.querySelectorAll('.nodes-table tbody tr'));
			expect(rows.length).toBe(2);
			expect(rows[0].textContent).toContain('Beta');
			expect(rows[1].textContent).toContain('Alpha');
		});

		const singleBtn = screen.getByRole('button', { name: m.firc_probe_single_aria({ name: 'Alpha' }) });
		await fireEvent.click(singleBtn);

		await waitFor(() => {
			expect(fircProbeTunnel).toHaveBeenCalledWith(
				expect.objectContaining({
					tunnel: expect.objectContaining({
						id: 'tun-edit',
						order: ['src1:Beta', 'src1:Alpha'],
					}),
					keys: ['src1:Alpha'],
				}),
			);
			expect(screen.getByText(m.firc_tunnel_edit_ms({ ms: 30 }))).toBeInTheDocument();
			expect(screen.getByText(m.firc_probe_breakdown({ connect: '12', reply: '18' }))).toBeInTheDocument();
		});
	});

	it('explains unselected FIRC engine without falsely showing an active node from saved order', async () => {
		const offStatus = {
			fircdInstalled: true,
			tunvlessInstalled: true,
			running: false,
			selected: false,
			socketPath: '/opt/var/run/fircd.sock',
			dnsProxyEndpoint: '127.0.0.1:3553',
			inboundPort: 1099,
			tunnelsCount: 1,
			groupsCount: 0,
		};
		fircStatus.set(offStatus);
		fircGetStatus.mockResolvedValue(offStatus);
		fircGetTunnels.mockResolvedValue([
			{
				id: 'tunnel1',
				device: 'tunvless0',
				description: '',
				enable: true,
				active: 1,
				by: 'connection',
				order: ['sub1:YouTube без рекламы'],
				sources: [
					{
						id: 'sub1',
						kind: 'subscription',
						name: '999',
						url: 'https://example.com/sub',
						interval: 21600,
					},
				],
				inbounds: [],
			},
		]);
		fircGetTunnelsState.mockResolvedValue([]);

		render(FircTunnelsTabSection, { props: { dashboardMode: false, showTrustTunnels: false } });

		const diag = await screen.findByTestId('firc-card-diagnostic');
		expect(diag).toHaveTextContent(m.firc_reason_engine_not_selected());
		expect(screen.queryByText(new RegExp(`· ${m.firc_resource_active_node()}`, 'i'))).not.toBeInTheDocument();
	});

	it('surfaces subscription placeholder error on card, in edit modal banner, and in Sources tab', async () => {
		fircGetTunnels.mockResolvedValue([
			{
				id: 'tunnel1',
				device: 'tunvless0',
				description: 'Sub 999',
				enable: true,
				active: 1,
				by: 'connection',
				sources: [
					{
						id: 'sub1',
						kind: 'subscription',
						name: '999',
						url: 'https://example.com/sub',
						interval: 21600,
					},
				],
				inbounds: [],
			},
		]);
		fircGetTunnelsState.mockResolvedValue([
			{
				id: 'tunnel1',
				device: 'tunvless0',
				status: 'waiting',
				active: [],
				nodes: [],
				inbounds: [],
				groups: [],
				rxBps: 0,
				txBps: 0,
				since: 0,
				backoffS: 0,
				lastExit: 0,
				uplinkOk: true,
				tcpPort: 0,
				tcpSkipped: [],
				tcpOff: '',
				subscriptions: [
					{
						name: '999',
						nodes: 0,
						fetching: false,
						lastOk: 0,
						lastTry: 1700000000,
						error: 'the server answered: Device limit reached',
					},
				],
			},
		]);

		render(FircTunnelsTabSection, { props: { dashboardMode: false, showTrustTunnels: false } });

		const expectedMsg = m.firc_sub_err_placeholder({ said: 'Device limit reached' });
		const cardDiag = await screen.findByTestId('firc-card-diagnostic');
		expect(cardDiag).toHaveTextContent(expectedMsg);

		await fireEvent.click(screen.getByText(m.common_edit()));
		expect(await screen.findByTestId('firc-modal-diagnostic')).toHaveTextContent(expectedMsg);

		await fireEvent.click(screen.getByRole('button', { name: m.firc_tunnel_edit_tab_sources({ count: 1 }) }));
		expect(await screen.findByTestId('firc-source-error')).toHaveTextContent(expectedMsg);
	});

	it('surfaces skipped node reason in card and Nodes tab and defaults new inbound listen interface to br0', async () => {
		fircGetTunnels.mockResolvedValue([
			{
				id: 'tunnel1',
				device: 'tunvless0',
				description: 'Sub TLS',
				enable: true,
				active: 1,
				by: 'connection',
				sources: [
					{
						id: 'sub1',
						kind: 'subscription',
						name: '999',
						url: 'https://example.com/sub',
						interval: 21600,
					},
				],
				inbounds: [],
			},
		]);
		fircGetTunnelsState.mockResolvedValue([
			{
				id: 'tunnel1',
				device: 'tunvless0',
				status: 'waiting',
				active: [],
				nodes: [
					{
						key: 'sub1:Node1',
						name: 'Node1',
						source: '999',
						state: 'skipped',
						since: 0,
						skipReason: 'allowInsecure: needs --insecure',
					},
				],
				inbounds: [],
				groups: [],
				rxBps: 0,
				txBps: 0,
				since: 0,
				backoffS: 0,
				lastExit: 0,
				uplinkOk: true,
				tcpPort: 0,
				tcpSkipped: [],
				tcpOff: '',
				subscriptions: [{ name: '999', nodes: 1, fetching: false, lastOk: 100, lastTry: 100, error: '' }],
			},
		]);

		render(FircTunnelsTabSection, { props: { dashboardMode: false, showTrustTunnels: false } });

		const cardDiag = await screen.findByTestId('firc-card-diagnostic');
		expect(cardDiag).toHaveTextContent(
			m.firc_reason_all_nodes_skipped({ count: 1, reason: m.firc_skip_allow_insecure() }),
		);

		await fireEvent.click(screen.getByText(m.common_edit()));
		expect(await screen.findByText(m.firc_node_state_skipped())).toBeInTheDocument();
		expect(screen.getByText(m.firc_skip_allow_insecure())).toBeInTheDocument();

		await fireEvent.click(screen.getByRole('button', { name: m.firc_tunnel_edit_tab_inbounds({ count: 0 }) }));
		await fireEvent.click(screen.getByRole('button', { name: m.firc_tunnel_edit_add_inbound() }));
		const listenInput = await screen.findByLabelText(m.firc_tunnel_edit_inbound_listen());
		expect(listenInput).toHaveValue('br0');
	});

	it('renders server regex filter inputs, lookaround warning, hidden nodes list, and saves trimmed filters', async () => {
		const subTunnel = {
			id: 'vox',
			device: 'tunvless0',
			description: 'VOX',
			enable: true,
			active: 1,
			by: 'connection',
			interval: 60,
			silence: 20,
			filter: '(?i)(DE|NL)',
			filterExclude: '',
			order: ['s1:DE-1', 's1:RU-1'],
			exclude: [],
			sources: [{ id: 's1', kind: 'subscription', name: 'VOX', url: 'https://example.com/sub', interval: 21600 }],
			uplink: { kind: 'auto', ref: '' },
			advanced: { ca: '', insecure: false, timeout: 8, kernelTcp: true, fingerprint: '' },
			inbounds: [],
		};
		fircGetTunnels.mockResolvedValue([subTunnel]);
		fircGetTunnelsState.mockResolvedValue([
			{
				id: 'vox',
				device: 'tunvless0',
				status: 'up',
				active: ['DE-1'],
				nodes: [{ key: 's1:DE-1', name: 'DE-1', source: 'VOX', state: 'active', since: 0 }],
				filteredNodes: [{ key: 's1:RU-1', name: 'RU-1', source: 'VOX' }],
				inbounds: [],
				groups: [],
				rxBps: 0,
				txBps: 0,
				since: 0,
				backoffS: 0,
				lastExit: 0,
				uplinkOk: true,
				tcpPort: 41000,
				tcpSkipped: [],
				tcpOff: '',
				subscriptions: [{ name: 'VOX', nodes: 2, fetching: false, lastOk: 0, lastTry: 0, error: '' }],
			},
		]);
		fircPreviewTunnel.mockResolvedValue({
			total: 2,
			matched: 1,
			nodes: [{ key: 's1:DE-1', name: 'DE-1', source: 'VOX', isNew: false, missing: false, excluded: false, overCap: false }],
			filteredNodes: [
				{ key: 's1:RU-1', name: 'RU-1', source: 'VOX' },
				{ key: 's1:NL-LTE', name: 'NL-LTE', source: 'VOX' },
			],
			subscriptions: [{ name: 'VOX', cached: true }],
		});
		fircSaveTunnels.mockResolvedValue({ tunnels: [{ ...subTunnel, filter: '(?i)DE', filterExclude: '(?i)(RU|LTE)' }] });

		render(FircTunnelsTabSection, { props: { dashboardMode: false, showTrustTunnels: false } });
		expect(await screen.findByText('VOX')).toBeInTheDocument();

		await fireEvent.click(screen.getByText(m.common_edit()));
		expect(await screen.findByText(m.firc_tunnel_edit_title({ name: 'VOX', device: 'tunvless0' }))).toBeInTheDocument();
		expect(screen.getByPlaceholderText(m.firc_tunnel_edit_search_placeholder())).toBeInTheDocument();

		// Nodes tab shows collapsible «Скрыто фильтром (1)»
		const hiddenToggle = await screen.findByRole('button', { name: m.firc_filter_hidden_toggle({ count: 1 }) });
		expect(hiddenToggle).toHaveAttribute('aria-expanded', 'false');
		await fireEvent.click(hiddenToggle);
		expect(hiddenToggle).toHaveAttribute('aria-expanded', 'true');
		expect(screen.getByText('RU-1')).toBeInTheDocument();

		// Switch to Подписки и ссылки tab -> Filter section appears
		await fireEvent.click(screen.getByRole('button', { name: m.firc_tunnel_edit_tab_sources({ count: 1 }) }));
		expect(await screen.findByText(m.firc_filter_section_title())).toBeInTheDocument();
		const includeInp = screen.getByLabelText(m.firc_filter_include_label());
		const excludeInp = screen.getByLabelText(m.firc_filter_exclude_label());
		expect(includeInp).toHaveAttribute('placeholder', 'DE|NL|🇩🇪');
		expect(excludeInp).toHaveAttribute('placeholder', m.firc_filter_exclude_example());

		// Lookaround triggers lookaround hint warning and skips preview API call
		await fireEvent.input(includeInp, { target: { value: '^(?!RU).*$' } });
		expect(await screen.findByText(m.firc_filter_warn_lookaround())).toBeInTheDocument();
		await new Promise((r) => setTimeout(r, 380));
		expect(fircPreviewTunnel).not.toHaveBeenCalled();

		// Valid filter pair triggers debounced preview and updates hidden count
		await fireEvent.input(includeInp, { target: { value: '  (?i)DE  ' } });
		await fireEvent.input(excludeInp, { target: { value: '  (?i)(RU|LTE)  ' } });
		await waitFor(
			() => {
				expect(fircPreviewTunnel).toHaveBeenCalledTimes(1);
			},
			{ timeout: 1500 },
		);

		await fireEvent.click(screen.getByRole('button', { name: m.firc_tunnel_edit_tab_nodes({ count: 1 }) }));
		expect(await screen.findByRole('button', { name: m.firc_filter_hidden_toggle({ count: 2 }) })).toBeInTheDocument();
		expect(screen.getByText('NL-LTE')).toBeInTheDocument();

		await fireEvent.click(screen.getByRole('button', { name: m.common_save() }));
		await waitFor(() => {
			expect(fircSaveTunnels).toHaveBeenCalled();
		});
		const savedArg = fircSaveTunnels.mock.calls[0][0];
		expect(savedArg[0].filter).toBe('(?i)DE');
		expect(savedArg[0].filterExclude).toBe('(?i)(RU|LTE)');
	});
});
