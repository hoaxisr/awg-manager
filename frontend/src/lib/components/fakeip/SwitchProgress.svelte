<!--
  Живой экран хода переключения режима маршрутизации (FE-spec §7.3, 1E.6).
  Презентационный: рендерит накопленные шаги из transition-стора (DATA-DRIVEN —
  показываем ровно те milestone'ы, что пришли по SSE, без хардкода полного
  списка). На успех — итог + «Закрыть»; на ошибку — упавший шаг, текст ошибки и
  итоговое состояние отката. Пока переход в полёте, «Закрыть» скрыта, backdrop
  не закрывает. Действие (POST + reset) принадлежит странице.
-->
<script lang="ts">
	import { Check, Loader, X } from 'lucide-svelte';
	import { Modal, Button } from '$lib/components/ui';
	import { humanLabel } from './switchConsequences';
	import type {
		FakeIPTransitionState,
		FakeIPMode,
	} from '$lib/stores/fakeipTransition';
	import type { SingboxRouterTransitionStep } from '$lib/types';

	interface Props {
		open: boolean;
		state: FakeIPTransitionState | null;
		onClose: () => void;
	}

	let { open, state, onClose }: Props = $props();

	// Honest Russian labels per milestone. Only labels for steps actually
	// received are ever rendered (data-driven), so an unmapped step falls back to
	// its raw name rather than fabricating copy.
	const STEP_LABELS: Record<SingboxRouterTransitionStep['step'], string> = {
		start: 'Начало',
		teardown: 'Снятие предыдущего режима',
		provision: 'Создание интерфейса, маршрутов и конфигурации',
		readiness: 'Проверка готовности',
		ready: 'Готово',
		rollback: 'Откат',
		error: 'Ошибка',
	};

	function stepLabel(step: SingboxRouterTransitionStep['step']): string {
		return STEP_LABELS[step] ?? step;
	}

	const steps = $derived(state?.steps ?? []);
	const done = $derived(state?.done ?? false);
	// Success = the engine landed on the mode we asked for. Anything else after a
	// terminal event means a rollback happened.
	const succeeded = $derived(
		done && state != null && state.finalState === state.to,
	);
	const finalLabel = $derived(
		state?.finalState ? humanLabel(state.finalState as FakeIPMode) : '',
	);
	const failedStep = $derived(
		steps.find((s) => s.status === 'error') ?? null,
	);
</script>

<Modal
	{open}
	title="Переключение режима"
	size="md"
	onclose={onClose}
	closeOnBackdrop={false}
>
	<div class="progress">
		<ol class="steps" aria-label="Ход переключения">
			{#each steps as step (step.step)}
				<li class="step" class:is-error={step.status === 'error'}>
					<span class="indicator" aria-hidden="true">
						{#if step.status === 'done'}
							<Check size={16} class="ic-done" />
						{:else if step.status === 'error'}
							<X size={16} class="ic-error" />
						{:else}
							<Loader size={16} class="ic-current spin" />
						{/if}
					</span>
					<span class="step-body">
						<span class="step-label">{stepLabel(step.step)}</span>
						{#if step.message}
							<span class="step-message">{step.message}</span>
						{/if}
					</span>
				</li>
			{/each}
		</ol>

		{#if done && succeeded}
			<p class="summary summary-ok">Режим переключён: {finalLabel}</p>
		{:else if done && !succeeded}
			<div class="summary summary-error">
				{#if state?.error}
					<p class="summary-line">{state.error}</p>
				{/if}
				{#if failedStep}
					<p class="summary-line">
						Упавший шаг: {stepLabel(failedStep.step)}
					</p>
				{/if}
				<p class="summary-line">Откат в {finalLabel}</p>
			</div>
		{/if}
	</div>

	{#snippet actions()}
		{#if done}
			<Button variant="primary" size="md" onclick={onClose}>Закрыть</Button>
		{/if}
	{/snippet}
</Modal>

<style>
	.progress {
		display: flex;
		flex-direction: column;
		gap: 1rem;
	}

	.steps {
		list-style: none;
		margin: 0;
		padding: 0;
		display: flex;
		flex-direction: column;
		gap: 0.625rem;
	}

	.step {
		display: flex;
		align-items: flex-start;
		gap: 0.625rem;
		font-size: 0.875rem;
		line-height: 1.4;
	}

	.indicator {
		flex: 0 0 auto;
		display: inline-flex;
		align-items: center;
		justify-content: center;
		width: 1rem;
		height: 1.25rem;
	}

	.step-body {
		display: flex;
		flex-direction: column;
		gap: 0.125rem;
	}

	.step-label {
		color: var(--text-primary);
	}

	.step.is-error .step-label {
		color: var(--color-error);
	}

	.step-message {
		font-size: 0.8125rem;
		color: var(--text-muted);
	}

	.summary {
		margin: 0;
		font-size: 0.875rem;
		font-weight: 600;
	}

	.summary-ok {
		color: var(--color-success);
	}

	.summary-error {
		display: flex;
		flex-direction: column;
		gap: 0.25rem;
		padding: 0.75rem 0.875rem;
		border: 1px solid var(--color-error-border);
		background: var(--color-error-tint);
		border-radius: var(--radius);
		color: var(--color-error);
	}

	.summary-line {
		margin: 0;
		font-weight: 500;
	}

	:global(.ic-done) {
		color: var(--color-success);
	}

	:global(.ic-error) {
		color: var(--color-error);
	}

	:global(.ic-current) {
		color: var(--accent);
	}

	:global(.spin) {
		animation: spin 0.9s linear infinite;
	}

	@keyframes spin {
		to {
			transform: rotate(360deg);
		}
	}
</style>
