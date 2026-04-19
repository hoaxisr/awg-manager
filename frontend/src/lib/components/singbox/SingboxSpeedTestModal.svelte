<script lang="ts">
	import { onDestroy } from 'svelte';
	import { api } from '$lib/api/client';
	import { notifications } from '$lib/stores/notifications';
	import { Modal, SpeedGauge } from '$lib/components/ui';
	import type { SpeedTestInfo, SpeedTestServer } from '$lib/types';

	interface Props {
		open: boolean;
		tag: string;
		kernelInterface: string;
		onclose: () => void;
	}

	let { open, tag, kernelInterface, onclose }: Props = $props();

	let info = $state<SpeedTestInfo | null>(null);
	let selectedServerIdx = $state(0);
	let phase = $state<'idle' | 'ping' | 'download' | 'upload' | 'done' | 'error' | 'cancelled'>('idle');
	let downloadMbps = $state<number | null>(null);
	let uploadMbps = $state<number | null>(null);
	let currentBandwidth = $state(0);
	let currentSecond = $state(0);
	let errorMsg = $state('');
	let eventSource: EventSource | null = null;
	const TOTAL_SECONDS = 10; // iperf3 -t 10 on backend

	const selectedServer = $derived<SpeedTestServer | null>(info?.servers[selectedServerIdx] ?? null);
	const gaugeMax = $derived(Math.max(1000, (downloadMbps ?? 0) * 1.2, (uploadMbps ?? 0) * 1.2));
	const gaugePhase = $derived<'idle' | 'download' | 'upload' | 'done'>(
		phase === 'download' ? 'download'
			: phase === 'upload' ? 'upload'
				: phase === 'done' ? 'done'
					: 'idle'
	);
	const isRunning = $derived(phase === 'ping' || phase === 'download' || phase === 'upload');
	// Live values for the big metric blocks while the test is running:
	// during each phase the corresponding metric follows currentBandwidth, so
	// the user sees numbers move second-by-second instead of staying at 0
	// until the phase finishes.
	const displayDownload = $derived(
		phase === 'download' && currentBandwidth > 0
			? currentBandwidth
			: downloadMbps,
	);
	const displayUpload = $derived(
		phase === 'upload' && currentBandwidth > 0
			? currentBandwidth
			: uploadMbps,
	);
	const progressPct = $derived(
		(phase === 'download' || phase === 'upload') && currentSecond > 0
			? Math.min(100, (currentSecond / TOTAL_SECONDS) * 100)
			: 0,
	);

	$effect(() => {
		if (open && info === null) {
			void loadInfo();
		}
	});

	async function loadInfo(): Promise<void> {
		try {
			info = await api.getSpeedTestInfo();
		} catch (e) {
			errorMsg = e instanceof Error ? e.message : String(e);
		}
	}

	function reset(): void {
		phase = 'idle';
		downloadMbps = null;
		uploadMbps = null;
		currentBandwidth = 0;
		currentSecond = 0;
		errorMsg = '';
	}

	function runTest(): void {
		if (!selectedServer) return;
		reset();
		phase = 'ping';
		eventSource = api.singboxSpeedTestStream(
			tag,
			selectedServer.host,
			selectedServer.port,
			(p) => {
				phase = p;
				currentBandwidth = 0;
				currentSecond = 0;
			},
			(iv) => {
				currentBandwidth = iv.bandwidth ?? 0;
				currentSecond = iv.second ?? currentSecond;
			},
			(r) => {
				const mbps = r.bandwidth ?? 0;
				if (r.phase === 'download') {
					downloadMbps = mbps;
				} else if (r.phase === 'upload') {
					uploadMbps = mbps;
				}
			},
			() => {
				phase = 'done';
				currentBandwidth = downloadMbps ?? uploadMbps ?? 0;
			},
			(err) => {
				phase = 'error';
				errorMsg = err;
			},
		);
	}

	// cancelTest closes the SSE connection which drops r.Context() on the
	// server; exec.CommandContext kills the iperf3 process. Keeps the modal
	// open in a "cancelled" state so the user has feedback instead of a
	// silent close.
	function cancelTest(): void {
		eventSource?.close();
		eventSource = null;
		phase = 'cancelled';
		currentBandwidth = 0;
		currentSecond = 0;
		notifications.info('Тест скорости отменён');
	}

	function close(): void {
		eventSource?.close();
		eventSource = null;
		onclose();
	}

	onDestroy(() => {
		eventSource?.close();
	});

	function fmt(n: number | null): string {
		if (n === null) return '—';
		return n.toFixed(n >= 10 ? 1 : 2);
	}

	// Step descriptor used to render the linear phase indicator. Kept tiny
	// and visual — one row of dots/labels that advances as the test moves
	// through ping → download → upload → done.
	type StepState = 'pending' | 'active' | 'done';
	function stepState(step: 'ping' | 'download' | 'upload'): StepState {
		const order = ['ping', 'download', 'upload'];
		const curIdx = phase === 'done' ? 3 : order.indexOf(phase);
		const stepIdx = order.indexOf(step);
		if (curIdx < 0) return 'pending';
		if (stepIdx < curIdx) return 'done';
		if (stepIdx === curIdx) return 'active';
		return 'pending';
	}
