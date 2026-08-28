<script lang="ts">
	// Шаговая обвязка мастеров «Прокси»: номера с подписями, содержимое шага и
	// навигация «Назад»/«Дальше» (WE-27/28). Вперёд ведёт только «Дальше» —
	// иначе шаг можно перешагнуть незаполненным.
	import type { Snippet } from 'svelte';
	import { Button } from '$lib/components/ui';
	import { Check } from 'lucide-svelte';

	interface Props {
		steps: string[];
		/** Индекс текущего шага, с нуля. */
		current: number;
		/** Условие перехода вперёд — готовность текущего шага. */
		canNext?: boolean;
		ongo: (i: number) => void;
		children: Snippet;
		/** Действия последнего шага вместо «Дальше». */
		finish?: Snippet;
	}

	let { steps, current, canNext = true, ongo, children, finish }: Props = $props();

	const last = $derived(current >= steps.length - 1);
</script>

<ol class="steps">
	{#each steps as label, i (label)}
		<li class="step" class:active={i === current} class:done={i < current}>
			<button type="button" class="step-btn" disabled={i > current} onclick={() => ongo(i)}>
				<span class="num">
					{#if i < current}
						<Check size={13} strokeWidth={3} />
					{:else}
						{i + 1}
					{/if}
				</span>
				<span class="label">{label}</span>
			</button>
		</li>
	{/each}
</ol>

<div class="body">{@render children()}</div>

<div class="foot">
	{#if current > 0}
		<Button variant="ghost" onclick={() => ongo(current - 1)}>Назад</Button>
	{/if}
	{#if last}
		{#if finish}{@render finish()}{/if}
	{:else}
		<Button variant="primary" disabled={!canNext} onclick={() => ongo(current + 1)}>Дальше</Button>
	{/if}
</div>

<style>
	.steps {
		display: flex;
		align-items: center;
		gap: 0.25rem;
		list-style: none;
		margin: 0 0 1rem;
		padding: 0;
		flex-wrap: wrap;
	}

	.step-btn {
		display: flex;
		align-items: center;
		gap: 0.4rem;
		padding: 0.375rem 0.625rem;
		background: none;
		border: none;
		border-radius: 999px;
		cursor: pointer;
		color: var(--color-text-muted);
		font-size: 0.8125rem;
	}

	.step-btn:disabled {
		cursor: default;
	}

	.step-btn:not(:disabled):hover {
		background: var(--color-bg-hover);
	}

	.num {
		display: inline-flex;
		align-items: center;
		justify-content: center;
		width: 20px;
		height: 20px;
		border-radius: 50%;
		border: 1px solid var(--color-border);
		font-size: 0.6875rem;
		font-weight: 600;
		flex-shrink: 0;
	}

	.step.active .step-btn {
		color: var(--color-text-primary);
		font-weight: 600;
	}

	.step.active .num {
		background: var(--color-accent);
		border-color: var(--color-accent);
		color: #fff;
	}

	.step.done .num {
		border-color: var(--color-success);
		color: var(--color-success);
	}

	.label {
		white-space: nowrap;
	}

	.foot {
		display: flex;
		justify-content: flex-end;
		gap: 0.5rem;
		margin-top: 1.25rem;
		padding-top: 1rem;
		border-top: 1px solid var(--color-border);
		flex-wrap: wrap;
	}
</style>
