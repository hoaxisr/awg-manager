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
	fircRefreshTunnel,
	fircNormalizeSource,
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
		fircRefreshTunnel: vi.fn(),
		fircNormalizeSource: vi.fn(),
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
		fircRefreshTunnel,
		fircNormalizeSource,
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
	fircRefreshTunnel.mockResolvedValue({ ok: true });
	fircNormalizeSource.mockResolvedValue({ sources: [] });
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
			expect(strip).toHaveTextContent('AWGM Route');
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
		expect(screen.getAllByText('AWGM Route').length).toBeGreaterThanOrEqual(2);
		expect(screen.getByText('2/2')).toBeInTheDocument();
		expect(screen.getByText(m.firc_tabs_traffic_while_viewing())).toBeInTheDocument();
		expect(screen.getByText(m.singbox_tabs_avg_delay())).toBeInTheDocument();
		expect(screen.getByText(m.tunnels_awg_stat_leader())).toBeInTheDocument();

		const testBtn = screen.getByRole('button', {
			name: m.firc_resource_test_title({ name: 'FIN (Финляндия) (Premium)' }),
		});
		await fireEvent.click(testBtn);
		expect(
			await screen.findByText(m.tunnel_test_title({ prefix: 'AWGM Route', name: 'FIN (Финляндия) (Premium)' })),
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

	it('до статуса вкладка ждёт: ни кнопок, ни пояснения', async () => {
		fircStatus.set(null);
		fircGetStatus.mockReturnValue(new Promise(() => {}));
		const { container } = render(FircTunnelsTabSection);
		expect(container.querySelector('.animate-spin')).not.toBeNull();
		expect(screen.queryAllByRole('button')).toHaveLength(0);
		expect(screen.queryByText(m.firc_foreign_text())).not.toBeInTheDocument();
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

	it('shows live nodes after adding an uncached subscription and saving without getting stuck on empty preview', async () => {
		const baseTunnel = {
			id: 'tun-new-sub',
			device: 'tunvless2',
			description: 'NewSub',
			enable: true,
			active: 1,
			by: 'connection',
			interval: 60,
			silence: 20,
			filter: '',
			filterExclude: '',
			order: [],
			exclude: [],
			sources: [],
			uplink: { kind: 'auto', ref: '' },
			advanced: { ca: '', insecure: false, timeout: 8, kernelTcp: true, fingerprint: '' },
			inbounds: [],
		};
		const emptyState = {
			id: 'tun-new-sub',
			device: 'tunvless2',
			status: 'no_node',
			active: [],
			nodes: [],
			filteredNodes: [],
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
		};
		fircTunnels.set([baseTunnel as never]);
		fircTunnelsState.set([emptyState as never]);
		fircGetTunnels.mockResolvedValue([baseTunnel]);
		fircGetTunnelsState.mockResolvedValue([emptyState]);
		fircNormalizeSource.mockResolvedValue({
			sources: [
				{
					id: 'sub-new',
					kind: 'subscription',
					name: 'Nebesniy',
					url: 'https://sub.example/list',
					interval: 21600,
				},
			],
		});
		fircPreviewTunnel.mockResolvedValue({
			total: 0,
			matched: 0,
			nodes: [],
			filteredNodes: [],
			subscriptions: [{ name: 'Nebesniy', cached: false }],
		});
		fircSaveTunnels.mockImplementation(async (list: unknown[]) => {
			fircGetTunnels.mockResolvedValue(list);
			return { tunnels: list };
		});

		fircEditTunnelId.set('tun-new-sub');
		fircEditTunnelSubTab.set('sources');
		render(FircTunnelEditModal);

		await fireEvent.click(await screen.findByRole('button', { name: m.firc_tunnel_edit_add_subscription() }));
		const urlInput = await screen.findByLabelText(m.firc_tunnel_edit_sub_url_label());
		await fireEvent.input(urlInput, { target: { value: 'https://sub.example/list' } });
		await fireEvent.click(screen.getByRole('button', { name: m.common_add() }));

		await waitFor(() => {
			expect(fircPreviewTunnel).toHaveBeenCalledTimes(1);
		});

		await fireEvent.click(screen.getByRole('button', { name: m.common_save() }));
		await waitFor(() => {
			expect(fircSaveTunnels).toHaveBeenCalledTimes(1);
			expect(notifications.success).toHaveBeenCalledWith(m.firc_tunnel_edit_saved({ device: 'tunvless2' }));
		});

		// Background fetch completes after Save and updates live state via poller.
		fircTunnelsState.set([
			{
				...emptyState,
				status: 'up',
				active: ['Germany'],
				nodes: [
					{ key: 'sub-new:Germany', name: 'Germany', source: 'Nebesniy', state: 'active', since: 0 },
					{ key: 'sub-new:Netherlands', name: 'Netherlands', source: 'Nebesniy', state: 'reserve', since: 0 },
				],
				subscriptions: [{ name: 'Nebesniy', nodes: 2, fetching: false, lastOk: 1700000100, lastTry: 1700000100, error: '' }],
			} as never,
		]);

		expect(await screen.findByText('Germany')).toBeInTheDocument();
		expect(screen.getByText('Netherlands')).toBeInTheDocument();
		expect(screen.queryByText(m.firc_tunnel_edit_nodes_empty())).not.toBeInTheDocument();
		await new Promise((r) => setTimeout(r, 360));
		expect(fircPreviewTunnel).toHaveBeenCalledTimes(1);
	});

	it('clears stale preview on subscription refresh instead of overwriting with pre-fetch preview', async () => {
		const subTunnel = {
			id: 'tun-ref',
			device: 'tunvless3',
			description: 'RefSub',
			enable: true,
			active: 1,
			by: 'connection',
			interval: 60,
			silence: 20,
			filter: '',
			filterExclude: '',
			order: [],
			exclude: [],
			sources: [{ id: 's1', kind: 'subscription', name: 'RefSub', url: 'https://sub.example/ref', interval: 21600 }],
			uplink: { kind: 'auto', ref: '' },
			advanced: { ca: '', insecure: false, timeout: 8, kernelTcp: true, fingerprint: '' },
			inbounds: [],
		};
		const initialState = {
			id: 'tun-ref',
			device: 'tunvless3',
			status: 'up',
			active: ['OldNode'],
			nodes: [{ key: 's1:OldNode', name: 'OldNode', source: 'RefSub', state: 'active', since: 0 }],
			filteredNodes: [],
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
			subscriptions: [{ name: 'RefSub', nodes: 1, fetching: false, lastOk: 100, lastTry: 100, error: '' }],
		};
		fircTunnels.set([subTunnel as never]);
		fircTunnelsState.set([initialState as never]);
		fircGetTunnels.mockResolvedValue([subTunnel]);
		fircGetTunnelsState.mockResolvedValue([initialState]);
		fircPreviewTunnel.mockResolvedValue({
			total: 1,
			matched: 1,
			nodes: [{ key: 's1:PreviewOnly', name: 'PreviewOnly', source: 'RefSub', isNew: false, missing: false, excluded: false, overCap: false }],
			filteredNodes: [],
			subscriptions: [{ name: 'RefSub', cached: true }],
		});
		fircSaveTunnels.mockImplementation(async (list: unknown[]) => {
			fircGetTunnels.mockResolvedValue(list);
			return { tunnels: list };
		});

		fircEditTunnelId.set('tun-ref');
		fircEditTunnelSubTab.set('sources');
		render(FircTunnelEditModal);

		// Create an active preview via filter edit.
		const includeInp = await screen.findByLabelText(m.firc_filter_include_label());
		await fireEvent.input(includeInp, { target: { value: 'Preview' } });
		await waitFor(
			() => {
				expect(fircPreviewTunnel).toHaveBeenCalledTimes(1);
			},
			{ timeout: 1500 },
		);

		await fireEvent.click(screen.getByRole('button', { name: m.firc_tunnel_edit_tab_nodes({ count: 1 }) }));
		expect(await screen.findByText('PreviewOnly')).toBeInTheDocument();

		// Trigger Refresh: must NOT call fircPreviewTunnel again; clears preview so fresh liveState shows up.
		await fireEvent.click(screen.getByRole('button', { name: m.firc_tunnel_edit_refresh() }));
		await waitFor(() => {
			expect(fircRefreshTunnel).toHaveBeenCalledWith('tun-ref');
			expect(notifications.success).toHaveBeenCalledWith(m.firc_tunnel_edit_refresh_done({ device: 'tunvless3' }));
		});
		expect(fircPreviewTunnel).toHaveBeenCalledTimes(1);

		fircTunnelsState.set([
			{
				...initialState,
				active: ['FreshLive'],
				nodes: [{ key: 's1:FreshLive', name: 'FreshLive', source: 'RefSub', state: 'active', since: 0 }],
				subscriptions: [{ name: 'RefSub', nodes: 1, fetching: false, lastOk: 200, lastTry: 200, error: '' }],
			} as never,
		]);

		expect(await screen.findByText('FreshLive')).toBeInTheDocument();
		expect(screen.queryByText('PreviewOnly')).not.toBeInTheDocument();
	});

	it('clears stale preview when the last subscription source is removed', async () => {
		const subTunnel = {
			id: 'tun-rm',
			device: 'tunvless4',
			description: 'RmSub',
			enable: true,
			active: 1,
			by: 'connection',
			interval: 60,
			silence: 20,
			filter: '',
			filterExclude: '',
			order: [],
			exclude: [],
			sources: [{ id: 's1', kind: 'subscription', name: 'RmSub', url: 'https://sub.example/rm', interval: 21600 }],
			uplink: { kind: 'auto', ref: '' },
			advanced: { ca: '', insecure: false, timeout: 8, kernelTcp: true, fingerprint: '' },
			inbounds: [],
		};
		fircTunnels.set([subTunnel as never]);
		fircTunnelsState.set([]);
		fircPreviewTunnel.mockResolvedValue({
			total: 1,
			matched: 1,
			nodes: [{ key: 's1:GhostNode', name: 'GhostNode', source: 'RmSub', isNew: false, missing: false, excluded: false, overCap: false }],
			filteredNodes: [],
			subscriptions: [{ name: 'RmSub', cached: true }],
		});

		fircEditTunnelId.set('tun-rm');
		fircEditTunnelSubTab.set('sources');
		render(FircTunnelEditModal);

		const includeInp = await screen.findByLabelText(m.firc_filter_include_label());
		await fireEvent.input(includeInp, { target: { value: 'Ghost' } });
		await waitFor(
			() => {
				expect(fircPreviewTunnel).toHaveBeenCalledTimes(1);
			},
			{ timeout: 1500 },
		);
		expect(
			await screen.findByRole('button', { name: m.firc_tunnel_edit_tab_nodes({ count: 1 }) }),
		).toBeInTheDocument();

		await fireEvent.click(screen.getByRole('button', { name: m.firc_tunnel_edit_remove_source() }));
		await fireEvent.click(screen.getByRole('button', { name: m.firc_tunnel_edit_tab_nodes({ count: 0 }) }));
		expect(await screen.findByText(m.firc_tunnel_edit_nodes_empty())).toBeInTheDocument();
		expect(screen.queryByText('GhostNode')).not.toBeInTheDocument();
	});

	it('cancels an armed debounced preview timer when refresh is triggered with clean draft', async () => {
		const subTunnel = {
			id: 'tun-race',
			device: 'tunvless5',
			description: 'RaceSub',
			enable: true,
			active: 1,
			by: 'connection',
			interval: 60,
			silence: 20,
			filter: '',
			filterExclude: '',
			order: [],
			exclude: [],
			sources: [{ id: 's1', kind: 'subscription', name: 'RaceSub', url: 'https://sub.example/race', interval: 21600 }],
			uplink: { kind: 'auto', ref: '' },
			advanced: { ca: '', insecure: false, timeout: 8, kernelTcp: true, fingerprint: '' },
			inbounds: [],
		};
		const liveState = {
			id: 'tun-race',
			device: 'tunvless5',
			status: 'up',
			active: ['LiveNode'],
			nodes: [{ key: 's1:LiveNode', name: 'LiveNode', source: 'RaceSub', state: 'active', since: 0 }],
			filteredNodes: [],
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
			subscriptions: [{ name: 'RaceSub', nodes: 1, fetching: false, lastOk: 100, lastTry: 100, error: '' }],
		};
		fircTunnels.set([subTunnel as never]);
		fircTunnelsState.set([liveState as never]);
		fircGetTunnels.mockResolvedValue([subTunnel]);
		fircGetTunnelsState.mockResolvedValue([liveState]);
		fircPreviewTunnel.mockResolvedValue({
			total: 1,
			matched: 1,
			nodes: [{ key: 's1:StalePreview', name: 'StalePreview', source: 'RaceSub', isNew: false, missing: false, excluded: false, overCap: false }],
			filteredNodes: [],
			subscriptions: [{ name: 'RaceSub', cached: true }],
		});
		fircRefreshTunnel.mockImplementation(
			() => new Promise((resolve) => setTimeout(() => resolve({ ok: true }), 380)),
		);

		fircEditTunnelId.set('tun-race');
		fircEditTunnelSubTab.set('sources');
		render(FircTunnelEditModal);

		const includeInp = await screen.findByLabelText(m.firc_filter_include_label());
		await fireEvent.input(includeInp, { target: { value: 'Stale' } });
		await waitFor(() => expect(fircPreviewTunnel).toHaveBeenCalledTimes(1), { timeout: 1500 });
		expect(
			await screen.findByRole('button', { name: m.firc_tunnel_edit_tab_nodes({ count: 1 }) }),
		).toBeInTheDocument();

		// Revert filter back to saved '' so isDirty === false while preview is active -> arms 300ms timer.
		await fireEvent.input(includeInp, { target: { value: '' } });
		// Immediately trigger Refresh while timer is armed and refresh takes > 300ms.
		await fireEvent.click(screen.getByRole('button', { name: m.firc_tunnel_edit_refresh() }));
		await waitFor(
			() => {
				expect(notifications.success).toHaveBeenCalledWith(m.firc_tunnel_edit_refresh_done({ device: 'tunvless5' }));
			},
			{ timeout: 1500 },
		);

		expect(fircPreviewTunnel).toHaveBeenCalledTimes(1);
		await fireEvent.click(screen.getByRole('button', { name: m.firc_tunnel_edit_tab_nodes({ count: 1 }) }));
		expect(await screen.findByText('LiveNode')).toBeInTheDocument();
		expect(screen.queryByText('StalePreview')).not.toBeInTheDocument();
	});

	it('keeps remaining unsaved link source visible after removing another link source', async () => {
		const baseTunnel = {
			id: 'tun-links',
			device: 'tunvless6',
			description: 'Links',
			enable: true,
			active: 1,
			by: 'connection',
			interval: 60,
			silence: 20,
			filter: '',
			filterExclude: '',
			order: [],
			exclude: [],
			sources: [],
			uplink: { kind: 'auto', ref: '' },
			advanced: { ca: '', insecure: false, timeout: 8, kernelTcp: true, fingerprint: '' },
			inbounds: [],
		};
		fircTunnels.set([baseTunnel as never]);
		fircTunnelsState.set([]);
		fircNormalizeSource
			.mockResolvedValueOnce({
				sources: [{ id: 'lnk-a', kind: 'link', name: 'LinkA', link: 'vless://u@1.1.1.1:443#NodeA' }],
			})
			.mockResolvedValueOnce({
				sources: [{ id: 'lnk-b', kind: 'link', name: 'LinkB', link: 'vless://u@2.2.2.2:443#NodeB' }],
			});
		fircPreviewTunnel.mockImplementation(async (tun: { sources: Array<{ id: string; name?: string }> }) => ({
			total: tun.sources.length,
			matched: tun.sources.length,
			nodes: tun.sources.map((s) => ({
				key: `${s.id}:${s.id === 'lnk-a' ? 'NodeA' : 'NodeB'}`,
				name: s.id === 'lnk-a' ? 'NodeA' : 'NodeB',
				source: s.name ?? s.id,
				isNew: true,
				missing: false,
				excluded: false,
				overCap: false,
			})),
			filteredNodes: [],
			subscriptions: [],
		}));

		fircEditTunnelId.set('tun-links');
		fircEditTunnelSubTab.set('sources');
		render(FircTunnelEditModal);

		// Add LinkA
		await fireEvent.click(await screen.findByRole('button', { name: m.firc_tunnel_edit_add_link() }));
		await fireEvent.input(await screen.findByLabelText(m.firc_tunnel_edit_links_label()), {
			target: { value: 'vless://u@1.1.1.1:443#NodeA' },
		});
		await fireEvent.click(screen.getByRole('button', { name: m.common_add() }));
		expect(await screen.findByText('NodeA')).toBeInTheDocument();

		// Switch back to Sources and add LinkB
		await fireEvent.click(screen.getByRole('button', { name: m.firc_tunnel_edit_tab_sources({ count: 1 }) }));
		await fireEvent.click(await screen.findByRole('button', { name: m.firc_tunnel_edit_add_link() }));
		await fireEvent.input(await screen.findByLabelText(m.firc_tunnel_edit_links_label()), {
			target: { value: 'vless://u@2.2.2.2:443#NodeB' },
		});
		await fireEvent.click(screen.getByRole('button', { name: m.common_add() }));
		expect(await screen.findByText('NodeB')).toBeInTheDocument();

		// Switch to Sources and remove LinkA -> NodeB must remain visible in Nodes tab.
		await fireEvent.click(screen.getByRole('button', { name: m.firc_tunnel_edit_tab_sources({ count: 2 }) }));
		const rmBtns = await screen.findAllByRole('button', { name: m.firc_tunnel_edit_remove_source() });
		await fireEvent.click(rmBtns[0]);

		await fireEvent.click(
			await screen.findByRole('button', { name: m.firc_tunnel_edit_tab_nodes({ count: 1 }) }),
		);
		expect(await screen.findByText('NodeB')).toBeInTheDocument();
		expect(screen.queryByText('NodeA')).not.toBeInTheDocument();
	});

	it('updates active preview when removing a link while a subscription source remains with unchanged filters', async () => {
		const subTunnel = {
			id: 'tun-sub-link',
			device: 'tunvless7',
			description: 'SubPlusLink',
			enable: true,
			active: 1,
			by: 'connection',
			interval: 60,
			silence: 20,
			filter: '',
			filterExclude: '',
			order: [],
			exclude: [],
			sources: [{ id: 'sub-a', kind: 'subscription', name: 'SubA', url: 'https://sub.example/a', interval: 21600 }],
			uplink: { kind: 'auto', ref: '' },
			advanced: { ca: '', insecure: false, timeout: 8, kernelTcp: true, fingerprint: '' },
			inbounds: [],
		};
		const liveState = {
			id: 'tun-sub-link',
			device: 'tunvless7',
			status: 'up',
			active: ['SubANode'],
			nodes: [{ key: 'sub-a:SubANode', name: 'SubANode', source: 'SubA', state: 'active', since: 0 }],
			filteredNodes: [],
			inbounds: [],
			groups: [],
			rxBps: 0,
			txBps: 0,
			since: 0,
			backoffS: 0,
			lastExit: 0,
			uplinkOk: true,
			tcpPort: 41007,
			tcpSkipped: [],
			tcpOff: '',
			subscriptions: [{ name: 'SubA', nodes: 1, fetching: false, lastOk: 100, lastTry: 100, error: '' }],
		};
		fircTunnels.set([subTunnel as never]);
		fircTunnelsState.set([liveState as never]);
		fircNormalizeSource.mockResolvedValue({
			sources: [{ id: 'lnk-b', kind: 'link', name: 'LinkB', link: 'vless://u@2.2.2.2:443#LinkBNode' }],
		});
		fircPreviewTunnel.mockImplementation(async (tun: { sources: Array<{ id: string }> }) => {
			const nodes = [];
			if (tun.sources.some((s) => s.id === 'sub-a')) {
				nodes.push({
					key: 'sub-a:SubANode',
					name: 'SubANode',
					source: 'SubA',
					isNew: false,
					missing: false,
					excluded: false,
					overCap: false,
				});
			}
			if (tun.sources.some((s) => s.id === 'lnk-b')) {
				nodes.push({
					key: 'lnk-b:LinkBNode',
					name: 'LinkBNode',
					source: 'LinkB',
					isNew: true,
					missing: false,
					excluded: false,
					overCap: false,
				});
			}
			return {
				total: nodes.length,
				matched: nodes.length,
				nodes,
				filteredNodes: [],
				subscriptions: [{ name: 'SubA', cached: true }],
			};
		});

		fircEditTunnelId.set('tun-sub-link');
		fircEditTunnelSubTab.set('sources');
		render(FircTunnelEditModal);

		// Add LinkB while SubA exists and filters remain unchanged -> activates preview with 2 nodes.
		await fireEvent.click(await screen.findByRole('button', { name: m.firc_tunnel_edit_add_link() }));
		await fireEvent.input(await screen.findByLabelText(m.firc_tunnel_edit_links_label()), {
			target: { value: 'vless://u@2.2.2.2:443#LinkBNode' },
		});
		await fireEvent.click(screen.getByRole('button', { name: m.common_add() }));
		expect(await screen.findByText('LinkBNode')).toBeInTheDocument();
		expect(screen.getByText('SubANode')).toBeInTheDocument();

		// Remove LinkB while SubA remains -> preview must refresh and drop LinkBNode.
		await fireEvent.click(screen.getByRole('button', { name: m.firc_tunnel_edit_tab_sources({ count: 2 }) }));
		const rmBtns = await screen.findAllByRole('button', { name: m.firc_tunnel_edit_remove_source() });
		await fireEvent.click(rmBtns[1]);

		await fireEvent.click(
			await screen.findByRole('button', { name: m.firc_tunnel_edit_tab_nodes({ count: 1 }) }),
		);
		expect(await screen.findByText('SubANode')).toBeInTheDocument();
		expect(screen.queryByText('LinkBNode')).not.toBeInTheDocument();
	});

	it('does not duplicate preview or drop draft.order init when addSources preview takes longer than debounce', async () => {
		const baseTunnel = {
			id: 'tun-slow-prev',
			device: 'tunvless8',
			description: 'SlowPrev',
			enable: true,
			active: 1,
			by: 'connection',
			interval: 60,
			silence: 20,
			filter: '',
			filterExclude: '',
			order: [],
			exclude: [],
			sources: [],
			uplink: { kind: 'auto', ref: '' },
			advanced: { ca: '', insecure: false, timeout: 8, kernelTcp: true, fingerprint: '' },
			inbounds: [],
		};
		fircTunnels.set([baseTunnel as never]);
		fircTunnelsState.set([]);
		fircGetTunnels.mockResolvedValue([baseTunnel]);
		fircGetTunnelsState.mockResolvedValue([]);
		fircNormalizeSource.mockResolvedValue({
			sources: [
				{
					id: 'sub-slow',
					kind: 'subscription',
					name: 'SlowSub',
					url: 'https://sub.example/slow',
					interval: 21600,
				},
			],
		});
		fircPreviewTunnel.mockImplementation(
			() =>
				new Promise((resolve) =>
					setTimeout(
						() =>
							resolve({
								total: 1,
								matched: 1,
								nodes: [
									{
										key: 'sub-slow:SlowNode',
										name: 'SlowNode',
										source: 'SlowSub',
										isNew: true,
										missing: false,
										excluded: false,
										overCap: false,
									},
								],
								filteredNodes: [],
								subscriptions: [{ name: 'SlowSub', cached: true }],
							}),
						380,
					),
				),
		);
		fircSaveTunnels.mockImplementation(async (list: unknown[]) => {
			fircGetTunnels.mockResolvedValue(list);
			return { tunnels: list };
		});

		fircEditTunnelId.set('tun-slow-prev');
		fircEditTunnelSubTab.set('sources');
		render(FircTunnelEditModal);

		// Make filter dirty BEFORE adding a subscription so Effect 2 is eligible once hasSubscriptionSource flips true.
		const includeInp = await screen.findByLabelText(m.firc_filter_include_label());
		await fireEvent.input(includeInp, { target: { value: 'Slow' } });

		await fireEvent.click(screen.getByRole('button', { name: m.firc_tunnel_edit_add_subscription() }));
		await fireEvent.input(await screen.findByLabelText(m.firc_tunnel_edit_sub_url_label()), {
			target: { value: 'https://sub.example/slow' },
		});
		await fireEvent.click(screen.getByRole('button', { name: m.common_add() }));

		expect(await screen.findByText('SlowNode', {}, { timeout: 1500 })).toBeInTheDocument();
		await new Promise((r) => setTimeout(r, 360));
		expect(fircPreviewTunnel).toHaveBeenCalledTimes(1);

		await fireEvent.click(screen.getByRole('button', { name: m.common_save() }));
		await waitFor(() => {
			expect(fircSaveTunnels).toHaveBeenCalledTimes(1);
		});
		const savedArg = fircSaveTunnels.mock.calls[0][0];
		expect(savedArg[0].order).toEqual(['sub-slow:SlowNode']);
	});

	it('aborts in-flight addSources preview when that source is removed before preview resolves', async () => {
		const baseTunnel = {
			id: 'tun-rm-inflight',
			device: 'tunvless9',
			description: 'RmInFlight',
			enable: true,
			active: 1,
			by: 'connection',
			interval: 60,
			silence: 20,
			filter: '',
			filterExclude: '',
			order: [],
			exclude: [],
			sources: [{ id: 'lnk-keep', kind: 'link', name: 'Keep', link: 'vless://u@1.1.1.1:443#KeepNode' }],
			uplink: { kind: 'auto', ref: '' },
			advanced: { ca: '', insecure: false, timeout: 8, kernelTcp: true, fingerprint: '' },
			inbounds: [],
		};
		const liveState = {
			id: 'tun-rm-inflight',
			device: 'tunvless9',
			status: 'up',
			active: ['KeepNode'],
			nodes: [{ key: 'lnk-keep:KeepNode', name: 'KeepNode', source: 'Keep', state: 'active', since: 0 }],
			filteredNodes: [],
			inbounds: [],
			groups: [],
			rxBps: 0,
			txBps: 0,
			since: 0,
			backoffS: 0,
			lastExit: 0,
			uplinkOk: true,
			tcpPort: 41009,
			tcpSkipped: [],
			tcpOff: '',
			subscriptions: [],
		};
		fircTunnels.set([baseTunnel as never]);
		fircTunnelsState.set([liveState as never]);
		fircGetTunnels.mockResolvedValue([baseTunnel]);
		fircGetTunnelsState.mockResolvedValue([liveState]);
		fircNormalizeSource.mockResolvedValue({
			sources: [{ id: 'lnk-del', kind: 'link', name: 'Deleted', link: 'vless://u@2.2.2.2:443#DeletedNode' }],
		});

		let resolveAddPreview!: (val: unknown) => void;
		let addPreviewSignal: AbortSignal | undefined;
		fircPreviewTunnel
			.mockImplementationOnce(
				(_payload: unknown, signal?: AbortSignal) =>
					new Promise((resolve) => {
						addPreviewSignal = signal;
						resolveAddPreview = resolve;
					}),
			)
			.mockResolvedValueOnce({
				total: 1,
				matched: 1,
				nodes: [
					{
						key: 'lnk-keep:KeepNode',
						name: 'KeepNode',
						source: 'Keep',
						isNew: false,
						missing: false,
						excluded: false,
						overCap: false,
					},
				],
				filteredNodes: [],
				subscriptions: [],
			});
		fircSaveTunnels.mockImplementation(async (list: unknown[]) => {
			fircGetTunnels.mockResolvedValue(list);
			return { tunnels: list };
		});

		fircEditTunnelId.set('tun-rm-inflight');
		fircEditTunnelSubTab.set('sources');
		render(FircTunnelEditModal);

		// Start adding lnk-del; its preview hangs while preview === null.
		await fireEvent.click(await screen.findByRole('button', { name: m.firc_tunnel_edit_add_link() }));
		await fireEvent.input(await screen.findByLabelText(m.firc_tunnel_edit_links_label()), {
			target: { value: 'vless://u@2.2.2.2:443#DeletedNode' },
		});
		await fireEvent.click(screen.getByRole('button', { name: m.common_add() }));
		await waitFor(() => expect(fircPreviewTunnel).toHaveBeenCalledTimes(1));

		// Close the source modal and delete lnk-del before the first preview resolves.
		await fireEvent.click(screen.getByRole('button', { name: m.common_cancel() }));
		const rmBtns = await screen.findAllByRole('button', { name: m.firc_tunnel_edit_remove_source() });
		expect(rmBtns).toHaveLength(2);
		await fireEvent.click(rmBtns[1]);

		await waitFor(() => expect(fircPreviewTunnel).toHaveBeenCalledTimes(2));
		expect(addPreviewSignal?.aborted).toBe(true);

		// Even if the stale first preview resolves anyway, DeletedNode must not resurrect in preview or order.
		resolveAddPreview({
			total: 2,
			matched: 2,
			nodes: [
				{
					key: 'lnk-keep:KeepNode',
					name: 'KeepNode',
					source: 'Keep',
					isNew: false,
					missing: false,
					excluded: false,
					overCap: false,
				},
				{
					key: 'lnk-del:DeletedNode',
					name: 'DeletedNode',
					source: 'Deleted',
					isNew: true,
					missing: false,
					excluded: false,
					overCap: false,
				},
			],
			filteredNodes: [],
			subscriptions: [],
		});
		await new Promise((r) => setTimeout(r, 30));

		await fireEvent.click(
			await screen.findByRole('button', { name: m.firc_tunnel_edit_tab_nodes({ count: 1 }) }),
		);
		expect(await screen.findByText('KeepNode')).toBeInTheDocument();
		expect(screen.queryByText('DeletedNode')).not.toBeInTheDocument();
	});

	it('does not let an aborted first addSources close a reopened second source modal', async () => {
		const baseTunnel = {
			id: 'tun-modal-session',
			device: 'tunvless10',
			description: 'ModalSess',
			enable: true,
			active: 1,
			by: 'connection',
			interval: 60,
			silence: 20,
			filter: '',
			filterExclude: '',
			order: [],
			exclude: [],
			sources: [],
			uplink: { kind: 'auto', ref: '' },
			advanced: { ca: '', insecure: false, timeout: 8, kernelTcp: true, fingerprint: '' },
			inbounds: [],
		};
		fircTunnels.set([baseTunnel as never]);
		fircTunnelsState.set([]);
		fircNormalizeSource
			.mockResolvedValueOnce({
				sources: [{ id: 'lnk-1', kind: 'link', name: 'Link1', link: 'vless://u@1.1.1.1:443#Node1' }],
			})
			.mockResolvedValueOnce({
				sources: [{ id: 'lnk-2', kind: 'link', name: 'Link2', link: 'vless://u@2.2.2.2:443#Node2' }],
			});

		let resolveFirst!: (v: unknown) => void;
		let resolveSecond!: (v: unknown) => void;
		fircPreviewTunnel
			.mockImplementationOnce(
				() =>
					new Promise((resolve) => {
						resolveFirst = resolve;
					}),
			)
			.mockImplementationOnce(
				() =>
					new Promise((resolve) => {
						resolveSecond = resolve;
					}),
			);

		fircEditTunnelId.set('tun-modal-session');
		fircEditTunnelSubTab.set('sources');
		render(FircTunnelEditModal);

		// Open modal #1, submit Link1 (preview hangs), then close modal #1 via Cancel.
		await fireEvent.click(await screen.findByRole('button', { name: m.firc_tunnel_edit_add_link() }));
		await fireEvent.input(await screen.findByLabelText(m.firc_tunnel_edit_links_label()), {
			target: { value: 'vless://u@1.1.1.1:443#Node1' },
		});
		await fireEvent.click(screen.getByRole('button', { name: m.common_add() }));
		await waitFor(() => expect(fircPreviewTunnel).toHaveBeenCalledTimes(1));
		await fireEvent.click(screen.getByRole('button', { name: m.common_cancel() }));

		// Open modal #2 and submit Link2 (aborts preview #1).
		await fireEvent.click(await screen.findByRole('button', { name: m.firc_tunnel_edit_add_link() }));
		await fireEvent.input(await screen.findByLabelText(m.firc_tunnel_edit_links_label()), {
			target: { value: 'vless://u@2.2.2.2:443#Node2' },
		});
		await fireEvent.click(screen.getByRole('button', { name: m.common_add() }));
		await waitFor(() => expect(fircPreviewTunnel).toHaveBeenCalledTimes(2));

		// Resolve aborted preview #1: modal #2 MUST remain open while preview #2 is still pending.
		resolveFirst({ total: 1, matched: 1, nodes: [], filteredNodes: [], subscriptions: [] });
		await new Promise((r) => setTimeout(r, 20));
		expect(screen.getByLabelText(m.firc_tunnel_edit_links_label())).toBeInTheDocument();

		// Now resolve preview #2: modal #2 closes and Node2 appears.
		resolveSecond({
			total: 2,
			matched: 2,
			nodes: [
				{ key: 'lnk-1:Node1', name: 'Node1', source: 'Link1', isNew: true, missing: false, excluded: false, overCap: false },
				{ key: 'lnk-2:Node2', name: 'Node2', source: 'Link2', isNew: true, missing: false, excluded: false, overCap: false },
			],
			filteredNodes: [],
			subscriptions: [],
		});
		expect(await screen.findByText('Node2')).toBeInTheDocument();
		expect(screen.queryByLabelText(m.firc_tunnel_edit_links_label())).not.toBeInTheDocument();
	});

	it('does not abort in-flight addSources preview on background liveSubStamp poll and refreshes cache afterwards', async () => {
		const subTunnel = {
			id: 'tun-poll-race',
			device: 'tunvless11',
			description: 'PollRace',
			enable: true,
			active: 1,
			by: 'connection',
			interval: 60,
			silence: 20,
			filter: '',
			filterExclude: '',
			order: [],
			exclude: [],
			sources: [{ id: 'sub-1', kind: 'subscription', name: 'Sub1', url: 'https://sub.example/1', interval: 21600 }],
			uplink: { kind: 'auto', ref: '' },
			advanced: { ca: '', insecure: false, timeout: 8, kernelTcp: true, fingerprint: '' },
			inbounds: [],
		};
		const initialState = {
			id: 'tun-poll-race',
			device: 'tunvless11',
			status: 'up',
			active: [],
			nodes: [],
			filteredNodes: [],
			inbounds: [],
			groups: [],
			rxBps: 0,
			txBps: 0,
			since: 0,
			backoffS: 0,
			lastExit: 0,
			uplinkOk: true,
			tcpPort: 41011,
			tcpSkipped: [],
			tcpOff: '',
			subscriptions: [{ name: 'Sub1', nodes: 0, fetching: true, lastOk: 0, lastTry: 100, error: '' }],
		};
		fircTunnels.set([subTunnel as never]);
		fircTunnelsState.set([initialState as never]);
		fircGetTunnels.mockResolvedValue([subTunnel]);
		fircGetTunnelsState.mockResolvedValue([initialState]);
		fircNormalizeSource.mockResolvedValue({
			sources: [{ id: 'lnk-extra', kind: 'link', name: 'Extra', link: 'vless://u@3.3.3.3:443#ExtraNode' }],
		});

		let addSignal: AbortSignal | undefined;
		fircPreviewTunnel
			.mockImplementationOnce(
				(_payload: unknown, signal?: AbortSignal) =>
					new Promise((resolve) => {
						addSignal = signal;
						setTimeout(
							() =>
								resolve({
									total: 1,
									matched: 1,
									nodes: [
										{
											key: 'lnk-extra:ExtraNode',
											name: 'ExtraNode',
											source: 'Extra',
											isNew: true,
											missing: false,
											excluded: false,
											overCap: false,
										},
									],
									filteredNodes: [],
									subscriptions: [{ name: 'Sub1', cached: false }],
								}),
							380,
						);
					}),
			)
			.mockResolvedValueOnce({
				total: 2,
				matched: 2,
				nodes: [
					{
						key: 'lnk-extra:ExtraNode',
						name: 'ExtraNode',
						source: 'Extra',
						isNew: true,
						missing: false,
						excluded: false,
						overCap: false,
					},
					{
						key: 'sub-1:Sub1Node',
						name: 'Sub1Node',
						source: 'Sub1',
						isNew: false,
						missing: false,
						excluded: false,
						overCap: false,
					},
				],
				filteredNodes: [],
				subscriptions: [{ name: 'Sub1', cached: true }],
			});
		fircSaveTunnels.mockImplementation(async (list: unknown[]) => {
			fircGetTunnels.mockResolvedValue(list);
			return { tunnels: list };
		});

		fircEditTunnelId.set('tun-poll-race');
		fircEditTunnelSubTab.set('sources');
		render(FircTunnelEditModal);

		// Make filter dirty so Effect 2 is eligible to run on liveSubStamp changes.
		const includeInp = await screen.findByLabelText(m.firc_filter_include_label());
		await fireEvent.input(includeInp, { target: { value: 'Node' } });

		await fireEvent.click(screen.getByRole('button', { name: m.firc_tunnel_edit_add_link() }));
		await fireEvent.input(await screen.findByLabelText(m.firc_tunnel_edit_links_label()), {
			target: { value: 'vless://u@3.3.3.3:443#ExtraNode' },
		});
		await fireEvent.click(screen.getByRole('button', { name: m.common_add() }));
		await waitFor(() => expect(fircPreviewTunnel).toHaveBeenCalledTimes(1));

		// Simulate a background poll updating subscription status while addSources preview is in flight.
		fircTunnelsState.set([
			{
				...initialState,
				subscriptions: [{ name: 'Sub1', nodes: 1, fetching: false, lastOk: 200, lastTry: 200, error: '' }],
			} as never,
		]);

		// addSources must complete cleanly (not aborted), switch to Nodes, notify, and follow up with cached Sub1Node.
		expect(await screen.findByText('Sub1Node', {}, { timeout: 1500 })).toBeInTheDocument();
		expect(screen.getByText('ExtraNode')).toBeInTheDocument();
		expect(addSignal?.aborted).toBe(false);
		expect(notifications.success).toHaveBeenCalledWith(m.firc_tunnel_edit_source_added());

		await fireEvent.click(screen.getByRole('button', { name: m.common_save() }));
		await waitFor(() => expect(fircSaveTunnels).toHaveBeenCalledTimes(1));
		expect(fircSaveTunnels.mock.calls[0][0][0].order).toEqual(['lnk-extra:ExtraNode']);
	});

	it('aborts in-flight direct preview and skips post-unmount notifications when modal unmounts', async () => {
		const baseTunnel = {
			id: 'tun-unmount',
			device: 'tunvless12',
			description: 'Unmount',
			enable: true,
			active: 1,
			by: 'connection',
			interval: 60,
			silence: 20,
			filter: '',
			filterExclude: '',
			order: [],
			exclude: [],
			sources: [],
			uplink: { kind: 'auto', ref: '' },
			advanced: { ca: '', insecure: false, timeout: 8, kernelTcp: true, fingerprint: '' },
			inbounds: [],
		};
		fircTunnels.set([baseTunnel as never]);
		fircTunnelsState.set([]);
		fircNormalizeSource.mockResolvedValue({
			sources: [{ id: 'lnk-u', kind: 'link', name: 'U', link: 'vless://u@4.4.4.4:443#UNode' }],
		});

		let previewSignal: AbortSignal | undefined;
		let resolvePreview!: (v: unknown) => void;
		fircPreviewTunnel.mockImplementationOnce(
			(_payload: unknown, signal?: AbortSignal) =>
				new Promise((resolve) => {
					previewSignal = signal;
					resolvePreview = resolve;
				}),
		);

		fircEditTunnelId.set('tun-unmount');
		fircEditTunnelSubTab.set('sources');
		const { unmount } = render(FircTunnelEditModal);

		await fireEvent.click(await screen.findByRole('button', { name: m.firc_tunnel_edit_add_link() }));
		await fireEvent.input(await screen.findByLabelText(m.firc_tunnel_edit_links_label()), {
			target: { value: 'vless://u@4.4.4.4:443#UNode' },
		});
		await fireEvent.click(screen.getByRole('button', { name: m.common_add() }));
		await waitFor(() => expect(fircPreviewTunnel).toHaveBeenCalledTimes(1));

		unmount();
		expect(previewSignal?.aborted).toBe(true);

		resolvePreview({ total: 1, matched: 1, nodes: [], filteredNodes: [], subscriptions: [] });
		await new Promise((r) => setTimeout(r, 20));
		expect(notifications.success).not.toHaveBeenCalled();
	});

	it('ignores a cancelled source modal submission when fircNormalizeSource resolves after cancel and reopen', async () => {
		const baseTunnel = {
			id: 'tun-norm-cancel',
			device: 'tunvless13',
			description: 'NormCancel',
			enable: true,
			active: 1,
			by: 'connection',
			interval: 60,
			silence: 20,
			filter: '',
			filterExclude: '',
			order: [],
			exclude: [],
			sources: [],
			uplink: { kind: 'auto', ref: '' },
			advanced: { ca: '', insecure: false, timeout: 8, kernelTcp: true, fingerprint: '' },
			inbounds: [],
		};
		fircTunnels.set([baseTunnel as never]);
		fircTunnelsState.set([]);

		let resolveFirstNorm!: (v: unknown) => void;
		fircNormalizeSource.mockImplementationOnce(
			() =>
				new Promise((resolve) => {
					resolveFirstNorm = resolve;
				}),
		);

		fircEditTunnelId.set('tun-norm-cancel');
		fircEditTunnelSubTab.set('sources');
		render(FircTunnelEditModal);

		// Open modal #1, submit while fircNormalizeSource hangs, then click Cancel.
		await fireEvent.click(await screen.findByRole('button', { name: m.firc_tunnel_edit_add_link() }));
		await fireEvent.input(await screen.findByLabelText(m.firc_tunnel_edit_links_label()), {
			target: { value: 'vless://u@5.5.5.5:443#CancelledNode' },
		});
		await fireEvent.click(screen.getByRole('button', { name: m.common_add() }));
		await waitFor(() => expect(fircNormalizeSource).toHaveBeenCalledTimes(1));
		await fireEvent.click(screen.getByRole('button', { name: m.common_cancel() }));

		// Reopen modal #2 before first normalize settles.
		await fireEvent.click(await screen.findByRole('button', { name: m.firc_tunnel_edit_add_link() }));
		expect(await screen.findByLabelText(m.firc_tunnel_edit_links_label())).toBeInTheDocument();

		// Now resolve cancelled modal #1's normalize: must NOT add source, preview, close modal #2, or toast.
		resolveFirstNorm({
			sources: [{ id: 'lnk-cancelled', kind: 'link', name: 'Cancelled', link: 'vless://u@5.5.5.5:443#CancelledNode' }],
		});
		await new Promise((r) => setTimeout(r, 30));

		expect(fircPreviewTunnel).not.toHaveBeenCalled();
		expect(notifications.success).not.toHaveBeenCalled();
		expect(screen.getByLabelText(m.firc_tunnel_edit_links_label())).toBeInTheDocument();
		expect(screen.getByRole('button', { name: m.firc_tunnel_edit_tab_sources({ count: 0 }) })).toBeInTheDocument();
	});

	it('discards stale direct preview and refreshes when draft is edited inline while preview is in flight', async () => {
		const baseTunnel = {
			id: 'tun-inline-edit',
			device: 'tunvless14',
			description: 'InlineEdit',
			enable: true,
			active: 1,
			by: 'connection',
			interval: 60,
			silence: 20,
			filter: '',
			filterExclude: '',
			order: [],
			exclude: [],
			sources: [],
			uplink: { kind: 'auto', ref: '' },
			advanced: { ca: '', insecure: false, timeout: 8, kernelTcp: true, fingerprint: '' },
			inbounds: [],
		};
		fircTunnels.set([baseTunnel as never]);
		fircTunnelsState.set([]);
		fircNormalizeSource.mockResolvedValueOnce({
			sources: [{ id: 'lnk-ed', kind: 'link', name: 'Orig', link: 'vless://u@1.1.1.1:443#OldNode' }],
		});

		let resolveFirstPreview!: (v: unknown) => void;
		fircPreviewTunnel
			.mockImplementationOnce(
				() =>
					new Promise((resolve) => {
						resolveFirstPreview = resolve;
					}),
			)
			.mockResolvedValueOnce({
				total: 1,
				matched: 1,
				nodes: [
					{
						key: 'lnk-ed:EditedNode',
						name: 'EditedNode',
						source: 'Orig',
						isNew: true,
						missing: false,
						excluded: false,
						overCap: false,
					},
				],
				filteredNodes: [],
				subscriptions: [],
			});

		fircEditTunnelId.set('tun-inline-edit');
		fircEditTunnelSubTab.set('sources');
		render(FircTunnelEditModal);

		await fireEvent.click(await screen.findByRole('button', { name: m.firc_tunnel_edit_add_link() }));
		await fireEvent.input(await screen.findByLabelText(m.firc_tunnel_edit_links_label()), {
			target: { value: 'vless://u@1.1.1.1:443#OldNode' },
		});
		await fireEvent.click(screen.getByRole('button', { name: m.common_add() }));
		await waitFor(() => expect(fircPreviewTunnel).toHaveBeenCalledTimes(1));

		// Close modal and edit the link inline before the first preview resolves.
		await fireEvent.click(screen.getByRole('button', { name: m.common_cancel() }));
		const linkInp = await screen.findByDisplayValue('vless://u@1.1.1.1:443#OldNode');
		await fireEvent.input(linkInp, { target: { value: 'vless://u@9.9.9.9:443#EditedNode' } });

		// First (stale) preview resolves -> must be discarded and replaced by follow-up preview for EditedNode.
		resolveFirstPreview({
			total: 1,
			matched: 1,
			nodes: [
				{
					key: 'lnk-ed:OldNode',
					name: 'OldNode',
					source: 'Orig',
					isNew: true,
					missing: false,
					excluded: false,
					overCap: false,
				},
			],
			filteredNodes: [],
			subscriptions: [],
		});

		await waitFor(() => expect(fircPreviewTunnel).toHaveBeenCalledTimes(2));
		await fireEvent.click(
			await screen.findByRole('button', { name: m.firc_tunnel_edit_tab_nodes({ count: 1 }) }),
		);
		expect(await screen.findByText('EditedNode')).toBeInTheDocument();
		expect(screen.queryByText('OldNode')).not.toBeInTheDocument();
	});

	it('preserves loaded preview if background liveSubStamp follow-up preview fails transiently', async () => {
		const subTunnel = {
			id: 'tun-followup-err',
			device: 'tunvless15',
			description: 'FollowUpErr',
			enable: true,
			active: 1,
			by: 'connection',
			interval: 60,
			silence: 20,
			filter: '',
			filterExclude: '',
			order: ['sub-1:ExistingNode'],
			exclude: [],
			sources: [{ id: 'sub-1', kind: 'subscription', name: 'Sub1', url: 'https://sub.example/1', interval: 21600 }],
			uplink: { kind: 'auto', ref: '' },
			advanced: { ca: '', insecure: false, timeout: 8, kernelTcp: true, fingerprint: '' },
			inbounds: [],
		};
		const initialState = {
			id: 'tun-followup-err',
			device: 'tunvless15',
			status: 'up',
			active: ['ExistingNode'],
			nodes: [{ key: 'sub-1:ExistingNode', name: 'ExistingNode', source: 'Sub1', state: 'active', since: 0 }],
			filteredNodes: [],
			inbounds: [],
			groups: [],
			rxBps: 0,
			txBps: 0,
			since: 0,
			backoffS: 0,
			lastExit: 0,
			uplinkOk: true,
			tcpPort: 41015,
			tcpSkipped: [],
			tcpOff: '',
			subscriptions: [{ name: 'Sub1', nodes: 1, fetching: true, lastOk: 100, lastTry: 100, error: '' }],
		};
		fircTunnels.set([subTunnel as never]);
		fircTunnelsState.set([initialState as never]);
		fircNormalizeSource.mockResolvedValueOnce({
			sources: [{ id: 'lnk-new', kind: 'link', name: 'NewLink', link: 'vless://u@6.6.6.6:443#NewLinkNode' }],
		});

		let resolveAddPreview!: (v: unknown) => void;
		fircPreviewTunnel
			.mockImplementationOnce(
				() =>
					new Promise((resolve) => {
						resolveAddPreview = resolve;
					}),
			)
			.mockRejectedValueOnce(new Error('transient preview error'));

		fircEditTunnelId.set('tun-followup-err');
		fircEditTunnelSubTab.set('sources');
		render(FircTunnelEditModal);

		await fireEvent.click(await screen.findByRole('button', { name: m.firc_tunnel_edit_add_link() }));
		await fireEvent.input(await screen.findByLabelText(m.firc_tunnel_edit_links_label()), {
			target: { value: 'vless://u@6.6.6.6:443#NewLinkNode' },
		});
		await fireEvent.click(screen.getByRole('button', { name: m.common_add() }));
		await waitFor(() => expect(fircPreviewTunnel).toHaveBeenCalledTimes(1));

		// Background poll updates subscription stamp during addSources preview.
		fircTunnelsState.set([
			{
				...initialState,
				subscriptions: [{ name: 'Sub1', nodes: 1, fetching: false, lastOk: 200, lastTry: 200, error: '' }],
			} as never,
		]);

		resolveAddPreview({
			total: 2,
			matched: 2,
			nodes: [
				{ key: 'sub-1:ExistingNode', name: 'ExistingNode', source: 'Sub1', isNew: false, missing: false, excluded: false, overCap: false },
				{ key: 'lnk-new:NewLinkNode', name: 'NewLinkNode', source: 'NewLink', isNew: true, missing: false, excluded: false, overCap: false },
			],
			filteredNodes: [],
			subscriptions: [{ name: 'Sub1', cached: true }],
		});

		await waitFor(() => expect(fircPreviewTunnel).toHaveBeenCalledTimes(2));
		expect(await screen.findByText('NewLinkNode')).toBeInTheDocument();
		expect(screen.getByText('ExistingNode')).toBeInTheDocument();
	});

	it('clears stale removed-source preview if an invalid filter regex interrupts removeSource preview', async () => {
		const baseTunnel = {
			id: 'tun-rm-bad-re',
			device: 'tunvless16',
			description: 'RmBadRegex',
			enable: true,
			active: 1,
			by: 'connection',
			interval: 60,
			silence: 20,
			filter: '',
			filterExclude: '',
			order: [],
			exclude: [],
			sources: [],
			uplink: { kind: 'auto', ref: '' },
			advanced: { ca: '', insecure: false, timeout: 8, kernelTcp: true, fingerprint: '' },
			inbounds: [],
		};
		fircTunnels.set([baseTunnel as never]);
		fircTunnelsState.set([]);
		fircNormalizeSource
			.mockResolvedValueOnce({
				sources: [{ id: 'lnk-a', kind: 'link', name: 'LinkA', link: 'vless://u@1.1.1.1:443#NodeA' }],
			})
			.mockResolvedValueOnce({
				sources: [{ id: 'lnk-b', kind: 'link', name: 'LinkB', link: 'vless://u@2.2.2.2:443#NodeB' }],
			});

		let resolveRemovePreview!: (v: unknown) => void;
		fircPreviewTunnel
			.mockResolvedValueOnce({
				total: 1,
				matched: 1,
				nodes: [{ key: 'lnk-a:NodeA', name: 'NodeA', source: 'LinkA', isNew: true, missing: false, excluded: false, overCap: false }],
				filteredNodes: [],
				subscriptions: [],
			})
			.mockResolvedValueOnce({
				total: 2,
				matched: 2,
				nodes: [
					{ key: 'lnk-a:NodeA', name: 'NodeA', source: 'LinkA', isNew: true, missing: false, excluded: false, overCap: false },
					{ key: 'lnk-b:NodeB', name: 'NodeB', source: 'LinkB', isNew: true, missing: false, excluded: false, overCap: false },
				],
				filteredNodes: [],
				subscriptions: [],
			})
			.mockImplementationOnce(
				() =>
					new Promise((resolve) => {
						resolveRemovePreview = resolve;
					}),
			);

		fircEditTunnelId.set('tun-rm-bad-re');
		fircEditTunnelSubTab.set('sources');
		render(FircTunnelEditModal);

		await fireEvent.click(await screen.findByRole('button', { name: m.firc_tunnel_edit_add_link() }));
		await fireEvent.input(await screen.findByLabelText(m.firc_tunnel_edit_links_label()), {
			target: { value: 'vless://u@1.1.1.1:443#NodeA' },
		});
		await fireEvent.click(screen.getByRole('button', { name: m.common_add() }));
		expect(await screen.findByText('NodeA')).toBeInTheDocument();

		await fireEvent.click(screen.getByRole('button', { name: m.firc_tunnel_edit_tab_sources({ count: 1 }) }));
		await fireEvent.click(await screen.findByRole('button', { name: m.firc_tunnel_edit_add_link() }));
		await fireEvent.input(await screen.findByLabelText(m.firc_tunnel_edit_links_label()), {
			target: { value: 'vless://u@2.2.2.2:443#NodeB' },
		});
		await fireEvent.click(screen.getByRole('button', { name: m.common_add() }));
		expect(await screen.findByText('NodeB')).toBeInTheDocument();

		// Remove LinkB while LinkA remains -> starts removeSource preview, then enter invalid regex before it resolves.
		await fireEvent.click(screen.getByRole('button', { name: m.firc_tunnel_edit_tab_sources({ count: 2 }) }));
		const rmBtns = await screen.findAllByRole('button', { name: m.firc_tunnel_edit_remove_source() });
		await fireEvent.click(rmBtns[1]);
		await waitFor(() => expect(fircPreviewTunnel).toHaveBeenCalledTimes(3));

		const includeInp = screen.getByLabelText(m.firc_filter_include_label());
		await fireEvent.input(includeInp, { target: { value: '[' } });
		expect(await screen.findByText(m.firc_filter_warn_invalid())).toBeInTheDocument();

		resolveRemovePreview({
			total: 1,
			matched: 1,
			nodes: [{ key: 'lnk-a:NodeA', name: 'NodeA', source: 'LinkA', isNew: true, missing: false, excluded: false, overCap: false }],
			filteredNodes: [],
			subscriptions: [],
		});

		await fireEvent.click(
			await screen.findByRole('button', { name: m.firc_tunnel_edit_tab_nodes({ count: 1 }) }),
		);
		expect(await screen.findByText('NodeA')).toBeInTheDocument();
		expect(screen.queryByText('NodeB')).not.toBeInTheDocument();
	});

	it('synchronously prunes removed source even when subscription tunnel filter turns valid then invalid during removeSource preview', async () => {
		const subTunnel = {
			id: 'tun-rm-sub-bad-re',
			device: 'tunvless17',
			description: 'RmSubBadRe',
			enable: true,
			active: 1,
			by: 'connection',
			interval: 60,
			silence: 20,
			filter: '',
			filterExclude: '',
			order: ['sub-a:SubANode'],
			exclude: [],
			sources: [{ id: 'sub-a', kind: 'subscription', name: 'SubA', url: 'https://sub.example/a', interval: 21600 }],
			uplink: { kind: 'auto', ref: '' },
			advanced: { ca: '', insecure: false, timeout: 8, kernelTcp: true, fingerprint: '' },
			inbounds: [],
		};
		const liveState = {
			id: 'tun-rm-sub-bad-re',
			device: 'tunvless17',
			status: 'up',
			active: ['SubANode'],
			nodes: [{ key: 'sub-a:SubANode', name: 'SubANode', source: 'SubA', state: 'active', since: 0 }],
			filteredNodes: [],
			inbounds: [],
			groups: [],
			rxBps: 0,
			txBps: 0,
			since: 0,
			backoffS: 0,
			lastExit: 0,
			uplinkOk: true,
			tcpPort: 41017,
			tcpSkipped: [],
			tcpOff: '',
			subscriptions: [{ name: 'SubA', nodes: 1, fetching: false, lastOk: 100, lastTry: 100, error: '' }],
		};
		fircTunnels.set([subTunnel as never]);
		fircTunnelsState.set([liveState as never]);
		fircNormalizeSource.mockResolvedValueOnce({
			sources: [{ id: 'lnk-b', kind: 'link', name: 'LinkB', link: 'vless://u@2.2.2.2:443#LinkBNode' }],
		});

		let resolveRemovePreview!: (v: unknown) => void;
		fircPreviewTunnel
			.mockResolvedValueOnce({
				total: 2,
				matched: 2,
				nodes: [
					{ key: 'sub-a:SubANode', name: 'SubANode', source: 'SubA', isNew: false, missing: false, excluded: false, overCap: false },
					{ key: 'lnk-b:LinkBNode', name: 'LinkBNode', source: 'LinkB', isNew: true, missing: false, excluded: false, overCap: false },
				],
				filteredNodes: [],
				subscriptions: [{ name: 'SubA', cached: true }],
			})
			.mockImplementationOnce(
				() =>
					new Promise((resolve) => {
						resolveRemovePreview = resolve;
					}),
			);

		fircEditTunnelId.set('tun-rm-sub-bad-re');
		fircEditTunnelSubTab.set('sources');
		render(FircTunnelEditModal);

		await fireEvent.click(await screen.findByRole('button', { name: m.firc_tunnel_edit_add_link() }));
		await fireEvent.input(await screen.findByLabelText(m.firc_tunnel_edit_links_label()), {
			target: { value: 'vless://u@2.2.2.2:443#LinkBNode' },
		});
		await fireEvent.click(screen.getByRole('button', { name: m.common_add() }));
		expect(await screen.findByText('LinkBNode')).toBeInTheDocument();

		// Remove LinkB, then type valid 'a' (arms Effect 2), let removeSource settle, then type invalid 'a[' before 300ms.
		await fireEvent.click(screen.getByRole('button', { name: m.firc_tunnel_edit_tab_sources({ count: 2 }) }));
		const rmBtns = await screen.findAllByRole('button', { name: m.firc_tunnel_edit_remove_source() });
		await fireEvent.click(rmBtns[1]);
		await waitFor(() => expect(fircPreviewTunnel).toHaveBeenCalledTimes(2));

		const includeInp = screen.getByLabelText(m.firc_filter_include_label());
		await fireEvent.input(includeInp, { target: { value: 'a' } });
		resolveRemovePreview({
			total: 1,
			matched: 1,
			nodes: [{ key: 'sub-a:SubANode', name: 'SubANode', source: 'SubA', isNew: false, missing: false, excluded: false, overCap: false }],
			filteredNodes: [],
			subscriptions: [{ name: 'SubA', cached: true }],
		});
		await new Promise((r) => setTimeout(r, 20));
		await fireEvent.input(includeInp, { target: { value: 'a[' } });
		expect(await screen.findByText(m.firc_filter_warn_invalid())).toBeInTheDocument();

		await fireEvent.click(
			await screen.findByRole('button', { name: m.firc_tunnel_edit_tab_nodes({ count: 1 }) }),
		);
		expect(await screen.findByText('SubANode')).toBeInTheDocument();
		expect(screen.queryByText('LinkBNode')).not.toBeInTheDocument();
	});

	it('still previews added source after a transient filter edit is reverted while addSources preview deferred to Effect 2', async () => {
		const subTunnel = {
			id: 'tun-revert-deferred',
			device: 'tunvless18',
			description: 'RevertDeferred',
			enable: true,
			active: 1,
			by: 'connection',
			interval: 60,
			silence: 20,
			filter: '',
			filterExclude: '',
			order: ['sub-a:SubANode'],
			exclude: [],
			sources: [{ id: 'sub-a', kind: 'subscription', name: 'SubA', url: 'https://sub.example/a', interval: 21600 }],
			uplink: { kind: 'auto', ref: '' },
			advanced: { ca: '', insecure: false, timeout: 8, kernelTcp: true, fingerprint: '' },
			inbounds: [],
		};
		const liveState = {
			id: 'tun-revert-deferred',
			device: 'tunvless18',
			status: 'up',
			active: ['SubANode'],
			nodes: [{ key: 'sub-a:SubANode', name: 'SubANode', source: 'SubA', state: 'active', since: 0 }],
			filteredNodes: [],
			inbounds: [],
			groups: [],
			rxBps: 0,
			txBps: 0,
			since: 0,
			backoffS: 0,
			lastExit: 0,
			uplinkOk: true,
			tcpPort: 41018,
			tcpSkipped: [],
			tcpOff: '',
			subscriptions: [{ name: 'SubA', nodes: 1, fetching: false, lastOk: 100, lastTry: 100, error: '' }],
		};
		fircTunnels.set([subTunnel as never]);
		fircTunnelsState.set([liveState as never]);
		fircNormalizeSource.mockResolvedValueOnce({
			sources: [{ id: 'lnk-b', kind: 'link', name: 'LinkB', link: 'vless://u@2.2.2.2:443#LinkBNode' }],
		});

		let resolveAddPreview!: (v: unknown) => void;
		fircPreviewTunnel
			.mockImplementationOnce(
				() =>
					new Promise((resolve) => {
						resolveAddPreview = resolve;
					}),
			)
			.mockResolvedValueOnce({
				total: 2,
				matched: 2,
				nodes: [
					{ key: 'sub-a:SubANode', name: 'SubANode', source: 'SubA', isNew: false, missing: false, excluded: false, overCap: false },
					{ key: 'lnk-b:LinkBNode', name: 'LinkBNode', source: 'LinkB', isNew: true, missing: false, excluded: false, overCap: false },
				],
				filteredNodes: [],
				subscriptions: [{ name: 'SubA', cached: true }],
			});

		fircEditTunnelId.set('tun-revert-deferred');
		fircEditTunnelSubTab.set('sources');
		render(FircTunnelEditModal);

		// Add LinkB while preview === null -> hangs in addSources preview.
		await fireEvent.click(await screen.findByRole('button', { name: m.firc_tunnel_edit_add_link() }));
		await fireEvent.input(await screen.findByLabelText(m.firc_tunnel_edit_links_label()), {
			target: { value: 'vless://u@2.2.2.2:443#LinkBNode' },
		});
		await fireEvent.click(screen.getByRole('button', { name: m.common_add() }));
		await waitFor(() => expect(fircPreviewTunnel).toHaveBeenCalledTimes(1));

		// Close modal, type 'a' (arms Effect 2), resolve deferred addSources preview, then revert filter back to ''.
		await fireEvent.click(screen.getByRole('button', { name: m.common_cancel() }));
		const includeInp = screen.getByLabelText(m.firc_filter_include_label());
		await fireEvent.input(includeInp, { target: { value: 'a' } });

		resolveAddPreview({
			total: 2,
			matched: 2,
			nodes: [
				{ key: 'sub-a:SubANode', name: 'SubANode', source: 'SubA', isNew: false, missing: false, excluded: false, overCap: false },
				{ key: 'lnk-b:LinkBNode', name: 'LinkBNode', source: 'LinkB', isNew: true, missing: false, excluded: false, overCap: false },
			],
			filteredNodes: [],
			subscriptions: [{ name: 'SubA', cached: true }],
		});
		await new Promise((r) => setTimeout(r, 20));

		// Revert filter to '' before Effect 2's 300ms timer fires -> deferredDirectPreview ensures Effect 2 still runs!
		await fireEvent.input(includeInp, { target: { value: '' } });
		await waitFor(() => expect(fircPreviewTunnel).toHaveBeenCalledTimes(2), { timeout: 1500 });

		await fireEvent.click(
			await screen.findByRole('button', { name: m.firc_tunnel_edit_tab_nodes({ count: 2 }) }),
		);
		expect(await screen.findByText('LinkBNode')).toBeInTheDocument();
		expect(screen.getByText('SubANode')).toBeInTheDocument();
	});

	it('recovers deferred preview on a link-only tunnel after adding a link while filter regex is invalid then fixing it', async () => {
		const baseTunnel = {
			id: 'tun-link-bad-re',
			device: 'tunvless19',
			description: 'LinkBadRe',
			enable: true,
			active: 1,
			by: 'connection',
			interval: 60,
			silence: 20,
			filter: '',
			filterExclude: '',
			order: [],
			exclude: [],
			sources: [],
			uplink: { kind: 'auto', ref: '' },
			advanced: { ca: '', insecure: false, timeout: 8, kernelTcp: true, fingerprint: '' },
			inbounds: [],
		};
		fircTunnels.set([baseTunnel as never]);
		fircTunnelsState.set([]);
		fircNormalizeSource.mockResolvedValueOnce({
			sources: [{ id: 'lnk-x', kind: 'link', name: 'LinkX', link: 'vless://u@7.7.7.7:443#NodeX' }],
		});
		fircPreviewTunnel.mockResolvedValueOnce({
			total: 1,
			matched: 1,
			nodes: [{ key: 'lnk-x:NodeX', name: 'NodeX', source: 'LinkX', isNew: true, missing: false, excluded: false, overCap: false }],
			filteredNodes: [],
			subscriptions: [],
		});

		fircEditTunnelId.set('tun-link-bad-re');
		fircEditTunnelSubTab.set('sources');
		render(FircTunnelEditModal);

		// Enter invalid regex first, then add a link -> skips doomed 400 preview and arms deferred recovery.
		const includeInp = await screen.findByLabelText(m.firc_filter_include_label());
		await fireEvent.input(includeInp, { target: { value: '[' } });
		expect(await screen.findByText(m.firc_filter_warn_invalid())).toBeInTheDocument();

		await fireEvent.click(screen.getByRole('button', { name: m.firc_tunnel_edit_add_link() }));
		await fireEvent.input(await screen.findByLabelText(m.firc_tunnel_edit_links_label()), {
			target: { value: 'vless://u@7.7.7.7:443#NodeX' },
		});
		await fireEvent.click(screen.getByRole('button', { name: m.common_add() }));
		expect(fircPreviewTunnel).not.toHaveBeenCalled();

		// Switch back to Sources and clear the invalid regex -> Effect 2 recovers and previews NodeX!
		await fireEvent.click(screen.getByRole('button', { name: m.firc_tunnel_edit_tab_sources({ count: 1 }) }));
		await fireEvent.input(screen.getByLabelText(m.firc_filter_include_label()), { target: { value: '' } });
		await waitFor(() => expect(fircPreviewTunnel).toHaveBeenCalledTimes(1), { timeout: 1500 });

		await fireEvent.click(
			await screen.findByRole('button', { name: m.firc_tunnel_edit_tab_nodes({ count: 1 }) }),
		);
		expect(await screen.findByText('NodeX')).toBeInTheDocument();
	});

	it('recovers preview when Effect 2 aborts a slow direct preview and the transient filter edit is reverted', async () => {
		const subTunnel = {
			id: 'tun-abort-revert',
			device: 'tunvless20',
			description: 'AbortRevert',
			enable: true,
			active: 1,
			by: 'connection',
			interval: 60,
			silence: 20,
			filter: '',
			filterExclude: '',
			order: ['sub-a:SubANode'],
			exclude: [],
			sources: [{ id: 'sub-a', kind: 'subscription', name: 'SubA', url: 'https://sub.example/a', interval: 21600 }],
			uplink: { kind: 'auto', ref: '' },
			advanced: { ca: '', insecure: false, timeout: 8, kernelTcp: true, fingerprint: '' },
			inbounds: [],
		};
		const liveState = {
			id: 'tun-abort-revert',
			device: 'tunvless20',
			status: 'up',
			active: ['SubANode'],
			nodes: [{ key: 'sub-a:SubANode', name: 'SubANode', source: 'SubA', state: 'active', since: 0 }],
			filteredNodes: [],
			inbounds: [],
			groups: [],
			rxBps: 0,
			txBps: 0,
			since: 0,
			backoffS: 0,
			lastExit: 0,
			uplinkOk: true,
			tcpPort: 41020,
			tcpSkipped: [],
			tcpOff: '',
			subscriptions: [{ name: 'SubA', nodes: 1, fetching: false, lastOk: 100, lastTry: 100, error: '' }],
		};
		fircTunnels.set([subTunnel as never]);
		fircTunnelsState.set([liveState as never]);
		fircNormalizeSource.mockResolvedValueOnce({
			sources: [{ id: 'lnk-b', kind: 'link', name: 'LinkB', link: 'vless://u@8.8.8.8:443#LinkBNode' }],
		});

		let addSignal: AbortSignal | undefined;
		fircPreviewTunnel
			.mockImplementationOnce(
				(_p: unknown, signal?: AbortSignal) =>
					new Promise(() => {
						addSignal = signal;
					}),
			)
			.mockImplementationOnce(() => new Promise(() => {}))
			.mockResolvedValueOnce({
				total: 2,
				matched: 2,
				nodes: [
					{ key: 'sub-a:SubANode', name: 'SubANode', source: 'SubA', isNew: false, missing: false, excluded: false, overCap: false },
					{ key: 'lnk-b:LinkBNode', name: 'LinkBNode', source: 'LinkB', isNew: true, missing: false, excluded: false, overCap: false },
				],
				filteredNodes: [],
				subscriptions: [{ name: 'SubA', cached: true }],
			});

		fircEditTunnelId.set('tun-abort-revert');
		fircEditTunnelSubTab.set('sources');
		render(FircTunnelEditModal);

		// Add LinkB (direct preview hangs >300ms), close modal, type 'a' so Effect 2 fires at 300ms and aborts direct preview.
		await fireEvent.click(await screen.findByRole('button', { name: m.firc_tunnel_edit_add_link() }));
		await fireEvent.input(await screen.findByLabelText(m.firc_tunnel_edit_links_label()), {
			target: { value: 'vless://u@8.8.8.8:443#LinkBNode' },
		});
		await fireEvent.click(screen.getByRole('button', { name: m.common_add() }));
		await waitFor(() => expect(fircPreviewTunnel).toHaveBeenCalledTimes(1));

		await fireEvent.click(screen.getByRole('button', { name: m.common_cancel() }));
		const includeInp = screen.getByLabelText(m.firc_filter_include_label());
		await fireEvent.input(includeInp, { target: { value: 'a' } });

		// Wait for Effect 2's 300ms timer to fire and abort the direct preview.
		await waitFor(() => expect(fircPreviewTunnel).toHaveBeenCalledTimes(2), { timeout: 1500 });
		expect(addSignal?.aborted).toBe(true);

		// Revert filter back to '' before Effect 2's fetch resolves -> deferredDirectPreview must trigger call #3!
		await fireEvent.input(includeInp, { target: { value: '' } });
		await waitFor(() => expect(fircPreviewTunnel).toHaveBeenCalledTimes(3), { timeout: 1500 });

		await fireEvent.click(
			await screen.findByRole('button', { name: m.firc_tunnel_edit_tab_nodes({ count: 2 }) }),
		);
		expect(await screen.findByText('LinkBNode')).toBeInTheDocument();
		expect(screen.getByText('SubANode')).toBeInTheDocument();
	});

	it('refreshes active preview on a link-only tunnel when a link is edited inline after addSources settles', async () => {
		const baseTunnel = {
			id: 'tun-link-settled-edit',
			device: 'tunvless21',
			description: 'LinkSettledEdit',
			enable: true,
			active: 1,
			by: 'connection',
			interval: 60,
			silence: 20,
			filter: '',
			filterExclude: '',
			order: [],
			exclude: [],
			sources: [],
			uplink: { kind: 'auto', ref: '' },
			advanced: { ca: '', insecure: false, timeout: 8, kernelTcp: true, fingerprint: '' },
			inbounds: [],
		};
		fircTunnels.set([baseTunnel as never]);
		fircTunnelsState.set([]);
		fircNormalizeSource.mockResolvedValueOnce({
			sources: [{ id: 'lnk-1', kind: 'link', name: 'Link1', link: 'vless://u@1.1.1.1:443?allowInsecure=1#LinkNode' }],
		});
		fircPreviewTunnel
			.mockResolvedValueOnce({
				total: 1,
				matched: 1,
				nodes: [
					{
						key: 'lnk-1:LinkNode',
						name: 'LinkNode',
						source: 'Link1',
						isNew: true,
						missing: false,
						excluded: false,
						overCap: false,
						skipReason: 'allowInsecure: needs --insecure',
					},
				],
				filteredNodes: [],
				subscriptions: [],
			})
			.mockResolvedValueOnce({
				total: 1,
				matched: 1,
				nodes: [
					{
						key: 'lnk-1:LinkNode',
						name: 'LinkNode',
						source: 'Link1',
						isNew: true,
						missing: false,
						excluded: false,
						overCap: false,
					},
				],
				filteredNodes: [],
				subscriptions: [],
			});

		fircEditTunnelId.set('tun-link-settled-edit');
		fircEditTunnelSubTab.set('sources');
		render(FircTunnelEditModal);

		// Add Link1 and let its direct preview settle completely (showing skipped status).
		await fireEvent.click(await screen.findByRole('button', { name: m.firc_tunnel_edit_add_link() }));
		await fireEvent.input(await screen.findByLabelText(m.firc_tunnel_edit_links_label()), {
			target: { value: 'vless://u@1.1.1.1:443?allowInsecure=1#LinkNode' },
		});
		await fireEvent.click(screen.getByRole('button', { name: m.common_add() }));
		expect(await screen.findByText('LinkNode')).toBeInTheDocument();
		expect(screen.getByText(m.firc_node_state_skipped())).toBeInTheDocument();
		expect(fircPreviewTunnel).toHaveBeenCalledTimes(1);

		// Switch back to Sources and fix the link inline -> Effect 2 must re-preview even on link-only tunnel.
		await fireEvent.click(screen.getByRole('button', { name: m.firc_tunnel_edit_tab_sources({ count: 1 }) }));
		const linkInp = await screen.findByDisplayValue('vless://u@1.1.1.1:443?allowInsecure=1#LinkNode');
		await fireEvent.input(linkInp, { target: { value: 'vless://u@1.1.1.1:443#LinkNode' } });
		await waitFor(() => expect(fircPreviewTunnel).toHaveBeenCalledTimes(2), { timeout: 1500 });

		await fireEvent.click(
			await screen.findByRole('button', { name: m.firc_tunnel_edit_tab_nodes({ count: 1 }) }),
		);
		expect(await screen.findByText('LinkNode')).toBeInTheDocument();
		expect(screen.queryByText(m.firc_node_state_skipped())).not.toBeInTheDocument();
	});
});
