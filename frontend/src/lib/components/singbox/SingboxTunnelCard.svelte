<script lang="ts">
	import type { SingboxTunnel } from '$lib/types';
	import { goto } from '$app/navigation';
	import { browser } from '$app/environment';
	import { api } from '$lib/api/client';
	import {
		singboxTunnels,
		singboxDelayHistory,
		singboxTraffic,
		triggerDelayCheck,
	} from '$lib/stores/singbox';
	import { onMount, untrack } from 'svelte';
	import { Modal, Button, TrafficChart, TrafficSparkline, PingButton } from '$lib/components/ui';
	import { getTrafficRates, subscribeTraffic, loadHistory } from '$lib/stores/traffic';
	import { singboxDelayFromHistory } from '$lib/utils/singboxDelay';
	import type { SingboxLayoutMode } from '$lib/constants/singboxLayout';
	import TunnelDiagnosticsModal from '$lib/components/testing/TunnelDiagnosticsModal.svelte';

	interface Props {
		tunnel: SingboxTunnel;
		layout?: SingboxLayoutMode;
		autoDelayCheckNonce?: number;
		autoDelayCheckDelayMs?: number;
		ondetail?: (tag: string) => void;
	}

	let {
		tunnel,
		layout = 'compact',
		autoDelayCheckNonce = 0,
		autoDelayCheckDelayMs = 0,
		ondetail,
	}: Props = $props();

	let deleting = $state(false);
	let confirmDeleteOpen = $state(false);
	let diagnosticsOpen = $state(false);
	let showServer = $state(false);
	let checking = $state(false);

	const history = $derived($singboxDelayHistory.get(tunnel.tag) ?? []);
	const delayPresentation = $derived(
		singboxDelayFromHistory(history, { running: tunnel.running !== false }),
	);
	const latest = $derived(delayPresentation.latest);
	const positiveHistory = $derived(history.filter((v) => v > 0));
	const avg = $derived(
		positiveHistory.length > 0
			? Math.round(positiveHistory.reduce((s, v) => s + v, 0) / positiveHistory.length)
			: 0,
	);
	const traffic = $derived($singboxTraffic.get(tunnel.tag));

	const trafficSparkSeries = $derived.by(() => {
		const n = Math.min(rxRates.length, txRates.length);
		if (n === 0) return { rx: [] as number[], tx: [] as number[] };
		const take = Math.min(36, n);
		const start = n - take;
		return {
			rx: rxRates.slice(start, n),
			tx: txRates.slice(start, n),
		};
	});

	const cardState = $derived(delayPresentation.state);
	const latText = $derived(delayPresentation.label);

	const protocolLabel = $derived.by(() => {
		// Widen locally: the generated/static type is currently an exhaustive union,
		// but runtime data may contain a newer sing-box protocol before frontend types
		// are updated. Keep a safe fallback without making the default branch `never`.
		const protocol = tunnel.protocol as string | undefined;

		switch (protocol) {
			case 'vless':
				return 'VLESS';
			case 'hysteria2':
				return 'Hysteria2';
			case 'trojan':
				return 'Trojan';
			case 'shadowsocks':
				return 'Shadowsocks';
			case 'naive':
				return 'Naive';
			default:
				return protocol ? protocol.charAt(0).toUpperCase() + protocol.slice(1) : '—';
		}
	});

	async function triggerCheck(): Promise<void> {
		if (checking) return;
		checking = true;
		try {
			await triggerDelayCheck(tunnel.tag);
		} finally {
			checking = false;
		}
	}

	let lastAutoDelayCheckNonce = 0;
	$effect(() => {
		const nonce = autoDelayCheckNonce;
		const delay = autoDelayCheckDelayMs;
		if (nonce <= 0 || nonce === lastAutoDelayCheckNonce) return;
		lastAutoDelayCheckNonce = nonce;
		if (tunnel.running !== true) return;

		const timer = setTimeout(() => {
			untrack(() => void triggerCheck());
		}, delay);
		return () => clearTimeout(timer);
	});

	async function remove(): Promise<void> {
		deleting = true;
		confirmDeleteOpen = false;
		try {
			const fresh = await api.singboxDeleteTunnel(tunnel.tag);
			// Instant update — beats waiting for the poll or SSE hint refetch.
			singboxTunnels.applyMutationResponse(fresh);
		} finally {
			deleting = false;
		}
	}

	function edit(): void {
		goto(`/singbox/${encodeURIComponent(tunnel.tag)}`);
	}

	function formatBytes(n: number): string {
		if (n < 1024) return `${n} B`;
		if (n < 1024 * 1024) return `${(n / 1024).toFixed(1)} KB`;
		if (n < 1024 * 1024 * 1024) return `${(n / (1024 * 1024)).toFixed(1)} MB`;
		return `${(n / (1024 * 1024 * 1024)).toFixed(1)} GB`;
	}

	// ─── Traffic sparkline (rate history fed by +layout SSE handler) ─
	let rxRates = $state<number[]>([]);
	let txRates = $state<number[]>([]);
	let tunnelTag = $derived(tunnel.tag);

	$effect(() => {
		const tag = tunnelTag;
		const update = () => {
			const t = getTrafficRates(tag);
			rxRates = t.rx;
			txRates = t.tx;
		};
		update();
		return subscribeTraffic(update);
	});

	let initialLoadDone = false;
	$effect(() => {
		const tag = tunnelTag;
		if (initialLoadDone) return;
		initialLoadDone = true;
		untrack(() => loadHistory(tag));
	});

	const CHART_KEY_PREFIX = 'sbx_chart_expanded_';
	let chartStorageKey = $derived(`${CHART_KEY_PREFIX}${tunnel.tag}`);
	let chartExpanded = $state(true);
	onMount(() => {
		chartExpanded = localStorage.getItem(chartStorageKey) !== 'false';
	});
	function toggleCharts() {
		chartExpanded = !chartExpanded;
		if (browser) {
			localStorage.setItem(chartStorageKey, String(chartExpanded));
		}
	}
