import { writable, derived, get } from 'svelte/store';
import { api } from '$lib/api/client';
import { awgTags } from './awgTags';
import { subscriptionsStore } from './subscriptions';
import { singboxTunnels } from './singbox';
import { buildOutboundOptions, type OutboundGroup } from '$lib/components/routing/singboxRouter/outboundOptions';
import { reconcileRuleUiKeys } from '$lib/utils/ruleUiKeys';
import {
	normalizeRulesForUI,
	normalizeRuleSetsForUI,
} from '$lib/utils/singboxInlineRules';
import type {
	SingboxRouterStatus,
	SingboxRouterSettings,
	SingboxRouterRule,
	SingboxRouterRuleSet,
	SingboxRouterOutbound,
	SingboxRouterPreset,
	SingboxRouterDNSServer,
	SingboxRouterDNSRule,
	SingboxRouterDNSRewrite,
	SingboxRouterDNSGlobals,
	RouterStagingStatusResponse,
} from '$lib/types';

// Экспортируется ради тестов: они проверяют поведение loadAll на чистом сторе,
// а синглтон переживает весь файл тестов.
export function createSingboxRouterStore() {
	const status = writable<SingboxRouterStatus | null>(null);
	const settings = writable<SingboxRouterSettings | null>(null);
	const rules = writable<SingboxRouterRule[]>([]);
	const ruleUiKeys = writable<string[]>([]);
	const ruleSets = writable<SingboxRouterRuleSet[]>([]);
	const outbounds = writable<SingboxRouterOutbound[]>([]);
	const presets = writable<SingboxRouterPreset[]>([]);
	const dnsServers = writable<SingboxRouterDNSServer[]>([]);
	const dnsRules = writable<SingboxRouterDNSRule[]>([]);
	const dnsRewrites = writable<SingboxRouterDNSRewrite[]>([]);
	const dnsGlobals = writable<SingboxRouterDNSGlobals>({ final: '', strategy: '' });
	const staging = writable<RouterStagingStatusResponse | null>(null);
	const loading = writable(false);
	// Два разных вопроса, поэтому два флага:
	//   initialized — данные УСПЕШНО загружены (гейт повторной загрузки на mount);
	//   attempted   — попытка загрузки завершена, чем угодно (гейт скелетона/спиннера).
	// Слить их в один нельзя: «успех» обязан пускать повторный запрос после
	// неудачи (кнопки «повторить» на табах нет), а спиннер обязан отпускать даже
	// на неудаче — иначе упавший бэкенд крутил бы его вечно.
	const initialized = writable(false);
	const attempted = writable(false);
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
			const [st, s, r, rs, o, p, ds, dr, drw, dg] = await Promise.all([
				api.singboxRouterStatus(),
				api.singboxRouterGetSettings(),
				api.singboxRouterListRules(),
				api.singboxRouterListRuleSets(),
				api.singboxRouterListOutbounds(),
				api.singboxRouterListPresets(),
				api.singboxRouterListDNSServers(),
				api.singboxRouterListDNSRules(),
				api.singboxRouterListDNSRewrites(),
				api.singboxRouterGetDNSGlobals(),
			]);
			status.set(st);
			settings.set(s);
			setRulesWithKeys(r);
			setRuleSetsForUI(rs);
			outbounds.set(o);
			presets.set(p);
			dnsServers.set(ds);
			dnsRules.set(dr);
			dnsRewrites.set(drw);
			dnsGlobals.set(dg);
			initialized.set(true);
		} catch (e) {
			error.set(e instanceof Error ? e.message : 'Не удалось загрузить singbox-router');
		} finally {
			loading.set(false);
			attempted.set(true);
		}
		void loadStaging();
	}

	// loadOnce — холодная загрузка для onMount. Гейта `if (!initialized)` мало:
	// на входе на страницу FakeIP монтируются ДВА компонента сразу (каркас
	// FakeIPTab и активный таб), обе onMount выполняются в одном флаше, а
	// initialized поднимается только после await — то есть оба видят false и
	// шлют по полному кругу запросов (в нём GetStatus с iptables-пробой и RCI).
	// Здесь дедуп по in-flight обещанию.
	//
	// Мутации ЭТИМ не пользуются: им нужен свежий круг ПОСЛЕ записи, а не
	// подписка на круг, начатый до неё. Они и дальше зовут loadAll напрямую.
	let inFlight: Promise<void> | null = null;
	function loadOnce(): Promise<void> {
		if (get(initialized)) return Promise.resolve();
		if (!inFlight) {
			inFlight = loadAll().finally(() => {
				inFlight = null;
			});
		}
		return inFlight;
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
			setRulesWithKeys(r);
			setRuleSetsForUI(rs);
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

	// reloadSettings is the settings counterpart of reloadStatus: a light
	// primer for consumers that need routingMode before any sing-box tab has
	// run loadAll (issue #420 — the tab bar mutes the dormant TProxy/FakeIP
	// chip from `enabled && routingMode`, so both must be primed on page load).
	async function reloadSettings(): Promise<void> {
		try {
			settings.set(await api.singboxRouterGetSettings());
		} catch {
			return;
		}
	}

	function applyStatus(data: SingboxRouterStatus): void {
		status.set(data);
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

	function applyDNSRewrites(data: SingboxRouterDNSRewrite[]): void {
		dnsRewrites.set(data);
	}

	function applyDNSGlobals(data: SingboxRouterDNSGlobals): void {
		dnsGlobals.set(data);
	}

	return {
		status: { subscribe: status.subscribe },
		settings: { subscribe: settings.subscribe },
		rules: { subscribe: rules.subscribe },
		ruleUiKeys: { subscribe: ruleUiKeys.subscribe },
		ruleSets: { subscribe: ruleSets.subscribe },
		outbounds: { subscribe: outbounds.subscribe },
		presets: { subscribe: presets.subscribe },
		dnsServers: { subscribe: dnsServers.subscribe },
		dnsRules: { subscribe: dnsRules.subscribe },
		dnsRewrites: { subscribe: dnsRewrites.subscribe },
		dnsGlobals: { subscribe: dnsGlobals.subscribe },
		staging: { subscribe: staging.subscribe } as import('svelte/store').Readable<RouterStagingStatusResponse | null>,
		options: { subscribe: options.subscribe },
		optionsReady: { subscribe: optionsReady.subscribe },
		loading: { subscribe: loading.subscribe },
		initialized: { subscribe: initialized.subscribe },
		attempted: { subscribe: attempted.subscribe },
		error: { subscribe: error.subscribe },
		loadAll,
		loadOnce,
		reloadStatus,
		reloadSettings,
		loadStaging,
		loadRulesSnapshot,
		applyStatus,
		applyRules,
		applyRuleSets,
		applyOutbounds,
		applyDNSServers,
		applyDNSRules,
		applyDNSRewrites,
		applyDNSGlobals,
		setSettings: settings.set,
	};
}

export const singboxRouter = createSingboxRouterStore();
