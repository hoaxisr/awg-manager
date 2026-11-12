import { describe, it, expect } from 'vitest';
import type { SubscriptionMember } from '$lib/types';
import { extractMemberProtocols, filterAndSortMembers, memberMatchesSearch } from './memberSelectors';

const sampleMembers: SubscriptionMember[] = [
	{
		tag: 'sub-1',
		label: '🇩🇪 Berlin VLESS',
		protocol: 'vless',
		server: 'de.example.com',
		port: 443,
		security: 'reality',
		transport: 'tcp',
		sni: 'cdn.example.com',
	},
	{
		tag: 'sub-2',
		label: '🇫🇮 Helsinki Hy2',
		protocol: 'hysteria2',
		server: 'fi.example.net',
		port: 8443,
		security: 'tls',
	},
	{
		tag: 'sub-3',
		label: '🇳🇱 Amsterdam SS',
		protocol: 'shadowsocks',
		server: '198.51.100.10',
		port: 9000,
	},
	{
		tag: 'sub-4',
		label: '🇺🇸 New York VLESS',
		protocol: 'vless',
		server: 'us.example.org',
		port: 443,
		transport: 'ws',
		security: 'tls',
	},
];

describe('memberSelectors', () => {
	it('extractMemberProtocols aggregates unique protocols with counts', () => {
		const protos = extractMemberProtocols(sampleMembers);
		expect(protos).toEqual([
			{ protocol: 'hysteria2', label: 'Hysteria2', count: 1 },
			{ protocol: 'shadowsocks', label: 'Shadowsocks', count: 1 },
			{ protocol: 'vless', label: 'VLESS', count: 2 },
		]);
	});

	it('memberMatchesSearch matches name, ip:port, sni, tag and protocol aliases', () => {
		expect(memberMatchesSearch(sampleMembers[0], 'berlin')).toBe(true);
		expect(memberMatchesSearch(sampleMembers[0], 'cdn.example')).toBe(true);
		expect(memberMatchesSearch(sampleMembers[0], 'reality')).toBe(true);
		expect(memberMatchesSearch(sampleMembers[1], 'hy2')).toBe(true);
		expect(memberMatchesSearch(sampleMembers[2], 'ss')).toBe(true);
		expect(memberMatchesSearch(sampleMembers[2], '198.51.100.10:9000')).toBe(true);
		expect(memberMatchesSearch(sampleMembers[3], 'sub-4')).toBe(true);
		expect(memberMatchesSearch(sampleMembers[3], 'ws')).toBe(true);
		expect(memberMatchesSearch(sampleMembers[3], 'nonexistent')).toBe(false);
	});

	it('filterAndSortMembers excludes unchecked protocols cleanly', () => {
		const result = filterAndSortMembers(sampleMembers, {
			searchQuery: '',
			excludedProtocols: new Set(['hysteria2', 'shadowsocks']),
			sortBy: null,
			asc: true,
			activeMember: null,
			getDelayHistory: () => new Map(),
		});
		expect(result.map((m) => m.tag)).toEqual(['sub-1', 'sub-4']);
	});

	it('filterAndSortMembers sorts by delay placing invalid/missing values last', () => {
		const delays = new Map<string, number[]>([
			['sub-1', [180]],
			['sub-2', [45]],
			['sub-3', [0, 0]], // timeout -> last even in ascending order
		]);
		const ascResult = filterAndSortMembers(sampleMembers, {
			searchQuery: '',
			excludedProtocols: new Set(),
			sortBy: 'delay',
			asc: true,
			activeMember: null,
			getDelayHistory: () => delays,
		});
		expect(ascResult.map((m) => m.tag)).toEqual(['sub-2', 'sub-1', 'sub-3', 'sub-4']);

		const descResult = filterAndSortMembers(sampleMembers, {
			searchQuery: '',
			excludedProtocols: new Set(),
			sortBy: 'delay',
			asc: false,
			activeMember: null,
			getDelayHistory: () => delays,
		});
		expect(descResult.map((m) => m.tag)).toEqual(['sub-1', 'sub-2', 'sub-3', 'sub-4']);
	});

	it('filterAndSortMembers sorts by server, protocol, tag and active status', () => {
		const byProtocol = filterAndSortMembers(sampleMembers, {
			searchQuery: '',
			excludedProtocols: new Set(),
			sortBy: 'protocol',
			asc: true,
			activeMember: 'sub-3',
			getDelayHistory: () => new Map(),
		});
		expect(byProtocol.map((m) => m.protocol)).toEqual(['hysteria2', 'shadowsocks', 'vless', 'vless']);

		const byStatusDesc = filterAndSortMembers(sampleMembers, {
			searchQuery: '',
			excludedProtocols: new Set(),
			sortBy: 'status',
			asc: false,
			activeMember: 'sub-3',
			getDelayHistory: () => new Map(),
		});
		expect(byStatusDesc[0].tag).toBe('sub-3');

		const byTagDesc = filterAndSortMembers(sampleMembers, {
			searchQuery: '',
			excludedProtocols: new Set(),
			sortBy: 'tag',
			asc: false,
			activeMember: null,
			getDelayHistory: () => new Map(),
		});
		expect(byTagDesc.map((m) => m.tag)).toEqual(['sub-4', 'sub-3', 'sub-2', 'sub-1']);
	});
});
