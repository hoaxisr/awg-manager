import { writable, derived, get } from 'svelte/store';
import { api } from '$lib/api/client';
import { awgTags } from './awgTags';
import { subscriptionsStore } from './subscriptions';
import { singboxTunnels } from './singbox';
import { buildOutboundOptions } from '$lib/components/routing/singboxRouter/outboundOptions';
import { reconcileRuleUiKeys } from '$lib/utils/ruleUiKeys';
import {
	normalizeRulesForUI,
	normalizeRuleSetsForUI,
} from '$lib/utils/singboxInlineRules';
import type {
	SingboxRouterRule,
	SingboxRouterRuleSet,
	SingboxRouterOutbound,
	SingboxRouterDNSServer,
	SingboxRouterDNSRule,
	SingboxRouterDNSGlobals,
} from '$lib/types';

function createFakeipConfigStore() {
	const rules = writable<SingboxRouterRule[]>([]);
	const ruleUiKeys = writable<string[]>([]);
	const ruleSets = writable<SingboxRouterRuleSet[]>([]);
	const outbounds = writable<SingboxRouterOutbound[]>([]);
	const dnsServers = writable<SingboxRouterDNSServer[]>([]);
	const dnsRules = writable<SingboxRouterDNSRule[]>([]);
	const dnsGlobals = writable<SingboxRouterDNSGlobals>({ final: '', strategy: '' });
	const loading = writable(false);
	const initialized = writable(false);
	const error = writable<string | null>(null);

	// options — unified outbound dropdown groups for fakeip sub-tabs.
	// Combines awgTags + sing-box tunnels + this store's composite outbounds,
	// with subscription labels mixed in for source='subscription' composites.
	const options = derived(
		[outbounds, singboxTunnels, awgTags, subscriptionsStore],
		([$outbounds, $sb, $awg, $subs]) =>
			buildOutboundOptions(
				$awg.data,
				$sb.data,
				$outbounds,
				true,
				$subs.data,
			),
	);

	// optionsReady — true once all PollingStore sources have settled.
	const optionsReady = derived(
		[singboxTunnels, awgTags, subscriptionsStore],
		([$sb, $awg, $subs]) => {
			const settled = (s: 'idle' | 'loading' | 'fresh' | 'stale' | 'error'): boolean =>
				s === 'fresh' || s === 'stale' || s === 'error';
			return settled($sb.status) && settled($awg.status) && settled($subs.status);
		},
	);

	function setRulesWithKeys(nextRules: SingboxRouterRule[]): void {
		const normalized = normalizeRulesForUI(nextRules);
		const prevRules = get(rules);
		const prevKeys = get(ruleUiKeys);
		ruleUiKeys.set(reconcileRuleUiKeys(normalized, prevRules, prevKeys));
		rules.set(normalized);
	}

	function setRuleSetsForUI(next: SingboxRouterRuleSet[]): void {
		ruleSets.set(normalizeRuleSetsForUI(next));
	}

	async function loadAll(): Promise<void> {
		loading.set(true);
		error.set(null);
		try {
			const [r, rs, o, ds, dr, dg] = await Promise.all([
				api.singboxFakeIPListRules(),
				api.singboxFakeIPListRuleSets(),
				api.singboxFakeIPListOutbounds(),
				api.singboxFakeIPListDNSServers(),
				api.singboxFakeIPListDNSRules(),
				api.singboxFakeIPGetDNSGlobals(),
			]);
			setRulesWithKeys(r);
			setRuleSetsForUI(rs);
			outbounds.set(o);
			dnsServers.set(ds);
			dnsRules.set(dr);
			dnsGlobals.set(dg);
		} catch (e) {
			error.set(e instanceof Error ? e.message : 'Не удалось загрузить fakeip-конфиг');
		} finally {
			loading.set(false);
			initialized.set(true);
		}
	}

	function applyRules(data: SingboxRouterRule[]): void {
		setRulesWithKeys(data);
	}

	function applyRuleSets(data: SingboxRouterRuleSet[]): void {
		setRuleSetsForUI(data);
	}

	function applyOutbounds(data: SingboxRouterOutbound[]): void {
		outbounds.set(data);
	}

	function applyDNSServers(data: SingboxRouterDNSServer[]): void {
		dnsServers.set(data);
	}

	function applyDNSRules(data: SingboxRouterDNSRule[]): void {
		dnsRules.set(data);
	}

	function applyDNSGlobals(data: SingboxRouterDNSGlobals): void {
		dnsGlobals.set(data);
	}

	return {
		rules: { subscribe: rules.subscribe },
		ruleUiKeys: { subscribe: ruleUiKeys.subscribe },
		ruleSets: { subscribe: ruleSets.subscribe },
		outbounds: { subscribe: outbounds.subscribe },
		dnsServers: { subscribe: dnsServers.subscribe },
		dnsRules: { subscribe: dnsRules.subscribe },
		dnsGlobals: { subscribe: dnsGlobals.subscribe },
		options: { subscribe: options.subscribe },
		optionsReady: { subscribe: optionsReady.subscribe },
		loading: { subscribe: loading.subscribe },
		initialized: { subscribe: initialized.subscribe },
		error: { subscribe: error.subscribe },
		loadAll,
		applyRules,
		applyRuleSets,
		applyOutbounds,
		applyDNSServers,
		applyDNSRules,
		applyDNSGlobals,
	};
}

export const fakeipConfig = createFakeipConfigStore();
