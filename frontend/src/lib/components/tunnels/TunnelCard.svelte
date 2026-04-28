<script lang="ts">
	import { untrack } from 'svelte';
	import type { TunnelListItem } from '$lib/types';
	import { Toggle, TrafficChart, VersionBadge, Badge } from '$lib/components/ui';
	import { tunnels } from '$lib/stores/tunnels';
	import { api } from '$lib/api/client';
	import { formatRelativeTime, formatDuration, secondsSince } from '$lib/utils/format';
	import { getTrafficRates, subscribeTraffic, loadHistory } from '$lib/stores/traffic';
	import ConnectivitySettingsModal from './ConnectivitySettingsModal.svelte';

	interface Props {
		tunnel: TunnelListItem;
		toggleLoading?: boolean;
		deleteLoading?: boolean;
		onToggleOnOff?: () => void;
		ondelete?: () => void;
		ondetail?: (id: string) => void;
	}

	let {
		tunnel,
		toggleLoading = false,
		deleteLoading = false,
		onToggleOnOff,
		ondelete,
		ondetail,
	}: Props = $props();

	// ─── Toggle / status logic ─────────────────────────────────────
	let isOn = $derived(['running', 'starting', 'broken'].includes(tunnel.status));
	let toggleDisabled = $derived(toggleLoading || tunnel.hasAddressConflict === true);

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

	let ledPulse = $derived(
		['starting', 'needs_start', 'needs_stop', 'broken'].includes(tunnel.status) ||
		(tunnel.status === 'running' && tunnel.pingCheck.status === 'recovering')
	);

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

	// ─── Connectivity (SSE-driven + manual recheck) ────────────────
	let connectivitySettingsOpen = $state(false);

	let checkMethod = $derived(tunnel.connectivityCheck?.method || 'http');
	let isCheckDisabled = $derived(checkMethod === 'disabled');

	const connMap = tunnels.connectivityMap;
	let connData = $derived(($connMap).get(tunnel.id));
	let isActive = $derived(tunnel.status === 'running' || tunnel.status === 'broken');

	type ConnectivityState = 'idle' | 'connected' | 'disconnected' | 'checking';
	let connectivity = $derived.by<ConnectivityState>(() => {
		if (!isActive || isCheckDisabled) return 'idle';
		if (!connData) return 'checking';
		return connData.connected ? 'connected' : 'disconnected';
	});
	let latencyMs = $derived(connData?.latency ?? null);

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

	// ─── Server / address parsing ───────────────────────────────────
	let showEndpoint = $state(false);

	let serverHost = $derived.by(() => {
		const endpoint = tunnel.endpoint ?? '';
		const match = endpoint.match(/^(?:\[([^\]]+)\]|([^:]+)):(\d+)$/);
		if (match) return match[1] || match[2] || endpoint;
		return endpoint;
	});

	let serverPort = $derived.by(() => {
		const endpoint = tunnel.endpoint ?? '';
		const match = endpoint.match(/:(\d+)$/);
		return match ? match[1] : '';
	});

	let addresses = $derived.by(() => {
		const addr = tunnel.address ?? '';
		const parts = addr.split(',').map((s) => s.trim());
		const ipv4 = parts.find((p) => !p.includes(':')) || '';
		const ipv6 = parts.find((p) => p.includes(':')) || '';
		return { ipv4, ipv6 };
	});

	let ipv6Display = $derived.by(() => {
		const full = addresses.ipv6;
		if (!full || full.length <= 20) return full;
		return full.slice(0, 12) + '...' + full.slice(-8);
	});

	// ─── Traffic chart (collapsible, persisted) ────────────────────
	let rxRates = $state<number[]>([]);
	let txRates = $state<number[]>([]);

	const CHART_KEY_PREFIX = 'chart_expanded_';
	// svelte-ignore state_referenced_locally — intentional: read once on mount
	let chartExpanded = $state(localStorage.getItem(CHART_KEY_PREFIX + tunnel.id) !== 'false');

	function toggleChart() {
		chartExpanded = !chartExpanded;
		localStorage.setItem(CHART_KEY_PREFIX + tunnel.id, String(chartExpanded));
	}

	let tunnelId = $derived(tunnel.id);

	$effect(() => {
		const id = tunnelId;
		const update = () => {
			const t = getTrafficRates(id);
			rxRates = t.rx;
			txRates = t.tx;
		};
		update();
		return subscribeTraffic(update);
	});

	let initialLoadDone = false;
	$effect(() => {
		const id = tunnelId;
		if (initialLoadDone) return;
		initialLoadDone = true;
		untrack(() => loadHistory(id));
	});

	// ─── Card border class hook (status-tinted) ─────────────────────
	let borderState = $derived.by<'running' | 'broken' | 'transitional' | 'disabled' | 'idle'>(() => {
		if (tunnel.status === 'running') return 'running';
		if (tunnel.status === 'broken') return 'broken';
		if (['starting', 'needs_start', 'needs_stop'].includes(tunnel.status)) return 'transitional';
		if (tunnel.status === 'disabled') return 'disabled';
		return 'idle';
	});
