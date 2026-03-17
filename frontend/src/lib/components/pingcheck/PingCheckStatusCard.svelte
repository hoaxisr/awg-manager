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
	}

	.tunnel-header {
		display: flex;
		justify-content: space-between;
		align-items: center;
		gap: 0.5rem;
	}

	.tunnel-name-row {
		display: flex;
		align-items: center;
		gap: 0.4rem;
		min-width: 0;
	}

	.tunnel-name {
		font-weight: 500;
		font-size: 0.875rem;
		min-width: 0;
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
</style>
