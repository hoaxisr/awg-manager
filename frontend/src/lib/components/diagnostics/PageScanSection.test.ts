import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import '@testing-library/jest-dom/vitest';
import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/svelte';
import { m } from '$lib/i18n';
import type { PageScanResult } from '$lib/types';
import type { FircGroup } from '$lib/types/firc';

const {
	fircGetGroupsMock,
	fircSaveGroupsMock,
	listDnsRoutesMock,
	updateDnsRouteMock,
	copyToClipboardMock,
} = vi.hoisted(() => ({
	fircGetGroupsMock: vi.fn<() => Promise<FircGroup[]>>(),
	fircSaveGroupsMock: vi.fn(),
	listDnsRoutesMock: vi.fn(),
	updateDnsRouteMock: vi.fn(),
	copyToClipboardMock: vi.fn<(val: string) => Promise<boolean>>(),
}));

vi.mock('$lib/api/client', () => ({
	api: {
		fircGetGroups: fircGetGroupsMock,
		fircSaveGroups: fircSaveGroupsMock,
		listDnsRoutes: listDnsRoutesMock,
		updateDnsRoute: updateDnsRouteMock,
	},
}));

vi.mock('$lib/utils/clipboard', () => ({
	copyToClipboard: copyToClipboardMock,
}));

import PageScanSection from './PageScanSection.svelte';

const sampleResult: PageScanResult = {
	requestedUrl: 'https://www.themoviedb.org',
	finalUrl: 'https://www.themoviedb.org/',
	statusCode: 200,
	fetchVia: 'direct',
	proxyAvailable: true,
	bodyBytes: 12345,
	bodyTruncated: true,
	totalHosts: 4,
	hostsTruncated: true,
	durationMs: 320,
	resources: [
		{
			host: 'www.themoviedb.org',
			sources: ['document', 'script'],
			ip: '143.204.238.90',
			fakeIp: '198.18.0.10',
			routed: true,
			groupId: 'media',
			groupName: 'Media',
			outbound: 'tun0',
			outboundLabel: 'Netherlands',
			inspect: {
				ip: '143.204.238.90',
				family: 'ipv4',
				scope: 'public',
				ptr: 'server-143-204-238-90.arn53.r.cloudfront.net',
				asn: 16509,
				prefix: '143.204.238.0/24',
				country: 'US',
				organization: 'AMAZON-02, US',
			},
		},
		{
			host: 'sub1.onetrust.io',
			sources: ['csp'],
			ip: '8.6.112.0',
			routed: false,
			inspect: {
				ip: '8.6.112.0',
				family: 'ipv4',
				scope: 'public',
				asn: 13335,
				prefix: '8.6.112.0/24',
				country: 'US',
				organization: 'CLOUDFLARENET, US',
			},
		},
		{
			host: 'sub2.onetrust.io',
			sources: ['image'],
			ip: '8.6.112.1',
			routed: false,
		},
	],
};

