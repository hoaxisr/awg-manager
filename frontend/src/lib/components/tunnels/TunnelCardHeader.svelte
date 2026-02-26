<script lang="ts">
	import type { TunnelListItem } from '$lib/types';
	import { Toggle } from '$lib/components/ui';
	import { api } from '$lib/api/client';

	interface Props {
		tunnel: TunnelListItem;
		activeBackend?: 'kernel' | 'userspace';
		toggleLoading?: boolean;
		onToggleOnOff?: () => void;
	}

	let { tunnel, activeBackend = 'kernel', toggleLoading = false, onToggleOnOff }: Props = $props();

	// Toggle state — ON for intent-up states
	let isOn = $derived(
		['running', 'starting', 'needs_start', 'broken'].includes(tunnel.status)
	);

	// Toggle disabled when address conflicts with a running tunnel
	let toggleDisabled = $derived(toggleLoading || tunnel.hasAddressConflict === true);

	// LED color based on tunnel status
	let ledColor = $derived.by(() => {
		switch (tunnel.status) {
			case 'running': return 'green';
			case 'starting':
			case 'needs_start':
			case 'needs_stop': return 'yellow';
			case 'broken': return 'orange';
			default: return 'gray';
		}
	});

	// LED pulses for transitional/problem states
	let ledPulse = $derived(
		['starting', 'needs_start', 'needs_stop', 'broken'].includes(tunnel.status)
	);

	// Status hint text — only for transitional/problem states
	let statusHint = $derived.by(() => {
		// Dead tunnel being restarted by monitoring
		if (tunnel.isDeadByMonitoring && (tunnel.status === 'starting' || tunnel.status === 'needs_start')) {
			return 'Попытка восстановления';
		}
		switch (tunnel.status) {
			case 'starting': return 'Запуск...';
			case 'needs_start': return 'Ожидает запуска';
			case 'needs_stop': return 'Остановка...';
			case 'broken': return 'Сломан';
			default: return '';
		}
	});

	// Connectivity state
	type ConnectivityState = 'idle' | 'checking' | 'connected' | 'disconnected';
	let connectivity = $state<ConnectivityState>('idle');
	let latencyMs = $state<number | null>(null);

	async function checkConnectivity(): Promise<void> {
		if ((tunnel.status !== 'running' && tunnel.status !== 'broken') || tunnel.isDeadByMonitoring) {
			connectivity = 'idle';
			latencyMs = null;
			return;
		}

		connectivity = 'checking';
		try {
			const result = await api.checkConnectivity(tunnel.id);
			if (result.connected) {
				connectivity = 'connected';
				latencyMs = result.latency ?? null;
			} else {
				connectivity = 'disconnected';
				latencyMs = null;
			}
		} catch {
			connectivity = 'disconnected';
			latencyMs = null;
		}
	}

	$effect(() => {
		const isActive = (tunnel?.status === 'running' || tunnel?.status === 'broken') && !tunnel?.isDeadByMonitoring;

		if (!isActive) {
			connectivity = 'idle';
			latencyMs = null;
			return;
		}

		// Wait for handshake before first connectivity check.
		// After boot, the tunnel may be "running" but handshake takes a few seconds.
		// Without this, the first check fails → red arrow persists for 60s.
		const hasHandshake = !!tunnel?.lastHandshake;

		if (!hasHandshake) {
			// No handshake yet — show checking state, poll will re-trigger $effect
			// when lastHandshake appears (tunnel object updates from API polling)
			connectivity = 'checking';
			return;
		}

		checkConnectivity();
		const interval = setInterval(checkConnectivity, 60000);

		return () => {
			clearInterval(interval);
		};
	});
</script>

