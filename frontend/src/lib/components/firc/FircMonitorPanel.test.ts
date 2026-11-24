import { beforeEach, describe, expect, it, vi } from 'vitest';

vi.hoisted(() => {
	Object.defineProperty(globalThis, 'matchMedia', {
		configurable: true,
		value: (query: string) => ({ matches: false, media: query, addEventListener() {}, removeEventListener() {} }),
	});
	globalThis.ResizeObserver = class {
		observe() {}
		unobserve() {}
		disconnect() {}
	};
});
const {
	fircGetMonitor,
	fircGetHosts,
	fircGetPolicies,
	fircCheckDomain,
	fircGetAutolist,
	fircRecheckAutolist,
	fircProbeAutolistHost,
	fircAutolistCandidateAction,
	fircDismissAutolistAddress,
	fircGetGroups,
	listDnsRoutes,
	getPageScanOptions,
	scanPage,
} = vi.hoisted(() => ({
	fircGetMonitor: vi.fn(),
	fircGetHosts: vi.fn(),
	fircGetPolicies: vi.fn(),
	fircCheckDomain: vi.fn(),
	fircGetAutolist: vi.fn(),
	fircRecheckAutolist: vi.fn(),
	fircProbeAutolistHost: vi.fn(),
	fircAutolistCandidateAction: vi.fn(),
	fircDismissAutolistAddress: vi.fn(),
	fircGetGroups: vi.fn(),
	listDnsRoutes: vi.fn(),
	getPageScanOptions: vi.fn(),
	scanPage: vi.fn(),
}));
vi.mock('$lib/api/client', () => ({
	api: {
		fircGetMonitor,
		fircGetHosts,
		fircGetPolicies,
		fircCheckDomain,
		fircGetAutolist,
		fircRecheckAutolist,
		fircProbeAutolistHost,
		fircAutolistCandidateAction,
		fircDismissAutolistAddress,
		fircGetGroups,
		listDnsRoutes,
		getPageScanOptions,
		scanPage,
	},
}));

import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/svelte';
import { afterEach } from 'vitest';
import FircMonitorPanel from './FircMonitorPanel.svelte';
import { m } from '$lib/i18n';
import { extractFircGroupDomainLines, type FircGroup } from '$lib/types/firc';

const snapshot = {
	running: true, totalConns: 2, totalMatched: 10, totalDns: 20, timestamp: 1,
	servers: [{ id: 'lan', name: 'Домашняя сеть', category: 'lan', detail: 'br0', running: true,
		routingEnabled: true, dnsPackets: 20, dnsBytes: 100, matchedQueries: 10, activeConns: 0, txBytes: 0, rxBytes: 0 }],
	groups: [{ id: 'video', name: 'Видео', activeOutbound: 'test-tunnel', live: true,
		activeConns: 2, matchedPackets: 10, matchedBytes: 100, connTxBytes: 10, connRxBytes: 90, matchedQueries: 10, fakeSubnet: '198.18.1.0/24' }],
	recentMatches: [],
};

const autolistMock = {
	enabled: true,
	groupId: 'video',
	groupName: 'Видео',
	interface: 'test-tunnel',
	mode: 'assist',
	failuresDay: 5,
	listed: 1,
	candidates: [
		{ domain: 'blocked.example.com', host: 'api.blocked.example.com', fails: 3, state: 'failed', why: 'tls_reset' },
	],
	addresses: [],
};

beforeEach(() => {
	vi.clearAllMocks();
	fircGetMonitor.mockResolvedValue(snapshot);
	fircGetHosts.mockResolvedValue([
		{ name: 'Workstation', mac: 'aa:bb:cc:dd:ee:01', ip: '192.168.1.5', ip6: [], active: true, policy: 'Policy3' },
	]);
	fircGetPolicies.mockResolvedValue([{ name: 'Policy3', description: 'Дети', devices: 1 }]);
	fircGetAutolist.mockResolvedValue(autolistMock);
	fircAutolistCandidateAction.mockResolvedValue({ ...autolistMock, candidates: [] });
	fircGetGroups.mockResolvedValue([]);
	listDnsRoutes.mockResolvedValue([]);
	getPageScanOptions.mockResolvedValue({
		defaultProxy: 'http://127.0.0.1:1099',
		defaultProxyLabel: 'FIRC inbound (127.0.0.1:1099)',
		proxies: [
			{ url: 'http://127.0.0.1:1099', label: 'FIRC inbound (127.0.0.1:1099)' },
			{ url: 'socks5://127.0.0.1:1080', label: 'SOCKS5 proxy (:1080)' },
		],
		interfaces: [{ name: 'opkgtun11', label: 'VPN DE (opkgtun11)' }],
	});
});
afterEach(cleanup);

