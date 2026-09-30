import type {
	BypassSetStatus,
	CatalogPreset,
	PolicyTunNATSegmentInfo,
	PolicyTunNATPreview,
	MihomoStatus,
	MihomoNativeList,
	MihomoNativeProxy,
	MihomoNativeSubscription,
	MihomoEnginePreference,
	MihomoSubscriptionFormat,
	MihomoRuntimeProxies,
	MihomoRuntimeProviders,
	MihomoNativeGroup,
	MihomoNativeRule,
	MihomoNativeRuleProvider,
	TelemtStatus,
	XrayStatus,
	XrayConfigRequest,
	RouterPolicy,
	RouterStagingStatusResponse,
	SingboxGeositesData,
	SingboxProxiesListResponse,
	SingboxProxiesSelectRequest,
	SingboxProxiesTestRequest,
	SingboxProxiesTestResponse,
	SingboxRouterDNSChainPreset,
	SingboxRouterDNSGlobals,
	SingboxRouterDNSLookupResult,
	SingboxRouterDNSRewrite,
	SingboxRouterDNSRule,
	SingboxRouterDNSServer,
	SingboxRouterInspectDNSRequest,
	SingboxRouterInspectDNSResult,
	SingboxRouterInspectProgress,
	SingboxRouterInspectRequest,
	SingboxRouterInspectResult,
	SingboxRouterOutbound,
	SingboxRouterPreset,
	SingboxRouterRule,
	SingboxRouterRuleSet,
	SingboxRouterSettings,
	SingboxRouterStatus,
	SingboxRouterWANInterface
} from '$lib/types';
import { sanitizeDnsServerForApi } from '$lib/utils/dnsServerDetour';
import { SingboxClient } from './clientSingbox';

export interface MihomoRuleMutationResponse {
	reordered?: boolean;
	item?: MihomoNativeRule;
	deleted?: boolean;
	items?: MihomoNativeRule[];
	revision?: number;
	generation?: number;
	applyPath?: string;
	transactionId?: string;
}

export class SbRouterClient extends SingboxClient {
	// ─────────────────────────────────────────────
	// #region Sing-box Router (TProxy routing engine)
	// ─────────────────────────────────────────────

	// singboxRouterStatus() already returns the full status (see
	// SingboxRouterStatus) — no separate getFakeipStatus is needed.
	async singboxRouterStatus(): Promise<SingboxRouterStatus> {
		return this.request('/singbox/router/status');
	}

	async singboxRouterSwitchMode(mode: 'off' | 'tproxy' | 'fakeip-tun' | 'policy-tun'): Promise<void> {
		await this.request('/singbox/router/mode', { method: 'POST', body: JSON.stringify({ mode }) });
	}

	/**
	 * Сегменты роутера с текущим режимом NAT — предпоказ «что изменится» за
	 * тумблером source-preserve в policy-tun.
	 */
	async getPolicyTunNATPreview(): Promise<PolicyTunNATPreview> {
		return this.request('/singbox/router/policy-tun/nat-preview');
	}

	async singboxRouterGetSettings(): Promise<SingboxRouterSettings> {
		return this.request('/singbox/router/settings');
	}

	async singboxRouterPutSettings(settings: SingboxRouterSettings): Promise<void> {
		await this.request('/singbox/router/settings', {
			method: 'PUT',
			body: JSON.stringify(settings),
		});
	}

	async singboxRouterListRules(): Promise<SingboxRouterRule[]> {
		return this.request('/singbox/router/rules/list');
	}

	async singboxRouterAddRule(rule: SingboxRouterRule): Promise<void> {
		await this.request('/singbox/router/rules/add', {
			method: 'POST',
			body: JSON.stringify(rule),
		});
	}

	async singboxRouterUpdateRule(index: number, rule: SingboxRouterRule): Promise<void> {
		await this.request('/singbox/router/rules/update', {
			method: 'POST',
			body: JSON.stringify({ index, rule }),
		});
	}

	async singboxRouterDeleteRule(index: number): Promise<void> {
		await this.request('/singbox/router/rules/delete', {
			method: 'POST',
			body: JSON.stringify({ index }),
		});
	}

	async singboxRouterMoveRule(from: number, to: number): Promise<void> {
		await this.request('/singbox/router/rules/move', {
			method: 'POST',
			body: JSON.stringify({ from, to }),
		});
	}

	async singboxRouterBulkOutbound(indices: number[], outbound: string): Promise<{ updated: number }> {
		return this.request('/singbox/router/rules/bulk-outbound', {
			method: 'POST',
			body: JSON.stringify({ indices, outbound }),
		});
	}

	async singboxRouterBulkDetour(tags: string[], downloadDetour: string): Promise<{ updated: number }> {
		return this.request('/singbox/router/rulesets/bulk-detour', {
			method: 'POST',
			body: JSON.stringify({ tags, downloadDetour }),
		});
	}

	async singboxRouterListRuleSets(): Promise<SingboxRouterRuleSet[]> {
		return this.request('/singbox/router/rulesets/list');
	}

	async singboxRouterDatRuleSetURL(kind: 'geosite' | 'geoip', tags: string[]): Promise<{ url: string }> {
		const q = new URLSearchParams({ kind });
		for (const t of tags) {
			q.append('tag', t);
		}
		return this.request(`/singbox/router/rulesets/dat-url?${q.toString()}`);
	}

