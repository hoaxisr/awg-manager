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
		fircSaveTunnels: vi.fn().mockResolvedValue({ tunnels: [] }),
		fircPreviewTunnel: vi.fn().mockResolvedValue({ nodes: [], errors: [] }),
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
		expect(screen.getByText(m.singbox_tabs_total_traffic())).toBeInTheDocument();
		expect(screen.getByText(m.singbox_tabs_avg_delay())).toBeInTheDocument();
		expect(screen.getByText(m.tunnels_awg_stat_leader())).toBeInTheDocument();

		const testBtn = screen.getByRole('button', {
			name: /Тест скорости и диагностика «FIN \(Финляндия\) \(Premium\)»/i,
		});
		await fireEvent.click(testBtn);
		expect(
			await screen.findByText(m.tunnel_test_title({ prefix: 'FIRC', name: 'FIN (Финляндия) (Premium)' })),
		).toBeInTheDocument();

		const editBtns = screen.getAllByText('Изменить');
		await fireEvent.click(editBtns[0]);
		expect(await screen.findByText(/Настройки туннеля FIN \(Финляндия\) \(Premium\) \(tunvless2\)/)).toBeInTheDocument();
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

		const probeAllBtn = await screen.findByRole('button', { name: /Проверить задержку/ });
		await fireEvent.click(probeAllBtn);

		await waitFor(() => {
			expect(screen.getByRole('button', { name: /Проверить задержку \(1\/2\)/ })).toBeInTheDocument();
			expect(screen.getByText('210 мс')).toBeInTheDocument();
		});

		releaseProbe([
			{ key: 'src1:Alpha', ok: true, why: '', handshakeMs: 200, firstByteMs: 10 },
			{ key: 'src1:Beta', ok: true, why: '', handshakeMs: 20, firstByteMs: 40 },
		]);

		expect(await screen.findByText('60 мс')).toBeInTheDocument();
		expect(screen.getByText('подкл. 20 · ответ 40')).toBeInTheDocument();
		expect(screen.getByText('подкл. 200 · ответ 10')).toBeInTheDocument();

		await fireEvent.click(screen.getByRole('button', { name: /Сортировать по пингу/ }));

		await waitFor(() => {
			const rows = Array.from(document.querySelectorAll('.nodes-table tbody tr'));
			expect(rows.length).toBe(2);
			expect(rows[0].textContent).toContain('Beta');
			expect(rows[1].textContent).toContain('Alpha');
		});

		const singleBtn = screen.getByRole('button', { name: 'Проверить узел Alpha' });
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
			expect(screen.getByText('30 мс')).toBeInTheDocument();
			expect(screen.getByText('подкл. 12 · ответ 18')).toBeInTheDocument();
		});
	});
});
