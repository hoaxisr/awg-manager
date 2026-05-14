<script lang="ts">
	import type { SingboxTunnel } from '$lib/types';
	import { goto } from '$app/navigation';
	import { api } from '$lib/api/client';
	import {
		singboxTunnels,
		singboxDelayHistory,
		singboxTraffic,
		triggerDelayCheck,
	} from '$lib/stores/singbox';
	import { untrack } from 'svelte';
	import { Modal, Button, TrafficChart, TrafficSparkline } from '$lib/components/ui';
	import { getTrafficRates, subscribeTraffic, loadHistory } from '$lib/stores/traffic';
	import SingboxSpeedTestModal from './SingboxSpeedTestModal.svelte';
	import type { SingboxLayoutMode } from '$lib/constants/singboxLayout';

	interface Props {
		tunnel: SingboxTunnel;
		layout?: SingboxLayoutMode;
		autoDelayCheckNonce?: number;
		autoDelayCheckDelayMs?: number;
	}

	let {
		tunnel,
		layout = 'grid',
		autoDelayCheckNonce = 0,
		autoDelayCheckDelayMs = 0,
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

	let speedtestOpen = $state(false);

	function openSpeedtest(): void {
		if (!tunnel.kernelInterface) return;
		speedtestOpen = true;
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
				<TrafficSparkline
					data={trafficSparkData}
					width={84}
					height={22}
					color={tunnel.running === true ? 'var(--color-accent)' : 'var(--color-border-hover)'}
				/>
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
				<Button variant="ghost" size="sm" onclick={edit}>Изменить</Button>
				<Button variant="danger" size="sm" onclick={() => (confirmDeleteOpen = true)} loading={deleting}>
					Удалить
				</Button>
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

	<div class="divider"></div>

	<div class="chart-block">
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
			onclick={triggerCheck}
			onkeydown={(e) => (e.key === 'Enter' || e.key === ' ') && triggerCheck()}
			role="button"
			tabindex="0"
			title="Клик — обновить delay"
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
	</div>

	<div class="chart-block traffic-block">
		<div class="chart-head">
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
		/>
	</div>

	<div class="actions">
		<Button
			variant="ghost"
			size="sm"
			onclick={openSpeedtest}
			disabled={!tunnel.kernelInterface}
		>
			Тест скорости
		</Button>
		<Button variant="ghost" size="sm" onclick={edit}>Изменить</Button>
		<Button variant="danger" size="sm" onclick={() => (confirmDeleteOpen = true)} loading={deleting}>
			Удалить
		</Button>
	</div>
</div>
{/if}

<SingboxSpeedTestModal
	open={speedtestOpen}
	tag={tunnel.tag}
	kernelInterface={tunnel.kernelInterface ?? ''}
	onclose={() => (speedtestOpen = false)}
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
	.chart-head .err { color: #ef4444; }

	.spark {
		height: 26px;
		display: flex;
		align-items: flex-end;
		gap: 2px;
		cursor: pointer;
		padding: 2px 0;
	}
	.spark:focus { outline: 1px dashed var(--text-muted); }
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

	.actions {
		display: flex;
		gap: 6px;
		justify-content: flex-end;
		margin-top: 12px;
		padding-top: 10px;
		border-top: 1px solid var(--border);
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
		flex-wrap: wrap;
		gap: 0.35rem;
		justify-content: flex-end;
	}
</style>