	async singboxRouterAddRuleSet(rs: SingboxRouterRuleSet): Promise<void> {
		await this.request('/singbox/router/rulesets/add', {
			method: 'POST',
			body: JSON.stringify(rs),
		});
	}

	async singboxRouterUpdateRuleSet(tag: string, rs: SingboxRouterRuleSet): Promise<void> {
		await this.request('/singbox/router/rulesets/update', {
			method: 'POST',
			body: JSON.stringify({ tag, ruleSet: rs }),
		});
	}

	async singboxRouterDeleteRuleSet(tag: string, force = false): Promise<void> {
		await this.request('/singbox/router/rulesets/delete', {
			method: 'POST',
			body: JSON.stringify({ tag, force }),
		});
	}

	async singboxRouterListOutbounds(): Promise<SingboxRouterOutbound[]> {
		return this.request('/singbox/router/outbounds/list');
	}

	async singboxRouterAddOutbound(o: SingboxRouterOutbound): Promise<void> {
		await this.request('/singbox/router/outbounds/add', {
			method: 'POST',
			body: JSON.stringify(o),
		});
	}

	async singboxRouterUpdateOutbound(tag: string, o: SingboxRouterOutbound): Promise<void> {
		await this.request('/singbox/router/outbounds/update', {
			method: 'POST',
			body: JSON.stringify({ tag, outbound: o }),
		});
	}

	async singboxRouterDeleteOutbound(tag: string, force = false): Promise<void> {
		await this.request('/singbox/router/outbounds/delete', {
			method: 'POST',
			body: JSON.stringify({ tag, force }),
		});
	}

	async singboxRouterListProxies(): Promise<SingboxProxiesListResponse> {
		return this.request<SingboxProxiesListResponse>('/singbox/router/proxies/list');
	}

	async singboxRouterListGeosites(refresh = false): Promise<SingboxGeositesData> {
		return this.request<SingboxGeositesData>(
			`/singbox/router/geosites/list${refresh ? '?refresh=1' : ''}`,
		);
	}

	async singboxRouterSelectProxy(req: SingboxProxiesSelectRequest): Promise<void> {
		await this.request<unknown>('/singbox/router/proxies/select', {
			method: 'POST',
			body: JSON.stringify(req),
		});
	}

	async singboxRouterTestProxy(req: SingboxProxiesTestRequest): Promise<SingboxProxiesTestResponse> {
		return this.request<SingboxProxiesTestResponse>('/singbox/router/proxies/test', {
			method: 'POST',
			body: JSON.stringify(req),
		});
	}

	async singboxRouterListPresets(): Promise<SingboxRouterPreset[]> {
		return this.request('/singbox/router/presets/list');
	}

	async listPresets(): Promise<{ presets: CatalogPreset[] }> {
		const payload = await this.request<{ presets?: CatalogPreset[] } | undefined>('/presets');
		return {
			presets: Array.isArray(payload?.presets) ? payload.presets : [],
		};
	}

	async singboxRouterApplyPreset(id: string, outbound: string): Promise<void> {
		await this.request('/singbox/router/presets/apply', {
			method: 'POST',
			body: JSON.stringify({ id, outbound }),
		});
	}

	async singboxRouterListPolicies(): Promise<RouterPolicy[]> {
		return this.request<RouterPolicy[]>('/singbox/router/policies');
	}

	async singboxRouterCreatePolicy(description?: string): Promise<RouterPolicy> {
		return this.request<RouterPolicy>('/singbox/router/policies', {
			method: 'POST',
			body: JSON.stringify({ description: description ?? 'awgm-router' }),
		});
	}

	async singboxRouterListWANInterfaces(): Promise<SingboxRouterWANInterface[]> {
		return this.request<SingboxRouterWANInterface[]>('/singbox/router/wan-interfaces');
	}

	// Включая уже занятые outbound'ами: какие прятать, решает пикер
	// (directBindChoices), подписки и туннели делят их свободно.
	async singboxRouterListBindableInterfaces(): Promise<SingboxRouterWANInterface[]> {
		return this.request<SingboxRouterWANInterface[]>('/singbox/router/bindable-interfaces');
	}

	async singboxRouterListDNSServers(): Promise<SingboxRouterDNSServer[]> {
		return this.request<SingboxRouterDNSServer[]>('/singbox/router/dns/servers/list');
	}

	async singboxRouterLookupDNSServer(server: string, port: number | '', serverName = ''): Promise<SingboxRouterDNSLookupResult> {
		const query = new URLSearchParams({ server });
		if (port !== '') query.set('server_port', String(port));
		if (serverName.trim()) query.set('server_name', serverName.trim());
		return this.request(`/singbox/router/dns/servers/lookup?${query}`);
	}

	async singboxRouterAddDNSServer(server: SingboxRouterDNSServer): Promise<void> {
		const payload = sanitizeDnsServerForApi(server);
		await this.request('/singbox/router/dns/servers/add', {
			method: 'POST',
			body: JSON.stringify(payload),
		});
	}

	async singboxRouterUpdateDNSServer(tag: string, server: SingboxRouterDNSServer): Promise<void> {
		const payload = sanitizeDnsServerForApi(server);
		await this.request('/singbox/router/dns/servers/update', {
			method: 'POST',
			body: JSON.stringify({ tag, server: payload }),
		});
	}

