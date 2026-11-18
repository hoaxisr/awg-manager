import { TelemtClient } from './clientTelemt';
import {
	stripFircEmojis,
	type FircCaptureRes,
	type FircCatalog,
	type FircDnsStatsRes,
	type FircEnginePurgeResult,
	type FircEventsRes,
	type FircGroup,
	type FircGroupIfaceState,
	type FircGroupLive,
	type FircGroupResolver,
	type FircGroupsStateRes,
	type FircHost,
	type FircInterfaceOption,
	type FircListPreviewRes,
	type FircListRulesPage,
	type FircMigrationOptions,
	type FircMigrationOverview,
	type FircMigrationSummary,
	type FircMigrationWizardState,
	type FircReverseMigrationOptions,
	type FircReverseMigrationSummary,
	type FircSavedSingboxRestoreResult,
	type FircSnapshotMeta,
	type FircPolicy,
	type FircSettingsRes,
	type FircSettingValue,
	type FircStatus,
	type FircSystemResolver,
	type FircTunnel,
	type FircTunnelSource,
	type FircTunnelPreview,
	type FircTunnelProbeRow,
	type FircTunnelState,
	type FircExtConfig,
	type FircMonitorSnapshot,
	type FircDomainCheckResult,
	type FircAutolistStatus,
	type FircAutolistProbes,
	type FircInstallStatus,
} from '$lib/types/firc';

export class FircClient extends TelemtClient {
	protected async requestV1<T>(subPath: string, options: RequestInit = {}): Promise<T> {
		const url = `${this.baseUrl}/firc/v1${subPath}`;
		let response: Response;
		try {
			response = await fetch(url, {
				...options,
				credentials: 'same-origin',
				headers: {
					'Content-Type': 'application/json',
					...options.headers,
				},
			});
		} catch (e) {
			if (e instanceof DOMException && e.name === 'AbortError') throw e;
			throw new Error('Ошибка сети при обращении к fircd');
		}

		if (response.status === 401) {
			this.onUnauthorized?.();
			throw new Error('Сессия истекла');
		}

		if (response.status === 204) {
			return undefined as T;
		}

		const text = await response.text();
		let parsed: unknown = null;
		if (text) {
			try {
				parsed = JSON.parse(text);
			} catch {
				parsed = null;
			}
		}

		if (!response.ok) {
			const obj = (parsed && typeof parsed === 'object' ? parsed : {}) as {
				error?: string;
				message?: string;
				field?: string;
				group?: string;
				tunnel?: string;
			};
			const msg = obj.error || obj.message || text || `Ошибка fircd (${response.status})`;
			const err: Error & {
				status?: number;
				field?: string;
				group?: string;
				tunnel?: string;
			} = new Error(msg);
			err.status = response.status;
			err.field = obj.field;
			err.group = obj.group;
			err.tunnel = obj.tunnel;
			throw err;
		}

		return parsed as T;
	}

	async fircGetStatus(): Promise<FircStatus> {
		return this.request<FircStatus>('/firc/status');
	}

	async fircStart(): Promise<FircStatus> {
		return this.request<FircStatus>('/firc/control', {
			method: 'POST',
			body: JSON.stringify({ action: 'start' }),
		});
	}

	async fircStop(): Promise<FircStatus> {
		return this.request<FircStatus>('/firc/control', {
			method: 'POST',
			body: JSON.stringify({ action: 'stop' }),
		});
	}

	async fircRestart(): Promise<FircStatus> {
		return this.request<FircStatus>('/firc/control', {
			method: 'POST',
			body: JSON.stringify({ action: 'reload' }),
		});
	}

	async fircGetInstallStatus(): Promise<FircInstallStatus> {
		return this.request<FircInstallStatus>('/firc/install-status');
	}

	async fircInstall(): Promise<{ ok: boolean }> {
		return this.request<{ ok: boolean }>('/firc/install', {
			method: 'POST',
			body: JSON.stringify({}),
		});
	}

	async fircMigrateRuntime(): Promise<{ ok: boolean }> {
		return this.request<{ ok: boolean }>('/firc/migrate-runtime', {
			method: 'POST',
			body: JSON.stringify({}),
		});
	}

	async fircUninstall(purgeConfigs: boolean = false): Promise<{ ok: boolean }> {
		return this.request<{ ok: boolean }>('/firc/uninstall', {
			method: 'POST',
			body: JSON.stringify({ purgeConfigs }),
		});
	}

	async fircReconcileRecovery(engine: string = 'firc'): Promise<unknown> {
		return this.request('/firc/recovery/reconcile', {
			method: 'POST',
			body: JSON.stringify({ engine }),
		});
	}

