<script lang="ts">
	import { onMount, onDestroy } from 'svelte';
	import { api } from '$lib/api/client';
	import { startVisiblePoll } from '$lib/utils/visiblePoll';
	import type {
		FircJournalEvent,
		FircDnsStatsRow,
		FircCaptureLast,
		FircGroup,
	} from '$lib/types/firc';
	import { m } from '$lib/i18n';
	import { Badge } from '$lib/components/ui';
	import { notifications } from '$lib/stores/notifications';
	import { Search } from 'lucide-svelte';
	import { DNS_ONLY_IFACE } from '$lib/utils/fircResolve';
	import { formatTime } from './fircMonitorFormat';
	import FircJournalToolbar from './FircJournalToolbar.svelte';

	type JournalSubTab = 'events' | 'dns' | 'capture';

	let subTab = $state<JournalSubTab>('events');
	let groups = $state<FircGroup[]>([]);

	// Events state
	let events = $state<FircJournalEvent[]>([]);
	let lastSeq = $state(0);
	let paused = $state(false);
	let eventSearch = $state('');
	let kindFilter = $state<string>('all');
	let levelFilter = $state<string>('all');
	let groupFilter = $state<string>('all');

	// DNS stats state
	let dnsRows = $state<FircDnsStatsRow[]>([]);
	let dnsQueries = $state(0);
	let dnsBlocked = $state(0);
	let dnsDomains = $state(0);
	let dnsLoading = $state(false);
	let dnsSearch = $state('');
	let dnsGroupFilter = $state('');

	// Capture state
	let captureRunning = $state(false);
	let captureToken = $state('');
	let captureRemaining = $state(0);
	let captureLast = $state<FircCaptureLast | undefined>(undefined);
	let captureBusy = $state(false);

	let stopPoll: (() => void) | null = null;
	let destroyed = false;

	const groupNameMap = $derived.by(() => {
		const map: Record<string, string> = {};
		for (const g of groups) {
			map[g.id] = g.name || g.id;
		}
		return map;
	});

	async function pollEvents() {
		if (paused) return;
		try {
			const res = await api.fircGetEvents(lastSeq);
			if (res?.events && res.events.length > 0) {
				const combined = [...events, ...res.events];
				events = combined.slice(-500);
			}
			if (res?.next !== undefined) {
				lastSeq = res.next;
			}
		} catch {
			// silent
		}
	}

	async function loadDnsStats() {
		dnsLoading = true;
		try {
			const res = await api.fircGetDnsStats({
				q: dnsSearch.trim() || undefined,
				limit: 300,
			});
			dnsRows = res?.rows ?? [];
			dnsQueries = res?.queries ?? 0;
			dnsBlocked = res?.blocked ?? 0;
			dnsDomains = res?.domains ?? dnsRows.length;
		} catch (e: unknown) {
			notifications.error((e instanceof Error ? e.message : undefined) ?? m.firc_journal_load_dns_failed());
		} finally {
			dnsLoading = false;
		}
	}

	async function pollCapture() {
		try {
			const res = await api.fircGetCapture();
			captureRunning = Boolean(res?.running);
			if (res?.token) captureToken = res.token;
			captureRemaining = res?.secondsLeft ?? 0;
			captureLast = res?.last;
		} catch {
			// silent
		}
	}

	async function toggleCapture() {
		captureBusy = true;
		try {
			if (captureRunning) {
				await api.fircStopCapture(captureToken);
				notifications.info(m.firc_journal_capture_stopped());
			} else {
				const res = await api.fircStartCapture();
				if (res?.token) captureToken = res.token;
				notifications.success(m.firc_journal_capture_started());
			}
			await pollCapture();
		} catch (e: unknown) {
			notifications.error((e instanceof Error ? e.message : undefined) ?? m.firc_journal_capture_failed());
		} finally {
			captureBusy = false;
		}
	}

	onMount(async () => {
		try {
			groups = await api.fircGetGroups();
		} catch {
			// ignore
		}
		await Promise.all([pollEvents(), loadDnsStats(), pollCapture()]);

		// onMount асинхронный: без этой проверки опрос, запущенный после
		// размонтирования, не остановился бы никогда.
		if (destroyed) return;
		// Хвост журнала — живые данные, остаётся опросом через visiblePoll
		// (спит в фоновой вкладке, запросы не накладываются).
		stopPoll = startVisiblePoll(() => {
			if (subTab === 'events') return pollEvents();
			if (subTab === 'dns') return loadDnsStats();
			if (subTab === 'capture') return pollCapture();
		}, 2500);
	});

	onDestroy(() => {
		destroyed = true;
		stopPoll?.();
	});

	function eventSummaryText(ev: FircJournalEvent): string {
		if (ev.kind === 'log') {
			return ev.message;
		}
		if (ev.kind === 'dns') {
			const target = ev.fake ? ` → FakeIP ${ev.fake}` : ev.reals?.length ? ` → ${ev.reals.join(', ')}` : '';
			return m.firc_journal_dns_requested({ client: ev.client, name: ev.name, qtype: ev.qtype, decision: ev.decision === 'dns-only' ? m.firc_dns_only() : ev.decision, target });
		}
		return `${ev.client} → ${ev.name || ev.dst}${ev.port ? `:${ev.port}` : ''} (${ev.proto}, ${ev.how})`;
	}

	function eventGroupId(ev: FircJournalEvent): string | undefined {
		if (ev.kind === 'dns' || ev.kind === 'bypass') {
			return ev.group?.id;
		}
		return undefined;
	}

	function eventSourceLabel(ev: FircJournalEvent): string {
		if (ev.kind === 'log') return ev.source || 'fircd';
		if (ev.kind === 'dns') return 'dns';
		return 'bypass';
	}

	function eventLevelLabel(ev: FircJournalEvent): string {
		if (ev.kind === 'log') return ev.level;
		if (ev.kind === 'dns') return ev.decision === 'blocked' ? 'warn' : 'info';
		return 'warn';
	}

	const filteredEvents = $derived.by(() => {
		const q = eventSearch.trim().toLowerCase();
		return events
			.filter((ev) => {
				if (kindFilter !== 'all' && ev.kind !== kindFilter) return false;
				const lvl = eventLevelLabel(ev);
				if (levelFilter !== 'all' && lvl !== levelFilter) return false;
				const gid = eventGroupId(ev);
				if (groupFilter !== 'all' && gid !== groupFilter) return false;
				if (q) {
					const hay = `${eventSummaryText(ev)} ${eventSourceLabel(ev)}`.toLowerCase();
					if (!hay.includes(q)) return false;
				}
				return true;
			})
			.slice()
			.reverse();
	});

	const filteredDnsRows = $derived.by(() => {
		if (!dnsGroupFilter) return dnsRows;
		return dnsRows.filter((r) => r.owner?.id === dnsGroupFilter);
	});

	function selectTab(tab: JournalSubTab) {
		subTab = tab;
		if (tab === 'dns') loadDnsStats();
		else if (tab === 'capture') pollCapture();
	}

	function levelBadgeVariant(level: string): 'error' | 'warning' | 'info' | 'muted' {
		if (level === 'error') return 'error';
		if (level === 'warn' || level === 'warning') return 'warning';
		if (level === 'info') return 'info';
		return 'muted';
	}
