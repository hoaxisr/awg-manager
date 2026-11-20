<script lang="ts">
	import { onMount, onDestroy } from 'svelte';
	import { api } from '$lib/api/client';
	import type {
		FircJournalEvent,
		FircDnsStatsRow,
		FircCaptureLast,
		FircGroup,
	} from '$lib/types/firc';
	import { Button, Badge, StatusDot } from '$lib/components/ui';
	import { notifications } from '$lib/stores/notifications';
	import { Search, Play, Pause, Trash2, RefreshCw, Radio } from 'lucide-svelte';

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

	let pollTimer: ReturnType<typeof setInterval> | null = null;

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
			notifications.error((e instanceof Error ? e.message : undefined) ?? 'Ошибка загрузки DNS-статистики');
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
				notifications.info('Захват трафика остановлен');
			} else {
				const res = await api.fircStartCapture();
				if (res?.token) captureToken = res.token;
				notifications.success('Захват трафика запущен на 60 секунд');
			}
			await pollCapture();
		} catch (e: unknown) {
			notifications.error((e instanceof Error ? e.message : undefined) ?? 'Ошибка управления захватом трафика');
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

		pollTimer = setInterval(() => {
			if (subTab === 'events') pollEvents();
			else if (subTab === 'dns') loadDnsStats();
			else if (subTab === 'capture') pollCapture();
		}, 2500);
	});

	onDestroy(() => {
		if (pollTimer) clearInterval(pollTimer);
	});

	function eventSummaryText(ev: FircJournalEvent): string {
		if (ev.kind === 'log') {
			return ev.message;
		}
		if (ev.kind === 'dns') {
			const target = ev.fake ? ` → FakeIP ${ev.fake}` : ev.reals?.length ? ` → ${ev.reals.join(', ')}` : '';
			return `${ev.client} запросил ${ev.name} (${ev.qtype}) [${ev.decision}]${target}`;
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

	function formatTime(ts?: number): string {
		if (!ts) return '—';
		const ms = ts > 1e12 ? ts : ts * 1000;
		const d = new Date(ms);
		return d.toLocaleTimeString('ru-RU', { hour12: false });
	}

	function levelBadgeVariant(level: string): 'error' | 'warning' | 'info' | 'muted' {
		if (level === 'error') return 'error';
		if (level === 'warn' || level === 'warning') return 'warning';
		if (level === 'info') return 'info';
		return 'muted';
	}
</script>

<div class="journal-panel">
	<!-- Subtabs Header -->
	<div class="journal-top-bar">
		<div class="journal-subtabs">
			<button
				type="button"
				class="subtab-btn"
				class:active={subTab === 'events'}
				onclick={() => (subTab = 'events')}
			>
				События ({events.length})
			</button>
			<button
				type="button"
				class="subtab-btn"
				class:active={subTab === 'dns'}
				onclick={() => {
					subTab = 'dns';
					loadDnsStats();
				}}
			>
				Статистика DNS ({dnsDomains})
			</button>
			<button
				type="button"
				class="subtab-btn"
				class:active={subTab === 'capture'}
				onclick={() => {
					subTab = 'capture';
					pollCapture();
				}}
			>
				Захват пакетов
				{#if captureRunning}
					<StatusDot variant="success" size="sm" pulse />
				{/if}
			</button>
		</div>

		{#if subTab === 'events'}
			<div class="journal-actions">
				<Button
					variant="secondary"
					size="sm"
					onclick={() => (paused = !paused)}
				>
					{#snippet iconBefore()}
						{#if paused}
							<Play size={13} />
						{:else}
							<Pause size={13} />
						{/if}
					{/snippet}
					{paused ? 'Продолжить' : 'Пауза'}
				</Button>
				<Button
					variant="ghost"
					size="sm"
					onclick={() => (events = [])}
				>
					{#snippet iconBefore()}<Trash2 size={13} />{/snippet}
					Очистить
				</Button>
			</div>
		{:else if subTab === 'dns'}
			<div class="journal-actions">
				<Badge variant="default" size="sm" mono>Запросов: {dnsQueries}</Badge>
				{#if dnsBlocked > 0}
					<Badge variant="warning" size="sm" mono>Блок: {dnsBlocked}</Badge>
				{/if}
				<Button
					variant="secondary"
					size="sm"
					loading={dnsLoading}
					onclick={loadDnsStats}
				>
					{#snippet iconBefore()}<RefreshCw size={13} />{/snippet}
					Обновить
				</Button>
			</div>
		{:else if subTab === 'capture'}
			<div class="journal-actions">
				{#if captureRunning}
					<Badge variant="warning" size="sm" mono>Осталось: {captureRemaining} с</Badge>
				{/if}
				<Button
					variant={captureRunning ? 'danger' : 'primary'}
					size="sm"
					loading={captureBusy}
					onclick={toggleCapture}
				>
					{#snippet iconBefore()}<Radio size={13} />{/snippet}
					{captureRunning ? 'Остановить захват' : 'Начать захват (60 с)'}
				</Button>
			</div>
		{/if}
	</div>

	<!-- TAB 1: EVENTS -->
	{#if subTab === 'events'}
		<div class="filters-bar">
			<div class="search-box">
				<Search size={14} />
				<input
					type="text"
					placeholder="Фильтр по сообщениям, доменам, клиентам…"
					bind:value={eventSearch}
				/>
			</div>

			<select class="filter-select" bind:value={kindFilter}>
				<option value="all">Все типы событий</option>
				<option value="log">Системные логи (log)</option>
				<option value="dns">Запросы DNS (dns)</option>
				<option value="bypass">Обход / Утечки (bypass)</option>
			</select>

			<select class="filter-select" bind:value={levelFilter}>
				<option value="all">Все уровни</option>
				<option value="error">Ошибки (error)</option>
				<option value="warn">Предупреждения (warn)</option>
				<option value="info">Инфо (info)</option>
				<option value="debug">Отладка (debug)</option>
			</select>

			<select class="filter-select" bind:value={groupFilter}>
				<option value="all">Все группы</option>
				{#each groups as g (g.id)}
					<option value={g.id}>{g.name || g.id}</option>
				{/each}
			</select>
		</div>

		<div class="terminal-card">
			{#if filteredEvents.length === 0}
				<div class="empty-box">Событий по выбранным фильтрам пока нет.</div>
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
					placeholder="Поиск по домену…"
					bind:value={dnsSearch}
					oninput={() => loadDnsStats()}
				/>
			</div>

			<select class="filter-select" bind:value={dnsGroupFilter}>
				<option value="">Все группы</option>
				{#each groups as g (g.id)}
					<option value={g.id}>{g.name || g.id}</option>
				{/each}
			</select>
		</div>

		<div class="table-card">
			{#if filteredDnsRows.length === 0}
				<div class="empty-box">Записи статистики DNS отсутствуют.</div>
			{:else}
				<table class="data-table">
					<thead>
						<tr>
							<th>Домен</th>
							<th>Группа</th>
							<th>Интерфейс</th>
							<th>Клиентов</th>
							<th>Запросов</th>
							<th>Последний</th>
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
										<span class="muted">Вне групп</span>
									{/if}
								</td>
								<td class="mono-cell">{row.owner?.interface || 'WAN'}</td>
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
					Нажмите «Начать захват (60 с)», чтобы собрать диагностику DNS и сетевых соединений клиентов в реальном времени.
				</div>
			{:else if captureRunning && (!captureLast || (captureLast.summary?.length ?? 0) === 0)}
				<div class="empty-box">
					Идёт захват пакетов (осталось {captureRemaining} с)… Ожидание сетевой активности устройств.
				</div>
			{:else if captureLast}
				<div class="capture-summary-header">
					<Badge variant={captureLast.leaks > 0 ? 'warning' : 'success'} size="sm">
						Утечек / прямых обращений: {captureLast.leaks}
					</Badge>
					<span class="mono-cell">
						Завершён: {formatTime(captureLast.endedAt)} ({captureLast.ended})
					</span>
				</div>
				{#if (captureLast.summary?.length ?? 0) === 0}
					<div class="empty-box">В ходе захвата аномалий или утечек не зафиксировано.</div>
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

	.journal-top-bar {
		display: flex;
		align-items: center;
		justify-content: space-between;
		gap: 10px;
		flex-wrap: wrap;
	}

	.journal-subtabs {
		display: inline-flex;
		align-items: center;
		gap: 4px;
		background: var(--bg-secondary);
		border: 1px solid var(--border);
		border-radius: 8px;
		padding: 3px;
	}

	.subtab-btn {
		display: inline-flex;
		align-items: center;
		gap: 6px;
		padding: 5px 12px;
		border: none;
		border-radius: 6px;
		background: transparent;
		color: var(--text-secondary);
		font-size: 12px;
		font-weight: 500;
		cursor: pointer;
		transition: all 0.15s;
	}

	.subtab-btn:hover {
		color: var(--text-primary);
	}

	.subtab-btn.active {
		background: var(--bg-tertiary);
		color: var(--accent);
		font-weight: 600;
	}

	.journal-actions {
		display: flex;
		align-items: center;
		gap: 8px;
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
