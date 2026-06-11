<script lang="ts">
	import { Check, Download, RefreshCw, Save, SaveAll, X } from 'lucide-svelte';
	import { Button, BackLink, type ButtonVariant } from '$lib/components/ui';

	type ActionStatus = 'loading' | 'success' | 'error';

	interface Props {
		tunnelName: string;
		tunnelState: string;
		saving: boolean;
		actionStatus: ActionStatus | null;
		onReplace?: () => void;
		onExport?: () => void;
		onSaveOnly?: () => void;
		onSaveAndStart: () => void;
	}

	let {
		tunnelName,
		tunnelState,
		saving,
		actionStatus,
		onReplace,
		onExport,
		onSaveOnly,
		onSaveAndStart
	}: Props = $props();

	const primaryVariant = $derived<ButtonVariant>(
		actionStatus === 'success' ? 'success' :
		actionStatus === 'error' ? 'danger' :
		'primary'
	);
</script>

<div class="sticky-header">
	<div class="header-left flex items-center gap-4">
		<BackLink href="/" />
		<div class="flex items-center gap-2.5">
			<h1 class="page-title text-lg font-semibold">{tunnelName}</h1>
			<span class="badge" class:badge-success={tunnelState === 'running'} class:badge-warning={tunnelState === 'starting' || tunnelState === 'broken' || tunnelState === 'needs_start' || tunnelState === 'needs_stop' || tunnelState === 'stopping'} class:badge-muted={tunnelState === 'disabled'} class:badge-error={tunnelState === 'stopped' || tunnelState === 'not_created'}>
				<span class="w-1.5 h-1.5 rounded-full bg-current"></span>
				{tunnelState === 'running' ? 'Работает'
				 : tunnelState === 'starting' ? 'Запускается'
				 : tunnelState === 'needs_start' ? 'Ожидает запуска'
				 : tunnelState === 'needs_stop' ? 'Ожидает остановки'
				 : tunnelState === 'stopping' ? 'Останавливается'
				 : tunnelState === 'disabled' ? 'Отключён'
				 : tunnelState === 'broken' ? 'Сломан'
				 : 'Остановлен'}
			</span>
		</div>
	</div>

	<div class="header-actions flex items-center gap-2">
		{#if onReplace}
			<!-- TODO Phase 1: secondary variant with accent-tinted border (was .btn-replace) -->
			<Button variant="secondary" onclick={onReplace}>
				{#snippet iconBefore()}
					<RefreshCw size={16} strokeWidth={2} aria-hidden="true" />
				{/snippet}
				Заменить
			</Button>
		{/if}
		{#if onExport}
			<Button variant="secondary" onclick={onExport}>
				{#snippet iconBefore()}
					<Download size={16} strokeWidth={2} aria-hidden="true" />
				{/snippet}
				Скачать
			</Button>
		{/if}
		{#if onSaveOnly}
			<Button variant="secondary" disabled={saving} onclick={onSaveOnly}>
				{#snippet iconBefore()}
					<Save size={16} strokeWidth={2} aria-hidden="true" />
				{/snippet}
				Сохранить
			</Button>
		{/if}
		{#snippet successIcon()}
			<Check size={16} strokeWidth={2} aria-hidden="true" />
		{/snippet}
		{#snippet errorIcon()}
			<X size={16} strokeWidth={2} aria-hidden="true" />
		{/snippet}
		{#snippet saveIcon()}
			<SaveAll size={16} strokeWidth={2} aria-hidden="true" />
		{/snippet}
		<Button
			variant={primaryVariant}
			disabled={saving}
			loading={actionStatus === 'loading'}
			iconBefore={actionStatus === 'success' ? successIcon : actionStatus === 'error' ? errorIcon : actionStatus === 'loading' ? undefined : saveIcon}
			onclick={onSaveAndStart}
		>
			{#if actionStatus === 'loading'}
				Сохранение...
			{:else if actionStatus === 'success'}
				Сохранено
			{:else if actionStatus === 'error'}
				Ошибка
			{:else}
				{tunnelState === 'running' ? 'Сохранить и перезапустить' : 'Сохранить и запустить'}
			{/if}
		</Button>
	</div>
</div>

<style>
	/* Sticky header */
	.sticky-header {
		position: sticky;
		top: 56px;
		z-index: var(--z-sticky-secondary);
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
			flex-wrap: wrap;
		}

	}
</style>