	async fircClearRecovery(): Promise<unknown> {
		return this.request('/firc/control', {
			method: 'POST',
			body: JSON.stringify({ action: 'reconcile_recovery', engine: 'firc' }),
		});
	}

	// ── Tunnels (v1 native daemon + fallback) ──

	async fircGetTunnels(): Promise<FircTunnel[]> {
		try {
			const res = await this.requestV1<{ tunnels?: FircTunnel[] }>('/tunnels');
			return (res?.tunnels ?? []).map((t) => ({
				...t,
				description: stripFircEmojis(t.description),
				sources: (t.sources ?? []).map((s) => ({
					...s,
					name: s.name ? stripFircEmojis(s.name) : s.name,
				})),
			}));
		} catch {
			const res = await this.request<{ tunnels?: FircTunnel[] } | FircTunnel[]>('/firc/tunnels');
			const list = Array.isArray(res) ? res : (res?.tunnels ?? []);
			return list;
		}
	}

	async fircSaveTunnels(
		tunnels: FircTunnel[],
	): Promise<{ tunnels: FircTunnel[]; restarted?: string[]; updated?: string[] }> {
		return this.requestV1<{ tunnels: FircTunnel[]; restarted?: string[]; updated?: string[] }>(
			'/tunnels',
			{
				method: 'PUT',
				body: JSON.stringify({ tunnels }),
			},
		);
	}

	async fircGetTunnelsState(): Promise<FircTunnelState[]> {
		const res = await this.requestV1<{ tunnels?: FircTunnelState[] }>('/tunnels/state');
		return (res?.tunnels ?? []).map((st) => ({
			...st,
			active: (st.active ?? []).map(stripFircEmojis),
			nodes: (st.nodes ?? []).map((n) => ({
				...n,
				name: stripFircEmojis(n.name),
				source: stripFircEmojis(n.source),
			})),
			subscriptions: (st.subscriptions ?? []).map((sub) => ({
				...sub,
				name: stripFircEmojis(sub.name),
			})),
		}));
	}

	async fircRefreshTunnel(id: string): Promise<{ queued?: boolean }> {
		return this.requestV1<{ queued?: boolean }>(
			`/tunnels/${encodeURIComponent(id)}/refresh`,
			{ method: 'POST', body: '{}' },
		);
	}

	async fircRestartTunnel(id: string): Promise<void> {
		await this.requestV1(`/tunnels/${encodeURIComponent(id)}/restart`, {
			method: 'POST',
			body: '{}',
		});
	}

	async fircPreviewTunnel(tunnel: FircTunnel, signal?: AbortSignal): Promise<FircTunnelPreview> {
		const res = await this.requestV1<FircTunnelPreview>('/tunnels/preview', {
			method: 'POST',
			body: JSON.stringify({ tunnel }),
			signal,
		});
		return {
			total: res?.total ?? 0,
			matched: res?.matched ?? 0,
			subscriptions: res?.subscriptions ?? [],
			nodes: (res?.nodes ?? []).map((n) => ({
				...n,
				name: stripFircEmojis(n.name),
				source: stripFircEmojis(n.source),
			})),
		};
	}

	async fircNormalizeSource(payload: {
		input: string;
		name?: string;
		interval?: number;
		headers?: Record<string, string>;
	}): Promise<{ sources: FircTunnelSource[] }> {
		return this.request<{ sources: FircTunnelSource[] }>('/firc/tunnels/normalize-source', {
			method: 'POST',
			body: JSON.stringify(payload),
		});
	}

	private fircProbeQueue: Promise<unknown> = Promise.resolve();

	async fircProbeTunnel(payload: { id?: string; tunnel?: FircTunnel }): Promise<FircTunnelProbeRow[]> {
		const run = async (): Promise<FircTunnelProbeRow[]> => {
			for (let attempt = 0; attempt < 4; attempt++) {
				try {
					const res = await this.requestV1<{ nodes?: FircTunnelProbeRow[] }>('/tunnels/probe', {
						method: 'POST',
						body: JSON.stringify(payload),
					});
					return res?.nodes ?? [];
				} catch (err) {
					const msg = err instanceof Error ? err.message.toLowerCase() : '';
					if (msg.includes('already running') && attempt < 3) {
						await new Promise((r) => setTimeout(r, 800 * (attempt + 1)));
						continue;
					}
					throw err;
				}
			}
			return [];
		};
		const next = this.fircProbeQueue.then(run, run);
		this.fircProbeQueue = next.catch(() => {});
		return next;
	}