</script>

<Modal {open} onclose={close} title="Тест скорости: {tag}">
	<div class="sbst">
		<div class="metrics">
			<div class="metric">
				<div class="m-label">DOWNLOAD</div>
				<div class="m-value" style:color={displayDownload !== null ? '#10b981' : undefined}>
					{fmt(displayDownload)}<span class="m-unit">Mbps</span>
				</div>
			</div>
			<div class="metric">
				<div class="m-label">UPLOAD</div>
				<div class="m-value" style:color={displayUpload !== null ? '#60a5fa' : undefined}>
					{fmt(displayUpload)}<span class="m-unit">Mbps</span>
				</div>
			</div>
		</div>

		<SpeedGauge value={currentBandwidth} max={gaugeMax} phase={gaugePhase} />

		{#if isRunning || phase === 'done'}
			<div class="step-row">
				{#each [
					{ key: 'ping', label: 'Пинг' },
					{ key: 'download', label: 'Загрузка' },
					{ key: 'upload', label: 'Отдача' },
				] as s}
					{@const st = stepState(s.key as 'ping' | 'download' | 'upload')}
					<div class="step" class:active={st === 'active'} class:done={st === 'done'}>
						{#if st === 'done'}
							<svg class="step-mark" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="3" stroke-linecap="round" stroke-linejoin="round" width="12" height="12">
								<polyline points="20 6 9 17 4 12" />
							</svg>
						{:else if st === 'active'}
							<span class="step-spinner"></span>
						{:else}
							<span class="step-dot"></span>
						{/if}
						<span class="step-label">{s.label}</span>
					</div>
				{/each}
			</div>

			{#if phase === 'ping'}
				<div class="progress-hint">Устанавливаем соединение…</div>
			{:else if phase === 'download' || phase === 'upload'}
				<div class="progress-track">
					<div class="progress-fill" class:download={phase === 'download'} class:upload={phase === 'upload'} style="width: {progressPct}%"></div>
				</div>
				<div class="progress-hint">
					{#if currentSecond > 0}
						{currentSecond} / {TOTAL_SECONDS} сек
					{:else}
						подключение…
					{/if}
				</div>
			{/if}
		{/if}

		<div class="footer">
			<div class="iface-info">
				<span class="iface-label">Интерфейс</span>
				<code>{kernelInterface}</code>
			</div>

			{#if info}
				<select bind:value={selectedServerIdx} disabled={isRunning}>
					{#each info.servers as srv, i}
						<option value={i}>{srv.label} ({srv.host}:{srv.port})</option>
					{/each}
				</select>
			{/if}

			<div class="actions">
				{#if isRunning}
					<button class="btn btn-ghost btn-sm" onclick={cancelTest}>Отмена</button>
				{:else}
					<button class="btn btn-primary btn-sm" onclick={runTest} disabled={!selectedServer}>
						{phase === 'idle' ? 'Запустить' : phase === 'cancelled' ? 'Запустить заново' : 'Повторить'}
					</button>
				{/if}
			</div>

			{#if phase === 'cancelled'}
				<div class="hint hint-muted">Тест отменён. Можно запустить заново.</div>
			{/if}
			{#if errorMsg}
				<div class="error">{errorMsg}</div>
			{/if}
		</div>
	</div>
</Modal>

<style>
	.sbst {
		display: flex;
		flex-direction: column;
		gap: 16px;
		padding: 8px 4px;
	}
	.metrics {
		display: grid;
		grid-template-columns: repeat(2, 1fr);
		gap: 12px;
		padding-bottom: 12px;
		border-bottom: 1px solid var(--border);
	}
	.metric {
		display: flex;
		flex-direction: column;
		gap: 4px;
	}
	.m-label {
		font-size: 0.7rem;
		color: var(--text-muted);
		letter-spacing: 0.1em;
		font-weight: 600;
	}
	.m-value {
		font-size: 1.6rem;
		font-weight: 600;
		font-variant-numeric: tabular-nums;
		color: var(--text);
	}
	.m-unit {
		font-size: 0.75rem;
		color: var(--text-muted);
		margin-left: 4px;
		font-weight: normal;
	}
	.footer {
		display: flex;
		flex-direction: column;
		gap: 12px;
		padding-top: 12px;
		border-top: 1px solid var(--border);
	}
	.iface-info {
		display: flex;
		align-items: center;
		gap: 8px;
		font-size: 0.8rem;
	}
	.iface-label {
		color: var(--text-muted);
		text-transform: uppercase;
		letter-spacing: 0.05em;
	}
	.iface-info code {
		color: var(--text);
		background: var(--bg-secondary);
		padding: 2px 8px;
		border-radius: 4px;
		font-family: var(--font-mono, monospace);
	}
	select {
		background: var(--bg-secondary);
		border: 1px solid var(--border);
		color: var(--text);
		padding: 6px 10px;
		border-radius: 4px;
		font-size: 12px;
		font-family: inherit;
	}
	.actions {
		display: flex;
		justify-content: flex-end;
		gap: 6px;
	}
	.error {
		padding: 8px 12px;
		background: rgba(239, 68, 68, 0.08);
		border-left: 2px solid var(--error, #ef4444);
		border-radius: 3px;
		font-size: 12px;
		color: var(--error, #ef4444);
	}

	.hint {
		padding: 6px 10px;
		font-size: 12px;
		border-radius: 3px;
	}
	.hint-muted {
		background: var(--bg-secondary);
		color: var(--text-muted);
	}

	.step-row {
		display: flex;
		justify-content: center;
		gap: 18px;
		padding: 4px 0;
	}
	.step {
		display: inline-flex;
		align-items: center;
		gap: 6px;
		font-size: 12px;
		color: var(--text-muted);
		font-weight: 500;
	}
	.step.active {
		color: var(--accent, #60a5fa);
	}
	.step.done {
		color: #10b981;
	}
	.step-dot {
		width: 8px;
		height: 8px;
		border-radius: 50%;
		background: currentColor;
		opacity: 0.35;
	}
	.step.active .step-dot {
		opacity: 1;
	}
	.step-mark {
		color: #10b981;
	}
	.step-spinner {
		width: 10px;
		height: 10px;
		border-radius: 50%;
		border: 2px solid currentColor;
		border-top-color: transparent;
		animation: sbst-spin 0.8s linear infinite;
	}
	@keyframes sbst-spin {
		to {
			transform: rotate(360deg);
		}
	}

	.progress-track {
		height: 4px;
		background: var(--bg-secondary);
		border-radius: 2px;
		overflow: hidden;
	}
	.progress-fill {
		height: 100%;
		background: var(--text-muted);
		transition: width 0.25s linear;
	}
	.progress-fill.download {
		background: #10b981;
	}
	.progress-fill.upload {
		background: #60a5fa;
	}
	.progress-hint {
		font-size: 11px;
		color: var(--text-muted);
		text-align: center;
		letter-spacing: 0.05em;
	}
</style>
