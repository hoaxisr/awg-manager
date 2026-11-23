<script lang="ts">
	import { onMount, onDestroy } from 'svelte';
	import { m } from '$lib/i18n';
	import { api } from '$lib/api/client';
	import { startVisiblePoll } from '$lib/utils/visiblePoll';
	import type {
		FircMonitorSnapshot,
		FircDomainCheckResult,
	} from '$lib/types/firc';
	import type { PageScanFetchMode, PageScanResult } from '$lib/types/diagnostics';
	import { Button, Badge, StatusDot } from '$lib/components/ui';
	import { IpInspectSection, PageScanSection, QuickAddRouteModal } from '$lib/components/diagnostics';
	import { Activity, Search, RefreshCw, Play, Pause, ArrowRight, CheckCircle2, Globe, Info } from 'lucide-svelte';

	let snapshot = $state<FircMonitorSnapshot | null>(null);
	let loading = $state(true);
	let paused = $state(false);
	let view = $state<'overview' | 'sources' | 'events' | 'details'>('overview');
	let loadError = $state('');
	let updatedAt = $state<number | null>(null);
	let inFlight = false;
	let disposed = false;
	let sourceFilter = $state<string>('all');
	let searchFilter = $state('');

	let testDomain = $state('');
	let checkingDomain = $state(false);
	let checkResult = $state<FircDomainCheckResult | null>(null);

	let scanningPage = $state(false);
	let pageScanMode = $state<PageScanFetchMode>('auto');
	let pageScanResult = $state<PageScanResult | null>(null);
	let pageScanError = $state('');

	let inspectedEventSeq = $state<number | null>(null);
	let quickAddOpen = $state(false);
	let quickAddTarget = $state<{ ip?: string; domain?: string; prefix?: string }>({});

	function openQuickAdd(target: { ip?: string; domain?: string; prefix?: string }) {
		quickAddTarget = target;
		quickAddOpen = true;
	}

	let stopPoll: (() => void) | null = null;

	const quickDomains = [
		'gemini.google.com',
		'generativelanguage.googleapis.com',
		'chatgpt.com',
		't.me',
		'youtube.com',
		'2ip.io',
	];

	async function loadMonitor(silent = false) {
		if (disposed || inFlight || (paused && silent)) return;
		inFlight = true;
		if (!silent) loading = true;
		try {
			const next = await api.fircGetMonitor();
			if (disposed) return;
			snapshot = next;
			updatedAt = Date.now();
			loadError = '';
		} catch {
			if (!disposed) loadError = 'Не удалось обновить данные. Показан последний полученный снимок.';
		} finally {
			inFlight = false;
			if (!silent) loading = false;
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
				error: (e as Error).message || 'Ошибка проверки домена',
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
			pageScanError = (e as Error).message || 'Request failed';
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

	function formatBytes(bytes: number): string {
		if (!bytes || bytes <= 0) return '0 Б';
		const units = ['Б', 'КБ', 'МБ', 'ГБ'];
		let val = bytes;
		let idx = 0;
		while (val >= 1024 && idx < units.length - 1) {
			val /= 1024;
			idx++;
		}
		return `${val >= 10 || idx === 0 ? val.toFixed(0) : val.toFixed(1)} ${units[idx]}`;
	}

	function formatTime(ts?: number): string {
		if (!ts) return '—';
		const ms = ts > 1e12 ? ts : ts * 1000;
		return new Date(ms).toLocaleTimeString('ru-RU', { hour12: false });
	}

	function formatOutboundLabel(id?: string, label?: string): string {
		if (label && id && label !== id) return `${label} (${id})`;
		return label || id || 'Выход не указан';
	}

	let totalTxBytes = $derived.by(() => {
		if (snapshot?.totalTxBytes !== undefined) return snapshot.totalTxBytes;
		return (snapshot?.groups ?? []).reduce((acc, g) => acc + (g.connTxBytes || 0), 0);
	});

	let totalRxBytes = $derived.by(() => {
		if (snapshot?.totalRxBytes !== undefined) return snapshot.totalRxBytes;
		return (snapshot?.groups ?? []).reduce((acc, g) => acc + (g.connRxBytes || 0), 0);
	});

	let totalRulePackets = $derived.by(() =>
		(snapshot?.groups ?? []).reduce((acc, g) => acc + (g.matchedPackets || 0), 0),
	);

	let totalRuleBytes = $derived.by(() =>
		(snapshot?.groups ?? []).reduce((acc, g) => acc + (g.matchedBytes || 0), 0),
	);

	let filteredMatches = $derived.by(() => {
		const list = snapshot?.recentMatches ?? [];
		const q = searchFilter.trim().toLowerCase();
		return list.filter((m) => {
			if (sourceFilter !== 'all' && m.sourceId !== sourceFilter) return false;
			if (q) {
				const hay = `${m.domain} ${m.clientIp} ${m.sourceName} ${m.groupName} ${m.outbound} ${m.fakeIp ?? ''} ${m.realIp ?? ''}`.toLowerCase();
				if (!hay.includes(q)) return false;
			}
			return true;
		});
	});
</script>

<div class="mon-panel">
	<!-- Top header & controls -->
	<div class="mon-head">
		<div class="mon-title-wrap">
			<Activity size={16} class="mon-icon" />
			<span class="mon-title">Мониторинг</span>
			{#if snapshot?.running}
				<StatusDot variant="success" size="sm" pulse={!paused} />
				<span class="muted-meta">Активные соединения: {snapshot.totalConns}</span>
			{:else if snapshot}
				<Badge variant="muted" size="xs">Служба остановлена</Badge>
			{/if}
			<span class="muted-meta">{paused ? 'Обновление на паузе' : updatedAt ? `Обновлено ${formatTime(updatedAt)}` : 'Загрузка данных…'}</span>
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
				{paused ? 'Продолжить' : 'Пауза'}
			</Button>
			<Button variant="secondary" size="sm" loading={loading} onclick={() => loadMonitor(false)}>
				{#snippet iconBefore()}<RefreshCw size={13} />{/snippet}
				Обновить
			</Button>
		</div>
	</div>
	{#if loadError}
		<p class="monitor-notice" role="status">{snapshot ? loadError : 'Данные мониторинга недоступны. Попробуйте обновить.'}</p>
	{/if}
	<nav class="monitor-views" aria-label="Разделы мониторинга">
		{#each [
			{ id: 'overview', label: 'Обзор' },
			{ id: 'sources', label: 'Источники' },
			{ id: 'events', label: 'События' },
			{ id: 'details', label: 'Счётчики' },
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
						<span class="group-summary-count">Соединения: {group.activeConns}</span>
						{#if (group.connRxBytes ?? 0) + (group.connTxBytes ?? 0) > 0}
							<span class="bytes-meta mono">↓{formatBytes(group.connRxBytes)} ↑{formatBytes(group.connTxBytes)}</span>
						{/if}
					</div>
					{#if !group.live}<span class="monitor-warning">Нет подтверждения активности группы</span>{/if}
				</div>
			{:else}
				<p class="feed-empty">{loading ? 'Загрузка групп…' : snapshot ? 'Групп для отображения пока нет.' : 'Ожидаем данные мониторинга.'}</p>
			{/each}
		</div>
		<p class="muted-meta">Соединения — по последнему снимку. Проверка домена показывает выбор маршрута, а не скорость или доступность сайта.</p>
	{/if}

	<!-- Instant Domain Routing Checker -->
	<details class="domain-checker">
		<summary>Проверить маршрут домена</summary>
	<div class="checker-box">
		<div class="checker-top">
			<div class="checker-label">
				<Globe size={14} />
				<span>Какой маршрут выбран для домена</span>
			</div>
			<div class="checker-quick-chips">
				{#each quickDomains as qd (qd)}
					<button
						type="button"
						class="quick-chip"
						class:active={testDomain === qd}
						disabled={checkingDomain}
						onclick={() => runDomainCheck(qd)}
					>
						{qd}
					</button>
				{/each}
			</div>
		</div>

		<form
			class="checker-form"
			onsubmit={(e) => {
				e.preventDefault();
				void runDomainCheck();
			}}
		>
			<input
				type="text"
				class="checker-input"
				aria-label="Домен для проверки маршрута"
				placeholder="Например, youtube.com"
				bind:value={testDomain}
			/>
			<Button variant="primary" size="sm" type="submit" loading={checkingDomain} disabled={scanningPage}>
				Проверить
			</Button>
			<Button
				variant="secondary"
				size="sm"
				type="button"
				loading={scanningPage}
				disabled={!testDomain.trim() || checkingDomain}
				onclick={() => void runPageScan()}
			>
				{m.page_scan_btn()}
			</Button>
		</form>

		{#if checkResult}
			<div class="checker-res" class:is-routed={checkResult.routed} class:is-error={Boolean(checkResult.error)}>
				{#if checkResult.error}
					<Badge variant="error" size="sm">Ошибка</Badge>
					<span class="mono">{checkResult.domain}</span>
					<span>— {checkResult.error}</span>
				{:else if checkResult.routed}
					<CheckCircle2 size={15} />
					<Badge variant="success" size="sm">Выбрано правило FIRC</Badge>
					<span class="mono"><strong>{checkResult.domain}</strong></span>
					<ArrowRight size={13} />
					<span class="mono">FakeIP {checkResult.fakeIp}</span>
					{#if checkResult.realIp}
						<span class="muted-meta">(реальный IP: {checkResult.realIp})</span>
					{/if}
					<ArrowRight size={13} />
					<Badge variant="accent" size="sm">{checkResult.groupName || checkResult.groupId}</Badge>
					<ArrowRight size={13} />
					<Badge variant="info" size="sm" mono>Туннель: {formatOutboundLabel(checkResult.outbound, checkResult.outboundLabel)}</Badge>
					<span class="muted-meta">({checkResult.latencyMs} мс)</span>
				{:else}
					<Badge variant="warning" size="sm">Напрямую (вне правил FIRC)</Badge>
					<span class="mono"><strong>{checkResult.domain}</strong></span>
					{#if checkResult.realIp}
						<span class="mono">→ {checkResult.realIp}</span>
					{/if}
					<span class="muted-meta">→ {checkResult.outbound} ({checkResult.latencyMs} мс)</span>
				{/if}
			</div>
			{#if !checkResult.error}
				<IpInspectSection
					ip={checkResult.realIp || ''}
					domain={checkResult.domain}
					compact
					onOpenQuickAdd={openQuickAdd}
				/>
			{/if}
		{/if}

		{#if scanningPage || pageScanResult || pageScanError}
			<PageScanSection
				loading={scanningPage}
				error={pageScanError}
				result={pageScanResult}
				onOpenQuickAdd={openQuickAdd}
				onRescan={(mode) => void runPageScan(undefined, mode)}
				onRoutesChanged={() => {
					void loadMonitor(true);
					if (pageScanResult) void runPageScan(undefined, pageScanMode);
				}}
			/>
		{/if}
	</div>
	</details>

	<!-- Grid: 1) Inbound Servers & LAN  2) FIRC Groups -->
	{#if view === 'sources' || view === 'details'}
	<div class="mon-grid-2">
		<!-- Section 1: Inbound VPN Servers & LAN -->
		{#if view === 'sources'}
		<div class="mon-card">
			<div class="mon-card-head">
				<span>Источники трафика (VPN-серверы и LAN)</span>
				<span class="mon-card-sub">Перехват DNS :3553 и активные сессии</span>
			</div>
			<div class="mon-table-wrap">
				<table class="mon-table">
					<thead>
						<tr>
							<th>Источник / Сегмент</th>
							<th>Статус в FIRC</th>
							<th>DNS-запросы (:3553)</th>
							<th>Активные сессии и трафик</th>
							<th>Последнее срабатывание</th>
						</tr>
					</thead>
					<tbody>
						{#each snapshot?.servers ?? [] as srv (srv.id)}
							{@const hasTraffic = srv.activeConns > 0 || srv.matchedQueries > 0}
							<tr class:row-active={hasTraffic}>
								<td>
									<div class="srv-cell">
										<StatusDot
											variant={hasTraffic ? 'success' : srv.routingEnabled && srv.running ? 'info' : 'muted'}
											size="sm"
											pulse={srv.activeConns > 0}
										/>
										<div>
											<div class="srv-name">{srv.name}</div>
											<div class="srv-detail mono">{srv.detail}</div>
										</div>
									</div>
								</td>
								<td>
									{#if !srv.routingEnabled}
										<Badge variant="muted" size="xs">Выкл. в FIRC</Badge>
									{:else if hasTraffic}
										<Badge variant="success" size="xs">{srv.activeConns > 0 ? 'Есть соединения' : 'Есть DNS-совпадения'}</Badge>
									{:else if srv.running}
										<Badge variant="info" size="xs">Включён · ждёт трафик</Badge>
									{:else}
										<Badge variant="muted" size="xs">Сервер остановлен</Badge>
									{/if}
								</td>
								<td class="mono">
									<div>{srv.dnsPackets} пкт</div>
									{#if srv.matchedQueries > 0}
										<div class="bytes-meta">по правилам: {srv.matchedQueries}</div>
									{/if}
								</td>
								<td class="mono">
									{#if srv.activeConns > 0 || srv.txBytes > 0 || srv.rxBytes > 0}
										<div>
											<span class="conn-pill">{srv.activeConns} сесс.</span>
											{#if srv.activeClients}
												<span class="bytes-meta"> · {srv.activeClients} устр.</span>
											{/if}
										</div>
										<div class="bytes-meta">
											↓{formatBytes(srv.rxBytes)} · ↑{formatBytes(srv.txBytes)}
										</div>
									{:else}
										<span class="muted-meta">0 сесс.</span>
									{/if}
								</td>
								<td>
									{#if srv.lastDomain}
										<div class="last-match">
											<span class="mono domain-text">{srv.lastDomain}</span>
											{#if srv.lastGroup}
												<Badge variant="accent" size="xs">{srv.lastGroup}</Badge>
											{/if}
											{#if srv.lastOutbound}
												<Badge variant="info" size="xs" mono>{formatOutboundLabel(srv.lastOutbound, srv.lastOutboundLabel)}</Badge>
											{/if}
											<span class="muted-meta">{formatTime(srv.lastAt)}</span>
										</div>
									{:else}
										<span class="muted-meta">—</span>
									{/if}
								</td>
							</tr>
						{/each}
					</tbody>
				</table>
			</div>
		</div>
		{/if}

		<!-- Section 2: FIRC Groups Live Counters -->
		{#if view === 'details'}
		<div class="counters-kpi-grid">
			<div class="kpi-card">
				<div class="kpi-label">Открыто соединений сейчас</div>
				<div class="kpi-value mono">{snapshot?.totalConns ?? 0} <span class="kpi-unit">сесс.</span></div>
				<div class="kpi-hint">
					{snapshot?.totalClients ? `Активных устройств: ${snapshot.totalClients} · данные из таблицы соединений ядра` : 'Текущие открытые TCP/UDP-сессии через правила FIRC'}
				</div>
			</div>
			<div class="kpi-card">
				<div class="kpi-label">Объём в активных сессиях</div>
				<div class="kpi-value mono">{formatBytes(totalRxBytes + totalTxBytes)}</div>
				<div class="kpi-hint mono">
					↓ Получено: {formatBytes(totalRxBytes)} · ↑ Отправлено: {formatBytes(totalTxBytes)}
				</div>
			</div>
			<div class="kpi-card">
				<div class="kpi-label">DNS-обращений по правилам</div>
				<div class="kpi-value mono">{snapshot?.totalMatched ?? 0} <span class="kpi-unit">запр.</span></div>
				<div class="kpi-hint">
					Всего DNS-пакетов через FIRC (:3553): {snapshot?.totalDns ?? 0}
				</div>
			</div>
			<div class="kpi-card">
				<div class="kpi-label">Пакеты разметки правил</div>
				<div class="kpi-value mono">{totalRulePackets} <span class="kpi-unit">пкт</span></div>
				<div class="kpi-hint">
					↑ {formatBytes(totalRuleBytes)} · исходящие пакеты, попавшие под FakeIP и IP-подсети
				</div>
			</div>
		</div>

		<div class="mon-card">
			<div class="mon-card-head">
				<span>Подробная статистика по группам правил</span>
				<span class="mon-card-sub">Текущие сессии (conntrack), DNS за 24 ч и срабатывания правил</span>
			</div>
			<div class="mon-table-wrap">
				<table class="mon-table">
					<thead>
						<tr>
							<th>Группа и правила</th>
							<th>Выходной туннель</th>
							<th>Сейчас открыто (сессии и трафик)</th>
							<th>DNS-обращения и домены</th>
							<th>Разметка правил (исх.)</th>
						</tr>
					</thead>
					<tbody>
						{#each snapshot?.groups ?? [] as grp (grp.id)}
							{@const hasActivity = grp.matchedPackets > 0 || grp.activeConns > 0 || grp.matchedQueries > 0}
							<tr class:row-active={hasActivity}>
								<td>
									<div class="srv-cell">
										<StatusDot
											variant={grp.live ? 'success' : 'warning'}
											size="sm"
											pulse={grp.activeConns > 0}
										/>
										<div>
											<div class="srv-name">{grp.name}</div>
											{#if grp.fakeSubnet}
												<div class="srv-detail mono">FakeIP: {grp.fakeSubnet}</div>
											{/if}
											{#if grp.rulesTotal || grp.subnetRules}
												<div class="srv-detail">
													{grp.rulesTotal ? `Правил: ${grp.rulesTotal}` : ''}{grp.subnetRules ? `${grp.rulesTotal ? ' · ' : ''}IP-подсетей: ${grp.subnetRules}` : ''}
												</div>
											{/if}
										</div>
									</div>
								</td>
								<td>
									<div class="outbound-cell">
										{#if grp.activeOutboundLabel && grp.activeOutboundLabel !== grp.activeOutbound}
											<div class="srv-name">{grp.activeOutboundLabel}</div>
										{/if}
										<Badge variant={grp.live ? 'info' : 'warning'} size="xs" mono>
											{grp.activeOutbound || '—'}
										</Badge>
									</div>
								</td>
								<td class="mono">
									{#if grp.activeConns > 0}
										<div>
											<span class="conn-pill">{grp.activeConns} сесс.</span>
											{#if grp.activeClients}
												<span class="bytes-meta"> · {grp.activeClients} устр.</span>
											{/if}
										</div>
										<div class="bytes-meta">↓ Получено: <strong>{formatBytes(grp.connRxBytes)}</strong></div>
										<div class="bytes-meta">↑ Отправлено: <strong>{formatBytes(grp.connTxBytes)}</strong></div>
									{:else}
										<span class="muted-meta">Нет активных сессий</span>
									{/if}
								</td>
								<td>
									{#if grp.matchedQueries > 0}
										<div class="mono">
											<strong>{grp.matchedQueries}</strong> запр.{grp.uniqueDomains ? ` · ${grp.uniqueDomains} дом.` : ''}
										</div>
										{#if grp.lastDomain}
											<div class="srv-detail mono">Посл.: {grp.lastDomain} ({formatTime(grp.lastAt)})</div>
										{/if}
										{#if grp.topDomains && grp.topDomains.length > 0}
											<div class="top-domains-list">
												{#each grp.topDomains as td (td.domain)}
													<span class="top-domain-chip mono">{td.domain} ×{td.queries}</span>
												{/each}
											</div>
										{/if}
									{:else if (grp.subnetRules || grp.activeConns > 0) && (grp.activeConns > 0 || grp.matchedPackets > 0)}
										<div class="srv-detail">По IP-подсетям (без DNS)</div>
										<div class="muted-meta">Прямые IP-соединения приложений</div>
									{:else}
										<div class="mono muted-meta">0 запр.</div>
									{/if}
								</td>
								<td class="mono">
									<div>{grp.matchedPackets} пкт</div>
									<div class="bytes-meta">↑ {formatBytes(grp.matchedBytes)}</div>
									<div class="muted-meta">исх. пакеты по правилам</div>
								</td>
							</tr>
						{/each}
					</tbody>
				</table>
			</div>
		</div>

		<div class="counters-legend">
			<div><strong>Сейчас открыто (сессии и трафик)</strong> — реальный объём данных (<code>↓ Получено</code> и <code>↑ Отправлено</code>) по открытым в данный момент соединениям в таблице ядра <code>conntrack</code>.</div>
			<div><strong>DNS-обращения и домены</strong> — сколько раз устройства запрашивали домены группы (за 24 ч) и какие сайты открывались чаще всего. Если мессенджер (например, Telegram) подключается напрямую по IP-адресам, трафик идёт по IP-подсетям без DNS-запросов.</div>
			<div><strong>Разметка правил (исх.)</strong> — счётчик исходящих пакетов в <code>iptables mangle</code>, попавших под правила группы с момента последнего применения правил.</div>
		</div>
		{/if}
	</div>
	{/if}

	<!-- Section 3: Live Matched Feed -->
	{#if view === 'events'}
	<div class="mon-card">
		<div class="mon-card-head">
			<span>Последние события маршрутизации</span>
			<div class="feed-filters">
				<div class="feed-search">
					<Search size={13} />
					<input
						type="text"
						placeholder="Поиск по домену или IP клиента…"
						bind:value={searchFilter}
					/>
				</div>
				<select class="feed-select" bind:value={sourceFilter}>
					<option value="all">Все источники ({snapshot?.recentMatches?.length ?? 0})</option>
					{#each snapshot?.servers ?? [] as srv (srv.id)}
						<option value={srv.id}>{srv.name}</option>
					{/each}
				</select>
			</div>
		</div>

		{#if filteredMatches.length === 0}
			<div class="feed-empty">
				Пока нет зафиксированных срабатываний по выбранному фильтру. Нажмите на любой домен выше («Проверить») или откройте сайт на подключённом устройстве.
			</div>
		{:else}
			<div class="feed-list">
				{#each filteredMatches as ev (ev.seq)}
					<div class="feed-item">
						<div class="feed-row">
							<span class="feed-time mono">{formatTime(ev.at)}</span>
							<Badge variant={ev.sourceId === 'lan_br0' ? 'default' : 'accent'} size="xs">
								{ev.sourceName}
							</Badge>
							<span class="feed-client mono">{ev.clientIp}</span>
							<ArrowRight size={12} class="feed-arrow" />
							<span class="feed-domain mono">{ev.domain}</span>
							{#if ev.fakeIp}
								<span class="feed-fake mono">
									({ev.fakeIp}{ev.realIp ? ` → ${ev.realIp}` : ''})
								</span>
							{/if}
							<ArrowRight size={12} class="feed-arrow" />
							<Badge variant="success" size="xs">{ev.groupName || ev.groupId}</Badge>
							{#if ev.outbound}
								<ArrowRight size={12} class="feed-arrow" />
								<Badge variant="info" size="xs" mono>{formatOutboundLabel(ev.outbound, ev.outboundLabel)}</Badge>
							{/if}
							<button
								type="button"
								class="feed-inspect-btn"
								class:active={inspectedEventSeq === ev.seq}
								aria-label={m.ip_inspect_toggle_aria()}
								aria-expanded={inspectedEventSeq === ev.seq}
								onclick={() => (inspectedEventSeq = inspectedEventSeq === ev.seq ? null : ev.seq)}
							>
								<Info size={12} aria-hidden="true" />
							</button>
						</div>
						{#if inspectedEventSeq === ev.seq}
							<div class="feed-inspect-wrap">
								<IpInspectSection
									ip={ev.realIp || ev.fakeIp || ''}
									domain={ev.domain}
									compact
									onOpenQuickAdd={openQuickAdd}
								/>
							</div>
						{/if}
					</div>
				{/each}
			</div>
		{/if}
	</div>
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
	.domain-checker summary { cursor: pointer; font-size: .8rem; color: var(--text-secondary); padding: .3rem 0; }
	.domain-checker[open] summary { margin-bottom: .5rem; }
	.counters-kpi-grid {
		display: grid;
		grid-template-columns: repeat(auto-fit, minmax(200px, 1fr));
		gap: 0.6rem;
		min-width: 0;
	}
	.kpi-card {
		display: flex;
		flex-direction: column;
		gap: 0.2rem;
		padding: 0.65rem 0.8rem;
		border-radius: 8px;
		background: var(--bg-primary, rgba(0, 0, 0, 0.14));
		border: 1px solid var(--border);
		min-width: 0;
	}
	.kpi-label {
		font-size: 0.72rem;
		font-weight: 600;
		color: var(--text-secondary);
	}
	.kpi-value {
		font-size: 1.05rem;
		font-weight: 700;
		color: var(--text-primary);
	}
	.kpi-unit {
		font-size: 0.78rem;
		font-weight: 500;
		color: var(--text-secondary);
	}
	.kpi-hint {
		font-size: 0.7rem;
		color: var(--text-muted);
		line-height: 1.35;
	}
	.outbound-cell {
		display: flex;
		flex-direction: column;
		align-items: flex-start;
		gap: 0.2rem;
	}
	.top-domains-list {
		display: flex;
		flex-wrap: wrap;
		gap: 0.25rem;
		margin-top: 0.25rem;
	}
	.top-domain-chip {
		display: inline-block;
		padding: 1px 6px;
		border-radius: 4px;
		font-size: 0.68rem;
		background: var(--bg-secondary);
		border: 1px solid var(--border);
		color: var(--text-secondary);
		max-width: 220px;
		overflow: hidden;
		text-overflow: ellipsis;
		white-space: nowrap;
	}
	.counters-legend {
		display: flex;
		flex-direction: column;
		gap: 0.3rem;
		padding: 0.65rem 0.8rem;
		border-radius: 8px;
		background: var(--bg-primary, rgba(0, 0, 0, 0.1));
		border: 1px solid var(--border);
		font-size: 0.74rem;
		color: var(--text-muted);
		line-height: 1.4;
	}
	.counters-legend strong {
		color: var(--text-secondary);
	}
	.counters-legend code {
		font-family: var(--font-mono);
		font-size: 0.7rem;
		color: var(--text-secondary);
	}
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

	.checker-box {
		display: flex;
		flex-direction: column;
		gap: 0.55rem;
		padding: 0.75rem;
		border-radius: 8px;
		background: var(--bg-tertiary, rgba(0, 0, 0, 0.16));
		border: 1px solid var(--border);
	}

	.checker-top {
		display: flex;
		align-items: center;
		justify-content: space-between;
		gap: 0.5rem;
		flex-wrap: wrap;
	}

	.checker-label {
		display: inline-flex;
		align-items: center;
		gap: 0.4rem;
		font-size: 0.8rem;
		font-weight: 500;
		color: var(--text-secondary);
	}

	.checker-quick-chips {
		display: flex;
		align-items: center;
		gap: 0.35rem;
		flex-wrap: wrap;
	}

	.quick-chip {
		padding: 2px 8px;
		border-radius: 999px;
		font-family: var(--font-mono);
		font-size: 0.72rem;
		background: var(--bg-secondary);
		border: 1px solid var(--border);
		color: var(--text-secondary);
		cursor: pointer;
		transition: all 0.15s ease;
		max-width: 100%;
		overflow: hidden;
		text-overflow: ellipsis;
		white-space: nowrap;
	}

	.quick-chip:hover,
	.quick-chip.active {
		border-color: var(--accent);
		color: var(--text-primary);
		background: color-mix(in srgb, var(--accent) 14%, var(--bg-secondary));
	}

	.checker-form {
		display: flex;
		gap: 0.5rem;
		flex-wrap: wrap;
		min-width: 0;
	}

	.checker-input {
		flex: 1 1 180px;
		min-width: 0;
		height: 32px;
		padding: 0 0.65rem;
		border-radius: 6px;
		border: 1px solid var(--border);
		background: var(--bg-primary, var(--bg-secondary));
		color: var(--text-primary);
		font-family: var(--font-mono);
		font-size: 0.8rem;
	}

	.checker-res {
		display: flex;
		align-items: center;
		gap: 0.45rem;
		flex-wrap: wrap;
		padding: 0.5rem 0.65rem;
		border-radius: 6px;
		font-size: 0.8rem;
		background: rgba(224, 175, 104, 0.1);
		border: 1px solid rgba(224, 175, 104, 0.35);
	}

	.checker-res.is-routed {
		background: rgba(158, 206, 106, 0.1);
		border-color: rgba(158, 206, 106, 0.35);
	}

	.checker-res.is-error {
		background: rgba(247, 118, 142, 0.1);
		border-color: rgba(247, 118, 142, 0.35);
	}

	.mon-grid-2 {
		display: grid;
		grid-template-columns: minmax(0, 1fr);
		gap: 0.75rem;
		min-width: 0;
		max-width: 100%;
	}

	.mon-card {
		display: flex;
		flex-direction: column;
		border-radius: 8px;
		background: var(--bg-primary, rgba(0, 0, 0, 0.14));
		border: 1px solid var(--border);
		overflow: hidden;
		min-width: 0;
		max-width: 100%;
	}

	.mon-card-head {
		display: flex;
		align-items: center;
		justify-content: space-between;
		gap: 0.5rem;
		padding: 0.55rem 0.75rem;
		border-bottom: 1px solid var(--border);
		font-size: 0.8rem;
		font-weight: 600;
		color: var(--text-primary);
		flex-wrap: wrap;
	}

	.mon-card-sub {
		font-size: 0.72rem;
		font-weight: 400;
		color: var(--text-muted);
	}

	.mon-table-wrap {
		overflow-x: auto;
	}

	.mon-table {
		width: 100%;
		border-collapse: collapse;
		font-size: 0.78rem;
	}

	.mon-table th {
		text-align: left;
		padding: 0.45rem 0.65rem;
		font-size: 0.7rem;
		font-weight: 600;
		text-transform: uppercase;
		color: var(--text-muted);
		border-bottom: 1px solid var(--border);
		white-space: nowrap;
	}

	.mon-table td {
		padding: 0.48rem 0.65rem;
		border-bottom: 1px solid color-mix(in srgb, var(--border) 60%, transparent);
		vertical-align: middle;
	}

	.mon-table tr:last-child td {
		border-bottom: none;
	}

	.mon-table tr.row-active {
		background: color-mix(in srgb, var(--color-success, #9ece6a) 5%, transparent);
	}

	.srv-cell {
		display: flex;
		align-items: center;
		gap: 0.5rem;
	}

	.srv-name {
		font-weight: 500;
		color: var(--text-primary);
	}

	.srv-detail {
		font-size: 0.7rem;
		color: var(--text-muted);
	}

	.mono {
		font-family: var(--font-mono);
	}

	.conn-pill {
		display: inline-block;
		padding: 1px 6px;
		border-radius: 4px;
		font-size: 0.72rem;
		background: color-mix(in srgb, var(--color-success, #9ece6a) 15%, transparent);
		color: var(--color-success, #9ece6a);
	}

	.bytes-meta,
	.muted-meta {
		font-size: 0.71rem;
		color: var(--text-muted);
	}

	.last-match {
		display: flex;
		align-items: center;
		gap: 0.35rem;
		flex-wrap: wrap;
	}

	.domain-text {
		color: var(--text-primary);
		font-weight: 500;
	}

	.feed-filters {
		display: flex;
		align-items: center;
		gap: 0.45rem;
		flex-wrap: wrap;
		min-width: 0;
		max-width: 100%;
	}

	.feed-search {
		display: inline-flex;
		align-items: center;
		gap: 0.35rem;
		padding: 0 0.5rem;
		height: 28px;
		border-radius: 6px;
		border: 1px solid var(--border);
		background: var(--bg-secondary);
		color: var(--text-muted);
		min-width: 0;
		max-width: 100%;
		flex: 1 1 180px;
	}

	.feed-search input {
		border: none;
		background: transparent;
		color: var(--text-primary);
		font-size: 0.75rem;
		outline: none;
		width: 100%;
		min-width: 0;
	}

	.feed-select {
		height: 28px;
		padding: 0 0.5rem;
		border-radius: 6px;
		border: 1px solid var(--border);
		background: var(--bg-secondary);
		color: var(--text-primary);
		font-size: 0.75rem;
		min-width: 0;
		max-width: 100%;
	}

	.feed-empty {
		padding: 1rem;
		text-align: center;
		font-size: 0.78rem;
		color: var(--text-muted);
	}

	.feed-list {
		display: flex;
		flex-direction: column;
		max-height: 240px;
		overflow-y: auto;
	}

	.feed-row {
		display: flex;
		align-items: center;
		gap: 0.45rem;
		padding: 0.38rem 0.75rem;
		border-bottom: 1px solid color-mix(in srgb, var(--border) 50%, transparent);
		font-size: 0.76rem;
		flex-wrap: wrap;
	}

	.feed-row:last-child {
		border-bottom: none;
	}

	.feed-time {
		color: var(--text-muted);
		font-size: 0.72rem;
	}

	.feed-client {
		color: var(--text-secondary);
		font-size: 0.73rem;
	}

	.feed-domain {
		color: var(--text-primary);
		font-weight: 600;
	}

	.feed-fake {
		color: var(--text-muted);
		font-size: 0.71rem;
	}

	.feed-item {
		border-bottom: 1px solid color-mix(in srgb, var(--border) 50%, transparent);
	}

	.feed-item:last-child {
		border-bottom: none;
	}

	.feed-item .feed-row {
		border-bottom: none;
	}

	.feed-inspect-btn {
		all: unset;
		margin-left: auto;
		display: inline-flex;
		align-items: center;
		justify-content: center;
		width: 22px;
		height: 22px;
		border-radius: 4px;
		color: var(--text-muted);
		cursor: pointer;
	}

	.feed-inspect-btn:hover,
	.feed-inspect-btn.active {
		color: var(--text-primary);
		background: var(--color-bg-hover, rgba(255, 255, 255, 0.08));
	}

	.feed-inspect-wrap {
		padding: 0 0.75rem 0.6rem;
	}
</style>
