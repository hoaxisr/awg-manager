<script lang="ts">
	import type { PreflightCheck } from '$lib/types/serverWizard';
	import { CheckCircle2, AlertTriangle, XCircle, ChevronDown, ChevronUp } from 'lucide-svelte';

	interface Props {
		checks: PreflightCheck[];
	}

	let { checks }: Props = $props();

	let expanded = $state<Record<string, boolean>>({});

	function toggleExpand(id: string) {
		expanded[id] = !expanded[id];
	}
</script>

<div class="check-list">
	{#if checks.length === 0}
		<div class="empty-box">
			Выполняется проверка готовности системы...
		</div>
	{:else}
		{#each checks as check}
			<div class="check-item status-{check.status}">
				<div class="check-row">
					<div class="check-status-icon icon-{check.status}">
						{#if check.status === 'ok'}
							<CheckCircle2 size={18} />
						{:else if check.status === 'warning'}
							<AlertTriangle size={18} />
						{:else}
							<XCircle size={18} />
						{/if}
					</div>
					<div class="check-content">
						<div class="check-header">
							<span class="check-title">{check.title}</span>
							<span class="status-badge badge-{check.status}">
								{check.status === 'blocked'
									? 'Блокирует'
									: check.status === 'warning'
										? 'Предупреждение'
										: 'Готово'}
							</span>
						</div>
						<p class="check-msg">{check.message}</p>
					</div>

					{#if check.details || check.remediation}
						<button
							type="button"
							class="btn-expand"
							onclick={() => toggleExpand(check.id)}
							title="Подробнее"
						>
							{#if expanded[check.id]}
								<ChevronUp size={16} />
							{:else}
								<ChevronDown size={16} />
							{/if}
						</button>
					{/if}
				</div>

				{#if (check.details || check.remediation) && expanded[check.id]}
					<div class="check-extra">
						{#if check.details}
							<div class="detail-block">
								<span class="detail-label">Детали:</span>
								<pre class="detail-code">{check.details}</pre>
							</div>
						{/if}
						{#if check.remediation}
							<div class="remediation-block">
								<span class="remediation-label">Как исправить:</span>
								<p class="remediation-text">{check.remediation}</p>
							</div>
						{/if}
					</div>
				{/if}
			</div>
		{/each}
	{/if}
</div>

<style>
	.check-list {
		display: flex;
		flex-direction: column;
		gap: 0.625rem;
	}

	.empty-box {
		padding: 1.5rem;
		text-align: center;
		font-size: 0.8125rem;
		color: var(--color-text-muted);
		background: var(--color-bg-primary);
		border-radius: var(--radius);
		border: 1px solid var(--color-border);
	}

	.check-item {
		padding: 0.875rem 1rem;
		border-radius: var(--radius);
		background: var(--color-bg-primary);
		border: 1px solid var(--color-border);
		transition: border-color var(--t-fast) ease;
	}

	.check-item.status-warning {
		background: var(--color-warning-tint);
		border-color: var(--color-warning-border);
	}

	.check-item.status-blocked {
		background: var(--color-error-tint);
		border-color: var(--color-error-border);
	}

	.check-row {
		display: flex;
		align-items: flex-start;
		gap: 0.75rem;
	}

	.check-status-icon {
		margin-top: 2px;
		flex-shrink: 0;
		display: flex;
		align-items: center;
		justify-content: center;
	}

	.icon-ok {
		color: var(--color-success);
	}

	.icon-warning {
		color: var(--color-warning);
	}

	.icon-blocked {
		color: var(--color-error);
	}

	.check-content {
		flex: 1;
		min-width: 0;
		display: flex;
		flex-direction: column;
		gap: 0.25rem;
	}

	.check-header {
		display: flex;
		align-items: center;
		justify-content: space-between;
		gap: 0.5rem;
	}

	.check-title {
		font-size: 0.8125rem;
		font-weight: 600;
		color: var(--color-text-primary);
	}

	.status-badge {
		font-size: 0.625rem;
		font-weight: 700;
		text-transform: uppercase;
		letter-spacing: 0.05em;
		padding: 2px 6px;
		border-radius: var(--radius-pill);
		flex-shrink: 0;
	}

	.badge-ok {
		background: var(--color-success-tint);
		border: 1px solid var(--color-success-border);
		color: var(--color-success);
	}

	.badge-warning {
		background: var(--color-warning-tint);
		border: 1px solid var(--color-warning-border);
		color: var(--color-warning);
	}

	.badge-blocked {
		background: var(--color-error-tint);
		border: 1px solid var(--color-error-border);
		color: var(--color-error);
	}

	.check-msg {
		font-size: 0.75rem;
		color: var(--color-text-secondary);
		margin: 0;
		line-height: 1.4;
	}

	.btn-expand {
		padding: 4px;
		border: none;
		background: transparent;
		color: var(--color-text-muted);
		border-radius: var(--radius-sm);
		cursor: pointer;
		display: flex;
		align-items: center;
		justify-content: center;
		transition: color var(--t-fast) ease;
	}

	.btn-expand:hover {
		color: var(--color-text-primary);
	}

	.check-extra {
		margin-top: 0.75rem;
		padding-top: 0.75rem;
		border-top: 1px solid var(--color-border);
		display: flex;
		flex-direction: column;
		gap: 0.5rem;
	}

	.detail-block {
		display: flex;
		flex-direction: column;
		gap: 0.25rem;
	}

	.detail-label {
		font-size: 0.6875rem;
		font-weight: 600;
		color: var(--color-text-muted);
	}

	.detail-code {
		margin: 0;
		font-family: var(--font-mono);
		font-size: 0.6875rem;
		background: var(--color-bg-secondary);
		border: 1px solid var(--color-border);
		border-radius: var(--radius-sm);
		padding: 0.5rem;
		color: var(--color-text-primary);
		white-space: pre-wrap;
		word-break: break-all;
	}

	.remediation-block {
		background: var(--color-warning-tint);
		border: 1px solid var(--color-warning-border);
		border-radius: var(--radius-sm);
		padding: 0.5rem 0.75rem;
		display: flex;
		flex-direction: column;
		gap: 0.25rem;
	}

	.remediation-label {
		font-size: 0.6875rem;
		font-weight: 600;
		color: var(--color-warning);
	}

	.remediation-text {
		font-size: 0.75rem;
		color: var(--color-text-primary);
		margin: 0;
		line-height: 1.4;
	}
</style>
