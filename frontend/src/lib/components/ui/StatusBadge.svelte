<script lang="ts">
	let { status, size = 'sm' }: {
		status: 'running' | 'stopped' | 'broken' | 'dead' | 'paused' | 'disabled' | string;
		size?: 'xs' | 'sm';
	} = $props();

	const labels: Record<string, string> = {
		running: 'Работает',
		stopped: 'Остановлен',
		starting: 'Запускается',
		broken: 'Сломан',
		needs_start: 'Ожидает запуска',
		needs_stop: 'Ожидает остановки',
		disabled: 'Отключён',
		dead: 'DEAD',
		paused: 'Приостановлен'
	};

	const colorClasses: Record<string, string> = {
		running: 'badge-success',
		stopped: 'badge-error',
		starting: 'badge-warning',
		broken: 'badge-warning',
		needs_start: 'badge-warning',
		needs_stop: 'badge-warning',
		disabled: 'badge-muted',
		dead: 'badge-dead',
		paused: 'badge-warning'
	};
</script>

{#if status === 'dead'}
	<span class="badge badge-dead" class:xs={size === 'xs'} title="Туннель недоступен (ping check)">
		<svg width="10" height="10" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.5">
			<path d="M10.29 3.86L1.82 18a2 2 0 001.71 3h16.94a2 2 0 001.71-3L13.71 3.86a2 2 0 00-3.42 0z"/>
			<line x1="12" y1="9" x2="12" y2="13"/>
			<line x1="12" y1="17" x2="12.01" y2="17"/>
		</svg>
		DEAD
	</span>
{:else}
	<span class="badge {colorClasses[status] || 'badge-muted'}" class:xs={size === 'xs'}>
		<span class="dot"></span>
		{labels[status] || status}
	</span>
{/if}

<style>
	.badge {
		display: inline-flex;
		align-items: center;
		gap: 6px;
		padding: 4px 10px;
		font-size: 12px;
		font-weight: 500;
		border-radius: 12px;
	}

	.badge.xs {
		padding: 2px 8px;
		font-size: 11px;
		gap: 4px;
	}

	.dot {
		width: 6px;
		height: 6px;
		border-radius: 50%;
		background: currentColor;
	}

	.badge-success {
		background: rgba(16, 185, 129, 0.15);
		color: var(--success);
	}

	.badge-error {
		background: rgba(239, 68, 68, 0.15);
		color: var(--error);
	}

	.badge-dead {
		background: rgba(239, 68, 68, 0.2);
		color: var(--error);
		font-weight: 600;
	}

	.badge-warning {
		background: rgba(245, 158, 11, 0.15);
		color: var(--warning);
	}

	.badge-muted {
		background: var(--bg-tertiary);
		color: var(--text-muted);
	}
</style>
