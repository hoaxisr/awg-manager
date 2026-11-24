<script lang="ts">
	import { onMount, onDestroy, untrack } from 'svelte';
	import { m, uiText, type UiText } from '$lib/i18n';
	import { api } from '$lib/api/client';
	import { startVisiblePoll } from '$lib/utils/visiblePoll';
	import type { FircHost, FircMonitorSnapshot, FircPolicy } from '$lib/types/firc';
	import type { PageScanFetchMode, PageScanResult } from '$lib/types/diagnostics';
	import { Button, Badge, StatusDot } from '$lib/components/ui';
	import { QuickAddRouteModal } from '$lib/components/diagnostics';
	import { Activity, RefreshCw, Play, Pause } from 'lucide-svelte';
	import { formatBytes, formatTime, formatOutboundLabel, type FircDomainCheckView } from './fircMonitorFormat';
	import FircMonitorDomainChecker from './FircMonitorDomainChecker.svelte';
	import FircMonitorTables from './FircMonitorTables.svelte';
	import FircMonitorEventsFeed from './FircMonitorEventsFeed.svelte';

	let snapshot = $state<FircMonitorSnapshot | null>(null);
	let loading = $state(true);
	let paused = $state(false);
	let view = $state<'overview' | 'sources' | 'events' | 'details'>('overview');
	let loadFailed = $state(false);
	let updatedAt = $state<number | null>(null);
	let inFlight = false;
	let reloadAfter = false;
	let disposed = false;
	let sourceFilter = $state<string>('all');
	let searchFilter = $state('');
	let clientFilter = $state('');

	let expandedClients = $state<Record<string, boolean>>({});
	let hosts = $state<FircHost[]>([]);
	let policies = $state<FircPolicy[]>([]);
	// Списки клиентов велики, поэтому запрашиваются, только пока хоть один виден раскрытым.
	let wantClients = $derived(
		Object.keys(expandedClients).some(
			(key) => expandedClients[key] && (key.startsWith('srv:') ? view === 'sources' : view === 'details'),
		),
	);

	let testDomain = $state('');
	let checkingDomain = $state(false);
	let checkResult = $state<FircDomainCheckView | null>(null);

	let scanningPage = $state(false);
	let pageScanMode = $state<PageScanFetchMode>('auto');
	let pageScanResult = $state<PageScanResult | null>(null);
	let pageScanError = $state<UiText>('');

	let inspectedEventSeq = $state<number | null>(null);
	let quickAddOpen = $state(false);
	let quickAddTarget = $state<{ ip?: string; domain?: string; prefix?: string }>({});

	function openClientEvents(ip: string) {
		clientFilter = ip;
		searchFilter = '';
		sourceFilter = 'all';
		view = 'events';
	}

	function openQuickAdd(target: { ip?: string; domain?: string; prefix?: string }) {
		quickAddTarget = target;
		quickAddOpen = true;
	}

	function toggleClients(key: string) {
		expandedClients[key] = !expandedClients[key];
		if (expandedClients[key]) void loadClientMeta();
	}

	// Имена и политики — один раз на раскрытие: на запрос устройств fircd заново
	// опрашивает роутер, для опроса монитора это дорого.
	async function loadClientMeta() {
		const [nextHosts, nextPolicies] = await Promise.all([
			api.fircGetHosts().catch(() => hosts),
			api.fircGetPolicies().catch(() => policies),
		]);
		hosts = nextHosts;
		policies = nextPolicies;
	}

	// Список раскрыли — снимок с клиентами нужен сразу, в том числе на паузе.
	$effect(() => {
		if (wantClients) untrack(() => void loadMonitor(false));
	});

	let stopPoll: (() => void) | null = null;

	async function loadMonitor(silent = false) {
		if (inFlight) {
			reloadAfter ||= !silent;
			return;
		}
		if (disposed || (paused && silent)) return;
		inFlight = true;
		if (!silent) loading = true;
		try {
			const next = await api.fircGetMonitor(wantClients);
			if (disposed) return;
			snapshot = next;
			updatedAt = Date.now();
			loadFailed = false;
		} catch {
			if (!disposed) loadFailed = true;
		} finally {
			inFlight = false;
			if (!silent) loading = false;
			if (reloadAfter) {
				reloadAfter = false;
				void loadMonitor(false);
			}
		}
	}

	async function runDomainCheck(dom?: string) {
		const target = (dom ?? testDomain).trim();
		if (!target) return;
		testDomain = target;
		checkingDomain = true;
		try {
			checkResult = await api.fircCheckDomain(target);
			await loadMonitor(true);
		} catch (e) {
			checkResult = {
				domain: target,
				routed: false,
				outbound: 'none',
				latencyMs: 0,
				error: (e as Error).message || (() => m.firc_monitor_check_failed()),
			};
		} finally {
			checkingDomain = false;
		}
	}

	async function runPageScan(targetOverride?: string, modeOverride?: PageScanFetchMode) {
		const target = (targetOverride ?? testDomain).trim();
		if (!target) return;
		const mode = modeOverride ?? 'auto';
		pageScanMode = mode;
		scanningPage = true;
		pageScanError = '';
		try {
			pageScanResult = await api.scanPage(target, mode);
			await loadMonitor(true);
		} catch (e) {
			pageScanError = (e as Error).message || (() => m.firc_monitor_request_failed());
		} finally {
			scanningPage = false;
		}
	}

	onMount(() => {
		// Живые счётчики монитора — опрос, но через visiblePoll: спит в
		// фоновой вкладке и не накладывает запросы.
		void loadMonitor(false);
		stopPoll = startVisiblePoll(() => loadMonitor(true), 2500);
	});

	onDestroy(() => {
		disposed = true;
		stopPoll?.();
	});