	// ── Groups & Rules (v1 native daemon) ──

	async fircGetGroups(): Promise<FircGroup[]> {
		try {
			const res = await this.requestV1<{ groups?: FircGroup[] }>('/groups?with_rules=true');
			return res?.groups ?? [];
		} catch {
			const res = await this.request<{ groups?: FircGroup[] } | FircGroup[]>('/firc/groups');
			if (Array.isArray(res)) return res;
			return res?.groups ?? [];
		}
	}

	async fircSaveGroups(
		groups: FircGroup[],
		save = true,
	): Promise<{ groups: FircGroup[] }> {
		return this.requestV1<{ groups: FircGroup[] }>(`/groups?save=${save ? 'true' : 'false'}`, {
			method: 'PUT',
			body: JSON.stringify({ groups }),
		});
	}

	async fircGetGroupsState(): Promise<FircGroupsStateRes> {
		const [groupsRes, nfRes] = await Promise.all([
			this.requestV1<{
				groups?: Array<{
					id: string;
					live?: boolean;
					liveReason?: string;
					active?: string;
					interfaces?: FircGroupIfaceState[];
					resolver?: FircGroupResolver;
					list?: FircGroup['list'];
				}>;
			}>('/groups'),
			this.requestV1<{
				ok?: boolean;
				firstWritePending?: boolean;
				error?: string;
				since?: number;
			}>('/system/netfilter').catch(
				(): { ok?: boolean; firstWritePending?: boolean; error?: string; since?: number } => ({
					ok: true,
				}),
			),
		]);
		const map: Record<string, FircGroupLive> = {};
		for (const g of groupsRes?.groups ?? []) {
			map[g.id] = {
				live: g.live !== false,
				active: g.active,
				reason: g.liveReason,
				interfaces: g.interfaces,
				resolver: g.resolver,
				list: g.list ?? null,
			};
		}
		return {
			groups: map,
			netfilter: {
				dirty: Boolean(nfRes?.firstWritePending || nfRes?.error),
				since: nfRes?.since,
				error: nfRes?.error,
			},
		};
	}

	async fircMoveUpGroup(groupId: string): Promise<void> {
		await this.requestV1(`/groups/${encodeURIComponent(groupId)}/move-up`, {
			method: 'POST',
			body: '{}',
		});
	}

	async fircSyncGroupList(
		groupId: string,
		url?: string,
	): Promise<{ list?: { url?: string; sync?: FircGroup['list'] } }> {
		return this.requestV1(`/groups/${encodeURIComponent(groupId)}/list/sync`, {
			method: 'POST',
			body: JSON.stringify(url ? { url } : {}),
		});
	}

	async fircGetGroupListRules(
		groupId: string,
		offset = 0,
		limit = 50,
		q = '',
	): Promise<FircListRulesPage> {
		const qParam = q.trim() ? `&q=${encodeURIComponent(q.trim())}` : '';
		return this.requestV1<FircListRulesPage>(
			`/groups/${encodeURIComponent(groupId)}/list/rules?offset=${offset}&limit=${limit}${qParam}`,
		);
	}

	async fircUpdateGroupListRules(
		groupId: string,
		rules: { id: string; type?: string; enable?: boolean }[],
		save = true,
	): Promise<void> {
		await this.requestV1(
			`/groups/${encodeURIComponent(groupId)}/list/rules?save=${save ? 'true' : 'false'}`,
			{
				method: 'PATCH',
				body: JSON.stringify({ rules }),
			},
		);
	}

	async fircGetListCatalog(): Promise<FircCatalog> {
		return this.requestV1<FircCatalog>('/groups/list/catalog');
	}

	async fircPreviewGroupList(url: string): Promise<FircListPreviewRes> {
		return this.requestV1<FircListPreviewRes>(
			`/groups/list/preview?url=${encodeURIComponent(url.trim())}`,
		);
	}

	// ── System Metadata (interfaces, hosts, policies, resolvers) ──

	async fircGetInterfaces(): Promise<FircInterfaceOption[]> {
		const res = await this.requestV1<{ interfaces?: FircInterfaceOption[] }>('/system/interfaces');
		return (res?.interfaces ?? []).map((i) => ({
			...i,
			name: i.name ? stripFircEmojis(i.name) : i.name,
		}));
	}

	async fircGetHosts(): Promise<FircHost[]> {
		const res = await this.requestV1<{ hosts?: FircHost[] }>('/system/hosts');
		return res?.hosts ?? [];
	}

