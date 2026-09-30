import { describe, expect, it, vi } from 'vitest';
import { api } from './client';

describe('Mihomo API client surface', () => {
	it('keeps routing and native resource methods on the public client', () => {
		for (const method of [
			'mihomoStatus',
			'mihomoInstall',
			'mihomoUpdate',
			'mihomoUninstall',
			'mihomoConfig',
			'mihomoReload',
			'mihomoNativeProxies',
			'mihomoNativeSubscriptions',
			'mihomoNativeGroups',
			'mihomoNativeRules',
			'mihomoNativeUnsupportedRules',
			'mihomoNativeDeleteUnsupportedRules',
			'mihomoNativeRuleProviders',
			'mihomoRuntimeProxies',
			'mihomoRuntimeProviders',
			'mihomoRuntimeRuleProviders',
			'mihomoRuntimeRefreshRuleProvider',
		] as const) {
			expect(typeof api[method], method).toBe('function');
		}
	});

	it('dispatches unsupported rules query and deletion payloads correctly', async () => {
		const requestSpy = vi.spyOn(api as any, 'request').mockImplementation(async () => ({ items: [], revision: 'v1:test' }));

		await api.mihomoNativeUnsupportedRules();
		expect(requestSpy).toHaveBeenCalledWith('/mihomo/native/rules/unsupported');

		requestSpy.mockImplementation(async () => ({ deleted: true, deletedCount: 2 }));

		await api.mihomoNativeDeleteUnsupportedRules(['rule-1', 'rule-2'], 'v1:rev1', true);
		expect(requestSpy).toHaveBeenCalledWith('/mihomo/native/rules/unsupported/delete', {
			method: 'POST',
			body: JSON.stringify({ ids: ['rule-1', 'rule-2'], revision: 'v1:rev1' }),
		});

		await api.mihomoNativeDeleteUnsupportedRules(['rule-1'], 'v1:rev1', false);
		expect(requestSpy).toHaveBeenCalledWith('/mihomo/native/rules/unsupported/delete?apply=false', {
			method: 'POST',
			body: JSON.stringify({ ids: ['rule-1'], revision: 'v1:rev1' }),
		});

		requestSpy.mockRestore();
	});
});