</script>

{#if layout === 'list'}
	<div
		class="sbx-tunnel-list-row"
		class:ok={cardState === 'ok'}
		class:slow={cardState === 'slow'}
		class:fail={cardState === 'fail'}
		class:unknown={cardState === 'unknown'}
		class:stopped={cardState === 'stopped'}
	>
		<div class="list-cell list-cell-delay" data-label="Delay">
			<span class="dot {cardState}" aria-hidden="true"></span>
			<PingButton
				label={latText}
				state={cardState}
				{checking}
				onclick={triggerCheck}
			/>
		</div>
		<div class="list-cell list-cell-name" data-label="Туннель">
			<button type="button" class="name-btn" onclick={edit}>{tunnel.tag}</button>
			<div class="list-sub mono">
				{tunnel.proxyInterface}
				{#if tunnel.kernelInterface}<span> · {tunnel.kernelInterface}</span>{/if}
			</div>
		</div>
		<div class="list-cell list-cell-badges" data-label="Протокол">
			<div class="badges-inline">
				<span class="badge b-{tunnel.protocol}">{protocolLabel}</span>
				{#if tunnel.security === 'reality'}
					<span class="badge b-reality">Reality</span>
				{:else if tunnel.security === 'tls'}
					<span class="badge b-tls">TLS</span>
				{/if}
				<span class="badge b-transport">{tunnel.transport.toUpperCase()}</span>
			</div>
		</div>
		<div class="list-cell list-cell-server" data-label="Сервер">
			<div class="server-line">
				{#if showServer}
					<span class="mono">{tunnel.server}</span>
				{:else}
					<span class="muted">••••••••</span>
				{/if}
				<button
					type="button"
					class="eye-inline"
					onclick={() => (showServer = !showServer)}
					aria-label={showServer ? 'Скрыть' : 'Показать'}
				>
					{#if showServer}
						<svg width="12" height="12" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><path d="M1 12s4-8 11-8 11 8 11 8-4 8-11 8-11-8-11-8z"/><circle cx="12" cy="12" r="3"/></svg>
					{:else}
						<svg width="12" height="12" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><path d="M17.94 17.94A10.07 10.07 0 0 1 12 20c-7 0-11-8-11-8a18.45 18.45 0 0 1 5.06-5.94M9.9 4.24A9.12 9.12 0 0 1 12 4c7 0 11 8 11 8a18.5 18.5 0 0 1-2.16 3.19m-6.72-1.07a3 3 0 1 1-4.24-4.24"/><line x1="1" y1="1" x2="23" y2="23"/></svg>
					{/if}
				</button>
				<span class="mono">:{tunnel.port}</span>
			</div>
		</div>
		<div class="list-cell list-cell-run" data-label="Процесс">
			<span class="run-pill" class:run-on={tunnel.running === true}>{tunnel.running === true ? 'running' : 'stopped'}</span>
		</div>
		<div class="list-cell list-cell-traffic" data-label="Трафик">
			<div class="traffic-row-list">
				<div
					role="button"
					tabindex="0"
					class="traffic-mini-click"
					onclick={() => ondetail?.(tunnel.tag)}
					onkeydown={(e) => {
						if (e.key === 'Enter' || e.key === ' ') {
							e.preventDefault();
							ondetail?.(tunnel.tag);
						}
					}}
					title="Открыть детальный график"
				>
					<TrafficSparkline
						rxData={trafficSparkSeries.rx}
						txData={trafficSparkSeries.tx}
						width={84}
						height={22}
					/>
				</div>
				<div class="traffic-mini-col mono">
					<span class="traffic-rate rx">↓ {formatBytes(traffic?.download ?? 0)}</span>
					<span class="traffic-rate tx">↑ {formatBytes(traffic?.upload ?? 0)}</span>
				</div>
			</div>
		</div>
		<div class="list-cell list-cell-ping-mini" data-label="Ping">
			<div
				class="spark-mini spark {cardState}"
				onclick={triggerCheck}
				onkeydown={(e) => (e.key === 'Enter' || e.key === ' ') && triggerCheck()}
				role="button"
				tabindex="0"
				title="Клик — обновить delay"
			>
				{#if history.length === 0}
					{#each Array(10) as _, i (i)}
						<div class="bar empty"></div>
					{/each}
				{:else}
					{@const max = Math.max(...history.map((v) => (v <= 0 ? 100 : v)), 100)}
					{#each history.slice(-14) as d, i (i)}
						<div class="bar" style="height: {Math.max((d <= 0 ? max : d) / max, 0.08) * 100}%;"></div>
					{/each}
				{/if}
			</div>
		</div>
		<div class="list-cell list-cell-actions" data-label="Действия">
			<div class="list-actions">
				<button
					class="action-btn"
					type="button"
					onclick={edit}
					title="Изменить туннель «{tunnel.tag}»"
					aria-label="Изменить туннель «{tunnel.tag}»"
				>
					<svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2">
						<path d="M11 4H4a2 2 0 0 0-2 2v14a2 2 0 0 0 2 2h14a2 2 0 0 0 2-2v-7"/>
						<path d="M18.5 2.5a2.121 2.121 0 0 1 3 3L12 15l-4 1 1-4 9.5-9.5z"/>
					</svg>
				</button>
				<button
					class="action-btn action-test"
					type="button"
					onclick={() => (diagnosticsOpen = true)}
					disabled={!tunnel.kernelInterface}
					title="Тест туннеля «{tunnel.tag}»"
					aria-label="Тест туннеля «{tunnel.tag}»"
				>
					<svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2">
						<path d="M22 11.08V12a10 10 0 1 1-5.93-9.14"/>
						<polyline points="22,4 12,14.01 9,11.01"/>
					</svg>
				</button>
				<button
					class="action-btn action-danger"
					type="button"
					onclick={() => (confirmDeleteOpen = true)}
					disabled={deleting}
					title="Удалить туннель «{tunnel.tag}»"
					aria-label="Удалить туннель «{tunnel.tag}»"
				>
					{#if deleting}
						<span class="action-spinner"></span>
					{:else}
						<svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2">
							<polyline points="3,6 5,6 21,6"/>
							<path d="M19 6v14a2 2 0 0 1-2 2H7a2 2 0 0 1-2-2V6m3 0V4a2 2 0 0 1 2-2h4a2 2 0 0 1 2 2v2"/>
						</svg>
					{/if}
				</button>
			</div>
		</div>
	</div>
{:else if layout === 'dense'}
<div
	class="card view-dense"
	class:ok={cardState === 'ok'}
	class:slow={cardState === 'slow'}
	class:fail={cardState === 'fail'}
	class:unknown={cardState === 'unknown'}
	class:stopped={cardState === 'stopped'}
>
	<div class="header header-dense">
		<div class="header-dense-body">
			<div class="title-row-dense">
				<button type="button" class="title title-dense" onclick={edit}>{tunnel.tag}</button>
			</div>
			<div class="meta-tags-dense">
				<span class="iface-dense" title="{tunnel.proxyInterface}{tunnel.kernelInterface ? ` · ${tunnel.kernelInterface}` : ''}">
					<span>{tunnel.proxyInterface}</span>
					{#if tunnel.kernelInterface}<span class="meta-dot" aria-hidden="true">·</span><span>{tunnel.kernelInterface}</span>{/if}
				</span>
				<span class="badge b-{tunnel.protocol}">{protocolLabel}</span>
				{#if tunnel.security === 'reality'}
					<span class="badge b-reality">Reality</span>
				{:else if tunnel.security === 'tls'}
					<span class="badge b-tls">TLS</span>
				{/if}
				<span class="badge b-transport">{tunnel.transport.toUpperCase()}</span>
			</div>
		</div>
		<div class="dense-toolbar">
			<div class="dense-toolbar-top">
				<span class="dot {cardState}" aria-hidden="true"></span>
			</div>
			<div class="dense-toolbar-bottom">
				<PingButton label={latText} state={cardState} {checking} size="sm" onclick={triggerCheck} />
			</div>
		</div>
	</div>

	<div class="details">
	<div class="details-dense-cols">
		<div class="details-dense-col">
			<div class="kv-stacked-stat">
				<span class="kv-stacked-label">Сервер</span>
				<span class="kv-endpoint">
					<span class="kv-stacked-value" title={showServer ? tunnel.server : ''}>
						{showServer ? tunnel.server : '••••••••'}
					</span>
					<button class="icon-btn" onclick={() => (showServer = !showServer)} aria-label={showServer ? 'Скрыть' : 'Показать'}>
						<svg width="12" height="12" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2">
							{#if showServer}
								<path d="M1 12s4-8 11-8 11 8 11 8-4 8-11 8-11-8-11-8z"/><circle cx="12" cy="12" r="3"/>
							{:else}
								<path d="M17.94 17.94A10.07 10.07 0 0 1 12 20c-7 0-11-8-11-8a18.45 18.45 0 0 1 5.06-5.94M9.9 4.24A9.12 9.12 0 0 1 12 4c7 0 11 8 11 8a18.5 18.5 0 0 1-2.16 3.19m-6.72-1.07a3 3 0 1 1-4.24-4.24"/><line x1="1" y1="1" x2="23" y2="23"/>
							{/if}
						</svg>
					</button>
				</span>
			</div>
			{#if tunnel.protocol === 'naive'}
				<div class="kv-stacked-stat">
					<span class="kv-stacked-label">Логин</span>
					<span class="kv-stacked-value">{tunnel.username || '—'}</span>
				</div>
			{:else if tunnel.sni}
				<div class="kv-stacked-stat">
					<span class="kv-stacked-label">SNI</span>
					<span class="kv-stacked-value">{showServer ? tunnel.sni : '••••••••'}</span>
				</div>
			{/if}
		</div>
		<div class="details-dense-col details-dense-col-right">
			<div class="kv-stacked-stat">
				<span class="kv-stacked-label">Порт</span>
				<span class="kv-stacked-value">:{tunnel.port}</span>
			</div>
			<div class="kv-stacked-stat">
				<span class="kv-stacked-label">Delay</span>
				<span class="kv-stacked-value">
					{#if cardState === 'unknown'}—{:else if cardState === 'fail'}fail{:else}avg {avg}ms{/if}
				</span>
			</div>
		</div>
	</div>
	</div>

	<div class="actions">
		<button class="action-btn" type="button" onclick={edit} title="Изменить туннель «{tunnel.tag}»" aria-label="Изменить туннель «{tunnel.tag}»">
			<svg width="12" height="12" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2">
				<path d="M11 4H4a2 2 0 0 0-2 2v14a2 2 0 0 0 2 2h14a2 2 0 0 0 2-2v-7"/>
				<path d="M18.5 2.5a2.121 2.121 0 0 1 3 3L12 15l-4 1 1-4 9.5-9.5z"/>
			</svg>
			Изменить
		</button>
		<button
			class="action-btn action-test"
			type="button"
			disabled={!tunnel.kernelInterface}
			title="Тест туннеля «{tunnel.tag}»"
			aria-label="Тест туннеля «{tunnel.tag}»"
			onclick={() => (diagnosticsOpen = true)}
		>
			<svg width="12" height="12" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2">
				<path d="M22 11.08V12a10 10 0 1 1-5.93-9.14"/>
				<polyline points="22,4 12,14.01 9,11.01"/>
			</svg>
			Тест
		</button>
		<button
			class="action-btn action-danger"
			type="button"
			onclick={() => (confirmDeleteOpen = true)}
			disabled={deleting}
			title="Удалить туннель «{tunnel.tag}»"
			aria-label="Удалить туннель «{tunnel.tag}»"
		>
			{#if deleting}
				<span class="action-spinner"></span>
			{:else}
				<svg width="12" height="12" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2">
					<polyline points="3,6 5,6 21,6"/>
					<path d="M19 6v14a2 2 0 0 1-2 2H7a2 2 0 0 1-2-2V6m3 0V4a2 2 0 0 1 2-2h4a2 2 0 0 1 2 2v2"/>
				</svg>
			{/if}
			Удалить
		</button>
	</div>

	<div class="charts-dense">
		<button
			type="button"
			class="traffic-inline"
			onclick={() => ondetail?.(tunnel.tag)}
			title="Открыть график трафика"
		>
			<TrafficSparkline
				rxData={trafficSparkSeries.rx}
				txData={trafficSparkSeries.tx}
				width={42}
				height={20}
			/>
			<div class="traffic-inline-rates">
				<span class="traffic-inline-rate rx">↓ {formatBytes(traffic?.download ?? 0)}</span>
				<span class="traffic-inline-rate tx">↑ {formatBytes(traffic?.upload ?? 0)}</span>
			</div>
		</button>
		<div class="chart-inline delay-inline">
			<div class="chart-inline-head">
				<span class="chart-inline-label">Delay</span>
				<span class="chart-inline-stats">
					{#if cardState === 'unknown'}
						ещё не тестировали
					{:else if cardState === 'fail'}
						<span class="err">не отвечает</span>
					{:else}
						avg {avg}ms
					{/if}
				</span>
			</div>
			<button
				type="button"
				class="spark-mini spark {cardState}"
				onclick={triggerCheck}
				title="Клик — обновить delay"
				aria-label="Обновить delay"
			>
				{#if history.length === 0}
					{#each Array(10) as _, i (i)}
						<div class="bar empty"></div>
					{/each}
				{:else}
					{@const max = Math.max(...history.map((v) => (v <= 0 ? 100 : v)), 100)}
					{#each history.slice(-14) as d, i (i)}
						<div class="bar" style="height: {Math.max((d <= 0 ? max : d) / max, 0.08) * 100}%;"></div>
					{/each}
				{/if}
			</button>
		</div>
	</div>
</div>
{:else}
<div
	class="card view-compact"
	class:ok={cardState === 'ok'}
	class:slow={cardState === 'slow'}
	class:fail={cardState === 'fail'}
	class:unknown={cardState === 'unknown'}
	class:stopped={cardState === 'stopped'}
>
	<div class="led-wrap">
		<span class="dot {cardState}" aria-hidden="true"></span>
		<PingButton label={latText} state={cardState} {checking} onclick={triggerCheck} />
	</div>

	<h3 class="title">{tunnel.tag}</h3>
	<div class="iface">
		<span>{tunnel.proxyInterface}</span>
		{#if tunnel.kernelInterface}
			<span class="meta-dot" aria-hidden="true">·</span><span>{tunnel.kernelInterface}</span>
		{/if}
	</div>

	<div class="badges">
		<span class="badge b-{tunnel.protocol}">{protocolLabel}</span>
		{#if tunnel.security === 'reality'}
			<span class="badge b-reality">Reality</span>
		{:else if tunnel.security === 'tls'}
			<span class="badge b-tls">TLS</span>
		{/if}
		<span class="badge b-transport">{tunnel.transport.toUpperCase()}</span>
	</div>

	<div class="row">
		<span class="label">Сервер</span>
		<div class="server-row value">
			{#if showServer}
				<span class="server-text">{tunnel.server}</span>
			{:else}
				<span class="server-hidden">●●●●●●●●</span>
			{/if}
			<button class="icon-btn" onclick={() => (showServer = !showServer)} aria-label={showServer ? 'Скрыть' : 'Показать'}>
				<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2">
					<path d="M1 12s4-8 11-8 11 8 11 8-4 8-11 8-11-8-11-8z"/>
					<circle cx="12" cy="12" r="3"/>
				</svg>
			</button>
			<span class="port">:{tunnel.port}</span>
		</div>
	</div>

	{#if tunnel.protocol === 'naive'}
		<div class="row">
			<span class="label">Логин</span>
			<span class="value">{tunnel.username || '—'}</span>
		</div>
	{:else if tunnel.sni}
		<div class="row">
			<span class="label">SNI</span>
			<span class="value">
				{#if showServer}
					{tunnel.sni}
				{:else}
					<span class="server-hidden">●●●●●●●●</span>
				{/if}
			</span>
		</div>
	{/if}

	<div class="actions">
		<button
			class="action-btn"
			type="button"
			onclick={edit}
			title="Изменить туннель «{tunnel.tag}»"
			aria-label="Изменить туннель «{tunnel.tag}»"
		>
			<svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2">
				<path d="M11 4H4a2 2 0 0 0-2 2v14a2 2 0 0 0 2 2h14a2 2 0 0 0 2-2v-7"/>
				<path d="M18.5 2.5a2.121 2.121 0 0 1 3 3L12 15l-4 1 1-4 9.5-9.5z"/>
			</svg>
			Изменить
		</button>
		<button
			class="action-btn action-test"
			type="button"
			disabled={!tunnel.kernelInterface}
			title="Тест туннеля «{tunnel.tag}»"
			aria-label="Тест туннеля «{tunnel.tag}»"
			onclick={() => (diagnosticsOpen = true)}
		>
			<svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2">
				<path d="M22 11.08V12a10 10 0 1 1-5.93-9.14"/>
				<polyline points="22,4 12,14.01 9,11.01"/>
			</svg>
			Тест
		</button>
		<button
			class="action-btn action-danger"
			type="button"
			onclick={() => (confirmDeleteOpen = true)}
			disabled={deleting}
			title="Удалить туннель «{tunnel.tag}»"
			aria-label="Удалить туннель «{tunnel.tag}»"
		>
			{#if deleting}
				<span class="action-spinner"></span>
			{:else}
				<svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2">
					<polyline points="3,6 5,6 21,6"/>
					<path d="M19 6v14a2 2 0 0 1-2 2H7a2 2 0 0 1-2-2V6m3 0V4a2 2 0 0 1 2-2h4a2 2 0 0 1 2 2v2"/>
				</svg>
			{/if}
			Удалить
		</button>
	</div>

	<div class="chart-section">
		<button type="button" class="chart-header" onclick={toggleCharts}>
			<span class="chart-label">Графики</span>
			<span class="chart-chevron" class:expanded={chartExpanded}>▾</span>
		</button>
		<div class="chart-body" class:expanded={chartExpanded}>
			<div class="chart-head">
				<span>Delay (5 мин)</span>
				<span class="stats">
					{#if cardState === 'unknown'}
						ещё не тестировали
					{:else if cardState === 'fail'}
						<span class="err">не отвечает</span>
					{:else}
						avg {avg}ms
					{/if}
				</span>
			</div>
			<div
				class="spark {cardState}"
				title="Delay за последние проверки"
			>
				{#if history.length === 0}
					{#each Array(6) as _}
						<div class="bar empty"></div>
					{/each}
				{:else}
					{@const max = Math.max(...history.map((v) => (v <= 0 ? 100 : v)), 100)}
					{#each history as d}
						<div class="bar" style="height: {Math.max((d <= 0 ? max : d) / max, 0.1) * 100}%;"></div>
					{/each}
				{/if}
			</div>
			<div class="chart-head traffic-head">
				<span>Трафик</span>
				<span class="stats">
					↓ {formatBytes(traffic?.download ?? 0)} · ↑ {formatBytes(traffic?.upload ?? 0)}
				</span>
			</div>
			<TrafficChart
				{rxRates}
				{txRates}
				rxTotal={traffic?.download ?? 0}
				txTotal={traffic?.upload ?? 0}
				height={56}
				onclick={() => ondetail?.(tunnel.tag)}
			/>
		</div>
	</div>
</div>
{/if}

<TunnelDiagnosticsModal
	open={diagnosticsOpen}
	kind="singbox"
	targetId={tunnel.tag}
	displayName={tunnel.tag}
	subjectLabel="туннель"
	iface={tunnel.kernelInterface}
	loading={false}
	unavailableReason={tunnel.kernelInterface ? undefined : 'У этого sing-box туннеля нет kernel interface, расширенные тесты недоступны.'}
	onclose={() => (diagnosticsOpen = false)}
/>

<Modal
	open={confirmDeleteOpen}
	title="Удаление"
	size="sm"
	onclose={() => (confirmDeleteOpen = false)}
>
	<p class="confirm-text">Удалить туннель <strong>{tunnel.tag}</strong>?</p>
	{#snippet actions()}
		<Button variant="ghost" size="md" onclick={() => (confirmDeleteOpen = false)}>Отмена</Button>
		<Button variant="danger" size="md" onclick={remove}>Удалить</Button>
	{/snippet}
</Modal>

<style>
	.card {
		display: flex;
		flex-direction: column;
		gap: 10px;
		padding: 12px 14px;
		background: var(--color-bg-secondary);
		border: 1px solid var(--color-border);
		border-radius: var(--radius);
		color: var(--color-text-primary);
		position: relative;
		transition: border-color var(--t-fast) ease;
	}
	.card.ok { border-color: var(--color-success-border); }
	.card.slow { border-color: var(--color-warning-border); }
	.card.fail { border-color: var(--color-error-border); }
	.card.unknown { border-color: var(--color-border); }
	.card.stopped { border-color: var(--color-muted-border); opacity: 0.7; }

	.card.view-dense {
		gap: 8px;
		padding: 10px 12px;
		position: relative;
	}

	.card.view-dense .header.header-dense {
		display: grid;
		grid-template-columns: minmax(0, 1fr) auto;
		align-items: flex-start;
		gap: 6px;
	}

	.header-dense-body {
		display: flex;
		flex-direction: column;
		gap: 1px;
		min-width: 0;
	}

	.title-row-dense {
		display: grid;
		grid-template-columns: minmax(0, 1fr);
		align-items: center;
		min-width: 0;
	}

	.title-dense {
		margin: 0;
		padding: 0;
		border: none;
		background: none;
		font: inherit;
		font-size: 13px;
		font-weight: 600;
		color: inherit;
		cursor: pointer;
		text-align: left;
		min-width: 0;
		overflow: hidden;
		text-overflow: ellipsis;
		white-space: nowrap;
	}

	.title-dense:hover {
		color: var(--color-accent);
	}

	.meta-tags-dense {
		display: flex;
		flex-wrap: wrap;
		align-items: center;
		margin-top: 3px;
		gap: 3px;
		min-width: 0;
	}

	.iface-dense {
		display: inline-flex;
		flex-wrap: wrap;
		align-items: center;
		font-size: 9px;
		font-weight: 500;
		font-family: var(--font-mono, monospace);
		color: var(--color-text-muted);
		min-width: 0;
	}

	.meta-dot {
		margin: 0 0.35em;
		opacity: 0.75;
	}

	.card.view-dense .meta-tags-dense .badge {
		font-size: 9px;
		padding: 1px 5px;
		line-height: 1.3;
	}

	.dense-toolbar {
		display: flex;
		flex-direction: column;
		align-items: flex-end;
		flex-shrink: 0;
	}

	.dense-toolbar-top {
		display: flex;
		align-items: center;
		gap: 8px;
	}

	.dense-toolbar-bottom {
		display: flex;
		align-items: center;
	}

	.card.view-dense .dense-toolbar-top .dot {
		width: 6px;
		height: 6px;
	}

	.details-dense-cols {
		display: grid;
		grid-template-columns: minmax(0, 1.2fr) 4.75rem;
		gap: 10px 10px;
		align-items: start;
	}

	.details-dense-col {
		display: flex;
		flex-direction: column;
		gap: 6px;
		min-width: 0;
	}

	.kv-stacked-stat {
		display: flex;
		flex-direction: column;
		gap: 1px;
		min-width: 0;
	}

	.card.view-dense .kv-endpoint {
		display: flex;
		align-items: center;
		gap: 2px;
		min-width: 0;
	}

	.kv-stacked-label {
		font-size: 9px;
		text-transform: uppercase;
		letter-spacing: 0.04em;
		color: var(--color-text-muted);
		line-height: 1.2;
	}

	.kv-stacked-value {
		font-size: 10px;
		font-family: var(--font-mono, monospace);
		color: var(--color-text-secondary);
		white-space: nowrap;
		overflow: hidden;
		text-overflow: ellipsis;
		line-height: 1.25;
	}

	.card.view-dense .actions {
		gap: 2px;
		justify-content: center;
		margin-top: 0;
		padding: 0;
		border: none;
	}

	.card.view-dense .action-btn {
		padding: 3px 6px;
		font-size: 10px;
		gap: 3px;
	}

	.card.view-dense .details {
		display: flex;
		flex-direction: column;
		gap: 6px;
		padding: 4px 0;
		border-top: 1px solid var(--color-border);
		border-bottom: 1px solid var(--color-border);
	}

	.charts-dense {
		display: flex;
		flex-direction: row;
		align-items: stretch;
		gap: 4px;
		width: 100%;
		min-width: 0;
	}

	.charts-dense > .delay-inline,
	.charts-dense > .traffic-inline {
		flex: 1 1 0;
		min-width: 0;
		width: auto;
	}

	.chart-inline {
		display: flex;
		flex-direction: column;
		gap: 3px;
		min-width: 0;
		padding: 5px 6px;
		border: 1px solid var(--color-border);
		border-radius: var(--radius-sm);
		background: var(--color-bg-secondary);
		font: inherit;
		color: inherit;
		text-align: left;
	}

	.chart-inline.delay-inline {
		padding: 5px 6px 4px;
	}

	.charts-dense .traffic-inline {
		display: flex;
		align-items: center;
		gap: 0.3rem;
		padding: 5px 4px 5px 5px;
		margin: 0;
		border: 1px solid var(--color-border);
		border-radius: var(--radius-sm);
		background: var(--color-bg-secondary);
		cursor: pointer;
		font: inherit;
		color: inherit;
		text-align: left;
		transition: background var(--t-fast) ease, border-color var(--t-fast) ease;
	}

	.traffic-inline:hover {
		background: var(--color-bg-hover);
		border-color: var(--color-border-hover);
	}

	.traffic-inline:focus-visible,
	.card.view-dense .delay-inline .spark-mini:focus-visible {
		outline: 2px solid var(--color-accent);
		outline-offset: 2px;
	}

	.charts-dense .traffic-inline-rates {
		display: flex;
		flex-direction: column;
		align-items: flex-start;
		gap: 0.06rem;
		padding-block: 2px;
		min-width: 0;
		flex: 1 1 auto;
		font-size: 9px;
		line-height: 1.1;
		font-family: var(--font-mono, monospace);
		font-variant-numeric: tabular-nums;
	}

	.charts-dense .traffic-inline-rate {
		max-width: 100%;
		overflow: hidden;
		text-overflow: ellipsis;
		white-space: nowrap;
	}

	.traffic-inline-rate.rx {
		color: var(--color-accent);
	}

	.traffic-inline-rate.tx {
		color: var(--color-success);
	}

	.chart-inline-head {
		display: flex;
		justify-content: space-between;
		align-items: baseline;
		gap: 6px;
		font-size: 9px;
		line-height: 1.2;
	}

	.chart-inline-label {
		color: var(--color-text-muted);
		text-transform: uppercase;
		letter-spacing: 0.04em;
		font-weight: 500;
	}

	.chart-inline-stats {
		color: var(--color-text-muted);
		font-family: var(--font-mono, monospace);
		font-variant-numeric: tabular-nums;
		white-space: nowrap;
	}

	.chart-inline-stats .err {
		color: var(--color-error);
	}

	.charts-dense .chart-inline-head {
		gap: 4px;
	}

	.charts-dense .chart-inline-stats {
		overflow: hidden;
		text-overflow: ellipsis;
	}

	.card.view-dense .delay-inline .spark-mini {
		display: flex;
		align-items: flex-end;
		gap: 1px;
		width: 100%;
		height: 18px;
		padding: 0;
		border: none;
		background: none;
		cursor: pointer;
	}

	.card.view-dense .delay-inline .spark-mini .bar {
		flex: 1;
		min-width: 0;
		min-height: 2px;
		border-radius: 1px;
		background: linear-gradient(to top, rgba(59, 130, 246, 0.6), rgba(96, 165, 250, 0.9));
	}

	.card.view-dense .delay-inline .spark-mini.fail .bar {
		background: var(--latency-bar-fail);
		height: 100% !important;
	}

	.card.view-dense .delay-inline .spark-mini.unknown .bar,
	.card.view-dense .delay-inline .spark-mini .bar.empty {
		background: var(--color-border);
		height: 30% !important;
	}

	.led-wrap {
		position: absolute;
		top: 12px;
		right: 12px;
		display: flex;
		align-items: center;
		gap: 6px;
	}
	.dot {
		width: 9px;
		height: 9px;
		border-radius: 50%;
		background: var(--text-muted);
	}
	.dot.ok   { background: var(--latency-color-ok); box-shadow: 0 0 6px var(--latency-dot-ok-shadow); }
	.dot.slow { background: var(--latency-color-slow); box-shadow: 0 0 6px var(--latency-dot-slow-shadow); }
	.dot.fail { background: var(--latency-color-fail); box-shadow: 0 0 6px var(--latency-dot-fail-shadow); }
	.dot.stopped { background: var(--color-text-muted); }

	.title {
		margin: 0;
		font-size: var(--sbx-card-title);
		font-weight: 600;
		padding-right: 90px;
	}
	.iface {
		display: flex;
		flex-wrap: wrap;
		align-items: center;
		color: var(--color-text-muted);
		font-size: var(--sbx-card-meta);
		margin-bottom: 0;
		font-family: var(--font-mono, monospace);
	}

	.badges {
		display: flex;
		gap: 5px;
		flex-wrap: wrap;
		margin-bottom: 12px;
	}
	.badge {
		padding: 2px 8px;
		font-size: var(--sbx-card-badge);
		border-radius: 10px;
		font-weight: 500;
	}
	.b-vless { background: rgba(59, 130, 246, 0.15); color: #60a5fa; }
	.b-hysteria2 { background: rgba(245, 158, 11, 0.15); color: #fbbf24; }
	.b-trojan { background: rgba(244, 63, 94, 0.15); color: #fb7185; }
	.b-shadowsocks { background: rgba(16, 185, 129, 0.15); color: #34d399; }
	/* Cyan-400 on 15% alpha perceptually washed out against the dark
	   bg — bump to cyan-300 text with slightly denser background so
	   NaiveProxy matches the contrast of the other protocol badges. */
	.b-naive { background: rgba(34, 211, 238, 0.22); color: #67e8f9; }
	.b-reality { background: rgba(236, 72, 153, 0.15); color: #f472b6; }
	.b-tls { background: rgba(139, 92, 246, 0.15); color: #a78bfa; }
	.b-transport { background: rgba(100, 100, 100, 0.3); color: var(--text-muted); }

	.row {
		display: flex;
		align-items: center;
		margin: 0;
	}
	.row .label {
		color: var(--color-text-muted);
		font-size: var(--sbx-card-label);
		text-transform: uppercase;
		letter-spacing: 0.04em;
		width: 60px;
		flex-shrink: 0;
	}
	.row .value {
		font-size: var(--sbx-card-value);
		color: var(--color-text-secondary);
		font-family: var(--font-mono, monospace);
	}
	.server-row {
		display: flex;
		align-items: center;
		gap: 6px;
		flex: 1;
	}
	.server-hidden { color: var(--text-muted); letter-spacing: 2px; }
	.server-text { font-family: var(--font-mono, monospace); }
	.icon-btn {
		background: none;
		border: none;
		color: var(--text-muted);
		cursor: pointer;
		padding: 2px;
		display: inline-flex;
	}
	.icon-btn:hover { color: var(--text); }
	.icon-btn svg { width: 12px; height: 12px; }
	.port { color: var(--text); margin-left: auto; font-variant-numeric: tabular-nums; }

	.divider {
		height: 1px;
		background: var(--border);
		margin: 12px 0 10px;
	}

	.chart-block { margin-bottom: 10px; }
	.chart-head {
		display: flex;
		justify-content: space-between;
		color: var(--color-text-muted);
		font-size: var(--sbx-card-label);
		text-transform: uppercase;
		letter-spacing: 0.04em;
		margin-bottom: 4px;
	}
	.chart-head .stats {
		color: var(--color-text-muted);
		font-size: var(--sbx-card-value);
		text-transform: none;
		letter-spacing: normal;
	}
	.traffic-head { margin-top: 8px; }
	.chart-head .err { color: #ef4444; }

	.spark {
		height: 26px;
		display: flex;
		align-items: flex-end;
		gap: 2px;
		padding: 2px 0;
	}
	.spark .bar {
		flex: 1;
		background: linear-gradient(to top, rgba(59, 130, 246, 0.6), rgba(96, 165, 250, 0.9));
		border-radius: 1px;
		min-height: 2px;
	}
	.spark.fail .bar { background: var(--latency-bar-fail); height: 100% !important; }
	.spark.unknown .bar,
	.spark .bar.empty {
		background: var(--border);
		height: 30% !important;
	}

	.spark-mini {
		height: 22px;
		max-width: 100%;
		gap: 1px;
		padding: 1px 0;
	}
	.spark-mini .bar {
		min-width: 0;
		min-height: 2px;
	}
	.list-cell-ping-mini {
		justify-content: flex-start;
		padding-right: 0.6rem;
	}
	.traffic-row-list {
		display: flex;
		align-items: center;
		gap: 0.5rem;
		min-width: 0;
	}
	.traffic-mini-col {
		display: flex;
		flex-direction: column;
		gap: 0.1rem;
		font-size: var(--sbx-card-note);
		line-height: 1.15;
		flex-shrink: 0;
	}
	.traffic-mini-click {
		display: inline-flex;
		border-radius: 4px;
		cursor: pointer;
		transition: background var(--t-fast) ease;
	}
	.traffic-mini-click:hover {
		background: rgba(96, 165, 250, 0.06);
	}
	.traffic-mini-click:focus-visible {
		outline: 1px solid var(--color-accent, #58a6ff);
		outline-offset: 1px;
	}

	.actions {
		display: flex;
		gap: 6px;
		justify-content: flex-end;
		margin-top: 12px;
		padding: 10px 0;
		border-top: 1px solid var(--border);
		border-bottom: 1px solid var(--border);
	}
	.action-btn {
		display: inline-flex;
		align-items: center;
		gap: 4px;
		padding: 5px 9px;
		font-size: var(--sbx-card-action);
		font-weight: 500;
		border: none;
		background: transparent;
		color: var(--color-text-secondary);
		cursor: pointer;
		border-radius: var(--radius-sm);
		text-decoration: none;
		font-family: inherit;
		transition: background var(--t-fast) ease, color var(--t-fast) ease;
	}
	.action-btn:hover:not(:disabled) {
		background: var(--color-bg-hover);
		color: var(--color-text-primary);
	}
	.action-btn:disabled {
		opacity: 0.5;
		cursor: not-allowed;
	}
	.chart-section {
		margin: 0 -14px -12px;
		border-radius: 0 0 var(--radius) var(--radius);
		background: var(--color-bg-secondary);
		overflow: hidden;
	}
	.chart-header {
		display: flex;
		justify-content: space-between;
		align-items: center;
		width: 100%;
		padding: 6px 12px;
		border: none;
		background: none;
		cursor: pointer;
		user-select: none;
		font: inherit;
		transition: background var(--t-fast) ease;
	}
	.chart-header:hover {
		background: var(--bg-tertiary);
	}
	.chart-label {
		font-size: var(--sbx-card-note);
		font-weight: 500;
		color: var(--color-text-muted);
		text-transform: uppercase;
		letter-spacing: 0.03em;
	}
	.chart-chevron {
		font-size: 14px;
		color: var(--text-muted);
		transition: transform var(--t-fast) ease;
		transform: rotate(-90deg);
	}
	.chart-chevron.expanded {
		transform: rotate(0deg);
	}
	.chart-body {
		max-height: 0;
		overflow: hidden;
		transition: max-height var(--t-med) ease;
		padding: 0 12px;
	}
	.chart-body.expanded {
		max-height: 300px;
		padding: 0 12px 8px;
	}

	/* List row (grid columns set on parent .singbox-tunnel-list-table) */
	.sbx-tunnel-list-row {
		align-items: center;
		min-width: 0;
	}
	.sbx-tunnel-list-row .list-cell {
		min-width: 0;
	}
	.sbx-tunnel-list-row .list-cell-delay {
		display: flex;
		align-items: center;
		gap: 0.4rem;
	}
	.sbx-tunnel-list-row .name-btn {
		font: inherit;
		font-size: var(--sbx-card-title);
		font-weight: 600;
		color: var(--text);
		background: none;
		border: none;
		padding: 0;
		cursor: pointer;
		text-align: left;
	}
	.sbx-tunnel-list-row .name-btn:hover {
		color: var(--color-accent, #58a6ff);
	}
	.sbx-tunnel-list-row .list-sub {
		margin-top: 0.2rem;
		font-size: var(--sbx-card-meta);
		color: var(--color-text-muted);
		overflow: hidden;
		text-overflow: ellipsis;
		white-space: nowrap;
	}
	.badges-inline {
		display: flex;
		flex-wrap: wrap;
		gap: 0.25rem;
	}
	.server-line {
		display: flex;
		align-items: center;
		gap: 0.35rem;
		font-size: var(--sbx-card-value);
		overflow: hidden;
	}
	.muted {
		color: var(--text-muted);
	}
	.eye-inline {
		display: inline-flex;
		padding: 0.1rem;
		border: none;
		background: none;
		color: var(--text-muted);
		cursor: pointer;
	}
	.run-pill {
		font-size: var(--sbx-card-badge);
		font-weight: 600;
		padding: 0.15rem 0.45rem;
		border-radius: 999px;
		background: var(--bg-tertiary, rgba(100, 100, 100, 0.25));
		color: var(--text-muted);
		text-transform: uppercase;
		letter-spacing: 0.04em;
	}
	.run-pill.run-on {
		background: rgba(16, 185, 129, 0.2);
		color: #10b981;
	}
	.traffic-mini {
		font-size: var(--sbx-card-note);
		color: var(--color-text-muted);
	}
	.list-actions {
		display: flex;
		flex-wrap: nowrap;
		gap: 0.375rem;
		justify-content: flex-end;
		align-items: center;
		white-space: nowrap;
	}
	.list-actions .action-btn {
		justify-content: center;
		padding: 0.375rem;
	}
	.action-danger:hover:not(:disabled),
	.list-actions :global(.action-danger:hover:not(:disabled)) {
		color: #ff6b6b;
		background: rgba(239, 68, 68, 0.18);
	}
	.action-btn.action-test:hover:not(:disabled) {
		color: var(--color-success, #9ece6a);
		background: var(--color-success-tint, rgba(158, 206, 106, 0.28));
	}
</style>