	async fircGetPolicies(): Promise<FircPolicy[]> {
		const res = await this.requestV1<{ policies?: FircPolicy[] }>('/system/policies');
		return res?.policies ?? [];
	}

	async fircGetResolvers(): Promise<FircSystemResolver[]> {
		const res = await this.requestV1<{ resolvers?: FircSystemResolver[] }>('/system/resolvers');
		return res?.resolvers ?? [];
	}

	// ── Daemon Settings (v1 /system/settings) ──

	async fircGetDaemonSettings(): Promise<FircSettingsRes> {
		return this.requestV1<FircSettingsRes>('/system/settings');
	}

	async fircSaveDaemonSettings(
		settings: Record<string, FircSettingValue>,
	): Promise<{ pendingRestart: string[] }> {
		return this.requestV1<{ pendingRestart: string[] }>('/system/settings', {
			method: 'PUT',
			body: JSON.stringify({ settings }),
		});
	}

	async fircRestartDaemonV1(): Promise<void> {
		await this.requestV1('/system/restart', { method: 'POST' });
	}

	// ── Journal, DNS Stats & Capture (v1 /system/*) ──

	async fircGetEvents(since = 0): Promise<FircEventsRes> {
		let cur = since;
		let boot = '';
		let dropped = 0;
		let next = since;
		let allEvents: FircEventsRes['events'] = [];
		for (let page = 0; page < 16; page++) {
			const res = await this.requestV1<FircEventsRes>(`/system/events?since=${cur}`);
			boot = res?.boot ?? boot;
			dropped += res?.dropped ?? 0;
			next = res?.next ?? cur;
			const batch = (res?.events ?? []).map((ev) =>
				ev.kind === 'log' ? { ...ev, message: stripFircEmojis(ev.message) } : ev,
			);
			allEvents = allEvents.concat(batch);
			if (allEvents.length > 1000) {
				allEvents = allEvents.slice(-1000);
			}
			if (batch.length < 1024 || next <= cur) break;
			cur = next;
		}
		return {
			boot,
			dropped,
			next,
			events: allEvents,
		};
	}

	async fircGetDnsStats(params?: {
		limit?: number;
		device?: string;
		q?: string;
		arpa?: boolean;
	}): Promise<FircDnsStatsRes> {
		const sp = new URLSearchParams({ limit: String(params?.limit ?? 500) });
		if (params?.device) sp.set('device', params.device);
		if (params?.q?.trim()) sp.set('q', params.q.trim());
		if (params?.arpa) sp.set('arpa', '1');
		return this.requestV1<FircDnsStatsRes>(`/system/dnsstats?${sp}`);
	}

	async fircGetCapture(): Promise<FircCaptureRes> {
		return this.requestV1<FircCaptureRes>('/system/capture');
	}

	async fircStartCapture(): Promise<FircCaptureRes> {
		return this.requestV1<FircCaptureRes>('/system/capture', { method: 'POST' });
	}

	async fircStopCapture(token: string): Promise<FircCaptureRes> {
		return this.requestV1<FircCaptureRes>(
			`/system/capture?token=${encodeURIComponent(token)}`,
			{ method: 'DELETE' },
		);
	}

	// ── AWG Manager FIRC Engine & Migration ──

	async fircMigrateFromSingbox(
		dryRun = false,
		options?: FircMigrationOptions,
	): Promise<FircMigrationSummary> {
		const query = dryRun ? '?dry_run=1' : '';
		return this.request<FircMigrationSummary>(`/firc/migrate${query}`, {
			method: 'POST',
			body: JSON.stringify({
				dry_run: dryRun,
				dryRun,
				...(options ?? {}),
			}),
		});
	}

	async fircGetMigrationState(): Promise<FircMigrationOverview> {
		return this.request<FircMigrationOverview>('/firc/migrate/state');
	}

	async fircUpdateMigrationState(payload: {
		dismissedAutoPopup?: boolean;
	}): Promise<FircMigrationWizardState> {
		return this.request<FircMigrationWizardState>('/firc/migrate/state', {
			method: 'PUT',
			body: JSON.stringify(payload),
		});
	}

	async fircReverseMigrateToSingbox(
		dryRun = false,
		options?: FircReverseMigrationOptions,
	): Promise<FircReverseMigrationSummary> {
		const query = dryRun ? '?dry_run=1' : '';
		return this.request<FircReverseMigrationSummary>(`/firc/migrate/reverse${query}`, {
			method: 'POST',
			body: JSON.stringify({
				dry_run: dryRun,
				dryRun,
				...(options ?? {}),
			}),
		});
	}

