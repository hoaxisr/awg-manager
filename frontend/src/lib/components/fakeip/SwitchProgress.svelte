<!--
  Экран хода переключения режима. Просто: показываем ВСЕ шаги сразу, ставим
  галочку когда шаг выполнен (состояние из transition-стора через deriveSteps).
-->
<script lang="ts">
	import { Check, X, Loader, Circle } from 'lucide-svelte';
	import { Modal, Button } from '$lib/components/ui';
	import { humanLabel } from './switchConsequences';
	import { deriveSteps } from './switchSteps';
	import type { FakeIPTransitionState, FakeIPMode } from '$lib/stores/fakeipTransition';

	interface Props {
		open: boolean;
		state: FakeIPTransitionState | null;
		onClose: () => void;
	}

	let { open, state, onClose }: Props = $props();

	const done = $derived(state?.done ?? false);
	const succeeded = $derived(done && state != null && state.finalState === state.to);
	const failed = $derived(done && !succeeded);

	const fromMode = $derived((state?.from ?? 'tproxy') as FakeIPMode);
	const toMode = $derived((state?.to ?? 'fakeip-tun') as FakeIPMode);
	const title = $derived(`Переключение: ${humanLabel(fromMode)} → ${humanLabel(toMode)}`);

	const rows = $derived(deriveSteps(fromMode, toMode, state?.steps ?? [], { failed }));
	const finalLabel = $derived(humanLabel((state?.finalState as FakeIPMode) ?? fromMode));
	const failedRow = $derived(rows.find((r) => r.state === 'error') ?? null);
</script>

<Modal {open} {title} size="md" onclose={onClose} closeOnBackdrop={false} bodyMinHeight="22rem">
	<ul class="steps">
		{#each rows as row (row.title)}
			<li class="step state-{row.state}">
				<span class="mark">
					{#if row.state === 'done'}<Check size={18} strokeWidth={2.5} />
					{:else if row.state === 'error'}<X size={18} strokeWidth={2.5} />
					{:else if row.state === 'current'}<Loader size={18} class="spin" />
					{:else}<Circle size={16} />{/if}
				</span>
				<span class="text">
					<span class="title">{row.title}</span>
					{#if row.detail}<span class="detail">{row.detail}</span>{/if}
				</span>
			</li>
		{/each}
	</ul>

	{#if done && succeeded}
		<p class="result ok">✓ Режим «{finalLabel}» активен.</p>
	{:else if failed}
		<p class="result err">
			✕ Откат в «{finalLabel}».{#if failedRow}&nbsp;Упавший шаг: {failedRow.title}.{/if}{#if state?.error}&nbsp;{state.error}{/if}
		</p>
	{/if}

	{#snippet actions()}
		{#if done}
			<Button variant="primary" size="md" onclick={onClose}>Закрыть</Button>
		{/if}
	{/snippet}
</Modal>

<style>
	.steps {
		list-style: none;
		margin: 0;
		padding: 0;
	}

	.step {
		display: flex;
		align-items: flex-start;
		gap: 0.625rem;
		padding: 0.625rem 0;
		border-bottom: 1px solid var(--border);
	}

	.step:last-child {
		border-bottom: none;
	}

	.mark {
		flex: 0 0 1.375rem;
		display: inline-flex;
		align-items: center;
		justify-content: center;
		height: 1.4rem;
		color: var(--text-muted);
	}

	.step.state-done .mark {
		color: var(--color-success);
	}
	.step.state-current .mark {
		color: var(--color-accent, var(--accent));
	}
	.step.state-error .mark {
		color: var(--color-error);
	}

	.text {
		display: flex;
		flex-direction: column;
		gap: 0.125rem;
	}

	.title {
		font-size: 0.9375rem;
		line-height: 1.4;
		color: var(--text-primary);
	}

	.step.state-pending .title {
		color: var(--text-muted);
	}

	.detail {
		font-size: 0.8125rem;
		color: var(--text-muted);
	}

	.result {
		margin: 1rem 0 0;
		padding: 0.625rem 0.875rem;
		border-radius: var(--radius-sm);
		font-size: 0.9375rem;
		line-height: 1.4;
	}

	.result.ok {
		background: var(--color-success-tint);
		border: 1px solid var(--color-success-border);
		color: var(--color-success);
	}

	.result.err {
		background: var(--color-error-tint);
		border: 1px solid var(--color-error-border);
		color: var(--color-error);
	}

	/* lucide-svelte forwards `class` onto the <svg>; spin the current-step ring. */
	:global(.spin) {
		animation: spin 0.9s linear infinite;
	}

	@keyframes spin {
		to {
			transform: rotate(360deg);
		}
	}
</style>