	async singboxRouterDeleteDNSServer(tag: string, force = false): Promise<void> {
		await this.request('/singbox/router/dns/servers/delete', {
			method: 'POST',
			body: JSON.stringify({ tag, force }),
		});
	}

	async singboxRouterListDNSRules(): Promise<SingboxRouterDNSRule[]> {
		return this.request('/singbox/router/dns/rules/list');
	}

	async singboxRouterAddDNSRule(rule: SingboxRouterDNSRule): Promise<void> {
		await this.request('/singbox/router/dns/rules/add', {
			method: 'POST',
			body: JSON.stringify(rule),
		});
	}

	async singboxRouterUpdateDNSRule(index: number, rule: SingboxRouterDNSRule): Promise<void> {
		await this.request('/singbox/router/dns/rules/update', {
			method: 'POST',
			body: JSON.stringify({ index, rule }),
		});
	}

	async singboxRouterDeleteDNSRule(index: number): Promise<void> {
		await this.request('/singbox/router/dns/rules/delete', {
			method: 'POST',
			body: JSON.stringify({ index }),
		});
	}

	async singboxRouterMoveDNSRule(from: number, to: number): Promise<void> {
		await this.request('/singbox/router/dns/rules/move', {
			method: 'POST',
			body: JSON.stringify({ from, to }),
		});
	}

	async singboxRouterMoveDNSServer(from: number, to: number): Promise<void> {
		await this.request('/singbox/router/dns/servers/move', {
			method: 'POST',
			body: JSON.stringify({ from, to }),
		});
	}

	async singboxRouterListDNSRewrites(): Promise<SingboxRouterDNSRewrite[]> {
		return this.request('/singbox/router/dns/rewrites/list');
	}

	async singboxRouterAddDNSRewrite(rewrite: SingboxRouterDNSRewrite): Promise<void> {
		await this.request('/singbox/router/dns/rewrites/add', {
			method: 'POST',
			body: JSON.stringify(rewrite),
		});
	}

	async singboxRouterUpdateDNSRewrite(index: number, rewrite: SingboxRouterDNSRewrite): Promise<void> {
		await this.request('/singbox/router/dns/rewrites/update', {
			method: 'POST',
			body: JSON.stringify({ index, rewrite }),
		});
	}

	async singboxRouterDeleteDNSRewrite(index: number): Promise<void> {
		await this.request('/singbox/router/dns/rewrites/delete', {
			method: 'POST',
			body: JSON.stringify({ index }),
		});
	}

	async singboxRouterMoveDNSRewrite(from: number, to: number): Promise<void> {
		await this.request('/singbox/router/dns/rewrites/move', {
			method: 'POST',
			body: JSON.stringify({ from, to }),
		});
	}

	async singboxRouterGetDNSGlobals(): Promise<SingboxRouterDNSGlobals> {
		return this.request('/singbox/router/dns/globals');
	}

	async singboxRouterPutDNSGlobals(globals: SingboxRouterDNSGlobals): Promise<void> {
		await this.request('/singbox/router/dns/globals', {
			method: 'PUT',
			body: JSON.stringify(globals),
		});
	}

	async singboxRouterGetDNSChainPreset(): Promise<SingboxRouterDNSChainPreset> {
		return this.request('/singbox/router/dns/chain-preset');
	}

	async singboxRouterSetDNSChainPreset(preset: SingboxRouterDNSChainPreset): Promise<void> {
		await this.request('/singbox/router/dns/chain-preset', {
			method: 'POST',
			body: JSON.stringify(preset),
		});
	}

	async singboxRouterPutRouteFinal(final: string): Promise<void> {
		await this.request('/singbox/router/route/final', {
			method: 'POST',
			body: JSON.stringify({ final }),
		});
	}

	async singboxRouterInspectRoute(
		req: SingboxRouterInspectRequest,
	): Promise<SingboxRouterInspectResult> {
		return this.request('/singbox/router/inspect', {
			method: 'POST',
			body: JSON.stringify(req),
		});
	}

	async singboxRouterInspectDNS(
		req: SingboxRouterInspectDNSRequest,
	): Promise<SingboxRouterInspectDNSResult> {
		return this.request('/singbox/router/inspect-dns', {
			method: 'POST',
			body: JSON.stringify(req),
		});
	}

	singboxRouterInspectRouteStream(
		req: SingboxRouterInspectRequest,
		handlers: {
			onProgress: (progress: SingboxRouterInspectProgress) => void;
			onResult: (result: SingboxRouterInspectResult) => void;
			onInspectError: (message: string) => void;
			onError: (message: string) => void;
		},
	): EventSource {
		const qs = new URLSearchParams();
		qs.set('domain', req.domain);
		if (typeof req.port === 'number') qs.set('port', String(req.port));
		if (req.protocol) qs.set('protocol', req.protocol);
		const es = new EventSource(`${this.baseUrl}/singbox/router/inspect/stream?${qs.toString()}`);
		es.addEventListener('progress', (e) => {
			try {
				const payload = JSON.parse((e).data);
				if (payload?.progress) handlers.onProgress(payload.progress as SingboxRouterInspectProgress);
			} catch {}
		});
		es.addEventListener('result', (e) => {
			try {
				const payload = JSON.parse((e).data);
				if (payload?.result) handlers.onResult(payload.result as SingboxRouterInspectResult);
			} catch (err) {
				handlers.onError(err instanceof Error ? err.message : 'Invalid stream result');
			}
			es.close();
		});
		es.addEventListener('inspect-error', (e) => {
			try {
				const payload = JSON.parse((e).data);
				handlers.onInspectError(String(payload?.error ?? 'Inspect failed'));
			} catch {
				handlers.onInspectError('Inspect failed');
			}
			es.close();
		});
		es.addEventListener('error', () => {
			handlers.onError('Stream connection lost');
			es.close();
		});
		return es;
	}