</script>

<div class="mon-panel">
	<!-- Top header & controls -->
	<div class="mon-head">
		<div class="mon-title-wrap">
			<Activity size={16} class="mon-icon" />
			<span class="mon-title">{m.firc_monitor_title()}</span>
			{#if snapshot?.running}
				<StatusDot variant="success" size="sm" pulse={!paused} />
				<span class="muted-meta">{m.firc_monitor_active_conns({ n: snapshot.totalConns })}</span>
			{:else if snapshot}
				<Badge variant="muted" size="xs">{m.firc_monitor_service_stopped()}</Badge>
			{/if}
			<span class="muted-meta">{paused ? m.firc_monitor_paused() : updatedAt ? m.firc_monitor_updated_at({ time: formatTime(updatedAt) }) : m.firc_monitor_loading_data()}</span>
		</div>

		<div class="mon-actions">
			<Button variant="secondary" size="sm" onclick={() => (paused = !paused)}>
				{#snippet iconBefore()}
					{#if paused}
						<Play size={13} />
					{:else}
						<Pause size={13} />
					{/if}
				{/snippet}
				{paused ? m.diag_logs_resume() : m.diag_logs_pause()}
			</Button>
			<Button variant="secondary" size="sm" loading={loading} onclick={() => loadMonitor(false)}>
				{#snippet iconBefore()}<RefreshCw size={13} />{/snippet}
				{m.common_refresh()}
			</Button>
		</div>
	</div>
	{#if loadFailed}
		<p class="monitor-notice" role="status">{snapshot ? m.firc_monitor_load_stale() : m.firc_monitor_load_unavailable()}</p>
	{/if}
	<nav class="monitor-views" aria-label={m.firc_monitor_views_aria()}>
		{#each [
			{ id: 'overview', label: m.firc_monitor_view_overview() },
			{ id: 'sources', label: m.firc_monitor_view_sources() },
			{ id: 'events', label: m.firc_monitor_view_events() },
			{ id: 'details', label: m.firc_monitor_view_details() },
		] as item}
			<button type="button" class:active={view === item.id} aria-pressed={view === item.id} onclick={() => { view = item.id as typeof view; }}>{item.label}</button>
		{/each}
	</nav>

	{#if view === 'overview'}
		<div class="group-overview">
			{#each snapshot?.groups ?? [] as group (group.id)}
				<div class="group-summary">
					<div class="group-summary-name"><StatusDot variant={group.live ? 'success' : 'warning'} size="sm" /><strong>{group.name}</strong></div>
					<span class="group-summary-route">{formatOutboundLabel(group.activeOutbound, group.activeOutboundLabel)}</span>
					<div class="group-summary-stats">
						<span class="group-summary-count">{m.firc_monitor_group_conns({ n: group.activeConns })}</span>
						{#if (group.connRxBytes ?? 0) + (group.connTxBytes ?? 0) > 0}
							<span class="bytes-meta mono">↓{formatBytes(group.connRxBytes)} ↑{formatBytes(group.connTxBytes)}</span>
						{/if}
					</div>
					{#if !group.live}<span class="monitor-warning">{m.firc_monitor_group_not_live()}</span>{/if}
				</div>
			{:else}
				<p class="feed-empty">{loading ? m.firc_monitor_groups_loading() : snapshot ? m.firc_monitor_groups_empty() : m.firc_monitor_waiting_data()}</p>
			{/each}
		</div>
		<p class="muted-meta">{m.firc_monitor_overview_hint()}</p>
	{/if}

	<FircMonitorDomainChecker
		bind:testDomain
		{checkingDomain}
		{checkResult}
		{scanningPage}
		{pageScanResult}
		pageScanError={uiText(pageScanError)}
		onCheck={(dom) => void runDomainCheck(dom)}
		onScan={(mode) => void runPageScan(undefined, mode)}
		onOpenQuickAdd={openQuickAdd}
		onRoutesChanged={() => {
			void loadMonitor(true);
			if (pageScanResult) void runPageScan(undefined, pageScanMode);
		}}
	/>

	{#if view === 'sources' || view === 'details'}
		<FircMonitorTables {view} {snapshot} {expandedClients} {hosts} {policies} onToggleClients={toggleClients} onFilterClientEvents={openClientEvents} />
	{/if}

	{#if view === 'events'}
		<FircMonitorEventsFeed {snapshot} bind:sourceFilter bind:searchFilter bind:clientFilter bind:inspectedEventSeq onOpenQuickAdd={openQuickAdd} />
	{/if}
</div>

<QuickAddRouteModal
	open={quickAddOpen}
	ip={quickAddTarget.ip}
	domain={quickAddTarget.domain}
	prefix={quickAddTarget.prefix}
	onclose={() => (quickAddOpen = false)}
	onadded={() => {
		void loadMonitor(true);
		if (pageScanResult) void runPageScan(undefined, pageScanMode);
	}}
/>

<style>
	.monitor-views { display: flex; flex-wrap: wrap; gap: .35rem; }
	.monitor-views button { border: 1px solid var(--border); border-radius: 6px; background: transparent; color: var(--text-secondary); padding: .4rem .7rem; cursor: pointer; font: inherit; font-size: .8rem; }
	.monitor-views button.active { background: var(--color-accent-tint, var(--bg-tertiary)); border-color: var(--accent); color: var(--text-primary); }
	.monitor-views button:focus-visible { outline: 2px solid var(--accent); outline-offset: 2px; }
	.group-overview { display: flex; flex-direction: column; border: 1px solid var(--border); border-radius: 8px; overflow: hidden; }
	.group-summary { display: grid; grid-template-columns: minmax(0, 1.2fr) minmax(0, 1fr) auto; gap: .5rem 1rem; align-items: center; padding: .65rem .8rem; border-bottom: 1px solid var(--border); font-size: .82rem; }
	.group-summary:last-child { border-bottom: 0; }
	.group-summary-name { display: flex; align-items: center; gap: .5rem; min-width: 0; overflow-wrap: anywhere; }
	.group-summary-route { color: var(--text-secondary); overflow-wrap: anywhere; }
	.group-summary-stats { display: flex; flex-direction: column; align-items: flex-end; gap: 2px; }
	.group-summary-count { color: var(--text-muted); }
	.monitor-warning { grid-column: 1 / -1; color: var(--color-warning, var(--text-secondary)); font-size: .75rem; }
	.monitor-notice { margin: 0; color: var(--color-warning, var(--text-secondary)); font-size: .8rem; }
	@media (max-width: 560px) {
		.group-summary { grid-template-columns: minmax(0, 1fr) auto; }
		.group-summary-route { grid-column: 1; }
		.group-summary-stats { grid-column: 2; grid-row: 1 / 3; }
	}
	.mon-panel {
		display: flex;
		flex-direction: column;
		gap: 0.75rem;
		padding: 0.9rem;
		margin-bottom: 0.9rem;
		border-radius: 10px;
		background: var(--bg-secondary);
		border: 1px solid color-mix(in srgb, var(--accent) 30%, var(--border));
		min-width: 0;
		max-width: 100%;
		box-sizing: border-box;
	}

	.mon-head {
		display: flex;
		align-items: center;
		justify-content: space-between;
		gap: 0.75rem;
		flex-wrap: wrap;
	}

	.mon-title-wrap {
		display: flex;
		align-items: center;
		gap: 0.5rem;
		flex-wrap: wrap;
	}

	.mon-title {
		font-size: 0.92rem;
		font-weight: 600;
		color: var(--text-primary);
	}

	.mon-actions {
		display: flex;
		align-items: center;
		gap: 0.45rem;
	}

	.mono {
		font-family: var(--font-mono);
	}

	.bytes-meta,
	.muted-meta {
		font-size: 0.71rem;
		color: var(--text-muted);
	}

	.feed-empty {
		padding: 1rem;
		text-align: center;
		font-size: 0.78rem;
		color: var(--text-muted);
	}
</style>
