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
	import { Modal, Button, TrafficChart, TrafficSparkline } from '$lib/components/ui';
	import { getTrafficRates, subscribeTraffic, loadHistory } from '$lib/stores/traffic';
	import type { SingboxLayoutMode } from '$lib/constants/singboxLayout';

	interface Props {
		tunnel: SingboxTunnel;
		layout?: SingboxLayoutMode;
		autoDelayCheckNonce?: number;
		autoDelayCheckDelayMs?: number;
		ondetail?: (tag: string) => void;
	}

	let {
		tunnel,
		layout = 'grid',
		autoDelayCheckNonce = 0,
		autoDelayCheckDelayMs = 0,
		ondetail,
	}: Props = $props();

	let deleting = $state(false);
	let confirmDeleteOpen = $state(false);
	let showServer = $state(false);
	let checking = $state(false);

	const DELAY_OK = 200;
	const DELAY_SLOW = 500;

	const history = $derived($singboxDelayHistory.get(tunnel.tag) ?? []);
	const latest = $derived(history.length > 0 ? history[history.length - 1] : undefined);
	const hasConsecutiveTimeout = $derived(
		history.length >= 2 &&
			history[history.length - 1] <= 0 &&
			history[history.length - 2] <= 0,
	);
	const positiveHistory = $derived(history.filter((v) => v > 0));
	const avg = $derived(
		positiveHistory.length > 0
			? Math.round(positiveHistory.reduce((s, v) => s + v, 0) / positiveHistory.length)
			: 0,
	);
	const traffic = $derived($singboxTraffic.get(tunnel.tag));

	const trafficSparkData = $derived.by(() => {
		const n = Math.min(rxRates.length, txRates.length);
		if (n === 0) return [];
		const take = Math.min(36, n);
		const out: number[] = [];
		for (let i = n - take; i < n; i++) {
			out.push(Math.max(0, rxRates[i] ?? 0) + Math.max(0, txRates[i] ?? 0));
		}
		return out;
	});

	type State = 'ok' | 'slow' | 'fail' | 'unknown' | 'stopped';
	const cardState: State = $derived.by(() => {
		// Runtime truth takes priority over delay history: if the process
		// is dead or the TUN is missing, recent latency numbers are stale
		// noise. Show 'stopped' so the user knows to restart the daemon
		// instead of debugging a timeout that isn't actually a timeout.
		if (tunnel.running === false) return 'stopped';
		if (latest === undefined) return 'unknown';
		if (latest <= 0) return hasConsecutiveTimeout ? 'fail' : 'slow';
		if (latest < DELAY_OK) return 'ok';
		if (latest < DELAY_SLOW) return 'slow';
		return 'slow';
	});

	const latText = $derived.by(() => {
		if (cardState === 'stopped') return 'stopped';
		if (cardState === 'unknown') return '—';
		if (cardState === 'fail') return 'timeout';
		if (latest !== undefined && latest <= 0) return 'проверка...';
		return `${latest}ms`;
	});

	const testHref = $derived(`/singbox/${encodeURIComponent(tunnel.tag)}/test`);
	const resolvedTestHref = $derived(tunnel.kernelInterface ? testHref : undefined);

	const protocolLabel = $derived.by(() => {
		if (tunnel.protocol === 'vless') return 'VLESS';
		if (tunnel.protocol === 'hysteria2') return 'Hysteria2';
		return 'NaiveProxy';
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
			<button
				type="button"
				class="lat-btn {cardState}"
				class:checking
				onclick={triggerCheck}
				title="Обновить delay"
				disabled={checking}
			>
				{checking ? '...' : latText}
			</button>
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
						data={trafficSparkData}
						width={84}
						height={22}
						color={tunnel.running === true ? 'var(--color-accent)' : 'var(--color-border-hover)'}
					/>
				</div>
				<div class="traffic-mini-col mono">
					<span>↓ {formatBytes(traffic?.download ?? 0)}</span>
					<span>↑ {formatBytes(traffic?.upload ?? 0)}</span>
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
				<button class="action-btn" type="button" onclick={edit}>
					<svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2">
						<path d="M11 4H4a2 2 0 0 0-2 2v14a2 2 0 0 0 2 2h14a2 2 0 0 0 2-2v-7"/>
						<path d="M18.5 2.5a2.121 2.121 0 0 1 3 3L12 15l-4 1 1-4 9.5-9.5z"/>
					</svg>
					Изменить
				</button>
				{#if resolvedTestHref}
					<a class="action-btn" href={resolvedTestHref}>
						<svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2">
							<path d="M22 11.08V12a10 10 0 1 1-5.93-9.14"/>
							<polyline points="22,4 12,14.01 9,11.01"/>
						</svg>
						Тест
					</a>
				{:else}
					<button class="action-btn" type="button" disabled>
						<svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2">
							<path d="M22 11.08V12a10 10 0 1 1-5.93-9.14"/>
							<polyline points="22,4 12,14.01 9,11.01"/>
						</svg>
						Тест
					</button>
				{/if}
				<button class="action-btn action-danger" type="button" onclick={() => (confirmDeleteOpen = true)} disabled={deleting}>
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
		</div>
	</div>
{:else}
<div class="card" class:ok={cardState === 'ok'} class:slow={cardState === 'slow'} class:fail={cardState === 'fail'} class:unknown={cardState === 'unknown'} class:stopped={cardState === 'stopped'}>
	<div class="led-wrap">
		<span class="dot {cardState}" aria-hidden="true"></span>
		<button
			class="lat-btn {cardState}"
			class:checking
			onclick={triggerCheck}
			title="Обновить delay"
			disabled={checking}
		>
			<span>{latText}</span>
			<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" aria-hidden="true">
				<path d="M23 4v6h-6M1 20v-6h6"/>
				<path d="M3.51 9a9 9 0 0 1 14.85-3.36L23 10M1 14l4.64 4.36A9 9 0 0 0 20.49 15"/>
			</svg>
		</button>
	</div>

	<h3 class="title">{tunnel.tag}</h3>
	<div class="iface">
		{tunnel.proxyInterface}
		{#if tunnel.kernelInterface}
			<span class="kernel">· {tunnel.kernelInterface}</span>
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
		<Button variant="ghost" size="sm" onclick={edit} iconBefore={editIcon}>Изменить</Button>
		<Button
			variant="ghost"
			size="sm"
			href={resolvedTestHref}
			disabled={!tunnel.kernelInterface}
			iconBefore={testIcon}
		>
			Тест
		</Button>
		<button
			class="action-btn action-danger"
			type="button"
			onclick={() => (confirmDeleteOpen = true)}
			disabled={deleting}
			title="Удалить туннель"
		>
			{#if deleting}
				<span class="action-spinner"></span>
			{:else}
				{@render deleteIcon()}
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

{#snippet testIcon()}
	<svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2">
		<path d="M22 11.08V12a10 10 0 1 1-5.93-9.14"/>
		<polyline points="22,4 12,14.01 9,11.01"/>
	</svg>
{/snippet}

{#snippet editIcon()}
	<svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2">
		<path d="M11 4H4a2 2 0 0 0-2 2v14a2 2 0 0 0 2 2h14a2 2 0 0 0 2-2v-7"/>
		<path d="M18.5 2.5a2.121 2.121 0 0 1 3 3L12 15l-4 1 1-4 9.5-9.5z"/>
	</svg>
{/snippet}

{#snippet deleteIcon()}
	<svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2">
		<polyline points="3,6 5,6 21,6"/>
		<path d="M19 6v14a2 2 0 0 1-2 2H7a2 2 0 0 1-2-2V6m3 0V4a2 2 0 0 1 2-2h4a2 2 0 0 1 2 2v2"/>
	</svg>
{/snippet}

<style>
	.card {
		display: flex;
		flex-direction: column;
		padding: 16px;
		border: 1px solid var(--border);
		border-radius: 10px;
		background: var(--bg-card);
		color: var(--text);
		position: relative;
		transition: border-color 0.2s;
	}
	.card.ok { border-color: rgba(16, 185, 129, 0.3); }
	.card.slow { border-color: rgba(245, 158, 11, 0.3); }
	.card.fail { border-color: rgba(239, 68, 68, 0.3); }
	.card.stopped { border-color: rgba(148, 163, 184, 0.4); opacity: 0.7; }

	.led-wrap {
		position: absolute;
		top: 14px;
		right: 14px;
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
	.dot.ok { background: #10b981; box-shadow: 0 0 6px rgba(16, 185, 129, 0.6); }
	.dot.slow { background: #f59e0b; box-shadow: 0 0 6px rgba(245, 158, 11, 0.6); }
	.dot.fail { background: #ef4444; box-shadow: 0 0 6px rgba(239, 68, 68, 0.6); }
	.dot.stopped { background: #94a3b8; }

	.lat-btn {
		background: none;
		border: 1px solid transparent;
		color: var(--text-muted);
		font-family: inherit;
		font-size: 12px;
		font-weight: 500;
		padding: 2px 8px;
		border-radius: 4px;
		cursor: pointer;
		display: inline-flex;
		align-items: center;
		gap: 5px;
		font-variant-numeric: tabular-nums;
		transition: background 0.15s, border-color 0.15s;
	}
	.lat-btn:hover:not(:disabled) {
		background: var(--bg-tertiary);
		border-color: var(--border);
	}
	.lat-btn.ok { color: #10b981; }
	.lat-btn.slow { color: #fbbf24; }
	.lat-btn.fail { color: #ef4444; }
	.lat-btn.stopped { color: #94a3b8; }
	.lat-btn svg {
		width: 11px;
		height: 11px;
		opacity: 0.5;
		transition: opacity 0.15s, transform 0.3s;
	}
	.lat-btn:hover:not(:disabled) svg { opacity: 1; }
	.lat-btn.checking svg { animation: spin 1s linear infinite; }
	@keyframes spin { to { transform: rotate(360deg); } }

	.title {
		margin: 0 0 3px;
		font-size: 15px;
		font-weight: 600;
		padding-right: 80px;
	}
	.iface {
		color: var(--text-muted);
		font-size: 11px;
		margin-bottom: 10px;
		font-family: var(--font-mono, monospace);
	}
	.iface .kernel {
		color: var(--text-muted);
		opacity: 0.7;
		margin-left: 4px;
	}

	.badges {
		display: flex;
		gap: 5px;
		flex-wrap: wrap;
		margin-bottom: 12px;
	}
	.badge {
		padding: 2px 8px;
		font-size: 10px;
		border-radius: 10px;
		font-weight: 500;
	}
	.b-vless { background: rgba(59, 130, 246, 0.15); color: #60a5fa; }
	.b-hysteria2 { background: rgba(245, 158, 11, 0.15); color: #fbbf24; }
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
		margin: 4px 0;
		font-size: 11px;
	}
	.row .label {
		color: var(--text-muted);
		font-size: 10px;
		text-transform: uppercase;
		letter-spacing: 0.05em;
		width: 60px;
		flex-shrink: 0;
	}
	.row .value { color: var(--text-secondary, var(--text)); font-family: var(--font-mono, monospace); }
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
		color: var(--text-muted);
		font-size: 10px;
		text-transform: uppercase;
		letter-spacing: 0.05em;
		margin-bottom: 4px;
	}
	.chart-head .stats {
		color: var(--text-muted);
		font-size: 10px;
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
	.spark.fail .bar { background: rgba(239, 68, 68, 0.4); height: 100% !important; }
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
		font-size: 0.7rem;
		line-height: 1.15;
		color: var(--text-muted);
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
		font-size: 11px;
		font-weight: 500;
		border: none;
		background: transparent;
		color: var(--text-secondary, var(--text));
		cursor: pointer;
		border-radius: var(--radius-sm);
		text-decoration: none;
		font-family: inherit;
		transition: background var(--t-fast) ease, color var(--t-fast) ease;
	}
	.action-btn:hover:not(:disabled) {
		background: var(--bg-hover, var(--color-bg-hover));
		color: var(--text);
	}
	.action-btn:disabled {
		opacity: 0.5;
		cursor: not-allowed;
	}
	.chart-section {
		margin: 0 -16px -16px;
		border-radius: 0 0 var(--radius) var(--radius);
		background: var(--bg-card);
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
		font-size: 11px;
		font-weight: 500;
		color: var(--text-muted);
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
		font-size: 0.9375rem;
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
		font-size: 0.75rem;
		color: var(--text-muted);
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
		font-size: 0.8125rem;
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
		font-size: 0.68rem;
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
		font-size: 0.75rem;
		color: var(--text-muted);
	}
	.list-actions {
		display: flex;
		flex-wrap: nowrap;
		gap: 0.5rem;
		justify-content: flex-end;
		align-items: center;
		white-space: nowrap;
		margin-left: 0.5rem;
	}
	.action-danger:hover:not(:disabled),
	.list-actions :global(.action-danger:hover:not(:disabled)) {
		color: #ff6b6b;
		background: rgba(239, 68, 68, 0.18);
	}
</style>