	mihomoRouterInspectRouteStream(
		req: SingboxRouterInspectRequest,
		handlers: {
			onProgress: (progress: SingboxRouterInspectProgress) => void;
			onResult: (result: SingboxRouterInspectResult) => void;
			onInspectError: (message: string) => void;
			onError: (message: string) => void;
		},
	): EventSource {
		const qs = new URLSearchParams();
		qs.set('domain', req.domain);
		if (typeof req.port === 'number') qs.set('port', String(req.port));
		if (req.protocol) qs.set('protocol', req.protocol);
		const es = new EventSource(`${this.baseUrl}/mihomo/router/inspect/stream?${qs.toString()}`);
		es.addEventListener('progress', (e) => {
			try {
				const payload = JSON.parse((e).data);
				if (payload?.progress) handlers.onProgress(payload.progress as SingboxRouterInspectProgress);
			} catch {}
		});
		es.addEventListener('result', (e) => {
			try {
				const payload = JSON.parse((e).data);
				if (payload?.result) handlers.onResult(payload.result as SingboxRouterInspectResult);
			} catch (err) {
				handlers.onError(err instanceof Error ? err.message : 'Invalid stream result');
			}
			es.close();
		});
		es.addEventListener('inspect-error', (e) => {
			try {
				const payload = JSON.parse((e).data);
				handlers.onInspectError(String(payload?.error ?? 'Inspect failed'));
			} catch {
				handlers.onInspectError('Inspect failed');
			}
			es.close();
		});
		es.addEventListener('error', () => {
			handlers.onError('Stream connection lost');
			es.close();
		});
		return es;
	}

	async singboxRouterStagingStatus(): Promise<RouterStagingStatusResponse> {
		return this.request('/singbox/router/staging');
	}

	async singboxRouterStagingApply(): Promise<void> {
		await this.request('/singbox/router/staging/apply', {
			method: 'POST',
		});
	}

	async singboxRouterStagingDiscard(): Promise<void> {
		await this.request('/singbox/router/staging/discard', {
			method: 'POST',
		});
	}

	// Mihomo API. These methods are kept on the shared client inheritance
	// chain because routing, subscriptions and tunnel cards all consume them.
	async mihomoResetConfig(): Promise<{ success: boolean; message?: string }> {
		return this.request('/mihomo/native/reset', { method: 'POST' });
	}

	async mihomoGetClashConfigs(): Promise<{ mode: 'rule' | 'global' | 'direct'; [key: string]: unknown }> {
		return this.request('/mihomo/clash/configs');
	}

	async mihomoPatchClashConfigs(patch: { mode?: 'rule' | 'global' | 'direct' }): Promise<void> {
		await this.request('/mihomo/clash/configs', { method: 'PATCH', body: JSON.stringify(patch) });
	}

	async mihomoGetGlobalProxy(): Promise<{ name: string; now: string; all: string[] }> {
		return this.request('/mihomo/clash/proxies/GLOBAL');
	}

	async mihomoSetGlobalProxy(name: string): Promise<void> {
		await this.request('/mihomo/clash/proxies/GLOBAL', { method: 'PUT', body: JSON.stringify({ name }) });
	}

	async mihomoStatus(): Promise<MihomoStatus> {
		return this.request('/mihomo/status');
	}

	async mihomoInstall(): Promise<MihomoStatus> {
		return this.request('/mihomo/install', { method: 'POST' });
	}

	async mihomoUpdate(): Promise<MihomoStatus> {
		return this.request('/mihomo/update', { method: 'POST' });
	}

	async mihomoUninstall(): Promise<MihomoStatus> {
		return this.request('/mihomo/uninstall', { method: 'POST' });
	}

	async xrayStatus(): Promise<XrayStatus> {
		return this.request('/xray/status');
	}

	async xrayInstall(): Promise<XrayStatus> {
		return this.request('/xray/install', { method: 'POST' });
	}

	async xrayUninstall(): Promise<void> {
		await this.request('/xray/uninstall', { method: 'POST' });
	}

	async mihomoConfig(): Promise<{ yaml: string }> {
		return this.request('/mihomo/config');
	}

	async mihomoReload(): Promise<void> {
		await this.request('/mihomo/reload', { method: 'POST' });
	}

	async mihomoRestart(): Promise<void> {
		await this.request('/mihomo/restart', { method: 'POST' });
	}

	async telemtStatus(): Promise<TelemtStatus> {
		return this.request<TelemtStatus>('/telemt/status');
	}

	async telemtInstall(): Promise<TelemtStatus> {
		return this.request<TelemtStatus>('/telemt/install', { method: 'POST' });
	}

	async telemtUpdate(): Promise<TelemtStatus> {
		return this.request<TelemtStatus>('/telemt/update', { method: 'POST' });
	}

	async telemtRestart(): Promise<TelemtStatus> {
		return this.request<TelemtStatus>('/telemt/restart', { method: 'POST' });
	}