</script>

<div class="journal-panel">
	<FircJournalToolbar
		{subTab}
		eventsCount={events.length}
		bind:paused
		{dnsDomains}
		{dnsQueries}
		{dnsBlocked}
		{dnsLoading}
		{captureRunning}
		{captureRemaining}
		{captureBusy}
		onSelectTab={selectTab}
		onClearEvents={() => (events = [])}
		onRefreshDns={loadDnsStats}
		onToggleCapture={toggleCapture}
	/>

	<!-- TAB 1: EVENTS -->
	{#if subTab === 'events'}
		<div class="filters-bar">
			<div class="search-box">
				<Search size={14} />
				<input
					type="text"
					placeholder={m.firc_journal_events_search()}
					bind:value={eventSearch}
				/>
			</div>

			<select class="filter-select" bind:value={kindFilter}>
				<option value="all">{m.firc_journal_kind_all()}</option>
				<option value="log">{m.firc_journal_kind_log()}</option>
				<option value="dns">{m.firc_journal_kind_dns()}</option>
				<option value="bypass">{m.firc_journal_kind_bypass()}</option>
			</select>

			<select class="filter-select" bind:value={levelFilter}>
				<option value="all">{m.firc_journal_level_all()}</option>
				<option value="error">{m.firc_journal_level_error()}</option>
				<option value="warn">{m.firc_journal_level_warn()}</option>
				<option value="info">{m.firc_journal_level_info()}</option>
				<option value="debug">{m.firc_journal_level_debug()}</option>
			</select>

			<select class="filter-select" bind:value={groupFilter}>
				<option value="all">{m.firc_journal_all_groups()}</option>
				{#each groups as g (g.id)}
					<option value={g.id}>{g.name || g.id}</option>
				{/each}
			</select>
		</div>

		<div class="terminal-card">
			{#if filteredEvents.length === 0}
				<div class="empty-box">{m.firc_journal_events_empty()}</div>
			{:else}
				<div class="events-list">
					{#each filteredEvents as ev (ev.seq)}
						{@const lvl = eventLevelLabel(ev)}
						{@const gid = eventGroupId(ev)}
						<div class="event-row">
							<span class="ev-time">{formatTime(ev.at)}</span>
							<Badge variant={levelBadgeVariant(lvl)} size="xs" mono>
								{lvl}
							</Badge>
							<Badge variant="default" size="xs" mono>
								{eventSourceLabel(ev)}
							</Badge>
							{#if gid}
								<Badge variant="accent" size="xs">
									{groupNameMap[gid] || gid}
								</Badge>
							{/if}
							<span class="ev-msg">{eventSummaryText(ev)}</span>
						</div>
					{/each}
				</div>
			{/if}
		</div>
	{/if}

	<!-- TAB 2: DNS STATS -->
	{#if subTab === 'dns'}
		<div class="filters-bar">
			<div class="search-box">
				<Search size={14} />
				<input
					type="text"
					placeholder={m.firc_journal_dns_search()}
					bind:value={dnsSearch}
					oninput={() => loadDnsStats()}
				/>
			</div>

			<select class="filter-select" bind:value={dnsGroupFilter}>
				<option value="">{m.firc_journal_all_groups()}</option>
				{#each groups as g (g.id)}
					<option value={g.id}>{g.name || g.id}</option>
				{/each}
			</select>
		</div>

		<div class="table-card">
			{#if filteredDnsRows.length === 0}
				<div class="empty-box">{m.firc_journal_dns_empty()}</div>
			{:else}
				<table class="data-table">
					<thead>
						<tr>
							<th>{m.firc_journal_th_domain()}</th>
							<th>{m.firc_journal_th_group()}</th>
							<th>{m.firc_journal_th_interface()}</th>
							<th>{m.firc_journal_th_clients()}</th>
							<th>{m.firc_journal_th_queries()}</th>
							<th>{m.firc_journal_th_last()}</th>
						</tr>
					</thead>
					<tbody>
						{#each filteredDnsRows as row (row.name)}
							<tr>
								<td class="mono-primary">{row.name}</td>
								<td>
									{#if row.owner}
										<Badge variant={row.owner.blocked ? 'error' : 'accent'} size="xs">
											{row.owner.name || row.owner.id}
										</Badge>
									{:else}
										<span class="muted">{m.firc_journal_no_group()}</span>
									{/if}
								</td>
								<td class="mono-cell">{row.owner?.interface === DNS_ONLY_IFACE ? m.firc_dns_only() : row.owner?.interface || 'WAN'}</td>
								<td class="mono-cell">{row.clientsTotal ?? row.clients?.length ?? 0}</td>
								<td class="mono-cell">{row.queries}</td>
								<td class="mono-cell">{formatTime(row.last)}</td>
							</tr>
						{/each}
					</tbody>
				</table>
			{/if}
		</div>
	{/if}

	<!-- TAB 3: PACKET CAPTURE -->
	{#if subTab === 'capture'}
		<div class="table-card">
			{#if !captureLast && !captureRunning}
				<div class="empty-box">
					{m.firc_journal_capture_idle()}
				</div>
			{:else if captureRunning && (!captureLast || (captureLast.summary?.length ?? 0) === 0)}
				<div class="empty-box">
					{m.firc_journal_capture_running({ n: captureRemaining })}
				</div>
			{:else if captureLast}
				<div class="capture-summary-header">
					<Badge variant={captureLast.leaks > 0 ? 'warning' : 'success'} size="sm">
						{m.firc_journal_capture_leaks({ n: captureLast.leaks })}
					</Badge>
					<span class="mono-cell">
						{m.firc_journal_capture_ended({ time: formatTime(captureLast.endedAt), reason: captureLast.ended })}
					</span>
				</div>
				{#if (captureLast.summary?.length ?? 0) === 0}
					<div class="empty-box">{m.firc_journal_capture_clean()}</div>
				{:else}
					<div class="events-list">
						{#each captureLast.summary as line, idx (idx)}
							<div class="event-row">
								<span class="ev-time">#{idx + 1}</span>
								<span class="ev-msg">{line}</span>
							</div>
						{/each}
					</div>
				{/if}
			{/if}
		</div>
	{/if}
</div>

<style>
	.journal-panel {
		display: flex;
		flex-direction: column;
		gap: 10px;
	}


	.filters-bar {
		display: flex;
		align-items: center;
		gap: 8px;
		flex-wrap: wrap;
	}

	.search-box {
		display: flex;
		align-items: center;
		gap: 8px;
		padding: 6px 10px;
		background: var(--bg-secondary);
		border: 1px solid var(--border);
		border-radius: 7px;
		color: var(--text-secondary);
		flex: 1;
		min-width: 240px;
	}

	.search-box input {
		border: none;
		background: transparent;
		color: var(--text-primary);
		font-size: 12px;
		width: 100%;
		outline: none;
	}

	.filter-select {
		padding: 6px 10px;
		background: var(--bg-secondary);
		border: 1px solid var(--border);
		border-radius: 7px;
		color: var(--text-primary);
		font-size: 12px;
	}

	.terminal-card,
	.table-card {
		border: 1px solid var(--border);
		border-radius: 10px;
		background: var(--bg-secondary);
		overflow: hidden;
	}

	.capture-summary-header {
		display: flex;
		align-items: center;
		justify-content: space-between;
		gap: 10px;
		padding: 10px 14px;
		border-bottom: 1px solid var(--border);
		background: var(--bg-tertiary);
	}

	.events-list {
		max-height: 540px;
		overflow-y: auto;
		padding: 8px 12px;
		display: flex;
		flex-direction: column;
		gap: 4px;
		font-family: var(--font-mono);
		font-size: 12px;
	}

	.event-row {
		display: flex;
		align-items: baseline;
		gap: 8px;
		padding: 4px 6px;
		border-radius: 5px;
		border-bottom: 1px solid rgba(255, 255, 255, 0.03);
		flex-wrap: wrap;
	}

	.event-row:hover {
		background: var(--bg-tertiary);
	}

	.ev-time {
		color: var(--text-secondary);
		font-size: 11px;
		flex-shrink: 0;
	}

	.ev-msg {
		color: var(--text-primary);
		word-break: break-word;
	}

	.data-table {
		width: 100%;
		border-collapse: collapse;
		font-size: 12px;
	}

	.data-table th {
		text-align: left;
		padding: 9px 12px;
		background: var(--bg-tertiary);
		color: var(--text-secondary);
		font-size: 11px;
		font-weight: 600;
		border-bottom: 1px solid var(--border);
	}

	.data-table td {
		padding: 8px 12px;
		border-bottom: 1px solid rgba(255, 255, 255, 0.04);
	}

	.mono-primary {
		font-family: var(--font-mono);
		font-size: 12px;
		color: var(--text-primary);
	}

	.mono-cell {
		font-family: var(--font-mono);
		font-size: 11px;
		color: var(--text-secondary);
	}

	.muted {
		color: var(--text-secondary);
	}

	.empty-box {
		padding: 36px 20px;
		text-align: center;
		color: var(--text-secondary);
		font-size: 13px;
	}
</style>