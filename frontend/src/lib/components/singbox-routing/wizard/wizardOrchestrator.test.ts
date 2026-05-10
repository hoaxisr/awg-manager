import { describe, it, expect, vi, beforeEach } from 'vitest';
import type { WizardState } from '$lib/types';
import { runWizard } from './wizardOrchestrator';

const baseState = (): WizardState => ({
	step: 'applying',
	presetIds: ['netflix'],
	tunnelTag: 'awg-vpn0',
	deviceMacs: ['aa:aa:aa:aa:aa:01'],
	policyName: 'SBRouter',
	dnsServer: '1.1.1.1',
	applyLog: [],
	error: null,
});

const fakeApi = () => ({
	singboxRouterListPolicies: vi.fn().mockResolvedValue([]),
	singboxRouterCreatePolicy: vi.fn().mockResolvedValue({ name: 'Policy0', description: 'SBRouter' }),
	singboxRouterGetSettings: vi.fn().mockResolvedValue({ policyName: '', enabled: false }),
	singboxRouterPutSettings: vi.fn().mockResolvedValue(undefined),
	assignDeviceToPolicy: vi.fn().mockResolvedValue(undefined),
	singboxRouterListDNSServers: vi.fn().mockResolvedValue([]),
	singboxRouterAddDNSServer: vi.fn().mockResolvedValue(undefined),
	singboxRouterApplyPreset: vi.fn().mockResolvedValue(undefined),
	singboxRouterListDNSRules: vi.fn().mockResolvedValue([]),
	singboxRouterAddDNSRule: vi.fn().mockResolvedValue(undefined),
	singboxRouterUpdateDNSRule: vi.fn().mockResolvedValue(undefined),
	singboxRouterEnable: vi.fn().mockResolvedValue(undefined),
	singboxRouterStatus: vi.fn().mockResolvedValue({ running: true }),
});

const presets = [
	{
		id: 'netflix',
		name: 'Netflix',
		ruleSets: [{ tag: 'geosite-netflix', url: 'x' }],
		rules: [{ ruleSetRef: 'geosite-netflix', actionTarget: 'tunnel' as const }],
	},
];

describe('runWizard', () => {
	let api: ReturnType<typeof fakeApi>;
	beforeEach(() => {
		api = fakeApi();
	});

	it('happy path: settings empty → creates policy, persists, runs all phases', async () => {
		const onProgress = vi.fn();
		const result = await runWizard(baseState(), { api, presets, onProgress });
		expect(result).toEqual({
			policyCreated: true,
			devicesBound: 1,
			presetsApplied: 1,
			dnsServerCreated: true,
			dnsRuleApplied: true,
			engineStarted: true,
		});
		expect(api.singboxRouterGetSettings).toHaveBeenCalled();
		expect(api.singboxRouterCreatePolicy).toHaveBeenCalledWith('SBRouter');
		expect(api.singboxRouterPutSettings).toHaveBeenCalledWith(
			expect.objectContaining({ policyName: 'Policy0' }),
		);
		expect(api.assignDeviceToPolicy).toHaveBeenCalledWith('aa:aa:aa:aa:aa:01', 'Policy0');
		expect(api.singboxRouterAddDNSServer).toHaveBeenCalled();
		expect(api.singboxRouterApplyPreset).toHaveBeenCalledWith('netflix', 'awg-vpn0');
		expect(api.singboxRouterAddDNSRule).toHaveBeenCalled();
		expect(api.singboxRouterEnable).toHaveBeenCalled();
		expect(onProgress).toHaveBeenCalled();
	});

	it('reuses persisted policyName from settings (retry-safe)', async () => {
		api.singboxRouterGetSettings.mockResolvedValueOnce({ policyName: 'SBRouter', enabled: false });
		const result = await runWizard(baseState(), { api, presets, onProgress: vi.fn() });
		expect(api.singboxRouterCreatePolicy).not.toHaveBeenCalled();
		expect(api.singboxRouterPutSettings).not.toHaveBeenCalled();
		expect(result.policyCreated).toBe(false);
		expect(api.assignDeviceToPolicy).toHaveBeenCalledWith('aa:aa:aa:aa:aa:01', 'SBRouter');
	});

	it('skips createPolicy if SBRouter already exists in NDMS but not in settings', async () => {
		api.singboxRouterGetSettings.mockResolvedValueOnce({ policyName: '', enabled: false });
		// Realistic NDMS shape: name is auto-assigned ("Policy0"), description is the user label
		api.singboxRouterListPolicies.mockResolvedValueOnce([{ name: 'Policy0', description: 'SBRouter' }]);
		const result = await runWizard(baseState(), { api, presets, onProgress: vi.fn() });
		expect(api.singboxRouterCreatePolicy).not.toHaveBeenCalled();
		expect(api.singboxRouterPutSettings).toHaveBeenCalledWith(
			expect.objectContaining({ policyName: 'Policy0' }),
		);
		expect(result.policyCreated).toBe(false);
	});

	it('skips addDNSServer if wizard-upstream tag exists', async () => {
		api.singboxRouterListDNSServers.mockResolvedValueOnce([{ tag: 'wizard-upstream' }]);
		const result = await runWizard(baseState(), { api, presets, onProgress: vi.fn() });
		expect(api.singboxRouterAddDNSServer).not.toHaveBeenCalled();
		expect(result.dnsServerCreated).toBe(false);
	});

	it('throws on applyPreset failure with phase=applyPreset', async () => {
		api.singboxRouterApplyPreset.mockRejectedValueOnce(new Error('rule_set 404'));
		await expect(runWizard(baseState(), { api, presets, onProgress: vi.fn() }))
			.rejects.toMatchObject({ phase: 'applyPreset', message: expect.stringContaining('404') });
		expect(api.singboxRouterAddDNSRule).not.toHaveBeenCalled();
		expect(api.singboxRouterEnable).not.toHaveBeenCalled();
	});

	it('throws on enableEngine failure with phase=enableEngine', async () => {
		api.singboxRouterEnable.mockRejectedValueOnce(new Error('netfilter unavailable'));
		await expect(runWizard(baseState(), { api, presets, onProgress: vi.fn() }))
			.rejects.toMatchObject({ phase: 'enableEngine' });
	});

	it('throws if status never returns running:true within timeout', async () => {
		api.singboxRouterStatus.mockResolvedValue({ running: false });
		await expect(runWizard(baseState(), { api, presets, onProgress: vi.fn(), statusTimeoutMs: 50 }))
			.rejects.toMatchObject({ phase: 'enableEngine', message: expect.stringContaining('подтвердил') });
	});

	it('updates existing DNS-rule with new rule_sets in append mode', async () => {
		api.singboxRouterListDNSRules.mockResolvedValueOnce([
			{ rule_set: ['geosite-spotify'], server: 'wizard-upstream' },
		]);
		await runWizard(baseState(), { api, presets, onProgress: vi.fn() });
		expect(api.singboxRouterUpdateDNSRule).toHaveBeenCalledWith(
			0,
			expect.objectContaining({
				rule_set: expect.arrayContaining(['geosite-spotify', 'geosite-netflix']),
				server: 'wizard-upstream',
			}),
		);
		expect(api.singboxRouterAddDNSRule).not.toHaveBeenCalled();
	});
});