	async telemtUninstall(): Promise<TelemtStatus> {
		return this.request<TelemtStatus>('/telemt/uninstall', { method: 'POST' });
	}

	async mihomoReconcile(action: 'rollback_to_lkg' | 'regenerate_from_desired', force = false): Promise<{ status: string }> {
		return this.request('/mihomo/recovery/reconcile', {
			method: 'POST',
			body: JSON.stringify({ action, force })
		});
	}

	async mihomoRecoveryEvidence(): Promise<Blob> {
		const res = await fetch('/api/mihomo/recovery/evidence', {
			headers: { 'Accept': 'application/json' }
		});
		if (!res.ok) {
			throw new Error(`Failed to download evidence: ${res.statusText}`);
		}
		return res.blob();
	}

	async mihomoNativeProxies(): Promise<MihomoNativeProxy[]> {
		return (await this.request<MihomoNativeList<MihomoNativeProxy>>('/mihomo/native/proxies')).items;
	}

	async mihomoNativeProxy(id: string): Promise<MihomoNativeProxy> {
		return this.request(`/mihomo/native/proxies/${encodeURIComponent(id)}`);
	}

	async mihomoNativeCreateProxy(uri: string, enginePreference: MihomoEnginePreference): Promise<MihomoNativeList<MihomoNativeProxy>> {
		return this.request('/mihomo/native/proxies', { method: 'POST', body: JSON.stringify({ uri, enginePreference }) });
	}

	async mihomoNativeCreateManualProxy(input: {
		name: string; protocol: string; server: string; port: number;
		enginePreference: MihomoEnginePreference; config: Record<string, unknown>;
	}): Promise<MihomoNativeList<MihomoNativeProxy>> {
		const { enginePreference, ...manual } = input;
		return this.request('/mihomo/native/proxies', { method: 'POST', body: JSON.stringify({ enginePreference, manual }) });
	}

	async mihomoNativeUpdateProxy(id: string, input: {
		uri?: string;
		manual?: { name: string; protocol: string; server: string; port: number; config: Record<string, unknown> };
		enginePreference: MihomoEnginePreference; enabled: boolean;
	}): Promise<MihomoNativeProxy> {
		return this.request(`/mihomo/native/proxies/${encodeURIComponent(id)}`, { method: 'PUT', body: JSON.stringify(input) });
	}

	async mihomoNativeDeleteProxy(id: string, apply = true): Promise<void> {
		await this.request(`/mihomo/native/proxies/${encodeURIComponent(id)}${apply ? '' : '?apply=false'}`, { method: 'DELETE' });
	}

	async mihomoNativeSubscriptions(): Promise<MihomoNativeSubscription[]> {
		return (await this.request<MihomoNativeList<MihomoNativeSubscription>>('/mihomo/native/subscriptions')).items;
	}

	async mihomoNativeSubscription(id: string): Promise<MihomoNativeSubscription> {
		return this.request(`/mihomo/native/subscriptions/${encodeURIComponent(id)}`);
	}

	async mihomoNativeCreateSubscription(input: {
		name: string; url?: string; inline?: string; format: MihomoSubscriptionFormat;
		enginePreference: MihomoEnginePreference; refreshHours: number; enabled: boolean;
		headers?: Array<{ name: string; value: string }>;
		mode?: string; testUrl?: string; testInterval?: number; testTolerance?: number;
		filterInclude?: string; filterExclude?: string; bindInterface?: string;
	}): Promise<MihomoNativeSubscription> {
		return this.request('/mihomo/native/subscriptions', { method: 'POST', body: JSON.stringify(input) });
	}

	async mihomoNativeUpdateSubscription(id: string, input: {
		name: string; url?: string; inline?: string; format: MihomoSubscriptionFormat;
		enginePreference: MihomoEnginePreference; refreshHours: number; enabled: boolean;
		headers?: Array<{ name: string; value: string }>;
		mode?: string; testUrl?: string; testInterval?: number; testTolerance?: number;
		filterInclude?: string; filterExclude?: string; bindInterface?: string;
	}): Promise<MihomoNativeSubscription> {
		return this.request(`/mihomo/native/subscriptions/${encodeURIComponent(id)}`, { method: 'PUT', body: JSON.stringify(input) });
	}

	async mihomoNativeDeleteSubscription(id: string, apply = true): Promise<void> {
		await this.request(`/mihomo/native/subscriptions/${encodeURIComponent(id)}${apply ? '' : '?apply=false'}`, { method: 'DELETE' });
	}

	async mihomoNativeRefreshSubscription(id: string): Promise<void> {
		await this.request(`/mihomo/native/subscriptions/${encodeURIComponent(id)}/refresh`, { method: 'POST' });
	}

	async mihomoNativeGroups(): Promise<MihomoNativeGroup[]> {
		return (await this.request<MihomoNativeList<MihomoNativeGroup>>('/mihomo/native/groups')).items;
	}

	async mihomoNativeSaveGroup(group: Partial<MihomoNativeGroup>, apply = true): Promise<MihomoNativeGroup> {
		const suffix = apply ? '' : '?apply=false';
		const path = group.id ? `/mihomo/native/groups/${encodeURIComponent(group.id)}${suffix}` : `/mihomo/native/groups${suffix}`;
		return this.request(path, { method: group.id ? 'PUT' : 'POST', body: JSON.stringify(group) });
	}

