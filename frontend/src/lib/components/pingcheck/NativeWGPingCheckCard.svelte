<script lang="ts">
	import type { TunnelListItem, NativePingCheckConfig, NativePingCheckStatus } from '$lib/types';
	import { Toggle } from '$lib/components/ui';

	interface Props {
		tunnel: TunnelListItem;
		status: NativePingCheckStatus | null;
		saving: boolean;
		onConfigure: (tunnelId: string, config: NativePingCheckConfig) => void;
		onRemove: (tunnelId: string) => void;
	}

	let { tunnel, status, saving, onConfigure, onRemove }: Props = $props();

	let expanded = $state(false);

	// Form fields — defaults only, synced from status via $effect
	let host = $state('8.8.8.8');
	let mode = $state<'icmp' | 'connect' | 'tls' | 'uri'>('icmp');
	let updateInterval = $state(10);
	let maxFails = $state(3);
	let minSuccess = $state(1);
	let timeout = $state(5);
	let port = $state(443);
	let restart = $state(true);

	let needsPort = $derived(mode === 'connect' || mode === 'tls');

	const presets = [
		{ label: 'ICMP 8.8.8.8', host: '8.8.8.8', mode: 'icmp' as const },
		{ label: 'ICMP 1.1.1.1', host: '1.1.1.1', mode: 'icmp' as const },
		{ label: 'TCP 8.8.8.8:53', host: '8.8.8.8', mode: 'connect' as const, port: 53 },
		{ label: 'TLS 1.1.1.1:443', host: '1.1.1.1', mode: 'tls' as const, port: 443 },
		{ label: 'HTTP cp.cloudflare.com', host: 'cp.cloudflare.com', mode: 'uri' as const, port: 80 },
	];

	function applyPreset(p: typeof presets[0]) {
		host = p.host;
		mode = p.mode;
		if (p.port) port = p.port;
	}

	function handleConfigure() {
		const config: NativePingCheckConfig = {
			host,
			mode,
			updateInterval,
			maxFails,
			minSuccess,
			timeout,
			restart,
		};
		if (needsPort) config.port = port;
		onConfigure(tunnel.id, config);
	}

	function handleRemove() {
		onRemove(tunnel.id);
	}

	// Sync form fields from status, but NOT when the user might be editing.
	$effect(() => {
		// If the card is expanded for editing, don't overwrite user input from periodic refreshes.
		// The form will be updated with fresh data once it's collapsed and reopened.
		if (expanded) {
			return;
		}

		if (status?.exists) {
			host = status.host || '8.8.8.8';
			mode = (status.mode as typeof mode) || 'icmp';
			updateInterval = status.interval || 10;
			maxFails = status.maxFails || 3;
			minSuccess = status.minSuccess || 1;
			timeout = status.timeout || 5;
			port = status.port || 443;
			restart = status.restart ?? true;
		} else {
			// If config is removed, reset form to defaults
			host = '8.8.8.8';
			mode = 'icmp';
			updateInterval = 10;
			maxFails = 3;
			minSuccess = 1;
			timeout = 5;
			port = 443;
			restart = true;
		}
	});

	let isPending = $derived(
		status?.exists && status.failCount === 0 && status.successCount === 0
	);

	let statusText = $derived.by(() => {
		if (!status?.exists) return 'Отключён';
		if (isPending) return 'Ожидание';
		if (status.status === 'pass') return 'Активен';
		if (status.status === 'fail') return 'Недоступен';
		return status.status;
	});

	let badgeClass = $derived.by(() => {
		if (!status?.exists) return 'badge-disabled';
		if (isPending) return 'badge-warning';
		if (status.status === 'pass') return 'badge-success';
		if (status.status === 'fail') return 'badge-error';
		return 'badge-disabled';
	});
</script>