<div class="flex justify-between items-start gap-3">
	<!-- Left: name, interface, badges -->
	<div class="flex flex-col gap-1 min-w-0">
		<h3 class="text-base font-semibold">{tunnel.name}</h3>
		<div class="flex items-center gap-2 flex-wrap">
			<span class="iface-name">{tunnel.interfaceName || tunnel.id}</span>
			{#if tunnel.awgVersion && tunnel.awgVersion !== 'wg'}
				<span class="version-badge">
					{tunnel.awgVersion === 'awg2.0' ? 'AWG 2.0'
					 : tunnel.awgVersion === 'awg1.5' ? 'AWG 1.5'
					 : 'AWG 1.0'}
				</span>
			{:else if tunnel.awgVersion === 'wg'}
				<span class="version-badge version-wg">WG</span>
			{/if}
			{#if tunnel.isDeadByMonitoring}
				<span class="dead-badge" title="Туннель недоступен (ping check)">
					<svg width="10" height="10" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.5">
						<path d="M10.29 3.86L1.82 18a2 2 0 001.71 3h16.94a2 2 0 001.71-3L13.71 3.86a2 2 0 00-3.42 0z"/>
						<line x1="12" y1="9" x2="12" y2="13"/>
						<line x1="12" y1="17" x2="12.01" y2="17"/>
					</svg>
					DEAD
				</span>
			{/if}
		</div>
		<span class="version-badge version-backend">{activeBackend}</span>
	</div>

	<!-- Right: toggle with LED, status hint, connectivity -->
	<div class="flex flex-col items-end gap-1.5 shrink-0">
		<div class="flex items-center gap-2">
			<span
				class="led"
				class:led-green={ledColor === 'green'}
				class:led-yellow={ledColor === 'yellow'}
				class:led-orange={ledColor === 'orange'}
				class:led-gray={ledColor === 'gray'}
				class:led-pulse={ledPulse}
			></span>
			<span title={tunnel.hasAddressConflict ? 'Конфликт адресов — другой туннель с таким же IP уже запущен' : undefined}>
			<Toggle
				checked={isOn}
				onchange={() => onToggleOnOff?.()}
				loading={toggleLoading}
				disabled={toggleDisabled}
				variant="flip"
			/>
		</span>
		</div>
		{#if statusHint}
			<span class="status-hint">{statusHint}</span>
		{/if}
		{#if (tunnel.status === 'running' || tunnel.status === 'broken') && !tunnel.isDeadByMonitoring}
			<div class="flex items-center gap-1.5">
				{#if connectivity === 'connected' && latencyMs !== null}
					<span class="latency-value">{latencyMs}ms</span>
				{/if}
				<button
					class="connectivity-btn"
					class:connected={connectivity === 'connected'}
					class:disconnected={connectivity === 'disconnected'}
					class:checking={connectivity === 'checking'}
					onclick={checkConnectivity}
					title={connectivity === 'connected'
						? 'Связь OK'
						: connectivity === 'disconnected'
							? 'Нет связи. Нажмите для проверки'
							: 'Проверка связи...'}
				>
					{#if connectivity === 'checking'}
						<span class="connectivity-spinner"></span>
					{:else if connectivity === 'connected'}
						<svg width="12" height="12" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.5">
							<path d="M5 12.55a11 11 0 0 1 14.08 0"/>
							<path d="M1.42 9a16 16 0 0 1 21.16 0"/>
							<path d="M8.53 16.11a6 6 0 0 1 6.95 0"/>
							<circle cx="12" cy="20" r="1" fill="currentColor"/>
						</svg>
					{:else}
						<svg width="12" height="12" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.5">
							<line x1="2" y1="2" x2="22" y2="22"/>
							<path d="M8.5 16.5a5 5 0 0 1 7 0"/>
							<path d="M2 8.82a15 15 0 0 1 4.17-2.65"/>
							<path d="M10.66 5c4.01-.36 8.14.9 11.34 3.76"/>
						</svg>
					{/if}
				</button>
			</div>
		{/if}
	</div>
</div>

<style>
	/* LED indicator */
	.led {
		width: 8px;
		height: 8px;
		border-radius: 50%;
		flex-shrink: 0;
		transition: background 0.3s ease, box-shadow 0.3s ease;
	}

	.led-green {
		background: var(--success, #10b981);
		box-shadow: 0 0 6px var(--success, #10b981);
	}

	.led-yellow {
		background: var(--warning, #f59e0b);
		box-shadow: 0 0 6px var(--warning, #f59e0b);
	}

	.led-orange {
		background: #f97316;
		box-shadow: 0 0 6px #f97316;
	}

	.led-gray {
		background: var(--text-muted, #6b7280);
		box-shadow: none;
	}

	.led-pulse {
		animation: led-blink 1.5s ease-in-out infinite;
	}

	@keyframes led-blink {
		0%, 100% { opacity: 1; }
		50% { opacity: 0.3; }
	}

	/* Status hint */
	.status-hint {
		font-size: 11px;
		color: var(--text-muted);
	}

	/* Latency value */
	.latency-value {
		font-variant-numeric: tabular-nums;
		font-size: 13px;
		font-weight: 500;
		color: var(--success);
	}

	/* Connectivity button */
	.connectivity-btn {
		display: flex;
		align-items: center;
		justify-content: center;
		width: 24px;
		height: 24px;
		border: none;
		border-radius: 6px;
		cursor: pointer;
		transition: all 0.2s ease;
		background: var(--bg-tertiary);
		color: var(--text-muted);
	}

	.connectivity-btn:hover {
		background: var(--border);
	}

	.connectivity-btn.connected {
		background: rgba(16, 185, 129, 0.15);
		color: var(--success);
	}

	.connectivity-btn.disconnected {
		background: rgba(239, 68, 68, 0.15);
		color: var(--error);
	}

	.connectivity-spinner {
		width: 10px;
		height: 10px;
		border: 2px solid currentColor;
		border-top-color: transparent;
		border-radius: 50%;
		animation: spin 0.8s linear infinite;
	}

	@keyframes spin {
		to { transform: rotate(360deg); }
	}

	/* Dead badge */
	.dead-badge {
		display: inline-flex;
		align-items: center;
		gap: 4px;
		padding: 2px 8px;
		font-size: 11px;
		font-weight: 600;
		border-radius: 10px;
		background: rgba(239, 68, 68, 0.2);
		color: var(--error, #ef4444);
		animation: pulse-dead 2s ease-in-out infinite;
	}

	@keyframes pulse-dead {
		0%, 100% { opacity: 1; }
		50% { opacity: 0.7; }
	}

	/* Version badge */
	.version-badge {
		display: inline-flex;
		align-items: center;
		padding: 2px 8px;
		font-size: 11px;
		font-weight: 500;
		border-radius: 10px;
		background: var(--bg-tertiary);
		color: var(--text-muted);
	}

	.iface-name {
		font-size: 12px;
		font-family: var(--font-mono, monospace);
		color: var(--text-muted);
	}

	.version-wg {
		opacity: 0.7;
	}

	.version-backend {
		opacity: 0.65;
	}
</style>