	async mihomoNativeDeleteGroup(id: string, apply = true): Promise<void> {
		await this.request(`/mihomo/native/groups/${encodeURIComponent(id)}${apply ? '' : '?apply=false'}`, { method: 'DELETE' });
	}

	async mihomoGroupReferences(id: string): Promise<Array<{ kind: string; id: string; name: string }>> {
		const res = await this.request<{ references: Array<{ kind: string; id: string; name: string }> }>(
			`/mihomo/native/groups/${encodeURIComponent(id)}/references`,
		);
		return res.references ?? [];
	}

	async mihomoSelectProxy(groupName: string, memberName: string): Promise<void> {
		await this.request(`/mihomo/clash/proxies/${encodeURIComponent(groupName)}`, {
			method: 'PUT',
			body: JSON.stringify({ name: memberName }),
		});
	}

	async mihomoProxyDelay(name: string, testUrl = 'https://www.gstatic.com/generate_204', timeout = 5000): Promise<number> {
		return this.mihomoRuntimeDelay(name, testUrl, timeout);
	}

	async mihomoNativeRules(): Promise<MihomoNativeRule[]> {
		const res = await this.request<MihomoNativeList<MihomoNativeRule> & { revision?: number }>('/mihomo/native/rules');
		return res.items;
	}

	async mihomoNativeRulesWithRevision(): Promise<{ items: MihomoNativeRule[]; revision: number }> {
		const res = await this.request<MihomoNativeList<MihomoNativeRule> & { revision?: number }>('/mihomo/native/rules');
		return { items: res.items, revision: res.revision ?? 0 };
	}

	async mihomoNativeSaveRule(rule: Partial<MihomoNativeRule>, apply = true): Promise<MihomoNativeRule> {
		const response = await this.mihomoNativeSaveRuleDetailed(rule, apply);
		if (!response.item) throw new Error('Mihomo rule mutation returned no item');
		return response.item;
	}

	async mihomoNativeSaveRuleDetailed(rule: Partial<MihomoNativeRule>, apply = true): Promise<MihomoRuleMutationResponse> {
		const suffix = apply ? '' : '?apply=false';
		const path = rule.id ? `/mihomo/native/rules/${encodeURIComponent(rule.id)}${suffix}` : `/mihomo/native/rules${suffix}`;
		return this.request<MihomoRuleMutationResponse>(path, { method: rule.id ? 'PUT' : 'POST', body: JSON.stringify(rule) });
	}

	async mihomoNativeDeleteRule(id: string, apply = true): Promise<void> {
		await this.mihomoNativeDeleteRuleDetailed(id, apply);
	}

	async mihomoNativeDeleteRuleDetailed(id: string, apply = true): Promise<MihomoRuleMutationResponse> {
		return this.request<MihomoRuleMutationResponse>(`/mihomo/native/rules/${encodeURIComponent(id)}${apply ? '' : '?apply=false'}`, { method: 'DELETE' });
	}

	async mihomoNativeReorderRules(
		ids: string[],
		baseRevision?: number,
		apply = true,
		signal?: AbortSignal,
		operationId = typeof crypto !== 'undefined' && typeof crypto.randomUUID === 'function'
			? crypto.randomUUID()
			: `reorder-${Date.now()}-${Math.random().toString(16).slice(2)}`,
	): Promise<MihomoRuleMutationResponse> {
		const suffix = apply ? '' : '?apply=false';
		return this.request<MihomoRuleMutationResponse>(`/mihomo/native/rules/order${suffix}`, {
			method: 'PUT',
			body: JSON.stringify({ ids, order: ids, baseRevision, operationId }),
			signal,
		});
	}

	async mihomoNativeUnsupportedRules(): Promise<{ items: MihomoNativeRule[]; revision: string }> {
		return this.request<{ items: MihomoNativeRule[]; revision: string }>('/mihomo/native/rules/unsupported');
	}

	async mihomoNativeDeleteUnsupportedRules(ids: string[], revision: string, apply = true): Promise<{ deleted: boolean; deletedCount: number }> {
		const suffix = apply ? '' : '?apply=false';
		return this.request<{ deleted: boolean; deletedCount: number }>(`/mihomo/native/rules/unsupported/delete${suffix}`, {
			method: 'POST',
			body: JSON.stringify({ ids, revision }),
		});
	}

	async mihomoNativeRuleProviders(): Promise<MihomoNativeRuleProvider[]> {
		return (await this.request<MihomoNativeList<MihomoNativeRuleProvider>>('/mihomo/native/rule-providers')).items;
	}

	async mihomoNativeSaveRuleProvider(provider: Partial<MihomoNativeRuleProvider>, apply = true): Promise<MihomoNativeRuleProvider> {
		const suffix = apply ? '' : '?apply=false';
		const path = provider.id ? `/mihomo/native/rule-providers/${encodeURIComponent(provider.id)}${suffix}` : `/mihomo/native/rule-providers${suffix}`;
		return this.request(path, { method: provider.id ? 'PUT' : 'POST', body: JSON.stringify(provider) });
	}

	async mihomoNativeDeleteRuleProvider(id: string, apply = true): Promise<void> {
		await this.request(`/mihomo/native/rule-providers/${encodeURIComponent(id)}${apply ? '' : '?apply=false'}`, { method: 'DELETE' });
	}