</script>

<div class="card border-{borderState}">
	<!-- Header -->
	<div class="header">
		<div class="head-left">
			<div class="title-line">
				<button
					type="button"
					class="tunnel-name"
					title={tunnel.name}
					onclick={() => ondetail?.(tunnel.id)}
				>
					{tunnel.name}
				</button>
				{#if tunnel.defaultRoute}
					<Badge variant="accent" size="sm">default</Badge>
				{/if}
			</div>
			<div class="meta-line">
				<span class="iface-name">{tunnel.interfaceName || tunnel.id}</span>
				{#if tunnel.backend}
					<VersionBadge kind="backend" value={tunnel.backend} />
				{/if}
				{#if tunnel.awgVersion}
					<VersionBadge kind="awg" value={tunnel.awgVersion} />
				{/if}
			</div>
		</div>

		<div class="head-right">
			<div class="led-toggle">
				<span
					class="led led-{ledColor}"
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
				<div class="connectivity-row">
					{#if !isCheckDisabled && connectivity === 'connected' && latencyMs !== null}
						<span class="latency-value">{latencyMs}ms</span>
					{/if}
					<button
						class="connectivity-gear"
						onclick={() => connectivitySettingsOpen = true}
						title="Настройки проверки связности"
					>
						<svg width="14" height="14" viewBox="0 0 20 20" fill="currentColor"><path fill-rule="evenodd" d="M7.84 1.804A1 1 0 018.82 1h2.36a1 1 0 01.98.804l.331 1.652a6.993 6.993 0 011.929 1.115l1.598-.54a1 1 0 011.186.447l1.18 2.044a1 1 0 01-.205 1.251l-1.267 1.113a7.047 7.047 0 010 2.228l1.267 1.113a1 1 0 01.206 1.25l-1.18 2.045a1 1 0 01-1.187.447l-1.598-.54a6.993 6.993 0 01-1.929 1.115l-.33 1.652a1 1 0 01-.98.804H8.82a1 1 0 01-.98-.804l-.331-1.652a6.993 6.993 0 01-1.929-1.115l-1.598.54a1 1 0 01-1.186-.447l-1.18-2.044a1 1 0 01.205-1.251l1.267-1.114a7.05 7.05 0 010-2.227L1.821 7.773a1 1 0 01-.206-1.25l1.18-2.045a1 1 0 011.187-.447l1.598.54A6.993 6.993 0 017.51 3.456l.33-1.652zM10 13a3 3 0 100-6 3 3 0 000 6z" clip-rule="evenodd" /></svg>
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
								<svg width="12" height="12" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.5"><path d="M5 12.55a11 11 0 0 1 14.08 0"/><path d="M1.42 9a16 16 0 0 1 21.16 0"/><path d="M8.53 16.11a6 6 0 0 1 6.95 0"/><circle cx="12" cy="20" r="1" fill="currentColor"/></svg>
							{:else}
								<svg width="12" height="12" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.5"><line x1="2" y1="2" x2="22" y2="22"/><path d="M8.5 16.5a5 5 0 0 1 7 0"/><path d="M2 8.82a15 15 0 0 1 4.17-2.65"/><path d="M10.66 5c4.01-.36 8.14.9 11.34 3.76"/></svg>
							{/if}
						</button>
					{/if}
				</div>
			{/if}
		</div>
	</div>

	<!-- Details -->
	<div class="details">
		<div class="kv-row">
			<div class="kv kv-grow">
				<span class="kv-label">Сервер</span>
				<span class="kv-endpoint">
					<span class="kv-value truncate" title={showEndpoint ? serverHost : ''}>{showEndpoint ? (serverHost || '—') : '•••••••••'}</span>
					<button
						class="eye-btn"
						onclick={() => showEndpoint = !showEndpoint}
						title={showEndpoint ? 'Скрыть' : 'Показать'}
					>
						{#if showEndpoint}
							<svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M1 12s4-8 11-8 11 8 11 8-4 8-11 8-11-8-11-8z"/><circle cx="12" cy="12" r="3"/></svg>
						{:else}
							<svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M17.94 17.94A10.07 10.07 0 0 1 12 20c-7 0-11-8-11-8a18.45 18.45 0 0 1 5.06-5.94M9.9 4.24A9.12 9.12 0 0 1 12 4c7 0 11 8 11 8a18.5 18.5 0 0 1-2.16 3.19m-6.72-1.07a3 3 0 1 1-4.24-4.24"/><line x1="1" y1="1" x2="23" y2="23"/></svg>
						{/if}
					</button>
				</span>
			</div>
			<div class="kv kv-shrink">
				<span class="kv-label">Порт</span>
				<span class="kv-value">{serverPort || '—'}</span>
			</div>
		</div>

		{#if tunnel.resolvedIspInterface || tunnel.ispInterface}
			{@const iface = tunnel.resolvedIspInterface || tunnel.ispInterface}
			{@const label = tunnel.resolvedIspInterfaceLabel || tunnel.ispInterfaceLabel || ''}
			<div class="kv-row">
				<div class="kv">
					<span class="kv-label">Подключение</span>
					<span class="kv-value">
						{#if label}
							{label} <span class="kv-secondary">({iface})</span>
						{:else}
							{iface}
						{/if}
					</span>
				</div>
			</div>
		{/if}

		<div class="kv-row">
			<div class="kv">
				<span class="kv-label">IPv4</span>
				<span class="kv-value">{addresses.ipv4 || '—'}</span>
			</div>
			{#if addresses.ipv6}
				<div class="kv">
					<span class="kv-label">IPv6</span>
					<span class="kv-value cursor-help" title={addresses.ipv6}>{ipv6Display}</span>
				</div>
			{/if}
		</div>

		{#if tunnel.status === 'running'}
			<hr class="divider" />
			<div class="kv-row stats-row">
				<div class="kv kv-grow">
					<span class="kv-label">Uptime</span>
					<span class="kv-value">
						{tunnel.startedAt ? formatDuration(secondsSince(tunnel.startedAt)) : '—'}
					</span>
				</div>
				<div class="kv kv-grow kv-right">
					<span class="kv-label">Handshake</span>
					<span class="kv-value" title={tunnel.lastHandshake || ''}>
						{tunnel.lastHandshake ? formatRelativeTime(tunnel.lastHandshake) : '—'}
					</span>
				</div>
			</div>
		{/if}
	</div>

	<!-- Actions -->
	<div class="actions">
		<a class="action-btn" href="/tunnels/{tunnel.id}">
			<svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><path d="M11 4H4a2 2 0 0 0-2 2v14a2 2 0 0 0 2 2h14a2 2 0 0 0 2-2v-7"/><path d="M18.5 2.5a2.121 2.121 0 0 1 3 3L12 15l-4 1 1-4 9.5-9.5z"/></svg>
			Изменить
		</a>
		<a class="action-btn" href="/tunnels/{tunnel.id}/test">
			<svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><path d="M22 11.08V12a10 10 0 1 1-5.93-9.14"/><polyline points="22,4 12,14.01 9,11.01"/></svg>
			Тест
		</a>
		<button
			class="action-btn action-danger"
			disabled={deleteLoading}
			onclick={() => ondelete?.()}
			title="Удалить туннель"
		>
			{#if deleteLoading}
				<span class="action-spinner"></span>
			{:else}
				<svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><polyline points="3,6 5,6 21,6"/><path d="M19 6v14a2 2 0 0 1-2 2H7a2 2 0 0 1-2-2V6m3 0V4a2 2 0 0 1 2-2h4a2 2 0 0 1 2 2v2"/></svg>
			{/if}
			Удалить
		</button>
	</div>

	<!-- Traffic chart (running only) -->
	{#if tunnel.status === 'running'}
		<div class="chart-section">
			<button type="button" class="chart-header" onclick={toggleChart}>
				<span class="chart-label">Трафик</span>
				<span class="chart-chevron" class:expanded={chartExpanded}>▾</span>
			</button>
			<div class="chart-body" class:expanded={chartExpanded}>
				<TrafficChart
					{rxRates}
					{txRates}
					rxTotal={tunnel.rxBytes ?? 0}
					txTotal={tunnel.txBytes ?? 0}
					height={100}
					onclick={() => ondetail?.(tunnel.id)}
				/>
			</div>
		</div>
	{/if}
</div>

<ConnectivitySettingsModal
	bind:open={connectivitySettingsOpen}
	tunnelId={tunnel.id}
	tunnelAddress={tunnel.address}
	onclose={() => connectivitySettingsOpen = false}
	onSaved={() => connectivitySettingsOpen = false}
/>

<style>
	.card {
		display: flex;
		flex-direction: column;
		gap: 12px;
		padding: 14px 16px;
		background: var(--color-bg-secondary);
		border: 1px solid var(--color-border);
		border-radius: var(--radius);
		transition: border-color var(--t-fast) ease;
	}

	.card.border-running { border-color: var(--color-success-border); }
	.card.border-broken { border-color: var(--color-broken-border); }
	.card.border-transitional { border-color: var(--color-warning-border); }
	.card.border-disabled { border-color: var(--color-text-muted); }

	/* Header */
	.header {
		display: flex;
		justify-content: space-between;
		align-items: flex-start;
		gap: 10px;
	}

	.head-left {
		display: flex;
		flex-direction: column;
		gap: 4px;
		min-width: 0;
	}

	.title-line {
		display: flex;
		align-items: center;
		gap: 8px;
		min-width: 0;
	}

	.tunnel-name {
		background: none;
		border: none;
		padding: 0;
		font: inherit;
		font-size: 15px;
		font-weight: 600;
		color: var(--color-text-primary);
		text-align: left;
		cursor: pointer;
		overflow: hidden;
		text-overflow: ellipsis;
		white-space: nowrap;
		min-width: 0;
	}

	.tunnel-name:hover {
		color: var(--color-accent);
	}

	.meta-line {
		display: flex;
		align-items: center;
		gap: 6px;
		flex-wrap: wrap;
	}

	.iface-name {
		font-size: 11px;
		font-family: var(--font-mono);
		color: var(--color-text-muted);
	}

	.head-right {
		display: flex;
		flex-direction: column;
		align-items: flex-end;
		gap: 4px;
		flex-shrink: 0;
	}

	.led-toggle {
		display: flex;
		align-items: center;
		gap: 8px;
	}

	.led {
		width: 8px;
		height: 8px;
		border-radius: 50%;
		flex-shrink: 0;
		transition: background var(--t-med) ease, box-shadow var(--t-med) ease;
	}

	.led-green {
		background: var(--color-success);
		box-shadow: 0 0 6px var(--color-success);
	}

	.led-yellow {
		background: var(--color-warning);
		box-shadow: 0 0 6px var(--color-warning);
	}

	.led-orange {
		background: var(--color-broken);
		box-shadow: 0 0 6px var(--color-broken);
	}

	.led-gray {
		background: var(--color-text-muted);
		box-shadow: none;
	}

	.led-pulse {
		animation: led-blink 1.5s ease-in-out infinite;
	}

	@keyframes led-blink {
		0%, 100% { opacity: 1; }
		50% { opacity: 0.3; }
	}

	.status-hint {
		font-size: 11px;
		color: var(--color-text-muted);
	}

	.connectivity-row {
		display: flex;
		align-items: center;
		gap: 5px;
	}

	.latency-value {
		font-variant-numeric: tabular-nums;
		font-size: 12px;
		font-weight: 500;
		color: var(--color-success);
	}

	.connectivity-gear,
	.connectivity-btn {
		display: inline-flex;
		align-items: center;
		justify-content: center;
		border: none;
		cursor: pointer;
		transition: color var(--t-fast) ease, background var(--t-fast) ease;
	}

	.connectivity-gear {
		padding: 2px;
		background: none;
		color: var(--color-text-muted);
		border-radius: var(--radius-sm);
	}

	.connectivity-gear:hover {
		color: var(--color-accent);
	}

	.connectivity-btn {
		width: 24px;
		height: 24px;
		border-radius: var(--radius-sm);
		background: var(--color-bg-tertiary);
		color: var(--color-text-muted);
	}

	.connectivity-btn:hover {
		background: var(--color-border);
	}

	.connectivity-btn.connected {
		background: var(--color-success-tint);
		color: var(--color-success);
	}

	.connectivity-btn.disconnected {
		background: var(--color-error-tint);
		color: var(--color-error);
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

	/* Details */
	.details {
		display: flex;
		flex-direction: column;
		gap: 10px;
		padding: 8px 0;
		border-top: 1px solid var(--color-border);
		border-bottom: 1px solid var(--color-border);
	}

	.kv-row {
		display: flex;
		gap: 14px;
		align-items: flex-start;
	}

	.kv {
		display: flex;
		flex-direction: column;
		gap: 2px;
		min-width: 0;
	}

	.kv-grow { flex: 1; }
	.kv-shrink { flex-shrink: 0; }
	.kv-right { align-items: flex-end; }

	.kv-label {
		font-size: 10px;
		text-transform: uppercase;
		letter-spacing: 0.04em;
		color: var(--color-text-muted);
	}

	.kv-value {
		font-size: 12px;
		font-family: var(--font-mono);
		color: var(--color-text-secondary);
	}

	.kv-secondary {
		color: var(--color-text-muted);
	}

	.kv-endpoint {
		display: flex;
		align-items: center;
		gap: 4px;
		min-width: 0;
	}

	.eye-btn {
		display: inline-flex;
		align-items: center;
		justify-content: center;
		padding: 2px;
		border: none;
		background: none;
		color: var(--color-text-muted);
		cursor: pointer;
		border-radius: var(--radius-sm);
		flex-shrink: 0;
		transition: color var(--t-fast) ease;
	}

	.eye-btn:hover {
		color: var(--color-text-secondary);
	}

	.divider {
		border: none;
		border-top: 1px dashed var(--color-border);
		margin: 4px 0;
	}

	.stats-row {
		white-space: nowrap;
	}

	.truncate {
		overflow: hidden;
		text-overflow: ellipsis;
		white-space: nowrap;
	}

	/* Actions */
	.actions {
		display: flex;
		gap: 4px;
		justify-content: flex-end;
		align-items: center;
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

	.action-danger:hover:not(:disabled) {
		color: var(--color-error);
		background: var(--color-error-tint);
	}

	.action-spinner {
		width: 12px;
		height: 12px;
		border: 2px solid currentColor;
		border-top-color: transparent;
		border-radius: 50%;
		animation: spin 0.8s linear infinite;
	}

	/* Chart */
	.chart-section {
		margin: 0 -16px -14px;
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
		background: var(--color-bg-hover);
	}

	.chart-label {
		font-size: 11px;
		font-weight: 500;
		color: var(--color-text-muted);
		text-transform: uppercase;
		letter-spacing: 0.03em;
	}

	.chart-chevron {
		font-size: 14px;
		color: var(--color-text-muted);
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
		padding: 0 12px 4px;
	}

	@media (max-width: 400px) {
		.actions {
			flex-wrap: wrap;
		}
	}
</style>
