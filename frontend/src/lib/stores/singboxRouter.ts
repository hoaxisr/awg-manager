import { writable } from 'svelte/store';
import { api } from '$lib/api/client';
import type {
	SingboxRouterStatus,
	SingboxRouterSettings,
	SingboxRouterRule,
	SingboxRouterRuleSet,
	SingboxRouterOutbound,
	SingboxRouterPreset,
	SingboxRouterDNSServer,
	SingboxRouterDNSRule,
	SingboxRouterDNSGlobals,
} from '$lib/types';

function createSingboxRouterStore() {
	const status = writable<SingboxRouterStatus | null>(null);
	const settings = writable<SingboxRouterSettings | null>(null);
	const rules = writable<SingboxRouterRule[]>([]);
	const ruleSets = writable<SingboxRouterRuleSet[]>([]);
	const outbounds = writable<SingboxRouterOutbound[]>([]);
	const presets = writable<SingboxRouterPreset[]>([]);
	const dnsServers = writable<SingboxRouterDNSServer[]>([]);
	const dnsRules = writable<SingboxRouterDNSRule[]>([]);
	const dnsGlobals = writable<SingboxRouterDNSGlobals>({ final: '', strategy: '' });
	const loading = writable(false);
	const error = writable<string | null>(null);

	async function loadAll(): Promise<void> {
		loading.set(true);
		error.set(null);
		try {
			const [st, s, r, rs, o, p, ds, dr, dg] = await Promise.all([
				api.singboxRouterStatus(),
				api.singboxRouterGetSettings(),
				api.singboxRouterListRules(),
				api.singboxRouterListRuleSets(),
				api.singboxRouterListOutbounds(),
				api.singboxRouterListPresets(),
				api.singboxRouterListDNSServers(),
				api.singboxRouterListDNSRules(),
				api.singboxRouterGetDNSGlobals(),
			]);
			status.set(st);
			settings.set(s);
			rules.set(r);
			ruleSets.set(rs);
			outbounds.set(o);
			presets.set(p);
			dnsServers.set(ds);
			dnsRules.set(dr);
			dnsGlobals.set(dg);
		} catch (e) {
			error.set(e instanceof Error ? e.message : 'Не удалось загрузить singbox-router');
		} finally {
			loading.set(false);
		}
	}

	async function reloadStatus(): Promise<void> {
		try {
			status.set(await api.singboxRouterStatus());
		} catch {
			return;
		}
	}

	function applyStatus(data: SingboxRouterStatus): void {
		status.set(data);
	}

	function applyRules(data: SingboxRouterRule[]): void {
		rules.set(data);
	}

	function applyRuleSets(data: SingboxRouterRuleSet[]): void {
		ruleSets.set(data);
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
		status: { subscribe: status.subscribe },
		settings: { subscribe: settings.subscribe },
		rules: { subscribe: rules.subscribe },
		ruleSets: { subscribe: ruleSets.subscribe },
		outbounds: { subscribe: outbounds.subscribe },
		presets: { subscribe: presets.subscribe },
		dnsServers: { subscribe: dnsServers.subscribe },
		dnsRules: { subscribe: dnsRules.subscribe },
		dnsGlobals: { subscribe: dnsGlobals.subscribe },
		loading: { subscribe: loading.subscribe },
		error: { subscribe: error.subscribe },
		loadAll,
		reloadStatus,
		applyStatus,
		applyRules,
		applyRuleSets,
		applyOutbounds,
		applyDNSServers,
		applyDNSRules,
		applyDNSGlobals,
		setSettings: settings.set,
	};
}

export const singboxRouter = createSingboxRouterStore();