	async mihomoRuntimeProxies(): Promise<MihomoRuntimeProxies> {
		const response = await fetch(`${this.baseUrl}/mihomo/clash/proxies`, { credentials: 'same-origin' });
		if (!response.ok) throw new Error(`Mihomo Clash API: ${response.status}`);
		return response.json();
	}

	async mihomoRuntimeSelect(group: string, proxy: string): Promise<void> {
		const response = await fetch(`${this.baseUrl}/mihomo/clash/proxies/${encodeURIComponent(group)}`, {
			method: 'PUT', credentials: 'same-origin', headers: { 'Content-Type': 'application/json' },
			body: JSON.stringify({ name: proxy }),
		});
		if (!response.ok) throw new Error(`Не удалось переключить группу (${response.status})`);
	}

	async mihomoRuntimeDelay(proxy: string, url = 'https://www.gstatic.com/generate_204', timeout = 5000): Promise<number> {
		const query = new URLSearchParams({ url, timeout: String(timeout) });
		const response = await fetch(`${this.baseUrl}/mihomo/clash/proxies/${encodeURIComponent(proxy)}/delay?${query}`, { credentials: 'same-origin' });
		if (!response.ok) throw new Error(`Mihomo delay test: ${response.status}`);
		const body = await response.json() as { delay?: number };
		return body.delay ?? 0;
	}

	async mihomoRuntimeProviders(): Promise<MihomoRuntimeProviders> {
		const response = await fetch(`${this.baseUrl}/mihomo/clash/providers/proxies`, { credentials: 'same-origin' });
		if (!response.ok) throw new Error(`Mihomo providers: ${response.status}`);
		return response.json();
	}

	async mihomoRuntimeRefreshProvider(name: string): Promise<void> {
		const response = await fetch(`${this.baseUrl}/mihomo/clash/providers/proxies/${encodeURIComponent(name)}`, {
			method: 'PUT', credentials: 'same-origin',
		});
		if (!response.ok) throw new Error(`Mihomo provider refresh: ${response.status}`);
	}

	async mihomoRuntimeProviderHealthcheck(name: string): Promise<void> {
		const response = await fetch(`${this.baseUrl}/mihomo/clash/providers/proxies/${encodeURIComponent(name)}/healthcheck`, {
			credentials: 'same-origin',
		});
		if (!response.ok) throw new Error(`Mihomo provider healthcheck: ${response.status}`);
	}

	async mihomoRuntimeRuleProviders(): Promise<MihomoRuntimeProviders> {
		const response = await fetch(`${this.baseUrl}/mihomo/clash/providers/rules`, { credentials: 'same-origin' });
		if (!response.ok) throw new Error(`Mihomo rule providers: ${response.status}`);
		return response.json();
	}

	async mihomoRuntimeRefreshRuleProvider(name: string): Promise<void> {
		const response = await fetch(`${this.baseUrl}/mihomo/clash/providers/rules/${encodeURIComponent(name)}`, {
			method: 'PUT', credentials: 'same-origin',
		});
		if (!response.ok) throw new Error(`Mihomo rule provider refresh: ${response.status}`);
	}

	// #endregion


	// ─────────────────────────────────────────────
	// #region Geoip bypass set (AWGM-BYPASS)
	// ─────────────────────────────────────────────

	async singboxRouterBypassSetStatus(): Promise<BypassSetStatus> {
		return this.request('/singbox/router/bypass-set/status');
	}

	/** Ставит пакет ipset; отвечает свежим статусом набора. */
	async singboxRouterBypassSetInstallDeps(): Promise<BypassSetStatus> {
		return this.request('/singbox/router/bypass-set/install-deps', { method: 'POST' });
	}

	/** Ставит conntrack-tools; отвечает свежим статусом набора. */
	async singboxRouterBypassSetInstallConntrack(): Promise<BypassSetStatus> {
		return this.request('/singbox/router/bypass-set/install-conntrack', { method: 'POST' });
	}

	// #endregion


	// #region FakeIP config CRUD

	async singboxFakeIPListDNSServers(): Promise<SingboxRouterDNSServer[]> {
		return this.request<SingboxRouterDNSServer[]>('/singbox/fakeip/config/dns/servers/list');
	}

	async singboxFakeIPAddDNSServer(server: SingboxRouterDNSServer): Promise<void> {
		const payload = sanitizeDnsServerForApi(server);
		await this.request('/singbox/fakeip/config/dns/servers/add', {
			method: 'POST',
			body: JSON.stringify(payload),
		});
	}

	async singboxFakeIPUpdateDNSServer(tag: string, server: SingboxRouterDNSServer): Promise<void> {
		const payload = sanitizeDnsServerForApi(server);
		await this.request('/singbox/fakeip/config/dns/servers/update', {
			method: 'POST',
			body: JSON.stringify({ tag, server: payload }),
		});
	}

	async singboxFakeIPDeleteDNSServer(tag: string, force = false): Promise<void> {
		await this.request('/singbox/fakeip/config/dns/servers/delete', {
			method: 'POST',
			body: JSON.stringify({ tag, force }),
		});
	}

	async singboxFakeIPMoveDNSServer(from: number, to: number): Promise<void> {
		await this.request('/singbox/fakeip/config/dns/servers/move', {
			method: 'POST',
			body: JSON.stringify({ from, to }),
		});
	}