describe('PageScanSection', () => {
	beforeEach(() => {
		vi.resetAllMocks();
		copyToClipboardMock.mockResolvedValue(true);
	});

	afterEach(() => {
		cleanup();
	});

	it('renders extracted resources, metadata, report copy, rescan, and quick-add', async () => {
		const onOpenQuickAdd = vi.fn();
		const onRescan = vi.fn();

		render(PageScanSection, {
			props: {
				result: sampleResult,
				onOpenQuickAdd,
				onRescan,
			},
		});

		expect(screen.getByText('HTTP 200')).toBeInTheDocument();
		expect(screen.getByText(m.page_scan_via_direct())).toBeInTheDocument();
		expect(screen.getByText(m.page_scan_body_truncated())).toBeInTheDocument();
		expect(screen.getByText('Media')).toBeInTheDocument();
		expect(screen.getByText('Netherlands (tun0)')).toBeInTheDocument();
		expect(screen.getByText('AS16509')).toBeInTheDocument();
		expect(screen.getByText('AMAZON-02, US')).toBeInTheDocument();
		expect(screen.getByText('143.204.238.0/24')).toBeInTheDocument();

		await fireEvent.click(screen.getByRole('button', { name: m.page_scan_rescan_proxy() }));
		expect(onRescan).toHaveBeenCalledWith('proxy');

		await fireEvent.click(screen.getByRole('button', { name: m.page_scan_copy_report() }));
		await waitFor(() => expect(copyToClipboardMock).toHaveBeenCalledTimes(1));
		const report = copyToClipboardMock.mock.calls[0][0];
		expect(report).toContain('www.themoviedb.org 143.204.238.90');
		expect(report).toContain('→ AS16509 AMAZON-02, US');
		expect(report).toContain('sub1.onetrust.io 8.6.112.0');

		const addButtons = screen.getAllByRole('button', {
			name: m.ip_inspect_quick_add_btn().replace(/^\+\s*/, ''),
		});
		await fireEvent.click(addButtons[1]);
		expect(onOpenQuickAdd).toHaveBeenCalledWith({
			domain: 'sub1.onetrust.io',
			ip: '8.6.112.0',
			prefix: '8.6.112.0/24',
		});
	});

	it('selects unrouted domains, collapses to root domains, and saves in batch to FIRC group', async () => {
		const groups: FircGroup[] = [
			{
				id: 'g-media',
				name: 'Media',
				interface: 'tun0',
				enable: true,
				devices: { allow: [], deny: [] },
				rules: [{ id: 'r1', type: 'namespace', rule: 'themoviedb.org', enable: true }],
			},
		];
		fircGetGroupsMock.mockResolvedValue(groups);
		fircSaveGroupsMock.mockResolvedValue({ groups });
		listDnsRoutesMock.mockResolvedValue([]);
		const onRoutesChanged = vi.fn();

		render(PageScanSection, {
			props: {
				result: sampleResult,
				onRoutesChanged,
			},
		});

		// Select all 2 unrouted subdomains (sub1.onetrust.io & sub2.onetrust.io).
		await fireEvent.click(
			screen.getByRole('button', {
				name: m.page_scan_select_unrouted({ count: 2 }),
			}),
		);

		// With collapseRoots=true by default, both collapse to 1 root domain (onetrust.io).
		await waitFor(() =>
			expect(
				screen.getByRole('button', { name: m.page_scan_bulk_add_btn({ count: 1 }) }),
			).toBeInTheDocument(),
		);

		// Toggling collapse off expands count to 2 exact subdomains.
		const collapseCheckbox = screen.getByRole('checkbox', {
			name: m.page_scan_collapse_root(),
		});
		await fireEvent.click(collapseCheckbox);
		expect(
			screen.getByRole('button', { name: m.page_scan_bulk_add_btn({ count: 2 }) }),
		).toBeInTheDocument();

		// Toggle collapse back on and submit.
		await fireEvent.click(collapseCheckbox);
		await waitFor(() =>
			expect(
				screen.getByRole('button', { name: m.page_scan_bulk_add_btn({ count: 1 }) }),
			).toBeEnabled(),
		);

		await fireEvent.click(
			screen.getByRole('button', { name: m.page_scan_bulk_add_btn({ count: 1 }) }),
		);

		await waitFor(() => expect(fircSaveGroupsMock).toHaveBeenCalledTimes(1));
		const savedGroups = fircSaveGroupsMock.mock.calls[0][0] as FircGroup[];
		expect(savedGroups[0].rules?.map((r) => r.rule)).toContain('onetrust.io');
		expect(fircSaveGroupsMock.mock.calls[0][1]).toBe(true);
		expect(onRoutesChanged).toHaveBeenCalledTimes(1);
	});

	it('renders explicit tunnel/proxy exit badge, defaultProxy tooltip, and retry action on error', async () => {
		const onRetry = vi.fn();
		const { rerender } = render(PageScanSection, {
			props: {
				result: {
					...sampleResult,
					fetchVia: 'interface',
					fetchTarget: 'opkgtun11',
					fetchTargetLabel: 'VPN DE (opkgtun11)',
					defaultProxy: 'http://127.0.0.1:1099',
				},
			},
		});

		const ifaceBadge = screen.getByText(
			m.page_scan_via_interface({ target: 'VPN DE (opkgtun11)' }),
		);
		expect(ifaceBadge).toBeInTheDocument();
		expect(ifaceBadge).toHaveAttribute('title', 'opkgtun11');

		await rerender({
			result: {
				...sampleResult,
				fetchVia: 'proxy',
				fetchTarget: 'http://127.0.0.1:1099',
				fetchTargetLabel: 'FIRC inbound (127.0.0.1:1099)',
				defaultProxy: 'http://127.0.0.1:1099',
			},
			onRescan: vi.fn(),
		});
		expect(
			screen.getByText(
				m.page_scan_via_proxy_target({ target: 'FIRC inbound (127.0.0.1:1099)' }),
			),
		).toBeInTheDocument();

		await rerender({
			result: {
				...sampleResult,
				fetchVia: 'direct',
				defaultProxy: 'http://127.0.0.1:1099',
			},
			onRescan: vi.fn(),
		});
		expect(screen.getByRole('button', { name: m.page_scan_rescan_proxy() })).toHaveAttribute(
			'title',
			m.page_scan_rescan_proxy_title({ proxy: 'http://127.0.0.1:1099' }),
		);

		await rerender({
			result: null,
			error: 'proxy timeout',
			onRetry,
		});
		const retryBtn = screen.getByRole('button', { name: m.page_scan_retry_btn() });
		await fireEvent.click(retryBtn);
		expect(onRetry).toHaveBeenCalledTimes(1);
	});

	it('does not call an unchecked host direct', async () => {
		const host = { host: 'target.example.com', sources: ['document'], routed: false };
		const { rerender } = render(PageScanSection, {
			props: { result: { ...sampleResult, resources: [{ ...host, routeUnknown: true }] } },
		});
		expect(screen.queryByText(m.page_scan_route_direct())).toBeNull();
		expect(screen.queryByText(m.page_scan_select_unrouted({ count: 1 }))).toBeNull();

		await rerender({ result: { ...sampleResult, resources: [host] } });
		expect(screen.getByText(m.page_scan_route_direct())).toBeInTheDocument();
	});
});
