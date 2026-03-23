<script lang="ts">
	import type { TunnelPingStatus } from '$lib/types';
	import { formatTime } from '$lib/utils/format';
	import { Toggle } from '$lib/components/ui';

	interface Props {
		tunnel: TunnelPingStatus;
		toggleLoading: boolean;
		onOpenSettings: (id: string) => void;
		onToggleEnabled: (id: string) => void;
	}

	let { tunnel, toggleLoading, onOpenSettings, onToggleEnabled }: Props = $props();

	function getMonitorLabel(enabled: boolean, s: string): string {
		if (!enabled) return 'Отключён';
		if (s === 'paused') return 'Ожидание';
		return 'Активен';
	}

	function getCheckLabel(s: string): string {
		return s === 'dead' ? 'Проверки неуспешны' : 'Проверки успешны';
	}

	function getMethodLabel(method: string): string {
		switch (method) {
			case 'http': return 'HTTP 204';
			case 'icmp': return 'ICMP';
			case 'connect': return 'TCP Connect';
			case 'tls': return 'TLS';
			case 'uri': return 'URI';
			default: return method || 'HTTP 204';
		}
	}

	let isNativeWG = $derived(tunnel.backend === 'nativewg');

	let monitorBadgeClass = $derived.by(() => {
		if (!tunnel.enabled) return 'badge-disabled';
		if (tunnel.status === 'paused') return 'badge-warning';
		return 'badge-success';
	});

	let showCheckBadge = $derived(tunnel.enabled && tunnel.status !== 'paused');
	let checkBadgeClass = $derived(tunnel.status === 'dead' ? 'badge-error' : 'badge-success');
</script>

<div class="tunnel-status">
	<div class="tunnel-header">
		<div class="tunnel-name-row">
			<span class="tunnel-name" title={tunnel.tunnelName}>{tunnel.tunnelName}</span>
			{#if isNativeWG}
				<span class="backend-badge">NDMS</span>
			{/if}
		</div>
		<div class="tunnel-actions">
			{#if tunnel.enabled}
				<button class="btn-settings" onclick={() => onOpenSettings(tunnel.tunnelId)} title="Настройки">
					<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 20 20" fill="currentColor">
						<path fill-rule="evenodd" d="M7.84 1.804A1 1 0 018.82 1h2.36a1 1 0 01.98.804l.331 1.652a6.993 6.993 0 011.929 1.115l1.598-.54a1 1 0 011.186.447l1.18 2.044a1 1 0 01-.205 1.251l-1.267 1.113a7.047 7.047 0 010 2.228l1.267 1.113a1 1 0 01.206 1.25l-1.18 2.045a1 1 0 01-1.187.447l-1.598-.54a6.993 6.993 0 01-1.929 1.115l-.33 1.652a1 1 0 01-.98.804H8.82a1 1 0 01-.98-.804l-.331-1.652a6.993 6.993 0 01-1.929-1.115l-1.598.54a1 1 0 01-1.186-.447l-1.18-2.044a1 1 0 01.205-1.251l1.267-1.114a7.05 7.05 0 010-2.227L1.821 7.773a1 1 0 01-.206-1.25l1.18-2.045a1 1 0 011.187-.447l1.598.54A6.993 6.993 0 017.51 3.456l.33-1.652zM10 13a3 3 0 100-6 3 3 0 000 6z" clip-rule="evenodd" />
					</svg>
				</button>
			{/if}
			<span class="badge {monitorBadgeClass}">
				{getMonitorLabel(tunnel.enabled, tunnel.status)}
			</span>
			{#if showCheckBadge}
				<span class="badge {checkBadgeClass}">
					{getCheckLabel(tunnel.status)}
				</span>
			{/if}
			<Toggle
				checked={tunnel.enabled}
				onchange={() => onToggleEnabled(tunnel.tunnelId)}
				loading={toggleLoading}
				size="sm"
			/>
		</div>
	</div>
	{#if tunnel.enabled}
		<div class="tunnel-details">
			<span class="detail">
				<span class="detail-label">Метод:</span>
				{getMethodLabel(tunnel.method)}
			</span>
			{#if isNativeWG}
				{#if tunnel.successCount != null || tunnel.failCount > 0}
					<span class="detail detail-success">
						<span class="detail-label">Успехов:</span> {tunnel.successCount ?? 0}
					</span>
					<span class="detail" class:detail-fail={tunnel.failCount > 0}>
						<span class="detail-label">Ошибок:</span> {tunnel.failCount}
					</span>
				{/if}
			{:else}
				{#if tunnel.lastCheck}
					<span class="detail">
						<span class="detail-label">Проверка:</span>
						{formatTime(tunnel.lastCheck)}
					</span>
				{/if}
				{#if tunnel.lastLatency > 0}
					<span class="detail">
						<span class="detail-label">Задержка:</span>
						{tunnel.lastLatency} мс
					</span>
				{/if}
				{#if tunnel.failCount > 0}
					<span class="detail detail-fail">
						Ошибок: {tunnel.failCount}/{tunnel.failThreshold}
					</span>
				{/if}
			{/if}
		</div>
	{/if}
</div>

<style>
	.tunnel-status {
		padding: 0.875rem;
		background: var(--bg-primary);
		border: 1px solid var(--border);
		border-radius: 6px;
		min-width: 0;
		overflow: hidden;
	}

	.tunnel-header {
		display: flex;
		justify-content: space-between;
		align-items: center;
		gap: 0.5rem;
		min-width: 0;
	}

	.tunnel-name-row {
		display: flex;
		align-items: center;
		gap: 0.4rem;
		min-width: 0;
		overflow: hidden;
	}

	.tunnel-name {
		font-weight: 500;
		font-size: 0.875rem;
		min-width: 0;
		max-width: 12rem;
		overflow: hidden;
		text-overflow: ellipsis;
		white-space: nowrap;
	}

	.backend-badge {
		font-size: 0.6rem;
		font-weight: 600;
		padding: 0.1rem 0.35rem;
		border-radius: 3px;
		background: rgba(122, 162, 247, 0.15);
		color: var(--accent);
		white-space: nowrap;
		flex-shrink: 0;
	}

	.tunnel-actions {
		display: flex;
		align-items: center;
		gap: 0.5rem;
		flex-shrink: 0;
	}

	.badge-disabled {
		background: rgba(115, 122, 162, 0.15);
		color: var(--text-muted);
	}

	.tunnel-details {
		display: flex;
		flex-wrap: wrap;
		gap: 0.75rem;
		font-size: 0.75rem;
		color: var(--text-secondary);
		margin-top: 0.625rem;
	}

	.detail-label {
		color: var(--text-muted);
	}

	.detail-fail {
		color: var(--warning);
	}

	.detail-success {
		color: var(--success);
	}

	.btn-settings {
		display: flex;
		align-items: center;
		justify-content: center;
		padding: 0.375rem;
		background: transparent;
		border: none;
		border-radius: var(--radius-sm, 4px);
		color: var(--text-secondary);
		cursor: pointer;
		transition: color 0.15s ease, background 0.15s ease;
	}

	.btn-settings:hover {
		color: var(--accent);
		background: var(--bg-hover);
	}

	.btn-settings svg {
		width: 1.25rem;
		height: 1.25rem;
	}
</style>