<div class="card nwg-card">
	<button class="nwg-header" onclick={() => expanded = !expanded}>
		<div class="header-left">
			<span class="tunnel-name">{tunnel.name}</span>
			<span class="badge {badgeClass}">{statusText}</span>
			{#if status?.exists}
				<span class="header-meta">{status.mode} → {status.host}</span>
				<span class="header-meta">fails: {status.failCount}/{status.maxFails}</span>
			{/if}
		</div>
		<svg class="chevron" class:rotated={expanded} width="16" height="16" viewBox="0 0 24 24"
			fill="none" stroke="currentColor" stroke-width="2">
			<polyline points="6 9 12 15 18 9"/>
		</svg>
	</button>

	{#if expanded}
		<div class="nwg-body">
			<div class="presets">
				{#each presets as p}
					<button class="preset-btn" onclick={() => applyPreset(p)}>{p.label}</button>
				{/each}
			</div>

			<div class="form-grid">
				<div class="field">
					<span class="field-label">Хост</span>
					<input type="text" bind:value={host} />
				</div>

				<div class="field">
					<span class="field-label">Метод</span>
					<select bind:value={mode}>
						<option value="icmp">ICMP</option>
						<option value="connect">TCP Connect</option>
						<option value="tls">TLS</option>
						<option value="uri">HTTP/URI</option>
					</select>
				</div>

				{#if needsPort}
					<div class="field">
						<span class="field-label">Порт</span>
						<input type="number" bind:value={port} min="1" max="65535" />
					</div>
				{/if}

				<div class="field">
					<span class="field-label">Интервал (сек)</span>
					<input type="number" bind:value={updateInterval} min="5" max="300" />
				</div>

				<div class="field">
					<span class="field-label">Максимум сбоев</span>
					<input type="number" bind:value={maxFails} min="1" max="100" />
				</div>

				<div class="field">
					<span class="field-label">Минимум успехов</span>
					<input type="number" bind:value={minSuccess} min="1" max="100" />
				</div>

				<div class="field">
					<span class="field-label">Таймаут (сек)</span>
					<input type="number" bind:value={timeout} min="1" max="60" />
				</div>
			</div>

			<div class="restart-row">
				<div class="restart-info">
					<span class="restart-label">Перезапуск при dead</span>
					<span class="restart-hint">Автоматически перезапускать туннель при потере связи</span>
				</div>
				<Toggle checked={restart} onchange={() => restart = !restart} size="sm" />
			</div>

			<div class="nwg-actions">
				<button class="btn btn-primary btn-sm" onclick={handleConfigure} disabled={saving}>
					{status?.exists ? 'Обновить' : 'Включить'}
				</button>
				{#if status?.exists}
					<button class="btn btn-danger btn-sm" onclick={handleRemove} disabled={saving}>
						Отключить
					</button>
				{/if}
			</div>
		</div>
	{/if}
</div>

<style>
	.nwg-card {
		padding: 0;
		overflow: hidden;
	}

	.nwg-header {
		display: flex;
		align-items: center;
		justify-content: space-between;
		width: 100%;
		padding: 0.875rem 1rem;
		background: none;
		border: none;
		color: inherit;
		cursor: pointer;
		text-align: left;
	}

	.nwg-header:hover {
		background: var(--bg-hover);
	}

	.header-left {
		display: flex;
		align-items: center;
		gap: 0.75rem;
		flex-wrap: wrap;
	}

	.tunnel-name {
		font-weight: 600;
		font-size: 0.9375rem;
	}

	.badge-disabled {
		background: rgba(115, 122, 162, 0.15);
		color: var(--text-muted);
	}

	.header-meta {
		font-size: 0.75rem;
		color: var(--text-muted);
		font-family: var(--font-mono, monospace);
	}

	.chevron {
		transition: transform 0.2s;
		flex-shrink: 0;
		color: var(--text-muted);
	}

	.chevron.rotated {
		transform: rotate(180deg);
	}

	.nwg-body {
		padding: 0.875rem 1rem 1rem;
		border-top: 1px solid var(--border);
		display: flex;
		flex-direction: column;
		gap: 0.875rem;
	}

	.presets {
		display: flex;
		flex-wrap: wrap;
		gap: 0.375rem;
	}

	.preset-btn {
		padding: 0.125rem 0.5rem;
		font-size: 0.75rem;
		font-family: var(--font-mono, monospace);
		border-radius: 10px;
		border: 1px solid var(--border);
		background: var(--bg-primary);
		color: var(--text-muted);
		cursor: pointer;
		transition: all 0.15s ease;
	}

	.preset-btn:hover {
		background: var(--accent);
		border-color: var(--accent);
		color: white;
	}

	.form-grid {
		display: grid;
		grid-template-columns: repeat(auto-fill, minmax(140px, 1fr));
		gap: 0.75rem;
	}

	.field {
		display: flex;
		flex-direction: column;
		gap: 0.25rem;
	}

	.field-label {
		font-size: 0.6875rem;
		text-transform: uppercase;
		color: var(--text-muted);
	}

	.restart-row {
		display: flex;
		justify-content: space-between;
		align-items: center;
		padding: 0.625rem 0.75rem;
		background: var(--bg-primary);
		border: 1px solid var(--border);
		border-radius: 6px;
	}

	.restart-info {
		display: flex;
		flex-direction: column;
		gap: 0.125rem;
	}

	.restart-label {
		font-size: 0.8125rem;
		font-weight: 500;
	}

	.restart-hint {
		font-size: 0.6875rem;
		color: var(--text-muted);
	}

	.nwg-actions {
		display: flex;
		gap: 0.5rem;
	}

	@media (max-width: 640px) {
		.form-grid {
			grid-template-columns: repeat(2, 1fr);
		}
	}
</style>
