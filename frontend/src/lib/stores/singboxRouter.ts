import { writable, derived } from 'svelte/store';
import { api } from '$lib/api/client';
import { awgTags } from './awgTags';
import { subscriptionsStore } from './subscriptions';
import { singboxTunnels } from './singbox';
import { buildOutboundOptions, type OutboundGroup } from '$lib/components/routing/singboxRouter/outboundOptions';
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
	RouterStagingStatusResponse,
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
	const staging = writable<RouterStagingStatusResponse | null>(null);
	const loading = writable(false);
	const error = writable<string | null>(null);

	// options — unified outbound dropdown groups for sub-tabs and wizard.
	// Combines awgTags + sing-box tunnels + composite outbounds, with
	// subscription labels mixed in for source='subscription' composites.
	// Defensive: components subscribing during cold-load see [] groups.
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

	// optionsReady — true once all PollingStore sources for `options`
	// have completed at least one fetch attempt. Consumers (e.g. wizard
	// auto-skip) gate behavior on this to avoid mis-firing during the
	// brief cold-load window where `outbounds` is populated but the
	// PollingStore-backed sources are still 'idle'/'loading'.
	const optionsReady = derived(
		[singboxTunnels, awgTags, subscriptionsStore],
		([$sb, $awg, $subs]) => {
			const settled = (s: 'idle' | 'loading' | 'fresh' | 'stale' | 'error'): boolean =>
				s === 'fresh' || s === 'stale' || s === 'error';
			return settled($sb.status) && settled($awg.status) && settled($subs.status);
		},
	);

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
		void loadStaging();
	}

	async function loadStaging(): Promise<void> {
		try {
			const data = await api.singboxRouterStagingStatus();
			staging.set(data);
		} catch {
			staging.set(null);
		}
	}

	// Reload the live rule snapshot (rules + rule-sets + outbounds + status)
	// after a staging apply/discard flips the running config.
	async function loadRulesSnapshot(): Promise<void> {
		try {
			const [r, rs, o, st] = await Promise.all([
				api.singboxRouterListRules(),
				api.singboxRouterListRuleSets(),
				api.singboxRouterListOutbounds(),
				api.singboxRouterStatus(),
			]);
			rules.set(r);
			ruleSets.set(rs);
			outbounds.set(o);
			status.set(st);
		} catch {
			// silent — stale data is better than an uncaught error
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
		staging: { subscribe: staging.subscribe } as import('svelte/store').Readable<RouterStagingStatusResponse | null>,
		options: { subscribe: options.subscribe },
		optionsReady: { subscribe: optionsReady.subscribe },
		loading: { subscribe: loading.subscribe },
		error: { subscribe: error.subscribe },
		loadAll,
		reloadStatus,
		loadStaging,
		loadRulesSnapshot,
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
