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
	import { Modal } from '$lib/components/ui';
	import SingboxSpeedTestModal from './SingboxSpeedTestModal.svelte';

	interface Props {
		tunnel: SingboxTunnel;
	}

	let { tunnel }: Props = $props();

	let deleting = $state(false);
	let confirmDeleteOpen = $state(false);
	let showServer = $state(false);
	let checking = $state(false);

	const DELAY_OK = 200;
	const DELAY_SLOW = 500;

	const history = $derived($singboxDelayHistory.get(tunnel.tag) ?? []);
	const latest = $derived(history.length > 0 ? history[history.length - 1] : undefined);
	const avg = $derived(
		history.length > 0
			? Math.round(history.reduce((s, v) => s + v, 0) / history.length)
			: 0,
	);
	const traffic = $derived($singboxTraffic.get(tunnel.tag));

	type State = 'ok' | 'slow' | 'fail' | 'unknown';
	const cardState: State = $derived.by(() => {
		if (latest === undefined) return 'unknown';
		if (latest <= 0) return 'fail';
		if (latest < DELAY_OK) return 'ok';
		if (latest < DELAY_SLOW) return 'slow';
		return 'fail';
	});

	const latText = $derived.by(() => {
		if (cardState === 'unknown') return '—';
		if (cardState === 'fail') return 'timeout';
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
</script>

<div class="card" class:ok={cardState === 'ok'} class:slow={cardState === 'slow'} class:fail={cardState === 'fail'} class:unknown={cardState === 'unknown'}>
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

	<div class="chart-block">
		<div class="chart-head">
			<span>Трафик</span>
			<span class="stats">
				↓ {formatBytes(traffic?.download ?? 0)} · ↑ {formatBytes(traffic?.upload ?? 0)}
			</span>
		</div>
		<div class="traffic-spark">
			<div class="track dl">
				{#each Array(8) as _, i}
					<div class="bar" style="height: {traffic && traffic.download > 0 ? Math.min((traffic.download / (1024 * 1024)) * 2, 18) : 1}px;"></div>
				{/each}
			</div>
			<div class="track ul">
				{#each Array(8) as _, i}
					<div class="bar" style="height: {traffic && traffic.upload > 0 ? Math.min((traffic.upload / (1024 * 1024)) * 2, 10) : 1}px;"></div>
				{/each}
			</div>
		</div>
	</div>

	<div class="actions">
		<button
			class="btn btn-ghost btn-sm"
			onclick={openSpeedtest}
			disabled={!tunnel.kernelInterface}
			title={tunnel.kernelInterface ? 'iperf3 через ' + tunnel.kernelInterface : 'Kernel interface не определён'}
		>
			Тест скорости
		</button>
		<button class="btn btn-ghost btn-sm" onclick={edit}>Изменить</button>
		<button class="btn btn-danger btn-sm" onclick={() => (confirmDeleteOpen = true)} disabled={deleting}>
			{deleting ? 'Удаление...' : 'Удалить'}
		</button>
	</div>
</div>

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
		<button class="btn btn-ghost" onclick={() => (confirmDeleteOpen = false)}>Отмена</button>
		<button class="btn btn-danger" onclick={remove}>Удалить</button>
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
	.b-naive { background: rgba(34, 211, 238, 0.15); color: #22d3ee; }
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

	.traffic-spark {
		height: 22px;
		display: flex;
		flex-direction: column;
		gap: 1px;
	}
	.traffic-spark .track {
		display: flex;
		gap: 1px;
		flex: 1;
		align-items: flex-end;
	}
	.traffic-spark .track .bar {
		flex: 1;
		min-height: 1px;
		border-radius: 1px;
	}
	.traffic-spark .track.dl .bar { background: rgba(16, 185, 129, 0.7); }
	.traffic-spark .track.ul .bar { background: rgba(59, 130, 246, 0.7); }

	.actions {
		display: flex;
		gap: 6px;
		justify-content: flex-end;
		margin-top: 12px;
		padding-top: 10px;
		border-top: 1px solid var(--border);
	}
</style>
