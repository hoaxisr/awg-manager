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
		fircProbeTunnel: vi.fn().mockResolvedValue([]),
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

		await fireEvent.click(screen.getByText('Изменить'));
		expect(await screen.findByText(/Настройки туннеля VOX \(tunvless0\)/)).toBeInTheDocument();

		// Nodes tab shows collapsible «Скрыто фильтром (1)»
		const hiddenToggle = await screen.findByRole('button', { name: /Скрыто фильтром \(1\)/i });
		expect(hiddenToggle).toHaveAttribute('aria-expanded', 'false');
		await fireEvent.click(hiddenToggle);
		expect(hiddenToggle).toHaveAttribute('aria-expanded', 'true');
		expect(screen.getByText('RU-1')).toBeInTheDocument();

		// Switch to Подписки и ссылки tab -> Filter section appears
		await fireEvent.click(screen.getByRole('button', { name: /Подписки и ссылки/i }));
		expect(await screen.findByText('Фильтр серверов')).toBeInTheDocument();
		const includeInp = screen.getByLabelText('Включать только (regex)') as HTMLInputElement;
		const excludeInp = screen.getByLabelText('Исключать (regex)') as HTMLInputElement;

		// Lookaround triggers Go RE2 hint warning and skips preview API call
		await fireEvent.input(includeInp, { target: { value: '^(?!RU).*$' } });
		expect(await screen.findByText(/Go RE2 не поддерживает lookahead\/lookbehind/i)).toBeInTheDocument();
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

		await fireEvent.click(screen.getByRole('button', { name: /^Узлы \(/i }));
		expect(await screen.findByRole('button', { name: /Скрыто фильтром \(2\)/i })).toBeInTheDocument();
		expect(screen.getByText('NL-LTE')).toBeInTheDocument();

		await fireEvent.click(screen.getByRole('button', { name: 'Сохранить' }));
		await waitFor(() => {
			expect(fircSaveTunnels).toHaveBeenCalled();
		});
		const savedArg = fircSaveTunnels.mock.calls[0][0];
		expect(savedArg[0].filter).toBe('(?i)DE');
		expect(savedArg[0].filterExclude).toBe('(?i)(RU|LTE)');
	});
});