	async singboxFakeIPListDNSRules(): Promise<SingboxRouterDNSRule[]> {
		return this.request('/singbox/fakeip/config/dns/rules/list');
	}

	async singboxFakeIPAddDNSRule(rule: SingboxRouterDNSRule): Promise<void> {
		await this.request('/singbox/fakeip/config/dns/rules/add', {
			method: 'POST',
			body: JSON.stringify(rule),
		});
	}

	async singboxFakeIPUpdateDNSRule(index: number, rule: SingboxRouterDNSRule): Promise<void> {
		await this.request('/singbox/fakeip/config/dns/rules/update', {
			method: 'POST',
			body: JSON.stringify({ index, rule }),
		});
	}

	async singboxFakeIPDeleteDNSRule(index: number): Promise<void> {
		await this.request('/singbox/fakeip/config/dns/rules/delete', {
			method: 'POST',
			body: JSON.stringify({ index }),
		});
	}

	async singboxFakeIPMoveDNSRule(from: number, to: number): Promise<void> {
		await this.request('/singbox/fakeip/config/dns/rules/move', {
			method: 'POST',
			body: JSON.stringify({ from, to }),
		});
	}

	async singboxFakeIPGetDNSGlobals(): Promise<SingboxRouterDNSGlobals> {
		return this.request('/singbox/fakeip/config/dns/globals');
	}

	async singboxFakeIPSetDNSGlobals(globals: SingboxRouterDNSGlobals): Promise<void> {
		await this.request('/singbox/fakeip/config/dns/globals', {
			method: 'PUT',
			body: JSON.stringify(globals),
		});
	}

	async singboxFakeIPListRules(): Promise<SingboxRouterRule[]> {
		return this.request('/singbox/fakeip/config/rules/list');
	}

	async singboxFakeIPAddRule(rule: SingboxRouterRule): Promise<void> {
		await this.request('/singbox/fakeip/config/rules/add', {
			method: 'POST',
			body: JSON.stringify(rule),
		});
	}

	async singboxFakeIPUpdateRule(index: number, rule: SingboxRouterRule): Promise<void> {
		await this.request('/singbox/fakeip/config/rules/update', {
			method: 'POST',
			body: JSON.stringify({ index, rule }),
		});
	}

	async singboxFakeIPDeleteRule(index: number): Promise<void> {
		await this.request('/singbox/fakeip/config/rules/delete', {
			method: 'POST',
			body: JSON.stringify({ index }),
		});
	}

	async singboxFakeIPMoveRule(from: number, to: number): Promise<void> {
		await this.request('/singbox/fakeip/config/rules/move', {
			method: 'POST',
			body: JSON.stringify({ from, to }),
		});
	}

	async singboxFakeIPBulkOutbound(indices: number[], outbound: string): Promise<{ updated: number }> {
		return this.request('/singbox/fakeip/config/rules/bulk-outbound', {
			method: 'POST',
			body: JSON.stringify({ indices, outbound }),
		});
	}

	async singboxFakeIPBulkDetour(tags: string[], downloadDetour: string): Promise<{ updated: number }> {
		return this.request('/singbox/fakeip/config/rulesets/bulk-detour', {
			method: 'POST',
			body: JSON.stringify({ tags, downloadDetour }),
		});
	}

	async singboxFakeIPSetRouteFinal(final: string): Promise<void> {
		await this.request('/singbox/fakeip/config/route/final', {
			method: 'POST',
			body: JSON.stringify({ final }),
		});
	}

	async singboxFakeIPListRuleSets(): Promise<SingboxRouterRuleSet[]> {
		return this.request('/singbox/fakeip/config/rulesets/list');
	}

	async singboxFakeIPAddRuleSet(rs: SingboxRouterRuleSet): Promise<void> {
		await this.request('/singbox/fakeip/config/rulesets/add', {
			method: 'POST',
			body: JSON.stringify(rs),
		});
	}

	async singboxFakeIPUpdateRuleSet(tag: string, rs: SingboxRouterRuleSet): Promise<void> {
		await this.request('/singbox/fakeip/config/rulesets/update', {
			method: 'POST',
			body: JSON.stringify({ tag, ruleSet: rs }),
		});
	}

	async singboxFakeIPDeleteRuleSet(tag: string, force = false): Promise<void> {
		await this.request('/singbox/fakeip/config/rulesets/delete', {
			method: 'POST',
			body: JSON.stringify({ tag, force }),
		});
	}

	async singboxFakeIPListOutbounds(): Promise<SingboxRouterOutbound[]> {
		return this.request('/singbox/fakeip/config/outbounds/list');
	}

	async singboxFakeIPAddOutbound(o: SingboxRouterOutbound): Promise<void> {
		await this.request('/singbox/fakeip/config/outbounds/add', {
			method: 'POST',
			body: JSON.stringify(o),
		});
	}

	async singboxFakeIPUpdateOutbound(tag: string, o: SingboxRouterOutbound): Promise<void> {
		await this.request('/singbox/fakeip/config/outbounds/update', {
			method: 'POST',
			body: JSON.stringify({ tag, outbound: o }),
		});
	}

	async singboxFakeIPDeleteOutbound(tag: string, force = false): Promise<void> {
		await this.request('/singbox/fakeip/config/outbounds/delete', {
			method: 'POST',
			body: JSON.stringify({ tag, force }),
		});
	}

	// #endregion

}
