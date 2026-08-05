import { describe, it, expect, vi, beforeEach } from 'vitest';
import { get } from 'svelte/store';

vi.mock('$lib/api/client', () => ({
	api: {
		singboxRouterStatus: vi.fn().mockResolvedValue(null),
		singboxRouterGetSettings: vi.fn().mockResolvedValue(null),
		singboxRouterListRules: vi.fn().mockResolvedValue([]),
		singboxRouterListRuleSets: vi.fn().mockResolvedValue([]),
		singboxRouterListOutbounds: vi.fn().mockResolvedValue([]),
		singboxRouterListPresets: vi.fn().mockResolvedValue([]),
		singboxRouterListDNSServers: vi.fn().mockResolvedValue([]),
		singboxRouterListDNSRules: vi.fn().mockResolvedValue([]),
		singboxRouterListDNSRewrites: vi.fn().mockResolvedValue([]),
		singboxRouterGetDNSGlobals: vi.fn().mockResolvedValue({ final: '', strategy: '' }),
		singboxRouterStagingStatus: vi.fn().mockResolvedValue(null),
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

import { createSingboxRouterStore } from './singboxRouter';
import { api } from '$lib/api/client';

describe('singboxRouter.loadAll', () => {
	beforeEach(() => {
		vi.clearAllMocks();
	});

	it('успешная загрузка помечает стор инициализированным', async () => {
		const store = createSingboxRouterStore();
		await store.loadAll();

		expect(get(store.initialized)).toBe(true);
		expect(get(store.attempted)).toBe(true);
		expect(get(store.loading)).toBe(false);
		expect(get(store.error)).toBeNull();
	});

	// Гейт onMount на табах пропускает загрузку при initialized === true, а кнопки
	// «повторить» там нет: подними флаг после неудачи — и однократно недоступный
	// бэкенд оставил бы пустые списки до перезагрузки страницы.
	it('ошибка загрузки НЕ помечает стор инициализированным', async () => {
		vi.mocked(api.singboxRouterListRules).mockRejectedValue(new Error('network error'));

		const store = createSingboxRouterStore();
		await store.loadAll();

		expect(get(store.initialized)).toBe(false);
		expect(get(store.loading)).toBe(false);
		expect(get(store.error)).toBe('network error');
		// attempted при этом поднимается: спиннер холодной загрузки обязан
		// отпустить страницу даже после неудачи.
		expect(get(store.attempted)).toBe(true);
	});
});
