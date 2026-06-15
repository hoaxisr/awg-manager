<!--
  Живой экран хода переключения режима маршрутизации (FE-spec §7.3, 1E.6).
  Презентационный: рендерит ПРЕДОПРЕДЕЛЁННЫЙ пошаговый список (мокап
  page-transition.html, часть 2) — кружок-индикатор + заголовок + деталь. Состояние
  каждой строки (done/current/pending/error) ВЫВОДИТСЯ из накопленных в transition-
  сторе coarse-milestone'ов (см. switchSteps.deriveSteps) — прогресс не выдумывается.
  На успех — зелёный итог-бокс + «Закрыть»; на ошибку — упавший шаг, текст ошибки и
  откат. Пока переход в полёте, «Закрыть» скрыта, backdrop не закрывает. Действие
  (POST + reset) принадлежит странице.
-->
<script lang="ts">
	import { Check, Loader, X } from 'lucide-svelte';
	import { Modal, Button } from '$lib/components/ui';
	import { humanLabel } from './switchConsequences';
	import { deriveSteps } from './switchSteps';
	import type {
		FakeIPTransitionState,
		FakeIPMode,
	} from '$lib/stores/fakeipTransition';

	interface Props {
		open: boolean;
		state: FakeIPTransitionState | null;
		onClose: () => void;
	}

	let { open, state, onClose }: Props = $props();

	const done = $derived(state?.done ?? false);
	// Success = the engine landed on the mode we asked for. Anything else after a
	// terminal event means a rollback happened.
	const succeeded = $derived(
		done && state != null && state.finalState === state.to,
	);
	const failed = $derived(done && !succeeded);

	const fromMode = $derived((state?.from ?? 'tproxy') as FakeIPMode);
	const toMode = $derived((state?.to ?? 'fakeip-tun') as FakeIPMode);

	const title = $derived(
		`Переключение: ${humanLabel(fromMode)} → ${humanLabel(toMode)}`,
	);

	// Predefined rows with their derived state (done/current/pending/error).
	const rows = $derived(
		deriveSteps(fromMode, toMode, state?.steps ?? [], { failed }),
	);

	// On success the engine settled on `to`; on failure it rolled back to
	// `finalState` (or, lacking one, the original `from`).
	const finalLabel = $derived(
		humanLabel((state?.finalState as FakeIPMode) ?? fromMode),
	);
	const failedRow = $derived(rows.find((r) => r.state === 'error') ?? null);
</script>

<Modal
	{open}
	{title}
	size="md"
	onclose={onClose}
	closeOnBackdrop={false}
	bodyMinHeight="min(24rem, 70vh)"
>
	<div class="progress">
		<ol class="steps" aria-label="Ход переключения">
			{#each rows as row, i (i)}
				<li class="step state-{row.state}">
					<span
						class="indicator ind-{row.state}"
						aria-hidden="true"
					>
						{#if row.state === 'done'}
							<Check size={13} strokeWidth={2.5} />
						{:else if row.state === 'error'}
							<X size={13} strokeWidth={2.5} />
						{:else if row.state === 'current'}
							<Loader size={13} strokeWidth={2.5} class="spin" />
						{:else}
							<span class="num">{i + 1}</span>
						{/if}
					</span>
					<span class="step-body">
						<span class="step-title">{row.title}</span>
						{#if row.detail}
							<span class="step-detail">{row.detail}</span>
						{/if}
					</span>
				</li>
			{/each}
		</ol>

		{#if !done}
			<p class="note">
				При ошибке любого шага — fail-closed: best-effort откат в предыдущий
				режим, движок не остаётся в полусобранном состоянии. Финальный шаг —
				mode-aware readiness.
			</p>
		{/if}

		{#if done && succeeded}
			<div class="summary summary-ok">
				<span class="summary-ic" aria-hidden="true"><Check size={15} strokeWidth={2.5} /></span>
				<span>Режим «{finalLabel}» активен.</span>
			</div>
		{:else if failed}
			<div class="summary summary-error">
				{#if failedRow}
					<p class="summary-line strong">Упавший шаг: {failedRow.title}</p>
				{/if}
				{#if state?.error}
					<p class="summary-line">{state.error}</p>
				{/if}
				<p class="summary-line">Откат в «{finalLabel}».</p>
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
	/* The modal gets an explicit `bodyMinHeight` (Modal prop) so its body can't
	   collapse to a thin strip when the step list streams in after open. */
	.progress {
		display: block;
	}

	.progress > :global(* + *) {
		margin-top: 0.875rem;
	}

	.steps {
		list-style: none;
		margin: 0;
		padding: 0;
		display: block;
	}

	.step {
		display: flex;
		align-items: flex-start;
		gap: 0.6875rem;
		padding: 0.5625rem 0.25rem;
		border-bottom: 1px solid var(--border);
		flex-shrink: 0;
	}

	.step:last-child {
		border-bottom: none;
	}

	/* Circular indicator — fixed size so the row never collapses to a thin bar. */
	.indicator {
		flex: 0 0 auto;
		width: 1.375rem;
		height: 1.375rem;
		border-radius: 50%;
		display: inline-flex;
		align-items: center;
		justify-content: center;
		line-height: 1;
	}

	.ind-done {
		background: var(--color-success-tint);
		color: var(--color-success);
	}

	.ind-current {
		background: var(--color-accent-tint);
		color: var(--accent);
	}

	.ind-error {
		background: var(--color-error-tint);
		color: var(--color-error);
	}

	.ind-pending {
		background: var(--bg-secondary, transparent);
		border: 1px solid var(--border);
		color: var(--text-muted);
	}

	.num {
		font-size: 0.75rem;
		font-weight: 600;
	}

	.step-body {
		display: flex;
		flex-direction: column;
		gap: 0.125rem;
		min-width: 0;
		padding-top: 0.0625rem;
	}

	.step-title {
		font-size: 0.875rem;
		line-height: 1.35;
		color: var(--text-primary);
	}

	.step.state-pending .step-title {
		color: var(--text-muted);
	}

	.step.state-current .step-title {
		color: var(--text-primary);
		font-weight: 600;
	}

	.step.state-error .step-title {
		color: var(--color-error);
		font-weight: 600;
	}

	.step-detail {
		font-size: 0.8125rem;
		line-height: 1.35;
		color: var(--text-muted);
	}

	.note {
		margin: 0;
		font-size: 0.8125rem;
		line-height: 1.5;
		color: var(--text-muted);
	}

	.summary {
		margin: 0;
		display: flex;
		gap: 0.5rem;
		align-items: center;
		padding: 0.6875rem 0.8125rem;
		border-radius: var(--radius-sm);
		font-size: 0.875rem;
		line-height: 1.4;
	}

	.summary-ok {
		border: 1px solid var(--color-success-border);
		background: var(--color-success-tint);
		color: var(--color-success);
	}

	.summary-ic {
		flex: 0 0 auto;
		display: inline-flex;
	}

	.summary-error {
		display: flex;
		flex-direction: column;
		align-items: stretch;
		gap: 0.25rem;
		border: 1px solid var(--color-error-border);
		background: var(--color-error-tint);
		color: var(--color-error);
	}

	.summary-line {
		margin: 0;
		font-weight: 500;
	}

	.summary-line.strong {
		font-weight: 700;
	}

	/* lucide-svelte forwards `class` onto the <svg>; we spin the current-step ring. */
	:global(.spin) {
		animation: spin 0.9s linear infinite;
	}

	@keyframes spin {
		to {
			transform: rotate(360deg);
		}
	}
</style>
