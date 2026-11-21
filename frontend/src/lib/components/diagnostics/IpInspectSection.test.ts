import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import '@testing-library/jest-dom/vitest';
import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/svelte';
import { m } from '$lib/i18n';
import type { ConntrackConnection, IpInspectResult, StaticRouteList } from '$lib/types';
import type { FircGroup } from '$lib/types/firc';

const {
	inspectIpMock,
	fircGetGroupsMock,
	fircSaveGroupsMock,
	listDnsRoutesMock,
	updateDnsRouteMock,
	listStaticRoutesMock,
	updateStaticRouteMock,
} = vi.hoisted(() => ({
	inspectIpMock: vi.fn<(ip: string) => Promise<IpInspectResult>>(),
	fircGetGroupsMock: vi.fn<() => Promise<FircGroup[]>>(),
	fircSaveGroupsMock: vi.fn(),
	listDnsRoutesMock: vi.fn(),
	updateDnsRouteMock: vi.fn(),
	listStaticRoutesMock: vi.fn<() => Promise<StaticRouteList[]>>(),
	updateStaticRouteMock: vi.fn(),
}));

vi.mock('$lib/api/client', () => ({
	api: {
		inspectIp: inspectIpMock,
		fircGetGroups: fircGetGroupsMock,
		fircSaveGroups: fircSaveGroupsMock,
		listDnsRoutes: listDnsRoutesMock,
		updateDnsRoute: updateDnsRouteMock,
		listStaticRoutes: listStaticRoutesMock,
		updateStaticRoute: updateStaticRouteMock,
	},
}));

import IpInspectSection, { clearIpInspectCache } from './IpInspectSection.svelte';
import QuickAddRouteModal from './QuickAddRouteModal.svelte';
import ConnectionDetailsPanel from '../connections/ConnectionDetailsPanel.svelte';

describe('IpInspectSection', () => {
	beforeEach(() => {
		vi.resetAllMocks();
		clearIpInspectCache();
	});

	afterEach(() => {
		cleanup();
	});

	it('inspects public IP, caches by IP, renders PTR/ASN/Prefix/links, and notifies quick-add', async () => {
		inspectIpMock.mockResolvedValueOnce({
			ip: '1.1.1.1',
			family: 'ipv4',
			scope: 'public',
			ptr: 'one.one.one.one',
			asn: 13335,
			prefix: '1.1.1.0/24',
			country: 'AU',
			organization: 'CLOUDFLARENET, US',
		});
		const onOpenQuickAdd = vi.fn();

		const view = render(IpInspectSection, {
			props: { ip: '1.1.1.1', domain: 'api.cloudflare.com', onOpenQuickAdd },
		});

		await waitFor(() => expect(screen.getByText('AS13335')).toBeInTheDocument());
		expect(screen.getByText('CLOUDFLARENET, US')).toBeInTheDocument();
		expect(screen.getByText('1.1.1.0/24')).toBeInTheDocument();
		expect(screen.getByText('one.one.one.one')).toBeInTheDocument();
		expect(screen.getByRole('link', { name: /BGP\.Tools/i })).toHaveAttribute(
			'href',
			'https://bgp.tools/prefix/1.1.1.1',
		);
		expect(screen.getByRole('link', { name: /WHOIS/i })).toHaveAttribute(
			'href',
			'https://who.is/whois/cloudflare.com',
		);

		await fireEvent.click(
			screen.getByRole('button', { name: m.ip_inspect_quick_add_btn().replace(/^\+\s*/, '') }),
		);
		expect(onOpenQuickAdd).toHaveBeenCalledWith({
			ip: '1.1.1.1',
			domain: 'api.cloudflare.com',
			prefix: '1.1.1.0/24',
		});

		// Cache hit when remounting / switching back.
		inspectIpMock.mockResolvedValueOnce({
			ip: '198.18.0.5',
			family: 'ipv4',
			scope: 'benchmark',
		});
		await view.rerender({ ip: '198.18.0.5', domain: '' });
		await waitFor(() =>
			expect(screen.getByText(m.ip_inspect_scope_benchmark())).toBeInTheDocument(),
		);
		await view.rerender({ ip: '1.1.1.1', domain: 'api.cloudflare.com', onOpenQuickAdd });
		await waitFor(() => expect(screen.getByText('AS13335')).toBeInTheDocument());
		expect(inspectIpMock).toHaveBeenCalledTimes(2);
	});
});

