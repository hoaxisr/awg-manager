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

	function getStatusClass(s: string): string {
		switch (s) {
			case 'alive': return 'status-alive';
			case 'dead': return 'status-dead';
			case 'paused': return 'status-paused';
			default: return 'status-disabled';
		}
	}

	function getStatusLabel(enabled: boolean, s: string): string {
		if (!enabled) return 'Отключён';
		switch (s) {
			case 'alive': return 'Активен';
			case 'dead': return 'Недоступен';
			case 'paused': return 'Пауза';
			default: return 'Отключён';
		}
	}

	let displayStatus = $derived(!tunnel.enabled ? 'disabled' : tunnel.status);
</script>

<div class="tunnel-status">
	<div class="tunnel-header">
		<span class="tunnel-name">{tunnel.tunnelName}</span>
		<div class="tunnel-actions">
			<button
				class="btn-settings"
				onclick={() => onOpenSettings(tunnel.tunnelId)}
			>
				Настройки
			</button>
			<span class="status-badge {getStatusClass(displayStatus)}">
				{getStatusLabel(tunnel.enabled, tunnel.status)}
			</span>
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
				{tunnel.method === 'http' ? 'HTTP 204' : 'ICMP'}
			</span>
			{#if tunnel.lastCheck}
				<span class="detail">
					<span class="detail-label">Проверка:</span>
					{formatTime(tunnel.lastCheck)}
				</span>
			{/if}
			{#if tunnel.failCount > 0}
				<span class="detail fail-count">
					Ошибок: {tunnel.failCount}/{tunnel.failThreshold}
				</span>
			{/if}
		</div>
	{/if}
</div>

<style>
	.tunnel-status {
		padding: 1rem;
		background: var(--bg-tertiary);
		border-radius: 8px;
	}

	.tunnel-header {
		display: flex;
		justify-content: space-between;
		align-items: center;
	}

	.tunnel-name {
		font-weight: 500;
	}

	.tunnel-actions {
		display: flex;
		align-items: center;
		gap: 0.5rem;
	}

	.btn-settings {
		background: transparent;
		border: 1px solid var(--border);
		padding: 0.2rem 0.5rem;
		cursor: pointer;
		color: var(--text-secondary);
		border-radius: 4px;
		font-size: 0.7rem;
		white-space: nowrap;
	}

	.btn-settings:hover {
		color: var(--text-primary);
		background: var(--bg-secondary);
		border-color: var(--text-muted);
	}

	.status-badge {
		padding: 0.25rem 0.5rem;
		border-radius: 4px;
		font-size: 0.75rem;
		font-weight: 500;
	}

	.status-alive {
		background: var(--success-bg, rgba(34, 197, 94, 0.1));
		color: var(--success, #22c55e);
	}

	.status-dead {
		background: var(--danger-bg, rgba(239, 68, 68, 0.1));
		color: var(--danger, #ef4444);
	}

	.status-paused {
		background: var(--warning-bg, rgba(234, 179, 8, 0.1));
		color: var(--warning, #eab308);
	}

	.status-disabled {
		background: var(--bg-secondary);
		color: var(--text-muted);
	}

	.tunnel-details {
		display: flex;
		flex-wrap: wrap;
		gap: 0.75rem;
		font-size: 0.8125rem;
		color: var(--text-secondary);
		margin-top: 0.75rem;
	}

	.detail-label {
		color: var(--text-muted);
	}

	.fail-count {
		color: var(--warning, #eab308);
	}
</style>
