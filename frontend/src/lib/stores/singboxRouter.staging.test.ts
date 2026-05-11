import { describe, it, expect, vi, beforeEach } from 'vitest';
import { get } from 'svelte/store';

vi.mock('$lib/api/client', () => ({
	api: {
		singboxRouterStagingStatus: vi.fn(),
		singboxRouterStatus: vi.fn().mockResolvedValue(null),
		singboxRouterGetSettings: vi.fn().mockResolvedValue(null),
		singboxRouterListRules: vi.fn().mockResolvedValue([]),
		singboxRouterListRuleSets: vi.fn().mockResolvedValue([]),
		singboxRouterListOutbounds: vi.fn().mockResolvedValue([]),
		singboxRouterListPresets: vi.fn().mockResolvedValue([]),
		singboxRouterListDNSServers: vi.fn().mockResolvedValue([]),
		singboxRouterListDNSRules: vi.fn().mockResolvedValue([]),
		singboxRouterGetDNSGlobals: vi.fn().mockResolvedValue({ final: '', strategy: '' }),
	},
}));

vi.mock('$lib/stores/awgTags', () => ({
	awgTags: { subscribe: vi.fn(() => () => {}) },
}));

vi.mock('$lib/stores/subscriptions', () => ({
	subscriptionsStore: { subscribe: vi.fn(() => () => {}) },
}));

vi.mock('$lib/stores/singbox', () => ({
	singboxTunnels: { subscribe: vi.fn(() => () => {}) },
}));

vi.mock('$lib/components/routing/singboxRouter/outboundOptions', () => ({
	buildOutboundOptions: vi.fn(() => []),
}));

import { singboxRouter } from './singboxRouter';
import { api } from '$lib/api/client';

describe('singboxRouter.staging', () => {
	beforeEach(() => {
		vi.clearAllMocks();
	});

	it('loadStaging sets the store value', async () => {
		const mockResp = { hasDraft: true, draftedAt: '2026-05-11T16:32:00Z' };
		vi.mocked(api.singboxRouterStagingStatus).mockResolvedValue(mockResp);
		await singboxRouter.loadStaging();
		expect(get(singboxRouter.staging)).toEqual(mockResp);
	});

	it('loadStaging on API error sets store to null', async () => {
		vi.mocked(api.singboxRouterStagingStatus).mockRejectedValue(new Error('boom'));
		await singboxRouter.loadStaging();
		expect(get(singboxRouter.staging)).toBeNull();
	});
});
