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
} = vi.hoisted(() => ({
	fircGetMonitor: vi.fn(),
	fircCheckDomain: vi.fn(),
	fircGetAutolist: vi.fn(),
	fircRecheckAutolist: vi.fn(),
	fircProbeAutolistHost: vi.fn(),
	fircAutolistCandidateAction: vi.fn(),
	fircDismissAutolistAddress: vi.fn(),
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
});
