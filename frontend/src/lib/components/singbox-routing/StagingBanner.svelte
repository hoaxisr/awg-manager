<script lang="ts">
	import { singboxRouter } from '$lib/stores/singboxRouter';
	import { api } from '$lib/api/client';
	import { Button, Modal } from '$lib/components/ui';
	import { formatTime } from '$lib/utils/format';
	import { stripAnsi } from '$lib/utils/ansi';
	import type { RouterStagingValidationError, RouterValidationErrorDTO } from '$lib/types';

	const stagingStore = singboxRouter.staging;
	const staging = $derived($stagingStore);
	const hasDraft = $derived(staging?.hasDraft === true);
	const draftedAt = $derived(staging?.draftedAt ? new Date(staging.draftedAt) : null);

	let applying = $state(false);
	let discarding = $state(false);
	let inlineErrors = $state<RouterValidationErrorDTO[] | null>(null);
	let inlineSbCheck = $state<string | null>(null);
	let confirmDiscard = $state(false);

	async function onApply(): Promise<void> {
		if (applying) return;
		applying = true;
		inlineErrors = null;
		inlineSbCheck = null;
		try {
			await api.singboxRouterStagingApply();
			// success: SSE will flip hasDraft to false and the banner disappears
		} catch (e: unknown) {
			const err = e as { status?: number; body?: RouterStagingValidationError };
			if (err.status === 422 && err.body?.validation) {
				inlineErrors = err.body.validation.errors;
			} else if (err.status === 422 && err.body?.sbCheck) {
				inlineSbCheck = stripAnsi(err.body.sbCheck);
			} else {
				inlineSbCheck = String(e);
			}
		} finally {
			applying = false;
		}
	}

	async function onDiscard(): Promise<void> {
		if (discarding) return;
		discarding = true;
		try {
			await api.singboxRouterStagingDiscard();
		} finally {
			discarding = false;
			confirmDiscard = false;
		}
	}

	function formatDrafted(d: Date | null): string {
		if (!d) return '';
		return `с ${formatTime(d.toISOString())}`;
	}

	const hasErrors = $derived(!!(inlineErrors || inlineSbCheck));
</script>

{#if hasDraft}
	<div class="staging-banner" class:has-errors={hasErrors}>
		<div class="staging-row">
			<span class="dot" aria-hidden="true"></span>
			<span class="title">
				{hasErrors ? 'Не могу применить' : 'Несохранённые правки'}
				·&nbsp;<span class="time">{formatDrafted(draftedAt)}</span>
			</span>
			<div class="spacer"></div>
			<Button variant="ghost" size="sm" disabled={applying || discarding} onclick={() => (confirmDiscard = true)}>
				Сбросить
			</Button>
			<Button variant="primary" size="sm" disabled={applying || discarding} onclick={onApply}>
				{applying ? 'Применяю…' : 'Применить'}
			</Button>
		</div>
		{#if inlineErrors}
			<ul class="error-list">
				{#each inlineErrors as e}
					<li><strong>{e.inRule || e.slot}</strong>: {e.message}{#if e.tag} ({e.tag}){/if}</li>
				{/each}
			</ul>
		{/if}
		{#if inlineSbCheck}
			<pre class="sb-check">{inlineSbCheck}</pre>
		{/if}
	</div>
{/if}

<Modal
	open={confirmDiscard}
	title="Откатить правки?"
	size="sm"
	onclose={() => (confirmDiscard = false)}
>
	<p class="discard-body">Все накопленные изменения будут отброшены.</p>
	{#snippet actions()}
		<Button variant="ghost" size="md" onclick={() => (confirmDiscard = false)}>Отмена</Button>
		<Button variant="danger" size="md" disabled={discarding} onclick={onDiscard}>
			{discarding ? 'Откатываю…' : 'Сбросить'}
		</Button>
	{/snippet}
</Modal>

<style>
	.staging-banner {
		display: flex;
		flex-direction: column;
		gap: 0.5rem;
		padding: 0.75rem 1rem;
		margin-bottom: 0.75rem;
		background: color-mix(in srgb, var(--color-warning) 10%, transparent);
		border-left: 3px solid var(--color-warning);
		border-radius: var(--radius-sm);
		font-size: 0.9rem;
	}
	.staging-banner.has-errors {
		background: color-mix(in srgb, var(--color-error) 12%, transparent);
		border-left-color: var(--color-error);
	}
	.staging-row {
		display: flex;
		align-items: center;
		gap: 0.5rem;
	}
	.dot {
		width: 8px;
		height: 8px;
		border-radius: 999px;
		background: var(--color-warning);
		flex-shrink: 0;
	}
	.staging-banner.has-errors .dot {
		background: var(--color-error);
	}
	.title {
		font-weight: 600;
	}
	.time {
		color: var(--color-text-muted);
		font-weight: 400;
	}
	.spacer {
		flex: 1;
	}
	.error-list {
		margin: 0;
		padding-left: 1.5rem;
		font-size: 0.85rem;
	}
	.sb-check {
		margin: 0;
		padding: 0.5rem;
		background: var(--color-bg-tertiary);
		border-radius: 4px;
		font-size: 0.8rem;
		white-space: pre-wrap;
		overflow-x: auto;
	}
	.discard-body {
		margin: 0;
		color: var(--color-text-secondary);
	}
</style>
