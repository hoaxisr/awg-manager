<script lang="ts">
	// Шаговая обвязка мастеров: номера с подписями, содержимое шага и
	// навигация «Назад»/«Дальше» (WE-27/28). Вперёд ведёт только «Дальше» —
	// иначе шаг можно перешагнуть незаполненным. Единственный примитив
	// пошаговых мастеров (F855): «Прокси», «Внешний доступ», Telegram-мастер.
	import { m } from '$lib/i18n';
	import type { Snippet } from 'svelte';
	import Button from './Button.svelte';
	import { Check } from 'lucide-svelte';

	interface Props {
		steps: string[];
		/** Индекс текущего шага, с нуля. */
		current: number;
		/** Условие перехода вперёд — готовность текущего шага. */
		canNext?: boolean;
		/**
		 * Можно ли вернуться к пройденным шагам (кнопка «Назад» и клик по
		 * номеру). Мастер с необратимым применением гасит её на время и после него.
		 */
		canBack?: boolean;
		/** Подпись «Дальше», когда шаг ведёт не просто вперёд (например, «Применить»). */
		nextLabel?: string;
		/** Идёт асинхронный переход: «Дальше» крутит спиннер, навигация заблокирована. */
		busy?: boolean;
		/** Вызывается с индексом шага; переход вперёд — всегда current + 1. */
		ongo: (i: number) => void;
		children: Snippet;
		/** Действия последнего шага вместо «Дальше». */
		finish?: Snippet;
	}

	let {
		steps,
		current,
		canNext = true,
		canBack = true,
		nextLabel,
		busy = false,
		ongo,
		children,
		finish,
	}: Props = $props();

	const last = $derived(current >= steps.length - 1);
	const showBack = $derived(current > 0 && canBack);
</script>

<ol class="steps">
	{#each steps as label, i (label)}
		<li class="step" class:active={i === current} class:done={i < current}>
			<button
				type="button"
				class="step-btn"
				disabled={i > current || (i < current && (!canBack || busy))}
				onclick={() => ongo(i)}
			>
				<span class="num">
					{#if i < current}
						<Check size={13} strokeWidth={3} />
					{:else}
						{i + 1}
					{/if}
				</span>
				<span class="wizard-label">{label}</span>
			</button>
		</li>
	{/each}
</ol>

<div class="body">{@render children()}</div>

{#if showBack || !last || finish}
	<div class="foot">
		{#if showBack}
			<Button variant="ghost" disabled={busy} onclick={() => ongo(current - 1)}>{m.common_back()}</Button>
		{/if}
		{#if last}
			{#if finish}{@render finish()}{/if}
		{:else}
			<Button variant="primary" disabled={!canNext} loading={busy} onclick={() => ongo(current + 1)}>
				{nextLabel ?? m.proxy_wizard_next()}
			</Button>
		{/if}
	</div>
{/if}

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

	.wizard-label {
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
