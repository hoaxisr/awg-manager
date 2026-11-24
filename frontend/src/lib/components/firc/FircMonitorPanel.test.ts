import { beforeEach, describe, expect, it, vi } from 'vitest';

vi.hoisted(() => {
	Object.defineProperty(globalThis, 'matchMedia', {
		configurable: true,
		value: (query: string) => ({ matches: false, media: query, addEventListener() {}, removeEventListener() {} }),
	});
});
const {
	fircGetMonitor,
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
	fircGetAutolist.mockResolvedValue(autolistMock);
	fircAutolistCandidateAction.mockResolvedValue({ ...autolistMock, candidates: [] });
	fircGetGroups.mockResolvedValue([]);
	listDnsRoutes.mockResolvedValue([]);
	getPageScanOptions.mockResolvedValue({
		defaultProxy: 'http://127.0.0.1:1099',
		defaultProxyLabel: 'FIRC inbound (127.0.0.1:1099)',
		proxies: [
			{ url: 'http://127.0.0.1:1099', label: 'FIRC inbound (127.0.0.1:1099)', kind: 'firc' },
			{ url: 'socks5://127.0.0.1:1080', label: 'SOCKS5 proxy (:1080)', kind: 'socks5' },
		],
		interfaces: [{ name: 'opkgtun11', label: 'VPN DE (opkgtun11)', up: true }],
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

		const select = screen.getByLabelText<HTMLSelectElement>(m.page_scan_exit_label());
		expect(select.textContent).toContain(
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
		await fireEvent.change(select, { target: { value: 'iface:opkgtun11' } });
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
		expect(select.value).toBe('proxy:http://127.0.0.1:1099');

		// 3. Select custom proxy with empty URL -> shows validation error, clears stale cdn.example.com, skips API call.
		await fireEvent.change(select, { target: { value: 'custom' } });
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
	});
});
