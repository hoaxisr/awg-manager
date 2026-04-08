<script lang="ts">
	import type { TunnelListItem } from '$lib/types';
	import { Toggle } from '$lib/components/ui';
	import { tunnels } from '$lib/stores/tunnels';
	import { api } from '$lib/api/client';
	import ConnectivitySettingsModal from './ConnectivitySettingsModal.svelte';

	interface Props {
		tunnel: TunnelListItem;
		toggleLoading?: boolean;
		onToggleOnOff?: () => void;
	}

	let { tunnel, toggleLoading = false, onToggleOnOff }: Props = $props();

	// Toggle state — ON only for states where the tunnel is actually working
	// (or transitioning into it). needs_start is "intent up but not running",
	// so the toggle should show OFF and clicking it should fire Start.
	let isOn = $derived(
		['running', 'starting', 'broken'].includes(tunnel.status)
	);

	// Toggle disabled when address conflicts with a running tunnel
	let toggleDisabled = $derived(toggleLoading || tunnel.hasAddressConflict === true);

	// LED color based on tunnel status
	let ledColor = $derived.by(() => {
		switch (tunnel.status) {
			case 'running':
				return tunnel.pingCheck.status === 'recovering' ? 'orange' : 'green';
			case 'starting':
			case 'needs_start':
			case 'needs_stop': return 'yellow';
			case 'broken': return 'orange';
			default: return 'gray';
		}
	});

	// LED pulses for transitional/problem states
	let ledPulse = $derived(
		['starting', 'needs_start', 'needs_stop', 'broken'].includes(tunnel.status) ||
		(tunnel.status === 'running' && tunnel.pingCheck.status === 'recovering')
	);

	let connectivitySettingsOpen = $state(false);

	let checkMethod = $derived(tunnel.connectivityCheck?.method || 'http');
	let isCheckDisabled = $derived(checkMethod === 'disabled');

	// Status hint text — only for transitional/problem states
	let statusHint = $derived.by(() => {
		switch (tunnel.status) {
			case 'starting': return 'Запуск...';
			case 'needs_start': return 'Ожидает запуска';
			case 'needs_stop': return 'Остановка...';
			case 'broken': return 'Сломан';
			case 'running':
				if (tunnel.pingCheck.status === 'recovering') {
					const n = tunnel.pingCheck.restartCount;
					return `Восстановление (попытка ${n})`;
				}
				return '';
			default: return '';
		}
	});

	// Connectivity from SSE-driven store
	const connMap = tunnels.connectivityMap;
	let connData = $derived(($connMap).get(tunnel.id));

	let isActive = $derived(tunnel.status === 'running' || tunnel.status === 'broken');

	type ConnectivityState = 'idle' | 'connected' | 'disconnected' | 'checking';
	let connectivity = $derived.by<ConnectivityState>(() => {
		if (!isActive || isCheckDisabled) return 'idle';
		if (!connData) return 'checking'; // waiting for handshake + first check
		return connData.connected ? 'connected' : 'disconnected';
	});
	let latencyMs = $derived(connData?.latency ?? null);

	// Manual connectivity check (one-shot)
	let manualChecking = $state(false);
	async function checkConnectivityManual(): Promise<void> {
		if (manualChecking) return;
		manualChecking = true;
		try {
			const result = await api.checkConnectivity(tunnel.id);
			tunnels.updateConnectivity(tunnel.id, result.connected, result.latency ?? null);
		} catch {
			tunnels.updateConnectivity(tunnel.id, false, null);
		} finally {
			manualChecking = false;
		}
	}
</script>

<div class="flex justify-between items-start gap-3">
	<!-- Left: name, interface, badges -->
	<div class="flex flex-col gap-1 min-w-0">
		<h3 class="tunnel-name" title={tunnel.name}>{tunnel.name}</h3>
		<div class="flex items-center gap-2">
			<span class="iface-name">{tunnel.interfaceName || tunnel.id}</span>
			{#if tunnel.backend}
				<span class="version-badge version-backend">{tunnel.backend === 'nativewg' ? 'NativeWG' : 'Kernel'}</span>
			{/if}
		</div>
		<div class="flex items-center gap-2 flex-wrap">
			{#if tunnel.awgVersion && tunnel.awgVersion !== 'wg'}
				<span class="version-badge">
					{tunnel.awgVersion === 'awg2.0' ? 'AWG 2.0'
					 : tunnel.awgVersion === 'awg1.5' ? 'AWG 1.5'
					 : 'AWG 1.0'}
				</span>
			{:else if tunnel.awgVersion === 'wg'}
				<span class="version-badge version-wg">WG</span>
			{/if}
		</div>
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
		{#if tunnel.status === 'running' || tunnel.status === 'broken'}
			<div class="flex items-center gap-1.5">
				{#if !isCheckDisabled && connectivity === 'connected' && latencyMs !== null}
					<span class="latency-value">{latencyMs}ms</span>
				{/if}
				<button
					class="connectivity-gear"
					onclick={() => connectivitySettingsOpen = true}
					title="Настройки проверки связности"
				>
					<svg width="14" height="14" viewBox="0 0 20 20" fill="currentColor">
						<path fill-rule="evenodd" d="M7.84 1.804A1 1 0 018.82 1h2.36a1 1 0 01.98.804l.331 1.652a6.993 6.993 0 011.929 1.115l1.598-.54a1 1 0 011.186.447l1.18 2.044a1 1 0 01-.205 1.251l-1.267 1.113a7.047 7.047 0 010 2.228l1.267 1.113a1 1 0 01.206 1.25l-1.18 2.045a1 1 0 01-1.187.447l-1.598-.54a6.993 6.993 0 01-1.929 1.115l-.33 1.652a1 1 0 01-.98.804H8.82a1 1 0 01-.98-.804l-.331-1.652a6.993 6.993 0 01-1.929-1.115l-1.598.54a1 1 0 01-1.186-.447l-1.18-2.044a1 1 0 01.205-1.251l1.267-1.114a7.05 7.05 0 010-2.227L1.821 7.773a1 1 0 01-.206-1.25l1.18-2.045a1 1 0 011.187-.447l1.598.54A6.993 6.993 0 017.51 3.456l.33-1.652zM10 13a3 3 0 100-6 3 3 0 000 6z" clip-rule="evenodd" />
					</svg>
				</button>
				{#if !isCheckDisabled}
					<button
						class="connectivity-btn"
						class:connected={connectivity === 'connected'}
						class:disconnected={connectivity === 'disconnected'}
						class:checking={manualChecking}
						onclick={checkConnectivityManual}
						title={connectivity === 'connected'
							? 'Связь OK'
							: connectivity === 'disconnected'
								? 'Нет связи. Нажмите для проверки'
								: 'Проверка связи...'}
					>
						{#if manualChecking}
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
				{/if}
			</div>
		{/if}
	</div>
</div>

<ConnectivitySettingsModal
	bind:open={connectivitySettingsOpen}
	tunnelId={tunnel.id}
	tunnelAddress={tunnel.address}
	onclose={() => connectivitySettingsOpen = false}
	onSaved={() => connectivitySettingsOpen = false}
/>

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

	.tunnel-name {
		font-size: 1rem;
		font-weight: 600;
		overflow: hidden;
		text-overflow: ellipsis;
		white-space: nowrap;
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

	.connectivity-gear {
		display: flex;
		align-items: center;
		justify-content: center;
		padding: 2px;
		background: none;
		border: none;
		color: var(--text-muted);
		cursor: pointer;
		border-radius: 4px;
		transition: color 0.15s;
	}

	.connectivity-gear:hover {
		color: var(--accent);
	}
</style>
