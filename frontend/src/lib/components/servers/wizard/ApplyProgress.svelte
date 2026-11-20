<script lang="ts">
	import type { JobStatusResponse } from '$lib/types/serverWizard';
	import { Button } from '$lib/components/ui';
	import { CheckCircle2, AlertOctagon, Loader2, Ban } from 'lucide-svelte';

	interface Props {
		job: JobStatusResponse;
		cancelling?: boolean;
		oncancel?: () => void;
	}

	let { job, cancelling = false, oncancel }: Props = $props();

	const isFinished = $derived(job.phase === 'succeeded' || job.phase === 'failed' || job.phase === 'cancelled');
	const canCancel = $derived(!isFinished && job.phase !== 'committing' && !cancelling);
</script>

<div class="progress-container">
	<!-- Status Icon & Phase -->
	<div class="status-box">
		{#if job.phase === 'succeeded'}
			<div class="status-icon icon-succeeded">
				<CheckCircle2 size={32} />
			</div>
			<div class="status-title">Применение успешно завершено</div>
		{:else if job.phase === 'failed' || job.phase === 'recovery_required'}
			<div class="status-icon icon-failed">
				<AlertOctagon size={32} />
			</div>
			<div class="status-title title-error">Ошибка применения конфигурации</div>
		{:else if job.phase === 'cancelled'}
			<div class="status-icon icon-cancelled">
				<Ban size={32} />
			</div>
			<div class="status-title">Операция отменена</div>
		{:else}
			<div class="status-icon icon-applying">
				<Loader2 size={32} class="animate-spin" />
			</div>
			<div class="status-title">
				{job.phase === 'committing' ? 'Фиксация конфигурации (не закрывайте окно)...' : 'Применение изменений...'}
			</div>
		{/if}

		<p class="status-desc">
			{job.current_step || 'Выполняются операции координатора...'}
		</p>
	</div>

	<!-- Progress Bar -->
	<div class="progress-bar-group">
		<div class="progress-meta">
			<span>Прогресс</span>
			<span class="progress-percent">{job.progress}%</span>
		</div>
		<div class="progress-track">
			<div
				class="progress-fill phase-{job.phase}"
				style="width: {job.progress}%"
			></div>
		</div>
	</div>

	<!-- Error Alert -->
	{#if job.error}
		<div class="error-alert">
			<div class="error-header">
				<AlertOctagon size={16} />
				<span>Ошибка ({job.error_code || 'ERROR'})</span>
			</div>
			<p class="error-msg">{job.error}</p>
		</div>
	{/if}

	<!-- Cancel Option -->
	{#if oncancel && !isFinished}
		<div class="cancel-row">
			<Button
				variant="secondary"
				size="sm"
				onclick={oncancel}
				disabled={!canCancel}
				title={job.phase === 'committing' ? 'Фиксация транзакции не может быть прервана' : 'Прервать операцию'}
			>
				{#if cancelling}
					<Loader2 size={14} class="animate-spin mr-1.5" />
					Отмена...
				{:else}
					Отменить операцию
				{/if}
			</Button>
		</div>
	{/if}
</div>

<style>
	.progress-container {
		display: flex;
		flex-direction: column;
		gap: 1.5rem;
		padding: 1rem 0;
	}

	.status-box {
		display: flex;
		flex-direction: column;
		align-items: center;
		justify-content: center;
		text-align: center;
		gap: 0.75rem;
	}

	.status-icon {
		width: 56px;
		height: 56px;
		border-radius: 50%;
		display: flex;
		align-items: center;
		justify-content: center;
	}

	.icon-succeeded {
		background: var(--color-success-tint);
		border: 1px solid var(--color-success-border);
		color: var(--color-success);
	}

	.icon-failed {
		background: var(--color-error-tint);
		border: 1px solid var(--color-error-border);
		color: var(--color-error);
	}

	.icon-cancelled {
		background: var(--color-bg-tertiary);
		border: 1px solid var(--color-border);
		color: var(--color-text-muted);
	}

	.icon-applying {
		background: var(--color-accent-tint);
		border: 1px solid var(--color-accent-border);
		color: var(--color-accent);
	}

	.status-title {
		font-size: 1rem;
		font-weight: 600;
		color: var(--color-text-primary);
	}

	.title-error {
		color: var(--color-error);
	}

	.status-desc {
		font-size: 0.75rem;
		color: var(--color-text-muted);
		max-width: 28rem;
		margin: 0;
		line-height: 1.4;
	}

	.progress-bar-group {
		display: flex;
		flex-direction: column;
		gap: 0.375rem;
	}

	.progress-meta {
		display: flex;
		justify-content: space-between;
		font-size: 0.75rem;
		font-weight: 600;
		color: var(--color-text-secondary);
	}

	.progress-percent {
		font-family: var(--font-mono);
	}

	.progress-track {
		width: 100%;
		height: 8px;
		background: var(--color-bg-primary);
		border: 1px solid var(--color-border);
		border-radius: var(--radius-pill);
		overflow: hidden;
	}

	.progress-fill {
		height: 100%;
		border-radius: var(--radius-pill);
		transition: width 0.3s ease;
		background: var(--color-accent);
	}

	.progress-fill.phase-succeeded {
		background: var(--color-success);
	}

	.progress-fill.phase-failed,
	.progress-fill.phase-recovery_required {
		background: var(--color-error);
	}

	.error-alert {
		padding: 0.875rem 1rem;
		border-radius: var(--radius);
		background: var(--color-error-tint);
		border: 1px solid var(--color-error-border);
		display: flex;
		flex-direction: column;
		gap: 0.25rem;
	}

	.error-header {
		font-size: 0.75rem;
		font-weight: 600;
		color: var(--color-error);
		display: flex;
		align-items: center;
		gap: 0.375rem;
	}

	.error-msg {
		font-family: var(--font-mono);
		font-size: 0.6875rem;
		color: var(--color-text-primary);
		margin: 0;
		word-break: break-all;
	}

	.cancel-row {
		display: flex;
		justify-content: center;
		padding-top: 0.5rem;
	}
</style>