describe('QuickAddRouteModal', () => {
	beforeEach(() => {
		vi.resetAllMocks();
	});

	afterEach(() => {
		cleanup();
	});

	it('adds root domain to a FIRC group and detects duplicates', async () => {
		const groups: FircGroup[] = [
			{
				id: 'g-ai',
				name: 'AI Services',
				interface: 'tunvless0',
				enable: true,
				devices: { allow: [], deny: [] },
				rules: [{ id: 'existing-1', type: 'namespace', rule: 'openai.com', enable: true }],
			},
		];
		fircGetGroupsMock.mockResolvedValue(groups);
		fircSaveGroupsMock.mockResolvedValue({ groups });
		listDnsRoutesMock.mockResolvedValue([]);
		listStaticRoutesMock.mockResolvedValue([]);

		const onadded = vi.fn();
		const onclose = vi.fn();
		const view = render(QuickAddRouteModal, {
			props: {
				open: true,
				ip: '104.18.32.7',
				domain: 'api.anthropic.com',
				prefix: '104.18.32.0/20',
				onclose,
				onadded,
			},
		});

		await waitFor(() =>
			expect(screen.getByRole('button', { name: m.ip_inspect_quick_add_submit() })).toBeEnabled(),
		);
		await fireEvent.click(screen.getByRole('button', { name: m.ip_inspect_quick_add_submit() }));

		await waitFor(() => expect(fircSaveGroupsMock).toHaveBeenCalledTimes(1));
		const savedGroups = fircSaveGroupsMock.mock.calls[0][0] as FircGroup[];
		expect(savedGroups[0].rules[0]).toMatchObject({
			type: 'namespace',
			rule: 'anthropic.com',
			enable: true,
		});
		expect(onadded).toHaveBeenCalledWith('AI Services', 'anthropic.com');

		// Duplicate attempt closes cleanly without calling save.
		fircGetGroupsMock.mockResolvedValue(savedGroups);
		await view.rerender({
			open: false,
			ip: '104.18.32.7',
			domain: 'api.anthropic.com',
			prefix: '104.18.32.0/20',
			onclose,
			onadded,
		});
		await view.rerender({
			open: true,
			ip: '104.18.32.7',
			domain: 'chat.anthropic.com',
			prefix: '104.18.32.0/20',
			onclose,
			onadded,
		});
		await waitFor(() =>
			expect(screen.getByRole('button', { name: m.ip_inspect_quick_add_submit() })).toBeEnabled(),
		);
		await fireEvent.click(screen.getByRole('button', { name: m.ip_inspect_quick_add_submit() }));
		await waitFor(() => expect(onclose).toHaveBeenCalledTimes(2));
		expect(fircSaveGroupsMock).toHaveBeenCalledTimes(1);
	});

	it('preserves all existing fields when adding a CIDR prefix to StaticRouteList', async () => {
		const staticList: StaticRouteList = {
			id: 'sr-1',
			name: 'CDN Subnets',
			tunnelID: 'tun-wg0',
			subnets: ['1.0.0.0/24'],
			fallback: 'reject',
			enabled: true,
			createdAt: '2026-01-01T00:00:00Z',
			updatedAt: '2026-01-02T00:00:00Z',
			iconUrl: 'https://example.com/icon.png',
		};
		fircGetGroupsMock.mockResolvedValue([]);
		listDnsRoutesMock.mockResolvedValue([]);
		listStaticRoutesMock.mockResolvedValue([staticList]);
		updateStaticRouteMock.mockResolvedValue({ ...staticList, subnets: ['1.0.0.0/24', '1.1.1.0/24'] });

		render(QuickAddRouteModal, {
			props: {
				open: true,
				ip: '1.1.1.1',
				prefix: '1.1.1.0/24',
				onclose: vi.fn(),
			},
		});

		await waitFor(() =>
			expect(screen.getByRole('button', { name: m.ip_inspect_quick_add_submit() })).toBeEnabled(),
		);
		await fireEvent.click(screen.getByRole('button', { name: m.ip_inspect_quick_add_submit() }));

		await waitFor(() => expect(updateStaticRouteMock).toHaveBeenCalledTimes(1));
		expect(updateStaticRouteMock.mock.calls[0][0]).toEqual({
			...staticList,
			subnets: ['1.0.0.0/24', '1.1.1.0/24'],
		});
	});
});

describe('ConnectionDetailsPanel IP inspector integration', () => {
	beforeEach(() => {
		vi.resetAllMocks();
		clearIpInspectCache();
	});

	afterEach(() => {
		cleanup();
	});

	it('renders IP family badge, snipped FQDN, and triggers IP inspection', async () => {
		inspectIpMock.mockResolvedValueOnce({
			ip: '1.1.1.1',
			family: 'ipv4',
			scope: 'public',
			asn: 13335,
			prefix: '1.1.1.0/24',
			organization: 'CLOUDFLARENET, US',
		});

		const conn: ConntrackConnection = {
			protocol: 'tcp',
			src: '192.168.1.50',
			dst: '1.1.1.1',
			srcPort: 54321,
			dstPort: 443,
			state: 'ESTABLISHED',
			packets: 12,
			bytes: 4096,
			bytesIn: 3072,
			bytesOut: 1024,
			ttl: 120,
			routeClass: 'tunnel',
			interface: 'nwg0',
			tunnelId: 'tun0',
			tunnelName: 'WG Home',
			clientMac: 'aa:bb:cc:dd:ee:ff',
			clientName: 'Laptop',
			rules: [
				{
					listId: 'l1',
					listName: 'Work',
					fqdn: 'very-long-subdomain.edge.eu-central.cloudflare.com',
				},
			],
		};

		render(ConnectionDetailsPanel, {
			props: {
				conn,
				onClose: vi.fn(),
				onKill: vi.fn(),
				onFilterClient: vi.fn(),
			},
		});

		expect(screen.getAllByText('IPv4').length).toBeGreaterThanOrEqual(1);
		await waitFor(() => expect(screen.getByText('AS13335')).toBeInTheDocument());
		expect(screen.getByText('1.1.1.0/24')).toBeInTheDocument();
	});
});
