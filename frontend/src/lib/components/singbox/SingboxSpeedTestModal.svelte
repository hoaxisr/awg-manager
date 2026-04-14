<script lang="ts">
	import { onDestroy } from 'svelte';
	import { api } from '$lib/api/client';
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
	let phase = $state<'idle' | 'ping' | 'download' | 'upload' | 'done' | 'error'>('idle');
	let downloadMbps = $state<number | null>(null);
	let uploadMbps = $state<number | null>(null);
	let currentBandwidth = $state(0);
	let errorMsg = $state('');
	let eventSource: EventSource | null = null;

	const selectedServer = $derived<SpeedTestServer | null>(info?.servers[selectedServerIdx] ?? null);
	const gaugeMax = $derived(Math.max(1000, (downloadMbps ?? 0) * 1.2, (uploadMbps ?? 0) * 1.2));
	const gaugePhase = $derived<'idle' | 'download' | 'upload' | 'done'>(
		phase === 'download' ? 'download'
			: phase === 'upload' ? 'upload'
				: phase === 'done' ? 'done'
					: 'idle'
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
			},
			(iv) => {
				currentBandwidth = iv.bandwidth ?? 0;
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
</script>

<Modal {open} onclose={close} title="Тест скорости: {tag}">
	<div class="sbst">
		<div class="metrics">
			<div class="metric">
				<div class="m-label">DOWNLOAD</div>
				<div class="m-value" style:color={downloadMbps !== null ? '#10b981' : undefined}>
					{fmt(downloadMbps)}<span class="m-unit">Mbps</span>
				</div>
			</div>
			<div class="metric">
				<div class="m-label">UPLOAD</div>
				<div class="m-value" style:color={uploadMbps !== null ? '#60a5fa' : undefined}>
					{fmt(uploadMbps)}<span class="m-unit">Mbps</span>
				</div>
			</div>
		</div>

		<SpeedGauge value={currentBandwidth} max={gaugeMax} phase={gaugePhase} />

		<div class="footer">
			<div class="iface-info">
				<span class="iface-label">Интерфейс</span>
				<code>{kernelInterface}</code>
			</div>

			{#if info}
				<select bind:value={selectedServerIdx} disabled={phase === 'ping' || phase === 'download' || phase === 'upload'}>
					{#each info.servers as srv, i}
						<option value={i}>{srv.label} ({srv.host}:{srv.port})</option>
					{/each}
				</select>
			{/if}

			<div class="actions">
				{#if phase === 'idle' || phase === 'done' || phase === 'error'}
					<button class="btn btn-primary btn-sm" onclick={runTest} disabled={!selectedServer}>
						{phase === 'idle' ? 'Запустить' : 'Повторить'}
					</button>
				{:else}
					<button class="btn btn-ghost btn-sm" onclick={close}>Отмена</button>
				{/if}
			</div>

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
</style>
