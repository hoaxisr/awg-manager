<script lang="ts">
	type ActionStatus = 'loading' | 'success' | 'error';

	interface Props {
		tunnelName: string;
		tunnelState: string;
		saving: boolean;
		actionStatus: ActionStatus | null;
		onSaveOnly?: () => void;
		onSaveAndStart: () => void;
	}

	let {
		tunnelName,
		tunnelState,
		saving,
		actionStatus,
		onSaveOnly,
		onSaveAndStart
	}: Props = $props();
</script>

<div class="sticky-header">
	<div class="header-left flex items-center gap-4">
		<a href="/" class="back-link">
			<svg width="20" height="20" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2">
				<line x1="19" y1="12" x2="5" y2="12"/>
				<polyline points="12 19 5 12 12 5"/>
			</svg>
			Назад
		</a>
		<div class="flex items-center gap-2.5">
			<h1 class="page-title text-lg font-semibold">{tunnelName}</h1>
			<span class="badge" class:badge-success={tunnelState === 'running'} class:badge-warning={tunnelState === 'starting' || tunnelState === 'broken' || tunnelState === 'needs_start' || tunnelState === 'needs_stop'} class:badge-muted={tunnelState === 'disabled'} class:badge-error={tunnelState === 'stopped' || tunnelState === 'not_created'}>
				<span class="w-1.5 h-1.5 rounded-full bg-current"></span>
				{tunnelState === 'running' ? 'Работает'
				 : tunnelState === 'starting' ? 'Запускается'
				 : tunnelState === 'needs_start' ? 'Ожидает запуска'
				 : tunnelState === 'needs_stop' ? 'Ожидает остановки'
				 : tunnelState === 'disabled' ? 'Отключён'
				 : tunnelState === 'broken' ? 'Сломан'
				 : 'Остановлен'}
			</span>
		</div>
	</div>

	<div class="header-actions flex items-center gap-2">
		{#if onSaveOnly}
			<button
				type="button"
				class="btn btn-secondary"
				disabled={saving}
				onclick={onSaveOnly}
			>
				Сохранить
			</button>
		{/if}
		<button
			type="button"
			class="btn btn-primary"
			class:btn-loading={actionStatus === 'loading'}
			class:btn-success={actionStatus === 'success'}
			class:btn-error={actionStatus === 'error'}
			disabled={saving}
			onclick={onSaveAndStart}
		>
			{#if actionStatus === 'loading'}
				<span class="spinner"></span>
				Сохранение...
			{:else if actionStatus === 'success'}
				<svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2">
					<polyline points="20 6 9 17 4 12"/>
				</svg>
				Сохранено
			{:else if actionStatus === 'error'}
				<svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2">
					<line x1="18" y1="6" x2="6" y2="18"/><line x1="6" y1="6" x2="18" y2="18"/>
				</svg>
				Ошибка
			{:else}
				<svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2">
					<path d="M19 21H5a2 2 0 0 1-2-2V5a2 2 0 0 1 2-2h11l5 5v11a2 2 0 0 1-2 2z"/>
					<polyline points="17 21 17 13 7 13 7 21"/>
					<polyline points="7 3 7 8 15 8"/>
				</svg>
				{tunnelState === 'running' ? 'Сохранить и перезапустить' : 'Сохранить и запустить'}
			{/if}
		</button>
	</div>
</div>

<style>
	/* Sticky header */
	.sticky-header {
		position: sticky;
		top: 56px;
		z-index: 50;
		display: flex;
		justify-content: space-between;
		align-items: center;
		gap: 16px;
		padding: 12px 16px;
		margin: -16px -16px 20px -16px;
		background: var(--bg-primary);
		border-bottom: 1px solid var(--border);
		flex-wrap: wrap;
	}

	.back-link {
		display: inline-flex;
		align-items: center;
		gap: 4px;
		color: var(--text-secondary);
		font-size: 13px;
		padding: 6px 10px;
		border-radius: 6px;
		transition: all 0.15s;
	}

	.back-link:hover {
		background: var(--bg-tertiary);
		color: var(--text-primary);
	}

	/* Badge */
	.badge {
		display: flex;
		align-items: center;
		gap: 6px;
		padding: 4px 10px;
		font-size: 12px;
		font-weight: 500;
		border-radius: 12px;
	}

	.badge-success {
		background: rgba(16, 185, 129, 0.15);
		color: var(--success);
	}

	.badge-error {
		background: rgba(239, 68, 68, 0.15);
		color: var(--error);
	}

	.badge-warning {
		background: rgba(245, 158, 11, 0.15);
		color: var(--warning, #f59e0b);
	}

	.badge-muted {
		background: var(--bg-tertiary);
		color: var(--text-muted);
	}

	.btn-success {
		background: var(--success) !important;
		color: white !important;
		pointer-events: none;
	}

	.btn-error {
		background: var(--error) !important;
		color: white !important;
		pointer-events: none;
	}

	@media (max-width: 600px) {
		.sticky-header {
			padding: 10px 12px;
			margin: -12px -12px 16px -12px;
		}

		.header-left {
			flex-wrap: wrap;
			gap: 8px;
		}

		.page-title {
			font-size: 16px;
		}

		.header-actions {
			width: 100%;
			justify-content: flex-end;
		}

	}
</style>