describe('FIRC compact monitor', () => {
	it('starts with a compact overview, not two technical tables and a feed', async () => {
		const { container } = render(FircMonitorPanel);
		await screen.findByText('Видео');
		expect(screen.getByText(m.firc_monitor_group_conns({ n: 2 }))).toBeTruthy();
		expect(container.querySelectorAll('table')).toHaveLength(0);
		expect(container.querySelector('details')?.open).toBe(false);
		expect(fircCheckDomain).not.toHaveBeenCalled();
	});
	it('shows sources and counters separately; DNS history is not current traffic', async () => {
		const { container } = render(FircMonitorPanel);
		await screen.findByText('Видео');
		await fireEvent.click(screen.getByRole('button', { name: m.firc_monitor_view_sources() }));
		expect(screen.getByText('Домашняя сеть')).toBeTruthy();
		expect(screen.getByText(m.firc_monitor_src_has_dns())).toBeTruthy();
		expect(container.querySelectorAll('table')).toHaveLength(1);
		await fireEvent.click(screen.getByRole('button', { name: m.firc_monitor_view_details() }));
		expect(screen.queryByText('Домашняя сеть')).toBeNull();
		expect(screen.getByText('FakeIP: 198.18.1.0/24')).toBeTruthy();
	});
	it('does not claim the daemon is stopped when the first request fails', async () => {
		fircGetMonitor.mockRejectedValueOnce(new Error('offline'));
		render(FircMonitorPanel);
		await screen.findByText(m.firc_monitor_load_unavailable());
		expect(screen.queryByText(m.firc_monitor_service_stopped())).toBeNull();
	});
	it('keeps the previous snapshot with a warning on refresh failure', async () => {
		render(FircMonitorPanel);
		await screen.findByText('Видео');
		fircGetMonitor.mockRejectedValueOnce(new Error('offline'));
		await fireEvent.click(screen.getByRole('button', { name: m.common_refresh() }));
		await waitFor(() => expect(screen.getByRole('status').textContent).toBe(m.firc_monitor_load_stale()));
		expect(screen.getByText('Видео')).toBeTruthy();
	});
	it('does not render the removed bypass tab', async () => {
		render(FircMonitorPanel);
		await screen.findByText('Видео');
		expect(screen.queryByRole('button', { name: /Обход/i })).toBeNull();
	});
	it('extracts enabled domain rules of selected groups and reports skipped regex/CIDR/lists', () => {
		const groups: FircGroup[] = [
			{
				id: 'g1',
				name: 'AI',
				interface: 'tunvless0',
				enable: true,
				devices: { allow: [], deny: [] },
				list: { url: 'https://example.com/openai.srs', interval: 86400 },
				rules: [
					{ id: 'r1', type: 'namespace', rule: 'openai.com', enable: true },
					{ id: 'r2', type: 'wildcard', rule: '*.chatgpt.com', enable: true },
					{ id: 'r3', type: 'domain', rule: 'disabled.example.com', enable: false },
					{ id: 'r4', type: 'subnet', rule: '1.1.1.0/24', enable: true },
					{ id: 'r5', type: 'regex', rule: '^.*\\.claude\\.ai$', enable: true },
				],
			},
		];
		const res = extractFircGroupDomainLines(groups, ['g1']);
		expect(res.lines).toEqual(['openai.com', '*.chatgpt.com']);
		expect(res.skippedRegexOrCidr).toBe(2);
		expect(res.skippedRemoteLists).toBe(1);
	});

	it('loads page scan egress options, supports tunnel/custom proxy selection, syncs rescan selector, and clears stale results on error', async () => {
		const { container } = render(FircMonitorPanel);
		await screen.findByText('Видео');

		const details = container.querySelector('details.domain-checker') as HTMLDetailsElement;
		details.open = true;
		await fireEvent(details, new Event('toggle'));

		await waitFor(() => {
			expect(getPageScanOptions).toHaveBeenCalledTimes(1);
		});

		const trigger = screen.getByLabelText(m.page_scan_exit_label());
		const pickExit = async (name: string) => {
			await fireEvent.click(trigger);
			await fireEvent.click(await screen.findByRole('option', { name }));
		};
		expect(trigger.textContent).toContain(
			m.page_scan_exit_auto_with_proxy({ proxy: 'FIRC inbound (127.0.0.1:1099)' }),
		);

		const domainInput = screen.getByLabelText(m.firc_monitor_checker_input_aria());
		await fireEvent.input(domainInput, { target: { value: 'example.com' } });

		// 1. Select tunnel interface and scan.
		scanPage.mockResolvedValueOnce({
			requestedUrl: 'https://example.com',
			finalUrl: 'https://example.com/',
			statusCode: 200,
			fetchVia: 'interface',
			fetchTarget: 'opkgtun11',
			fetchTargetLabel: 'VPN DE (opkgtun11)',
			proxyAvailable: true,
			bodyBytes: 1200,
			totalHosts: 1,
			durationMs: 42,
			resources: [{ host: 'cdn.example.com', sources: ['script'], routed: false }],
		});
		await pickExit('VPN DE (opkgtun11)');
		await fireEvent.click(screen.getByRole('button', { name: m.page_scan_btn() }));

		await waitFor(() => {
			expect(scanPage).toHaveBeenCalledWith('example.com', {
				fetchMode: 'interface',
				interface: 'opkgtun11',
			});
		});
		expect(await screen.findByText('cdn.example.com')).toBeTruthy();

		// 2. Rescan via proxy from interface result syncs selector to default proxy.
		scanPage.mockResolvedValueOnce({
			requestedUrl: 'https://example.com',
			finalUrl: 'https://example.com/',
			statusCode: 200,
			fetchVia: 'proxy',
			fetchTarget: 'http://127.0.0.1:1099',
			fetchTargetLabel: 'FIRC inbound (127.0.0.1:1099)',
			defaultProxy: 'http://127.0.0.1:1099',
			proxyAvailable: true,
			bodyBytes: 1200,
			totalHosts: 1,
			durationMs: 40,
			resources: [{ host: 'cdn.example.com', sources: ['script'], routed: false }],
		});
		await fireEvent.click(screen.getByRole('button', { name: m.page_scan_rescan_proxy() }));
		await waitFor(() => {
			expect(scanPage).toHaveBeenLastCalledWith('example.com', { fetchMode: 'proxy' });
		});
		expect(trigger.textContent).toContain('FIRC inbound (127.0.0.1:1099)');

		// 3. Select custom proxy with empty URL -> shows validation error, clears stale cdn.example.com, skips API call.
		await pickExit(m.page_scan_exit_custom_proxy());
		await fireEvent.click(screen.getByRole('button', { name: m.page_scan_btn() }));
		expect(await screen.findByText(m.page_scan_custom_proxy_required())).toBeTruthy();
		expect(screen.queryByText('cdn.example.com')).toBeNull();
		expect(scanPage).toHaveBeenCalledTimes(2);

		// 4. Enter custom proxy URL -> scan fails -> error displayed and stale result remains cleared.
		const customInput = screen.getByLabelText(m.page_scan_exit_custom_proxy());
		await fireEvent.input(customInput, { target: { value: 'socks5://127.0.0.1:1080' } });
		scanPage.mockRejectedValueOnce(new Error('socks5 timeout'));
		await fireEvent.click(screen.getByRole('button', { name: m.page_scan_btn() }));

		await waitFor(() => {
			expect(scanPage).toHaveBeenLastCalledWith('example.com', {
				fetchMode: 'proxy',
				proxyUrl: 'socks5://127.0.0.1:1080',
			});
		});
		expect(await screen.findByText('socks5 timeout')).toBeTruthy();
		expect(screen.queryByText('cdn.example.com')).toBeNull();

		// 5. Поле не предлагается запомнить браузеру и забывается при сворачивании панели.
		expect(customInput.getAttribute('autocomplete')).toBe('off');
		details.open = false;
		await fireEvent(details, new Event('toggle'));
		expect(screen.getByLabelText<HTMLInputElement>(m.page_scan_exit_custom_proxy()).value).toBe('');
	});

	// Как бэкенд: списки клиентов — только по запросу, счётчики — всегда.
	function clientsSnapshot(withClients: boolean) {
		const lan = { ip: '192.168.1.5', sourceId: 'lan_br0', sourceName: 'LAN / Wi-Fi', conns: 15, txBytes: 500000, rxBytes: 2500000 };
		const vpn = { ip: '10.10.0.5', sourceId: 'awg_server', sourceName: 'AWGM WireGuard', conns: 5, txBytes: 177000, rxBytes: 750000 };
		const lists = <T>(clients: T) => (withClients ? { clients } : {});
		const event = { at: 1700000000, groupId: 'tg', groupName: 'Telegram', outbound: 'opkgtun10' };
		return {
			...snapshot,
			totalConns: 20,
			totalClients: 130,
			...lists([{ ...lan, groups: ['Telegram'] }, { ...vpn, groups: ['Telegram'] }]),
			servers: [{ ...snapshot.servers[0], id: 'lan_br0', activeConns: 15, activeClients: 1, ...lists([{ ...lan, groups: ['Telegram'] }]) }],
			groups: [{ ...snapshot.groups[0], id: 'tg', name: 'Telegram', activeConns: 20, activeClients: 2, ...lists([lan, vpn]) }],
			recentMatches: [
				{ ...event, seq: 1, clientIp: '192.168.1.5', sourceId: 'lan_br0', sourceName: 'LAN / Wi-Fi', domain: 'api.telegram.org' },
				{ ...event, seq: 2, clientIp: '192.168.1.50', sourceId: 'lan_br0', sourceName: 'LAN / Wi-Fi', domain: 'web.telegram.org' },
			],
		};
	}

	async function renderWithClients() {
		fircGetMonitor.mockImplementation(async (withClients: boolean) => clientsSnapshot(withClients));
		const view = render(FircMonitorPanel);
		await screen.findByText('Telegram');
		return view;
	}

	const openView = (name: string) => fireEvent.click(screen.getByRole('button', { name }));
	const openGlobalClients = () => fireEvent.click(screen.getByRole('button', { name: m.firc_monitor_clients_show({ count: 130 }) }));

	it('requests client lists only while one is open, and names once per opening', async () => {
		await renderWithClients();
		await openView(m.firc_monitor_view_details());
		expect(fircGetMonitor.mock.calls).toEqual([[false]]);
		expect(fircGetHosts).not.toHaveBeenCalled();

		await openGlobalClients();
		await waitFor(() => expect(fircGetMonitor).toHaveBeenLastCalledWith(true));
		expect(await screen.findByText('Workstation')).toBeTruthy();

		const calls = fircGetMonitor.mock.calls.length;
		await fireEvent.click(screen.getByRole('button', { name: m.common_refresh() }));
		await waitFor(() => expect(fircGetMonitor).toHaveBeenCalledTimes(calls + 1));
		expect(fircGetMonitor).toHaveBeenLastCalledWith(true);
		expect(fircGetHosts).toHaveBeenCalledTimes(1);
		expect(fircGetPolicies).toHaveBeenCalledTimes(1);

		// Раскрытый список другого раздела не виден — и не запрашивается.
		await openView(m.firc_monitor_view_sources());
		await fireEvent.click(screen.getByRole('button', { name: m.common_refresh() }));
		await waitFor(() => expect(fircGetMonitor).toHaveBeenLastCalledWith(false));

		await fireEvent.click(screen.getByRole('button', { name: m.firc_monitor_devices({ n: 1 }) }));
		await waitFor(() => expect(fircGetMonitor).toHaveBeenLastCalledWith(true));
		expect(fircGetHosts).toHaveBeenCalledTimes(2);
		expect(fircGetPolicies).toHaveBeenCalledTimes(2);
	});

	it('loads an opened list on pause and after the poll already in flight', async () => {
		await renderWithClients();
		await openView(m.firc_monitor_view_details());
		await fireEvent.click(screen.getByRole('button', { name: m.diag_logs_pause() }));
		let release = () => {};
		fircGetMonitor.mockImplementationOnce(
			() => new Promise((resolve) => { release = () => resolve(clientsSnapshot(false)); }),
		);
		await fireEvent.click(screen.getByRole('button', { name: m.common_refresh() }));
		await openGlobalClients();
		expect(screen.getByTestId('firc-mon-global-clients').textContent).toContain(m.firc_monitor_loading_data());
		release();
		expect(await screen.findByText('Workstation')).toBeTruthy();
	});

	it('shows device names, policy descriptions and the cut-off note in every list', async () => {
		const { container } = await renderWithClients();

		await openView(m.firc_monitor_view_sources());
		await fireEvent.click(screen.getByRole('button', { name: m.firc_monitor_devices({ n: 1 }) }));
		await screen.findByText('Workstation');
		const srvList = screen.getByTestId('firc-mon-server-clients-lan_br0').textContent;
		expect(srvList).toContain('AA:BB:CC:DD:EE:01');
		expect(srvList).not.toContain(m.firc_monitor_clients_truncated({ shown: 1, total: 1 }));
		expect(container.querySelectorAll('table')).toHaveLength(1);

		await openView(m.firc_monitor_view_details());
		await fireEvent.click(screen.getByRole('button', { name: m.firc_monitor_devices({ n: 2 }) }));
		await waitFor(() => expect(screen.getByTestId('firc-mon-group-clients-tg').textContent).toContain('AWGM WireGuard'));
		const grpList = screen.getByTestId('firc-mon-group-clients-tg').textContent;
		expect(grpList).toContain('192.168.1.5');
		expect(grpList).toContain('10.10.0.5');

		await openGlobalClients();
		const globalList = screen.getByTestId('firc-mon-global-clients').textContent;
		expect(globalList).toContain('Workstation');
		expect(globalList).toContain('Дети');
		expect(globalList).not.toContain('Policy3');
		expect(globalList).toContain(m.firc_monitor_clients_truncated({ shown: 2, total: 130 }));
	});

	it('still lists clients by address when names are unavailable', async () => {
		fircGetHosts.mockRejectedValue(new Error('offline'));
		fircGetPolicies.mockRejectedValue(new Error('offline'));
		await renderWithClients();
		await openView(m.firc_monitor_view_details());
		await openGlobalClients();
		await waitFor(() => expect(screen.getByTestId('firc-mon-global-clients').textContent).toContain('192.168.1.5'));
	});

	it('jumps to events of exactly this device and lets the filter be removed', async () => {
		const { container } = await renderWithClients();
		// Прежние фильтры ленты не должны прятать события устройства.
		await openView(m.firc_monitor_view_events());
		await fireEvent.change(container.querySelector('.feed-select')!, { target: { value: 'lan_br0' } });
		await fireEvent.input(screen.getByPlaceholderText(m.firc_monitor_feed_search()), { target: { value: 'web' } });
		expect(screen.queryByText('api.telegram.org')).toBeNull();

		await openView(m.firc_monitor_view_details());
		await openGlobalClients();
		await screen.findByText('Workstation');
		await fireEvent.click(screen.getByTitle(m.firc_monitor_clients_filter_events_title({ ip: '192.168.1.5' })));

		expect(screen.getByText('api.telegram.org')).toBeTruthy();
		expect(screen.queryByText('web.telegram.org')).toBeNull();
		expect(container.querySelector<HTMLSelectElement>('.feed-select')!.value).toBe('all');

		await fireEvent.click(screen.getByTitle(m.firc_monitor_feed_client_clear()));
		expect(screen.getByText('web.telegram.org')).toBeTruthy();
		expect(screen.queryByTitle(m.firc_monitor_feed_client_clear())).toBeNull();
	});
});