	async fircReturnToSavedSingbox(options?: {
		createSnapshot?: boolean;
		postFircAction?: 'switch_to_singbox' | 'switch_and_stop_firc' | 'switch_and_uninstall_firc' | 'keep_current';
	}): Promise<FircSavedSingboxRestoreResult> {
		return this.request<FircSavedSingboxRestoreResult>('/firc/migrate/return-saved-singbox', {
			method: 'POST',
			body: JSON.stringify(options ?? {}),
		});
	}

	async fircPurgeEngine(engine: 'sing-box' | 'firc'): Promise<FircEnginePurgeResult> {
		return this.request<FircEnginePurgeResult>('/firc/engine/purge', {
			method: 'POST',
			body: JSON.stringify({ engine }),
		});
	}

	async fircListSnapshots(): Promise<FircSnapshotMeta[]> {
		const res = await this.request<{ snapshots?: FircSnapshotMeta[] }>('/firc/snapshots');
		return res?.snapshots ?? [];
	}

	async fircCreateSnapshot(label?: string): Promise<FircSnapshotMeta> {
		return this.request<FircSnapshotMeta>('/firc/snapshots', {
			method: 'POST',
			body: JSON.stringify({ label: label ?? '' }),
		});
	}

	async fircRestoreSnapshot(id: string, switchEngine = true): Promise<FircSnapshotMeta> {
		return this.request<FircSnapshotMeta>('/firc/snapshots/restore', {
			method: 'POST',
			body: JSON.stringify({ id, switchEngine }),
		});
	}

	async fircDeleteSnapshot(id: string): Promise<{ deleted: boolean }> {
		return this.request<{ deleted: boolean }>(`/firc/snapshots?id=${encodeURIComponent(id)}`, {
			method: 'DELETE',
		});
	}

	async fircUpdateSettings(payload: {
		routingEngine?: string;
		inboundPort?: number;
	}): Promise<FircStatus> {
		return this.request<FircStatus>('/firc/settings', {
			method: 'PUT',
			body: JSON.stringify(payload),
		});
	}

	async fircGetExt(): Promise<FircExtConfig> {
		return this.request<FircExtConfig>('/firc/ext');
	}

	async fircSaveExt(ext: FircExtConfig): Promise<FircExtConfig> {
		return this.request<FircExtConfig>('/firc/ext', {
			method: 'PUT',
			body: JSON.stringify(ext),
		});
	}

	async fircGetMonitor(): Promise<FircMonitorSnapshot> {
		return this.request<FircMonitorSnapshot>('/firc/monitor');
	}

	async fircCheckDomain(domain: string): Promise<FircDomainCheckResult> {
		return this.request<FircDomainCheckResult>('/firc/monitor/check', {
			method: 'POST',
			body: JSON.stringify({ domain }),
		});
	}

	async fircGetAutolist(): Promise<FircAutolistStatus> {
		try {
			return await this.request<FircAutolistStatus>('/firc/autolist');
		} catch {
			return this.requestV1<FircAutolistStatus>('/system/autolist');
		}
	}

	async fircConfigureAutolist(payload: {
		enabled: boolean;
		groupId?: string;
	}): Promise<FircAutolistStatus> {
		return this.request<FircAutolistStatus>('/firc/autolist/config', {
			method: 'PUT',
			body: JSON.stringify(payload),
		});
	}

	async fircRecheckAutolist(): Promise<{ ok: boolean }> {
		return this.request<{ ok: boolean }>('/firc/autolist/check', {
			method: 'POST',
			body: '{}',
		});
	}

	async fircProbeAutolistHost(host: string): Promise<FircAutolistProbes> {
		return this.request<FircAutolistProbes>('/firc/autolist/probe', {
			method: 'POST',
			body: JSON.stringify({ host: host.trim() }),
		});
	}

	async fircAutolistCandidateAction(payload: {
		domain: string;
		action: 'add' | 'dismiss';
		groupId?: string;
		confirmed?: boolean;
	}): Promise<{ ok: boolean; domain: string; groupId?: string; groups?: FircGroup[] }> {
		return this.request<{ ok: boolean; domain: string; groupId?: string; groups?: FircGroup[] }>(
			'/firc/autolist/candidate',
			{
				method: 'POST',
				body: JSON.stringify(payload),
			},
		);
	}

	async fircDismissAutolistAddress(payload: {
		address: string;
		proto: string;
		port: number;
	}): Promise<{ ok: boolean }> {
		return this.request<{ ok: boolean }>('/firc/autolist/address/dismiss', {
			method: 'POST',
			body: JSON.stringify(payload),
		});
	}
}
