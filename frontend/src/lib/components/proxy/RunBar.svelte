<script lang="ts">
	import { Button, FieldHint, StatusDot } from '$lib/components/ui';
	import { Play, Square, Wand2 } from 'lucide-svelte';
	import type { Snippet } from 'svelte';
	import type { ProxyRunState } from './rows';

	interface Props {
		state: ProxyRunState;
		/** RB-06 (выход) / RB-07 (раздача): порты, аптайм, PID — собирает деталь. */
		meta: string;
		busy?: boolean;
		onstart: () => void;
		onstop: () => void;
		/** RB-08: возврат в мастер. Кнопки нет, пока возвращаться некуда. */
		onwizard?: () => void;
		/** Слот управления рядом с «Запустить/Остановить» — тумблер sing-box у раздачи. */
		aside?: Snippet;
		/**
		 * Непустой текст блокирует «Запустить» и печатается подсказкой рядом:
		 * гейт SH-91 у WDTT-раздачи без рабочих абонентов. Пусто — гейта нет.
		 */
		startBlockedHint?: string;
	}

	let {
		state,
		meta,
		busy = false,
		onstart,
		onstop,
		onwizard,
		aside,
		startBlockedHint = '',
	}: Props = $props();

	// RB-01..RB-03. «Включён, но не работает» вместо прежнего «Не запускается»:
	// то состояние — включённый инстанс без живого процесса, а старая надпись
	// читалась как приговор («никогда не запустится») и не говорила, что делать.
	// Причину показывает LastErrorBox под строкой.
	const label = $derived(
		state === 'running'
			? 'Запущен'
			: state === 'error'
				? 'Включён, но не работает'
				: 'Остановлен',
	);
	const dot = $derived(state === 'running' ? 'success' : state === 'error' ? 'error' : 'muted');
</script>

<div class="run-bar">
	<div class="left">
		<StatusDot variant={dot} pulse={state === 'running'} />
		<span class="label">{label}</span>
		{#if meta}<span class="meta">{meta}</span>{/if}
	</div>
	<div class="actions">
		{#if aside}{@render aside()}{/if}
		{#if onwizard}
			<Button variant="ghost" onclick={onwizard}>
				{#snippet iconBefore()}<Wand2 size={14} strokeWidth={2.5} />{/snippet}
				Мастер
			</Button>
		{/if}
		<Button
			variant="success"
			disabled={state === 'running' || busy || !!startBlockedHint}
			loading={busy && state !== 'running'}
			onclick={onstart}
		>
			{#snippet iconBefore()}<Play size={14} strokeWidth={2.5} />{/snippet}
			Запустить
		</Button>
		{#if startBlockedHint}
			<FieldHint text={startBlockedHint} ariaLabel="Подсказка: запуск заблокирован" />
		{/if}
		<!-- «Остановить» доступна и при упавшем процессе: иначе не снять enabled (WC-32). -->
		<Button variant="secondary" disabled={busy} onclick={onstop}>
			{#snippet iconBefore()}<Square size={14} strokeWidth={2.5} />{/snippet}
			Остановить
		</Button>
	</div>
</div>

<style>
	.run-bar {
		display: flex;
		align-items: center;
		justify-content: space-between;
		gap: 0.75rem;
		flex-wrap: wrap;
	}

	.left {
		display: flex;
		align-items: center;
		gap: 0.5rem;
		flex-wrap: wrap;
		min-width: 0;
	}

	/* Ширина по содержимому: с `width: auto` короткая подпись занимала всю
	   строку группы, и состояние разъезжалось на три строки вместо одной. */
	.label {
		width: max-content;
		max-width: 100%;
		font-size: 0.9375rem;
		font-weight: 600;
		color: var(--color-text-primary);
	}

	.meta {
		width: max-content;
		max-width: 100%;
		font-size: 0.8125rem;
		color: var(--color-text-muted);
		font-family: var(--font-mono);
	}

	.actions {
		display: flex;
		align-items: center;
		gap: 0.5rem;
		flex-wrap: wrap;
	}
</style>
